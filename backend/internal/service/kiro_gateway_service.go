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
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	kiroMaxRetries     = 3
	kiroRetryBaseDelay = 1 * time.Second
	kiroRetryMaxDelay  = 16 * time.Second
)

// kiroEndpointConfig defines an upstream endpoint for Kiro requests
type kiroEndpointConfig struct {
	URL       string // Full endpoint URL
	Host      string // Host header value
	AmzTarget string // X-Amz-Target header (empty for AWSQ)
	Name      string // Endpoint name for logging
}

// getKiroEndpoints returns ordered endpoint list based on account config.
// Both AWSQ and CodeWhisperer share the same upstream body size limit (~810KB),
// so there is no benefit to token-based dynamic routing.
// Priority: preferred_endpoint config > default (AWSQ first, CW fallback).
func getKiroEndpoints(account *Account) []kiroEndpointConfig {
	// Request routing always uses us-east-1 regardless of account's token region.
	// Verified: EU accounts (e.g. eu-north-1 IdC) can send requests to us-east-1 endpoints.
	awsq := kiroEndpointConfig{
		URL:  "https://q.us-east-1.amazonaws.com/generateAssistantResponse",
		Host: "q.us-east-1.amazonaws.com",
		Name: "AWSQ",
	}
	cw := kiroEndpointConfig{
		URL:       "https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse",
		Host:      "codewhisperer.us-east-1.amazonaws.com",
		AmzTarget: "AmazonCodeWhispererStreamingService.GenerateAssistantResponse",
		Name:      "CodeWhisperer",
	}

	// Explicit preferred_endpoint overrides default order
	switch account.GetKiroPreferredEndpoint() {
	case "awsq", "q", "cli":
		return []kiroEndpointConfig{awsq, cw}
	case "cw", "codewhisperer", "kiro":
		return []kiroEndpointConfig{cw, awsq}
	}

	// Default: AWSQ first (supports thinking), CW as fallback
	return []kiroEndpointConfig{awsq, cw}
}

// KiroGatewayService handles Kiro/CodeWhisperer platform API forwarding
type KiroGatewayService struct {
	accountRepo      AccountRepository
	tokenProvider    *KiroTokenProvider
	rateLimitService *RateLimitService
	httpUpstream     HTTPUpstream
	settingService   *SettingService
}

// NewKiroGatewayService creates a new KiroGatewayService
func NewKiroGatewayService(
	accountRepo AccountRepository,
	tokenProvider *KiroTokenProvider,
	rateLimitService *RateLimitService,
	httpUpstream HTTPUpstream,
	settingService *SettingService,
) *KiroGatewayService {
	return &KiroGatewayService{
		accountRepo:      accountRepo,
		tokenProvider:    tokenProvider,
		rateLimitService: rateLimitService,
		httpUpstream:     httpUpstream,
		settingService:   settingService,
	}
}

// GetTokenProvider returns the token provider
func (s *KiroGatewayService) GetTokenProvider() *KiroTokenProvider {
	return s.tokenProvider
}

// IsModelSupported checks if the model is supported by Kiro
// Kiro supports Claude models through CodeWhisperer
func (s *KiroGatewayService) IsModelSupported(requestedModel string) bool {
	return strings.HasPrefix(requestedModel, "claude-")
}

