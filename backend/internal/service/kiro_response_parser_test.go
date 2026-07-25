package service

import (
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"strings"
	"testing"
)

// mustContentPayload builds a CodeWhisperer-shaped content payload that the
// upstream event-stream parser can consume. It mirrors the helper in the
// kiro package's internal tests — duplicated here so we don't cross package
// boundaries for a trivial fixture.
func mustContentPayload(t *testing.T, content string) []byte {
	t.Helper()
	return mustKiroEventFrame(t, "assistantResponseEvent", map[string]string{"content": content})
}

func mustKiroEventFrame(t *testing.T, eventType string, body any) []byte {
	t.Helper()
	return mustKiroMessageFrame(t, "event", ":event-type", eventType, body)
}

func mustKiroExceptionFrame(t *testing.T, exceptionType string, body any) []byte {
	t.Helper()
	return mustKiroMessageFrame(t, "exception", ":exception-type", exceptionType, body)
}

func mustKiroMessageFrame(t *testing.T, messageType, typeHeader, typeValue string, body any) []byte {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal content payload: %v", err)
	}

	headers := appendKiroEventStreamStringHeader(nil, ":message-type", messageType)
	headers = appendKiroEventStreamStringHeader(headers, typeHeader, typeValue)
	totalLength := 12 + len(headers) + len(payload) + 4
	frame := make([]byte, totalLength)
	binary.BigEndian.PutUint32(frame[0:4], uint32(totalLength))
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(headers)))
	binary.BigEndian.PutUint32(frame[8:12], crc32.ChecksumIEEE(frame[:8]))
	copy(frame[12:], headers)
	copy(frame[12+len(headers):], payload)
	binary.BigEndian.PutUint32(frame[totalLength-4:], crc32.ChecksumIEEE(frame[:totalLength-4]))
	return frame
}

func appendKiroEventStreamStringHeader(dst []byte, name, value string) []byte {
	dst = append(dst, byte(len(name)))
	dst = append(dst, name...)
	dst = append(dst, 7)
	length := make([]byte, 2)
	binary.BigEndian.PutUint16(length, uint16(len(value)))
	dst = append(dst, length...)
	dst = append(dst, value...)
	return dst
}

// TestKiroResponseParser_ParseComplete_ThinkingEnabledSeparatesBlocks verifies
// that when thinking mode is enabled, the parser emits thinking content into
// ParseResult.Thinking rather than TextContent. This is the core invariant the
// WebSearch agentic loop relies on to avoid leaking private reasoning into
// follow-up assistant context.
func TestKiroResponseParser_ParseComplete_ThinkingEnabledSeparatesBlocks(t *testing.T) {
	payload := mustContentPayload(t, "<thinking>\nprivate reasoning</thinking>\n\nI need to search the web.")

	parser := &KiroResponseParser{}
	result, err := parser.ParseComplete(payload, true)
	if err != nil {
		t.Fatalf("ParseComplete: %v", err)
	}

	if !strings.Contains(result.Thinking, "private reasoning") {
		t.Fatalf("Thinking = %q, want to contain 'private reasoning'", result.Thinking)
	}
	if strings.Contains(result.TextContent, "private reasoning") {
		t.Fatalf("TextContent leaked thinking: %q", result.TextContent)
	}
	if strings.Contains(result.TextContent, "<thinking>") || strings.Contains(result.TextContent, "</thinking>") {
		t.Fatalf("TextContent retained thinking tags: %q", result.TextContent)
	}
	if !strings.Contains(result.TextContent, "I need to search the web.") {
		t.Fatalf("TextContent = %q, want visible answer retained", result.TextContent)
	}
}

// TestKiroResponseParser_ParseComplete_ThinkingDisabledTreatsTagsAsText verifies
// that when thinking mode is disabled, <thinking> tags are preserved in
// TextContent as plain text (preserving existing non-thinking behavior and
// avoiding accidental stripping of literal content).
func TestKiroResponseParser_ParseComplete_ThinkingDisabledTreatsTagsAsText(t *testing.T) {
	payload := mustContentPayload(t, "<thinking>\nnot really thinking</thinking>\n\nhello")

	parser := &KiroResponseParser{}
	result, err := parser.ParseComplete(payload, false)
	if err != nil {
		t.Fatalf("ParseComplete: %v", err)
	}

	if result.Thinking != "" {
		t.Fatalf("Thinking = %q, want empty when thinking disabled", result.Thinking)
	}
	if !strings.Contains(result.TextContent, "not really thinking") {
		t.Fatalf("TextContent = %q, want raw content preserved when thinking disabled", result.TextContent)
	}
}

// TestBuildWebSearchAssistantContent_ThinkingOmittedFromContext verifies that
// private reasoning parsed from the upstream response is intentionally dropped
// when constructing the follow-up assistant content: it is neither leaked as
// plain text (the original bug) nor preserved as an unsigned thinking block
// (which would not be a valid Claude Extended Thinking history entry).
func TestBuildWebSearchAssistantContent_ThinkingOmittedFromContext(t *testing.T) {
	parseResult := &ParseResult{
		Thinking:    "private reasoning",
		TextContent: "searching now",
		ToolCalls: []ToolCall{
			{ID: "toolu_1", Name: "web_search", Arguments: map[string]any{"query": "go 1.23"}},
		},
	}

	content := buildWebSearchAssistantContent(parseResult)
	if len(content) != 2 {
		t.Fatalf("content len=%d, want 2 (text + tool_use, thinking dropped)", len(content))
	}

	// No thinking block must appear in the produced assistant content.
	for i, block := range content {
		if block["type"] == "thinking" {
			t.Fatalf("content[%d] = %#v, thinking block must be omitted", i, block)
		}
	}

	// Private reasoning must not leak into any text block either.
	for i, block := range content {
		if block["type"] != "text" {
			continue
		}
		text, _ := block["text"].(string)
		if strings.Contains(text, "private reasoning") {
			t.Fatalf("content[%d] text leaked thinking: %q", i, text)
		}
	}

	if content[0]["type"] != "text" || content[0]["text"] != "searching now" {
		t.Fatalf("first block = %#v, want text block with visible answer", content[0])
	}
	if content[1]["type"] != "tool_use" || content[1]["id"] != "toolu_1" {
		t.Fatalf("second block = %#v, want tool_use block", content[1])
	}
}

// TestBuildWebSearchAssistantContent_ThinkingOmittedWhenEmpty ensures no empty
// thinking block is emitted when the upstream response has no thinking content.
func TestBuildWebSearchAssistantContent_ThinkingOmittedWhenEmpty(t *testing.T) {
	parseResult := &ParseResult{
		TextContent: "direct answer",
		ToolCalls: []ToolCall{
			{ID: "toolu_1", Name: "web_search", Arguments: map[string]any{"query": "x"}},
		},
	}

	content := buildWebSearchAssistantContent(parseResult)
	if len(content) != 2 {
		t.Fatalf("content len=%d, want 2 (text + tool_use, no thinking)", len(content))
	}
	if content[0]["type"] != "text" {
		t.Fatalf("first block = %#v, want text block", content[0])
	}
}
