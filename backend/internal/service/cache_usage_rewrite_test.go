package service

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRewriteCacheUsageInJSON(t *testing.T) {
	body := `{
		"id": "msg_123",
		"type": "message",
		"usage": {
			"input_tokens": 100,
			"output_tokens": 50,
			"cache_creation_input_tokens": 200,
			"cache_read_input_tokens": 300
		}
	}`

	result := rewriteCacheUsageInJSON([]byte(body))
	var data map[string]any
	if err := json.Unmarshal(result, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	usage := data["usage"].(map[string]any)

	// input_tokens should be 100 + 200 + 300 = 600
	if intFromAny(usage["input_tokens"]) != 600 {
		t.Errorf("expected input_tokens=600, got %v", usage["input_tokens"])
	}

	// output_tokens should be unchanged
	if intFromAny(usage["output_tokens"]) != 50 {
		t.Errorf("expected output_tokens=50, got %v", usage["output_tokens"])
	}

	// cache fields should be removed
	if _, has := usage["cache_creation_input_tokens"]; has {
		t.Error("cache_creation_input_tokens should be removed")
	}
	if _, has := usage["cache_read_input_tokens"]; has {
		t.Error("cache_read_input_tokens should be removed")
	}
}

func TestRewriteCacheUsageInJSON_NoCacheTokens(t *testing.T) {
	body := `{"usage": {"input_tokens": 100, "output_tokens": 50}}`
	result := rewriteCacheUsageInJSON([]byte(body))

	// Should return unchanged
	var data map[string]any
	json.Unmarshal(result, &data)
	usage := data["usage"].(map[string]any)
	if intFromAny(usage["input_tokens"]) != 100 {
		t.Errorf("expected input_tokens=100, got %v", usage["input_tokens"])
	}
}

func TestRewriteCacheUsageInJSON_ZeroCacheTokens(t *testing.T) {
	body := `{"usage": {"input_tokens": 100, "output_tokens": 50, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0}}`
	result := rewriteCacheUsageInJSON([]byte(body))

	// Both cache fields are 0 → input_tokens unchanged, but cache fields should be deleted
	var data map[string]any
	json.Unmarshal(result, &data)
	usage := data["usage"].(map[string]any)
	if intFromAny(usage["input_tokens"]) != 100 {
		t.Errorf("expected input_tokens=100, got %v", usage["input_tokens"])
	}
	if _, has := usage["cache_creation_input_tokens"]; has {
		t.Error("cache_creation_input_tokens should be removed even when zero")
	}
	if _, has := usage["cache_read_input_tokens"]; has {
		t.Error("cache_read_input_tokens should be removed even when zero")
	}
}

func TestRewriteCacheUsageInSSELine_MessageStart(t *testing.T) {
	line := `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-20250514","usage":{"input_tokens":50,"cache_creation_input_tokens":100,"cache_read_input_tokens":200}}}`

	result := rewriteCacheUsageInSSELine(line)

	// Extract the data part
	data := strings.TrimPrefix(result, "data: ")
	var event map[string]any
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	msg := event["message"].(map[string]any)
	usage := msg["usage"].(map[string]any)

	// input_tokens should be 50 + 100 + 200 = 350
	if intFromAny(usage["input_tokens"]) != 350 {
		t.Errorf("expected input_tokens=350, got %v", usage["input_tokens"])
	}

	// cache fields should be removed
	if _, has := usage["cache_creation_input_tokens"]; has {
		t.Error("cache_creation_input_tokens should be removed from message_start")
	}
	if _, has := usage["cache_read_input_tokens"]; has {
		t.Error("cache_read_input_tokens should be removed from message_start")
	}
}

func TestRewriteCacheUsageInSSELine_MessageDelta(t *testing.T) {
	line := `data: {"type":"message_delta","usage":{"output_tokens":100,"cache_creation_input_tokens":0,"cache_read_input_tokens":50}}`

	result := rewriteCacheUsageInSSELine(line)

	data := strings.TrimPrefix(result, "data: ")
	var event map[string]any
	json.Unmarshal([]byte(data), &event)

	usage := event["usage"].(map[string]any)

	// input_tokens should be 0 + 0 + 50 = 50
	if intFromAny(usage["input_tokens"]) != 50 {
		t.Errorf("expected input_tokens=50, got %v", usage["input_tokens"])
	}

	// cache_read should be removed
	if _, has := usage["cache_read_input_tokens"]; has {
		t.Error("cache_read_input_tokens should be removed from message_delta")
	}
}

func TestRewriteCacheUsageInSSELine_NoCacheTokens(t *testing.T) {
	line := `data: {"type":"message_start","message":{"usage":{"input_tokens":100}}}`

	result := rewriteCacheUsageInSSELine(line)
	// Should return unchanged line
	if result != line {
		t.Errorf("expected unchanged line, got %s", result)
	}
}

func TestRewriteCacheUsageInSSELine_ContentDelta(t *testing.T) {
	// content_block_delta should pass through unchanged
	line := `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`

	result := rewriteCacheUsageInSSELine(line)
	if result != line {
		t.Error("content_block_delta should be unchanged")
	}
}

func TestRewriteCacheUsageInSSELine_NonDataLine(t *testing.T) {
	line := `event: message_start`
	result := rewriteCacheUsageInSSELine(line)
	if result != line {
		t.Error("non-data lines should be unchanged")
	}
}

func TestRewriteCacheUsageInSSELine_DoneLine(t *testing.T) {
	line := `data: [DONE]`
	result := rewriteCacheUsageInSSELine(line)
	if result != line {
		t.Error("[DONE] line should be unchanged")
	}
}

func TestMergeCacheTokensToInput(t *testing.T) {
	usage := &ClaudeUsage{
		InputTokens:              100,
		OutputTokens:             50,
		CacheCreationInputTokens: 200,
		CacheReadInputTokens:     300,
	}

	mergeCacheTokensToInput(usage)

	if usage.InputTokens != 600 {
		t.Errorf("expected InputTokens=600, got %d", usage.InputTokens)
	}
	if usage.OutputTokens != 50 {
		t.Errorf("expected OutputTokens=50, got %d", usage.OutputTokens)
	}
	if usage.CacheCreationInputTokens != 0 {
		t.Errorf("expected CacheCreationInputTokens=0, got %d", usage.CacheCreationInputTokens)
	}
	if usage.CacheReadInputTokens != 0 {
		t.Errorf("expected CacheReadInputTokens=0, got %d", usage.CacheReadInputTokens)
	}
}

func TestMergeCacheTokensToInput_NoCacheTokens(t *testing.T) {
	usage := &ClaudeUsage{
		InputTokens:  100,
		OutputTokens: 50,
	}

	mergeCacheTokensToInput(usage)

	if usage.InputTokens != 100 {
		t.Errorf("expected InputTokens=100, got %d", usage.InputTokens)
	}
}

func TestMergeCacheTokensToInput_Nil(t *testing.T) {
	// Should not panic
	mergeCacheTokensToInput(nil)
}

func TestMergeCacheTokensInUsage(t *testing.T) {
	usageMap := map[string]any{
		"input_tokens":                float64(100),
		"output_tokens":               float64(50),
		"cache_creation_input_tokens": float64(200),
		"cache_read_input_tokens":     float64(300),
	}

	modified := mergeCacheTokensInUsage(usageMap)
	if !modified {
		t.Error("expected modification")
	}
	if usageMap["input_tokens"] != float64(600) {
		t.Errorf("expected float64(600), got %v (type %T)", usageMap["input_tokens"], usageMap["input_tokens"])
	}
	if _, has := usageMap["cache_creation_input_tokens"]; has {
		t.Error("cache_creation_input_tokens should be deleted")
	}
	if _, has := usageMap["cache_read_input_tokens"]; has {
		t.Error("cache_read_input_tokens should be deleted")
	}
}

func TestMergeCacheTokensInUsage_Nil(t *testing.T) {
	modified := mergeCacheTokensInUsage(nil)
	if modified {
		t.Error("nil map should not be modified")
	}
}

func TestIntFromAny(t *testing.T) {
	tests := []struct {
		input    any
		expected int
	}{
		{float64(42), 42},
		{int(42), 42},
		{int64(42), 42},
		{json.Number("42"), 42},
		{json.Number("invalid"), 0},
		{nil, 0},
		{"42", 0},
		{true, 0},
	}

	for _, tt := range tests {
		result := intFromAny(tt.input)
		if result != tt.expected {
			t.Errorf("intFromAny(%v) = %d, want %d", tt.input, result, tt.expected)
		}
	}
}

// --- Additional edge case tests ---

func TestRewriteCacheUsageInJSON_InvalidJSON(t *testing.T) {
	body := []byte(`not valid json`)
	result := rewriteCacheUsageInJSON(body)
	if string(result) != string(body) {
		t.Error("invalid JSON should return original body unchanged")
	}
}

func TestRewriteCacheUsageInJSON_NoUsageKey(t *testing.T) {
	body := []byte(`{"id":"msg_123","content":[]}`)
	result := rewriteCacheUsageInJSON(body)
	if string(result) != string(body) {
		t.Error("body without usage key should be unchanged")
	}
}

func TestRewriteCacheUsageInSSELine_OnlyCacheCreation(t *testing.T) {
	// Only cache_creation_input_tokens, no cache_read_input_tokens
	line := `data: {"type":"message_start","message":{"usage":{"input_tokens":100,"cache_creation_input_tokens":200}}}`
	result := rewriteCacheUsageInSSELine(line)

	data := strings.TrimPrefix(result, "data: ")
	var event map[string]any
	json.Unmarshal([]byte(data), &event)
	msg := event["message"].(map[string]any)
	usage := msg["usage"].(map[string]any)

	if intFromAny(usage["input_tokens"]) != 300 {
		t.Errorf("expected 300, got %v", usage["input_tokens"])
	}
	if _, has := usage["cache_creation_input_tokens"]; has {
		t.Error("cache_creation_input_tokens should be removed")
	}
}

func TestRewriteCacheUsageInSSELine_DataNoSpace(t *testing.T) {
	// "data:" without space (non-standard but valid SSE)
	line := `data:{"type":"message_delta","usage":{"output_tokens":50,"cache_read_input_tokens":100}}`
	result := rewriteCacheUsageInSSELine(line)

	// Should be normalized to "data: " with space
	if !strings.HasPrefix(result, "data: ") {
		t.Errorf("expected 'data: ' prefix, got: %s", result[:10])
	}
	data := strings.TrimPrefix(result, "data: ")
	var event map[string]any
	json.Unmarshal([]byte(data), &event)
	usage := event["usage"].(map[string]any)
	if intFromAny(usage["input_tokens"]) != 100 {
		t.Errorf("expected 100, got %v", usage["input_tokens"])
	}
}

func TestMergeCacheTokensInUsage_EmptyMap(t *testing.T) {
	usageMap := map[string]any{}
	modified := mergeCacheTokensInUsage(usageMap)
	if modified {
		t.Error("empty map should not be modified")
	}
}

func TestMergeCacheTokensInUsage_ZeroValues(t *testing.T) {
	// Zero-value fields should be deleted (not leak to client)
	usageMap := map[string]any{
		"input_tokens":                float64(100),
		"cache_creation_input_tokens": float64(0),
		"cache_read_input_tokens":     float64(0),
	}
	modified := mergeCacheTokensInUsage(usageMap)
	if !modified {
		t.Error("should be modified (fields exist and need deletion)")
	}
	if intFromAny(usageMap["input_tokens"]) != 100 {
		t.Errorf("expected 100, got %v", usageMap["input_tokens"])
	}
	if _, has := usageMap["cache_creation_input_tokens"]; has {
		t.Error("zero-value cache_creation should be deleted")
	}
	if _, has := usageMap["cache_read_input_tokens"]; has {
		t.Error("zero-value cache_read should be deleted")
	}
}

func TestMergeCacheTokensToInput_OnlyCacheCreation(t *testing.T) {
	usage := &ClaudeUsage{
		InputTokens:              100,
		CacheCreationInputTokens: 200,
	}
	mergeCacheTokensToInput(usage)
	if usage.InputTokens != 300 {
		t.Errorf("expected 300, got %d", usage.InputTokens)
	}
	if usage.CacheCreationInputTokens != 0 {
		t.Errorf("expected 0, got %d", usage.CacheCreationInputTokens)
	}
}