// Forward handles Claude API requests and forwards to CodeWhisperer
func (s *KiroGatewayService) Forward(ctx context.Context, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
	startTime := time.Now()
	prefix := fmt.Sprintf("[kiro-Forward] account=%s", account.Name)

	// Parse Claude request
	claudeReq, err := kiro.ParseClaudeRequestFromJSON(body)
	if err != nil {
		return nil, fmt.Errorf("parse claude request: %w", err)
	}
	if strings.TrimSpace(claudeReq.Model) == "" {
		return nil, fmt.Errorf("missing model")
	}

	originalModel := claudeReq.Model
	mappedModel := kiro.GetModelID(originalModel)

	// Cache estimation for billing
	cacheEstimation := kiro.EstimateCache(claudeReq)
	var cacheHit bool
	if cacheEstimation.MeetsCacheThreshold {
		// Generate cache key from system + tools
		systemText := kiro.ExtractSystemPromptText(claudeReq.System)
		toolsJSON := ""
		if len(claudeReq.Tools) > 0 {
			if toolsBytes, err := json.Marshal(claudeReq.Tools); err == nil {
				toolsJSON = string(toolsBytes)
			}
		}
		cacheKey := kiro.GenerateCacheKey(systemText, toolsJSON)
		cacheHit = kiro.GlobalCacheTracker.CheckAndMark(cacheKey)
		log.Printf("%s cache_estimation: cacheable=%d non_cacheable=%d meets_threshold=%v cache_hit=%v",
			prefix, cacheEstimation.CacheableTokens, cacheEstimation.NonCacheableTokens,
			cacheEstimation.MeetsCacheThreshold, cacheHit)
	}

	// Pre-check: Estimate input tokens and truncate if exceeding context limit.
	// Upstream has two limits: token count (~200k) AND body size (~810KB).
	// This handles the token limit; body size is checked after serialization.
	// Unlike before, truncation failure does NOT reject the request — we let
	// the body size check and upstream handle edge cases.
	estimatedTokens := kiro.EstimateInputTokens(claudeReq)
	if estimatedTokens > kiro.KiroContextPreCheckLimit {
		log.Printf("%s status=context_exceeds_limit estimated_tokens=%d limit=%d, attempting truncation",
			prefix, estimatedTokens, kiro.KiroContextPreCheckLimit)

		truncatedReq, truncated := kiro.TruncateAndRetry(claudeReq)
		if truncated {
			newEstimate := kiro.EstimateInputTokens(truncatedReq)
			log.Printf("%s status=messages_truncated original_messages=%d new_messages=%d tokens=%d->%d",
				prefix, len(claudeReq.Messages), len(truncatedReq.Messages), estimatedTokens, newEstimate)
			claudeReq = truncatedReq
			estimatedTokens = newEstimate

			// Re-serialize body for apikey passthrough path
			if newBody, err := json.Marshal(claudeReq); err == nil {
				body = newBody
			}
		} else {
			log.Printf("%s status=truncation_not_possible estimated_tokens=%d limit=%d, proceeding anyway",
				prefix, estimatedTokens, kiro.KiroContextPreCheckLimit)
		}
	}

	// Get access token
	if s.tokenProvider == nil {
		return nil, errors.New("kiro token provider not configured")
	}
	accessToken, err := s.tokenProvider.GetAccessToken(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("get access_token failed: %w", err)
	}

	// Proxy URL
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	// apikey accounts: direct Claude API passthrough (no CodeWhisperer transform)
	if account.IsKiroApiKey() {
		return s.forwardClaudeAPIRequest(ctx, c, account, claudeReq, body, accessToken, proxyURL, originalModel, startTime)
	}

	// Get profile ARN
	profileArn := account.GetKiroProfileArn()

	// Transform Claude request to CodeWhisperer format
	cwReq, err := kiro.TransformClaudeToCodeWhisperer(claudeReq, profileArn, c)
	if err != nil {
		return nil, fmt.Errorf("transform request: %w", err)
	}

	// Serialize request
	reqBody, err := json.Marshal(cwReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Check request body size and truncate history if needed.
	// Both AWSQ and CodeWhisperer reject requests with body > ~810KB.
	// Use 800KB as safe limit with margin.
	const maxRequestBodySize = 800 * 1024 // 800 KB (upstream hard limit ~810KB)
	if len(reqBody) > maxRequestBodySize {
		log.Printf("%s status=request_body_oversized body_size=%d limit=%d, attempting history truncation",
			prefix, len(reqBody), maxRequestBodySize)

		truncatedReq, truncatedBody, truncErr := kiro.TruncateToFitBodySize(claudeReq, profileArn, c, maxRequestBodySize)
		if truncErr != nil {
			log.Printf("%s status=body_truncation_failed error=%v", prefix, truncErr)
			return nil, &ContextTooLongError{
				EstimatedTokens: estimatedTokens,
				Limit:           kiro.KiroContextWindowLimit,
			}
		}

		log.Printf("%s status=body_truncated original_size=%d new_size=%d",
			prefix, len(reqBody), len(truncatedBody))
		cwReq = truncatedReq
		reqBody = truncatedBody
	}

	// Final safety check: if still too large after truncation, reject
	if len(reqBody) > maxRequestBodySize {
		log.Printf("%s status=request_still_too_large after truncation body_size=%d limit=%d",
			prefix, len(reqBody), maxRequestBodySize)
		return nil, &ContextTooLongError{
			EstimatedTokens: estimatedTokens,
			Limit:           kiro.KiroContextWindowLimit,
		}
	}

	log.Printf("%s request_size=%d model=%s mapped_model=%s", prefix, len(reqBody), originalModel, mappedModel)

	// Build endpoint list (primary + fallback)
	endpoints := getKiroEndpoints(account)

	// Generate machine ID for User-Agent headers
	machineID := kiro.GenerateMachineID(account.GetKiroRefreshToken())
	kiroVersion := "1.6.0"

	// Endpoint loop: try each endpoint, with retries per endpoint
	var resp *http.Response
	var lastErr error
	tokenRefreshed := false // shared across endpoints to avoid redundant refresh
	for epIdx, ep := range endpoints {
		for attempt := 1; attempt <= kiroMaxRetries; attempt++ {
			select {
			case <-ctx.Done():
				log.Printf("%s status=context_canceled error=%v", prefix, ctx.Err())
				return nil, ctx.Err()
			default:
			}

			upstreamReq, err := http.NewRequestWithContext(ctx, "POST", ep.URL, bytes.NewReader(reqBody))
			if err != nil {
				return nil, fmt.Errorf("create request: %w", err)
			}

			// Set headers
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

			resp, err = s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
			if err != nil {
				safeErr := sanitizeUpstreamErrorMessage(err.Error())
				appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
					Platform:           account.Platform,
					AccountID:          account.ID,
					AccountName:        account.Name,
					UpstreamStatusCode: 0,
					Kind:               "request_error",
					Message:            safeErr,
				})

				if attempt < kiroMaxRetries {
					log.Printf("%s endpoint=%s status=request_failed retry=%d/%d error=%v", prefix, ep.Name, attempt, kiroMaxRetries, err)
					if !sleepKiroBackoffWithContext(ctx, attempt) {
						return nil, ctx.Err()
					}
					continue
				}
				log.Printf("%s endpoint=%s status=request_failed retries_exhausted error=%v", prefix, ep.Name, err)
				lastErr = fmt.Errorf("endpoint %s: %w", ep.Name, err)
				break // try next endpoint
			}

			// Handle 429 rate limit — try next endpoint
			if resp.StatusCode == http.StatusTooManyRequests {
				respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
				_ = resp.Body.Close()

				upstreamMsg := extractKiroErrorMessage(respBody)
				upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)

				s.handleUpstreamError(ctx, prefix, account, resp.StatusCode, resp.Header, respBody)
				appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
					Platform:           account.Platform,
					AccountID:          account.ID,
					AccountName:        account.Name,
					UpstreamStatusCode: resp.StatusCode,
					UpstreamRequestID:  resp.Header.Get("x-amzn-requestid"),
					Kind:               "account_rate_limited",
					Message:            upstreamMsg,
				})
				log.Printf("%s endpoint=%s status=429 rate_limited, trying next endpoint", prefix, ep.Name)
				lastErr = &UpstreamFailoverError{StatusCode: resp.StatusCode}
				break // try next endpoint
			}

			// Handle 401: refresh token once, then retry
			if resp.StatusCode == http.StatusUnauthorized {
				respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
				_ = resp.Body.Close()

				upstreamMsg := extractKiroErrorMessage(respBody)

				if tokenRefreshed || s.tokenProvider == nil {
					log.Printf("%s endpoint=%s status=401 token_refresh_already_attempted, trying next endpoint msg=%s", prefix, ep.Name, upstreamMsg)
					lastErr = &UpstreamFailoverError{StatusCode: resp.StatusCode}
					break // try next endpoint
				}

				log.Printf("%s endpoint=%s status=401 token_expired, refreshing token... msg=%s", prefix, ep.Name, upstreamMsg)
				s.tokenProvider.InvalidateToken(account.ID)
				newToken, refreshErr := s.tokenProvider.GetAccessToken(ctx, account)
				if refreshErr != nil {
					log.Printf("%s endpoint=%s status=401 token_refresh_failed error=%v", prefix, ep.Name, refreshErr)
					lastErr = &UpstreamFailoverError{StatusCode: resp.StatusCode}
					break // try next endpoint
				}

				accessToken = newToken
				tokenRefreshed = true
				log.Printf("%s endpoint=%s status=401 token_refreshed, retrying", prefix, ep.Name)
				continue
			}

			// Handle 529 overloaded — try next endpoint
			if resp.StatusCode == 529 {
				respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
				_ = resp.Body.Close()
				s.handleUpstreamError(ctx, prefix, account, resp.StatusCode, resp.Header, respBody)
				log.Printf("%s endpoint=%s status=529 overloaded, trying next endpoint", prefix, ep.Name)
				lastErr = &UpstreamFailoverError{StatusCode: resp.StatusCode}
				break // try next endpoint
			}

			// Handle 400 bad request — return immediately (both endpoints share the same limits)
			if resp.StatusCode == http.StatusBadRequest {
				respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
				_ = resp.Body.Close()
				s.handleUpstreamError(ctx, prefix, account, resp.StatusCode, resp.Header, respBody)

				errorMsg := extractKiroErrorMessage(respBody)
				log.Printf("%s endpoint=%s status=400 bad_request body=%s", prefix, ep.Name, truncateForLog(respBody, 500))

				cwToolCount := 0
				if cwReq.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext != nil {
					cwToolCount = len(cwReq.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools)
				}
				log.Printf("%s endpoint=%s status=400 debug: request_body_size=%d model=%s messages=%d tools=%d history_entries=%d cw_tools=%d estimated_tokens=%d",
					prefix, ep.Name, len(reqBody), originalModel, len(claudeReq.Messages), len(claudeReq.Tools),
					len(cwReq.ConversationState.History), cwToolCount, estimatedTokens)

				// Check if error is context/input size related
				errorMsgLower := strings.ToLower(errorMsg)
				if strings.Contains(errorMsgLower, "input too long") ||
					strings.Contains(errorMsgLower, "too large") ||
					strings.Contains(errorMsgLower, "context") ||
					strings.Contains(errorMsgLower, "exceeds") ||
					strings.Contains(errorMsgLower, "maximum") {
					log.Printf("%s status=context_error_detected error=%s", prefix, errorMsg)
					setOpsUpstreamError(c, resp.StatusCode, errorMsg, "")
					appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
						Platform:           account.Platform,
						AccountID:          account.ID,
						AccountName:        account.Name,
						UpstreamStatusCode: resp.StatusCode,
						UpstreamRequestID:  resp.Header.Get("x-amzn-requestid"),
						Kind:               "context_too_long",
						Message:            errorMsg,
					})
					return nil, &ContextTooLongError{
						EstimatedTokens: estimatedTokens,
						Limit:           kiro.KiroContextWindowLimit,
					}
				}

				// 400 errors are deterministic — no point trying the other endpoint
				return nil, s.writeMappedClaudeError(c, account, resp.StatusCode, resp.Header.Get("x-amzn-requestid"), respBody)
			}

			// Handle retryable errors (5xx)
			if resp.StatusCode >= 400 && s.shouldRetryUpstreamError(resp.StatusCode) {
				respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
				_ = resp.Body.Close()

				if attempt < kiroMaxRetries {
					upstreamMsg := extractKiroErrorMessage(respBody)
					upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
					appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
						Platform:           account.Platform,
						AccountID:          account.ID,
						AccountName:        account.Name,
						UpstreamStatusCode: resp.StatusCode,
						UpstreamRequestID:  resp.Header.Get("x-amzn-requestid"),
						Kind:               "retry",
						Message:            upstreamMsg,
					})
					log.Printf("%s endpoint=%s status=%d retry=%d/%d body=%s", prefix, ep.Name, resp.StatusCode, attempt, kiroMaxRetries, truncateForLog(respBody, 500))
					if !sleepKiroBackoffWithContext(ctx, attempt) {
						return nil, ctx.Err()
					}
					continue
				}

				// Retries exhausted on this endpoint
				log.Printf("%s endpoint=%s status=%d retries_exhausted, trying next endpoint", prefix, ep.Name, resp.StatusCode)
				lastErr = &UpstreamFailoverError{StatusCode: resp.StatusCode}
				resp = &http.Response{
					StatusCode: resp.StatusCode,
					Header:     resp.Header.Clone(),
					Body:       io.NopCloser(bytes.NewReader(respBody)),
				}
				break // try next endpoint
			}

			// Success or non-retryable error — stop endpoint loop
			log.Printf("%s endpoint=%s status=%d", prefix, ep.Name, resp.StatusCode)
			goto endpointDone
		}

		// Reset resp before trying next endpoint to avoid using stale response
		resp = nil

		// Log endpoint switch
		if epIdx < len(endpoints)-1 {
			log.Printf("%s switching from endpoint %s to %s", prefix, ep.Name, endpoints[epIdx+1].Name)
		}
	}

	// All endpoints exhausted
	if resp == nil {
		if lastErr != nil {
			setOpsUpstreamError(c, 0, lastErr.Error(), "")
			return nil, lastErr
		}
		return nil, s.writeClaudeError(c, http.StatusBadGateway, "upstream_error", "All endpoints failed")
	}

