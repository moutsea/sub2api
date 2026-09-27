package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateOpenAIStatelessInputReferences_RejectsReasoningIDWithoutEncryptedContent(t *testing.T) {
	reqBody := map[string]any{
		"store": false,
		"input": []any{
			map[string]any{"type": "reasoning", "id": "rs_02b340e9f9ee714e016a04830017c08193a6ba36add422c726"},
		},
	}

	err := validateOpenAIStatelessInputReferences(reqBody)

	require.Error(t, err)
	require.Contains(t, err.ClientMessage(), "rs_02b340e9f9ee714e016a04830017c08193a6ba36add422c726")
}

func TestValidateOpenAIStatelessInputReferences_AllowsEncryptedReasoningContent(t *testing.T) {
	reqBody := map[string]any{
		"store": false,
		"input": []any{
			map[string]any{
				"type":              "reasoning",
				"id":                "rs_02b340e9f9ee714e016a04830017c08193a6ba36add422c726",
				"encrypted_content": "enc",
			},
		},
	}

	require.Nil(t, validateOpenAIStatelessInputReferences(reqBody))
}

func TestValidateOpenAIStatelessInputReferences_RejectsReasoningWithoutIDOrEncryptedContent(t *testing.T) {
	reqBody := map[string]any{
		"store": false,
		"input": []any{
			map[string]any{"type": "reasoning"},
		},
	}

	err := validateOpenAIStatelessInputReferences(reqBody)

	require.Error(t, err)
	require.Contains(t, err.ClientMessage(), "reasoning item without encrypted_content")
}

func TestValidateOpenAIStatelessInputReferences_RejectsReasoningItemReference(t *testing.T) {
	reqBody := map[string]any{
		"store": false,
		"input": []any{
			map[string]any{"type": "item_reference", "id": "rs_02b340e9f9ee714e016a04830017c08193a6ba36add422c726"},
		},
	}

	require.Error(t, validateOpenAIStatelessInputReferences(reqBody))
}

func TestValidateOpenAIStatelessInputReferences_StoreTrueNotStateless(t *testing.T) {
	reqBody := map[string]any{
		"store": true,
		"input": []any{
			map[string]any{"type": "reasoning", "id": "rs_02b340e9f9ee714e016a04830017c08193a6ba36add422c726"},
		},
	}

	require.Nil(t, validateOpenAIStatelessInputReferences(reqBody))
}

func TestNeedsToolContinuationSignals(t *testing.T) {
	// 覆盖所有触发续链的信号来源，确保判定逻辑完整。
	cases := []struct {
		name string
		body map[string]any
		want bool
	}{
		{name: "nil", body: nil, want: false},
		{name: "previous_response_id", body: map[string]any{"previous_response_id": "resp_1"}, want: true},
		{name: "previous_response_id_blank", body: map[string]any{"previous_response_id": "  "}, want: false},
		{name: "function_call_output", body: map[string]any{"input": []any{map[string]any{"type": "function_call_output"}}}, want: true},
		{name: "item_reference", body: map[string]any{"input": []any{map[string]any{"type": "item_reference"}}}, want: true},
		{name: "tools", body: map[string]any{"tools": []any{map[string]any{"type": "function"}}}, want: true},
		{name: "tools_empty", body: map[string]any{"tools": []any{}}, want: false},
		{name: "tools_invalid", body: map[string]any{"tools": "bad"}, want: false},
		{name: "tool_choice", body: map[string]any{"tool_choice": "auto"}, want: true},
		{name: "tool_choice_object", body: map[string]any{"tool_choice": map[string]any{"type": "function"}}, want: true},
		{name: "tool_choice_empty_object", body: map[string]any{"tool_choice": map[string]any{}}, want: false},
		{name: "none", body: map[string]any{"input": []any{map[string]any{"type": "text", "text": "hi"}}}, want: false},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, NeedsToolContinuation(tt.body))
		})
	}
}

