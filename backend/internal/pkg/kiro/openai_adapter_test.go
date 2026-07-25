package kiro

import (
	"encoding/json"
	"strings"
	"testing"
)

// ==================== ConvertOpenAIToClaude Tests ====================

func TestConvertOpenAIToClaude_BasicMessages(t *testing.T) {
	body := `{
		"model": "claude-sonnet-4-20250514",
		"stream": true,
		"max_tokens": 4096,
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": "Hi there!"},
			{"role": "user", "content": "How are you?"}
		]
	}`

	req, err := ConvertOpenAIToClaude([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if req.Model != "claude-sonnet-4-20250514" {
		t.Errorf("model: got %s, want claude-sonnet-4-20250514", req.Model)
	}
	if !req.Stream {
		t.Error("stream: got false, want true")
	}
	if req.MaxTokens != 4096 {
		t.Errorf("max_tokens: got %d, want 4096", req.MaxTokens)
	}

	// System should be extracted
	systemStr, ok := req.System.(string)
	if !ok || systemStr != "You are a helpful assistant." {
		t.Errorf("system: got %v, want 'You are a helpful assistant.'", req.System)
	}

	// Should have 3 messages (user, assistant, user) — system extracted
	if len(req.Messages) != 3 {
		t.Fatalf("messages: got %d, want 3", len(req.Messages))
	}
	if req.Messages[0].Role != "user" {
		t.Errorf("messages[0].role: got %s, want user", req.Messages[0].Role)
	}
	if req.Messages[1].Role != "assistant" {
		t.Errorf("messages[1].role: got %s, want assistant", req.Messages[1].Role)
	}
	if req.Messages[2].Role != "user" {
		t.Errorf("messages[2].role: got %s, want user", req.Messages[2].Role)
	}
}

func TestConvertOpenAIToClaude_StreamDefaultsToFalse(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hello"}]}`)

	req, err := ConvertOpenAIToClaude(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Stream {
		t.Error("stream: got true, want false when omitted")
	}
}

func TestConvertOpenAIToClaude_ToolCalls(t *testing.T) {
	body := `{
		"model": "claude-sonnet-4-20250514",
		"messages": [
			{"role": "user", "content": "What's the weather?"},
			{
				"role": "assistant",
				"content": null,
				"tool_calls": [{
					"id": "call_123",
					"type": "function",
					"function": {
						"name": "get_weather",
						"arguments": "{\"city\":\"Tokyo\"}"
					}
				}]
			},
			{
				"role": "tool",
				"tool_call_id": "call_123",
				"content": "Sunny, 25°C"
			}
		],
		"tools": [{
			"type": "function",
			"function": {
				"name": "get_weather",
				"description": "Get weather info",
				"parameters": {
					"type": "object",
					"properties": {
						"city": {"type": "string"}
					},
					"required": ["city"]
				}
			}
		}]
	}`

	req, err := ConvertOpenAIToClaude([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 3 messages: user, assistant (with tool_use), user (with tool_result)
	if len(req.Messages) != 3 {
		t.Fatalf("messages: got %d, want 3", len(req.Messages))
	}

	// Check assistant message has tool_use block
	assistantContent, ok := req.Messages[1].Content.([]any)
	if !ok {
		t.Fatal("assistant content should be []any")
	}
	foundToolUse := false
	for _, block := range assistantContent {
		blockMap, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if blockMap["type"] == "tool_use" {
			foundToolUse = true
			if blockMap["id"] != "call_123" {
				t.Errorf("tool_use id: got %v, want call_123", blockMap["id"])
			}
			if blockMap["name"] != "get_weather" {
				t.Errorf("tool_use name: got %v, want get_weather", blockMap["name"])
			}
			// Input should be parsed JSON
			input, ok := blockMap["input"].(map[string]any)
			if !ok {
				t.Fatal("tool_use input should be map[string]any")
			}
			if input["city"] != "Tokyo" {
				t.Errorf("tool_use input.city: got %v, want Tokyo", input["city"])
			}
		}
	}
	if !foundToolUse {
		t.Error("assistant message should contain tool_use block")
	}

	// Check tool result message
	toolResultContent, ok := req.Messages[2].Content.([]any)
	if !ok {
		t.Fatal("tool result content should be []any")
	}
	if len(toolResultContent) != 1 {
		t.Fatalf("tool result blocks: got %d, want 1", len(toolResultContent))
	}
	trBlock, ok := toolResultContent[0].(map[string]any)
	if !ok {
		t.Fatal("tool result block should be map[string]any")
	}
	if trBlock["type"] != "tool_result" {
		t.Errorf("tool result type: got %v, want tool_result", trBlock["type"])
	}
	if trBlock["tool_use_id"] != "call_123" {
		t.Errorf("tool_use_id: got %v, want call_123", trBlock["tool_use_id"])
	}

	// Check tools conversion
	if len(req.Tools) != 1 {
		t.Fatalf("tools: got %d, want 1", len(req.Tools))
	}
	if req.Tools[0].Name != "get_weather" {
		t.Errorf("tool name: got %s, want get_weather", req.Tools[0].Name)
	}
	if req.Tools[0].Description != "Get weather info" {
		t.Errorf("tool description: got %s, want 'Get weather info'", req.Tools[0].Description)
	}
}

func TestConvertOpenAIToClaude_ToolChoice(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantType string
	}{
		{"none", `"none"`, "none"},
		{"auto", `"auto"`, "auto"},
		{"required", `"required"`, "any"},
		{"specific", `{"type":"function","function":{"name":"foo"}}`, "tool"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"model":"test","messages":[{"role":"user","content":"hi"}],"tool_choice":` + tt.input + `}`
			req, err := ConvertOpenAIToClaude([]byte(body))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tcMap, ok := req.ToolChoice.(map[string]any)
			if !ok {
				t.Fatalf("tool_choice should be map, got %T", req.ToolChoice)
			}
			if tcMap["type"] != tt.wantType {
				t.Errorf("tool_choice type: got %v, want %s", tcMap["type"], tt.wantType)
			}
		})
	}
}

func TestConvertOpenAIToClaude_MultipleSystemMessages(t *testing.T) {
	body := `{
		"model": "test",
		"messages": [
			{"role": "system", "content": "Part 1."},
			{"role": "system", "content": "Part 2."},
			{"role": "user", "content": "Hello"}
		]
	}`

	req, err := ConvertOpenAIToClaude([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	systemStr, ok := req.System.(string)
	if !ok {
		t.Fatal("system should be string")
	}
	if !strings.Contains(systemStr, "Part 1.") || !strings.Contains(systemStr, "Part 2.") {
		t.Errorf("system should contain both parts, got: %s", systemStr)
	}
}

func TestConvertOpenAIToClaude_NoSystem(t *testing.T) {
	body := `{
		"model": "test",
		"messages": [
			{"role": "user", "content": "Hello"}
		]
	}`

	req, err := ConvertOpenAIToClaude([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if req.System != nil {
		t.Errorf("system should be nil, got %v", req.System)
	}
}

func TestConvertOpenAIToClaude_MaxCompletionTokens(t *testing.T) {
	body := `{
		"model": "test",
		"max_completion_tokens": 8192,
		"messages": [{"role": "user", "content": "Hello"}]
	}`

	req, err := ConvertOpenAIToClaude([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if req.MaxTokens != 8192 {
		t.Errorf("max_tokens: got %d, want 8192", req.MaxTokens)
	}
}

func TestConvertOpenAIToClaude_ImageContent(t *testing.T) {
	body := `{
		"model": "test",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "What is this?"},
				{"type": "image_url", "image_url": {"url": "https://example.com/img.png"}}
			]
		}]
	}`

	req, err := ConvertOpenAIToClaude([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content, ok := req.Messages[0].Content.([]any)
	if !ok {
		t.Fatal("content should be []any")
	}
	if len(content) != 2 {
		t.Fatalf("content blocks: got %d, want 2", len(content))
	}

	imgBlock, ok := content[1].(map[string]any)
	if !ok {
		t.Fatal("image block should be map")
	}
	if imgBlock["type"] != "image" {
		t.Errorf("image block type: got %v, want image", imgBlock["type"])
	}
}

// ==================== OpenAIStreamConverter Tests ====================

func TestOpenAIStreamConverter_TextDelta(t *testing.T) {
	conv := NewOpenAIStreamConverter("chatcmpl-test", "claude-sonnet-4-20250514", 100)

	// Block start (text)
	result := conv.ConvertEvent(StreamEvent{
		Type:      EventContentBlockStart,
		BlockType: ContentBlockType{Kind: BlockText},
	})
	if result != "" {
		t.Errorf("text block start should produce no output, got: %s", result)
	}

	// Text delta
	result = conv.ConvertEvent(StreamEvent{
		Type: EventTextDelta,
		Text: "Hello world",
	})
	if result == "" {
		t.Fatal("text delta should produce output")
	}
	if !strings.Contains(result, "Hello world") {
		t.Errorf("output should contain text delta, got: %s", result)
	}
	if !strings.Contains(result, "chat.completion.chunk") {
		t.Errorf("output should be chat.completion.chunk format, got: %s", result)
	}
}

func TestOpenAIStreamConverter_ToolUse(t *testing.T) {
	conv := NewOpenAIStreamConverter("chatcmpl-test", "test-model", 100)

	// Tool block start
	result := conv.ConvertEvent(StreamEvent{
		Type: EventContentBlockStart,
		BlockType: ContentBlockType{
			Kind:     BlockToolUse,
			ToolID:   "call_abc",
			ToolName: "get_weather",
		},
	})
	if result == "" {
		t.Fatal("tool block start should produce output")
	}
	if !strings.Contains(result, "get_weather") {
		t.Errorf("should contain tool name, got: %s", result)
	}
	if !strings.Contains(result, "call_abc") {
		t.Errorf("should contain tool call id, got: %s", result)
	}

	if !conv.SawToolUse() {
		t.Error("SawToolUse should be true")
	}

	// Tool input delta
	result = conv.ConvertEvent(StreamEvent{
		Type:        EventToolUseInputDelta,
		PartialJSON: `{"city":"Tokyo"}`,
	})
	if result == "" {
		t.Fatal("tool input delta should produce output")
	}
	// JSON is embedded inside another JSON string, so quotes are escaped
	if !strings.Contains(result, `city`) || !strings.Contains(result, `Tokyo`) {
		t.Errorf("should contain partial JSON content, got: %s", result)
	}
}

func TestOpenAIStreamConverter_ThinkingSkipped(t *testing.T) {
	conv := NewOpenAIStreamConverter("chatcmpl-test", "test-model", 100)

	result := conv.ConvertEvent(StreamEvent{
		Type: EventThinkingDelta,
		Text: "Let me think...",
	})
	if result != "" {
		t.Errorf("thinking delta should be skipped, got: %s", result)
	}
}

func TestOpenAIStreamConverter_FinalEvent(t *testing.T) {
	conv := NewOpenAIStreamConverter("chatcmpl-test", "test-model", 100)
	conv.SetCacheTokens(50, 30)

	// Simulate some text output
	conv.ConvertEvent(StreamEvent{Type: EventContentBlockStart, BlockType: ContentBlockType{Kind: BlockText}})
	conv.ConvertEvent(StreamEvent{Type: EventTextDelta, Text: "Hello"})

	final := conv.BuildFinalEvent()

	if !strings.Contains(final, "stop") {
		t.Error("final should contain finish_reason=stop")
	}
	if !strings.Contains(final, "[DONE]") {
		t.Error("final should contain [DONE]")
	}
	if !strings.Contains(final, "prompt_tokens") {
		t.Error("final should contain usage")
	}
}

func TestOpenAIStreamConverter_ToolUseFinishReason(t *testing.T) {
	conv := NewOpenAIStreamConverter("chatcmpl-test", "test-model", 100)

	// Simulate tool use
	conv.ConvertEvent(StreamEvent{
		Type:      EventContentBlockStart,
		BlockType: ContentBlockType{Kind: BlockToolUse, ToolID: "call_1", ToolName: "foo"},
	})

	final := conv.BuildFinalEvent()
	if !strings.Contains(final, "tool_calls") {
		t.Error("final should contain finish_reason=tool_calls when tool use was seen")
	}
}

// ==================== BuildOpenAINonStreamResponse Tests ====================

func TestBuildOpenAINonStreamResponse_TextOnly(t *testing.T) {
	resp := &CompleteResponse{
		Text: "Hello world",
	}

	result := BuildOpenAINonStreamResponse("chatcmpl-test", "test-model", 100, 10, resp, 0, 0)
	if result == nil {
		t.Fatal("result should not be nil")
	}

	choices, ok := result["choices"].([]any)
	if !ok || len(choices) != 1 {
		t.Fatal("should have 1 choice")
	}
	choice := choices[0].(map[string]any)
	msg := choice["message"].(map[string]any)

	if msg["content"] != "Hello world" {
		t.Errorf("content: got %v, want 'Hello world'", msg["content"])
	}
	if choice["finish_reason"] != "stop" {
		t.Errorf("finish_reason: got %v, want stop", choice["finish_reason"])
	}
}

func TestBuildOpenAINonStreamResponse_WithToolCalls(t *testing.T) {
	resp := &CompleteResponse{
		ToolCalls: []ToolCallData{
			{ID: "call_1", Name: "get_weather", ArgumentsRaw: `{"city":"Tokyo"}`},
		},
	}

	result := BuildOpenAINonStreamResponse("chatcmpl-test", "test-model", 100, 20, resp, 0, 0)
	if result == nil {
		t.Fatal("result should not be nil")
	}

	choices := result["choices"].([]any)
	choice := choices[0].(map[string]any)

	if choice["finish_reason"] != "tool_calls" {
		t.Errorf("finish_reason: got %v, want tool_calls", choice["finish_reason"])
	}

	msg := choice["message"].(map[string]any)
	toolCalls, ok := msg["tool_calls"].([]map[string]any)
	if !ok || len(toolCalls) != 1 {
		t.Fatal("should have 1 tool call")
	}
	if toolCalls[0]["id"] != "call_1" {
		t.Errorf("tool call id: got %v, want call_1", toolCalls[0]["id"])
	}
	fn := toolCalls[0]["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Errorf("tool call name: got %v, want get_weather", fn["name"])
	}
}

func TestBuildOpenAINonStreamResponse_WithCache(t *testing.T) {
	resp := &CompleteResponse{Text: "Hi"}
	result := BuildOpenAINonStreamResponse("chatcmpl-test", "test-model", 100, 5, resp, 50, 30)

	usage := result["usage"].(map[string]any)
	if usage["cache_creation_input_tokens"] != 50 {
		t.Errorf("cache_creation: got %v, want 50", usage["cache_creation_input_tokens"])
	}
	if usage["cache_read_input_tokens"] != 30 {
		t.Errorf("cache_read: got %v, want 30", usage["cache_read_input_tokens"])
	}
}

func TestBuildOpenAINonStreamResponse_Nil(t *testing.T) {
	result := BuildOpenAINonStreamResponse("id", "model", 0, 0, nil, 0, 0)
	if result != nil {
		t.Error("nil response should return nil")
	}
}

// ==================== Round-trip JSON Test ====================

func TestConvertOpenAIToClaude_RoundTripJSON(t *testing.T) {
	// Verify the converted request can be serialized to valid JSON
	body := `{
		"model": "claude-sonnet-4-20250514",
		"stream": true,
		"temperature": 0.7,
		"max_tokens": 4096,
		"messages": [
			{"role": "system", "content": "Be helpful."},
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": "Hi!", "tool_calls": [{"id": "c1", "type": "function", "function": {"name": "test", "arguments": "{}"}}]},
			{"role": "tool", "tool_call_id": "c1", "content": "result"},
			{"role": "user", "content": "Thanks"}
		],
		"tools": [{"type": "function", "function": {"name": "test", "description": "A test tool", "parameters": {"type": "object"}}}]
	}`

	req, err := ConvertOpenAIToClaude([]byte(body))
	if err != nil {
		t.Fatalf("convert error: %v", err)
	}

	// Should be serializable
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	if len(data) == 0 {
		t.Error("serialized data should not be empty")
	}

	t.Logf("Converted request size: %d bytes", len(data))
}
