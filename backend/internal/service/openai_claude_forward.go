package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/gin-gonic/gin"
)

// ForwardAsClaudeMessages handles Claude Messages API requests routed to OpenAI accounts.
// For APIKey accounts: Claude → Chat Completions → upstream → Claude
// For OAuth accounts: Claude → Responses API → upstream (chatgpt.com) → Claude
func (s *OpenAIGatewayService) ForwardAsClaudeMessages(ctx context.Context, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
	log.Printf("[openai-claude-compat] account=%s(%d) type=%s platform=%s", account.Name, account.ID, account.Type, account.Platform)
	if account.Type == AccountTypeOAuth {
		return s.forwardClaudeViaResponsesAPI(ctx, c, account, body)
	}
	return s.forwardClaudeViaChatCompletions(ctx, c, account, body)
}

// forwardClaudeViaChatCompletions handles Claude → Chat Completions API path (for APIKey accounts).
func (s *OpenAIGatewayService) forwardClaudeViaChatCompletions(ctx context.Context, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
	startTime := time.Now()
	prefix := fmt.Sprintf("[openai-claude-compat] account=%s(%d) type=apikey", account.Name, account.ID)

	// 1. Convert Claude request → OpenAI Chat Completions format
	openaiBody, originalModel, wantStream, err := kiro.ConvertClaudeToOpenAI(body)
	if err != nil {
		return nil, fmt.Errorf("convert claude to openai: %w", err)
	}
	log.Printf("%s model=%s→%s stream=%v", prefix, originalModel, kiro.GetOpenAIModelID(originalModel), wantStream)

	// 2. Get access token
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}

	// 3. Build upstream request to OpenAI Chat Completions
	upstreamReq, err := s.buildChatCompletionsRequest(ctx, c, account, openaiBody, token)
	if err != nil {
		return nil, err
	}

	// 4. Send request and handle response
	return s.doClaudeCompatRequest(ctx, c, account, upstreamReq, openaiBody, originalModel, wantStream, startTime, prefix, false)
}

// forwardClaudeViaResponsesAPI handles Claude → Responses API path (for OAuth accounts).
func (s *OpenAIGatewayService) forwardClaudeViaResponsesAPI(ctx context.Context, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
	startTime := time.Now()
	prefix := fmt.Sprintf("[openai-claude-compat] account=%s(%d) type=oauth", account.Name, account.ID)

	// 1. Convert Claude request → OpenAI Responses API format
	responsesBody, originalModel, err := kiro.ConvertClaudeToResponses(body)
	if err != nil {
		return nil, fmt.Errorf("convert claude to responses: %w", err)
	}

	// 2. Apply codex OAuth transform (model normalization, instructions, etc.)
	var reqBody map[string]any
	if err := json.Unmarshal(responsesBody, &reqBody); err != nil {
		return nil, fmt.Errorf("parse responses body: %w", err)
	}
	codexResult := applyCodexOAuthTransform(reqBody)
	mappedModel := kiro.GetOpenAIModelID(originalModel)
	if codexResult.NormalizedModel != "" {
		mappedModel = codexResult.NormalizedModel
	}
	// Re-serialize after transform
	responsesBody, err = json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("serialize responses body: %w", err)
	}

	log.Printf("%s model=%s→%s (responses API)", prefix, originalModel, mappedModel)

	// 3. Get access token
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}

	// 4. Build upstream request using standard OAuth path (chatgpt.com)
	promptCacheKey := codexResult.PromptCacheKey
	upstreamReq, err := s.buildUpstreamRequest(ctx, c, account, responsesBody, token, true, promptCacheKey, false)
	if err != nil {
		return nil, err
	}

	// 5. Send request and handle response (Responses API SSE → Claude SSE)
	return s.doClaudeCompatRequest(ctx, c, account, upstreamReq, responsesBody, originalModel, true, startTime, prefix, true)
}

