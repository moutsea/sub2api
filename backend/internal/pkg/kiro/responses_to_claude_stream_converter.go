// Package kiro provides OpenAI Responses API SSE → Claude Messages SSE conversion.
// Converts Responses API streaming events to Claude Messages streaming events.
package kiro

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// ResponsesStreamConverter converts OpenAI Responses API SSE events to Claude Messages SSE events.
type ResponsesStreamConverter struct {
	originalModel string
	messageID     string

	// State
	contentIndex             int
	inTextBlock              bool
	inThinkingBlock          bool
	toolCallMap              map[string]int // call_id -> claude content block index
	toolItemMap              map[string]int // item_id -> claude content block index
	toolOutputIndexMap       map[int]int    // output_index -> claude content block index
	openToolBlocks           map[int]struct{}
	toolBlockHasInput        map[int]bool
	outputTokens             int
	inputTokens              int
	cacheCreationInputTokens int
	cacheReadInputTokens     int
	sawToolUse               bool
	stopReason               string
	thinkingBuffer           strings.Builder
}

// NewResponsesStreamConverter creates a new Responses API → Claude converter.
func NewResponsesStreamConverter(originalModel, messageID string) *ResponsesStreamConverter {
	return &ResponsesStreamConverter{
		originalModel:      originalModel,
		messageID:          messageID,
		toolCallMap:        make(map[string]int),
		toolItemMap:        make(map[string]int),
		toolOutputIndexMap: make(map[int]int),
		openToolBlocks:     make(map[int]struct{}),
		toolBlockHasInput:  make(map[int]bool),
	}
}

// OutputTokens returns the accumulated output token count.
func (c *ResponsesStreamConverter) OutputTokens() int {
	return c.outputTokens
}

// InputTokens returns the input token count.
func (c *ResponsesStreamConverter) InputTokens() int {
	return c.inputTokens
}

// SetInputTokens sets initial input tokens for message_start usage.
func (c *ResponsesStreamConverter) SetInputTokens(tokens int) {
	c.inputTokens = tokens
}

// CacheCreationInputTokens returns cache_creation_input_tokens usage.
func (c *ResponsesStreamConverter) CacheCreationInputTokens() int {
	return c.cacheCreationInputTokens
}

// CacheReadInputTokens returns cache_read_input_tokens usage.
func (c *ResponsesStreamConverter) CacheReadInputTokens() int {
	return c.cacheReadInputTokens
}

// BuildMessageStart builds the initial message_start SSE event.
func (c *ResponsesStreamConverter) BuildMessageStart() string {
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

// BuildMessageStop builds the final message_stop event.
func (c *ResponsesStreamConverter) BuildMessageStop() string {
	// If no stop reason was set, send a default message_delta
	if c.stopReason == "" {
		c.stopReason = "end_turn"
	}
	return formatClaudeSSE(map[string]any{
		"type": "message_stop",
	})
}

// ConvertResponsesEvent converts a Responses API SSE event to Claude SSE event(s).
// eventType is the "event:" value, data is the "data:" JSON payload.
// Returns empty string if the event doesn't produce output.
func (c *ResponsesStreamConverter) ConvertResponsesEvent(eventType string, data []byte) string {
	switch eventType {
	case "response.output_text.delta":
		return c.handleTextDelta(data)
	case "response.output_text.done":
		return c.handleTextDone(data)
	case "response.reasoning.delta", "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
		return c.handleReasoningDelta(data)
	case "response.reasoning.done", "response.reasoning_text.done", "response.reasoning_summary_text.done":
		return c.handleReasoningDone()
	case "response.function_call_arguments.delta":
		return c.handleFunctionCallDelta(data)
	case "response.function_call_arguments.done":
		return c.handleFunctionCallDone(data)
	case "response.output_item.done":
		return c.handleOutputItemDone(data)
	case "response.output_item.added":
		return c.handleOutputItemAdded(data)
	case "response.completed", "response.done":
		return c.handleResponseCompleted(data)
	default:
		return ""
	}
}

func (c *ResponsesStreamConverter) handleTextDelta(data []byte) string {
	var ev map[string]any
	if err := json.Unmarshal(data, &ev); err != nil {
		return ""
	}
	delta, _ := ev["delta"].(string)
	if delta == "" {
		return ""
	}

	var result strings.Builder
	// Close thinking block if open
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
		"delta": map[string]any{"type": "text_delta", "text": delta},
	}))
	c.outputTokens += len(delta) >> 2
	return result.String()
}

