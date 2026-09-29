package kiro

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// KiroResponsesConverter converts native Kiro events into Responses output items.
// The same state builds streaming events and the non-streaming response object.
type KiroResponsesConverter struct {
	id, model                                           string
	created                                             int64
	sequence                                            int
	inputTokens, outputTokens, cacheCreation, cacheRead int
	reasoningTokens                                     int
	items                                               []map[string]any
	active                                              map[uint32]int
	texts                                               map[int]*strings.Builder
	status                                              string
	failure                                             any
	incomplete                                          any
	terminal                                            bool
	// clientTools restores the Codex item types that were lowered to plain
	// function tools on the way in.
	clientTools ResponsesClientTools
}

func NewKiroResponsesConverter(id, model string, inputTokens int) *KiroResponsesConverter {
	return &KiroResponsesConverter{id: id, model: model, created: time.Now().Unix(), inputTokens: inputTokens, items: []map[string]any{}, active: map[uint32]int{}, texts: map[int]*strings.Builder{}, status: "in_progress"}
}

// SetClientTools enables restoring Codex-private tool item types on output.
func (c *KiroResponsesConverter) SetClientTools(tools ResponsesClientTools) {
	c.clientTools = tools
}

func (c *KiroResponsesConverter) SetCacheTokens(creation, read int) {
	c.cacheCreation, c.cacheRead = creation, read
}

func (c *KiroResponsesConverter) TotalOutputTokens() int { return c.outputTokens }

func (c *KiroResponsesConverter) event(kind string, fields map[string]any) string {
	fields["type"], fields["sequence_number"] = kind, c.sequence
	c.sequence++
	data, _ := json.Marshal(fields)
	return fmt.Sprintf("event: %s\ndata: %s\n\n", kind, data)
}

func (c *KiroResponsesConverter) Response() map[string]any {
	var usage any
	if c.status != "in_progress" {
		usage = map[string]any{"input_tokens": c.inputTokens, "output_tokens": c.outputTokens, "total_tokens": c.inputTokens + c.outputTokens,
			"input_tokens_details": map[string]any{"cached_tokens": c.cacheRead}, "output_tokens_details": map[string]any{"reasoning_tokens": c.reasoningTokens},
			"cache_creation_input_tokens": c.cacheCreation}
	}
	return map[string]any{"id": c.id, "object": "response", "created_at": c.created, "status": c.status, "model": c.model, "output": c.items, "usage": usage, "error": c.failure, "incomplete_details": c.incomplete, "store": false, "parallel_tool_calls": true}
}

func (c *KiroResponsesConverter) BuildInitialEvent() string {
	// Build before converting upstream events so sequence numbers match wire order.
	// The caller may buffer this envelope until it commits the stream.
	response := c.Response()
	response["output"] = []any{}
	response["status"] = "in_progress"
	response["usage"] = nil
	response["error"] = nil
	return c.event("response.created", map[string]any{"response": response}) + c.event("response.in_progress", map[string]any{"response": response})
}

