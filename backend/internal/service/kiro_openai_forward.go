package service

import (
	"bytes"
	"context"
	"encoding/json"
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

	freeTier := s.isKiroFreeTier(account)

	// Free 订阅类型账号不支持 Opus，自动降级为 Sonnet 4.5
	if remapped, ok := s.remapModelForFreeTier(account, claudeReq.Model, freeTier); ok {
		log.Printf("%s free_tier_model_remap: %s -> %s", prefix, claudeReq.Model, remapped)
		claudeReq.Model = remapped
	}

	originalModel := claudeReq.Model
	mappedModel := kiro.GetModelID(originalModel)

	// Set default max_tokens if not provided.
	// AWSQ thinking mode shares the output token budget between thinking and text.
	// A too-small max_tokens causes thinking to exhaust the budget with no room for text output.
	// Aligned with kiro.rs model list default (64000).
	if claudeReq.MaxTokens <= 0 {
		claudeReq.MaxTokens = 64000
	}

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

	// 3. Token pre-check and truncation
	contextPreCheckLimit := kiro.GetContextPreCheckLimit(originalModel)
	estimatedTokens := kiro.EstimateInputTokens(claudeReq)
	if estimatedTokens > contextPreCheckLimit {
		log.Printf("%s status=context_exceeds_limit estimated_tokens=%d limit=%d, attempting truncation",
			prefix, estimatedTokens, contextPreCheckLimit)

		truncatedReq, truncated := kiro.TruncateAndRetry(claudeReq)
		if truncated {
			newEstimate := kiro.EstimateInputTokens(truncatedReq)
			log.Printf("%s status=messages_truncated original_messages=%d new_messages=%d tokens=%d->%d",
				prefix, len(claudeReq.Messages), len(truncatedReq.Messages), estimatedTokens, newEstimate)
			claudeReq = truncatedReq
			estimatedTokens = newEstimate
		}
	}

	// 4. Get access token
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
	if !kiro.Is1MContext(originalModel) {
		if kiro.CompressImagesInRequest(claudeReq) {
			log.Printf("%s images compressed for body size reduction", prefix)
		}
	}

	// 6. Transform to CodeWhisperer format
	// Priority: in-memory cache (synchronous with token refresh) > account snapshot > database
	profileArn := ""
	if s.tokenProvider != nil {
		profileArn = s.tokenProvider.GetProfileArn(account.ID)
	}
	if profileArn == "" {
		profileArn = account.GetKiroProfileArn()
	}
	if profileArn == "" && s.accountRepo != nil {
		freshAccount, err := s.accountRepo.GetByID(ctx, account.ID)
		if err == nil && freshAccount != nil {
			profileArn = freshAccount.GetKiroProfileArn()
		}
	}
	cwReq, err := kiro.TransformClaudeToCodeWhisperer(claudeReq, profileArn, c)
	if err != nil {
		return nil, fmt.Errorf("transform request: %w", err)
	}

	reqBody, err := json.Marshal(cwReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// 7. Body size check and truncation
	maxBodySize := getMaxCWBodySize(originalModel)
	if len(reqBody) > maxBodySize {
		log.Printf("%s status=request_body_oversized body_size=%d limit=%d, attempting truncation",
			prefix, len(reqBody), maxBodySize)

		truncatedReq, truncatedBody, truncErr := kiro.TruncateToFitBodySize(claudeReq, profileArn, c, maxBodySize)
		if truncErr != nil {
			return nil, s.writeOpenAIError(c, http.StatusBadRequest, "invalid_request_error",
				"Input context too long. Please reduce context length.")
		}
		cwReq = truncatedReq
		reqBody = truncatedBody
	}

	if len(reqBody) > maxBodySize {
		return nil, s.writeOpenAIError(c, http.StatusBadRequest, "invalid_request_error",
			"Input context too long. Please reduce context length.")
	}

	log.Printf("%s request_size=%d model=%s mapped_model=%s", prefix, len(reqBody), originalModel, mappedModel)

	// 7. Endpoint loop (reuse existing pattern)
	endpoints := getKiroEndpoints(account)
	machineID := s.resolveMachineID(account, freeTier)
	kiroVersion := "0.11.107"

	var resp *http.Response
	var lastErr error
	tokenRefreshed := false

	for epIdx, ep := range endpoints {
		for attempt := 1; attempt <= kiroMaxRetries; attempt++ {
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

			resp, err = s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
			if err != nil {
				if attempt < kiroMaxRetries {
					log.Printf("%s endpoint=%s status=request_failed retry=%d/%d error=%v", prefix, ep.Name, attempt, kiroMaxRetries, err)
					if !sleepKiroBackoffWithContext(ctx, attempt) {
						return nil, ctx.Err()
					}
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
				errorMsg := strings.ToLower(extractKiroErrorMessage(respBody))
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

	// 8. Process response
	inputTokens := kiro.EstimateInputTokens(claudeReq)
	toolNameReverseMap := kiro.BuildReverseMapFromClaudeTools(claudeReq.Tools)

	// Calculate cache tokens (cache_read + cache_creation coexist)
	// CW path: cap to model-specific context window
	cacheReadTokens, cacheCreationTokens := cacheEstimation.SplitCacheTokens(cacheResult, kiro.GetContextWindowLimit(originalModel))

	var usage *OpenAIUsage
	var firstTokenMs *int

	if wantStream {
		result, err := s.handleOpenAIStreamingResponse(c, resp, startTime, originalModel, inputTokens, toolNameReverseMap, cacheCreationTokens, cacheReadTokens)
		if err != nil {
			return nil, err
		}
		usage = result.usage
		firstTokenMs = result.firstTokenMs
	} else {
		result, err := s.handleOpenAINonStreamingResponse(c, resp, originalModel, inputTokens, toolNameReverseMap, cacheCreationTokens, cacheReadTokens)
		if err != nil {
			return nil, err
		}
		usage = result.usage
	}

	return &OpenAIForwardResult{
		RequestID:    resp.Header.Get("x-amzn-requestid"),
		Usage:        *usage,
		Model:        originalModel,
		Stream:       wantStream,
		Duration:     time.Since(startTime),
		FirstTokenMs: firstTokenMs,
	}, nil
}
