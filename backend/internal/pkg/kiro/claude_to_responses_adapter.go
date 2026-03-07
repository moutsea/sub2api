// Package kiro provides Claude Messages API → OpenAI Responses API request conversion.
// This enables routing Claude-format requests to OpenAI OAuth accounts via chatgpt.com internal API.
package kiro

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ConvertClaudeToResponses converts a Claude Messages API request body to OpenAI Responses API format.
// Returns the converted body, the original Claude model name, and any error.
func ConvertClaudeToResponses(body []byte) (responsesBody []byte, originalModel string, err error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, "", fmt.Errorf("parse claude request: %w", err)
	}

	responsesReq := make(map[string]any)

	// Model mapping (Claude → OpenAI)
	if model, ok := req["model"].(string); ok {
		originalModel = model
		responsesReq["model"] = GetOpenAIModelID(model)
	}

	// Responses API always streams via OAuth
	responsesReq["stream"] = true
	responsesReq["store"] = false

	// Build input array from system + messages
	var input []any

	// System → instructions (Responses API uses "instructions" field, not input)
	if system := req["system"]; system != nil {
		sysText := extractClaudeSystemText(system)
		if sysText != "" {
			responsesReq["instructions"] = sysText
		}
	}

	// Convert Claude messages to Responses API input items
	if claudeMsgs, ok := req["messages"].([]any); ok {
		input = convertClaudeMessagesToResponsesInput(claudeMsgs)
	}

	responsesReq["input"] = input

	// Force reasoning effort for Claude-compat OpenAI path.
	responsesReq["reasoning"] = map[string]any{
		"effort":  "xhigh",
		"summary": "auto",
	}

	// tools → Responses API tools format
	if tools, ok := req["tools"].([]any); ok && len(tools) > 0 {
		responsesReq["tools"] = convertClaudeToolsToResponsesTools(tools)
	}

	// tool_choice
	if tc := req["tool_choice"]; tc != nil {
		if converted := convertClaudeToolChoiceToOpenAI(tc); converted != nil {
			responsesReq["tool_choice"] = converted
		}
	}

	responsesBody, err = json.Marshal(responsesReq)
	if err != nil {
		return nil, "", fmt.Errorf("marshal responses request: %w", err)
	}
	return responsesBody, originalModel, nil
}

// convertClaudeMessagesToResponsesInput converts Claude messages to Responses API input items.
func convertClaudeMessagesToResponsesInput(claudeMsgs []any) []any {
	var input []any
	for _, msg := range claudeMsgs {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		switch role {
		case "user":
			input = append(input, convertClaudeUserToResponsesInput(m)...)
		case "assistant":
			input = append(input, convertClaudeAssistantToResponsesInput(m)...)
		}
	}
	return input
}

// convertClaudeUserToResponsesInput converts a Claude user message to Responses API input items.
func convertClaudeUserToResponsesInput(m map[string]any) []any {
	content := m["content"]

	// Simple string content
	if s, ok := content.(string); ok {
		return []any{
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{"type": "input_text", "text": s},
				},
			},
		}
	}

	// Content block array
	blocks, ok := content.([]any)
	if !ok {
		return nil
	}

	var items []any
	var contentParts []any

	for _, block := range blocks {
		b, ok := block.(map[string]any)
		if !ok {
			continue
		}
		blockType, _ := b["type"].(string)
		switch blockType {
		case "tool_result":
			// Flush pending content as a user message
			if len(contentParts) > 0 {
				items = append(items, map[string]any{
					"type":    "message",
					"role":    "user",
					"content": contentParts,
				})
				contentParts = nil
			}
			// tool_result → function_call_output
			toolCallID, _ := b["tool_use_id"].(string)
			resultContent := extractToolResultContentText(b["content"])
			items = append(items, map[string]any{
				"type":    "function_call_output",
				"call_id": toolCallID,
				"output":  resultContent,
			})
		case "text":
			text, _ := b["text"].(string)
			contentParts = append(contentParts, map[string]any{"type": "input_text", "text": text})
		case "image":
			contentParts = append(contentParts, convertClaudeImageToResponsesInput(b))
		}
	}

	if len(contentParts) > 0 {
		items = append(items, map[string]any{
			"type":    "message",
			"role":    "user",
			"content": contentParts,
		})
	}

	return items
}

// convertClaudeAssistantToResponsesInput converts a Claude assistant message to Responses API input items.
func convertClaudeAssistantToResponsesInput(m map[string]any) []any {
	content := m["content"]

	// Simple string content
	if s, ok := content.(string); ok {
		return []any{
			map[string]any{
				"type": "message",
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "output_text", "text": s},
				},
			},
		}
	}

	blocks, ok := content.([]any)
	if !ok {
		return nil
	}

	var items []any
	var contentParts []any

	for _, block := range blocks {
		b, ok := block.(map[string]any)
		if !ok {
			continue
		}
		blockType, _ := b["type"].(string)
		switch blockType {
		case "thinking":
			// Skip thinking blocks for Responses API (handled via reasoning param)
		case "text":
			text, _ := b["text"].(string)
			contentParts = append(contentParts, map[string]any{"type": "output_text", "text": text})
		case "tool_use":
			// Flush pending content as assistant message
			if len(contentParts) > 0 {
				items = append(items, map[string]any{
					"type":    "message",
					"role":    "assistant",
					"content": contentParts,
				})
				contentParts = nil
			}
			// tool_use → function_call
			id, _ := b["id"].(string)
			name, _ := b["name"].(string)
			inputJSON, _ := json.Marshal(b["input"])
			items = append(items, map[string]any{
				"type":      "function_call",
				"call_id":   id,
				"name":      name,
				"arguments": string(inputJSON),
			})
		}
	}

	if len(contentParts) > 0 {
		items = append(items, map[string]any{
			"type":    "message",
			"role":    "assistant",
			"content": contentParts,
		})
	}

	return items
}

// convertClaudeImageToResponsesInput converts a Claude image block to Responses API input format.
func convertClaudeImageToResponsesInput(b map[string]any) map[string]any {
	source, _ := b["source"].(map[string]any)
	if source == nil {
		return map[string]any{"type": "input_text", "text": "[image]"}
	}
	sourceType, _ := source["type"].(string)
	if sourceType == "base64" {
		mediaType, _ := source["media_type"].(string)
		data, _ := source["data"].(string)
		return map[string]any{
			"type":      "input_image",
			"image_url": fmt.Sprintf("data:%s;base64,%s", mediaType, data),
		}
	}
	if sourceType == "url" {
		url, _ := source["url"].(string)
		return map[string]any{
			"type":      "input_image",
			"image_url": url,
		}
	}
	return map[string]any{"type": "input_text", "text": "[image]"}
}

// convertClaudeToolsToResponsesTools converts Claude tools to Responses API function tools.
func convertClaudeToolsToResponsesTools(tools []any) []any {
	var result []any
	for _, tool := range tools {
		t, ok := tool.(map[string]any)
		if !ok {
			continue
		}
		name, _ := t["name"].(string)
		desc, _ := t["description"].(string)
		inputSchema := t["input_schema"]

		fn := map[string]any{
			"type": "function",
			"name": name,
		}
		if desc != "" {
			fn["description"] = desc
		}
		if inputSchema != nil {
			fn["parameters"] = inputSchema
		}

		result = append(result, fn)
	}
	return result
}

// IsResponsesAPIEvent checks if an SSE line is a Responses API event (event: xxx format).
func IsResponsesAPIEvent(line string) bool {
	return strings.HasPrefix(line, "event:")
}
