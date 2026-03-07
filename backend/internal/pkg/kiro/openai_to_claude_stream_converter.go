// Package kiro provides OpenAI SSE stream → Claude SSE stream conversion.
// Converts OpenAI chat.completion.chunk events to Claude Messages streaming events.
package kiro

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ClaudeStreamConverter converts OpenAI SSE chunks to Claude Messages SSE events.
type ClaudeStreamConverter struct {
	originalModel string
	messageID     string

	// State
	contentIndex             int
	inTextBlock              bool
	inThinkingBlock          bool
	toolCallIndices          map[int]int // openai tool_call index → claude content block index
	toolUseIDs               map[int]string
	outputTokens             int
	inputTokens              int
	cacheCreationInputTokens int
	cacheReadInputTokens     int
	sawToolUse               bool
	messageStartSent         bool
	stopReason               string
	thinkingBuffer           strings.Builder
}

// NewClaudeStreamConverter creates a new converter.
func NewClaudeStreamConverter(originalModel, messageID string) *ClaudeStreamConverter {
	return &ClaudeStreamConverter{
		originalModel:   originalModel,
		messageID:       messageID,
		toolCallIndices: make(map[int]int),
		toolUseIDs:      make(map[int]string),
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

// CacheCreationInputTokens returns cache_creation_input_tokens usage.
func (c *ClaudeStreamConverter) CacheCreationInputTokens() int {
	return c.cacheCreationInputTokens
}

// CacheReadInputTokens returns cache_read_input_tokens usage.
func (c *ClaudeStreamConverter) CacheReadInputTokens() int {
	return c.cacheReadInputTokens
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

	var result strings.Builder

	// Handle delta
	delta, _ := choice["delta"].(map[string]any)
	if delta != nil {
		// reasoning delta → thinking block
		if reasoning, ok := delta["reasoning"].(map[string]any); ok {
			if content, ok := reasoning["content"].(string); ok && content != "" {
				result.WriteString(c.handleThinkingDelta(content))
			}
		}

		// content delta → text block
		if content, ok := delta["content"].(string); ok && content != "" {
			result.WriteString(c.handleTextDelta(content))
		}

		// tool_calls delta → tool_use blocks
		if toolCalls, ok := delta["tool_calls"].([]any); ok {
			for _, tc := range toolCalls {
				tcMap, ok := tc.(map[string]any)
				if !ok {
					continue
				}
				result.WriteString(c.handleToolCallDelta(tcMap))
			}
		}
	}

	// Handle finish_reason
	if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
		result.WriteString(c.handleFinishReason(fr))
	}

	// Handle usage in choice-bearing chunk
	if usage, ok := chunk["usage"].(map[string]any); ok {
		result.WriteString(c.handleUsageChunk(usage))
	}

	return result.String()
}

// BuildMessageStop builds the final message_stop event.
func (c *ClaudeStreamConverter) BuildMessageStop() string {
	event := map[string]any{
		"type": "message_stop",
	}
	return formatClaudeSSE(event)
}

func (c *ClaudeStreamConverter) handleThinkingDelta(content string) string {
	var result strings.Builder
	if !c.inThinkingBlock {
		// Close text block if open
		if c.inTextBlock {
			result.WriteString(c.closeCurrentBlock())
		}
		// Start thinking block
		result.WriteString(formatClaudeSSE(map[string]any{
			"type":          "content_block_start",
			"index":         c.contentIndex,
			"content_block": map[string]any{"type": "thinking", "thinking": ""},
		}))
		c.inThinkingBlock = true
	}
	result.WriteString(formatClaudeSSE(map[string]any{
		"type":  "content_block_delta",
		"index": c.contentIndex,
		"delta": map[string]any{"type": "thinking_delta", "thinking": content},
	}))
	c.thinkingBuffer.WriteString(content)
	c.outputTokens += len(content) >> 2
	return result.String()
}

func (c *ClaudeStreamConverter) handleTextDelta(content string) string {
	var result strings.Builder
	if c.inThinkingBlock {
		result.WriteString(c.closeThinkingBlock())
	}
	if !c.inTextBlock {
		result.WriteString(formatClaudeSSE(map[string]any{
			"type":          "content_block_start",
			"index":         c.contentIndex,
			"content_block": map[string]any{"type": "text", "text": ""},
		}))
		c.inTextBlock = true
	}
	result.WriteString(formatClaudeSSE(map[string]any{
		"type":  "content_block_delta",
		"index": c.contentIndex,
		"delta": map[string]any{"type": "text_delta", "text": content},
	}))
	c.outputTokens += len(content) >> 2
	return result.String()
}

func (c *ClaudeStreamConverter) handleToolCallDelta(tcMap map[string]any) string {
	var result strings.Builder
	idx := int(jsonFloat(tcMap, "index"))
	fn, _ := tcMap["function"].(map[string]any)

	c.sawToolUse = true

	// Check if this is a new tool call
	if _, exists := c.toolCallIndices[idx]; !exists {
		// Close any open block
		result.WriteString(c.closeOpenNonToolBlock())
		blockIdx := c.contentIndex
		c.toolCallIndices[idx] = blockIdx

		id, _ := tcMap["id"].(string)
		toolUseID := c.resolveToolUseID(idx, id)
		name := ""
		if fn != nil {
			name, _ = fn["name"].(string)
		}
		result.WriteString(formatClaudeSSE(map[string]any{
			"type":  "content_block_start",
			"index": blockIdx,
			"content_block": map[string]any{
				"type":  "tool_use",
				"id":    toolUseID,
				"name":  name,
				"input": map[string]any{},
			},
		}))
		// Reserve next content index for subsequent tool blocks.
		c.contentIndex++
	} else if id, _ := tcMap["id"].(string); strings.TrimSpace(id) != "" {
		// Keep a stable tool_use_id once emitted; only fill the cache if absent.
		c.resolveToolUseID(idx, id)
	}

	// Stream arguments as input_json_delta
	if fn != nil {
		if args, ok := fn["arguments"].(string); ok && args != "" {
			blockIdx := c.toolCallIndices[idx]
			result.WriteString(formatClaudeSSE(map[string]any{
				"type":  "content_block_delta",
				"index": blockIdx,
				"delta": map[string]any{
					"type":         "input_json_delta",
					"partial_json": args,
				},
			}))
			c.outputTokens += len(args) >> 2
		}
	}

	return result.String()
}

func (c *ClaudeStreamConverter) handleFinishReason(reason string) string {
	var result strings.Builder
	// Close any open block
	result.WriteString(c.closeOpenNonToolBlock())
	// Also close any open tool blocks
	for _, blockIdx := range c.sortedUniqueToolBlockIndices() {
		result.WriteString(formatClaudeSSE(map[string]any{
			"type":  "content_block_stop",
			"index": blockIdx,
		}))
	}
	c.toolCallIndices = make(map[int]int)
	c.toolUseIDs = make(map[int]string)

	c.stopReason = mapOpenAIFinishReason(reason)
	usage := map[string]any{
		"input_tokens":  c.inputTokens,
		"output_tokens": c.outputTokens,
	}
	addClaudeCacheUsageFields(usage, c.cacheCreationInputTokens, c.cacheReadInputTokens)

	// Send message_delta with stop_reason
	result.WriteString(formatClaudeSSE(map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   c.stopReason,
			"stop_sequence": nil,
		},
		"usage": usage,
	}))
	return result.String()
}

