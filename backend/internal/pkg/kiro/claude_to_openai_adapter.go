// Package kiro provides Claude Messages API → OpenAI Chat Completions request conversion.
// This enables routing Claude-format requests to OpenAI platform accounts.
package kiro

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Claude model → OpenAI model mapping
var claudeToOpenAIModelMap = map[string]string{
	// Opus series → gpt-5.4-codex
	"claude-opus-4-6":            "gpt-5.4-codex",
	"claude-opus-4-6-1m":         "gpt-5.4-codex",
	"claude-opus-4-5":            "gpt-5.4-codex",
	"claude-opus-4-5-20251101":   "gpt-5.4-codex",
	"claude-opus-4.5":            "gpt-5.4-codex",
	// Sonnet series → gpt-5.3-codex
	"claude-sonnet-4-6":            "gpt-5.3-codex",
	"claude-sonnet-4-6-1m":         "gpt-5.3-codex",
	"claude-sonnet-4-5":            "gpt-5.3-codex",
	"claude-sonnet-4-5-20250929":   "gpt-5.3-codex",
	"claude-sonnet-4-20250514":     "gpt-5.3-codex",
	"claude-3-7-sonnet-20250219":   "gpt-5.3-codex",
	"claude-3-5-sonnet-20241022":   "gpt-5.3-codex",
	"claude-3-5-sonnet-latest":     "gpt-5.3-codex",
	"claude-3-5-sonnet-v2":         "gpt-5.3-codex",
	// Haiku series → gpt-5.2-codex (fallback to lighter model)
	"claude-haiku-4-5":          "gpt-5.2-codex",
	"claude-haiku-4-5-20251001": "gpt-5.2-codex",
	"claude-3-5-haiku-20241022": "gpt-5.2-codex",
	"claude-3-5-haiku-latest":   "gpt-5.2-codex",
}

const defaultOpenAIModel = "gpt-5.4-codex"

// GetOpenAIModelID maps a Claude model name to an OpenAI model ID.
func GetOpenAIModelID(claudeModel string) string {
	if m, ok := claudeToOpenAIModelMap[claudeModel]; ok {
		return m
	}
	// Check prefix-based matching for unknown versions
	lower := strings.ToLower(claudeModel)
	if strings.Contains(lower, "haiku") {
		return "gpt-5.2-codex"
	}
	if strings.Contains(lower, "sonnet") {
		return "gpt-5.3-codex"
	}
	if strings.Contains(lower, "opus") {
		return "gpt-5.4-codex"
	}
	return defaultOpenAIModel
}

// ConvertClaudeToOpenAI converts a Claude Messages API request body to OpenAI Chat Completions format.
// Returns the converted body, the original Claude model name, and any error.
func ConvertClaudeToOpenAI(body []byte) (openaiBody []byte, originalModel string, stream bool, err error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, "", false, fmt.Errorf("parse claude request: %w", err)
	}

	openaiReq := make(map[string]any)

	// Model mapping
	if model, ok := req["model"].(string); ok {
		originalModel = model
		openaiReq["model"] = GetOpenAIModelID(model)
	}

	// Stream
	if s, ok := req["stream"].(bool); ok {
		stream = s
		openaiReq["stream"] = s
		if s {
			openaiReq["stream_options"] = map[string]any{"include_usage": true}
		}
	}

	// Build messages: system + conversation
	var messages []any

	// System → first system message
	if system := req["system"]; system != nil {
		sysText := extractClaudeSystemText(system)
		if sysText != "" {
			messages = append(messages, map[string]any{
				"role":    "system",
				"content": sysText,
			})
		}
	}

	// Convert Claude messages to OpenAI messages
	if claudeMsgs, ok := req["messages"].([]any); ok {
		messages = append(messages, convertClaudeMessagesToOpenAI(claudeMsgs)...)
	}

	openaiReq["messages"] = messages

	// max_tokens → max_tokens
	if maxTokens, ok := req["max_tokens"].(float64); ok && maxTokens > 0 {
		openaiReq["max_tokens"] = int(maxTokens)
	}

	// temperature → direct pass
	if temp, ok := req["temperature"].(float64); ok {
		openaiReq["temperature"] = temp
	}

	// tools → OpenAI function format
	if tools, ok := req["tools"].([]any); ok && len(tools) > 0 {
		openaiReq["tools"] = convertClaudeToolsToOpenAI(tools)
	}

	// tool_choice → OpenAI tool_choice
	if tc := req["tool_choice"]; tc != nil {
		if converted := convertClaudeToolChoiceToOpenAI(tc); converted != nil {
			openaiReq["tool_choice"] = converted
		}
	}

	// Force reasoning effort for Claude-compat OpenAI path.
	openaiReq["reasoning"] = map[string]any{
		"effort": "xhigh",
	}

	openaiBody, err = json.Marshal(openaiReq)
	if err != nil {
		return nil, "", false, fmt.Errorf("marshal openai request: %w", err)
	}
	return openaiBody, originalModel, stream, nil
}

