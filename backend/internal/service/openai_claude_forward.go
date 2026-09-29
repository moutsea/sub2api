package service

import (
	"bufio"
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
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
)

// ForwardAsClaudeMessages handles Claude Messages API requests routed to OpenAI accounts.
// For APIKey accounts: Claude → Chat Completions → upstream → Claude
// For OAuth accounts: Claude → Responses API → upstream (chatgpt.com) → Claude
func (s *OpenAIGatewayService) ForwardAsClaudeMessages(ctx context.Context, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
	// High-frequency entry log is intentionally muted to reduce noise.
	// log.Printf("[openai-claude-compat] account=%s(%d) type=%s platform=%s", account.Name, account.ID, account.Type, account.Platform)
	if account.Type == AccountTypeOAuth {
		return s.forwardClaudeViaResponsesAPI(ctx, c, account, body)
	}
	return s.forwardClaudeViaChatCompletions(ctx, c, account, body)
}

// forwardClaudeViaChatCompletions handles Claude → Chat Completions API path (for APIKey accounts).
func (s *OpenAIGatewayService) forwardClaudeViaChatCompletions(ctx context.Context, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
	startTime := time.Now()
	prefix := fmt.Sprintf("[openai-claude-compat] account=%s(%d) type=apikey", account.Name, account.ID)
	estimatedInputTokens := estimateClaudeRequestInputTokens(body)

	// 1. Convert Claude request → OpenAI Chat Completions format
	openaiBody, originalModel, wantStream, err := kiro.ConvertClaudeToOpenAI(body)
	if err != nil {
		return nil, fmt.Errorf("convert claude to openai: %w", err)
	}
	grokCacheKey := ""
	if account.IsGrok() {
		grokModel := resolveGrokRequestModel(account, originalModel)
		if modelErr := grokTextModelValidationError(grokModel); modelErr != nil {
			writeClaudeError(c, http.StatusBadRequest, "invalid_request_error", modelErr.Error())
			return nil, modelErr
		}
		openaiBody, err = patchGrokClaudeCompatModel(openaiBody, grokModel)
		if err != nil {
			return nil, fmt.Errorf("patch Grok model: %w", err)
		}
		openaiBody, err = applyGrokPromptCacheKey(openaiBody, c, body, grokModel)
		if err != nil {
			return nil, fmt.Errorf("apply Grok prompt cache key: %w", err)
		}
		var grokRequest map[string]any
		if err := json.Unmarshal(openaiBody, &grokRequest); err != nil {
			return nil, fmt.Errorf("parse Grok chat body: %w", err)
		}
		if apiKey := getAPIKeyFromGinContext(c); apiKey != nil && apiKey.ID > 0 {
			if cacheKey, ok := grokRequest["prompt_cache_key"].(string); ok {
				grokCacheKey = strings.TrimSpace(cacheKey)
			}
		}
		delete(grokRequest, "prompt_cache_key")
		normalizeGrokChatReasoning(grokRequest, grokModel)
		openaiBody, err = json.Marshal(grokRequest)
		if err != nil {
			return nil, fmt.Errorf("serialize Grok chat body: %w", err)
		}
	}
	// High-frequency model mapping log is intentionally muted to reduce noise.
	// log.Printf("%s model=%s→%s stream=%v", prefix, originalModel, kiro.GetOpenAIModelID(originalModel), wantStream)

	// 2. Get access token
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}

	// 3. Build upstream request to OpenAI Chat Completions
	requestCtx, releaseRequestCtx := ctx, func() {}
	if account.IsGrok() && !wantStream {
		requestCtx, releaseRequestCtx = grokUpstreamContext(ctx, false)
	}
	defer releaseRequestCtx()
	openaiBody, err = prepareOpenAIEnvironmentContext(account, openaiBody, "messages")
	if err != nil {
		return nil, err
	}
	upstreamReq, err := s.buildChatCompletionsRequest(requestCtx, c, account, openaiBody, token, grokCacheKey)
	if err != nil {
		return nil, err
	}

	// 4. Send request and handle response
	return s.doClaudeCompatRequest(ctx, c, account, upstreamReq, openaiBody, originalModel, wantStream, startTime, prefix, estimatedInputTokens, false)
}

