package service

import (
	"encoding/json"
	"strings"
	"time"
)

// convertCCRequestToResponses converts a Chat Completions request body to Responses API format.
// Returns the converted body and the extracted original model name.
func convertCCRequestToResponses(ccReqBody map[string]any) map[string]any {
	responsesBody := make(map[string]any)

	// Extract messages
	messages, _ := ccReqBody["messages"].([]any)

	var instructionParts []string
	var input []any

	for _, msg := range messages {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)

		switch role {
		case "system":
			text := extractContentText(m["content"])
			if text != "" {
				instructionParts = append(instructionParts, text)
			}

		case "user":
			item := map[string]any{
				"type": "message",
				"role": "user",
			}
			item["content"] = convertCCContentToResponses(m["content"], "user")
			input = append(input, item)

		case "assistant":
			toolCalls, hasToolCalls := m["tool_calls"].([]any)

			// Text content part
			contentText := extractContentText(m["content"])
			if contentText != "" {
				item := map[string]any{
					"type": "message",
					"role": "assistant",
				}
				item["content"] = convertCCContentToResponses(m["content"], "assistant")
				input = append(input, item)
			}

			// Tool calls
			if hasToolCalls {
				for _, tc := range toolCalls {
					tcMap, ok := tc.(map[string]any)
					if !ok {
						continue
					}
					fn, _ := tcMap["function"].(map[string]any)
					if fn == nil {
						continue
					}
					callID, _ := tcMap["id"].(string)
					name, _ := fn["name"].(string)
					arguments, _ := fn["arguments"].(string)

					fcItem := map[string]any{
						"type":      "function_call",
						"call_id":   callID,
						"name":      name,
						"arguments": arguments,
					}
					input = append(input, fcItem)
				}
			}

		case "tool":
			toolCallID, _ := m["tool_call_id"].(string)
			content := extractContentText(m["content"])
			fcoItem := map[string]any{
				"type":    "function_call_output",
				"call_id": toolCallID,
				"output":  content,
			}
			input = append(input, fcoItem)
		}
	}

	// Set instructions from system messages
	if len(instructionParts) > 0 {
		responsesBody["instructions"] = strings.Join(instructionParts, "\n")
	}

	// Set input
	if len(input) > 0 {
		responsesBody["input"] = input
	}

	// Copy model
	if model, ok := ccReqBody["model"].(string); ok {
		responsesBody["model"] = model
	}

	// Copy supported fields directly
	for _, key := range []string{"temperature", "top_p", "tools", "tool_choice", "prompt_cache_key", "service_tier"} {
		if v, ok := ccReqBody[key]; ok {
			responsesBody[key] = v
		}
	}

	return responsesBody
}