func (c *ClaudeStreamConverter) handleUsageChunk(usage map[string]any) string {
	if promptTokens := jsonInt(usage, "prompt_tokens"); promptTokens > 0 {
		c.cacheCreationInputTokens = jsonInt(usage, "cache_creation_input_tokens")
		c.cacheReadInputTokens = extractCachedTokensFromUsageDetails(usage, "prompt_tokens_details", "input_tokens_details")
		c.inputTokens = normalizeClaudeInputTokens(promptTokens, c.cacheCreationInputTokens, c.cacheReadInputTokens)
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

func (c *ClaudeStreamConverter) closeThinkingBlock() string {
	if !c.inThinkingBlock {
		return ""
	}
	signature := syntheticThinkingSignature(c.thinkingBuffer.String())
	c.thinkingBuffer.Reset()
	result := formatClaudeSSE(map[string]any{
		"type":  "content_block_delta",
		"index": c.contentIndex,
		"delta": map[string]any{
			"type":      "signature_delta",
			"signature": signature,
		},
	})
	result += c.closeCurrentBlock()
	return result
}

func (c *ClaudeStreamConverter) closeOpenNonToolBlock() string {
	if c.inThinkingBlock {
		return c.closeThinkingBlock()
	}
	if c.inTextBlock {
		return c.closeCurrentBlock()
	}
	return ""
}

func (c *ClaudeStreamConverter) resolveToolUseID(idx int, rawID string) string {
	if existing, ok := c.toolUseIDs[idx]; ok && strings.TrimSpace(existing) != "" {
		return existing
	}
	id := strings.TrimSpace(rawID)
	if id == "" {
		id = fmt.Sprintf("toolu_%s_%d", c.messageID, idx)
	}
	c.toolUseIDs[idx] = id
	return id
}

func (c *ClaudeStreamConverter) sortedUniqueToolBlockIndices() []int {
	if len(c.toolCallIndices) == 0 {
		return nil
	}
	seen := make(map[int]struct{}, len(c.toolCallIndices))
	for _, idx := range c.toolCallIndices {
		seen[idx] = struct{}{}
	}
	out := make([]int, 0, len(seen))
	for idx := range seen {
		out = append(out, idx)
	}
	sort.Ints(out)
	return out
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
