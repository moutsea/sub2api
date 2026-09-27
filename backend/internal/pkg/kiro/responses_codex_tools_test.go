package kiro

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// Codex ships tool declarations inside input rather than the top-level array.
func TestResponsesPromotesAdditionalTools(t *testing.T) {
	req, tools, err := ConvertResponsesToClaudeWithTools([]byte(`{"model":"gpt-5.6-sol","input":[
		{"type":"additional_tools","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]},
		{"role":"user","content":"hi"}
	]}`))
	require.NoError(t, err)
	require.False(t, tools.Restores())
	require.Len(t, req.Tools, 1)
	require.Equal(t, "lookup", req.Tools[0].Name)
	require.Len(t, req.Messages, 1)
}

// A promoted tool must not duplicate an identical top-level declaration.
func TestResponsesAdditionalToolsDedup(t *testing.T) {
	req, _, err := ConvertResponsesToClaudeWithTools([]byte(`{"model":"auto","input":[
		{"type":"additional_tools","tools":[{"type":"function","name":"read"},{"type":"function","name":"write"}]},
		{"role":"user","content":"hi"}
	],"tools":[{"type":"function","name":"read"}]}`))
	require.NoError(t, err)
	require.Len(t, req.Tools, 2)
	require.Equal(t, "read", req.Tools[0].Name)
	require.Equal(t, "write", req.Tools[1].Name)
}

func TestResponsesLowersLocalShellAndCustomTools(t *testing.T) {
	req, tools, err := ConvertResponsesToClaudeWithTools([]byte(`{"model":"gpt-5.6-sol","input":[{"role":"user","content":"ls"}],
		"tools":[{"type":"local_shell"},{"type":"custom","name":"apply_patch","description":"Edit files"}]}`))
	require.NoError(t, err)
	require.True(t, tools.Restores())
	require.True(t, tools.LocalShell)
	require.True(t, tools.Custom["apply_patch"])
	require.Equal(t, "local_shell", tools.Kind("local_shell"))
	require.Equal(t, "custom", tools.Kind("apply_patch"))
	require.Equal(t, "", tools.Kind("read"))

	require.Len(t, req.Tools, 2)
	require.Equal(t, "local_shell", req.Tools[0].Name)
	require.Contains(t, req.Tools[0].InputSchema["properties"], "command")
	require.Equal(t, "apply_patch", req.Tools[1].Name)
	// A freeform tool becomes a single string field, the only Claude-expressible shape.
	require.Contains(t, req.Tools[1].InputSchema["properties"], "input")
}

func TestResponsesRejectsClientToolNameConflicts(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		tools []any
	}{
		{"patch", []any{map[string]any{"type": "function", "name": "patch"}, map[string]any{"type": "custom", "name": "patch"}}},
		{"local_shell_function", []any{map[string]any{"type": "function", "name": "local_shell"}, map[string]any{"type": "local_shell"}}},
		{"local_shell_custom", []any{map[string]any{"type": "custom", "name": "local_shell"}, map[string]any{"type": "local_shell"}}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			for _, placement := range []string{"top_level", "promoted", "split"} {
				t.Run(placement, func(t *testing.T) {
					for _, tools := range [][]any{testCase.tools, {testCase.tools[1], testCase.tools[0]}} {
						input := []any{map[string]any{"role": "user", "content": "x"}}
						body := map[string]any{"model": "auto"}
						switch placement {
						case "top_level":
							body["tools"] = tools
						case "promoted":
							input = append(input, map[string]any{"type": "additional_tools", "tools": tools})
						case "split":
							body["tools"] = tools[:1]
							input = append(input, map[string]any{"type": "additional_tools", "tools": tools[1:]})
						}
						body["input"] = input
						encoded, err := json.Marshal(body)
						require.NoError(t, err)
						request, mapping, err := ConvertResponsesToClaudeWithTools(encoded)
						require.ErrorContains(t, err, "conflicts")
						require.Nil(t, request)
						require.False(t, mapping.Restores())
					}
				})
			}
		})
	}
}