endpointDone:
	defer func() { _ = resp.Body.Close() }()

	// Handle error response
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		requestID := resp.Header.Get("x-amzn-requestid")
		log.Printf("%s status=%d upstream_error request_id=%s body=%s", prefix, resp.StatusCode, requestID, truncateForLog(respBody, 1000))

		s.handleUpstreamError(ctx, prefix, account, resp.StatusCode, resp.Header, respBody)

		if s.shouldFailoverUpstreamError(resp.StatusCode) {
			upstreamMsg := extractKiroErrorMessage(respBody)
			upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
			appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
				Platform:           account.Platform,
				AccountID:          account.ID,
				AccountName:        account.Name,
				UpstreamStatusCode: resp.StatusCode,
				UpstreamRequestID:  resp.Header.Get("x-amzn-requestid"),
				Kind:               "failover",
				Message:            upstreamMsg,
			})
			return nil, &UpstreamFailoverError{StatusCode: resp.StatusCode}
		}

		return nil, s.writeMappedClaudeError(c, account, resp.StatusCode, resp.Header.Get("x-amzn-requestid"), respBody)
	}

	requestID := resp.Header.Get("x-amzn-requestid")
	if requestID != "" {
		c.Header("x-request-id", requestID)
	}

	// Estimate input tokens from the Claude request
	inputTokens := kiro.EstimateInputTokens(claudeReq)

	// Build tool name reverse map for restoring original names in response
	toolNameReverseMap := kiro.BuildReverseMapFromClaudeTools(claudeReq.Tools)

	var usage *ClaudeUsage
	var firstTokenMs *int

	// Calculate cache tokens to pass to handlers
	var cacheCreationTokens, cacheReadTokens int
	if cacheEstimation.MeetsCacheThreshold {
		if cacheHit {
			cacheReadTokens = cacheEstimation.CacheableTokens
		} else {
			cacheCreationTokens = cacheEstimation.CacheableTokens
		}
	}

	if claudeReq.Stream {
		// Streaming response
		streamRes, err := s.handleStreamingResponse(c, resp, startTime, originalModel, inputTokens, toolNameReverseMap, cacheCreationTokens, cacheReadTokens, cacheEstimation.MeetsCacheThreshold)
		if err != nil {
			log.Printf("%s status=stream_error error=%v", prefix, err)
			return nil, err
		}
		usage = streamRes.usage
		firstTokenMs = streamRes.firstTokenMs
	} else {
		// Non-streaming response
		streamRes, err := s.handleNonStreamingResponse(c, resp, startTime, originalModel, inputTokens, toolNameReverseMap, cacheCreationTokens, cacheReadTokens, cacheEstimation.MeetsCacheThreshold)
		if err != nil {
			log.Printf("%s status=non_stream_error error=%v", prefix, err)
			return nil, err
		}
		usage = streamRes.usage
		firstTokenMs = streamRes.firstTokenMs
	}

	// Apply cache token estimation to usage (for billing/logging)
	// Note: According to Anthropic's definition:
	// - input_tokens = non-cached input tokens (does NOT include cache_read_input_tokens)
	// - cache_read_input_tokens = tokens read from cache
	// - Total input = input_tokens + cache_read_input_tokens
	if cacheEstimation.MeetsCacheThreshold {
		if cacheHit {
			// Cache hit: attribute cacheable tokens to cache_read
			usage.CacheReadInputTokens = cacheEstimation.CacheableTokens
			// Subtract cached tokens from input_tokens to match Anthropic's definition
			usage.InputTokens -= cacheEstimation.CacheableTokens
			if usage.InputTokens < 0 {
				usage.InputTokens = 0
			}
		} else {
			// Cache miss: attribute cacheable tokens to cache_creation
			// Note: cache_creation tokens are still processed as input, so don't subtract
			usage.CacheCreationInputTokens = cacheEstimation.CacheableTokens
		}
	}

	return &ForwardResult{
		RequestID:    requestID,
		Usage:        *usage,
		Model:        originalModel,
		Stream:       claudeReq.Stream,
		Duration:     time.Since(startTime),
		FirstTokenMs: firstTokenMs,
	}, nil
}

