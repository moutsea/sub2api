// Package kiro provides OpenAI Chat Completions ↔ Claude format conversion
// for routing OpenAI-compatible requests through the Kiro/CodeWhisperer pipeline.
package kiro

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ConvertOpenAIToClaude converts an OpenAI Chat Completions request body
// to a Claude Messages API request. This enables OpenAI-compatible clients
// (e.g. Cursor) to use Kiro accounts.
func ConvertOpenAIToClaude(body []byte) (*ClaudeRequest, error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("parse openai request: %w", err)
	}

	claudeReq := &ClaudeRequest{
		Stream: true, // default to streaming for Kiro
	}

	// Model
	if model, ok := req["model"].(string); ok {
		claudeReq.Model = model
	}

	// Stream
	if stream, ok := req["stream"].(bool); ok {
		claudeReq.Stream = stream
	}

	// Max tokens
	if maxTokens, ok := req["max_tokens"].(float64); ok && maxTokens > 0 {
		claudeReq.MaxTokens = int(maxTokens)
	}
	if maxTokens, ok := req["max_completion_tokens"].(float64); ok && maxTokens > 0 {
		claudeReq.MaxTokens = int(maxTokens)
	}

	// Temperature
	if temp, ok := req["temperature"].(float64); ok {
		claudeReq.Temperature = &temp
	}

	// Messages: separate system from conversation
	messages, _ := req["messages"].([]any)
	claudeReq.System, claudeReq.Messages = convertOpenAIMessages(messages)

	// Tools
	if tools, ok := req["tools"].([]any); ok && len(tools) > 0 {
		claudeReq.Tools = convertOpenAITools(tools)
	}

	// Tool choice
	if tc, ok := req["tool_choice"]; ok {
		claudeReq.ToolChoice = convertOpenAIToolChoice(tc)
	}

	// Thinking (from extended_thinking or reasoning_effort)
	if thinking, ok := req["thinking"].(map[string]any); ok {
		claudeReq.Thinking = thinking
	}

	return claudeReq, nil
}

// convertOpenAIMessages converts OpenAI messages to Claude system + messages.
// System messages are extracted and concatenated; conversation messages are converted.
func convertOpenAIMessages(messages []any) (any, []ClaudeMessage) {
	var systemParts []string
	var claudeMessages []ClaudeMessage

	for _, msg := range messages {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)

		switch role {
		case "system":
			text := extractOpenAIContentText(m["content"])
			if text != "" {
				systemParts = append(systemParts, text)
			}

		case "user":
			claudeMessages = append(claudeMessages, ClaudeMessage{
				Role:    "user",
				Content: convertOpenAIContentToClaude(m["content"]),
			})

		case "assistant":
			claudeMessages = append(claudeMessages, convertOpenAIAssistantMessage(m))

		case "tool":
			claudeMessages = append(claudeMessages, convertOpenAIToolResultMessage(m))
		}
	}

	var system any
	if len(systemParts) > 0 {
		system = strings.Join(systemParts, "\n\n")
	}

	return system, claudeMessages
}

// convertOpenAIAssistantMessage converts an OpenAI assistant message to Claude format.
// Handles both text content and tool_calls.
func convertOpenAIAssistantMessage(m map[string]any) ClaudeMessage {
	var blocks []any

	// Text content
	contentText := extractOpenAIContentText(m["content"])
	if contentText != "" {
		blocks = append(blocks, map[string]any{
			"type": "text",
			"text": contentText,
		})
	}

	// Tool calls → tool_use blocks
	if toolCalls, ok := m["tool_calls"].([]any); ok {
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

			var input any
			if err := json.Unmarshal([]byte(arguments), &input); err != nil {
				input = map[string]any{}
			}

			blocks = append(blocks, map[string]any{
				"type":  "tool_use",
				"id":    callID,
				"name":  name,
				"input": input,
			})
		}
	}

	if len(blocks) == 0 {
		// Empty assistant message — use empty text
		blocks = append(blocks, map[string]any{
			"type": "text",
			"text": "",
		})
	}

	return ClaudeMessage{
		Role:    "assistant",
		Content: blocks,
	}
}

// convertOpenAIToolResultMessage converts an OpenAI tool result message to Claude format.
func convertOpenAIToolResultMessage(m map[string]any) ClaudeMessage {
	toolCallID, _ := m["tool_call_id"].(string)
	content := extractOpenAIContentText(m["content"])

	return ClaudeMessage{
		Role: "user",
		Content: []any{
			map[string]any{
				"type":        "tool_result",
				"tool_use_id": toolCallID,
				"content":     content,
			},
		},
	}
}

// convertOpenAIContentToClaude converts OpenAI content (string or array) to Claude format.
func convertOpenAIContentToClaude(content any) any {
	if content == nil {
		return ""
	}
	if s, ok := content.(string); ok {
		return s
	}
	if arr, ok := content.([]any); ok {
		var blocks []any
		for _, part := range arr {
			partMap, ok := part.(map[string]any)
			if !ok {
				continue
			}
			partType, _ := partMap["type"].(string)
			switch partType {
			case "text":
				blocks = append(blocks, map[string]any{
					"type": "text",
					"text": partMap["text"],
				})
			case "image_url":
				if imageURL, ok := partMap["image_url"].(map[string]any); ok {
					if url, ok := imageURL["url"].(string); ok {
						blocks = append(blocks, map[string]any{
							"type": "image",
							"source": map[string]any{
								"type": "url",
								"url":  url,
							},
						})
					}
				}
			default:
				blocks = append(blocks, partMap)
			}
		}
		if len(blocks) > 0 {
			return blocks
		}
	}
	return ""
}

// convertOpenAITools converts OpenAI tool definitions to Claude format.
func convertOpenAITools(tools []any) []ClaudeTool {
	var claudeTools []ClaudeTool
	for _, tool := range tools {
		toolMap, ok := tool.(map[string]any)
		if !ok {
			continue
		}
		toolType, _ := toolMap["type"].(string)
		if toolType != "function" {
			continue
		}
		fn, ok := toolMap["function"].(map[string]any)
		if !ok {
			continue
		}
		name, _ := fn["name"].(string)
		description, _ := fn["description"].(string)
		var inputSchema map[string]any
		if params, ok := fn["parameters"].(map[string]any); ok {
			inputSchema = params
		}

		claudeTools = append(claudeTools, ClaudeTool{
			Name:        name,
			Description: description,
			InputSchema: inputSchema,
		})
	}
	return claudeTools
}

// convertOpenAIToolChoice converts OpenAI tool_choice to Claude format.
func convertOpenAIToolChoice(tc any) any {
	if tc == nil {
		return nil
	}
	// String values: "none", "auto", "required"
	if s, ok := tc.(string); ok {
		switch s {
		case "none":
			return map[string]any{"type": "none"}
		case "auto":
			return map[string]any{"type": "auto"}
		case "required":
			return map[string]any{"type": "any"}
		}
		return nil
	}
	// Object: {"type": "function", "function": {"name": "..."}}
	if tcMap, ok := tc.(map[string]any); ok {
		if fn, ok := tcMap["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok {
				return map[string]any{"type": "tool", "name": name}
			}
		}
	}
	return nil
}

// extractOpenAIContentText extracts plain text from OpenAI content (string or array).
func extractOpenAIContentText(content any) string {
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
			if partMap["type"] == "text" {
				if text, ok := partMap["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "")
	}
	return ""
}
