package kiro

import (
	"encoding/json"
	"testing"
)

func TestExtractTokenUsage_NormalizesInputWithCache(t *testing.T) {
	input, output, cacheCreation, cacheRead, has := extractTokenUsage(map[string]any{
		"inputTokens":              float64(120),
		"outputTokens":             float64(30),
		"cacheCreationInputTokens": float64(10),
		"cacheReadInputTokens":     float64(20),
	})
	if !has {
		t.Fatal("expected has=true")
	}
	if input != 90 {
		t.Fatalf("input=%d, want 90", input)
	}
	if output != 30 {
		t.Fatalf("output=%d, want 30", output)
	}
	if cacheCreation != 10 {
		t.Fatalf("cacheCreation=%d, want 10", cacheCreation)
	}
	if cacheRead != 20 {
		t.Fatalf("cacheRead=%d, want 20", cacheRead)
	}
}

func TestAwsEventStreamParser_EmitsTokenUsageEvent(t *testing.T) {
	parser := NewAwsEventStreamParser("msg_test", "claude-sonnet-4-20250514")
	events := parser.Process([]byte(`{"tokenUsage":{"inputTokens":120,"outputTokens":30,"cacheCreationInputTokens":10,"cacheReadInputTokens":20,"contextUsagePercentage":55.5}}`))

	var usageEvent *StreamEvent
	for i := range events {
		if events[i].Type == EventBackendUsage {
			usageEvent = &events[i]
			break
		}
	}
	if usageEvent == nil {
		t.Fatalf("expected EventBackendUsage, got events=%v", events)
	}
	if !usageEvent.HasTokenUsage {
		t.Fatal("expected HasTokenUsage=true")
	}
	if usageEvent.InputTokens != 90 {
		t.Fatalf("input_tokens=%d, want 90", usageEvent.InputTokens)
	}
	if usageEvent.OutputTokens != 30 {
		t.Fatalf("output_tokens=%d, want 30", usageEvent.OutputTokens)
	}
	if usageEvent.CacheCreationInputTokens != 10 {
		t.Fatalf("cache_creation_input_tokens=%d, want 10", usageEvent.CacheCreationInputTokens)
	}
	if usageEvent.CacheReadInputTokens != 20 {
		t.Fatalf("cache_read_input_tokens=%d, want 20", usageEvent.CacheReadInputTokens)
	}
	if usageEvent.ContextPercentage != 55.5 {
		t.Fatalf("context_usage_percentage=%f, want 55.5", usageEvent.ContextPercentage)
	}
}

func TestParseCompleteResponseWithNameRestore_UsesUpstreamTokenUsage(t *testing.T) {
	// Simulate non-stream payload composed of multiple JSON event objects.
	payload := []byte(`{"content":"hello"}{"tokenUsage":{"inputTokens":42,"outputTokens":7,"cacheReadInputTokens":5}}{"stop":true}`)
	resp := ParseCompleteResponseWithNameRestore(payload, nil)

	if resp == nil {
		t.Fatal("resp should not be nil")
	}
	if !resp.HasTokenUsage {
		t.Fatal("expected HasTokenUsage=true")
	}
	if resp.InputTokens != 37 {
		t.Fatalf("input_tokens=%d, want 37", resp.InputTokens)
	}
	if resp.OutputTokens != 7 {
		t.Fatalf("output_tokens=%d, want 7", resp.OutputTokens)
	}
	if resp.CacheReadTokens != 5 {
		t.Fatalf("cache_read_input_tokens=%d, want 5", resp.CacheReadTokens)
	}
}

func TestAwsEventStreamParser_EnterPlanModeEmptyInputDoesNotInjectSoftLimit(t *testing.T) {
	parser := NewAwsEventStreamParser("msg_test", "claude-sonnet-4-20250514")
	events := parser.Process([]byte(`{"toolUseId":"toolu_plan","name":"EnterPlanMode","stop":true}`))
	events = append(events, parser.Finish()...)

	var sawToolStart, sawToolStop bool
	for _, event := range events {
		switch event.Type {
		case EventContentBlockStart:
			if event.BlockType.Kind == BlockToolUse && event.BlockType.ToolName == "EnterPlanMode" {
				sawToolStart = true
			}
		case EventToolUseInputDelta:
			t.Fatalf("EnterPlanMode with empty input should not emit input delta, got %q", event.PartialJSON)
		case EventToolUseStop:
			sawToolStop = true
		}
	}
	if !sawToolStart {
		t.Fatal("expected EnterPlanMode tool_use start")
	}
	if !sawToolStop {
		t.Fatal("expected EnterPlanMode tool_use stop")
	}
}