func (c *ResponsesStreamConverter) handleTextDone(_ []byte) string {
	if c.inTextBlock {
		return c.closeCurrentBlock()
	}
	return ""
}

func (c *ResponsesStreamConverter) handleReasoningDelta(data []byte) string {
	var ev map[string]any
	if err := json.Unmarshal(data, &ev); err != nil {
		return ""
	}
	delta, _ := ev["delta"].(string)
	if delta == "" {
		return ""
	}

	var result strings.Builder
	if c.inTextBlock {
		result.WriteString(c.closeCurrentBlock())
	}
	if !c.inThinkingBlock {
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
		"delta": map[string]any{"type": "thinking_delta", "thinking": delta},
	}))
	c.thinkingBuffer.WriteString(delta)
	c.outputTokens += len(delta) >> 2
	return result.String()
}

func (c *ResponsesStreamConverter) handleReasoningDone() string {
	if c.inThinkingBlock {
		return c.closeThinkingBlock()
	}
	return ""
}

func (c *ResponsesStreamConverter) handleOutputItemAdded(data []byte) string {
	var ev map[string]any
	if err := json.Unmarshal(data, &ev); err != nil {
		return ""
	}
	item, _ := ev["item"].(map[string]any)
	if item == nil {
		return ""
	}
	itemType, _ := item["type"].(string)
	if itemType != "function_call" {
		return ""
	}

	// Close any open block
	var result strings.Builder
	result.WriteString(c.closeOpenNonToolBlock())
	callID, _ := item["call_id"].(string)
	itemID, _ := item["id"].(string)
	name, _ := item["name"].(string)
	blockIdx := c.contentIndex
	toolUseID := strings.TrimSpace(callID)
	if toolUseID == "" {
		toolUseID = strings.TrimSpace(itemID)
	}
	if toolUseID == "" {
		if evItemID, ok := ev["item_id"].(string); ok {
			toolUseID = strings.TrimSpace(evItemID)
			if itemID == "" {
				itemID = evItemID
			}
		}
	}
	if toolUseID == "" {
		toolUseID = fmt.Sprintf("toolu_%s_%d", c.messageID, blockIdx)
	}

	c.sawToolUse = true
	if callID != "" {
		c.toolCallMap[callID] = blockIdx
	}
	if itemID != "" {
		c.toolItemMap[itemID] = blockIdx
	}
	if outputIndex, ok := intFromAny(ev["output_index"]); ok {
		c.toolOutputIndexMap[outputIndex] = blockIdx
	}
	if outputIndex, ok := intFromAny(item["output_index"]); ok {
		c.toolOutputIndexMap[outputIndex] = blockIdx
	}
	c.openToolBlocks[blockIdx] = struct{}{}

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
	// Reserve next content index when opening tool blocks so multiple outstanding
	// tool calls won't reuse the same index before any done events arrive.
	c.contentIndex++
	return result.String()
}

func (c *ResponsesStreamConverter) handleFunctionCallDelta(data []byte) string {
	var ev map[string]any
	if err := json.Unmarshal(data, &ev); err != nil {
		return ""
	}
	delta, _ := ev["delta"].(string)
	if delta == "" {
		return ""
	}
	blockIdx, ok := c.resolveToolBlockIndex(ev)
	if !ok {
		return ""
	}

	result := formatClaudeSSE(map[string]any{
		"type":  "content_block_delta",
		"index": blockIdx,
		"delta": map[string]any{
			"type":         "input_json_delta",
			"partial_json": delta,
		},
	})
	c.toolBlockHasInput[blockIdx] = true
	c.outputTokens += len(delta) / 4
	return result
}

func (c *ResponsesStreamConverter) handleFunctionCallDone(data []byte) string {
	var ev map[string]any
	if err := json.Unmarshal(data, &ev); err != nil {
		return ""
	}
	return c.handleToolCallDoneEvent(ev)
}

