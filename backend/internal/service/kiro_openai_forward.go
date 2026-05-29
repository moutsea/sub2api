package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ForwardChatCompletions handles OpenAI Chat Completions requests via Kiro pipeline.
// It converts OpenAI format to Claude, forwards through the existing Kiro upstream,
// and converts the response back to OpenAI format.
// This enables OpenAI-compatible clients (e.g. Cursor) to use Kiro accounts.
func (s *KiroGatewayService) ForwardChatCompletions(ctx context.Context, c *gin.Context, account *Account, body []byte) (*OpenAIForwardResult, error) {
	startTime := time.Now()
	prefix := fmt.Sprintf("[kiro-OpenAI] account=%s", account.Name)

	// 1. Convert OpenAI request to Claude format
	claudeReq, err := kiro.ConvertOpenAIToClaude(body)
	if err != nil {
		return nil, fmt.Errorf("convert openai to claude: %w", err)
	}

	// Ensure stream is set (Kiro always uses streaming internally)
	wantStream := claudeReq.Stream
	if !account.IsKiroApiKey() && kiro.ApplyThinkingDefaultsFromModelName(claudeReq) {
		log.Printf("%s enabled thinking mode from model alias: %s", prefix, claudeReq.Model)
	}

	freeTier := s.isKiroFreeTier(account)

	// Free 订阅类型账号不支持 Opus，自动降级为 Sonnet 4.5
	if remapped, ok := s.remapModelForFreeTier(account, claudeReq.Model, freeTier); ok {
		log.Printf("%s free_tier_model_remap: %s -> %s", prefix, claudeReq.Model, remapped)
		claudeReq.Model = remapped
	}

	originalModel := claudeReq.Model
	if account.IsKiroApiKey() {
		mappedModel := account.GetMappedModel(originalModel)
		if mappedModel != originalModel {
			claudeReq.Model = mappedModel
			log.Printf("%s apikey_model_mapping: %s -> %s", prefix, originalModel, mappedModel)
		}
		if isKiroOAuthOnlyModel(claudeReq.Model) {
			return nil, fmt.Errorf("model %s is only supported for Kiro OAuth accounts", claudeReq.Model)
		}
	}
	activeUpstreamModel := s.resolveKiroUpstreamModel(account, claudeReq.Model)
	mappedModel := kiro.GetModelID(activeUpstreamModel)

	// Force max_tokens to 64000.
	// AWSQ thinking mode shares the output token budget between thinking and text.
	// A too-small max_tokens causes thinking to exhaust the budget with no room for text output.
	// Fixed at 64000 to ensure consistent behavior across all clients.
	claudeReq.MaxTokens = 64000

	// 2. Cache estimation
	cacheEstimation := kiro.EstimateCache(claudeReq)
	var cacheResult kiro.CacheResult
	if cacheEstimation.MeetsCacheThreshold {
		// Use stable conversation ID (IP + UA + API key) as cache key.
		// CacheTracker tracks per-client cache state to predict upstream prompt cache hits,
		// so it needs a client-session identifier, not a content fingerprint.
		cacheKey := kiro.GenerateStableConversationID(c)
		cacheResult = kiro.GlobalCacheTracker.CheckAndMark(cacheKey, cacheEstimation.CacheableTokens)
	}

	// 3. Get access token
	if s.tokenProvider == nil {
		return nil, errors.New("kiro token provider not configured")
	}
	accessToken, err := s.tokenProvider.GetAccessToken(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("get access_token failed: %w", err)
	}

	// Proxy URL (Free-tier: random from pool; others: account-bound)
	proxyURL := s.resolveProxyURL(ctx, account, freeTier)

	// 5. Resolve URL images to base64 (CW only supports base64)
	if kiro.ResolveURLImagesInRequest(claudeReq) {
		log.Printf("%s URL images resolved to base64", prefix)
	}

	// 5b. Compress oversized images before CW transformation
	// Skip for 1M context models (4.6 series) which have 4MB body limit.
	if !kiro.Is1MContext(claudeReq.Model) {
		if kiro.CompressImagesInRequest(claudeReq) {
			log.Printf("%s images compressed for body size reduction", prefix)
		}
	}

	// 6. Transform to CodeWhisperer format
	// Priority: in-memory cache (synchronous with token refresh) > account snapshot > database
	profileArn, err := resolveKiroProfileArn(ctx, account, s.tokenProvider, s.accountRepo, prefix)
	if err != nil {
		return nil, err
	}
	_, reqBody, err := s.prepareCodeWhispererPayload(claudeReq, profileArn, c, activeUpstreamModel)
	if err != nil {
		return nil, fmt.Errorf("prepare request: %w", err)
	}

	log.Printf("%s request_size=%d model=%s mapped_model=%s", prefix, len(reqBody), originalModel, mappedModel)
	if activeUpstreamModel != originalModel {
		log.Printf("%s dynamic_model_fallback: %s -> %s", prefix, originalModel, activeUpstreamModel)
	}

	// 7. Endpoint loop (reuse existing pattern)
	endpoints := getKiroEndpoints(account, s.cfg)
	machineID := s.resolveMachineID(account, freeTier)
	kiroVersion := "0.11.107"

	var resp *http.Response
	var lastErr error
	modelFallbackAttempted := activeUpstreamModel != originalModel
	tokenRefreshed := false

	prepareSlowModelFallback := func(timeout time.Duration) error {
		if modelFallbackAttempted || !shouldFallbackSlowKiroOpus(account, originalModel, activeUpstreamModel) {
			return nil
		}
		log.Printf("%s status=all_endpoints_initial_response_timeout requested_model=%s effective_model=%s timeout=%s fallback=%s",
			prefix, originalModel, activeUpstreamModel, timeout, kiroDynamicFallbackModelOpus46)
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:    account.Platform,
			AccountID:   account.ID,
			AccountName: account.Name,
			Kind:        "model_fallback",
			Message:     fmt.Sprintf("%s initial response timeout on all endpoints; retrying with %s", activeUpstreamModel, kiroDynamicFallbackModelOpus46),
		})
		activeUpstreamModel = kiroDynamicFallbackModelOpus46
		mappedModel = kiro.GetModelID(activeUpstreamModel)
		var err error
		_, reqBody, err = s.prepareCodeWhispererPayload(claudeReq, profileArn, c, activeUpstreamModel)
		if err != nil {
			return fmt.Errorf("prepare slow fallback request: %w", err)
		}
		modelFallbackAttempted = true
		log.Printf("%s slow_model_fallback_retry requested_model=%s effective_model=%s request_size=%d mapped_model=%s",
			prefix, originalModel, activeUpstreamModel, len(reqBody), mappedModel)
		return nil
	}

