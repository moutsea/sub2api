package kiro

import (
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"strings"
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
	events := parser.Process(mustEventFrame(t, "messageMetadataEvent", map[string]any{
		"tokenUsage": map[string]any{
			"inputTokens":              120,
			"outputTokens":             30,
			"cacheCreationInputTokens": 10,
			"cacheReadInputTokens":     20,
			"contextUsagePercentage":   55.5,
		},
	}))

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
	payload := append(mustContentPayload(t, "hello"), mustEventFrame(t, "messageMetadataEvent", map[string]any{
		"tokenUsage": map[string]any{"inputTokens": 42, "outputTokens": 7, "cacheReadInputTokens": 5},
	})...)
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
	events := parser.Process(mustEventFrame(t, "toolUseEvent", map[string]any{
		"toolUseId": "toolu_plan",
		"name":      "EnterPlanMode",
		"stop":      true,
	}))
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

func TestAwsEventStreamParser_AccumulatesToolInputAcrossFrames(t *testing.T) {
	parser := NewAwsEventStreamParser("msg_test", "claude-sonnet-4-20250514")
	payload := append(
		mustEventFrame(t, "toolUseEvent", map[string]any{
			"toolUseId": "toolu_multi",
			"name":      "Read",
			"input":     `{"path":"`,
		}),
		mustEventFrame(t, "toolUseEvent", map[string]any{
			"toolUseId": "toolu_multi",
			"input":     "file.txt\"",
		})...,
	)
	payload = append(payload, mustEventFrame(t, "toolUseEvent", map[string]any{
		"toolUseId": "toolu_multi",
		"input":     `}`,
		"stop":      true,
	})...)

	var events []StreamEvent
	for offset := 0; offset < len(payload); offset += 7 {
		end := offset + 7
		if end > len(payload) {
			end = len(payload)
		}
		events = append(events, parser.Process(payload[offset:end])...)
	}
	events = append(events, parser.Finish()...)

	var starts, stops, errors int
	var input strings.Builder
	for _, event := range events {
		switch event.Type {
		case EventContentBlockStart:
			if event.BlockType.Kind == BlockToolUse {
				starts++
			}
		case EventToolUseInputDelta:
			input.WriteString(event.PartialJSON)
		case EventToolUseStop:
			stops++
		case EventError:
			errors++
		}
	}

	if starts != 1 || stops != 1 || errors != 0 {
		t.Fatalf("starts=%d stops=%d errors=%d, events=%#v", starts, stops, errors, events)
	}
	if got := input.String(); got != `{"path":"file.txt"}` {
		t.Fatalf("tool input = %q", got)
	}
}

func TestBuildClaudeNonStreamResponse_EnterPlanModeEmptyInputIsEmptyObject(t *testing.T) {
	payload := mustEventFrame(t, "toolUseEvent", map[string]any{
		"toolUseId": "toolu_plan",
		"name":      "EnterPlanMode",
		"stop":      true,
	})
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
	parser.SetThinkingEnabled(true)
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

func TestAwsEventStreamParser_ThinkingEnabledKeepsDelayedStartPreludeAsText(t *testing.T) {
	parser := NewAwsEventStreamParser("msg_test", "claude-opus-4-6")
	parser.SetThinkingEnabled(true)

	var events []StreamEvent
	for _, part := range []string{
		"I need to reason privately before the tag. ",
		"<thinking>\nsecret</thinking>\n\nanswer",
	} {
		events = append(events, parser.Process(mustContentPayload(t, part))...)
	}
	events = append(events, parser.Finish()...)

	thinking, text := collectThinkingAndText(events)
	if thinking != "secret" {
		t.Fatalf("thinking = %q, want secret", thinking)
	}
	if text != "I need to reason privately before the tag. answer" {
		t.Fatalf("text = %q, want prelude + answer", text)
	}
}

func TestAwsEventStreamParser_ThinkingEnabledUntaggedContentRemainsText(t *testing.T) {
	parser := NewAwsEventStreamParser("msg_test", "claude-opus-4-6")
	parser.SetThinkingEnabled(true)

	answer := "This is a normal answer without thinking tags."
	var events []StreamEvent
	events = append(events, parser.Process(mustContentPayload(t, answer))...)
	events = append(events, parser.Finish()...)

	thinking, text := collectThinkingAndText(events)
	if thinking != "" {
		t.Fatalf("thinking = %q, want empty", thinking)
	}
	if text != answer {
		t.Fatalf("text = %q, want answer", text)
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
	payload := mustContentPayload(t, "<thinking>\nsecret</thinking>\n\nanswer")
	resp := ParseCompleteResponseWithNameRestoreAndThinking(payload, nil, true)
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

func TestAwsEventStreamParser_IgnoresContentFromUnknownEvent(t *testing.T) {
	parser := NewAwsEventStreamParser("msg_test", "claude-sonnet-4-20250514")
	payload := append(
		mustEventFrame(t, "followupPromptEvent", map[string]any{"content": "course"}),
		mustContentPayload(t, "answer")...,
	)
	events := parser.Process(payload)
	events = append(events, parser.Finish()...)

	_, text := collectThinkingAndText(events)
	if text != "answer" {
		t.Fatalf("text = %q, want answer", text)
	}
}

func TestAwsEventStreamParser_DecodesFrameAcrossChunks(t *testing.T) {
	parser := NewAwsEventStreamParser("msg_test", "claude-sonnet-4-20250514")
	frame := mustContentPayload(t, "chunked")
	var events []StreamEvent
	for offset := 0; offset < len(frame); offset += 3 {
		end := offset + 3
		if end > len(frame) {
			end = len(frame)
		}
		events = append(events, parser.Process(frame[offset:end])...)
	}
	events = append(events, parser.Finish()...)

	_, text := collectThinkingAndText(events)
	if text != "chunked" {
		t.Fatalf("text = %q, want chunked", text)
	}
}

func TestAwsEventStreamParser_DecodesAggregateLargerThanBufferLimit(t *testing.T) {
	parser := NewAwsEventStreamParser("msg_test", "claude-sonnet-4-20250514")
	first := mustContentPayload(t, "first")
	second := mustContentPayload(t, "second")
	parser.SetMaxBufferSize(len(first) + 1)

	events := parser.Process(append(first, second...))
	events = append(events, parser.Finish()...)

	_, text := collectThinkingAndText(events)
	if text != "firstsecond" {
		t.Fatalf("text = %q, want firstsecond", text)
	}
}

func TestAwsEventStreamParser_RejectsInvalidMessageCRC(t *testing.T) {
	parser := NewAwsEventStreamParser("msg_test", "claude-sonnet-4-20250514")
	frame := mustContentPayload(t, "must not leak")
	frame[len(frame)-1] ^= 0xff
	events := parser.Process(frame)
	events = append(events, parser.Finish()...)

	_, text := collectThinkingAndText(events)
	if text != "" {
		t.Fatalf("text = %q, want empty", text)
	}
	if parser.ParseErrorCount() != 1 {
		t.Fatalf("parse errors = %d, want 1", parser.ParseErrorCount())
	}
	if len(events) != 1 || events[0].Type != EventError {
		t.Fatalf("events = %#v, want one EventError", events)
	}
}

func TestAwsEventStreamParser_ValidFrameFollowedByInvalidCRCStopsWithError(t *testing.T) {
	parser := NewAwsEventStreamParser("msg_test", "claude-sonnet-4-20250514")
	validFrame := mustContentPayload(t, "partial")
	invalidFrame := mustContentPayload(t, "must not leak")
	invalidFrame[len(invalidFrame)-1] ^= 0xff

	events := parser.Process(append(validFrame, invalidFrame...))
	events = append(events, parser.Finish()...)

	_, text := collectThinkingAndText(events)
	if text != "partial" {
		t.Fatalf("text = %q, want partial", text)
	}
	if events[len(events)-1].Type != EventError {
		t.Fatalf("last event = %#v, want EventError", events[len(events)-1])
	}
	for _, event := range events {
		if event.Type == EventMessageStop {
			t.Fatalf("events contain normal message stop after CRC failure: %#v", events)
		}
	}
}

func TestParseCompleteResponseStrictRejectsBadCRC(t *testing.T) {
	frame := mustContentPayload(t, "must not parse")
	frame[len(frame)-1] ^= 0xff

	resp, err := ParseCompleteResponseWithNameRestoreAndThinkingStrict(frame, nil, false)
	if err == nil {
		t.Fatal("expected CRC validation error")
	}
	if resp != nil {
		t.Fatalf("response = %+v, want nil", resp)
	}
	if !strings.Contains(err.Error(), "CRC mismatch") {
		t.Fatalf("error = %q, want CRC mismatch", err)
	}
}

func collectThinkingAndText(events []StreamEvent) (string, string) {
	var thinking, text string
	for _, event := range events {
		switch event.Type {
		case EventThinkingDelta:
			thinking += event.Text
		case EventTextDelta:
			text += event.Text
		}
	}
	return thinking, text
}

func mustContentPayload(t *testing.T, content string) []byte {
	t.Helper()
	return mustEventFrame(t, "assistantResponseEvent", map[string]string{"content": content})
}

func mustEventFrame(t *testing.T, eventType string, body any) []byte {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	headers := appendEventStreamStringHeader(nil, ":message-type", "event")
	headers = appendEventStreamStringHeader(headers, ":event-type", eventType)
	totalLength := awsEventStreamPreludeSize + len(headers) + len(payload) + 4
	frame := make([]byte, totalLength)
	binary.BigEndian.PutUint32(frame[0:4], uint32(totalLength))
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(headers)))
	binary.BigEndian.PutUint32(frame[8:12], crc32.ChecksumIEEE(frame[:8]))
	copy(frame[awsEventStreamPreludeSize:], headers)
	copy(frame[awsEventStreamPreludeSize+len(headers):], payload)
	binary.BigEndian.PutUint32(frame[totalLength-4:], crc32.ChecksumIEEE(frame[:totalLength-4]))
	return frame
}

func appendEventStreamStringHeader(dst []byte, name, value string) []byte {
	dst = append(dst, byte(len(name)))
	dst = append(dst, name...)
	dst = append(dst, 7)
	length := make([]byte, 2)
	binary.BigEndian.PutUint16(length, uint16(len(value)))
	dst = append(dst, length...)
	dst = append(dst, value...)
	return dst
}
