// Package kiro provides OpenAI SSE stream → Claude SSE stream conversion.
// Converts OpenAI chat.completion.chunk events to Claude Messages streaming events.
package kiro

import (
	"encoding/json"
	"fmt"
)

// ClaudeStreamConverter converts OpenAI SSE chunks to Claude Messages SSE events.
type ClaudeStreamConverter struct {
	originalModel string
	messageID     string

	// State
	contentIndex     int
	inTextBlock      bool
	inThinkingBlock  bool
	toolCallIndices  map[int]int // openai tool_call index → claude content block index
	outputTokens     int
	inputTokens      int
	sawToolUse       bool
	messageStartSent bool
	stopReason       string
}

// NewClaudeStreamConverter creates a new converter.
func NewClaudeStreamConverter(originalModel, messageID string) *ClaudeStreamConverter {
	return &ClaudeStreamConverter{
		originalModel:   originalModel,
		messageID:       messageID,
		toolCallIndices: make(map[int]int),
	}
}

// SetInputTokens sets the input token count for usage reporting.
func (c *ClaudeStreamConverter) SetInputTokens(tokens int) {
	c.inputTokens = tokens
}

// InputTokens returns the accumulated input token count.
func (c *ClaudeStreamConverter) InputTokens() int {
	return c.inputTokens
}

// OutputTokens returns the accumulated output token count.
func (c *ClaudeStreamConverter) OutputTokens() int {
	return c.outputTokens
}

// SawToolUse returns whether any tool use was seen.
func (c *ClaudeStreamConverter) SawToolUse() bool {
	return c.sawToolUse
}

// BuildMessageStart builds the initial message_start SSE event.
func (c *ClaudeStreamConverter) BuildMessageStart() string {
	c.messageStartSent = true
	event := map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":      c.messageID,
			"type":    "message",
			"role":    "assistant",
			"model":   c.originalModel,
			"content": []any{},
			"usage": map[string]any{
				"input_tokens":  c.inputTokens,
				"output_tokens": 0,
			},
		},
	}
	return formatClaudeSSE(event)
}

// ConvertChunk converts an OpenAI SSE data payload (JSON) to Claude SSE event(s).
// Returns empty string if the chunk doesn't produce output.
func (c *ClaudeStreamConverter) ConvertChunk(data []byte) string {
	var chunk map[string]any
	if err := json.Unmarshal(data, &chunk); err != nil {
		return ""
	}

	choices, _ := chunk["choices"].([]any)
	if len(choices) == 0 {
		// Could be a usage-only chunk
		if usage, ok := chunk["usage"].(map[string]any); ok {
			return c.handleUsageChunk(usage)
		}
		return ""
	}

	choice, _ := choices[0].(map[string]any)
	if choice == nil {
		return ""
	}

	var result string

	// Handle delta
	delta, _ := choice["delta"].(map[string]any)
	if delta != nil {
		// reasoning delta → thinking block
		if reasoning, ok := delta["reasoning"].(map[string]any); ok {
			if content, ok := reasoning["content"].(string); ok && content != "" {
				result += c.handleThinkingDelta(content)
			}
		}

		// content delta → text block
		if content, ok := delta["content"].(string); ok && content != "" {
			result += c.handleTextDelta(content)
		}

		// tool_calls delta → tool_use blocks
		if toolCalls, ok := delta["tool_calls"].([]any); ok {
			for _, tc := range toolCalls {
				tcMap, ok := tc.(map[string]any)
				if !ok {
					continue
				}
				result += c.handleToolCallDelta(tcMap)
			}
		}
	}

	// Handle finish_reason
	if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
		result += c.handleFinishReason(fr)
	}

	// Handle usage in choice-bearing chunk
	if usage, ok := chunk["usage"].(map[string]any); ok {
		result += c.handleUsageChunk(usage)
	}

	return result
}

// BuildMessageStop builds the final message_stop event.
func (c *ClaudeStreamConverter) BuildMessageStop() string {
	event := map[string]any{
		"type": "message_stop",
	}
	return formatClaudeSSE(event)
}