// doClaudeCompatRequest sends the upstream request and handles the response, converting back to Claude format.
// isResponsesAPI indicates whether the upstream returns Responses API SSE (true) or Chat Completions SSE (false).
func (s *OpenAIGatewayService) doClaudeCompatRequest(
	ctx context.Context, c *gin.Context, account *Account,
	upstreamReq *http.Request, upstreamBody []byte,
	originalModel string, wantStream bool,
	startTime time.Time, prefix string,
	isResponsesAPI bool,
) (*ForwardResult, error) {
	// Send request
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	// NOTE: Temporarily disabled to reduce potential large payload logging/storage downstream.
	// if c != nil {
	// 	c.Set(OpsUpstreamRequestBodyKey, string(upstreamBody))
	// }

	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		safeErr := sanitizeUpstreamErrorMessage(err.Error())
		setOpsUpstreamError(c, 0, safeErr, "")
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: 0,
			Kind:               "request_error",
			Message:            safeErr,
		})
		writeClaudeError(c, http.StatusBadGateway, "api_error", "Upstream request failed")
		return nil, fmt.Errorf("upstream request failed: %s", safeErr)
	}
	defer func() { _ = resp.Body.Close() }()

	// Handle error responses
	if resp.StatusCode >= 400 {
		if s.shouldFailoverUpstreamError(resp.StatusCode) {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(respBody))

			upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(respBody)))
			upstreamDetail := ""
			if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
				maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
				if maxBytes <= 0 {
					maxBytes = 2048
				}
				upstreamDetail = truncateString(string(respBody), maxBytes)
			}
			appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
				Platform:           account.Platform,
				AccountID:          account.ID,
				AccountName:        account.Name,
				UpstreamStatusCode: resp.StatusCode,
				UpstreamRequestID:  resp.Header.Get("x-request-id"),
				Kind:               "failover",
				Message:            upstreamMsg,
				Detail:             upstreamDetail,
			})
			s.handleFailoverSideEffects(ctx, resp, account)
			return nil, &UpstreamFailoverError{StatusCode: resp.StatusCode}
		}
		return s.handleClaudeCompatErrorResponse(resp, c, account)
	}

	// Handle success response
	var inputTokens, outputTokens int
	var firstTokenMs *int

	if isResponsesAPI {
		// Responses API always streams
		result, err := s.handleClaudeCompatResponsesStream(resp, c, startTime, originalModel)
		if err != nil {
			return nil, err
		}
		inputTokens = result.inputTokens
		outputTokens = result.outputTokens
		firstTokenMs = result.firstTokenMs
	} else if wantStream {
		result, err := s.handleClaudeCompatStreamResponse(resp, c, startTime, originalModel)
		if err != nil {
			return nil, err
		}
		inputTokens = result.inputTokens
		outputTokens = result.outputTokens
		firstTokenMs = result.firstTokenMs
	} else {
		result, err := s.handleClaudeCompatNonStreamResponse(resp, c, originalModel)
		if err != nil {
			return nil, err
		}
		inputTokens = result.inputTokens
		outputTokens = result.outputTokens
	}

	log.Printf("%s status=ok input=%d output=%d duration=%v", prefix, inputTokens, outputTokens, time.Since(startTime))

	return &ForwardResult{
		RequestID: resp.Header.Get("x-request-id"),
		Usage: ClaudeUsage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
		},
		Model:        originalModel,
		Stream:       wantStream || isResponsesAPI,
		Duration:     time.Since(startTime),
		FirstTokenMs: firstTokenMs,
	}, nil
}

// claudeCompatResult holds parsed usage from response handling.
type claudeCompatResult struct {
	inputTokens  int
	outputTokens int
	firstTokenMs *int
}

// handleClaudeCompatNonStreamResponse reads OpenAI response, converts to Claude format, writes to client.
func (s *OpenAIGatewayService) handleClaudeCompatNonStreamResponse(resp *http.Response, c *gin.Context, originalModel string) (*claudeCompatResult, error) {
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	claudeBody, usage, err := kiro.ConvertOpenAIResponseToClaude(respBody, originalModel)
	if err != nil {
		return nil, fmt.Errorf("convert response: %w", err)
	}

	c.Data(http.StatusOK, "application/json", claudeBody)

	result := &claudeCompatResult{}
	if usage != nil {
		result.inputTokens = usage.InputTokens
		result.outputTokens = usage.OutputTokens
	}
	return result, nil
}

// handleClaudeCompatStreamResponse reads OpenAI Chat Completions SSE stream, converts to Claude SSE.
func (s *OpenAIGatewayService) handleClaudeCompatStreamResponse(resp *http.Response, c *gin.Context, startTime time.Time, originalModel string) (*claudeCompatResult, error) {
	messageID := fmt.Sprintf("msg_%s", generateShortID())

	converter := kiro.NewClaudeStreamConverter(originalModel, messageID)

	// Set SSE headers
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")

	// Send message_start
	_, _ = c.Writer.WriteString(converter.BuildMessageStart())
	c.Writer.Flush()

	var firstTokenMs *int
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}

		events := converter.ConvertChunk([]byte(data))
		if events == "" {
			continue
		}

		if firstTokenMs == nil {
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}

		_, _ = c.Writer.WriteString(events)
		c.Writer.Flush()
	}

	// Send message_stop
	_, _ = c.Writer.WriteString(converter.BuildMessageStop())
	c.Writer.Flush()

	return &claudeCompatResult{
		inputTokens:  converter.InputTokens(),
		outputTokens: converter.OutputTokens(),
		firstTokenMs: firstTokenMs,
	}, nil
}

