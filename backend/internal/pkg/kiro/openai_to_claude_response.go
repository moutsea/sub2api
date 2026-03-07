// Package kiro provides OpenAI Chat Completions → Claude Messages API response conversion.
package kiro

import (
	"encoding/json"
	"fmt"
)

// ConvertOpenAIResponseToClaude converts an OpenAI Chat Completions non-streaming response
// to Claude Messages API format.
func ConvertOpenAIResponseToClaude(respBody []byte, originalModel string) ([]byte, *OpenAIResponseUsage, error) {
	var resp map[string]any
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, nil, fmt.Errorf("parse openai response: %w", err)
	}

	// Extract choices[0].message
	choices, _ := resp["choices"].([]any)
	if len(choices) == 0 {
		return nil, nil, fmt.Errorf("openai response has no choices")
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if message == nil {
		return nil, nil, fmt.Errorf("openai response has no message")
	}

	// Build Claude content blocks
	var contentBlocks []any

	// reasoning → thinking block (before text)
	if reasoning, ok := message["reasoning"].(map[string]any); ok {
		if reasoningContent, ok := reasoning["content"].(string); ok && reasoningContent != "" {
			contentBlocks = append(contentBlocks, map[string]any{
				"type":     "thinking",
				"thinking": reasoningContent,
			})
		}
	}

	// content → text block
	if content, ok := message["content"].(string); ok && content != "" {
		contentBlocks = append(contentBlocks, map[string]any{
			"type": "text",
			"text": content,
		})
	}

	// tool_calls → tool_use blocks
	if toolCalls, ok := message["tool_calls"].([]any); ok {
		for _, tc := range toolCalls {
			tcMap, ok := tc.(map[string]any)
			if !ok {
				continue
			}
			id, _ := tcMap["id"].(string)
			fn, _ := tcMap["function"].(map[string]any)
			if fn == nil {
				continue
			}
			name, _ := fn["name"].(string)
			argsStr, _ := fn["arguments"].(string)
			var input any
			if err := json.Unmarshal([]byte(argsStr), &input); err != nil {
				input = map[string]any{}
			}
			contentBlocks = append(contentBlocks, map[string]any{
				"type":  "tool_use",
				"id":    id,
				"name":  name,
				"input": input,
			})
		}
	}

	if len(contentBlocks) == 0 {
		contentBlocks = append(contentBlocks, map[string]any{
			"type": "text",
			"text": "",
		})
	}

	// Map finish_reason → stop_reason
	finishReason, _ := choice["finish_reason"].(string)
	stopReason := mapOpenAIFinishReason(finishReason)

	// Map usage
	var usageOut *OpenAIResponseUsage
	usage, _ := resp["usage"].(map[string]any)
	upstreamInputTokens := jsonInt(usage, "prompt_tokens")
	outputTokens := jsonInt(usage, "completion_tokens")
	cacheCreationTokens := jsonInt(usage, "cache_creation_input_tokens")
	cacheReadTokens := extractCachedTokensFromUsageDetails(usage, "prompt_tokens_details", "input_tokens_details")
	inputTokens := normalizeClaudeInputTokens(upstreamInputTokens, cacheCreationTokens, cacheReadTokens)
	usageOut = &OpenAIResponseUsage{
		InputTokens:              inputTokens,
		OutputTokens:             outputTokens,
		CacheCreationInputTokens: cacheCreationTokens,
		CacheReadInputTokens:     cacheReadTokens,
	}

	// Build Claude response
	claudeResp := map[string]any{
		"id":            fmt.Sprintf("msg_%s", extractOrGenerateID(resp)),
		"type":          "message",
		"role":          "assistant",
		"model":         originalModel,
		"content":       contentBlocks,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
		},
	}
	addClaudeCacheUsageFields(claudeResp["usage"].(map[string]any), cacheCreationTokens, cacheReadTokens)

	result, err := json.Marshal(claudeResp)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal claude response: %w", err)
	}
	return result, usageOut, nil
}

// OpenAIResponseUsage holds extracted usage from OpenAI response.
type OpenAIResponseUsage struct {
	InputTokens              int
	OutputTokens             int
	CacheCreationInputTokens int
	CacheReadInputTokens     int
}

// mapOpenAIFinishReason maps OpenAI finish_reason to Claude stop_reason.
func mapOpenAIFinishReason(reason string) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	case "content_filter":
		return "end_turn"
	default:
		return "end_turn"
	}
}

// extractOrGenerateID extracts the ID from OpenAI response or generates one.
func extractOrGenerateID(resp map[string]any) string {
	if id, ok := resp["id"].(string); ok && id != "" {
		return id
	}
	return "openai_compat"
}

// jsonInt extracts an int from a JSON map field.
func jsonInt(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	if v, ok := m[key].(int); ok {
		return v
	}
	return 0
}

func normalizeClaudeInputTokens(totalInputTokens, cacheCreationTokens, cacheReadTokens int) int {
	adjusted := totalInputTokens - cacheCreationTokens - cacheReadTokens
	if adjusted < 0 {
		return 0
	}
	return adjusted
}

func extractCachedTokensFromUsageDetails(usage map[string]any, detailKeys ...string) int {
	if usage == nil {
		return 0
	}
	for _, key := range detailKeys {
		details, ok := usage[key].(map[string]any)
		if !ok || details == nil {
			continue
		}
		return jsonInt(details, "cached_tokens")
	}
	return 0
}

func addClaudeCacheUsageFields(usage map[string]any, cacheCreationTokens, cacheReadTokens int) {
	if usage == nil {
		return
	}
	if cacheCreationTokens > 0 {
		usage["cache_creation_input_tokens"] = cacheCreationTokens
	}
	if cacheReadTokens > 0 {
		usage["cache_read_input_tokens"] = cacheReadTokens
	}
}