func (c *ResponsesStreamConverter) handleOutputItemDone(data []byte) string {
	var ev map[string]any
	if err := json.Unmarshal(data, &ev); err != nil {
		return ""
	}
	item, _ := ev["item"].(map[string]any)
	if item == nil {
		return ""
	}
	itemType, _ := item["type"].(string)
	if itemType != "function_call" {
		return ""
	}
	if _, ok := ev["call_id"]; !ok {
		ev["call_id"] = item["call_id"]
	}
	if _, ok := ev["item_id"]; !ok {
		ev["item_id"] = item["id"]
	}
	if _, ok := ev["arguments"]; !ok {
		ev["arguments"] = item["arguments"]
	}
	return c.handleToolCallDoneEvent(ev)
}

func (c *ResponsesStreamConverter) handleToolCallDoneEvent(ev map[string]any) string {
	blockIdx, ok := c.resolveToolBlockIndex(ev)
	if !ok {
		return ""
	}

	var result strings.Builder
	arguments := argumentsToJSONString(ev["arguments"])
	if arguments != "" && !c.toolBlockHasInput[blockIdx] {
		result.WriteString(formatClaudeSSE(map[string]any{
			"type":  "content_block_delta",
			"index": blockIdx,
			"delta": map[string]any{
				"type":         "input_json_delta",
				"partial_json": arguments,
			},
		}))
		c.toolBlockHasInput[blockIdx] = true
		c.outputTokens += len(arguments) >> 2
	}

	result.WriteString(c.closeToolBlockByIndex(blockIdx))
	return result.String()
}