// forwardClaudeViaResponsesAPI handles Claude → Responses API path (for OAuth accounts).
func (s *OpenAIGatewayService) forwardClaudeViaResponsesAPI(ctx context.Context, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
	startTime := time.Now()
	prefix := fmt.Sprintf("[openai-claude-compat] account=%s(%d) type=oauth", account.Name, account.ID)
	estimatedInputTokens := estimateClaudeRequestInputTokens(body)
	wantStream := true

	// 1. Convert Claude request → OpenAI Responses API format
	responsesBody, originalModel, err := kiro.ConvertClaudeToResponses(body)
	if err != nil {
		return nil, fmt.Errorf("convert claude to responses: %w", err)
	}
	if account.IsGrok() {
		grokModel := resolveGrokRequestModel(account, originalModel)
		if modelErr := grokTextModelValidationError(grokModel); modelErr != nil {
			writeClaudeError(c, http.StatusBadRequest, "invalid_request_error", modelErr.Error())
			return nil, modelErr
		}
		responsesBody, err = patchGrokResponsesBody(responsesBody, grokModel)
		if err != nil {
			return nil, fmt.Errorf("patch Grok model: %w", err)
		}
		// Claude Messages defaults to a non-streaming response. The OpenAI
		// OAuth path intentionally remains streaming-only, while xAI's public
		// Responses API supports both modes.
		wantStream = claudeMessagesRequestWantsStream(body)
		var grokRequest map[string]any
		if err := json.Unmarshal(responsesBody, &grokRequest); err != nil {
			return nil, fmt.Errorf("parse Grok responses body: %w", err)
		}
		grokRequest["stream"] = wantStream
		normalizeGrokResponsesReasoning(grokRequest, grokModel)
		responsesBody, err = json.Marshal(grokRequest)
		if err != nil {
			return nil, fmt.Errorf("serialize Grok responses body: %w", err)
		}
		responsesBody, err = applyGrokPromptCacheKey(responsesBody, c, body, grokModel)
		if err != nil {
			return nil, fmt.Errorf("apply Grok prompt cache key: %w", err)
		}
	}

	// 2. Apply the OpenAI OAuth transform only for OpenAI accounts. Grok speaks
	// the public Responses protocol and must retain its xAI model and payload.
	//    Preserve the original Claude system prompt — do NOT let applyCodexOAuthTransform
	//    overwrite instructions with OpenCode/Codex headers.
	var reqBody map[string]any
	if err := json.Unmarshal(responsesBody, &reqBody); err != nil {
		return nil, fmt.Errorf("parse responses body: %w", err)
	}
	promptCacheKey := ""
	if account.Platform == PlatformOpenAI {
		origInstructions, _ := reqBody["instructions"].(string)
		codexResult := applyCodexOAuthTransform(reqBody)
		if origInstructions != "" {
			reqBody["instructions"] = origInstructions
		}
		ensureResponsesReasoning(reqBody)
		promptCacheKey = codexResult.PromptCacheKey
	}
	// Re-serialize after transform
	responsesBody, err = json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("serialize responses body: %w", err)
	}

	// High-frequency model mapping log is intentionally muted to reduce noise.
	// log.Printf("%s model=%s→%s (responses API)", prefix, originalModel, kiro.GetOpenAIModelID(originalModel))

	// 3. Get access token
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}

	// 4. Build upstream request using standard OAuth path (chatgpt.com)
	responsesBody, err = prepareOpenAIEnvironmentContext(account, responsesBody, "input")
	if err != nil {
		return nil, err
	}
	upstreamReq, err := s.buildUpstreamRequest(ctx, c, account, responsesBody, token, wantStream, promptCacheKey, false)
	if err != nil {
		return nil, err
	}

	// 5. Send request and handle response (Responses API SSE → Claude SSE)
	return s.doClaudeCompatRequest(ctx, c, account, upstreamReq, responsesBody, originalModel, wantStream, startTime, prefix, estimatedInputTokens, true)
}

func claudeMessagesRequestWantsStream(body []byte) bool {
	var request struct {
		Stream *bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &request); err != nil || request.Stream == nil {
		return false
	}
	return *request.Stream
}

func patchGrokClaudeCompatModel(body []byte, model string) ([]byte, error) {
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, err
	}
	if strings.TrimSpace(model) != "" {
		request["model"] = model
	}
	return json.Marshal(request)
}

