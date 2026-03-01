package kiro

import (
	"encoding/json"
	"testing"
)

// ==================== ConvertClaudeToOpenAI Tests ====================

func TestConvertClaudeToOpenAI_BasicText(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 100,
		"messages": [{"role": "user", "content": "hello"}]
	}`)

	openaiBody, originalModel, stream, err := ConvertClaudeToOpenAI(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if originalModel != "claude-sonnet-4-20250514" {
		t.Errorf("originalModel = %q, want %q", originalModel, "claude-sonnet-4-20250514")
	}
	if stream {
		t.Error("stream should be false")
	}

	var req map[string]any
	if err := json.Unmarshal(openaiBody, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req["model"] != "gpt-5.2-codex" {
		t.Errorf("model = %v, want gpt-5.2-codex", req["model"])
	}
	msgs, _ := req["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages len = %d, want 1", len(msgs))
	}
	msg, _ := msgs[0].(map[string]any)
	if msg["role"] != "user" || msg["content"] != "hello" {
		t.Errorf("message = %v", msg)
	}
	if req["max_tokens"] != float64(100) {
		t.Errorf("max_tokens = %v", req["max_tokens"])
	}
}

func TestConvertClaudeToOpenAI_OpusModel(t *testing.T) {
	body := []byte(`{
		"model": "claude-opus-4-6",
		"max_tokens": 50,
		"messages": [{"role": "user", "content": "hi"}]
	}`)

	openaiBody, originalModel, _, err := ConvertClaudeToOpenAI(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if originalModel != "claude-opus-4-6" {
		t.Errorf("originalModel = %q", originalModel)
	}
	var req map[string]any
	json.Unmarshal(openaiBody, &req)
	if req["model"] != "gpt-5.3-codex" {
		t.Errorf("model = %v, want gpt-5.3-codex", req["model"])
	}
}

func TestConvertClaudeToOpenAI_SystemString(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 100,
		"system": "You are helpful.",
		"messages": [{"role": "user", "content": "hi"}]
	}`)

	openaiBody, _, _, err := ConvertClaudeToOpenAI(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var req map[string]any
	json.Unmarshal(openaiBody, &req)
	msgs, _ := req["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages len = %d, want 2", len(msgs))
	}
	sysMsg, _ := msgs[0].(map[string]any)
	if sysMsg["role"] != "system" || sysMsg["content"] != "You are helpful." {
		t.Errorf("system message = %v", sysMsg)
	}
}

func TestConvertClaudeToOpenAI_SystemBlocks(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 100,
		"system": [{"type": "text", "text": "Part 1"}, {"type": "text", "text": "Part 2"}],
		"messages": [{"role": "user", "content": "hi"}]
	}`)

	openaiBody, _, _, err := ConvertClaudeToOpenAI(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var req map[string]any
	json.Unmarshal(openaiBody, &req)
	msgs, _ := req["messages"].([]any)
	sysMsg, _ := msgs[0].(map[string]any)
	if sysMsg["content"] != "Part 1\n\nPart 2" {
		t.Errorf("system content = %v", sysMsg["content"])
	}
}

func TestConvertClaudeToOpenAI_Stream(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 100,
		"stream": true,
		"messages": [{"role": "user", "content": "hi"}]
	}`)

	openaiBody, _, stream, err := ConvertClaudeToOpenAI(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !stream {
		t.Error("stream should be true")
	}
	var req map[string]any
	json.Unmarshal(openaiBody, &req)
	if req["stream"] != true {
		t.Error("stream not set in openai request")
	}
	opts, _ := req["stream_options"].(map[string]any)
	if opts == nil || opts["include_usage"] != true {
		t.Error("stream_options.include_usage not set")
	}
}

func TestConvertClaudeToOpenAI_Tools(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 100,
		"messages": [{"role": "user", "content": "weather?"}],
		"tools": [{
			"name": "get_weather",
			"description": "Get weather",
			"input_schema": {"type": "object", "properties": {"city": {"type": "string"}}}
		}],
		"tool_choice": {"type": "auto"}
	}`)

	openaiBody, _, _, err := ConvertClaudeToOpenAI(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var req map[string]any
	json.Unmarshal(openaiBody, &req)

	tools, _ := req["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(tools))
	}
	tool, _ := tools[0].(map[string]any)
	if tool["type"] != "function" {
		t.Errorf("tool type = %v", tool["type"])
	}
	fn, _ := tool["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Errorf("tool name = %v", fn["name"])
	}

	if req["tool_choice"] != "auto" {
		t.Errorf("tool_choice = %v, want auto", req["tool_choice"])
	}
}

func TestConvertClaudeToOpenAI_Thinking(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 100,
		"thinking": {"type": "enabled", "budget_tokens": 30000},
		"messages": [{"role": "user", "content": "think hard"}]
	}`)

	openaiBody, _, _, err := ConvertClaudeToOpenAI(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var req map[string]any
	json.Unmarshal(openaiBody, &req)

	reasoning, _ := req["reasoning"].(map[string]any)
	if reasoning == nil {
		t.Fatal("reasoning is nil")
	}
	if reasoning["effort"] != "high" {
		t.Errorf("effort = %v, want high", reasoning["effort"])
	}
}

