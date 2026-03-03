// Package kiro provides OpenAI Responses API SSE → Claude Messages SSE conversion.
// Converts Responses API streaming events to Claude Messages streaming events.
package kiro

import (
	"encoding/json"
	"strings"
)

// ResponsesStreamConverter converts OpenAI Responses API SSE events to Claude Messages SSE events.
type ResponsesStreamConverter struct {
	originalModel string
	messageID     string

	// State
	contentIndex       int
	inTextBlock        bool
	inThinkingBlock    bool
	toolCallMap        map[string]int // call_id -> claude content block index
	toolItemMap        map[string]int // item_id -> claude content block index
	toolOutputIndexMap map[int]int    // output_index -> claude content block index
	openToolBlocks     map[int]struct{}
	toolBlockHasInput  map[int]bool
	lastToolBlockIndex int
	outputTokens       int
	inputTokens        int
	sawToolUse         bool
	stopReason         string
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
		lastToolBlockIndex: -1,
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
				"input_tokens":  0,
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
	case "response.reasoning.delta":
		return c.handleReasoningDelta(data)
	case "response.reasoning.done":
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

	var result string
	// Close thinking block if open
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
		"delta": map[string]any{"type": "text_delta", "text": delta},
	})
	c.outputTokens += len(delta) / 4
	return result
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

	var result string
	if c.inTextBlock {
		result += c.closeCurrentBlock()
	}
	if !c.inThinkingBlock {
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
		"delta": map[string]any{"type": "thinking_delta", "thinking": delta},
	})
	c.outputTokens += len(delta) / 4
	return result
}

func (c *ResponsesStreamConverter) handleReasoningDone() string {
	if c.inThinkingBlock {
		return c.closeCurrentBlock()
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
	var result string
	if c.inTextBlock || c.inThinkingBlock {
		result += c.closeCurrentBlock()
	}
	// Defensive: close any stale tool block if upstream skipped corresponding done event.
	if len(c.openToolBlocks) > 0 {
		result += c.closeAllOpenToolBlocks()
	}

	callID, _ := item["call_id"].(string)
	itemID, _ := item["id"].(string)
	name, _ := item["name"].(string)
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

	c.sawToolUse = true
	blockIdx := c.contentIndex
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
	c.lastToolBlockIndex = blockIdx

	result += formatClaudeSSE(map[string]any{
		"type":  "content_block_start",
		"index": blockIdx,
		"content_block": map[string]any{
			"type":  "tool_use",
			"id":    toolUseID,
			"name":  name,
			"input": map[string]any{},
		},
	})
	return result
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

	var result string
	arguments := argumentsToJSONString(ev["arguments"])
	if arguments != "" && !c.toolBlockHasInput[blockIdx] {
		result += formatClaudeSSE(map[string]any{
			"type":  "content_block_delta",
			"index": blockIdx,
			"delta": map[string]any{
				"type":         "input_json_delta",
				"partial_json": arguments,
			},
		})
		c.toolBlockHasInput[blockIdx] = true
		c.outputTokens += len(arguments) / 4
	}

	result += c.closeToolBlockByIndex(blockIdx)
	return result
}

func (c *ResponsesStreamConverter) handleResponseCompleted(data []byte) string {
	var result string

	// Close any open blocks
	if c.inTextBlock || c.inThinkingBlock {
		result += c.closeCurrentBlock()
	}
	// Close any still-open tool blocks.
	result += c.closeAllOpenToolBlocks()

	// Extract usage from response.completed
	var ev map[string]any
	if err := json.Unmarshal(data, &ev); err == nil {
		if response, ok := ev["response"].(map[string]any); ok {
			if usage, ok := response["usage"].(map[string]any); ok {
				if v := jsonInt(usage, "input_tokens"); v > 0 {
					c.inputTokens = v
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

	if c.sawToolUse {
		c.stopReason = "tool_use"
	} else if c.stopReason == "" {
		c.stopReason = "end_turn"
	}

	result += formatClaudeSSE(map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   c.stopReason,
			"stop_sequence": nil,
		},
		"usage": map[string]any{
			"output_tokens": c.outputTokens,
		},
	})
	return result
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

	// Fallback to the latest open tool block to avoid index reuse/nesting when IDs are missing.
	if blockIdx, ok := c.latestOpenToolBlock(); ok {
		return blockIdx, true
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
	if c.lastToolBlockIndex == blockIdx {
		if latest, ok := c.latestOpenToolBlock(); ok {
			c.lastToolBlockIndex = latest
		} else {
			c.lastToolBlockIndex = -1
		}
	}

	result := formatClaudeSSE(map[string]any{
		"type":  "content_block_stop",
		"index": blockIdx,
	})
	c.contentIndex++
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