// doClaudeCompatRequest sends the upstream request and handles the response, converting back to Claude format.
// isResponsesAPI indicates whether the upstream returns Responses API SSE (true) or Chat Completions SSE (false).
func (s *OpenAIGatewayService) doClaudeCompatRequest(
	ctx context.Context, c *gin.Context, account *Account,
	upstreamReq *http.Request, upstreamBody []byte,
	originalModel string, wantStream bool,
	startTime time.Time, prefix string,
	estimatedInputTokens int,
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

	if account.IsGrok() {
		s.applyGrokRequestJitter(ctx)
	}
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	if err == nil && isResponsesAPI && account.IsGrok() && resp != nil && resp.Body != nil &&
		(resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity) {
		statusCode := resp.StatusCode
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()
		if isGrokReplayDecodeError(statusCode, responseBody) {
			if retryBody, changed := stripGrokReplayEncryptedContent(upstreamBody); changed {
				upstreamBody = retryBody
				retryToken, _, tokenErr := s.GetAccessToken(ctx, account)
				if tokenErr != nil {
					return nil, tokenErr
				}
				requestCtx, releaseRequestCtx := detachUpstreamContext(ctx)
				retryReq, buildErr := buildGrokResponsesRequest(requestCtx, c, account, upstreamBody, retryToken, account.GetGrokBaseURL())
				releaseRequestCtx()
				if buildErr != nil {
					return nil, fmt.Errorf("build Grok replay retry request: %w", buildErr)
				}
				s.applyGrokRequestJitter(ctx)
				if retryResp, retryErr := s.httpUpstream.Do(retryReq, proxyURL, account.ID, account.Concurrency); retryErr == nil {
					if retryResp == nil || retryResp.Body == nil {
						return nil, errors.New("Grok replay retry returned an empty response")
					}
					resp = retryResp
				} else {
					return nil, retryErr
				}
			} else {
				resp.Body = io.NopCloser(bytes.NewReader(responseBody))
			}
		} else {
			resp.Body = io.NopCloser(bytes.NewReader(responseBody))
		}
	}
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
	if resp == nil || resp.Body == nil {
		err := errors.New("upstream request returned an empty response")
		writeClaudeError(c, http.StatusBadGateway, "api_error", "Upstream request failed")
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	// Handle error responses
	if resp.StatusCode >= 400 {
		if account.IsGrok() {
			s.updateGrokUsageSnapshot(ctx, account.ID, xai.ParseQuotaHeaders(resp.Header, resp.StatusCode))
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(respBody))
			if s.shouldFailoverGrokUpstreamError(resp.StatusCode, respBody) {
				if s.rateLimitService != nil {
					s.rateLimitService.HandleUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody)
				}
				return nil, &UpstreamFailoverError{StatusCode: resp.StatusCode, Message: sanitizeUpstreamErrorMessage(extractUpstreamErrorMessage(respBody))}
			}
		} else if s.shouldFailoverUpstreamError(resp.StatusCode) {
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
	if account.IsGrok() {
		s.updateGrokUsageSnapshot(ctx, account.ID, xai.ParseQuotaHeaders(resp.Header, resp.StatusCode))
	}

	// Handle success response
	var inputTokens, outputTokens int
	var cacheCreationInputTokens, cacheReadInputTokens int
	var firstTokenMs *int

	if isResponsesAPI {
		if wantStream {
			if account.IsGrok() {
				maxLineSize := defaultMaxLineSize
				if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
					maxLineSize = s.cfg.Gateway.MaxLineSize
				}
				resp.Body = newGrokResponsesPingFilterBody(resp.Body, account, maxLineSize)
			}
			result, err := s.handleClaudeCompatResponsesStream(resp, c, startTime, originalModel, estimatedInputTokens)
			if err != nil {
				return nil, err
			}
			inputTokens = result.inputTokens
			outputTokens = result.outputTokens
			cacheCreationInputTokens = result.cacheCreationInputTokens
			cacheReadInputTokens = result.cacheReadInputTokens
			firstTokenMs = result.firstTokenMs
		} else {
			result, err := s.handleClaudeCompatResponsesNonStreamResponse(resp, c, originalModel)
			if err != nil {
				return nil, err
			}
			inputTokens = result.inputTokens
			outputTokens = result.outputTokens
			cacheCreationInputTokens = result.cacheCreationInputTokens
			cacheReadInputTokens = result.cacheReadInputTokens
		}
	} else if wantStream {
		result, err := s.handleClaudeCompatStreamResponse(resp, c, startTime, originalModel, estimatedInputTokens)
		if err != nil {
			return nil, err
		}
		inputTokens = result.inputTokens
		outputTokens = result.outputTokens
		cacheCreationInputTokens = result.cacheCreationInputTokens
		cacheReadInputTokens = result.cacheReadInputTokens
		firstTokenMs = result.firstTokenMs
	} else {
		result, err := s.handleClaudeCompatNonStreamResponse(resp, c, originalModel)
		if err != nil {
			return nil, err
		}
		inputTokens = result.inputTokens
		outputTokens = result.outputTokens
		cacheCreationInputTokens = result.cacheCreationInputTokens
		cacheReadInputTokens = result.cacheReadInputTokens
	}
	if inputTokens == 0 && estimatedInputTokens > 0 {
		inputTokens = estimatedInputTokens
	}

	log.Printf("%s status=ok input=%d output=%d duration=%v", prefix, inputTokens, outputTokens, time.Since(startTime))

	resultModel := originalModel
	if account.IsGrok() {
		var upstreamRequest map[string]any
		if err := json.Unmarshal(upstreamBody, &upstreamRequest); err == nil {
			if model, ok := upstreamRequest["model"].(string); ok && strings.TrimSpace(model) != "" {
				resultModel = strings.TrimSpace(model)
			}
		}
	}

	return &ForwardResult{
		RequestID: firstNonEmptyGrokHeader(resp.Header, "x-request-id", "xai-request-id"),
		Usage: ClaudeUsage{
			InputTokens:              inputTokens,
			OutputTokens:             outputTokens,
			CacheCreationInputTokens: cacheCreationInputTokens,
			CacheReadInputTokens:     cacheReadInputTokens,
		},
		Model:        resultModel,
		Stream:       wantStream,
		Duration:     time.Since(startTime),
		FirstTokenMs: firstTokenMs,
	}, nil
}

