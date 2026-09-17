package kiro

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConvertResponsesToClaude(t *testing.T) {
	req, err := ConvertResponsesToClaude([]byte(`{"model":"claude-sonnet-4-6","instructions":"Be brief","stream":true,"store":false,"max_output_tokens":2000,"reasoning":{"effort":"medium"},"input":[{"role":"developer","content":[{"type":"input_text","text":"Use tools"}]},{"role":"user","content":[{"type":"input_text","text":"Look"},{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="}]},{"type":"function_call","call_id":"call_1","name":"read","arguments":"{\"path\":\"a.go\"}"},{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_text","text":"file contents"}]},{"role":"assistant","content":[{"type":"output_text","text":"Done"}]},{"role":"user","content":"Continue"}],"tools":[{"type":"function","name":"read","description":"Read a file","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}],"tool_choice":{"type":"function","name":"read"}}`))
	require.NoError(t, err)
	require.True(t, req.Stream)
	require.Equal(t, 2000, req.MaxTokens)
	require.Equal(t, "Be brief\n\nUse tools", req.System)
	require.Equal(t, float64(4096), req.Thinking["budget_tokens"])
	require.Len(t, req.Messages, 5)
	require.Len(t, req.Tools, 1)
	require.Equal(t, "read", req.Tools[0].Name)
	encoded, err := json.Marshal(req)
	require.NoError(t, err)
	for _, want := range []string{`"type":"image"`, `"type":"tool_use"`, `"type":"tool_result"`, `"tool_use_id":"call_1"`, `"file contents"`} {
		require.Contains(t, string(encoded), want)
	}
	require.Equal(t, map[string]any{"type": "tool", "name": "read"}, req.ToolChoice)
}
func TestConvertResponsesToClaudeStringAndInvalidRequests(t *testing.T) {
	req, err := ConvertResponsesToClaude([]byte(`{"model":"auto","input":"hello"}`))
	require.NoError(t, err)
	require.False(t, req.Stream)
	require.Equal(t, "hello", req.Messages[0].Content)
	for _, body := range []string{
		`{}`, `null`, `{`, `{"model":"auto","input":[]}`, `{"model":"auto","input":4}`,
		`{"model":"auto","input":[{"role":"unknown","content":"x"}]}`,
		`{"model":"auto","input":"hello","previous_response_id":"resp_1"}`,
		`{"model":"auto","input":"hello","conversation":{"id":"conv_1"}}`,
		`{"model":"auto","input":"hello","store":true}`, `{"model":"auto","input":"hello","background":true}`,
		`{"model":"auto","input":"hello","tools":[{"type":"web_search"}]}`,
		`{"model":"auto","input":[{"type":"item_reference","id":"msg_1"}]}`,
		`{"model":"auto","input":[{"type":"function_call","call_id":"1","name":"read","arguments":"broken"}]}`,
		`{"model":"auto","input":[{"role":"user","content":[{"type":"input_image","file_id":"file_1"}]}]}`,
		`{"model":"auto","input":"hello","stream":"true"}`,
		`{"model":"auto","input":"hello","text":{"format":{"type":"json_schema"}}}`,
	} {
		t.Run(body, func(t *testing.T) { _, err := ConvertResponsesToClaude([]byte(body)); require.Error(t, err) })
	}
}

func decodeResponsesEvents(t *testing.T, wire string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(wire, "\n") {
		if strings.HasPrefix(line, "data: ") {
			var event map[string]any
			require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event))
			events = append(events, event)
		}
	}
	return events
}
func TestKiroResponsesConverterInterleavedToolsAndReasoning(t *testing.T) {
	c := NewKiroResponsesConverter("resp_test", "auto", 100)
	c.SetCacheTokens(10, 20)
	wire := c.BuildInitialEvent()
	events := []StreamEvent{
		{Type: EventContentBlockStart, Index: 0, BlockType: ContentBlockType{Kind: BlockThinking}},
		{Type: EventThinkingDelta, Text: "think"}, {Type: EventContentBlockStop, Index: 0},
		{Type: EventContentBlockStart, Index: 1, BlockType: ContentBlockType{Kind: BlockText}},
		{Type: EventTextDelta, Text: "hello"}, {Type: EventContentBlockStop, Index: 1},
		{Type: EventContentBlockStart, Index: 2, BlockType: ContentBlockType{Kind: BlockToolUse, ToolID: "a", ToolName: "read"}},
		{Type: EventContentBlockStart, Index: 3, BlockType: ContentBlockType{Kind: BlockToolUse, ToolID: "b", ToolName: "write"}},
		{Type: EventToolUseInputDelta, ToolID: "b", PartialJSON: `{"b":`},
		{Type: EventToolUseInputDelta, ToolID: "a", PartialJSON: `{"a":1}`},
		{Type: EventToolUseInputDelta, ToolID: "b", PartialJSON: `2}`},
		{Type: EventContentBlockStop, Index: 2}, {Type: EventContentBlockStop, Index: 3},
	}
	for _, event := range events {
		wire += c.ConvertEvent(event)
	}
	wire += c.BuildFinalEvent()
	require.Empty(t, c.BuildFinalEvent())
	require.NotContains(t, wire, "[DONE]")
	decoded := decodeResponsesEvents(t, wire)
	for i, event := range decoded {
		require.Equal(t, float64(i), event["sequence_number"])
	}
	require.Equal(t, "response.created", decoded[0]["type"])
	require.Equal(t, "response.completed", decoded[len(decoded)-1]["type"])
	response := decoded[len(decoded)-1]["response"].(map[string]any)
	output := response["output"].([]any)
	require.Len(t, output, 4)
	require.Equal(t, `{"a":1}`, output[2].(map[string]any)["arguments"])
	require.Equal(t, `{"b":2}`, output[3].(map[string]any)["arguments"])
	usage := response["usage"].(map[string]any)
	require.Equal(t, float64(20), usage["input_tokens_details"].(map[string]any)["cached_tokens"])
	require.Greater(t, usage["output_tokens_details"].(map[string]any)["reasoning_tokens"].(float64), float64(0))
}
func TestKiroResponsesConverterFailureAndIncomplete(t *testing.T) {
	c := NewKiroResponsesConverter("resp_test", "auto", 1)
	wire := c.BuildInitialEvent() + c.BuildErrorEvent("throttled", "retry") + c.BuildFinalEvent()
	require.Contains(t, wire, "response.failed")
	require.NotContains(t, wire, "response.completed")
	c = NewKiroResponsesConverter("resp_test", "auto", 1)
	c.ConvertEvent(StreamEvent{Type: EventMessageStop, StopReason: StopReasonMaxTokens})
	require.Contains(t, c.BuildFinalEvent(), "response.incomplete")
	require.Equal(t, map[string]any{"reason": "max_output_tokens"}, c.Response()["incomplete_details"])
}