// extractClaudeSystemText extracts text from Claude system field (string or content block array).
func extractClaudeSystemText(system any) string {
	if s, ok := system.(string); ok {
		return s
	}
	if arr, ok := system.([]any); ok {
		var parts []string
		for _, block := range arr {
			if m, ok := block.(map[string]any); ok {
				if m["type"] == "text" {
					if text, ok := m["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
		}
		return strings.Join(parts, "\n\n")
	}
	return ""
}

// convertClaudeMessagesToOpenAI converts Claude messages array to OpenAI messages.
func convertClaudeMessagesToOpenAI(claudeMsgs []any) []any {
	var result []any
	for _, msg := range claudeMsgs {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		switch role {
		case "user":
			converted := convertClaudeUserMessage(m)
			// convertClaudeUserMessage may return []any when tool_result is present
			if msgs, ok := converted.([]any); ok {
				result = append(result, msgs...)
			} else {
				result = append(result, converted)
			}
		case "assistant":
			result = append(result, convertClaudeAssistantMessages(m)...)
		}
	}
	return result
}

// convertClaudeUserMessage converts a Claude user message to OpenAI format.
func convertClaudeUserMessage(m map[string]any) any {
	content := m["content"]

	// Simple string content
	if s, ok := content.(string); ok {
		return map[string]any{"role": "user", "content": s}
	}

	// Content block array
	if blocks, ok := content.([]any); ok {
		// Check if any block is a tool_result
		var openaiMsgs []any
		var contentParts []any
		hasToolResult := false

		for _, block := range blocks {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			blockType, _ := b["type"].(string)
			switch blockType {
			case "tool_result":
				hasToolResult = true
				// Flush any pending content parts as a user message
				if len(contentParts) > 0 {
					openaiMsgs = append(openaiMsgs, map[string]any{"role": "user", "content": contentParts})
					contentParts = nil
				}
				toolCallID, _ := b["tool_use_id"].(string)
				resultContent := extractToolResultContentText(b["content"])
				openaiMsgs = append(openaiMsgs, map[string]any{
					"role":         "tool",
					"tool_call_id": toolCallID,
					"content":      resultContent,
				})
			case "text":
				text, _ := b["text"].(string)
				contentParts = append(contentParts, map[string]any{"type": "text", "text": text})
			case "image":
				contentParts = append(contentParts, convertClaudeImageToOpenAI(b))
			}
		}

		if hasToolResult {
			if len(contentParts) > 0 {
				openaiMsgs = append(openaiMsgs, map[string]any{"role": "user", "content": contentParts})
			}
			// Return first message; caller should handle multiple
			// Actually, we need to return all messages. We'll handle this via a wrapper.
			if len(openaiMsgs) == 1 {
				return openaiMsgs[0]
			}
			// For multiple messages, return a special marker
			return openaiMsgs
		}

		// No tool_result: return as multipart content
		if len(contentParts) > 0 {
			return map[string]any{"role": "user", "content": contentParts}
		}
		return map[string]any{"role": "user", "content": ""}
	}

	return map[string]any{"role": "user", "content": ""}
}

// convertClaudeAssistantMessages converts a Claude assistant message to OpenAI format.
// May produce multiple messages (thinking → separate, tool_use → tool_calls).
func convertClaudeAssistantMessages(m map[string]any) []any {
	content := m["content"]

	// Simple string content
	if s, ok := content.(string); ok {
		return []any{map[string]any{"role": "assistant", "content": s}}
	}

	blocks, ok := content.([]any)
	if !ok {
		return []any{map[string]any{"role": "assistant", "content": ""}}
	}

	var textParts []string
	var toolCalls []any
	var thinkingText string

	for _, block := range blocks {
		b, ok := block.(map[string]any)
		if !ok {
			continue
		}
		blockType, _ := b["type"].(string)
		switch blockType {
		case "thinking":
			if t, ok := b["thinking"].(string); ok {
				thinkingText = t
			}
		case "text":
			if t, ok := b["text"].(string); ok {
				textParts = append(textParts, t)
			}
		case "tool_use":
			id, _ := b["id"].(string)
			name, _ := b["name"].(string)
			inputJSON, _ := json.Marshal(b["input"])
			toolCalls = append(toolCalls, map[string]any{
				"id":   id,
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": string(inputJSON),
				},
			})
		}
	}

	var result []any

	// Build assistant message
	assistantMsg := map[string]any{"role": "assistant"}
	if len(textParts) > 0 {
		assistantMsg["content"] = strings.Join(textParts, "")
	} else {
		assistantMsg["content"] = ""
	}
	if len(toolCalls) > 0 {
		assistantMsg["tool_calls"] = toolCalls
	}
	if thinkingText != "" {
		assistantMsg["reasoning"] = map[string]any{"content": thinkingText}
	}
	result = append(result, assistantMsg)

	return result
}

// extractToolResultContentText extracts text from tool_result content (string or blocks).
func extractToolResultContentText(content any) string {
	if s, ok := content.(string); ok {
		return s
	}
	if blocks, ok := content.([]any); ok {
		var parts []string
		for _, block := range blocks {
			if b, ok := block.(map[string]any); ok {
				if b["type"] == "text" {
					if t, ok := b["text"].(string); ok {
						parts = append(parts, t)
					}
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// convertClaudeImageToOpenAI converts a Claude image block to OpenAI image_url format.
func convertClaudeImageToOpenAI(b map[string]any) map[string]any {
	source, _ := b["source"].(map[string]any)
	if source == nil {
		return map[string]any{"type": "text", "text": "[image]"}
	}
	sourceType, _ := source["type"].(string)
	if sourceType == "base64" {
		mediaType, _ := source["media_type"].(string)
		data, _ := source["data"].(string)
		return map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": fmt.Sprintf("data:%s;base64,%s", mediaType, data),
			},
		}
	}
	if sourceType == "url" {
		url, _ := source["url"].(string)
		return map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": url,
			},
		}
	}
	return map[string]any{"type": "text", "text": "[image]"}
}

// convertClaudeToolsToOpenAI converts Claude tools to OpenAI function tools.
func convertClaudeToolsToOpenAI(tools []any) []any {
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
			"name": name,
		}
		if desc != "" {
			fn["description"] = desc
		}
		if inputSchema != nil {
			fn["parameters"] = inputSchema
		}

		result = append(result, map[string]any{
			"type":     "function",
			"function": fn,
		})
	}
	return result
}

// convertClaudeToolChoiceToOpenAI converts Claude tool_choice to OpenAI format.
func convertClaudeToolChoiceToOpenAI(tc any) any {
	m, ok := tc.(map[string]any)
	if !ok {
		return nil
	}
	tcType, _ := m["type"].(string)
	switch tcType {
	case "auto":
		return "auto"
	case "any":
		return "required"
	case "tool":
		name, _ := m["name"].(string)
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": name},
		}
	case "none":
		return "none"
	}
	return nil
}