// kiroStreamResult holds streaming result data
type kiroStreamResult struct {
	usage        *ClaudeUsage
	firstTokenMs *int
}

// handleStreamingResponse handles streaming response from CodeWhisperer
func (s *KiroGatewayService) handleStreamingResponse(c *gin.Context, resp *http.Response, startTime time.Time, originalModel string, inputTokens int, toolNameReverseMap map[string]string, cacheCreationTokens, cacheReadTokens int, cachingEnabled bool) (*kiroStreamResult, error) {
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}

	// Create message ID
	messageID := "msg_" + uuid.New().String()[:24]

	// Create AWS EventStream parser
	parser := kiro.NewAwsEventStreamParser(messageID, originalModel)

	// Create stream event converter
	converter := kiro.NewStreamEventConverter(messageID, originalModel, inputTokens)
	// Set tool name reverse map for restoring original names
	if toolNameReverseMap != nil {
		converter.SetToolNameReverseMap(toolNameReverseMap)
	}
	// Set cache tokens for response
	if cacheCreationTokens > 0 || cacheReadTokens > 0 {
		converter.SetCacheTokens(cacheCreationTokens, cacheReadTokens)
	}

	// Send initial events
	initialEvents := converter.BuildInitialEvents()
	for _, event := range initialEvents {
		sseStr, err := kiro.FormatClaudeSSE(event)
		if err != nil {
			continue
		}
		if _, err := c.Writer.Write([]byte(sseStr)); err != nil {
			return nil, err
		}
	}
	flusher.Flush()

	// Inject web search events (server_tool_use + web_search_tool_result) if present
	if wsEvents := GetWebSearchEvents(c); len(wsEvents) > 0 {
		blockIndex := 0
		for _, wsEvt := range wsEvents {
			wsSSEEvents := buildWebSearchSSEEvents(wsEvt, blockIndex)
			for _, evt := range wsSSEEvents {
				sseStr, err := kiro.FormatClaudeSSE(evt)
				if err != nil {
					continue
				}
				if _, err := c.Writer.Write([]byte(sseStr)); err != nil {
					return nil, err
				}
			}
			blockIndex += 2 // each search produces 2 blocks (server_tool_use + web_search_tool_result)
		}
		converter.SetContentBlockOffset(blockIndex)
		flusher.Flush()
	}

	var firstTokenMs *int
	var credits float64
	var contextPct float64

	// Read and process stream
	buf := make([]byte, 4096)
	var lastReadAt int64
	atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())

	// Stream interval timeout
	streamInterval := time.Duration(0)
	if s.settingService != nil && s.settingService.cfg != nil && s.settingService.cfg.Gateway.StreamDataIntervalTimeout > 0 {
		streamInterval = time.Duration(s.settingService.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}

	var intervalTicker *time.Ticker
	if streamInterval > 0 {
		intervalTicker = time.NewTicker(streamInterval)
		defer intervalTicker.Stop()
	}

	// Error event tracking
	errorEventSent := false
	sendErrorEvent := func(reason string) {
		if errorEventSent {
			return
		}
		errorEventSent = true
		errEvent := kiro.BuildClaudeError("overloaded_error", reason)
		if sseStr, err := kiro.FormatClaudeSSE(errEvent); err == nil {
			_, _ = c.Writer.Write([]byte(sseStr))
			flusher.Flush()
		}
	}

	for {
		// Check timeout
		if intervalTicker != nil {
			select {
			case <-intervalTicker.C:
				lastRead := time.Unix(0, atomic.LoadInt64(&lastReadAt))
				if time.Since(lastRead) >= streamInterval {
					log.Printf("Stream data interval timeout (kiro)")
					sendErrorEvent("stream_timeout")
					goto finishStream
				}
			default:
			}
		}

		n, err := resp.Body.Read(buf)
		if n > 0 {
			atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())

			// Process chunk through parser
			events := parser.Process(buf[:n])
			for _, event := range events {
				// Track first token time
				if firstTokenMs == nil && (event.Type == kiro.EventTextDelta || event.Type == kiro.EventToolUseInputDelta) {
					ms := int(time.Since(startTime).Milliseconds())
					firstTokenMs = &ms
				}

				// Track usage
				if event.Type == kiro.EventBackendUsage {
					if event.Credits > 0 {
						credits = event.Credits
					}
					if event.ContextPercentage > 0 {
						contextPct = event.ContextPercentage
					}
				}

				// Convert to Claude SSE events
				claudeEvents := converter.ConvertEvent(event)
				for _, claudeEvent := range claudeEvents {
					sseStr, err := kiro.FormatClaudeSSE(claudeEvent)
					if err != nil {
						continue
					}
					if _, err := c.Writer.Write([]byte(sseStr)); err != nil {
						sendErrorEvent("write_failed")
						goto finishStream
					}
				}
			}
			flusher.Flush()
		}

		if err != nil {
			if err == io.EOF {
				break
			}
			sendErrorEvent("stream_read_error")
			return nil, err
		}
	}