modelAttemptLoop:
	for {
		resp = nil
		lastErr = nil
		initialTimeoutEndpoints := 0
		attemptedEndpoints := 0

		for epIdx, ep := range endpoints {
			attemptedEndpoints++
			attempt := 1
			for attempt <= kiroMaxRetries {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				default:
				}

				upstreamReq, err := http.NewRequestWithContext(ctx, "POST", ep.URL, bytes.NewReader(reqBody))
				if err != nil {
					return nil, fmt.Errorf("create request: %w", err)
				}

				upstreamReq.Header.Set("Content-Type", "application/json")
				upstreamReq.Header.Set("Authorization", "Bearer "+accessToken)
				upstreamReq.Header.Set("Accept", "text/event-stream")
				upstreamReq.Header.Set("User-Agent", fmt.Sprintf("aws-sdk-js/1.0.27 ua/2.1 os/linux lang/js md/nodejs#22.12.0 api/codewhispererstreaming#1.0.27 m/E KiroIDE-%s-%s", kiroVersion, machineID))
				upstreamReq.Header.Set("x-amz-user-agent", fmt.Sprintf("aws-sdk-js/1.0.27 KiroIDE-%s-%s", kiroVersion, machineID))
				upstreamReq.Header.Set("x-amzn-kiro-agent-mode", "vibe")
				upstreamReq.Header.Set("x-amzn-codewhisperer-optout", "true")
				upstreamReq.Header.Set("Host", ep.Host)
				upstreamReq.Header.Set("Connection", "close")
				upstreamReq.Header.Set("amz-sdk-invocation-id", uuid.New().String())
				upstreamReq.Header.Set("amz-sdk-request", "attempt=1; max=3")
				if ep.AmzTarget != "" {
					upstreamReq.Header.Set("X-Amz-Target", ep.AmzTarget)
				}

				// Apply request jitter before upstream call to prevent thundering herd
				s.applyRequestJitter(ctx)

				initialTimeout := kiroOpusSlowFallbackInitialResponseTimeout(account, originalModel, activeUpstreamModel)
				var initialTimedOut bool
				resp, err, initialTimedOut = s.doUpstreamWithInitialTimeout(ctx, upstreamReq, proxyURL, account.ID, account.Concurrency, initialTimeout)
				if initialTimedOut {
					initialTimeoutEndpoints++
					lastErr = newKiroOpusSlowFallbackInitialResponseTimeoutError("response_headers", initialTimeout)
					log.Printf("%s endpoint=%s status=initial_response_timeout model=%s timeout=%s",
						prefix, ep.Name, activeUpstreamModel, initialTimeout)
					appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
						Platform:    account.Platform,
						AccountID:   account.ID,
						AccountName: account.Name,
						Kind:        "model_fallback_candidate",
						Message:     fmt.Sprintf("%s initial response timeout on endpoint %s", activeUpstreamModel, ep.Name),
					})
					break
				}
				if err != nil {
					if attempt < kiroMaxRetries {
						log.Printf("%s endpoint=%s status=request_failed retry=%d/%d error=%v", prefix, ep.Name, attempt, kiroMaxRetries, err)
						if !sleepKiroBackoffWithContext(ctx, attempt) {
							return nil, ctx.Err()
						}
						attempt++
						continue
					}
					lastErr = fmt.Errorf("endpoint %s: %w", ep.Name, err)
					break
				}

				if resp.StatusCode == http.StatusTooManyRequests {
					respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
					_ = resp.Body.Close()
					s.handleUpstreamError(ctx, prefix, account, resp.StatusCode, resp.Header, respBody)
					lastErr = &UpstreamFailoverError{StatusCode: resp.StatusCode}
					break
				}

				if resp.StatusCode == http.StatusUnauthorized {
					respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
					_ = resp.Body.Close()

					if tokenRefreshed || s.tokenProvider == nil {
						lastErr = &UpstreamFailoverError{StatusCode: resp.StatusCode}
						break
					}

					s.tokenProvider.InvalidateToken(account.ID)
					newToken, refreshErr := s.tokenProvider.GetAccessToken(ctx, account)
					if refreshErr != nil {
						lastErr = &UpstreamFailoverError{StatusCode: resp.StatusCode}
						break
					}
					_ = respBody
					accessToken = newToken
					tokenRefreshed = true
					continue
				}

				if resp.StatusCode == 529 {
					respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
					_ = resp.Body.Close()
					s.handleUpstreamError(ctx, prefix, account, resp.StatusCode, resp.Header, respBody)
					lastErr = &UpstreamFailoverError{StatusCode: resp.StatusCode}
					break
				}

				if resp.StatusCode == http.StatusBadRequest {
					respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
					_ = resp.Body.Close()
					s.handleUpstreamError(ctx, prefix, account, resp.StatusCode, resp.Header, respBody)

					// "profileArn is required" is endpoint-specific — failover to next endpoint
					rawErrorMsg := extractKiroErrorMessage(respBody)
					if fallbackModel, ok := s.maybeFallbackUnsupportedKiroModel(account, originalModel, activeUpstreamModel, rawErrorMsg); ok {
						log.Printf("%s endpoint=%s dynamic_model_fallback_retry: %s -> %s", prefix, ep.Name, activeUpstreamModel, fallbackModel)
						activeUpstreamModel = fallbackModel
						mappedModel = kiro.GetModelID(activeUpstreamModel)
						_, reqBody, err = s.prepareCodeWhispererPayload(claudeReq, profileArn, c, activeUpstreamModel)
						if err != nil {
							return nil, fmt.Errorf("prepare fallback request: %w", err)
						}
						modelFallbackAttempted = true
						log.Printf("%s endpoint=%s request_rebuilt request_size=%d model=%s mapped_model=%s", prefix, ep.Name, len(reqBody), originalModel, mappedModel)
						continue
					}

					errorMsg := strings.ToLower(rawErrorMsg)
					if strings.Contains(errorMsg, "profilearn is required") || strings.Contains(errorMsg, "profilearn") {
						log.Printf("%s endpoint=%s status=400 profileArn_required, trying next endpoint", prefix, ep.Name)
						lastErr = &UpstreamFailoverError{StatusCode: resp.StatusCode}
						break
					}

					return nil, s.writeOpenAIError(c, http.StatusBadRequest, "invalid_request_error",
						"Bad request: "+extractKiroErrorMessage(respBody))
				}

				if resp.StatusCode >= 400 && s.shouldRetryUpstreamError(resp.StatusCode) {
					respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
					_ = resp.Body.Close()
					if attempt < kiroMaxRetries {
						log.Printf("%s endpoint=%s status=%d retrying %d/%d", prefix, ep.Name, resp.StatusCode, attempt, kiroMaxRetries)
						if !sleepKiroBackoffWithContext(ctx, attempt) {
							return nil, ctx.Err()
						}
						attempt++
						continue
					}
					_ = respBody
					lastErr = &UpstreamFailoverError{StatusCode: resp.StatusCode}
					break
				}

				goto endpointDone
			}

			resp = nil
			if epIdx < len(endpoints)-1 {
				log.Printf("%s switching from endpoint %s to %s", prefix, ep.Name, endpoints[epIdx+1].Name)
			}
		}

		if resp == nil {
			if initialTimeoutEndpoints > 0 && initialTimeoutEndpoints == attemptedEndpoints {
				if err := prepareSlowModelFallback(kiroOpusSlowFallbackInitialResponseTimeout(account, originalModel, activeUpstreamModel)); err != nil {
					return nil, err
				}
				if modelFallbackAttempted && activeUpstreamModel == kiroDynamicFallbackModelOpus46 {
					continue modelAttemptLoop
				}
			}
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, s.writeOpenAIError(c, http.StatusBadGateway, "upstream_error", "All endpoints failed")
		}

	endpointDone:
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode >= 400 {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			s.handleUpstreamError(ctx, prefix, account, resp.StatusCode, resp.Header, respBody)
			if s.shouldFailoverUpstreamError(resp.StatusCode) {
				return nil, &UpstreamFailoverError{StatusCode: resp.StatusCode}
			}
			return nil, s.writeOpenAIError(c, http.StatusBadGateway, "upstream_error", "Upstream request failed")
		}
		s.markKiroModelSupported(account, originalModel, activeUpstreamModel)

		// 8. Process response
		inputTokens := kiro.EstimateInputTokens(claudeReq)
		toolNameReverseMap := kiro.BuildReverseMapFromClaudeTools(claudeReq.Tools)

		// Calculate cache tokens (cache_read + cache_creation coexist)
		// CW path: cap to model-specific context window
		cacheReadTokens, cacheCreationTokens := cacheEstimation.SplitCacheTokens(cacheResult, kiro.GetContextWindowLimit(activeUpstreamModel))
		thinkingEnabled := kiro.IsThinkingConfigEnabled(claudeReq)

		var usage *OpenAIUsage
		var firstTokenMs *int

		if wantStream {
			result, err := s.handleOpenAIStreamingResponse(c, resp, startTime, originalModel, inputTokens, toolNameReverseMap, cacheCreationTokens, cacheReadTokens, thinkingEnabled)
			if err != nil {
				return nil, err
			}
			usage = result.usage
			firstTokenMs = result.firstTokenMs
		} else {
			result, err := s.handleOpenAINonStreamingResponse(c, resp, originalModel, inputTokens, toolNameReverseMap, cacheCreationTokens, cacheReadTokens, thinkingEnabled)
			if err != nil {
				return nil, err
			}
			usage = result.usage
		}

		if activeUpstreamModel != originalModel {
			log.Printf("%s model_effective requested_model=%s effective_model=%s", prefix, originalModel, activeUpstreamModel)
		}
		return &OpenAIForwardResult{
			RequestID:    resp.Header.Get("x-amzn-requestid"),
			Usage:        *usage,
			Model:        activeUpstreamModel,
			Stream:       wantStream,
			Duration:     time.Since(startTime),
			FirstTokenMs: firstTokenMs,
		}, nil
	}
}