func TestBuildClaudeNonStreamResponse_EnterPlanModeEmptyInputIsEmptyObject(t *testing.T) {
	payload := []byte(`{"toolUseId":"toolu_plan","name":"EnterPlanMode","stop":true}`)
	resp := ParseCompleteResponseWithNameRestore(payload, nil)
	claudeResp := BuildClaudeNonStreamResponse("msg_test", "claude-sonnet-4-20250514", 1, resp)

	content, ok := claudeResp["content"].([]map[string]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content = %#v, want one tool_use block", claudeResp["content"])
	}
	input, ok := content[0]["input"].(map[string]any)
	if !ok {
		t.Fatalf("input = %#v, want map", content[0]["input"])
	}
	if len(input) != 0 {
		t.Fatalf("EnterPlanMode input = %#v, want empty object", input)
	}
}

func TestAwsEventStreamParser_ThinkingTagsMaximallySplit(t *testing.T) {
	parser := NewAwsEventStreamParser("msg_test", "claude-opus-4-6")
	var events []StreamEvent
	for _, part := range []string{
		"\n",
		"\n",
		"<thin",
		"king>",
		"\n",
		"hello",
		"</thi",
		"nking>",
		"\n",
		"\n",
		"world",
	} {
		events = append(events, parser.Process(mustContentPayload(t, part))...)
	}
	events = append(events, parser.Finish()...)

	var thinking, text string
	for _, event := range events {
		switch event.Type {
		case EventThinkingDelta:
			thinking += event.Text
		case EventTextDelta:
			text += event.Text
		}
	}
	if thinking != "hello" {
		t.Fatalf("thinking = %q, want %q", thinking, "hello")
	}
	if text != "world" {
		t.Fatalf("text = %q, want %q", text, "world")
	}
}

func TestStreamEventConverter_ThinkingStopEmitsSignature(t *testing.T) {
	converter := NewStreamEventConverter("msg_test", "claude-opus-4-6", 1)
	var events []ClaudeSSEEvent
	events = append(events, converter.ConvertEvent(StreamEvent{
		Type:      EventContentBlockStart,
		Index:     0,
		BlockType: ContentBlockType{Kind: BlockThinking},
	})...)
	events = append(events, converter.ConvertEvent(StreamEvent{Type: EventThinkingDelta, Index: 0, Text: "secret"})...)
	events = append(events, converter.ConvertEvent(StreamEvent{Type: EventContentBlockStop, Index: 0})...)

	if len(events) < 4 {
		t.Fatalf("events len = %d, want at least 4: %#v", len(events), events)
	}
	signatureEvent := events[len(events)-2]
	delta, _ := signatureEvent.Data["delta"].(map[string]any)
	if delta["type"] != "signature_delta" {
		t.Fatalf("penultimate event delta type = %v, want signature_delta; events=%#v", delta["type"], events)
	}
	if delta["signature"] == "" {
		t.Fatal("signature_delta signature should not be empty")
	}
	if events[len(events)-1].EventType != "content_block_stop" {
		t.Fatalf("last event = %s, want content_block_stop", events[len(events)-1].EventType)
	}
}

func TestBuildClaudeNonStreamResponse_IncludesThinkingBlock(t *testing.T) {
	payload := append(mustContentPayload(t, "<thinking>\nsecret</thinking>\n\nanswer"), []byte(`{"stop":true}`)...)
	resp := ParseCompleteResponseWithNameRestore(payload, nil)
	claudeResp := BuildClaudeNonStreamResponse("msg_test", "claude-opus-4-6", 1, resp)

	content, ok := claudeResp["content"].([]map[string]any)
	if !ok || len(content) != 2 {
		t.Fatalf("content = %#v, want thinking + text blocks", claudeResp["content"])
	}
	if content[0]["type"] != "thinking" {
		t.Fatalf("first block type = %v, want thinking", content[0]["type"])
	}
	if content[0]["thinking"] != "secret" {
		t.Fatalf("thinking = %q, want secret", content[0]["thinking"])
	}
	if content[0]["signature"] == "" {
		t.Fatal("thinking signature should not be empty")
	}
	if content[1]["type"] != "text" || content[1]["text"] != "answer" {
		t.Fatalf("second block = %#v, want text answer", content[1])
	}
}

func mustContentPayload(t *testing.T, content string) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