finishStream:
	// Finish parsing and get remaining events
	finalParserEvents := parser.Finish()
	for _, event := range finalParserEvents {
		claudeEvents := converter.ConvertEvent(event)
		for _, claudeEvent := range claudeEvents {
			sseStr, err := kiro.FormatClaudeSSE(claudeEvent)
			if err != nil {
				continue
			}
			_, _ = c.Writer.Write([]byte(sseStr))
		}
	}

	// Set context percentage before building final events
	// This allows BuildFinalEvents to calculate accurate input_tokens
	if contextPct > 0 {
		converter.SetContextPercentage(contextPct)
	}

	// Send final events
	finalEvents := converter.BuildFinalEvents()
	for _, event := range finalEvents {
		sseStr, err := kiro.FormatClaudeSSE(event)
		if err != nil {
			continue
		}
		_, _ = c.Writer.Write([]byte(sseStr))
	}
	flusher.Flush()

	// Calculate input tokens from context percentage if available
	// Note: Kiro's contextPct may only reflect current turn, not cumulative context size
	// To ensure client can correctly judge context size and trigger proactive compression,
	// use the larger value between estimated and calculated tokens
	// However, when caching is enabled, contextPct can be inflated, so we only use it
	// when caching is NOT active to avoid triggering compression too early
	accurateInputTokens := inputTokens
	if contextPct > 0 && !cachingEnabled {
		calculatedTokens := int(contextPct / 100.0 * float64(kiro.KiroContextWindowLimit))
		if calculatedTokens > accurateInputTokens {
			accurateInputTokens = calculatedTokens
		}
	}

	// Build usage from converter stats
	usage := &ClaudeUsage{
		InputTokens:         accurateInputTokens,
		OutputTokens:        converter.TotalOutputTokens(),
		ContextUsagePercent: contextPct,
	}

	// Log credits/context usage if available
	if credits > 0 || contextPct > 0 {
		log.Printf("[kiro-Forward] credits=%.4f context_pct=%.2f input_tokens=%d", credits, contextPct, accurateInputTokens)
	}

	return &kiroStreamResult{usage: usage, firstTokenMs: firstTokenMs}, nil
}

// handleNonStreamingResponse handles non-streaming response from CodeWhisperer
func (s *KiroGatewayService) handleNonStreamingResponse(c *gin.Context, resp *http.Response, startTime time.Time, originalModel string, inputTokens int, toolNameReverseMap map[string]string, cacheCreationTokens, cacheReadTokens int, cachingEnabled bool) (*kiroStreamResult, error) {
	// Read entire response
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var firstTokenMs *int
	ms := int(time.Since(startTime).Milliseconds())
	firstTokenMs = &ms

	// Parse complete response with tool name restoration
	messageID := "msg_" + uuid.New().String()[:24]
	parsedResp := kiro.ParseCompleteResponseWithNameRestore(respBody, toolNameReverseMap)

	// Calculate input tokens from context percentage if available
	// Note: Kiro's contextPct may only reflect current turn, not cumulative context size
	// To ensure client can correctly judge context size and trigger proactive compression,
	// use the larger value between estimated and calculated tokens
	// However, when caching is enabled, contextPct can be inflated, so we only use it
	// when caching is NOT active to avoid triggering compression too early
	accurateInputTokens := inputTokens
	if parsedResp.ContextPct > 0 && !cachingEnabled {
		calculatedTokens := int(parsedResp.ContextPct / 100.0 * float64(kiro.KiroContextWindowLimit))
		if calculatedTokens > accurateInputTokens {
			accurateInputTokens = calculatedTokens
		}
	}

	// Set cache tokens for response
	parsedResp.CacheCreationTokens = cacheCreationTokens
	parsedResp.CacheReadTokens = cacheReadTokens

	// Build Claude response with accurate input tokens
	claudeResp := kiro.BuildClaudeNonStreamResponse(messageID, originalModel, accurateInputTokens, parsedResp)

	// Inject web search blocks (server_tool_use + web_search_tool_result) if present
	if wsEvents := GetWebSearchEvents(c); len(wsEvents) > 0 {
		injectWebSearchContentBlocks(claudeResp, wsEvents)
	}

	// Serialize and send
	respJSON, err := json.Marshal(claudeResp)
	if err != nil {
		return nil, fmt.Errorf("marshal response: %w", err)
	}

	c.Data(http.StatusOK, "application/json", respJSON)

	// Extract usage
	usage := &ClaudeUsage{
		InputTokens:         accurateInputTokens,
		OutputTokens:        (len(parsedResp.Text) + 3) / 4, // Rough estimate
		ContextUsagePercent: parsedResp.ContextPct,
	}

	return &kiroStreamResult{usage: usage, firstTokenMs: firstTokenMs}, nil
}

