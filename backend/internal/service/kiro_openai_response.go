package service

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// kiroOpenAIStreamResult holds OpenAI streaming result data.
type kiroOpenAIStreamResult struct {
	usage        *OpenAIUsage
	firstTokenMs *int

	// 流已提交后才发生的失败无法再改状态码，函数返回 (result, nil) 视为部分成功。
	// 这些字段把故障信息带回调用方落 ops，语义同 kiroStreamResult。
	inBandException   string
	upstreamRequestID string
	failedAfterCommit bool
}

// handleOpenAIStreamingResponse handles CW streaming response and converts to OpenAI SSE format.
func (s *KiroGatewayService) handleOpenAIStreamingResponse(
	c *gin.Context, resp *http.Response, startTime time.Time,
	originalModel string, inputTokens int,
	toolNameReverseMap map[string]string,
	cacheCreationTokens, cacheReadTokens int,
	thinkingEnabled bool,
) (*kiroOpenAIStreamResult, error) {
	return s.handleKiroOpenAIStreamingResponse(c, resp, startTime, originalModel, inputTokens, toolNameReverseMap, cacheCreationTokens, cacheReadTokens, thinkingEnabled, nil, false)
}

func (s *KiroGatewayService) handleKiroOpenAIStreamingResponse(
	c *gin.Context, resp *http.Response, startTime time.Time,
	originalModel string, inputTokens int,
	toolNameReverseMap map[string]string,
	cacheCreationTokens, cacheReadTokens int,
	thinkingEnabled bool,
	responses *kiroResponsesMode,
	includeUsage bool,
) (*kiroOpenAIStreamResult, error) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming not supported")
	}

	messageID := "chatcmpl-" + uuid.New().String()[:24]

	// CW EventStream parser
	parser := kiro.NewAwsEventStreamParser(messageID, originalModel)
	parser.SetThinkingEnabled(thinkingEnabled)

	// Select only the wire format; upstream parsing and failure handling stay shared.
	chatConverter := kiro.NewOpenAIStreamConverter(messageID, originalModel, inputTokens)
	chatConverter.SetIncludeUsage(includeUsage)
	var converter kiroOpenAIEventConverter = chatConverter
	if responses != nil {
		responsesConverter := kiro.NewKiroResponsesConverter("resp_"+uuid.NewString(), originalModel, inputTokens)
		responsesConverter.SetClientTools(responses.clientTools)
		converter = responsesConverter
	}
	converter.SetCacheTokens(cacheCreationTokens, cacheReadTokens)
	initialEvent := converter.BuildInitialEvent()

	upstreamRequestID := resp.Header.Get("x-amzn-requestid")
	if upstreamRequestID != "" {
		c.Header("x-request-id", upstreamRequestID)
	}

	// 延迟提交 SSE 信封。
	//
	// 之前这里在函数入口就写 c.Status(200) + initial chunk，于是从第一行代码起
	// 就再也改不了状态码、也无法换账号重试 —— 上游 in-band exception / 空流
	// 只能以 SSE error 事件的形式吐给客户端，而 HTTP 状态仍是 200。
	// 现在推迟到"确实有内容要写"或达到 kiroOpenAICommitDeadline 才提交，
	// 在此之前的失败都能走正常的分类 + failover 路径。
	streamCommitted := false
	commitStream := func() error {
		if streamCommitted {
			return nil
		}
		c.Header("Content-Type", "text/event-stream; charset=utf-8")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")
		c.Status(http.StatusOK)

		if _, err := c.Writer.Write([]byte(initialEvent)); err != nil {
			return err
		}
		streamCommitted = true
		flusher.Flush()
		return nil
	}

	var firstTokenMs *int
	streamFailed := false
	sawRenderableEvent := false
	inBandException := ""

	// 提交前的失败要包装成分类故障，让上层能换账号重试
	preCommitFailure := func(reason string, statusCode int, message string) error {
		failure := newKiroTransportFailure(reason, statusCode, message)
		failure.RequestID = upstreamRequestID
		return failure.failoverError()
	}

	// 后台读取上游，主循环用 select 以便在无数据时也能触发提交上限
	type openAIStreamChunk struct {
		data []byte
		err  error
	}
	chunkCh := make(chan openAIStreamChunk, 32)
	done := make(chan struct{})
	defer close(done)

	go func() {
		defer close(chunkCh)
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				select {
				case chunkCh <- openAIStreamChunk{data: data}:
				case <-done:
					return
				}
			}
			if err != nil {
				select {
				case chunkCh <- openAIStreamChunk{err: err}:
				case <-done:
				}
				return
			}
		}
	}()

	commitDeadline := kiroStreamCommitDeadline
	if s.openAICommitDeadline > 0 {
		commitDeadline = s.openAICommitDeadline
	}
	commitTimer := time.NewTimer(commitDeadline)
	defer commitTimer.Stop()

