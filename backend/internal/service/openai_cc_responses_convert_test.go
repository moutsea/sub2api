package service

import (
	"strings"
	"testing"
)

func TestConvertCCRequestToResponses_PreservesServiceTier(t *testing.T) {
	body := map[string]any{
		"model":        "gpt-5.5",
		"service_tier": "priority",
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": "hello",
			},
		},
	}

	converted := convertCCRequestToResponses(body)
	serviceTier, _ := converted["service_tier"].(string)
	if serviceTier != "priority" {
		t.Fatalf("service_tier = %q, want %q", serviceTier, "priority")
	}
}

func TestConvertCCRequestToResponses_PreservesOutputTokenLimit(t *testing.T) {
	body := map[string]any{
		"model":                 "grok-4.3",
		"max_tokens":            float64(1024),
		"max_completion_tokens": float64(2048),
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
	}

	converted := convertCCRequestToResponses(body)
	if got := converted["max_output_tokens"]; got != 2048 {
		t.Fatalf("max_output_tokens = %v, want 2048", got)
	}
}

func TestConvertCCRequestToResponsesConvertsToolsAndToolChoice(t *testing.T) {
	body := map[string]any{
		"model": "grok-4.3",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "lookup",
					"description": "Look something up",
					"parameters":  map[string]any{"type": "object"},
				},
			},
		},
		"tool_choice": map[string]any{
			"type":     "function",
			"function": map[string]any{"name": "lookup"},
		},
	}

	converted := convertCCRequestToResponses(body)
	tools, ok := converted["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v, want one Responses function tool", converted["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if tool["name"] != "lookup" || tool["function"] != nil {
		t.Fatalf("tool = %#v, want flat Responses function tool", tool)
	}
	choice, _ := converted["tool_choice"].(map[string]any)
	if choice["name"] != "lookup" || choice["function"] != nil {
		t.Fatalf("tool_choice = %#v, want flat Responses function choice", converted["tool_choice"])
	}
}

func TestResponsesToCCStreamConverterInterleavesToolArguments(t *testing.T) {
	converter := newResponsesToCCStreamConverter("grok-4.3")
	converter.convertEvent(`{"type":"response.created","response":{"id":"resp_1"}}`)
	converter.convertEvent(`{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"first"}}`)
	converter.convertEvent(`{"type":"response.output_item.added","item":{"type":"function_call","id":"fc_2","call_id":"call_2","name":"second"}}`)

	first := converter.convertEvent(`{"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"a"}`)
	second := converter.convertEvent(`{"type":"response.function_call_arguments.delta","item_id":"fc_2","delta":"b"}`)
	if len(first) != 1 || !strings.Contains(first[0], `"index":0`) {
		t.Fatalf("first tool delta = %#v, want index 0", first)
	}
	if len(second) != 1 || !strings.Contains(second[0], `"index":1`) {
		t.Fatalf("second tool delta = %#v, want index 1", second)
	}
}

func TestResponsesToCCStreamConverterAcceptsResponseDone(t *testing.T) {
	converter := newResponsesToCCStreamConverter("grok-4.3")
	created := converter.convertEvent(`{"type":"response.created","response":{"id":"resp_1"}}`)
	if len(created) != 1 {
		t.Fatalf("response.created lines = %d, want 1", len(created))
	}

	lines := converter.convertEvent(`{"type":"response.done","response":{"usage":{"input_tokens":3,"output_tokens":2}}}`)
	if len(lines) != 2 {
		t.Fatalf("response.done lines = %d, want final chunk and [DONE]", len(lines))
	}
	if !strings.Contains(lines[0], `"prompt_tokens":3`) || !strings.Contains(lines[0], `"completion_tokens":2`) {
		t.Fatalf("response.done usage missing: %s", lines[0])
	}
	if lines[1] != "data: [DONE]" {
		t.Fatalf("terminal line = %q, want data: [DONE]", lines[1])
	}
}