func TestConvertClaudeToOpenAI_ThinkingLowBudget(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 100,
		"thinking": {"type": "enabled", "budget_tokens": 2000},
		"messages": [{"role": "user", "content": "quick"}]
	}`)

	openaiBody, _, _, err := ConvertClaudeToOpenAI(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var req map[string]any
	json.Unmarshal(openaiBody, &req)

	reasoning, _ := req["reasoning"].(map[string]any)
	if reasoning["effort"] != "low" {
		t.Errorf("effort = %v, want low", reasoning["effort"])
	}
}

func TestConvertClaudeToOpenAI_ToolUseMessages(t *testing.T) {
	body := []byte(`{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 100,
		"messages": [
			{"role": "user", "content": "weather?"},
			{"role": "assistant", "content": [
				{"type": "tool_use", "id": "call_1", "name": "get_weather", "input": {"city": "Tokyo"}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "call_1", "content": "Sunny 25C"}
			]}
		]
	}`)

	openaiBody, _, _, err := ConvertClaudeToOpenAI(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var req map[string]any
	json.Unmarshal(openaiBody, &req)

	msgs, _ := req["messages"].([]any)
	if len(msgs) < 3 {
		t.Fatalf("messages len = %d, want >= 3", len(msgs))
	}

	// assistant with tool_calls
	assistantMsg, _ := msgs[1].(map[string]any)
	if assistantMsg["role"] != "assistant" {
		t.Errorf("msg[1] role = %v", assistantMsg["role"])
	}
	toolCalls, _ := assistantMsg["tool_calls"].([]any)
	if len(toolCalls) != 1 {
		t.Fatalf("tool_calls len = %d", len(toolCalls))
	}

	// tool result
	toolMsg, _ := msgs[2].(map[string]any)
	if toolMsg["role"] != "tool" {
		t.Errorf("msg[2] role = %v", toolMsg["role"])
	}
	if toolMsg["tool_call_id"] != "call_1" {
		t.Errorf("tool_call_id = %v", toolMsg["tool_call_id"])
	}
}

// ==================== ConvertOpenAIResponseToClaude Tests ====================

func TestConvertOpenAIResponseToClaude_BasicText(t *testing.T) {
	resp := []byte(`{
		"id": "chatcmpl-123",
		"choices": [{
			"message": {"role": "assistant", "content": "Hello!"},
			"finish_reason": "stop"
		}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 5}
	}`)

	claudeBody, usage, err := ConvertOpenAIResponseToClaude(resp, "claude-sonnet-4-20250514")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	json.Unmarshal(claudeBody, &result)

	if result["type"] != "message" {
		t.Errorf("type = %v", result["type"])
	}
	if result["role"] != "assistant" {
		t.Errorf("role = %v", result["role"])
	}
	if result["model"] != "claude-sonnet-4-20250514" {
		t.Errorf("model = %v", result["model"])
	}
	if result["stop_reason"] != "end_turn" {
		t.Errorf("stop_reason = %v", result["stop_reason"])
	}

	content, _ := result["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content len = %d", len(content))
	}
	block, _ := content[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "Hello!" {
		t.Errorf("content block = %v", block)
	}

	if usage.InputTokens != 10 || usage.OutputTokens != 5 {
		t.Errorf("usage = %+v", usage)
	}
}

func TestConvertOpenAIResponseToClaude_ToolCalls(t *testing.T) {
	resp := []byte(`{
		"id": "chatcmpl-456",
		"choices": [{
			"message": {
				"role": "assistant",
				"content": null,
				"tool_calls": [{
					"id": "call_1",
					"type": "function",
					"function": {"name": "get_weather", "arguments": "{\"city\":\"Tokyo\"}"}
				}]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {"prompt_tokens": 20, "completion_tokens": 15}
	}`)

	claudeBody, _, err := ConvertOpenAIResponseToClaude(resp, "claude-sonnet-4-20250514")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	json.Unmarshal(claudeBody, &result)

	if result["stop_reason"] != "tool_use" {
		t.Errorf("stop_reason = %v, want tool_use", result["stop_reason"])
	}

	content, _ := result["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content len = %d, want 1", len(content))
	}
	block, _ := content[0].(map[string]any)
	if block["type"] != "tool_use" {
		t.Errorf("block type = %v", block["type"])
	}
	if block["name"] != "get_weather" {
		t.Errorf("name = %v", block["name"])
	}
	input, _ := block["input"].(map[string]any)
	if input["city"] != "Tokyo" {
		t.Errorf("input = %v", input)
	}
}

func TestConvertOpenAIResponseToClaude_Reasoning(t *testing.T) {
	resp := []byte(`{
		"id": "chatcmpl-789",
		"choices": [{
			"message": {
				"role": "assistant",
				"content": "The answer is 42.",
				"reasoning": {"content": "Let me think about this..."}
			},
			"finish_reason": "stop"
		}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 20}
	}`)

	claudeBody, _, err := ConvertOpenAIResponseToClaude(resp, "claude-opus-4-6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	json.Unmarshal(claudeBody, &result)

	content, _ := result["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content len = %d, want 2 (thinking + text)", len(content))
	}
	// First block should be thinking
	thinking, _ := content[0].(map[string]any)
	if thinking["type"] != "thinking" {
		t.Errorf("first block type = %v, want thinking", thinking["type"])
	}
	if thinking["thinking"] != "Let me think about this..." {
		t.Errorf("thinking content = %v", thinking["thinking"])
	}
	// Second block should be text
	text, _ := content[1].(map[string]any)
	if text["type"] != "text" || text["text"] != "The answer is 42." {
		t.Errorf("text block = %v", text)
	}
}

// ==================== ClaudeStreamConverter Tests ====================

func TestClaudeStreamConverter_TextDelta(t *testing.T) {
	conv := NewClaudeStreamConverter("claude-sonnet-4-20250514", "msg_test123")

	start := conv.BuildMessageStart()
	if start == "" {
		t.Error("BuildMessageStart returned empty")
	}

	chunk := `{"choices":[{"delta":{"content":"Hello"},"index":0}]}`
	events := conv.ConvertChunk([]byte(chunk))
	if events == "" {
		t.Error("ConvertChunk returned empty for text delta")
	}

	// Should contain content_block_start and content_block_delta
	if !containsStr(events, "content_block_start") {
		t.Error("missing content_block_start")
	}
	if !containsStr(events, "text_delta") {
		t.Error("missing text_delta")
	}
}

func TestClaudeStreamConverter_FinishReason(t *testing.T) {
	conv := NewClaudeStreamConverter("claude-sonnet-4-20250514", "msg_test123")

	// Send text first
	conv.ConvertChunk([]byte(`{"choices":[{"delta":{"content":"Hi"},"index":0}]}`))

	// Send finish
	events := conv.ConvertChunk([]byte(`{"choices":[{"delta":{},"finish_reason":"stop","index":0}]}`))
	if !containsStr(events, "message_delta") {
		t.Error("missing message_delta")
	}
	if !containsStr(events, "end_turn") {
		t.Error("missing end_turn stop_reason")
	}
}

func TestClaudeStreamConverter_ToolCallDelta(t *testing.T) {
	conv := NewClaudeStreamConverter("claude-sonnet-4-20250514", "msg_test123")

	chunk := `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"get_weather","arguments":""}}]},"index":0}]}`
	events := conv.ConvertChunk([]byte(chunk))
	if !containsStr(events, "tool_use") {
		t.Error("missing tool_use in content_block_start")
	}
	if !conv.SawToolUse() {
		t.Error("SawToolUse should be true")
	}

	// Stream arguments
	chunk2 := `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":\"Tokyo\"}"}}]},"index":0}]}`
	events2 := conv.ConvertChunk([]byte(chunk2))
	if !containsStr(events2, "input_json_delta") {
		t.Error("missing input_json_delta")
	}
}

func TestClaudeStreamConverter_ReasoningDelta(t *testing.T) {
	conv := NewClaudeStreamConverter("claude-opus-4-6", "msg_test123")

	chunk := `{"choices":[{"delta":{"reasoning":{"content":"Let me think..."}},"index":0}]}`
	events := conv.ConvertChunk([]byte(chunk))
	if !containsStr(events, "thinking") {
		t.Error("missing thinking block")
	}
	if !containsStr(events, "thinking_delta") {
		t.Error("missing thinking_delta")
	}
}

func TestClaudeStreamConverter_MessageStop(t *testing.T) {
	conv := NewClaudeStreamConverter("claude-sonnet-4-20250514", "msg_test123")
	stop := conv.BuildMessageStop()
	if !containsStr(stop, "message_stop") {
		t.Error("missing message_stop")
	}
}

// ==================== Model Mapping Tests ====================

func TestGetOpenAIModelID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"claude-sonnet-4-20250514", "gpt-5.2-codex"},
		{"claude-sonnet-4-6", "gpt-5.2-codex"},
		{"claude-opus-4-6", "gpt-5.3-codex"},
		{"claude-opus-4-5", "gpt-5.3-codex"},
		{"claude-haiku-4-5", "gpt-5.2-codex"},
		{"unknown-model", "gpt-5.3-codex"},
		{"some-sonnet-variant", "gpt-5.2-codex"},
		{"some-opus-variant", "gpt-5.3-codex"},
	}
	for _, tt := range tests {
		got := GetOpenAIModelID(tt.input)
		if got != tt.want {
			t.Errorf("GetOpenAIModelID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func containsStr(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && json.Valid([]byte("null")) && // just to use json
		(len(s) >= len(substr) && searchStr(s, substr))
}

func searchStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