readLoop:
	for !streamFailed {
		select {
		case <-c.Request.Context().Done():
			if !streamCommitted {
				return nil, c.Request.Context().Err()
			}
			streamFailed = true
			break readLoop

		case <-commitTimer.C:
			// 上游迟迟不发第一帧：先提交信封保住连接，放弃 failover 机会
			if !streamCommitted {
				log.Printf("[kiro-OpenAI] commit deadline reached before first frame: request_id=%s", upstreamRequestID)
				if err := commitStream(); err != nil {
					return nil, err
				}
			}

		case chunk, ok := <-chunkCh:
			if !ok {
				break readLoop
			}
			if chunk.err != nil {
				if chunk.err != io.EOF {
					log.Printf("[kiro-OpenAI] stream read error: request_id=%s error=%v", upstreamRequestID, chunk.err)
					if !streamCommitted {
						return nil, preCommitFailure("kiro_stream_read_error", http.StatusBadGateway, chunk.err.Error())
					}
					if _, err := c.Writer.Write([]byte(converter.BuildErrorEvent("upstream_read_error", sanitizeKiroClientErrorMessage(chunk.err.Error())))); err != nil {
						return nil, err
					}
					flusher.Flush()
					streamFailed = true
				}
				break readLoop
			}

			events := parser.Process(chunk.data)
			for _, e := range events {
				if isRenderableKiroStreamEvent(e) {
					sawRenderableEvent = true
				}
				// Restore tool names
				if e.Type == kiro.EventContentBlockStart && e.BlockType.Kind == kiro.BlockToolUse {
					if toolNameReverseMap != nil {
						if original, ok := toolNameReverseMap[e.BlockType.ToolName]; ok {
							e.BlockType.ToolName = original
						}
					}
				}

				if e.Type == kiro.EventError {
					inBandException = e.ErrorType
					log.Printf("[kiro-OpenAI] in-band exception: type=%s request_id=%s committed=%v retryable=%v message=%s",
						e.ErrorType, upstreamRequestID, streamCommitted,
						isRetryableKiroException(e.ErrorType), e.ErrorMessage)
					// 只有可重试的 exception（限流/过载）才在提交前 failover；
					// 确定性错误（订阅不支持等）换账号无意义，应透传上游原因。
					if !streamCommitted && isRetryableKiroException(e.ErrorType) {
						return nil, newKiroInBandFailure(e.ErrorType, e.ErrorMessage, upstreamRequestID).failoverError()
					}
					e.ErrorType = sanitizeKiroClientErrorMessage(e.ErrorType)
					e.ErrorMessage = sanitizeKiroClientErrorMessage(e.ErrorMessage)
				}
				sseStr := converter.ConvertEvent(e)
				if sseStr == "" {
					continue
				}

				// 有真实内容要写了，此时才提交信封
				if err := commitStream(); err != nil {
					return nil, err
				}

				// Track first token time
				if firstTokenMs == nil && (e.Type == kiro.EventTextDelta || e.Type == kiro.EventToolUseInputDelta) {
					ms := int(time.Since(startTime).Milliseconds())
					firstTokenMs = &ms
				}

				if _, err := c.Writer.Write([]byte(sseStr)); err != nil {
					return nil, err
				}
				if e.Type == kiro.EventError {
					streamFailed = true
					break
				}
			}
			if streamCommitted {
				flusher.Flush()
			}
		}
	}

	// Process remaining events
	if !streamFailed {
		finalEvents := parser.Finish()
		for _, e := range finalEvents {
			if isRenderableKiroStreamEvent(e) {
				sawRenderableEvent = true
			}
			if e.Type == kiro.EventContentBlockStart && e.BlockType.Kind == kiro.BlockToolUse {
				if toolNameReverseMap != nil {
					if original, ok := toolNameReverseMap[e.BlockType.ToolName]; ok {
						e.BlockType.ToolName = original
					}
				}
			}
			if e.Type == kiro.EventError {
				if inBandException == "" {
					inBandException = e.ErrorType
				}
				log.Printf("[kiro-OpenAI] in-band exception (finish): type=%s request_id=%s committed=%v message=%s",
					e.ErrorType, upstreamRequestID, streamCommitted, e.ErrorMessage)
				if !streamCommitted && isRetryableKiroException(e.ErrorType) {
					return nil, newKiroInBandFailure(e.ErrorType, e.ErrorMessage, upstreamRequestID).failoverError()
				}
				e.ErrorType = sanitizeKiroClientErrorMessage(e.ErrorType)
				e.ErrorMessage = sanitizeKiroClientErrorMessage(e.ErrorMessage)
			}
			sseStr := converter.ConvertEvent(e)
			if sseStr != "" {
				if err := commitStream(); err != nil {
					return nil, err
				}
				if _, err := c.Writer.Write([]byte(sseStr)); err != nil {
					return nil, err
				}
			}
			if e.Type == kiro.EventError {
				streamFailed = true
				break
			}
		}
	}
	if !streamFailed && !sawRenderableEvent {
		log.Printf("[kiro-OpenAI] empty stream: request_id=%s committed=%v exception=%s",
			upstreamRequestID, streamCommitted, inBandException)
		// 上游 HTTP 200 但零可渲染内容 —— A 类。未提交时可以换账号重试，
		// 且必须带 requestID（上游侧记录的是 200，这是唯一对单凭据）。
		if !streamCommitted {
			failure := newKiroEmptyFailure("kiro_empty_stream", upstreamRequestID)
			if inBandException != "" {
				failure = newKiroInBandFailure(inBandException, "", upstreamRequestID)
				failure.Reason = "kiro_empty_stream"
			}
			return nil, failure.failoverError()
		}
		if _, err := c.Writer.Write([]byte(converter.BuildErrorEvent("upstream_empty_stream", "kiro_empty_stream"))); err != nil {
			return nil, err
		}
		streamFailed = true
	}

	// Send final events (finish_reason + optional usage + [DONE])
	if !streamFailed {
		// 正常收尾也要确保信封已提交（例如上游只回了 usage 帧的边界情况）
		if err := commitStream(); err != nil {
			return nil, err
		}
		if _, err := c.Writer.Write([]byte(converter.BuildFinalEvent())); err != nil {
			return nil, err
		}
	}
	if streamCommitted {
		flusher.Flush()
	}

	outputTokens := converter.TotalOutputTokens()
	usage := &OpenAIUsage{
		InputTokens:              inputTokens,
		OutputTokens:             outputTokens,
		CacheCreationInputTokens: cacheCreationTokens,
		CacheReadInputTokens:     cacheReadTokens,
	}

	return &kiroOpenAIStreamResult{
		usage:             usage,
		firstTokenMs:      firstTokenMs,
		inBandException:   inBandException,
		upstreamRequestID: upstreamRequestID,
		failedAfterCommit: streamFailed,
	}, nil
}

