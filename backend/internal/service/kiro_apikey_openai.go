package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const defaultKiroAPIKeyOpenAIMaxTokens = 4096

func kiroAPIKeyOpenAIDefaultMaxTokens(req *kiro.ClaudeRequest) int {
	if kiro.IsThinkingConfigEnabled(req) {
		return kiro.KiroFixedMaxTokens
	}
	return defaultKiroAPIKeyOpenAIMaxTokens
}

func (s *KiroGatewayService) forwardKiroAPIKeyChatCompletions(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	claudeReq *kiro.ClaudeRequest,
	originalModel string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	prefix := fmt.Sprintf("[kiro-apikey-OpenAI] account=%s", account.Name)
	if claudeReq.MaxTokens <= 0 {
		claudeReq.MaxTokens = kiroAPIKeyOpenAIDefaultMaxTokens(claudeReq)
	}
	requestBody, err := json.Marshal(claudeReq)
	if err != nil {
		return nil, fmt.Errorf("marshal claude request: %w", err)
	}
	requestBody = stripUnsupportedClaudeFields(requestBody)
	requestBody = enforceCacheControlLimit(requestBody)

	if s.tokenProvider == nil {
		return nil, fmt.Errorf("kiro token provider not configured")
	}
	apiKey, err := s.tokenProvider.GetAccessToken(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("get api_key failed: %w", err)
	}
	requestBody = claude.EnsureMetadataUserID(requestBody, apiKey)

	baseURL := strings.TrimSpace(account.GetKiroBaseURL())
	if baseURL == "" {
		return nil, fmt.Errorf("base_url is empty for apikey account %d", account.ID)
	}
	targetURL := strings.TrimRight(baseURL, "/") + "/v1/messages"
	sessionID := kiro.GenerateContentBasedConversationID(kiro.ExtractAPIKey(c), claudeReq.Model, claudeReq.Messages)
	anthropicBeta := c.GetHeader("anthropic-beta")
	fingerprint := claude.NewRequestHeaders()
	req, err := s.buildClaudeAPIHTTPRequest(
		ctx,
		targetURL,
		apiKey,
		requestBody,
		sessionID,
		anthropicBeta,
		0,
		fingerprint,
	)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	proxyURL := s.resolveProxyURL(ctx, account, false)
	s.applyRequestJitter(ctx)
	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return nil, &UpstreamFailoverError{StatusCode: http.StatusBadGateway, Message: sanitizeKiroClientErrorMessage(err.Error())}
	}
	resp = s.retryClaudeAPISignatureError(
		ctx,
		resp,
		account,
		targetURL,
		apiKey,
		requestBody,
		sessionID,
		anthropicBeta,
		proxyURL,
		prefix,
		fingerprint,
	)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		s.handleUpstreamError(ctx, prefix, account, resp.StatusCode, resp.Header, respBody)
		message := sanitizeKiroClientErrorMessage(extractKiroErrorMessage(respBody))
		if resp.StatusCode == http.StatusBadRequest || s.shouldFailoverUpstreamError(resp.StatusCode) {
			return nil, &UpstreamFailoverError{StatusCode: resp.StatusCode, Message: message}
		}
		if message == "" {
			message = "Upstream request failed"
		}
		return nil, s.writeOpenAIError(c, resp.StatusCode, "upstream_error", message)
	}

	requestID := resp.Header.Get("x-request-id")
	if requestID == "" {
		requestID = resp.Header.Get("request-id")
	}
	if requestID != "" {
		c.Header("x-request-id", requestID)
	}

	inputTokens := kiro.EstimateInputTokens(claudeReq)
	var usage *OpenAIUsage
	var firstTokenMs *int
	if claudeReq.Stream {
		usage, firstTokenMs, err = s.handleClaudeAPIAsOpenAIStream(c, resp, originalModel, inputTokens, startTime)
	} else {
		usage, firstTokenMs, err = s.handleClaudeAPIAsOpenAIResponse(c, resp, originalModel, inputTokens, startTime)
	}
	if err != nil {
		return nil, err
	}

	return &OpenAIForwardResult{
		RequestID:    requestID,
		Usage:        *usage,
		Model:        originalModel,
		Stream:       claudeReq.Stream,
		Duration:     time.Since(startTime),
		FirstTokenMs: firstTokenMs,
	}, nil
}