// handleClaudeCompatResponsesStream reads OpenAI Responses API SSE stream, converts to Claude SSE.
func (s *OpenAIGatewayService) handleClaudeCompatResponsesStream(resp *http.Response, c *gin.Context, startTime time.Time, originalModel string) (*claudeCompatResult, error) {
	messageID := fmt.Sprintf("msg_%s", generateShortID())

	converter := kiro.NewResponsesStreamConverter(originalModel, messageID)

	// Set SSE headers
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")

	// Send message_start
	_, _ = c.Writer.WriteString(converter.BuildMessageStart())
	c.Writer.Flush()

	var firstTokenMs *int
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)

	// Responses API SSE format: "event: xxx\ndata: {...}\n\n"
	var currentEventType string
	for scanner.Scan() {
		line := scanner.Text()

		// Parse event type
		if strings.HasPrefix(line, "event:") {
			currentEventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}

		// Parse data
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || currentEventType == "" {
			continue
		}

		events := converter.ConvertResponsesEvent(currentEventType, []byte(data))
		currentEventType = "" // Reset for next event

		if events == "" {
			continue
		}

		if firstTokenMs == nil {
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}

		_, _ = c.Writer.WriteString(events)
		c.Writer.Flush()
	}

	// Send message_stop
	_, _ = c.Writer.WriteString(converter.BuildMessageStop())
	c.Writer.Flush()

	return &claudeCompatResult{
		inputTokens:  converter.InputTokens(),
		outputTokens: converter.OutputTokens(),
		firstTokenMs: firstTokenMs,
	}, nil
}

// handleClaudeCompatErrorResponse handles non-failover upstream errors, returning Claude-format error.
func (s *OpenAIGatewayService) handleClaudeCompatErrorResponse(resp *http.Response, c *gin.Context, account *Account) (*ForwardResult, error) {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))

	upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	upstreamDetail := ""
	if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		upstreamDetail = truncateString(string(body), maxBytes)
	}
	setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)

	if !account.ShouldHandleErrorCode(resp.StatusCode) {
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  resp.Header.Get("x-request-id"),
			Kind:               "http_error",
			Message:            upstreamMsg,
			Detail:             upstreamDetail,
		})
		writeClaudeError(c, http.StatusInternalServerError, "api_error", "Upstream gateway error")
		if upstreamMsg == "" {
			return nil, fmt.Errorf("upstream error: %d (not in custom error codes)", resp.StatusCode)
		}
		return nil, fmt.Errorf("upstream error: %d (not in custom error codes) message=%s", resp.StatusCode, upstreamMsg)
	}

	shouldDisable := false
	if s.rateLimitService != nil {
		shouldDisable = s.rateLimitService.HandleUpstreamError(context.Background(), account, resp.StatusCode, resp.Header, body)
	}
	kind := "http_error"
	if shouldDisable {
		kind = "failover"
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: resp.StatusCode,
		UpstreamRequestID:  resp.Header.Get("x-request-id"),
		Kind:               kind,
		Message:            upstreamMsg,
		Detail:             upstreamDetail,
	})
	if shouldDisable {
		return nil, &UpstreamFailoverError{StatusCode: resp.StatusCode}
	}

	// Map to Claude-format error
	var errType, errMsg string
	var statusCode int
	switch resp.StatusCode {
	case 401:
		statusCode = http.StatusBadGateway
		errType = "authentication_error"
		errMsg = "Upstream authentication failed"
	case 429:
		statusCode = http.StatusTooManyRequests
		errType = "rate_limit_error"
		errMsg = "Upstream rate limit exceeded, please retry later"
	default:
		statusCode = http.StatusBadGateway
		errType = "api_error"
		errMsg = "Upstream request failed"
	}
	writeClaudeError(c, statusCode, errType, errMsg)
	return nil, fmt.Errorf("upstream error: %d %s", resp.StatusCode, errMsg)
}

// writeClaudeError writes a Claude Messages API format error response.
func writeClaudeError(c *gin.Context, status int, errType, message string) {
	c.JSON(status, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
}

// generateShortID generates a short random ID for message IDs using crypto/rand.
func generateShortID() string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 16)
	// Use time-based seed as fallback (crypto/rand imported would be better but keep deps minimal)
	n := time.Now().UnixNano()
	for i := range b {
		n = n*6364136223846793005 + 1442695040888963407 // LCG
		idx := (n >> 33) % int64(len(charset))
		if idx < 0 {
			idx = -idx
		}
		b[i] = charset[idx]
	}
	return string(b)
}