// handleOpenAINonStreamingResponse handles CW response and converts to OpenAI JSON format.
func (s *KiroGatewayService) handleOpenAINonStreamingResponse(
	c *gin.Context, resp *http.Response,
	originalModel string, inputTokens int,
	toolNameReverseMap map[string]string,
	cacheCreationTokens, cacheReadTokens int,
	thinkingEnabled bool,
	initialResponseTimeout time.Duration,
) (*kiroOpenAIStreamResult, error) {
	return s.handleKiroOpenAINonStreamingResponse(c, resp, originalModel, inputTokens, toolNameReverseMap, cacheCreationTokens, cacheReadTokens, thinkingEnabled, initialResponseTimeout, nil)
}

func (s *KiroGatewayService) handleKiroOpenAINonStreamingResponse(
	c *gin.Context, resp *http.Response,
	originalModel string, inputTokens int,
	toolNameReverseMap map[string]string,
	cacheCreationTokens, cacheReadTokens int,
	thinkingEnabled bool,
	initialResponseTimeout time.Duration,
	responses *kiroResponsesMode,
) (*kiroOpenAIStreamResult, error) {
	messageID := "chatcmpl-" + uuid.New().String()[:24]

	// Read full response body
	respBody, err := readKiroNonStreamingBodyWithTimeout(c, resp, initialResponseTimeout)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	// Parse CW response through the event stream parser
	parser := kiro.NewAwsEventStreamParser(messageID, originalModel)
	parser.SetThinkingEnabled(thinkingEnabled)
	events := parser.Process(respBody)
	events = append(events, parser.Finish()...)
	upstreamRequestID := resp.Header.Get("x-amzn-requestid")
	if upstreamRequestID != "" {
		c.Header("x-request-id", upstreamRequestID)
	}
	for _, event := range events {
		if event.Type != kiro.EventError {
			continue
		}
		// A 类：上游 HTTP 200 + in-band exception。按 exception 类型映射状态码
		// （限流应为 429，不是 502），并带上 requestID 以便和上游对单。
		log.Printf("Kiro in-band exception (openai non-stream): type=%s request_id=%s message=%s",
			event.ErrorType, upstreamRequestID, event.ErrorMessage)
		return nil, newKiroInBandFailure(event.ErrorType, event.ErrorMessage, upstreamRequestID).failoverError()
	}

	if responses != nil {
		converter := kiro.NewKiroResponsesConverter("resp_"+uuid.NewString(), originalModel, inputTokens)
		converter.SetClientTools(responses.clientTools)
		converter.SetCacheTokens(cacheCreationTokens, cacheReadTokens)
		renderable := false
		for _, event := range events {
			if isRenderableKiroStreamEvent(event) {
				renderable = true
			}
			if event.Type == kiro.EventContentBlockStart && event.BlockType.Kind == kiro.BlockToolUse {
				if name, ok := toolNameReverseMap[event.BlockType.ToolName]; ok {
					event.BlockType.ToolName = name
				}
			}
			converter.ConvertEvent(event)
		}
		if !renderable {
			return nil, newKiroEmptyFailure("kiro_empty_response", upstreamRequestID).failoverError()
		}
		converter.BuildFinalEvent()
		c.JSON(http.StatusOK, converter.Response())
		return &kiroOpenAIStreamResult{usage: &OpenAIUsage{
			InputTokens: inputTokens, OutputTokens: converter.TotalOutputTokens(),
			CacheCreationInputTokens: cacheCreationTokens, CacheReadInputTokens: cacheReadTokens,
		}}, nil
	}

	// Collect text and tool calls from events
	var text string
	var toolCalls []kiro.ToolCallData
	var currentToolName string
	var currentToolID string
	var currentToolInput string
	outputTokens := 0

	for _, e := range events {
		switch e.Type {
		case kiro.EventTextDelta:
			text += e.Text
			outputTokens += (len(e.Text) + 3) / 4
		case kiro.EventContentBlockStart:
			if e.BlockType.Kind == kiro.BlockToolUse {
				toolName := e.BlockType.ToolName
				if toolNameReverseMap != nil {
					if original, ok := toolNameReverseMap[toolName]; ok {
						toolName = original
					}
				}
				currentToolName = toolName
				currentToolID = e.BlockType.ToolID
				currentToolInput = ""
			}
		case kiro.EventToolUseInputDelta:
			currentToolInput += e.PartialJSON
			outputTokens += (len(e.PartialJSON) + 3) / 4
		case kiro.EventContentBlockStop:
			if currentToolID != "" {
				toolCalls = append(toolCalls, kiro.ToolCallData{
					ID:           currentToolID,
					Name:         currentToolName,
					ArgumentsRaw: currentToolInput,
				})
				currentToolID = ""
				currentToolName = ""
				currentToolInput = ""
			}
		}
	}
	if text == "" && len(toolCalls) == 0 {
		return nil, newKiroEmptyFailure("kiro_empty_response", upstreamRequestID).failoverError()
	}

	// Build CompleteResponse for the non-stream builder
	completeResp := &kiro.CompleteResponse{
		Text:      text,
		ToolCalls: toolCalls,
	}

	result := kiro.BuildOpenAINonStreamResponse(messageID, originalModel, inputTokens, outputTokens, completeResp, cacheCreationTokens, cacheReadTokens)
	if result == nil {
		return nil, fmt.Errorf("failed to build non-stream response")
	}

	c.JSON(http.StatusOK, result)

	usage := &OpenAIUsage{
		InputTokens:              inputTokens,
		OutputTokens:             outputTokens,
		CacheCreationInputTokens: cacheCreationTokens,
		CacheReadInputTokens:     cacheReadTokens,
	}

	return &kiroOpenAIStreamResult{
		usage: usage,
	}, nil
}

// writeOpenAIError writes an OpenAI-format error response and returns an error for the caller.
func (s *KiroGatewayService) writeOpenAIError(c *gin.Context, status int, errType, message string) error {
	c.JSON(status, gin.H{
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
	return fmt.Errorf("%s: %s", errType, message)
}

// Both public OpenAI protocols consume the same native Kiro events.
type kiroOpenAIEventConverter interface {
	SetCacheTokens(int, int)
	BuildInitialEvent() string
	ConvertEvent(kiro.StreamEvent) string
	BuildFinalEvent() string
	BuildErrorEvent(string, string) string
	TotalOutputTokens() int
}