// buildWebSearchSSEEvents builds SSE events for a single web search (server_tool_use + web_search_tool_result)
func buildWebSearchSSEEvents(evt WebSearchEvent, startIndex int) []kiro.ClaudeSSEEvent {
	toolUseIndex := startIndex
	resultIndex := startIndex + 1

	// Build search result content blocks
	var resultContent []map[string]any
	for _, r := range evt.Results {
		item := map[string]any{
			"type":  "web_search_result",
			"url":   r.URL,
			"title": r.Title,
		}
		if r.EncryptedContent != "" {
			item["encrypted_content"] = r.EncryptedContent
		}
		if r.PageContent != "" {
			item["page_content"] = r.PageContent
		}
		resultContent = append(resultContent, item)
	}

	inputJSON, _ := json.Marshal(map[string]string{"query": evt.Query})

	return []kiro.ClaudeSSEEvent{
		// content_block_start: server_tool_use
		{
			EventType: "content_block_start",
			Data: map[string]any{
				"type":  "content_block_start",
				"index": toolUseIndex,
				"content_block": map[string]any{
					"type":  "server_tool_use",
					"id":    evt.ID,
					"name":  "web_search",
					"input": map[string]any{},
				},
			},
		},
		// content_block_delta: input_json_delta
		{
			EventType: "content_block_delta",
			Data: map[string]any{
				"type":  "content_block_delta",
				"index": toolUseIndex,
				"delta": map[string]any{
					"type":         "input_json_delta",
					"partial_json": string(inputJSON),
				},
			},
		},
		// content_block_stop: server_tool_use
		{
			EventType: "content_block_stop",
			Data: map[string]any{
				"type":  "content_block_stop",
				"index": toolUseIndex,
			},
		},
		// content_block_start: web_search_tool_result
		{
			EventType: "content_block_start",
			Data: map[string]any{
				"type":  "content_block_start",
				"index": resultIndex,
				"content_block": map[string]any{
					"type":        "web_search_tool_result",
					"tool_use_id": evt.ID,
					"content":     resultContent,
				},
			},
		},
		// content_block_stop: web_search_tool_result
		{
			EventType: "content_block_stop",
			Data: map[string]any{
				"type":  "content_block_stop",
				"index": resultIndex,
			},
		},
	}
}

// injectWebSearchContentBlocks prepends server_tool_use + web_search_tool_result blocks
// into a non-streaming Claude response's content array.
func injectWebSearchContentBlocks(resp map[string]any, events []WebSearchEvent) {
	existingContent, _ := resp["content"].([]map[string]any)

	var injected []map[string]any
	for _, evt := range events {
		// server_tool_use block
		injected = append(injected, map[string]any{
			"type":  "server_tool_use",
			"id":    evt.ID,
			"name":  "web_search",
			"input": map[string]string{"query": evt.Query},
		})

		// web_search_tool_result block
		var resultContent []map[string]any
		for _, r := range evt.Results {
			item := map[string]any{
				"type":  "web_search_result",
				"url":   r.URL,
				"title": r.Title,
			}
			if r.EncryptedContent != "" {
				item["encrypted_content"] = r.EncryptedContent
			}
			if r.PageContent != "" {
				item["page_content"] = r.PageContent
			}
			resultContent = append(resultContent, item)
		}
		injected = append(injected, map[string]any{
			"type":        "web_search_tool_result",
			"tool_use_id": evt.ID,
			"content":     resultContent,
		})
	}

	// Prepend web search blocks before existing content
	combined := make([]map[string]any, 0, len(injected)+len(existingContent))
	combined = append(combined, injected...)
	combined = append(combined, existingContent...)
	resp["content"] = combined
}

func (s *KiroGatewayService) shouldRetryUpstreamError(statusCode int) bool {
	switch statusCode {
	case 500, 502, 503, 504:
		return true
	default:
		return false
	}
}

func (s *KiroGatewayService) shouldFailoverUpstreamError(statusCode int) bool {
	switch statusCode {
	case 401, 403, 429, 529:
		return true
	default:
		return statusCode >= 500
	}
}

func (s *KiroGatewayService) handleUpstreamError(ctx context.Context, prefix string, account *Account, statusCode int, headers http.Header, body []byte) {
	switch statusCode {
	case 429:
		// Rate limited - mark cooldown
		if s.tokenProvider != nil {
			s.tokenProvider.MarkCooldown(account.ID, 30*time.Second)
		}
		log.Printf("%s status=429 rate_limited", prefix)

	case 401:
		// Token expired/invalid — invalidate cached token so next request triggers refresh.
		// Do NOT ban the account; 401 is typically a stale access token.
		// Skip rateLimitService to prevent permanent disable.
		errMsg := extractKiroErrorMessage(body)
		if s.tokenProvider != nil {
			s.tokenProvider.InvalidateToken(account.ID)
		}
		log.Printf("%s status=401 token_invalidated msg=%s", prefix, errMsg)
		return

	case 403:
		// Permission error — mark banned
		errMsg := extractKiroErrorMessage(body)
		if strings.Contains(strings.ToLower(errMsg), "expired") ||
			strings.Contains(strings.ToLower(errMsg), "invalid") ||
			strings.Contains(strings.ToLower(errMsg), "forbidden") {
			if s.tokenProvider != nil {
				s.tokenProvider.MarkBanned(account.ID, errMsg)
			}
		}
		log.Printf("%s status=403 auth_error msg=%s", prefix, errMsg)

	case 400:
		// Bad request — log full body for debugging (context too long, malformed request, etc.)
		errMsg := extractKiroErrorMessage(body)
		log.Printf("%s status=400 bad_request msg=%s body=%s", prefix, errMsg, truncateForLog(body, 1000))

	case 529:
		// Overloaded - mark cooldown
		if s.tokenProvider != nil {
			s.tokenProvider.MarkCooldown(account.ID, 60*time.Second)
		}
		log.Printf("%s status=529 overloaded", prefix)
	}

	// Use rate limit service for other handling
	if s.rateLimitService != nil {
		s.rateLimitService.HandleUpstreamError(ctx, account, statusCode, headers, body)
	}
}

func (s *KiroGatewayService) writeClaudeError(c *gin.Context, status int, errType, message string) error {
	c.JSON(status, gin.H{
		"type":  "error",
		"error": gin.H{"type": errType, "message": message},
	})
	return fmt.Errorf("%s", message)
}