func TestResponsesAllowsCustomNameInvalidConflict(t *testing.T) {
	request, mapping, err := ConvertResponsesToClaudeWithTools([]byte(`{"model":"auto","input":[{"role":"user","content":"x"}],"tools":[{"type":"custom","name":"__invalid_conflict__"}]}`))
	require.NoError(t, err)
	require.Len(t, request.Tools, 1)
	require.Equal(t, "__invalid_conflict__", request.Tools[0].Name)
	require.Equal(t, "custom", mapping.Kind("__invalid_conflict__"))
}

// Codex selects its built-in shell by bare type, carrying no tool name.
func TestResponsesToolChoiceAcceptsClientToolTypes(t *testing.T) {
	for _, body := range []string{
		`{"model":"auto","input":[{"role":"user","content":"x"}],"tools":[{"type":"local_shell"}],"tool_choice":{"type":"local_shell"}}`,
		`{"model":"auto","input":[{"role":"user","content":"x"}],"tools":[{"type":"custom","name":"patch"}],"tool_choice":{"type":"custom","name":"patch"}}`,
	} {
		_, _, err := ConvertResponsesToClaudeWithTools([]byte(body))
		require.NoError(t, err, body)
	}
	// An undeclared client tool type is still rejected.
	_, _, err := ConvertResponsesToClaudeWithTools([]byte(`{"model":"auto","input":[{"role":"user","content":"x"}],"tool_choice":{"type":"local_shell"}}`))
	require.Error(t, err)
}

func TestResponsesNormalizesCodexCallAliases(t *testing.T) {
	req, _, err := ConvertResponsesToClaudeWithTools([]byte(`{"model":"gpt-5.6-sol","input":[
		{"role":"user","content":"run it"},
		{"type":"local_shell_call","call_id":"call_1","action":{"type":"exec","command":["ls","-la"]}},
		{"type":"local_shell_call_output","call_id":"call_1","output":"a.go"},
		{"type":"custom_tool_call","call_id":"call_2","name":"apply_patch","input":"*** Begin Patch"},
		{"type":"custom_tool_call_output","call_id":"call_2","output":"done"}
	],"tools":[{"type":"local_shell"},{"type":"custom","name":"apply_patch"}]}`))
	require.NoError(t, err)
	encoded, err := json.Marshal(req)
	require.NoError(t, err)
	wire := string(encoded)
	for _, want := range []string{`"type":"tool_use"`, `"type":"tool_result"`, `"tool_use_id":"call_1"`, `"tool_use_id":"call_2"`, `"a.go"`, `Begin Patch`} {
		require.Contains(t, wire, want)
	}
}

// Aliases must survive without a matching tool declaration, since Codex may
// replay history from a turn whose tools were trimmed.
func TestResponsesCodexAliasesWithoutToolDeclarations(t *testing.T) {
	req, tools, err := ConvertResponsesToClaudeWithTools([]byte(`{"model":"auto","input":[
		{"role":"user","content":"x"},
		{"type":"shell_call","call_id":"c1","arguments":"{\"cmd\":\"ls\"}"},
		{"type":"shell_call_output","call_id":"c1","output":{"output":"ok"}}
	]}`))
	require.NoError(t, err)
	require.False(t, tools.Restores())
	require.Len(t, req.Messages, 3)
}

func TestResponsesFoldsCompaction(t *testing.T) {
	req, _, err := ConvertResponsesToClaudeWithTools([]byte(`{"model":"auto","input":[
		{"type":"compaction","encrypted_content":"blob","summary":[{"type":"summary_text","text":"earlier work"}]},
		{"role":"user","content":"continue"}
	]}`))
	require.NoError(t, err)
	require.Len(t, req.Messages, 2)
	parts, ok := req.Messages[0].Content.([]any)
	require.True(t, ok)
	text := parts[0].(map[string]any)["text"].(string)
	require.Contains(t, text, "<conversation_summary>")
	require.Contains(t, text, "earlier work")
}