// handleClaudeCompatResponsesNonStreamResponse converts a regular Responses
// API JSON response into a Claude Messages JSON response. This is used for
// Grok OAuth requests that explicitly disable streaming.
func (s *OpenAIGatewayService) handleClaudeCompatResponsesNonStreamResponse(resp *http.Response, c *gin.Context, originalModel string) (*claudeCompatResult, error) {
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	ccBody, ccUsage := convertResponsesJSONToCC(respBody, originalModel, resp.Header.Get("x-request-id"))
	if ccUsage == nil {
		return nil, fmt.Errorf("convert response: invalid Responses API response")
	}
	claudeBody, usage, err := kiro.ConvertOpenAIResponseToClaude(ccBody, originalModel)
	if err != nil {
		return nil, fmt.Errorf("convert response to Claude: %w", err)
	}
	if s.cfg != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.cfg.Security.ResponseHeaders)
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", claudeBody)
	result := &claudeCompatResult{
		inputTokens:              usage.InputTokens,
		outputTokens:             usage.OutputTokens,
		cacheCreationInputTokens: usage.CacheCreationInputTokens,
		cacheReadInputTokens:     usage.CacheReadInputTokens,
	}
	return result, nil
}

// claudeCompatResult holds parsed usage from response handling.
type claudeCompatResult struct {
	inputTokens              int
	outputTokens             int
	cacheCreationInputTokens int
	cacheReadInputTokens     int
	firstTokenMs             *int
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
		result.cacheCreationInputTokens = usage.CacheCreationInputTokens
		result.cacheReadInputTokens = usage.CacheReadInputTokens
	}
	return result, nil
}

// handleClaudeCompatStreamResponse reads OpenAI Chat Completions SSE stream, converts to Claude SSE.
func (s *OpenAIGatewayService) handleClaudeCompatStreamResponse(resp *http.Response, c *gin.Context, startTime time.Time, originalModel string, estimatedInputTokens int) (*claudeCompatResult, error) {
	messageID := fmt.Sprintf("msg_%s", generateShortID())

	converter := kiro.NewClaudeStreamConverter(originalModel, messageID)
	converter.SetInputTokens(estimatedInputTokens)

	// Set SSE headers
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")

	// Send message_start
	_, _ = c.Writer.WriteString(converter.BuildMessageStart())
	c.Writer.Flush()

	var firstTokenMs *int
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096, 256*1024), 256*1024)

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
	if scanErr := scanner.Err(); scanErr != nil {
		writeClaudeStreamError(c, "api_error", "Failed to read upstream stream")
		return &claudeCompatResult{
			inputTokens:              converter.InputTokens(),
			outputTokens:             converter.OutputTokens(),
			cacheCreationInputTokens: converter.CacheCreationInputTokens(),
			cacheReadInputTokens:     converter.CacheReadInputTokens(),
			firstTokenMs:             firstTokenMs,
		}, fmt.Errorf("read upstream stream: %w", scanErr)
	}

	// Send message_stop
	_, _ = c.Writer.WriteString(converter.BuildMessageStop())
	c.Writer.Flush()

	return &claudeCompatResult{
		inputTokens:              converter.InputTokens(),
		outputTokens:             converter.OutputTokens(),
		cacheCreationInputTokens: converter.CacheCreationInputTokens(),
		cacheReadInputTokens:     converter.CacheReadInputTokens(),
		firstTokenMs:             firstTokenMs,
	}, nil
}