func (c *ResponsesStreamConverter) handleResponseCompleted(data []byte) string {
	var result strings.Builder

	// Close any open blocks
	result.WriteString(c.closeOpenNonToolBlock())
	// Close any still-open tool blocks.
	result.WriteString(c.closeAllOpenToolBlocks())

	// Extract usage from response.completed
	var ev map[string]any
	if err := json.Unmarshal(data, &ev); err == nil {
		if response, ok := ev["response"].(map[string]any); ok {
			if usage, ok := response["usage"].(map[string]any); ok {
				if v := jsonInt(usage, "input_tokens"); v > 0 {
					c.cacheCreationInputTokens = jsonInt(usage, "cache_creation_input_tokens")
					c.cacheReadInputTokens = extractCachedTokensFromUsageDetails(usage, "input_tokens_details", "prompt_tokens_details")
					c.inputTokens = normalizeClaudeInputTokens(v, c.cacheCreationInputTokens, c.cacheReadInputTokens)
				}
				if v := jsonInt(usage, "output_tokens"); v > 0 {
					c.outputTokens = v
				}
			}
			// Extract stop reason from response status
			if status, ok := response["status"].(string); ok {
				c.stopReason = mapResponsesStatus(status)
			}
		}
	}

	if c.stopReason == "" {
		c.stopReason = "end_turn"
	}
	// Keep max_tokens semantics from upstream status. Only remap end_turn to
	// tool_use when we actually emitted tool_use blocks.
	if c.sawToolUse && c.stopReason == "end_turn" {
		c.stopReason = "tool_use"
	}

	usage := map[string]any{
		"input_tokens":  c.inputTokens,
		"output_tokens": c.outputTokens,
	}
	addClaudeCacheUsageFields(usage, c.cacheCreationInputTokens, c.cacheReadInputTokens)

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

func (c *ResponsesStreamConverter) closeCurrentBlock() string {
	result := formatClaudeSSE(map[string]any{
		"type":  "content_block_stop",
		"index": c.contentIndex,
	})
	c.inTextBlock = false
	c.inThinkingBlock = false
	c.contentIndex++
	return result
}

func (c *ResponsesStreamConverter) closeThinkingBlock() string {
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

func (c *ResponsesStreamConverter) closeOpenNonToolBlock() string {
	if c.inThinkingBlock {
		return c.closeThinkingBlock()
	}
	if c.inTextBlock {
		return c.closeCurrentBlock()
	}
	return ""
}

func syntheticThinkingSignature(thinking string) string {
	sum := sha256.Sum256([]byte(thinking))
	return "proxy_sig_" + base64.RawURLEncoding.EncodeToString(sum[:])
}

func (c *ResponsesStreamConverter) resolveToolBlockIndex(ev map[string]any) (int, bool) {
	if callID, ok := ev["call_id"].(string); ok && strings.TrimSpace(callID) != "" {
		if blockIdx, found := c.toolCallMap[callID]; found {
			return blockIdx, true
		}
	}
	if itemID, ok := ev["item_id"].(string); ok && strings.TrimSpace(itemID) != "" {
		if blockIdx, found := c.toolItemMap[itemID]; found {
			return blockIdx, true
		}
	}
	if outputIndex, ok := intFromAny(ev["output_index"]); ok {
		if blockIdx, found := c.toolOutputIndexMap[outputIndex]; found {
			return blockIdx, true
		}
	}

	// response.output_item.done carries identifiers under "item".
	if item, ok := ev["item"].(map[string]any); ok && item != nil {
		if callID, ok := item["call_id"].(string); ok && strings.TrimSpace(callID) != "" {
			if blockIdx, found := c.toolCallMap[callID]; found {
				return blockIdx, true
			}
		}
		if itemID, ok := item["id"].(string); ok && strings.TrimSpace(itemID) != "" {
			if blockIdx, found := c.toolItemMap[itemID]; found {
				return blockIdx, true
			}
		}
		if outputIndex, ok := intFromAny(item["output_index"]); ok {
			if blockIdx, found := c.toolOutputIndexMap[outputIndex]; found {
				return blockIdx, true
			}
		}
	}

	// Conservative fallback: only use the sole open block when mapping signals are absent.
	if blockIdx, ok := c.singleOpenToolBlock(); ok {
		return blockIdx, true
	}
	return 0, false
}

func (c *ResponsesStreamConverter) singleOpenToolBlock() (int, bool) {
	if len(c.openToolBlocks) != 1 {
		return 0, false
	}
	for idx := range c.openToolBlocks {
		return idx, true
	}
	return 0, false
}

func (c *ResponsesStreamConverter) latestOpenToolBlock() (int, bool) {
	if len(c.openToolBlocks) == 0 {
		return 0, false
	}
	latest := -1
	for idx := range c.openToolBlocks {
		if idx > latest {
			latest = idx
		}
	}
	if latest < 0 {
		return 0, false
	}
	return latest, true
}

func (c *ResponsesStreamConverter) closeAllOpenToolBlocks() string {
	var result string
	for len(c.openToolBlocks) > 0 {
		blockIdx, ok := c.latestOpenToolBlock()
		if !ok {
			break
		}
		result += c.closeToolBlockByIndex(blockIdx)
	}
	return result
}

func (c *ResponsesStreamConverter) closeToolBlockByIndex(blockIdx int) string {
	if _, open := c.openToolBlocks[blockIdx]; !open {
		return ""
	}
	delete(c.openToolBlocks, blockIdx)
	delete(c.toolBlockHasInput, blockIdx)
	c.removeToolMappingsForBlock(blockIdx)

	result := formatClaudeSSE(map[string]any{
		"type":  "content_block_stop",
		"index": blockIdx,
	})
	return result
}

func (c *ResponsesStreamConverter) removeToolMappingsForBlock(blockIdx int) {
	for callID, idx := range c.toolCallMap {
		if idx == blockIdx {
			delete(c.toolCallMap, callID)
		}
	}
	for itemID, idx := range c.toolItemMap {
		if idx == blockIdx {
			delete(c.toolItemMap, itemID)
		}
	}
	for outputIndex, idx := range c.toolOutputIndexMap {
		if idx == blockIdx {
			delete(c.toolOutputIndexMap, outputIndex)
		}
	}
}

func intFromAny(v any) (int, bool) {
	switch value := v.(type) {
	case float64:
		return int(value), true
	case int:
		return value, true
	case json.Number:
		if parsed, err := value.Int64(); err == nil {
			return int(parsed), true
		}
	}
	return 0, false
}

func argumentsToJSONString(arguments any) string {
	switch value := arguments.(type) {
	case string:
		return strings.TrimSpace(value)
	case map[string]any:
		b, err := json.Marshal(value)
		if err != nil {
			return ""
		}
		return string(b)
	}
	return ""
}

// mapResponsesStatus maps Responses API status to Claude stop_reason.
func mapResponsesStatus(status string) string {
	switch status {
	case "completed":
		return "end_turn"
	case "incomplete":
		return "max_tokens"
	default:
		return "end_turn"
	}
}

// ParseResponsesSSELine parses a Responses API SSE line pair.
// Returns eventType and data. Expects to be called with accumulated event lines.
func ParseResponsesSSELine(line string) (eventType string, isEvent bool) {
	if strings.HasPrefix(line, "event:") {
		return strings.TrimSpace(strings.TrimPrefix(line, "event:")), true
	}
	return "", false
}