func (c *ClaudeStreamConverter) handleThinkingDelta(content string) string {
	var result string
	if !c.inThinkingBlock {
		// Close text block if open
		if c.inTextBlock {
			result += c.closeCurrentBlock()
		}
		// Start thinking block
		result += formatClaudeSSE(map[string]any{
			"type":          "content_block_start",
			"index":         c.contentIndex,
			"content_block": map[string]any{"type": "thinking", "thinking": ""},
		})
		c.inThinkingBlock = true
	}
	result += formatClaudeSSE(map[string]any{
		"type":  "content_block_delta",
		"index": c.contentIndex,
		"delta": map[string]any{"type": "thinking_delta", "thinking": content},
	})
	c.outputTokens += len(content) / 4
	return result
}

func (c *ClaudeStreamConverter) handleTextDelta(content string) string {
	var result string
	if c.inThinkingBlock {
		result += c.closeCurrentBlock()
	}
	if !c.inTextBlock {
		result += formatClaudeSSE(map[string]any{
			"type":          "content_block_start",
			"index":         c.contentIndex,
			"content_block": map[string]any{"type": "text", "text": ""},
		})
		c.inTextBlock = true
	}
	result += formatClaudeSSE(map[string]any{
		"type":  "content_block_delta",
		"index": c.contentIndex,
		"delta": map[string]any{"type": "text_delta", "text": content},
	})
	c.outputTokens += len(content) / 4
	return result
}

func (c *ClaudeStreamConverter) handleToolCallDelta(tcMap map[string]any) string {
	var result string
	idx := int(jsonFloat(tcMap, "index"))
	fn, _ := tcMap["function"].(map[string]any)

	c.sawToolUse = true

	// Check if this is a new tool call
	if _, exists := c.toolCallIndices[idx]; !exists {
		// Close any open block
		if c.inTextBlock || c.inThinkingBlock {
			result += c.closeCurrentBlock()
		}
		c.toolCallIndices[idx] = c.contentIndex

		id, _ := tcMap["id"].(string)
		name := ""
		if fn != nil {
			name, _ = fn["name"].(string)
		}
		result += formatClaudeSSE(map[string]any{
			"type":  "content_block_start",
			"index": c.contentIndex,
			"content_block": map[string]any{
				"type":  "tool_use",
				"id":    id,
				"name":  name,
				"input": map[string]any{},
			},
		})
	}

	// Stream arguments as input_json_delta
	if fn != nil {
		if args, ok := fn["arguments"].(string); ok && args != "" {
			blockIdx := c.toolCallIndices[idx]
			result += formatClaudeSSE(map[string]any{
				"type":  "content_block_delta",
				"index": blockIdx,
				"delta": map[string]any{
					"type":         "input_json_delta",
					"partial_json": args,
				},
			})
			c.outputTokens += len(args) / 4
		}
	}

	return result
}

func (c *ClaudeStreamConverter) handleFinishReason(reason string) string {
	var result string
	// Close any open block
	if c.inTextBlock || c.inThinkingBlock {
		result += c.closeCurrentBlock()
	}
	// Also close any open tool blocks
	for _, blockIdx := range c.toolCallIndices {
		result += formatClaudeSSE(map[string]any{
			"type":  "content_block_stop",
			"index": blockIdx,
		})
	}
	c.toolCallIndices = make(map[int]int)

	c.stopReason = mapOpenAIFinishReason(reason)

	// Send message_delta with stop_reason
	result += formatClaudeSSE(map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   c.stopReason,
			"stop_sequence": nil,
		},
		"usage": map[string]any{
			"input_tokens":  c.inputTokens,
			"output_tokens": c.outputTokens,
		},
	})
	return result
}

func (c *ClaudeStreamConverter) handleUsageChunk(usage map[string]any) string {
	if promptTokens := jsonInt(usage, "prompt_tokens"); promptTokens > 0 {
		c.inputTokens = promptTokens
	}
	if completionTokens := jsonInt(usage, "completion_tokens"); completionTokens > 0 {
		c.outputTokens = completionTokens
	}
	return ""
}

func (c *ClaudeStreamConverter) closeCurrentBlock() string {
	result := formatClaudeSSE(map[string]any{
		"type":  "content_block_stop",
		"index": c.contentIndex,
	})
	c.inTextBlock = false
	c.inThinkingBlock = false
	c.contentIndex++
	return result
}

// formatClaudeSSE formats a Claude event as an SSE data line.
func formatClaudeSSE(event map[string]any) string {
	eventType, _ := event["type"].(string)
	data, err := json.Marshal(event)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, data)
}

// jsonFloat extracts a float64 from a JSON map field.
func jsonFloat(m map[string]any, key string) float64 {
	if m == nil {
		return 0
	}
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0
}
