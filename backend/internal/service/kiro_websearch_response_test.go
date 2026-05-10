package service

import "testing"

func TestBuildWebSearchSSEEventsUsesOfficialServerToolShape(t *testing.T) {
	events := buildWebSearchSSEEvents(WebSearchEvent{
		ID:    "srvtoolu_test",
		Query: "latest golang release",
		Results: []WebSearchResultItem{
			{
				URL:              "https://go.dev",
				Title:            "Go",
				EncryptedContent: "snippet",
			},
		},
	}, 1)

	if len(events) != 5 {
		t.Fatalf("events len=%d, want 5", len(events))
	}

	serverStart := events[0].Data["content_block"].(map[string]any)
	if serverStart["type"] != "server_tool_use" {
		t.Fatalf("server block type=%v, want server_tool_use", serverStart["type"])
	}
	input := serverStart["input"].(map[string]any)
	if len(input) != 0 {
		t.Fatalf("server start input=%v, want empty object before input_json_delta", input)
	}

	inputDelta := events[1].Data["delta"].(map[string]any)
	if inputDelta["type"] != "input_json_delta" {
		t.Fatalf("delta type=%v, want input_json_delta", inputDelta["type"])
	}
	if inputDelta["partial_json"] != `{"query":"latest golang release"}` {
		t.Fatalf("partial_json=%v, want query JSON", inputDelta["partial_json"])
	}

	resultStart := events[3].Data["content_block"].(map[string]any)
	if resultStart["type"] != "web_search_tool_result" {
		t.Fatalf("result block type=%v, want web_search_tool_result", resultStart["type"])
	}
	if resultStart["tool_use_id"] != "srvtoolu_test" {
		t.Fatalf("tool_use_id=%v, want srvtoolu_test", resultStart["tool_use_id"])
	}
}

func TestInjectWebSearchContentBlocksUsesOfficialServerToolShape(t *testing.T) {
	resp := map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": "done"},
		},
	}

	injectWebSearchContentBlocks(resp, []WebSearchEvent{
		{
			ID:    "srvtoolu_test",
			Query: "latest golang release",
			Results: []WebSearchResultItem{
				{URL: "https://go.dev", Title: "Go"},
			},
		},
	})

	content := resp["content"].([]map[string]any)
	if len(content) != 3 {
		t.Fatalf("content len=%d, want 3", len(content))
	}
	if content[0]["type"] != "server_tool_use" {
		t.Fatalf("first block type=%v, want server_tool_use", content[0]["type"])
	}
	if content[1]["type"] != "web_search_tool_result" {
		t.Fatalf("second block type=%v, want web_search_tool_result", content[1]["type"])
	}
	if content[1]["tool_use_id"] != "srvtoolu_test" {
		t.Fatalf("tool_use_id=%v, want srvtoolu_test", content[1]["tool_use_id"])
	}
}
