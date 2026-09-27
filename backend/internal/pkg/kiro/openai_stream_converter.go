// Package kiro provides OpenAI SSE stream conversion for Kiro/CodeWhisperer responses.
// This converts CW StreamEvents to OpenAI chat.completion.chunk SSE format.
package kiro

import (
	"encoding/json"
	"fmt"
	"strings"
)

// OpenAIStreamConverter converts CW StreamEvents to OpenAI chat.completion.chunk SSE events.
type OpenAIStreamConverter struct {
	messageID    string
	model        string
	inputTokens  int
	finishReason string
	includeUsage bool

	// State tracking
	sawToolUse        bool
	totalOutputTokens int
	toolCallIndex     int
	activeToolID      string
	inTextBlock       bool

	// Cache tokens
	cacheCreationTokens int
	cacheReadTokens     int
}

// NewOpenAIStreamConverter creates a new OpenAI stream converter.
func NewOpenAIStreamConverter(messageID, model string, inputTokens int) *OpenAIStreamConverter {
	return &OpenAIStreamConverter{
		messageID:     messageID,
		model:         model,
		inputTokens:   inputTokens,
		toolCallIndex: -1,
	}
}

// SetCacheTokens sets cache token counts for usage reporting.
func (c *OpenAIStreamConverter) SetCacheTokens(creation, read int) {
	c.cacheCreationTokens = creation
	c.cacheReadTokens = read
}

func (c *OpenAIStreamConverter) SetUsage(input, output, cacheCreation, cacheRead int) {
	c.inputTokens = input
	c.totalOutputTokens = output
	c.cacheCreationTokens = cacheCreation
	c.cacheReadTokens = cacheRead
}

// SetIncludeUsage controls the optional final usage chunk in Chat Completions streams.
func (c *OpenAIStreamConverter) SetIncludeUsage(include bool) {
	c.includeUsage = include
}

func (c *OpenAIStreamConverter) SetFinishReason(reason string) {
	switch reason {
	case "tool_use":
		c.finishReason = "tool_calls"
	case "max_tokens", "model_context_window_exceeded":
		c.finishReason = "length"
	case "refusal":
		c.finishReason = "content_filter"
	default:
		c.finishReason = "stop"
	}
}

// SawToolUse returns whether any tool use was seen.
func (c *OpenAIStreamConverter) SawToolUse() bool {
	return c.sawToolUse
}

// TotalOutputTokens returns the estimated output token count.
func (c *OpenAIStreamConverter) TotalOutputTokens() int {
	return c.totalOutputTokens
}

// ConvertEvent converts a CW StreamEvent to OpenAI SSE string(s).
// Returns empty string if the event doesn't produce OpenAI output.
func (c *OpenAIStreamConverter) ConvertEvent(e StreamEvent) string {
	switch e.Type {
	case EventContentBlockStart:
		return c.handleBlockStart(e)
	case EventTextDelta:
		return c.handleTextDelta(e)
	case EventToolUseInputDelta:
		return c.handleToolInputDelta(e)
	case EventContentBlockStop:
		return c.handleBlockStop(e)
	case EventThinkingDelta:
		// Skip thinking blocks for OpenAI format
		return ""
	case EventError:
		return c.BuildErrorEvent(e.ErrorType, e.ErrorMessage)
	default:
		return ""
	}
}

// BuildInitialEvent builds the initial SSE chunk with role: assistant.
func (c *OpenAIStreamConverter) BuildInitialEvent() string {
	chunk := c.buildChunk(map[string]any{
		"role":    "assistant",
		"content": "",
	}, nil)
	return formatOpenAISSE(chunk)
}

// BuildFinalEvent builds the final SSE events, with usage when requested.
func (c *OpenAIStreamConverter) BuildFinalEvent() string {
	var sb strings.Builder

	// Finish reason
	finishReason := c.finishReason
	if finishReason == "" {
		finishReason = "stop"
	}
	if c.sawToolUse && c.finishReason == "" {
		finishReason = "tool_calls"
	}

	// Finish chunk
	finishChunk := c.buildChunk(map[string]any{}, &finishReason)
	sb.WriteString(formatOpenAISSE(finishChunk))

	if c.includeUsage {
		// Usage chunk (separate chunk with usage field)
		usageChunk := map[string]any{
			"id":      c.messageID,
			"object":  "chat.completion.chunk",
			"created": 0,
			"model":   c.model,
			"choices": []any{},
			"usage": map[string]any{
				"prompt_tokens":     c.inputTokens,
				"completion_tokens": c.totalOutputTokens,
				"total_tokens":      c.inputTokens + c.totalOutputTokens,
				"prompt_tokens_details": map[string]any{
					"cached_tokens":      c.cacheReadTokens,
					"cache_write_tokens": c.cacheCreationTokens,
				},
				"cache_creation_input_tokens": c.cacheCreationTokens,
				"cache_read_input_tokens":     c.cacheReadTokens,
			},
		}
		sb.WriteString(formatOpenAISSE(usageChunk))
	}

	// [DONE]
	sb.WriteString("data: [DONE]\n\n")

	return sb.String()
}

