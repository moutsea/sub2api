package service

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// kiroOpenAIStreamResult holds OpenAI streaming result data.
type kiroOpenAIStreamResult struct {
	usage        *OpenAIUsage
	firstTokenMs *int
}

// handleOpenAIStreamingResponse handles CW streaming response and converts to OpenAI SSE format.
func (s *KiroGatewayService) handleOpenAIStreamingResponse(
	c *gin.Context, resp *http.Response, startTime time.Time,
	originalModel string, inputTokens int,
	toolNameReverseMap map[string]string,
	cacheCreationTokens, cacheReadTokens int,
	thinkingEnabled bool,
) (*kiroOpenAIStreamResult, error) {
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming not supported")
	}

	messageID := "chatcmpl-" + uuid.New().String()[:24]

	// CW EventStream parser
	parser := kiro.NewAwsEventStreamParser(messageID, originalModel)
	parser.SetThinkingEnabled(thinkingEnabled)

	// OpenAI stream converter
	converter := kiro.NewOpenAIStreamConverter(messageID, originalModel, inputTokens)
	converter.SetCacheTokens(cacheCreationTokens, cacheReadTokens)

	// Send initial chunk (role: assistant)
	if _, err := c.Writer.Write([]byte(converter.BuildInitialEvent())); err != nil {
		return nil, err
	}
	flusher.Flush()

	var firstTokenMs *int
	streamFailed := false
	sawRenderableEvent := false

	// Read and process stream
	buf := make([]byte, 4096)
	var lastReadAt int64
	atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())

	for !streamFailed {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())

			events := parser.Process(buf[:n])
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
					e.ErrorType = sanitizeKiroClientErrorMessage(e.ErrorType)
					e.ErrorMessage = sanitizeKiroClientErrorMessage(e.ErrorMessage)
				}
				sseStr := converter.ConvertEvent(e)
				if sseStr == "" {
					continue
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
			flusher.Flush()
		}

		if readErr != nil {
			if readErr != io.EOF {
				log.Printf("[kiro-OpenAI] stream read error: %v", readErr)
				if _, err := c.Writer.Write([]byte(converter.BuildErrorEvent("upstream_read_error", sanitizeKiroClientErrorMessage(readErr.Error())))); err != nil {
					return nil, err
				}
				flusher.Flush()
				streamFailed = true
			}
			break
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
				e.ErrorType = sanitizeKiroClientErrorMessage(e.ErrorType)
				e.ErrorMessage = sanitizeKiroClientErrorMessage(e.ErrorMessage)
			}
			sseStr := converter.ConvertEvent(e)
			if sseStr != "" {
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
		if _, err := c.Writer.Write([]byte(converter.BuildErrorEvent("upstream_empty_stream", "kiro_empty_stream"))); err != nil {
			return nil, err
		}
		streamFailed = true
	}

	// Send final events (finish_reason + usage + [DONE])
	if !streamFailed {
		if _, err := c.Writer.Write([]byte(converter.BuildFinalEvent())); err != nil {
			return nil, err
		}
	}
	flusher.Flush()

	outputTokens := converter.TotalOutputTokens()
	usage := &OpenAIUsage{
		InputTokens:              inputTokens,
		OutputTokens:             outputTokens,
		CacheCreationInputTokens: cacheCreationTokens,
		CacheReadInputTokens:     cacheReadTokens,
	}

	return &kiroOpenAIStreamResult{
		usage:        usage,
		firstTokenMs: firstTokenMs,
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
	for _, event := range events {
		if event.Type != kiro.EventError {
			continue
		}
		return nil, &UpstreamFailoverError{
			StatusCode: http.StatusBadGateway,
			Message:    sanitizeKiroClientErrorMessage(event.ErrorMessage),
		}
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
		return nil, kiroEmptyStreamFailover("kiro_empty_response")
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