func TestHasFunctionCallOutput(t *testing.T) {
	// 仅当 input 中存在 function_call_output 才视为续链输出。
	require.False(t, HasFunctionCallOutput(nil))
	require.True(t, HasFunctionCallOutput(map[string]any{
		"input": []any{map[string]any{"type": "function_call_output"}},
	}))
	require.False(t, HasFunctionCallOutput(map[string]any{
		"input": "text",
	}))
}

func TestHasToolCallContext(t *testing.T) {
	// tool_call/function_call 必须包含 call_id，才能作为可关联上下文。
	require.False(t, HasToolCallContext(nil))
	require.True(t, HasToolCallContext(map[string]any{
		"input": []any{map[string]any{"type": "tool_call", "call_id": "call_1"}},
	}))
	require.True(t, HasToolCallContext(map[string]any{
		"input": []any{map[string]any{"type": "function_call", "call_id": "call_2"}},
	}))
	require.False(t, HasToolCallContext(map[string]any{
		"input": []any{map[string]any{"type": "tool_call"}},
	}))
}

func TestHasToolCallContextCodexToolAliases(t *testing.T) {
	for _, itemType := range []string{"tool_call", "function_call", "local_shell_call", "shell_call", "custom_tool_call", "apply_patch_call", "computer_call", "tool_search_call"} {
		t.Run(itemType, func(t *testing.T) {
			for _, callID := range []string{"call_1", "", "  "} {
				require.Equal(t, callID == "call_1", HasToolCallContext(map[string]any{"input": []any{
					map[string]any{"type": itemType, "call_id": callID},
					map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "done"},
				}}))
			}
		})
	}
}

func TestHasToolCallContextRejectsOutputItems(t *testing.T) {
	for _, itemType := range []string{"function_call_output", "local_shell_call_output", "shell_call_output", "custom_tool_call_output", "apply_patch_call_output", "computer_call_output", "tool_search_output", "program_output", "message", "unknown_call"} {
		t.Run(itemType, func(t *testing.T) {
			require.False(t, HasToolCallContext(map[string]any{"input": []any{
				map[string]any{"type": itemType, "call_id": "call_1"},
				map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "done"},
			}}))
		})
	}
}

func TestFunctionCallOutputCallIDs(t *testing.T) {
	// 仅提取非空 call_id，去重后返回。
	require.Empty(t, FunctionCallOutputCallIDs(nil))
	callIDs := FunctionCallOutputCallIDs(map[string]any{
		"input": []any{
			map[string]any{"type": "function_call_output", "call_id": "call_1"},
			map[string]any{"type": "function_call_output", "call_id": ""},
			map[string]any{"type": "function_call_output", "call_id": "call_1"},
		},
	})
	require.ElementsMatch(t, []string{"call_1"}, callIDs)
}

func TestHasFunctionCallOutputMissingCallID(t *testing.T) {
	require.False(t, HasFunctionCallOutputMissingCallID(nil))
	require.True(t, HasFunctionCallOutputMissingCallID(map[string]any{
		"input": []any{map[string]any{"type": "function_call_output"}},
	}))
	require.False(t, HasFunctionCallOutputMissingCallID(map[string]any{
		"input": []any{map[string]any{"type": "function_call_output", "call_id": "call_1"}},
	}))
}

func TestHasItemReferenceForCallIDs(t *testing.T) {
	// item_reference 需要覆盖所有 call_id 才视为可关联上下文。
	require.False(t, HasItemReferenceForCallIDs(nil, []string{"call_1"}))
	require.False(t, HasItemReferenceForCallIDs(map[string]any{}, []string{"call_1"}))
	req := map[string]any{
		"input": []any{
			map[string]any{"type": "item_reference", "id": "call_1"},
			map[string]any{"type": "item_reference", "id": "call_2"},
		},
	}
	require.True(t, HasItemReferenceForCallIDs(req, []string{"call_1"}))
	require.True(t, HasItemReferenceForCallIDs(req, []string{"call_1", "call_2"}))
	require.False(t, HasItemReferenceForCallIDs(req, []string{"call_1", "call_3"}))
}