func (s *KiroGatewayService) handleClaudeAPIAsOpenAIResponse(
	c *gin.Context,
	resp *http.Response,
	model string,
	estimatedInputTokens int,
	startTime time.Time,
) (*OpenAIUsage, *int, error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, nil, fmt.Errorf("read response: %w", err)
	}
	complete, usage, stopReason, err := parseClaudeAPIResponse(body, estimatedInputTokens)
	if err != nil {
		return nil, nil, fmt.Errorf("parse claude response: %s", sanitizeKiroClientErrorMessage(err.Error()))
	}

	messageID := "chatcmpl-" + uuid.New().String()[:24]
	result := kiro.BuildOpenAINonStreamResponse(
		messageID,
		model,
		usage.InputTokens,
		usage.OutputTokens,
		complete,
		usage.CacheCreationInputTokens,
		usage.CacheReadInputTokens,
	)
	if choices, ok := result["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			choice["finish_reason"] = mapClaudeStopReasonToOpenAI(stopReason, len(complete.ToolCalls) > 0)
		}
	}
	c.JSON(http.StatusOK, result)
	firstTokenMs := int(time.Since(startTime).Milliseconds())
	return usage, &firstTokenMs, nil
}

func parseClaudeAPIResponse(body []byte, estimatedInputTokens int) (*kiro.CompleteResponse, *OpenAIUsage, string, error) {
	var payload struct {
		StopReason string            `json:"stop_reason"`
		Content    []json.RawMessage `json:"content"`
		Usage      map[string]any    `json:"usage"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, nil, "", err
	}

	complete := &kiro.CompleteResponse{}
	for _, rawBlock := range payload.Content {
		var block struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(rawBlock, &block); err != nil {
			return nil, nil, "", err
		}
		switch block.Type {
		case "text":
			complete.Text += block.Text
		case "tool_use":
			arguments := strings.TrimSpace(string(block.Input))
			if arguments == "" || arguments == "null" {
				arguments = "{}"
			}
			complete.ToolCalls = append(complete.ToolCalls, kiro.ToolCallData{
				ID:           block.ID,
				Name:         block.Name,
				ArgumentsRaw: arguments,
			})
		}
	}

	usage := &OpenAIUsage{InputTokens: estimatedInputTokens}
	mergeClaudeAPIUsage(usage, payload.Usage)
	if usage.OutputTokens <= 0 {
		usage.OutputTokens = estimateOpenAIOutputTokens(complete)
	}
	return complete, usage, payload.StopReason, nil
}

func (s *KiroGatewayService) handleClaudeAPIAsOpenAIStream(
	c *gin.Context,
	resp *http.Response,
	model string,
	estimatedInputTokens int,
	startTime time.Time,
) (*OpenAIUsage, *int, error) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return nil, nil, fmt.Errorf("streaming not supported")
	}
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	converter := kiro.NewOpenAIStreamConverter("chatcmpl-"+uuid.New().String()[:24], model, estimatedInputTokens)
	if _, err := c.Writer.Write([]byte(converter.BuildInitialEvent())); err != nil {
		return nil, nil, err
	}
	flusher.Flush()

	usage := &OpenAIUsage{InputTokens: estimatedInputTokens}
	var firstTokenMs *int
	reader := bufio.NewReader(resp.Body)
	dataLines := make([]string, 0, 1)
	streamFailed := false
	streamStopped := false
	type claudeAPIStreamLine struct {
		line string
		err  error
	}
	lines := make(chan claudeAPIStreamLine, 32)
	readerDone := make(chan struct{})
	defer close(readerDone)
	sendLine := func(line claudeAPIStreamLine) bool {
		select {
		case lines <- line:
			return true
		case <-readerDone:
			return false
		}
	}
	go func() {
		defer close(lines)
		for {
			line, readErr := reader.ReadString('\n')
			if len(line) > 0 {
				if !sendLine(claudeAPIStreamLine{line: line}) {
					return
				}
			}
			if readErr != nil {
				_ = sendLine(claudeAPIStreamLine{err: readErr})
				return
			}
		}
	}()

	keepaliveInterval := s.kiroStreamKeepaliveInterval()
	keepaliveTicker := time.NewTicker(keepaliveInterval)
	defer keepaliveTicker.Stop()
	lastDataAt := time.Now()

	flushData := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		data := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		if data == "[DONE]" {
			streamStopped = true
			return nil
		}
		events, stopReason, stopped, err := parseClaudeSSEData([]byte(data), usage)
		if err != nil {
			streamFailed = true
			_, _ = c.Writer.Write([]byte(converter.BuildErrorEvent("upstream_parse_error", sanitizeKiroClientErrorMessage(err.Error()))))
			flusher.Flush()
			return nil
		}
		if stopReason != "" {
			converter.SetFinishReason(stopReason)
		}
		streamStopped = streamStopped || stopped
		for _, event := range events {
			if firstTokenMs == nil && (event.Type == kiro.EventTextDelta || event.Type == kiro.EventToolUseInputDelta) {
				value := int(time.Since(startTime).Milliseconds())
				firstTokenMs = &value
			}
			if event.Type == kiro.EventError {
				event.ErrorType = sanitizeKiroClientErrorMessage(event.ErrorType)
				event.ErrorMessage = sanitizeKiroClientErrorMessage(event.ErrorMessage)
			}
			chunk := converter.ConvertEvent(event)
			if chunk == "" {
				continue
			}
			if _, err := c.Writer.Write([]byte(chunk)); err != nil {
				return err
			}
			if event.Type == kiro.EventError {
				streamFailed = true
			}
		}
		flusher.Flush()
		return nil
	}

streamLoop:
	for !streamFailed && !streamStopped {
		select {
		case streamLine, open := <-lines:
			if !open {
				break streamLoop
			}
			if streamLine.line != "" {
				lastDataAt = time.Now()
				line := strings.TrimRight(streamLine.line, "\r\n")
				switch {
				case line == "":
					if err := flushData(); err != nil {
						return nil, firstTokenMs, err
					}
				case strings.HasPrefix(line, "data:"):
					dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
				}
			}
			if streamLine.err != nil {
				if err := flushData(); err != nil {
					return nil, firstTokenMs, err
				}
				if streamLine.err == io.EOF && !streamFailed && !streamStopped {
					streamFailed = true
					_, _ = c.Writer.Write([]byte(converter.BuildErrorEvent("upstream_incomplete_stream", "Upstream stream ended before message_stop")))
					flusher.Flush()
				} else if streamLine.err != io.EOF && !streamFailed {
					streamFailed = true
					_, _ = c.Writer.Write([]byte(converter.BuildErrorEvent("upstream_read_error", sanitizeKiroClientErrorMessage(streamLine.err.Error()))))
					flusher.Flush()
				}
				break streamLoop
			}

		case <-keepaliveTicker.C:
			if time.Since(lastDataAt) < keepaliveInterval {
				continue
			}
			if _, err := c.Writer.Write([]byte(":\n\n")); err != nil {
				return nil, firstTokenMs, err
			}
			flusher.Flush()
		}
	}

	if streamFailed {
		return usage, firstTokenMs, nil
	}
	if usage.OutputTokens <= 0 {
		usage.OutputTokens = converter.TotalOutputTokens()
	}
	converter.SetUsage(
		usage.InputTokens,
		usage.OutputTokens,
		usage.CacheCreationInputTokens,
		usage.CacheReadInputTokens,
	)
	if _, err := c.Writer.Write([]byte(converter.BuildFinalEvent())); err != nil {
		return nil, firstTokenMs, err
	}
	flusher.Flush()
	return usage, firstTokenMs, nil
}

func parseClaudeSSEData(data []byte, usage *OpenAIUsage) ([]kiro.StreamEvent, string, bool, error) {
	var payload struct {
		Type    string `json:"type"`
		Index   uint32 `json:"index"`
		Message struct {
			Usage map[string]any `json:"usage"`
		} `json:"message"`
		ContentBlock struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"content_block"`
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Thinking    string `json:"thinking"`
			PartialJSON string `json:"partial_json"`
			StopReason  string `json:"stop_reason"`
		} `json:"delta"`
		Usage map[string]any `json:"usage"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, "", false, err
	}

	switch payload.Type {
	case "message_start":
		mergeClaudeAPIUsage(usage, payload.Message.Usage)
	case "content_block_start":
		switch payload.ContentBlock.Type {
		case "text":
			return []kiro.StreamEvent{{
				Type:      kiro.EventContentBlockStart,
				Index:     payload.Index,
				BlockType: kiro.ContentBlockType{Kind: kiro.BlockText},
			}}, "", false, nil
		case "thinking", "redacted_thinking":
			return []kiro.StreamEvent{{
				Type:      kiro.EventContentBlockStart,
				Index:     payload.Index,
				BlockType: kiro.ContentBlockType{Kind: kiro.BlockThinking},
			}}, "", false, nil
		case "tool_use":
			return []kiro.StreamEvent{{
				Type:  kiro.EventContentBlockStart,
				Index: payload.Index,
				BlockType: kiro.ContentBlockType{
					Kind:     kiro.BlockToolUse,
					ToolID:   payload.ContentBlock.ID,
					ToolName: payload.ContentBlock.Name,
				},
			}}, "", false, nil
		}
	case "content_block_delta":
		switch payload.Delta.Type {
		case "text_delta":
			return []kiro.StreamEvent{{Type: kiro.EventTextDelta, Index: payload.Index, Text: payload.Delta.Text}}, "", false, nil
		case "thinking_delta":
			return []kiro.StreamEvent{{Type: kiro.EventThinkingDelta, Index: payload.Index, Text: payload.Delta.Thinking}}, "", false, nil
		case "input_json_delta":
			return []kiro.StreamEvent{{Type: kiro.EventToolUseInputDelta, Index: payload.Index, PartialJSON: payload.Delta.PartialJSON}}, "", false, nil
		}
	case "content_block_stop":
		return []kiro.StreamEvent{{Type: kiro.EventContentBlockStop, Index: payload.Index}}, "", false, nil
	case "message_delta":
		mergeClaudeAPIUsage(usage, payload.Usage)
		return nil, payload.Delta.StopReason, false, nil
	case "message_stop":
		return nil, "", true, nil
	case "error":
		return []kiro.StreamEvent{{
			Type:         kiro.EventError,
			ErrorType:    payload.Error.Type,
			ErrorMessage: payload.Error.Message,
		}}, "", true, nil
	}
	return nil, "", false, nil
}

func mergeClaudeAPIUsage(target *OpenAIUsage, usage map[string]any) {
	if target == nil || usage == nil {
		return
	}
	setUsageValue := func(field *int, keys ...string) {
		for _, key := range keys {
			raw, exists := usage[key]
			if !exists {
				continue
			}
			if value, ok := usageIntFromAny(raw); ok && value >= 0 {
				*field = value
			}
			return
		}
	}
	setUsageValue(&target.InputTokens, "input_tokens", "inputTokens")
	setUsageValue(&target.OutputTokens, "output_tokens", "outputTokens")
	setUsageValue(&target.CacheCreationInputTokens, "cache_creation_input_tokens", "cacheCreationInputTokens")
	setUsageValue(&target.CacheReadInputTokens, "cache_read_input_tokens", "cacheReadInputTokens")
}

func estimateOpenAIOutputTokens(response *kiro.CompleteResponse) int {
	if response == nil {
		return 0
	}
	tokens := (len(response.Text) + 3) / 4
	for _, toolCall := range response.ToolCalls {
		tokens += (len(toolCall.ArgumentsRaw) + 3) / 4
	}
	return tokens
}

func mapClaudeStopReasonToOpenAI(stopReason string, sawToolUse bool) string {
	switch stopReason {
	case "tool_use":
		return "tool_calls"
	case "max_tokens", "model_context_window_exceeded":
		return "length"
	case "refusal":
		return "content_filter"
	case "end_turn", "stop_sequence":
		return "stop"
	default:
		if sawToolUse {
			return "tool_calls"
		}
		return "stop"
	}
}