func (s *KiroGatewayService) writeMappedClaudeError(c *gin.Context, account *Account, upstreamStatus int, upstreamRequestID string, body []byte) error {
	upstreamMsg := extractKiroErrorMessage(body)
	upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)

	setOpsUpstreamError(c, upstreamStatus, upstreamMsg, "")
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: upstreamStatus,
		UpstreamRequestID:  upstreamRequestID,
		Kind:               "http_error",
		Message:            upstreamMsg,
	})

	var statusCode int
	var errType, errMsg string

	switch upstreamStatus {
	case 400:
		statusCode = http.StatusBadRequest
		errType = "invalid_request_error"
		errMsg = "Invalid request"
	case 401:
		statusCode = http.StatusBadGateway
		errType = "authentication_error"
		errMsg = "Upstream authentication failed"
	case 403:
		statusCode = http.StatusBadGateway
		errType = "permission_error"
		errMsg = "Upstream access forbidden"
	case 429:
		statusCode = http.StatusTooManyRequests
		errType = "rate_limit_error"
		errMsg = "Upstream rate limit exceeded"
	case 529:
		statusCode = http.StatusServiceUnavailable
		errType = "overloaded_error"
		errMsg = "Upstream service overloaded"
	default:
		statusCode = http.StatusBadGateway
		errType = "upstream_error"
		errMsg = "Upstream request failed"
	}

	c.JSON(statusCode, gin.H{
		"type":  "error",
		"error": gin.H{"type": errType, "message": errMsg},
	})

	if upstreamMsg == "" {
		return fmt.Errorf("upstream error: %d", upstreamStatus)
	}
	return fmt.Errorf("upstream error: %d message=%s", upstreamStatus, upstreamMsg)
}

// extractKiroErrorMessage extracts error message from Kiro/CodeWhisperer response
func extractKiroErrorMessage(body []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}

	// Try message field
	if msg, ok := payload["message"].(string); ok && strings.TrimSpace(msg) != "" {
		return msg
	}

	// Try error.message
	if errObj, ok := payload["error"].(map[string]any); ok {
		if msg, ok := errObj["message"].(string); ok && strings.TrimSpace(msg) != "" {
			return msg
		}
	}

	// Try __type for AWS errors
	if errType, ok := payload["__type"].(string); ok {
		return errType
	}

	return ""
}

// sleepKiroBackoffWithContext sleeps with exponential backoff, respecting context cancellation
func sleepKiroBackoffWithContext(ctx context.Context, attempt int) bool {
	delay := kiroRetryBaseDelay * time.Duration(1<<uint(attempt-1))
	if delay > kiroRetryMaxDelay {
		delay = kiroRetryMaxDelay
	}

	select {
	case <-ctx.Done():
		return false
	case <-time.After(delay):
		return true
	}
}