// A compaction carrying only an encrypted blob yields no message, so the
// request must still be rejected rather than sent empty.
func TestResponsesCompactionOnlyEncryptedIsRejected(t *testing.T) {
	_, _, err := ConvertResponsesToClaudeWithTools([]byte(`{"model":"auto","input":[{"type":"compaction","encrypted_content":"blob"}]}`))
	require.Error(t, err)
}

// Kiro emits function_call for every tool; Codex only accepts the item type it
// declared, so the converter must rewrite it back on the way out.
func TestResponsesConverterRestoresClientToolItems(t *testing.T) {
	c := NewKiroResponsesConverter("resp_test", "gpt-5.6-sol", 10)
	c.SetClientTools(ResponsesClientTools{LocalShell: true, Custom: map[string]bool{"apply_patch": true}})
	wire := c.BuildInitialEvent()
	for _, event := range []StreamEvent{
		{Type: EventContentBlockStart, Index: 0, BlockType: ContentBlockType{Kind: BlockToolUse, ToolID: "call_1", ToolName: "local_shell"}},
		{Type: EventToolUseInputDelta, ToolID: "call_1", PartialJSON: `{"command":["ls","-la"]}`},
		{Type: EventContentBlockStop, Index: 0},
		{Type: EventContentBlockStart, Index: 1, BlockType: ContentBlockType{Kind: BlockToolUse, ToolID: "call_2", ToolName: "apply_patch"}},
		{Type: EventToolUseInputDelta, ToolID: "call_2", PartialJSON: `{"input":"*** Begin Patch"}`},
		{Type: EventContentBlockStop, Index: 1},
		{Type: EventContentBlockStart, Index: 2, BlockType: ContentBlockType{Kind: BlockToolUse, ToolID: "call_3", ToolName: "read"}},
		{Type: EventToolUseInputDelta, ToolID: "call_3", PartialJSON: `{"path":"a.go"}`},
		{Type: EventContentBlockStop, Index: 2},
	} {
		wire += c.ConvertEvent(event)
	}
	wire += c.BuildFinalEvent()

	decoded := decodeResponsesEvents(t, wire)
	for i, event := range decoded {
		require.Equal(t, float64(i), event["sequence_number"])
	}
	require.Contains(t, wire, "response.custom_tool_call_input.delta")
	require.Contains(t, wire, "response.custom_tool_call_input.done")

	output := decoded[len(decoded)-1]["response"].(map[string]any)["output"].([]any)
	require.Len(t, output, 3)

	shell := output[0].(map[string]any)
	require.Equal(t, "local_shell_call", shell["type"])
	require.Equal(t, "call_1", shell["call_id"])
	require.Contains(t, shell["id"], "lsh_")
	require.Equal(t, []any{"ls", "-la"}, shell["action"].(map[string]any)["command"])

	custom := output[1].(map[string]any)
	require.Equal(t, "custom_tool_call", custom["type"])
	require.Equal(t, "apply_patch", custom["name"])
	// The {"input": ...} envelope is unwrapped back to the freeform body.
	require.Equal(t, "*** Begin Patch", custom["input"])
	require.Contains(t, custom["id"], "ctc_")

	plain := output[2].(map[string]any)
	require.Equal(t, "function_call", plain["type"])
	require.Equal(t, `{"path":"a.go"}`, plain["arguments"])
}