func (c *KiroResponsesConverter) ConvertEvent(e StreamEvent) string {
	if c.terminal {
		return ""
	}
	switch e.Type {
	case EventContentBlockStart:
		return c.start(e)
	case EventTextDelta, EventThinkingDelta, EventToolUseInputDelta:
		// Native delta events have no block index. Text/thinking use the
		// active block of that kind; interleaved tools are keyed by ToolID.
		index := -1
		for _, candidate := range c.active {
			item := c.items[candidate]
			if (e.Type == EventTextDelta && item["type"] == "message") ||
				(e.Type == EventThinkingDelta && item["type"] == "reasoning") ||
				(e.Type == EventToolUseInputDelta && isToolCallItem(item) && item["call_id"] == e.ToolID) {
				index = candidate
				break
			}
		}
		if index < 0 {
			return c.BuildErrorEvent("invalid_stream", "Content arrived without an output item")
		}
		item := c.items[index]
		delta := e.Text
		if e.Type == EventToolUseInputDelta {
			delta = e.PartialJSON
		}
		c.texts[index].WriteString(delta)
		c.outputTokens += (len(delta) + 3) / 4
		if e.Type == EventThinkingDelta {
			c.reasoningTokens += (len(delta) + 3) / 4
		}
		if item["type"] == "custom_tool_call" {
			return ""
		}
		fields := map[string]any{"item_id": item["id"], "output_index": index, "delta": delta}
		event := "response.output_text.delta"
		switch {
		case isToolCallItem(item):
			event = "response.function_call_arguments.delta"
		case item["type"] == "reasoning":
			event = "response.reasoning_summary_text.delta"
			fields["summary_index"] = 0
		default:
			fields["content_index"] = 0
			fields["logprobs"] = []any{}
		}
		return c.event(event, fields)
	case EventContentBlockStop:
		index, exists := c.active[e.Index]
		if !exists {
			return ""
		}
		delete(c.active, e.Index)
		return c.finishItem(index)
	case EventMessageStop:
		if e.StopReason == StopReasonMaxTokens {
			c.incomplete = map[string]any{"reason": "max_output_tokens"}
		}
	case EventError:
		return c.BuildErrorEvent(e.ErrorType, e.ErrorMessage)
	}
	return ""
}

func (c *KiroResponsesConverter) start(e StreamEvent) string {
	if _, exists := c.active[e.Index]; exists {
		return ""
	}
	index := len(c.items)
	id := fmt.Sprintf("msg_%s_%d", strings.TrimPrefix(c.id, "resp_"), index)
	item := map[string]any{"id": id, "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}}
	switch e.BlockType.Kind {
	case BlockToolUse:
		item = c.newToolCallItem(index, e.BlockType.ToolID, e.BlockType.ToolName)
	case BlockThinking:
		item = map[string]any{"id": fmt.Sprintf("rs_%s_%d", strings.TrimPrefix(c.id, "resp_"), index), "type": "reasoning", "summary": []any{}}
	}
	c.items = append(c.items, item)
	c.active[e.Index] = index
	c.texts[index] = &strings.Builder{}
	result := c.event("response.output_item.added", map[string]any{"output_index": index, "item": item})
	switch e.BlockType.Kind {
	case BlockText:
		result += c.event("response.content_part.added", map[string]any{"item_id": id, "output_index": index, "content_index": 0, "part": responseTextPart("")})
	case BlockThinking:
		result += c.event("response.reasoning_summary_part.added", map[string]any{"item_id": item["id"], "output_index": index, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}})
	}
	return result
}

func responseTextPart(text string) map[string]any {
	return map[string]any{"type": "output_text", "text": text, "annotations": []any{}, "logprobs": []any{}}
}

// newToolCallItem emits the item type the client declared. Codex rejects a
// function_call for a tool it registered as local_shell or custom, so the
// lowered name is mapped back here and the id prefix follows suit.
func (c *KiroResponsesConverter) newToolCallItem(index int, callID, name string) map[string]any {
	suffix := fmt.Sprintf("%s_%d", strings.TrimPrefix(c.id, "resp_"), index)
	switch c.clientTools.Kind(name) {
	case "local_shell":
		return map[string]any{"id": "lsh_" + suffix, "type": "local_shell_call", "status": "in_progress",
			"call_id": callID, "action": map[string]any{"type": "exec", "command": []any{}}}
	case "custom":
		return map[string]any{"id": "ctc_" + suffix, "type": "custom_tool_call", "status": "in_progress",
			"call_id": callID, "name": name, "input": ""}
	default:
		return map[string]any{"id": "fc_" + suffix, "type": "function_call", "status": "in_progress",
			"call_id": callID, "name": name, "arguments": ""}
	}
}

// isToolCallItem covers the restored Codex types as well as function_call, so
// delta routing and completion stay correct after a rewrite.
func isToolCallItem(item map[string]any) bool {
	switch item["type"] {
	case "function_call", "local_shell_call", "custom_tool_call":
		return true
	default:
		return false
	}
}

