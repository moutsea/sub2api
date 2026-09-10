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

	// Preserve the downstream response mode; Kiro still streams internally.
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
		return s.forwardKiroAPIKeyChatCompletions(ctx, c, account, claudeReq, originalModel, startTime)
	}
	if injectKiroOAuthIdentitySystemPrompt(account, claudeReq) {
		log.Printf("%s injected Kiro OAuth identity system prompt", prefix)
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
	if cacheEstimation.MeetsCacheThreshold && !account.IsKiroApiKey() {
		// Use stable conversation ID (IP + UA + API key) as cache key.
		// CacheTracker tracks per-client cache state to predict upstream prompt cache hits,
		// so it needs a client-session identifier, not a content fingerprint.
		cacheResult = s.beginKiroOAuthCache(c, account, activeUpstreamModel, claudeReq, cacheEstimation)
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
	var respDeadline *kiroInitialDeadline
	var lastErr error
	tokenRefreshed := false

	{
		resp = nil
		lastErr = nil

		for epIdx, ep := range endpoints {
			attempt := 1
			for attempt <= kiroMaxRetries {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				default:
				}

				s.applyRequestJitter(ctx)

				reqCtx, deadline := s.startKiroInitialDeadline(ctx, account)
				upstreamReq, err := http.NewRequestWithContext(reqCtx, "POST", ep.URL, bytes.NewReader(reqBody))
				if err != nil {
					deadline.close()
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
				s.applyKiroConnectionHeader(upstreamReq)
				upstreamReq.Header.Set("amz-sdk-invocation-id", uuid.New().String())
				upstreamReq.Header.Set("amz-sdk-request", "attempt=1; max=3")
				if ep.AmzTarget != "" {
					upstreamReq.Header.Set("X-Amz-Target", ep.AmzTarget)
				}

				resp, err = s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
				deadline.stopHeaderTimer()
				if err != nil {
					if deadline.isTimeoutError(err) {
						deadline.close()
						log.Printf("%s endpoint=%s status=initial_response_timeout phase=response_headers timeout=%s", prefix, ep.Name, deadline.timeout)
						appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
							Platform:    account.Platform,
							AccountID:   account.ID,
							AccountName: account.Name,
							Kind:        "initial_response_timeout",
							Message:     kiroInitialResponseTimeoutMessage("response_headers", deadline.timeout),
						})
						return nil, kiroInitialResponseFailover("response_headers", deadline.timeout)
					}
					deadline.close()
					if attempt < kiroMaxRetries {
						log.Printf("%s endpoint=%s status=request_failed retry=%d/%d error=%v", prefix, ep.Name, attempt, kiroMaxRetries, err)
						if !sleepKiroBackoffWithContext(ctx, attempt) {
							return nil, ctx.Err()
						}
						attempt++
						continue
					}
					log.Printf("%s endpoint=%s status=request_failed retries_exhausted error=%v", prefix, ep.Name, err)
					appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
						Platform:    account.Platform,
						AccountID:   account.ID,
						AccountName: account.Name,
						Kind:        "transport_error",
						Message:     sanitizeKiroClientErrorMessage(err.Error()),
						Detail: fmt.Sprintf("class=%s endpoint=%s note=request_may_not_have_reached_upstream",
							kiroFailureTransport, ep.Name),
					})
					// B 类：必须包成 failover error，否则 handler 不写任何响应体
					lastErr = newKiroTransportFailure(
						"kiro_transport_error",
						http.StatusBadGateway,
						fmt.Sprintf("endpoint %s: %v", ep.Name, err),
					).failoverError()
					break
				}

				if resp.StatusCode == http.StatusTooManyRequests {
					respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
					_ = resp.Body.Close()
					deadline.close()
					s.handleUpstreamError(ctx, prefix, account, resp.StatusCode, resp.Header, respBody)
					lastErr = &UpstreamFailoverError{StatusCode: resp.StatusCode}
					break
				}

				if resp.StatusCode == http.StatusUnauthorized {
					respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
					_ = resp.Body.Close()
					deadline.close()

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
					deadline.close()
					s.handleUpstreamError(ctx, prefix, account, resp.StatusCode, resp.Header, respBody)
					lastErr = &UpstreamFailoverError{StatusCode: resp.StatusCode}
					break
				}

				if resp.StatusCode == http.StatusBadRequest {
					respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
					_ = resp.Body.Close()
					deadline.close()
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
						if !account.IsKiroApiKey() {
							cacheResult = s.beginKiroOAuthCache(c, account, activeUpstreamModel, claudeReq, cacheEstimation)
						}
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
						"Bad request: "+sanitizeKiroClientErrorMessage(rawErrorMsg))
				}

				if resp.StatusCode >= 400 && s.shouldRetryUpstreamError(resp.StatusCode) {
					respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
					_ = resp.Body.Close()
					deadline.close()
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

				respDeadline = attachKiroInitialDeadline(resp, deadline)
				goto endpointDone
			}

			resp = nil
			if epIdx < len(endpoints)-1 {
				log.Printf("%s switching from endpoint %s to %s", prefix, ep.Name, endpoints[epIdx+1].Name)
			}
		}

		if resp == nil {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, s.writeOpenAIError(c, http.StatusBadGateway, "upstream_error", "All endpoints failed")
		}

	endpointDone:
		defer func() {
			_ = resp.Body.Close()
			respDeadline.close()
		}()

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
		if !account.IsKiroApiKey() {
			cacheReadTokens, cacheCreationTokens = s.applyKiroSimulatedCacheReadRatio(cacheReadTokens, cacheCreationTokens)
		}
		thinkingEnabled := kiro.IsThinkingConfigEnabled(claudeReq)

		var usage *OpenAIUsage
		var firstTokenMs *int

		if wantStream {
			result, err := s.handleOpenAIStreamingResponse(c, resp, startTime, originalModel, inputTokens, toolNameReverseMap, cacheCreationTokens, cacheReadTokens, thinkingEnabled)
			if err != nil {
				recordKiroFailureOps(c, account, err)
				return nil, err
			}
			if result.failedAfterCommit {
				recordKiroPostCommitFailureOps(c, account, result.inBandException, result.upstreamRequestID)
			}
			usage = result.usage
			firstTokenMs = result.firstTokenMs
		} else {
			result, err := s.handleOpenAINonStreamingResponse(c, resp, originalModel, inputTokens, toolNameReverseMap, cacheCreationTokens, cacheReadTokens, thinkingEnabled, respDeadline.remaining())
			if err != nil {
				if timeoutErr, ok := isKiroInitialResponseTimeout(err); ok {
					log.Printf("%s status=initial_response_timeout phase=%s timeout=%s", prefix, timeoutErr.Phase, timeoutErr.Timeout)
					appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
						Platform:    account.Platform,
						AccountID:   account.ID,
						AccountName: account.Name,
						Kind:        "initial_response_timeout",
						Message:     kiroInitialResponseTimeoutMessage(timeoutErr.Phase, timeoutErr.Timeout),
					})
					return nil, kiroInitialResponseFailover(timeoutErr.Phase, timeoutErr.Timeout)
				}
				recordKiroFailureOps(c, account, err)
				return nil, err
			}
			usage = result.usage
		}

		if activeUpstreamModel != originalModel {
			log.Printf("%s model_effective requested_model=%s effective_model=%s", prefix, originalModel, activeUpstreamModel)
		}
		if !account.IsKiroApiKey() {
			cacheResult.Commit()
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