// Without a mapping every tool stays a function_call, preserving the shape
// non-Codex clients already rely on.
func TestResponsesConverterKeepsFunctionCallWithoutMapping(t *testing.T) {
	c := NewKiroResponsesConverter("resp_test", "auto", 1)
	wire := c.BuildInitialEvent()
	for _, event := range []StreamEvent{
		{Type: EventContentBlockStart, Index: 0, BlockType: ContentBlockType{Kind: BlockToolUse, ToolID: "call_1", ToolName: "local_shell"}},
		{Type: EventToolUseInputDelta, ToolID: "call_1", PartialJSON: `{"command":["ls"]}`},
		{Type: EventContentBlockStop, Index: 0},
	} {
		wire += c.ConvertEvent(event)
	}
	wire += c.BuildFinalEvent()
	decoded := decodeResponsesEvents(t, wire)
	output := decoded[len(decoded)-1]["response"].(map[string]any)["output"].([]any)
	require.Equal(t, "function_call", output[0].(map[string]any)["type"])
	require.NotContains(t, wire, "response.custom_tool_call_input")
}

// A stream that fails mid-call must still leave the restored item well-formed.
func TestResponsesConverterRestoresOnFailure(t *testing.T) {
	c := NewKiroResponsesConverter("resp_test", "gpt-5.6-sol", 1)
	c.SetClientTools(ResponsesClientTools{LocalShell: true})
	wire := c.BuildInitialEvent()
	wire += c.ConvertEvent(StreamEvent{Type: EventContentBlockStart, Index: 0, BlockType: ContentBlockType{Kind: BlockToolUse, ToolID: "call_1", ToolName: "local_shell"}})
	wire += c.ConvertEvent(StreamEvent{Type: EventToolUseInputDelta, ToolID: "call_1", PartialJSON: `{"command":["ls"`})
	wire += c.BuildErrorEvent("throttled", "retry")

	decoded := decodeResponsesEvents(t, wire)
	response := decoded[len(decoded)-1]["response"].(map[string]any)
	require.Equal(t, "failed", response["status"])
	item := response["output"].([]any)[0].(map[string]any)
	require.Equal(t, "local_shell_call", item["type"])
	// Truncated JSON cannot be parsed, so action falls back to an empty argv.
	require.Equal(t, []any{}, item["action"].(map[string]any)["command"])
}

func TestResponsesConverterFinalEventDoesNotDuplicateActiveItems(t *testing.T) {
	c := NewKiroResponsesConverter("resp_test", "auto", 1)
	c.SetClientTools(ResponsesClientTools{LocalShell: true})
	c.BuildInitialEvent()
	c.ConvertEvent(StreamEvent{Type: EventContentBlockStart, Index: 0, BlockType: ContentBlockType{Kind: BlockToolUse, ToolID: "call_1", ToolName: "local_shell"}})
	c.ConvertEvent(StreamEvent{Type: EventContentBlockStart, Index: 1, BlockType: ContentBlockType{Kind: BlockToolUse, ToolID: "call_2", ToolName: "local_shell"}})
	c.ConvertEvent(StreamEvent{Type: EventToolUseInputDelta, ToolID: "call_1", PartialJSON: `{"command":["one"]}`})
	c.ConvertEvent(StreamEvent{Type: EventToolUseInputDelta, ToolID: "call_2", PartialJSON: `{"command":["two"]}`})

	wire := c.BuildFinalEvent()
	decoded := decodeResponsesEvents(t, wire)
	var doneCount int
	for _, event := range decoded {
		if event["type"] == "response.output_item.done" {
			doneCount++
		}
	}
	require.Equal(t, 2, doneCount)
	require.Empty(t, c.active)
}

// Hosted tools stay unsupported; lowering must not silently accept them.
func TestResponsesStillRejectsHostedTools(t *testing.T) {
	for _, body := range []string{
		`{"model":"auto","input":"x","tools":[{"type":"web_search"}]}`,
		`{"model":"auto","input":"x","tools":[{"type":"file_search"}]}`,
	} {
		_, _, err := ConvertResponsesToClaudeWithTools([]byte(body))
		require.Error(t, err, body)
	}
}