// finishToolCallItem writes the accumulated argument text into whichever field
// the restored item type uses.
func finishToolCallItem(item map[string]any, text string) {
	switch item["type"] {
	case "local_shell_call":
		action := map[string]any{"type": "exec"}
		var parsed map[string]any
		if json.Unmarshal([]byte(text), &parsed) == nil {
			for key, value := range parsed {
				action[key] = value
			}
		}
		if _, ok := action["command"]; !ok {
			action["command"] = []any{}
		}
		item["action"] = action
	case "custom_tool_call":
		item["input"] = extractResponsesCustomInput(text)
	default:
		item["arguments"] = text
	}
}

// extractResponsesCustomInput unwraps the {"input": "..."} envelope that a
// freeform tool was lowered into, falling back to the raw text.
func extractResponsesCustomInput(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	var object map[string]any
	if json.Unmarshal([]byte(trimmed), &object) == nil {
		if input, ok := object["input"].(string); ok {
			return input
		}
	}
	return trimmed
}

func (c *KiroResponsesConverter) finishItem(index int) string {
	item := c.items[index]
	text := c.texts[index].String()
	fields := map[string]any{"item_id": item["id"], "output_index": index}
	result := ""
	switch {
	case item["type"] == "custom_tool_call":
		input := extractResponsesCustomInput(text)
		item["input"] = input
		fields["call_id"] = item["call_id"]
		fields["name"] = item["name"]
		fields["input"] = input
		if input != "" {
			result = c.event("response.custom_tool_call_input.delta", map[string]any{
				"item_id": item["id"], "output_index": index, "delta": input,
			})
		}
		result += c.event("response.custom_tool_call_input.done", fields)
	case isToolCallItem(item):
		finishToolCallItem(item, text)
		// Codex reads argv from the item; the done event keeps the raw JSON.
		fields["arguments"] = text
		result = c.event("response.function_call_arguments.done", fields)
	case item["type"] == "reasoning":
		part := map[string]any{"type": "summary_text", "text": text}
		item["summary"] = []any{part}
		fields["summary_index"] = 0
		fields["text"] = text
		result = c.event("response.reasoning_summary_text.done", fields)
		result += c.event("response.reasoning_summary_part.done", map[string]any{"item_id": item["id"], "output_index": index, "summary_index": 0, "part": part})
	default:
		part := responseTextPart(text)
		item["content"] = []any{part}
		fields["content_index"] = 0
		fields["text"] = text
		fields["logprobs"] = []any{}
		result = c.event("response.output_text.done", fields)
		result += c.event("response.content_part.done", map[string]any{"item_id": item["id"], "output_index": index, "content_index": 0, "part": part})
	}
	if item["type"] != "reasoning" {
		item["status"] = "completed"
	}
	return result + c.event("response.output_item.done", map[string]any{"output_index": index, "item": item})
}

func (c *KiroResponsesConverter) BuildFinalEvent() string {
	if c.terminal {
		return ""
	}
	var result strings.Builder
	// Close any outstanding blocks in output order, not Go map iteration order.
	for index := range c.items {
		for block, active := range c.active {
			if active == index {
				delete(c.active, block)
				result.WriteString(c.finishItem(index))
			}
		}
	}
	c.status = "completed"
	if c.incomplete != nil {
		c.status = "incomplete"
	}
	c.terminal = true
	result.WriteString(c.event("response."+c.status, map[string]any{"response": c.Response()}))
	return result.String()
}

func (c *KiroResponsesConverter) BuildErrorEvent(code, message string) string {
	if c.terminal {
		return ""
	}
	if code == "" {
		code = "upstream_error"
	}
	if message == "" {
		message = "Upstream stream failed"
	}
	for _, index := range c.active {
		item := c.items[index]
		text := c.texts[index].String()
		switch {
		case isToolCallItem(item):
			finishToolCallItem(item, text)
		case item["type"] == "reasoning":
			item["summary"] = []any{map[string]any{"type": "summary_text", "text": text}}
		default:
			item["content"] = []any{responseTextPart(text)}
		}
		if item["type"] != "reasoning" {
			item["status"] = "incomplete"
		}
	}
	c.status = "failed"
	c.failure = map[string]any{"code": code, "message": message}
	c.terminal = true
	return c.event("response.failed", map[string]any{"response": c.Response()})
}