// extractContentText extracts plain text from CC content (string or array).
func extractContentText(content any) string {
	if content == nil {
		return ""
	}
	if s, ok := content.(string); ok {
		return s
	}
	if arr, ok := content.([]any); ok {
		var parts []string
		for _, part := range arr {
			partMap, ok := part.(map[string]any)
			if !ok {
				continue
			}
			partType, _ := partMap["type"].(string)
			if partType == "text" {
				if text, ok := partMap["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "")
	}
	return ""
}

// convertCCContentToResponses converts CC message content to Responses API content format.
func convertCCContentToResponses(content any, role string) any {
	if content == nil {
		return ""
	}
	// String content: pass through directly
	if s, ok := content.(string); ok {
		return s
	}
	// Array content: convert part types
	if arr, ok := content.([]any); ok {
		var converted []any
		for _, part := range arr {
			partMap, ok := part.(map[string]any)
			if !ok {
				converted = append(converted, part)
				continue
			}
			partType, _ := partMap["type"].(string)
			newPart := make(map[string]any, len(partMap))
			for k, v := range partMap {
				newPart[k] = v
			}
			if partType == "text" {
				if role == "assistant" {
					newPart["type"] = "output_text"
				} else {
					newPart["type"] = "input_text"
				}
			}
			converted = append(converted, newPart)
		}
		return converted
	}
	return content
}

// convertResponsesJSONToCC converts a Responses API final JSON (from response.completed)
// to Chat Completions format.
func convertResponsesJSONToCC(responsesBody []byte, originalModel, requestID string) ([]byte, *OpenAIUsage) {
	var resp map[string]any
	if err := json.Unmarshal(responsesBody, &resp); err != nil {
		return responsesBody, nil
	}

	// Extract response ID
	respID, _ := resp["id"].(string)
	if respID == "" {
		respID = requestID
	}

	// Extract output items
	outputItems, _ := resp["output"].([]any)

	var contentText string
	var toolCalls []map[string]any
	toolCallIndex := 0

	for _, item := range outputItems {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := itemMap["type"].(string)

		switch itemType {
		case "message":
			// Extract text from message content
			if contentArr, ok := itemMap["content"].([]any); ok {
				for _, c := range contentArr {
					cMap, ok := c.(map[string]any)
					if !ok {
						continue
					}
					cType, _ := cMap["type"].(string)
					if cType == "output_text" {
						if text, ok := cMap["text"].(string); ok {
							contentText += text
						}
					}
				}
			}

		case "function_call":
			callID, _ := itemMap["call_id"].(string)
			name, _ := itemMap["name"].(string)
			arguments, _ := itemMap["arguments"].(string)

			tc := map[string]any{
				"id":   callID,
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": arguments,
				},
				"index": toolCallIndex,
			}
			toolCalls = append(toolCalls, tc)
			toolCallIndex++
		}
	}

	// Build message
	message := map[string]any{
		"role": "assistant",
	}
	if contentText != "" {
		message["content"] = contentText
	} else {
		message["content"] = nil
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}

	// Determine finish_reason
	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}

	// Build usage
	usage := &OpenAIUsage{}
	ccUsage := map[string]any{
		"prompt_tokens":     0,
		"completion_tokens": 0,
		"total_tokens":      0,
	}
	if respUsage, ok := resp["usage"].(map[string]any); ok {
		inputTokens := jsonInt(respUsage["input_tokens"])
		outputTokens := jsonInt(respUsage["output_tokens"])
		usage.InputTokens = inputTokens
		usage.OutputTokens = outputTokens

		ccUsage["prompt_tokens"] = inputTokens
		ccUsage["completion_tokens"] = outputTokens
		ccUsage["total_tokens"] = inputTokens + outputTokens

		// Extract cached tokens
		if details, ok := respUsage["input_tokens_details"].(map[string]any); ok {
			cachedTokens := jsonInt(details["cached_tokens"])
			usage.CacheReadInputTokens = cachedTokens
			ccUsage["prompt_tokens_details"] = map[string]any{
				"cached_tokens": cachedTokens,
			}
		}
	}
	if rawServiceTier, ok := resp["service_tier"].(string); ok {
		usage.ServiceTier = normalizeOpenAIServiceTier(rawServiceTier)
		usage.ServiceTierPresent = true
	}

	// Build CC response
	ccResp := map[string]any{
		"id":      "chatcmpl-" + respID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   originalModel,
		"choices": []any{
			map[string]any{
				"index":         0,
				"message":       message,
				"finish_reason": finishReason,
			},
		},
		"usage": ccUsage,
	}

	result, err := json.Marshal(ccResp)
	if err != nil {
		return responsesBody, usage
	}
	return result, usage
}

// responsesToCCStreamConverter is a stateful converter that transforms
// Responses API SSE events into Chat Completions SSE chunks.
type responsesToCCStreamConverter struct {
	responseID    string
	originalModel string
	toolCallIndex int
	hasToolCalls  bool
	usage         *OpenAIUsage
	started       bool
}

func newResponsesToCCStreamConverter(originalModel string) *responsesToCCStreamConverter {
	return &responsesToCCStreamConverter{
		originalModel: originalModel,
		usage:         &OpenAIUsage{},
	}
}

// convertEvent processes a single Responses API SSE data payload and returns
// zero or more CC SSE lines to send to the client. Each returned string is a
// complete "data: {...}" line (without trailing newline).
func (conv *responsesToCCStreamConverter) convertEvent(data string) []string {
	if data == "" || data == "[DONE]" {
		return nil
	}

	var event map[string]any
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return nil
	}

	eventType, _ := event["type"].(string)

	switch eventType {
	case "response.created":
		// Extract response ID
		if response, ok := event["response"].(map[string]any); ok {
			if id, ok := response["id"].(string); ok {
				conv.responseID = id
			}
			if rawServiceTier, ok := response["service_tier"].(string); ok {
				conv.usage.ServiceTier = normalizeOpenAIServiceTier(rawServiceTier)
				conv.usage.ServiceTierPresent = true
			}
		}
		// Send initial chunk with role
		conv.started = true
		return conv.makeChunks(map[string]any{
			"role":    "assistant",
			"content": "",
		}, nil)

	case "response.output_text.delta":
		delta, _ := event["delta"].(string)
		if delta == "" {
			return nil
		}
		return conv.makeChunks(map[string]any{
			"content": delta,
		}, nil)

	case "response.output_item.added":
		item, _ := event["item"].(map[string]any)
		if item == nil {
			return nil
		}
		itemType, _ := item["type"].(string)
		if itemType != "function_call" {
			return nil
		}
		callID, _ := item["call_id"].(string)
		name, _ := item["name"].(string)

		tc := map[string]any{
			"index": conv.toolCallIndex,
			"id":    callID,
			"type":  "function",
			"function": map[string]any{
				"name":      name,
				"arguments": "",
			},
		}
		conv.hasToolCalls = true
		conv.toolCallIndex++
		return conv.makeChunks(map[string]any{
			"tool_calls": []any{tc},
		}, nil)

	case "response.function_call_arguments.delta":
		delta, _ := event["delta"].(string)
		if delta == "" {
			return nil
		}
		// Use the current tool call index - 1 (the last added tool call)
		idx := conv.toolCallIndex - 1
		if idx < 0 {
			idx = 0
		}
		tc := map[string]any{
			"index": idx,
			"function": map[string]any{
				"arguments": delta,
			},
		}
		return conv.makeChunks(map[string]any{
			"tool_calls": []any{tc},
		}, nil)

	case "response.completed":
		// Extract usage from the completed response
		if response, ok := event["response"].(map[string]any); ok {
			if rawServiceTier, ok := response["service_tier"].(string); ok {
				conv.usage.ServiceTier = normalizeOpenAIServiceTier(rawServiceTier)
				conv.usage.ServiceTierPresent = true
			}
			if respUsage, ok := response["usage"].(map[string]any); ok {
				conv.usage.InputTokens = jsonInt(respUsage["input_tokens"])
				conv.usage.OutputTokens = jsonInt(respUsage["output_tokens"])
				if details, ok := respUsage["input_tokens_details"].(map[string]any); ok {
					conv.usage.CacheReadInputTokens = jsonInt(details["cached_tokens"])
				}
			}
		}

		finishReason := "stop"
		if conv.hasToolCalls {
			finishReason = "tool_calls"
		}

		// Build usage for CC format
		ccUsage := map[string]any{
			"prompt_tokens":     conv.usage.InputTokens,
			"completion_tokens": conv.usage.OutputTokens,
			"total_tokens":      conv.usage.InputTokens + conv.usage.OutputTokens,
		}
		if conv.usage.CacheReadInputTokens > 0 {
			ccUsage["prompt_tokens_details"] = map[string]any{
				"cached_tokens": conv.usage.CacheReadInputTokens,
			}
		}

		// Final chunk with finish_reason and usage
		lines := conv.makeChunks(map[string]any{}, &finishReason, ccUsage)
		lines = append(lines, "data: [DONE]")
		return lines
	}

	// Silently ignore other event types
	return nil
}

// makeChunks builds CC SSE data lines. delta is the delta object content.
// If finishReason is non-nil, it's set on the choice. usageOpt is optional usage to include.
func (conv *responsesToCCStreamConverter) makeChunks(delta map[string]any, finishReason *string, usageOpt ...map[string]any) []string {
	choice := map[string]any{
		"index": 0,
		"delta": delta,
	}
	if finishReason != nil {
		choice["finish_reason"] = *finishReason
	} else {
		choice["finish_reason"] = nil
	}

	chunk := map[string]any{
		"id":      "chatcmpl-" + conv.responseID,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   conv.originalModel,
		"choices": []any{choice},
	}

	if len(usageOpt) > 0 && usageOpt[0] != nil {
		chunk["usage"] = usageOpt[0]
	}

	data, err := json.Marshal(chunk)
	if err != nil {
		return nil
	}
	return []string{"data: " + string(data)}
}

// jsonInt extracts an int from a JSON number (which may be float64).
func jsonInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}