// TestConnection tests Kiro account connection
func (s *KiroGatewayService) TestConnection(ctx context.Context, account *Account, modelID string) (*TestConnectionResult, error) {
	// Get token
	if s.tokenProvider == nil {
		return nil, errors.New("kiro token provider not configured")
	}
	accessToken, err := s.tokenProvider.GetAccessToken(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("get access_token failed: %w", err)
	}

	// apikey accounts: test connection directly against base_url
	if account.IsKiroApiKey() {
		return s.testClaudeAPIConnection(ctx, account, accessToken, modelID)
	}

	// Get profile ARN
	profileArn := account.GetKiroProfileArn()

	// Build test request
	testModel := modelID
	if testModel == "" {
		testModel = "claude-3-5-sonnet-20241022"
	}
	testClaudeReq := &kiro.ClaudeRequest{
		Model: testModel,
		Messages: []kiro.ClaudeMessage{
			{
				Role:    "user",
				Content: "hi",
			},
		},
		MaxTokens: 10,
		Stream:    false,
	}

	cwReq, err := kiro.TransformClaudeToCodeWhisperer(testClaudeReq, profileArn, nil)
	if err != nil {
		return nil, fmt.Errorf("transform request: %w", err)
	}

	reqBody, err := json.Marshal(cwReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Proxy URL
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	// Build endpoint list (test connection: small request)
	endpoints := getKiroEndpoints(account)

	// Generate machine ID for User-Agent headers
	machineID := kiro.GenerateMachineID(account.GetKiroRefreshToken())
	kiroVersion := "1.6.0"

	// Try each endpoint
	var lastErr error
	for _, ep := range endpoints {
		req, err := http.NewRequestWithContext(ctx, "POST", ep.URL, bytes.NewReader(reqBody))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("User-Agent", fmt.Sprintf("aws-sdk-js/1.0.27 ua/2.1 os/linux lang/js md/nodejs#22.12.0 api/codewhispererstreaming#1.0.27 m/E KiroIDE-%s-%s", kiroVersion, machineID))
		req.Header.Set("x-amz-user-agent", fmt.Sprintf("aws-sdk-js/1.0.27 KiroIDE-%s-%s", kiroVersion, machineID))
		req.Header.Set("x-amzn-kiro-agent-mode", "vibe")
		req.Header.Set("x-amzn-codewhisperer-optout", "true")
		req.Header.Set("Host", ep.Host)
		req.Header.Set("Connection", "close")
		req.Header.Set("amz-sdk-invocation-id", uuid.New().String())
		req.Header.Set("amz-sdk-request", "attempt=1; max=3")
		if ep.AmzTarget != "" {
			req.Header.Set("X-Amz-Target", ep.AmzTarget)
		}

		resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
		if err != nil {
			log.Printf("[kiro-TestConnection] endpoint=%s request_failed error=%v", ep.Name, err)
			lastErr = err
			continue
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		if err != nil {
			return nil, fmt.Errorf("read response: %w", err)
		}

		log.Printf("[kiro-TestConnection] endpoint=%s response status=%d, body_len=%d, body_preview=%s",
			ep.Name, resp.StatusCode, len(respBody), truncateForLog(respBody, 500))

		if resp.StatusCode >= 400 {
			lastErr = fmt.Errorf("endpoint %s returned %d: %s", ep.Name, resp.StatusCode, string(respBody))
			log.Printf("[kiro-TestConnection] endpoint=%s failed, trying next", ep.Name)
			continue
		}

		// Parse response to extract text
		parsedResp := kiro.ParseCompleteResponse(respBody)

		log.Printf("[kiro-TestConnection] endpoint=%s parsed text=%q, tool_calls=%d", ep.Name, parsedResp.Text, len(parsedResp.ToolCalls))

		return &TestConnectionResult{
			Text:        parsedResp.Text,
			MappedModel: "claude-3-5-sonnet-20241022",
		}, nil
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("all endpoints failed")
}

// forwardClaudeAPIRequest forwards Claude API requests directly to a base_url endpoint (apikey accounts).
// No CodeWhisperer transformation — request and response are Claude API format.
func (s *KiroGatewayService) forwardClaudeAPIRequest(
	ctx context.Context, c *gin.Context, account *Account,
	claudeReq *kiro.ClaudeRequest, body []byte,
	apiKey, proxyURL, originalModel string,
	startTime time.Time,
) (*ForwardResult, error) {
	prefix := fmt.Sprintf("[kiro-apikey-Forward] account=%s", account.Name)

	baseURL := account.GetKiroBaseURL()
	if baseURL == "" {
		return nil, fmt.Errorf("base_url is empty for apikey account %d", account.ID)
	}
	targetURL := strings.TrimRight(baseURL, "/") + "/v1/messages"

	req, err := http.NewRequestWithContext(ctx, "POST", targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	log.Printf("%s url=%s model=%s stream=%v body_size=%d", prefix, targetURL, originalModel, claudeReq.Stream, len(body))

	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return nil, fmt.Errorf("upstream request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Handle error responses
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		log.Printf("%s status=%d body=%s", prefix, resp.StatusCode, truncateForLog(respBody, 1000))

		if s.shouldFailoverUpstreamError(resp.StatusCode) {
			return nil, &UpstreamFailoverError{StatusCode: resp.StatusCode}
		}
		return nil, s.writeMappedClaudeError(c, account, resp.StatusCode, resp.Header.Get("x-request-id"), respBody)
	}

	requestID := resp.Header.Get("x-request-id")
	if requestID == "" {
		requestID = resp.Header.Get("request-id")
	}
	if requestID != "" {
		c.Header("x-request-id", requestID)
	}

	inputTokens := kiro.EstimateInputTokens(claudeReq)

	if claudeReq.Stream {
		// Stream: pipe SSE directly to client
		c.Header("Content-Type", "text/event-stream; charset=utf-8")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")
		c.Status(http.StatusOK)

		flusher, ok := c.Writer.(http.Flusher)
		if !ok {
			return nil, errors.New("streaming not supported")
		}

		var usage ClaudeUsage
		var firstTokenMs *int
		buf := make([]byte, 4096)

		for {
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				if firstTokenMs == nil {
					ms := int(time.Since(startTime).Milliseconds())
					firstTokenMs = &ms
				}

				chunk := buf[:n]
				// Extract usage from SSE data lines
				s.extractClaudeSSEUsage(chunk, &usage)

				if _, writeErr := c.Writer.Write(chunk); writeErr != nil {
					break
				}
				flusher.Flush()
			}
			if readErr != nil {
				break
			}
		}

		if usage.InputTokens == 0 {
			usage.InputTokens = inputTokens
		}

		return &ForwardResult{
			RequestID:    requestID,
			Usage:        usage,
			Model:        originalModel,
			Stream:       true,
			Duration:     time.Since(startTime),
			FirstTokenMs: firstTokenMs,
		}, nil
	}

	// Non-stream: read and pipe JSON response
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	ms := int(time.Since(startTime).Milliseconds())

	c.Data(http.StatusOK, "application/json", respBody)

	// Extract usage from response
	usage := s.extractClaudeJSONUsage(respBody, inputTokens)

	return &ForwardResult{
		RequestID:    requestID,
		Usage:        usage,
		Model:        originalModel,
		Stream:       false,
		Duration:     time.Since(startTime),
		FirstTokenMs: &ms,
	}, nil
}

// extractClaudeSSEUsage extracts usage info from Claude SSE stream chunks.
// message_start: usage is at message.usage.input_tokens
// message_delta: usage is at usage.output_tokens
func (s *KiroGatewayService) extractClaudeSSEUsage(chunk []byte, usage *ClaudeUsage) {
	lines := bytes.Split(chunk, []byte("\n"))
	for _, line := range lines {
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		data := line[6:]
		var raw map[string]json.RawMessage
		if json.Unmarshal(data, &raw) != nil {
			continue
		}
		var eventType string
		if json.Unmarshal(raw["type"], &eventType) != nil {
			continue
		}
		switch eventType {
		case "message_start":
			var msg struct {
				Usage struct {
					InputTokens int `json:"input_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal(raw["message"], &msg) == nil && msg.Usage.InputTokens > 0 {
				usage.InputTokens = msg.Usage.InputTokens
			}
		case "message_delta":
			var u struct {
				OutputTokens int `json:"output_tokens"`
			}
			if json.Unmarshal(raw["usage"], &u) == nil && u.OutputTokens > 0 {
				usage.OutputTokens = u.OutputTokens
			}
		}
	}
}

// extractClaudeJSONUsage extracts usage from a Claude API JSON response.
func (s *KiroGatewayService) extractClaudeJSONUsage(body []byte, fallbackInput int) ClaudeUsage {
	var resp struct {
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &resp) == nil && resp.Usage.InputTokens > 0 {
		return ClaudeUsage{
			InputTokens:              resp.Usage.InputTokens,
			OutputTokens:             resp.Usage.OutputTokens,
			CacheCreationInputTokens: resp.Usage.CacheCreationInputTokens,
			CacheReadInputTokens:     resp.Usage.CacheReadInputTokens,
		}
	}
	return ClaudeUsage{InputTokens: fallbackInput}
}

// testClaudeAPIConnection tests connection for apikey accounts by sending a minimal request to base_url.
func (s *KiroGatewayService) testClaudeAPIConnection(ctx context.Context, account *Account, apiKey string, modelID string) (*TestConnectionResult, error) {
	baseURL := account.GetKiroBaseURL()
	if baseURL == "" {
		return nil, fmt.Errorf("base_url is empty for apikey account %d", account.ID)
	}
	targetURL := strings.TrimRight(baseURL, "/") + "/v1/messages"

	// Pick a test model: use provided modelID, or first key from model_mapping, or default
	testModel := modelID
	if testModel == "" {
		if mapping := account.GetModelMapping(); len(mapping) > 0 {
			for k := range mapping {
				testModel = k
				break
			}
		}
	}
	if testModel == "" {
		testModel = "claude-sonnet-4-20250514"
	}

	testReq := map[string]any{
		"model":      testModel,
		"max_tokens": 10,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
	}
	reqBody, err := json.Marshal(testReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	req, err := http.NewRequestWithContext(ctx, "POST", targetURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, truncateForLog(respBody, 500))
	}

	// Extract text from Claude API response
	var claudeResp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	text := string(respBody)
	if json.Unmarshal(respBody, &claudeResp) == nil && len(claudeResp.Content) > 0 {
		text = claudeResp.Content[0].Text
	}

	return &TestConnectionResult{
		Text:        text,
		MappedModel: testModel,
	}, nil
}