// handleClaudeCompatResponsesStream reads OpenAI Responses API SSE stream, converts to Claude SSE.
func (s *OpenAIGatewayService) handleClaudeCompatResponsesStream(resp *http.Response, c *gin.Context, startTime time.Time, originalModel string, estimatedInputTokens int) (*claudeCompatResult, error) {
	messageID := fmt.Sprintf("msg_%s", generateShortID())

	converter := kiro.NewResponsesStreamConverter(originalModel, messageID)
	converter.SetInputTokens(estimatedInputTokens)

	// Set SSE headers
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")

	// Send message_start
	_, _ = c.Writer.WriteString(converter.BuildMessageStart())
	c.Writer.Flush()

	var firstTokenMs *int
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096, 256*1024), 256*1024)

	// Responses API SSE format: "event: xxx\ndata: {...}\n\n"
	var currentEventType string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

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
		if data == "" {
			currentEventType = ""
			continue
		}
		if data == "[DONE]" {
			break
		}

		// Most Responses providers send both event: and data.type, but the
		// latter is sufficient and is common in proxy-normalized SSE streams.
		// Prefer the payload type when present and fall back to event: otherwise.
		eventType := currentEventType
		var payload map[string]any
		if json.Unmarshal([]byte(data), &payload) == nil {
			if payloadType, ok := payload["type"].(string); ok && strings.TrimSpace(payloadType) != "" {
				eventType = strings.TrimSpace(payloadType)
			}
		}

		events := converter.ConvertResponsesEvent(eventType, []byte(data))
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
	if scanErr := scanner.Err(); scanErr != nil {
		writeClaudeStreamError(c, "api_error", "Failed to read upstream stream")
		return &claudeCompatResult{
			inputTokens:              converter.InputTokens(),
			outputTokens:             converter.OutputTokens(),
			cacheCreationInputTokens: converter.CacheCreationInputTokens(),
			cacheReadInputTokens:     converter.CacheReadInputTokens(),
			firstTokenMs:             firstTokenMs,
		}, fmt.Errorf("read upstream stream: %w", scanErr)
	}

	// Send message_stop
	_, _ = c.Writer.WriteString(converter.BuildMessageStop())
	c.Writer.Flush()

	return &claudeCompatResult{
		inputTokens:              converter.InputTokens(),
		outputTokens:             converter.OutputTokens(),
		cacheCreationInputTokens: converter.CacheCreationInputTokens(),
		cacheReadInputTokens:     converter.CacheReadInputTokens(),
		firstTokenMs:             firstTokenMs,
	}, nil
}

func estimateClaudeRequestInputTokens(body []byte) int {
	claudeReq, err := kiro.ParseClaudeRequestFromJSON(body)
	if err != nil || claudeReq == nil {
		return 0
	}
	return kiro.EstimateInputTokens(claudeReq)
}

func ensureResponsesReasoning(reqBody map[string]any) bool {
	if reqBody == nil {
		return false
	}
	reasoning, ok := reqBody["reasoning"].(map[string]any)
	if !ok || reasoning == nil {
		reqBody["reasoning"] = map[string]any{
			"effort":  "xhigh",
			"summary": "auto",
		}
		return true
	}
	modified := false
	if effort, _ := reasoning["effort"].(string); strings.TrimSpace(effort) == "" {
		reasoning["effort"] = "xhigh"
		modified = true
	}
	if summary, _ := reasoning["summary"].(string); strings.TrimSpace(summary) == "" {
		reasoning["summary"] = "auto"
		modified = true
	}
	return modified
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

func writeClaudeStreamError(c *gin.Context, errType, message string) {
	if c == nil || c.Writer == nil {
		return
	}
	data, err := json.Marshal(map[string]any{
		"type": "error",
		"error": map[string]string{
			"type":    errType,
			"message": message,
		},
	})
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", data)
	if flusher, ok := c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
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