func (c *OpenAIStreamConverter) BuildErrorEvent(errorType, message string) string {
	if errorType == "" {
		errorType = "upstream_error"
	}
	if message == "" {
		message = "Upstream stream failed"
	}
	return formatOpenAISSE(map[string]any{
		"error": map[string]any{
			"type":    errorType,
			"message": message,
		},
	}) + "data: [DONE]\n\n"
}

// BuildOpenAINonStreamResponse builds a complete non-streaming OpenAI response from a CompleteResponse.
func BuildOpenAINonStreamResponse(messageID, model string, inputTokens, outputTokens int, resp *CompleteResponse, cacheCreation, cacheRead int) map[string]any {
	if resp == nil {
		return nil
	}

	var toolCalls []map[string]any
	for i, tc := range resp.ToolCalls {
		toolCalls = append(toolCalls, map[string]any{
			"id":   tc.ID,
			"type": "function",
			"function": map[string]any{
				"name":      tc.Name,
				"arguments": tc.ArgumentsRaw,
			},
			"index": i,
		})
	}

	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}

	message := map[string]any{
		"role": "assistant",
	}
	if resp.Text != "" {
		message["content"] = resp.Text
	} else {
		message["content"] = nil
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}

	result := map[string]any{
		"id":      messageID,
		"object":  "chat.completion",
		"created": 0,
		"model":   model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"message":       message,
				"finish_reason": finishReason,
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     inputTokens,
			"completion_tokens": outputTokens,
			"total_tokens":      inputTokens + outputTokens,
			"prompt_tokens_details": map[string]any{
				"cached_tokens":      cacheRead,
				"cache_write_tokens": cacheCreation,
			},
			"cache_creation_input_tokens": cacheCreation,
			"cache_read_input_tokens":     cacheRead,
		},
	}

	return result
}

// handleBlockStart handles content block start events.
func (c *OpenAIStreamConverter) handleBlockStart(e StreamEvent) string {
	switch e.BlockType.Kind {
	case BlockText:
		c.inTextBlock = true
		return ""
	case BlockToolUse:
		c.sawToolUse = true
		c.toolCallIndex++
		c.activeToolID = e.BlockType.ToolID

		// Send tool call start with function name
		chunk := c.buildChunk(map[string]any{
			"tool_calls": []any{
				map[string]any{
					"index": c.toolCallIndex,
					"id":    e.BlockType.ToolID,
					"type":  "function",
					"function": map[string]any{
						"name":      e.BlockType.ToolName,
						"arguments": "",
					},
				},
			},
		}, nil)
		return formatOpenAISSE(chunk)
	case BlockThinking:
		// Skip thinking blocks
		return ""
	}
	return ""
}

// handleTextDelta handles text delta events.
func (c *OpenAIStreamConverter) handleTextDelta(e StreamEvent) string {
	c.totalOutputTokens += (len(e.Text) + 3) / 4

	chunk := c.buildChunk(map[string]any{
		"content": e.Text,
	}, nil)
	return formatOpenAISSE(chunk)
}

// handleToolInputDelta handles tool input JSON delta events.
func (c *OpenAIStreamConverter) handleToolInputDelta(e StreamEvent) string {
	c.totalOutputTokens += (len(e.PartialJSON) + 3) / 4

	chunk := c.buildChunk(map[string]any{
		"tool_calls": []any{
			map[string]any{
				"index": c.toolCallIndex,
				"function": map[string]any{
					"arguments": e.PartialJSON,
				},
			},
		},
	}, nil)
	return formatOpenAISSE(chunk)
}

// handleBlockStop handles content block stop events.
func (c *OpenAIStreamConverter) handleBlockStop(e StreamEvent) string {
	c.inTextBlock = false
	return ""
}

// buildChunk builds an OpenAI chat.completion.chunk object.
func (c *OpenAIStreamConverter) buildChunk(delta map[string]any, finishReason *string) map[string]any {
	choice := map[string]any{
		"index": 0,
		"delta": delta,
	}
	if finishReason != nil {
		choice["finish_reason"] = *finishReason
	}

	chunk := map[string]any{
		"id":      c.messageID,
		"object":  "chat.completion.chunk",
		"created": 0,
		"model":   c.model,
		"choices": []any{choice},
	}
	if c.includeUsage {
		chunk["usage"] = nil
	}
	return chunk
}

// formatOpenAISSE formats a chunk as an SSE data line.
func formatOpenAISSE(chunk map[string]any) string {
	data, err := json.Marshal(chunk)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("data: %s\n\n", data)
}
