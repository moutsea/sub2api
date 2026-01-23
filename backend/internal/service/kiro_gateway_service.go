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

	// Get access token
	if s.tokenProvider == nil {
		return nil, errors.New("kiro token provider not configured")
	}
	accessToken, err := s.tokenProvider.GetAccessToken(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("get access_token failed: %w", err)
	}

	// Get profile ARN
	profileArn := account.GetKiroProfileArn()

	// Proxy URL
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	// Transform Claude request to CodeWhisperer format
	cwReq, err := kiro.TransformClaudeToCodeWhisperer(claudeReq, profileArn)
	if err != nil {
		return nil, fmt.Errorf("transform request: %w", err)
	}

	// Serialize request
	reqBody, err := json.Marshal(cwReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	log.Printf("%s request_size=%d model=%s", prefix, len(reqBody), originalModel)

	// Build HTTP request
	region := account.GetKiroRegion()
	// CodeWhisperer API endpoint - use generateAssistantResponse path
	endpoint := fmt.Sprintf("https://codewhisperer.%s.amazonaws.com/generateAssistantResponse", region)

	// Retry loop
	var resp *http.Response
	for attempt := 1; attempt <= kiroMaxRetries; attempt++ {
		// Check context cancellation
		select {
		case <-ctx.Done():
			log.Printf("%s status=context_canceled error=%v", prefix, ctx.Err())
			return nil, ctx.Err()
		default:
		}

		upstreamReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(reqBody))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}

		// Set headers
		upstreamReq.Header.Set("Content-Type", "application/json")
		upstreamReq.Header.Set("Authorization", "Bearer "+accessToken)
		// CodeWhisperer API always returns SSE stream response, must set Accept header
		upstreamReq.Header.Set("Accept", "text/event-stream")
		// AWS SDK headers (aligned with kiro4api)
		upstreamReq.Header.Set("User-Agent", "aws-sdk-js/3.738.0 ua/2.1 os/deno lang/ts KiroGateway")
		upstreamReq.Header.Set("x-amz-user-agent", "aws-sdk-js/3.738.0 KiroGateway")
		upstreamReq.Header.Set("x-amzn-kiro-agent-mode", "vibe")
		upstreamReq.Header.Set("x-amzn-codewhisperer-optout", "true")
		upstreamReq.Header.Set("amz-sdk-invocation-id", uuid.New().String())
		upstreamReq.Header.Set("amz-sdk-request", "attempt=1; max=3")

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
				log.Printf("%s status=request_failed retry=%d/%d error=%v", prefix, attempt, kiroMaxRetries, err)
				if !sleepKiroBackoffWithContext(ctx, attempt) {
					log.Printf("%s status=context_canceled_during_backoff", prefix)
					return nil, ctx.Err()
				}
				continue
			}
			log.Printf("%s status=request_failed retries_exhausted error=%v", prefix, err)
			setOpsUpstreamError(c, 0, safeErr, "")
			return nil, s.writeClaudeError(c, http.StatusBadGateway, "upstream_error", "Upstream request failed after retries")
		}

		// Handle 429 rate limit
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
			log.Printf("%s status=429 rate_limited body=%s", prefix, truncateForLog(respBody, 200))

			return nil, &UpstreamFailoverError{StatusCode: resp.StatusCode}
		}

		// Handle retryable errors
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
				log.Printf("%s status=%d retry=%d/%d body=%s", prefix, resp.StatusCode, attempt, kiroMaxRetries, truncateForLog(respBody, 500))
				if !sleepKiroBackoffWithContext(ctx, attempt) {
					log.Printf("%s status=context_canceled_during_backoff", prefix)
					return nil, ctx.Err()
				}
				continue
			}

			// All retries failed
			resp = &http.Response{
				StatusCode: resp.StatusCode,
				Header:     resp.Header.Clone(),
				Body:       io.NopCloser(bytes.NewReader(respBody)),
			}
			break
		}

		// Success
		break
	}
	defer func() { _ = resp.Body.Close() }()

	// Handle error response
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
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

	var usage *ClaudeUsage
	var firstTokenMs *int

	if claudeReq.Stream {
		// Streaming response
		streamRes, err := s.handleStreamingResponse(c, resp, startTime, originalModel)
		if err != nil {
			log.Printf("%s status=stream_error error=%v", prefix, err)
			return nil, err
		}
		usage = streamRes.usage
		firstTokenMs = streamRes.firstTokenMs
	} else {
		// Non-streaming response
		streamRes, err := s.handleNonStreamingResponse(c, resp, startTime, originalModel)
		if err != nil {
			log.Printf("%s status=non_stream_error error=%v", prefix, err)
			return nil, err
		}
		usage = streamRes.usage
		firstTokenMs = streamRes.firstTokenMs
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
func (s *KiroGatewayService) handleStreamingResponse(c *gin.Context, resp *http.Response, startTime time.Time, originalModel string) (*kiroStreamResult, error) {
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
	inputTokens := 0 // Will be updated from usage events
	converter := kiro.NewStreamEventConverter(messageID, originalModel, inputTokens)

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

	// Build usage from converter stats
	usage := &ClaudeUsage{
		InputTokens:  inputTokens,
		OutputTokens: converter.TotalOutputTokens(),
	}

	// Log credits/context usage if available
	if credits > 0 || contextPct > 0 {
		log.Printf("[kiro-Forward] credits=%.4f context_pct=%.2f", credits, contextPct)
	}

	return &kiroStreamResult{usage: usage, firstTokenMs: firstTokenMs}, nil
}

// handleNonStreamingResponse handles non-streaming response from CodeWhisperer
func (s *KiroGatewayService) handleNonStreamingResponse(c *gin.Context, resp *http.Response, startTime time.Time, originalModel string) (*kiroStreamResult, error) {
	// Read entire response
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var firstTokenMs *int
	ms := int(time.Since(startTime).Milliseconds())
	firstTokenMs = &ms

	// Parse complete response
	messageID := "msg_" + uuid.New().String()[:24]
	parsedResp := kiro.ParseCompleteResponse(respBody)

	// Build Claude response
	inputTokens := 0 // Estimate or extract from response
	claudeResp := kiro.BuildClaudeNonStreamResponse(messageID, originalModel, inputTokens, parsedResp)

	// Serialize and send
	respJSON, err := json.Marshal(claudeResp)
	if err != nil {
		return nil, fmt.Errorf("marshal response: %w", err)
	}

	c.Data(http.StatusOK, "application/json", respJSON)

	// Extract usage
	usage := &ClaudeUsage{
		InputTokens:  inputTokens,
		OutputTokens: (len(parsedResp.Text) + 3) / 4, // Rough estimate
	}

	return &kiroStreamResult{usage: usage, firstTokenMs: firstTokenMs}, nil
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

	case 401, 403:
		// Auth error - may need to refresh token or mark banned
		errMsg := extractKiroErrorMessage(body)
		if strings.Contains(strings.ToLower(errMsg), "expired") ||
			strings.Contains(strings.ToLower(errMsg), "invalid") {
			if s.tokenProvider != nil {
				s.tokenProvider.MarkBanned(account.ID, errMsg)
			}
		}
		log.Printf("%s status=%d auth_error msg=%s", prefix, statusCode, errMsg)

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
func (s *KiroGatewayService) TestConnection(ctx context.Context, account *Account) (*TestConnectionResult, error) {
	// Get token
	if s.tokenProvider == nil {
		return nil, errors.New("kiro token provider not configured")
	}
	accessToken, err := s.tokenProvider.GetAccessToken(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("get access_token failed: %w", err)
	}

	// Get profile ARN
	profileArn := account.GetKiroProfileArn()

	// Build test request
	testClaudeReq := &kiro.ClaudeRequest{
		Model: "claude-3-5-sonnet-20241022",
		Messages: []kiro.ClaudeMessage{
			{
				Role:    "user",
				Content: "hi",
			},
		},
		MaxTokens: 10,
		Stream:    false,
	}

	cwReq, err := kiro.TransformClaudeToCodeWhisperer(testClaudeReq, profileArn)
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

	// Build HTTP request
	region := account.GetKiroRegion()
	// CodeWhisperer API endpoint - use generateAssistantResponse path
	endpoint := fmt.Sprintf("https://codewhisperer.%s.amazonaws.com/generateAssistantResponse", region)

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	// CodeWhisperer API always returns SSE stream response, must set Accept header
	req.Header.Set("Accept", "text/event-stream")
	// AWS SDK headers (aligned with kiro4api)
	req.Header.Set("User-Agent", "aws-sdk-js/3.738.0 ua/2.1 os/deno lang/ts KiroGateway")
	req.Header.Set("x-amz-user-agent", "aws-sdk-js/3.738.0 KiroGateway")
	req.Header.Set("x-amzn-kiro-agent-mode", "vibe")
	req.Header.Set("x-amzn-codewhisperer-optout", "true")
	req.Header.Set("amz-sdk-invocation-id", uuid.New().String())
	req.Header.Set("amz-sdk-request", "attempt=1; max=3")

	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	log.Printf("[kiro-TestConnection] response status=%d, body_len=%d, body_preview=%s",
		resp.StatusCode, len(respBody), truncateForLog(respBody, 500))

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse response to extract text
	parsedResp := kiro.ParseCompleteResponse(respBody)

	log.Printf("[kiro-TestConnection] parsed text=%q, tool_calls=%d", parsedResp.Text, len(parsedResp.ToolCalls))

	return &TestConnectionResult{
		Text:        parsedResp.Text,
		MappedModel: "claude-3-5-sonnet-20241022",
	}, nil
}
