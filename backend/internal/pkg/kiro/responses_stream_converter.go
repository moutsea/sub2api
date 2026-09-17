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
}

func NewKiroResponsesConverter(id, model string, inputTokens int) *KiroResponsesConverter {
	return &KiroResponsesConverter{id: id, model: model, created: time.Now().Unix(), inputTokens: inputTokens, items: []map[string]any{}, active: map[uint32]int{}, texts: map[int]*strings.Builder{}, status: "in_progress"}
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
				(e.Type == EventToolUseInputDelta && item["type"] == "function_call" && item["call_id"] == e.ToolID) {
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
		fields := map[string]any{"item_id": item["id"], "output_index": index, "delta": delta}
		event := "response.output_text.delta"
		switch item["type"] {
		case "function_call":
			event = "response.function_call_arguments.delta"
		case "reasoning":
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
		item = map[string]any{"id": fmt.Sprintf("fc_%s_%d", strings.TrimPrefix(c.id, "resp_"), index), "type": "function_call", "status": "in_progress", "call_id": e.BlockType.ToolID, "name": e.BlockType.ToolName, "arguments": ""}
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

func (c *KiroResponsesConverter) finishItem(index int) string {
	item := c.items[index]
	text := c.texts[index].String()
	fields := map[string]any{"item_id": item["id"], "output_index": index}
	result := ""
	switch item["type"] {
	case "function_call":
		item["arguments"] = text
		fields["arguments"] = text
		result = c.event("response.function_call_arguments.done", fields)
	case "reasoning":
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
				result.WriteString(c.finishItem(index))
				delete(c.active, block)
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
		switch item["type"] {
		case "function_call":
			item["arguments"] = text
		case "reasoning":
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
