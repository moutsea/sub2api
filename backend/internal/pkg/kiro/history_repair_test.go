package kiro

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHistoryRepairPairsDeduplicatedCallsByTurn(t *testing.T) {
	for _, tc := range []struct{ name, messages, want string }{
		{
			name: "empty fragments before and after valid results are removed",
			messages: `[
				{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"read","input":{}},{"type":"tool_use","id":"a","name":"read","input":{}},{"type":"tool_use","id":"b","name":"read","input":{}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"orphan","content":"old"}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"A"}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"duplicate A"}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"b","content":"B"}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"b","content":"duplicate B"}]},
				{"role":"user","content":[{"type":"text","text":"keep"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"YQ=="}}]}
			]`,
			want: `[
				{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"read","input":{}},{"type":"tool_use","id":"b","name":"read","input":{}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"A"}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"b","content":"B"}]},
				{"role":"user","content":[{"type":"text","text":"keep"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"YQ=="}}]}
			]`,
		},
		{
			name: "entirely empty user turn retains one boundary",
			messages: `[
				{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"read","input":{}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"A"}]},
				{"role":"assistant","content":[{"type":"text","text":"again"},{"type":"tool_use","id":"a","name":"read","input":{}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"duplicate"}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"duplicate"}]},
				{"role":"assistant","content":"done"}
			]`,
			want: `[
				{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"read","input":{}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"A"}]},
				{"role":"assistant","content":[{"type":"text","text":"again"}]},
				{"role":"user","content":[{"type":"text","text":"Continue"}]},
				{"role":"assistant","content":"done"}
			]`,
		},
		{
			name: "duplicate calls and results in one turn",
			messages: `[
				{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"read","input":{}},{"type":"tool_use","id":"a","name":"read","input":{}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"first"},{"type":"tool_result","tool_use_id":"a","content":"second"},{"type":"text","text":"keep"}]}
			]`,
			want: `[
				{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"read","input":{}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"first"},{"type":"text","text":"keep"}]}
			]`,
		},
		{
			name: "removed turn cannot answer an earlier interrupted call",
			messages: `[
				{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"read","input":{}}]},
				{"role":"user","content":"cancelled"},
				{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"read","input":{}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"later"},{"type":"text","text":"keep"}]}
			]`,
			want: `[
				{"role":"user","content":"cancelled"},
				{"role":"user","content":[{"type":"text","text":"keep"}]}
			]`,
		},
		{
			name: "consecutive messages form one logical turn",
			messages: `[
				{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"read","input":{}}]},
				{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"read","input":{}},{"type":"tool_use","id":"b","name":"read","input":{}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"A"}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"b","content":"B"}]}
			]`,
			want: `[
				{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"read","input":{}}]},
				{"role":"assistant","content":[{"type":"tool_use","id":"b","name":"read","input":{}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"A"}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"b","content":"B"}]}
			]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var messages []ClaudeMessage
			require.NoError(t, json.Unmarshal([]byte(tc.messages), &messages))
			repaired, changed := DeduplicateToolUseIDsInClaudeMessages(messages)
			require.True(t, changed)
			got, err := json.Marshal(repaired)
			require.NoError(t, err)
			require.JSONEq(t, tc.want, string(got))
			original, err := json.Marshal(messages)
			require.NoError(t, err)
			require.JSONEq(t, tc.messages, string(original), "repair must not mutate input")
			again, changed := DeduplicateToolUseIDsInClaudeMessages(repaired)
			require.False(t, changed)
			require.Equal(t, repaired, again)
		})
	}
}

func TestHistoryRepairForceTruncationDropsEmptyFragments(t *testing.T) {
	req, err := ParseClaudeRequestFromJSON([]byte(`{"model":"claude-sonnet-4-6","messages":[
		{"role":"user","content":"old"},
		{"role":"assistant","content":"old"},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"orphan","content":"discard"}]},
		{"role":"user","content":"keep"},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"orphan","content":"discard"}]},
		{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"YQ=="}}]}
	]}`))
	require.NoError(t, err)
	before, err := json.Marshal(req)
	require.NoError(t, err)
	truncated, changed := ForceTruncateMessages(req, 1)
	require.True(t, changed)
	require.Equal(t, []ClaudeMessage{req.Messages[3], req.Messages[5]}, truncated.Messages)
	after, err := json.Marshal(req)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after))
}

func TestHistoryRepairPreservesOriginalContent(t *testing.T) {
	for _, tc := range []struct {
		name      string
		repair    func([]ClaudeMessage) ([]ClaudeMessage, bool)
		removedID string
	}{
		{"orphan", CleanOrphanToolUsesInClaudeMessages, "orphan"},
		{"duplicate", DeduplicateToolUseIDsInClaudeMessages, "paired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := []ClaudeMessage{
				{Role: "user", Content: "read the file"},
				{Role: "assistant", Content: []any{map[string]any{"type": "tool_use", "id": "paired", "name": "read", "input": map[string]any{}}}},
				{Role: "user", Content: []any{map[string]any{"type": "tool_result", "tool_use_id": "paired", "content": "file content"}}},
				{Role: "assistant", Content: []any{map[string]any{"type": "tool_use", "id": tc.removedID, "name": "read", "input": map[string]any{}}}},
				{Role: "user", Content: []any{
					map[string]any{"type": "text", "text": "inspect image"},
					map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "aW1hZ2U="}},
				}},
				{Role: "assistant", Content: "."},
				{Role: "user", Content: "continue"},
				{Role: "assistant", Content: []any{
					map[string]any{"type": "text", "text": "."},
					map[string]any{"type": "tool_use", "id": tc.removedID, "name": "read", "input": map[string]any{}},
				}},
				{Role: "user", Content: "next"},
			}
			before, err := json.Marshal(messages)
			if err != nil {
				t.Fatal(err)
			}
			repaired, changed := tc.repair(messages)
			if !changed || len(repaired) != len(messages)-1 {
				t.Fatalf("expected only the empty assistant turn to be removed: %#v", repaired)
			}
			// Keep paired calls/results, images, and real punctuation-only replies.
			for i, originalIndex := range []int{0, 1, 2, 4, 5, 6} {
				if !reflect.DeepEqual(repaired[i], messages[originalIndex]) {
					t.Fatalf("original content changed at %d: %#v", i, repaired[i])
				}
			}
			blocks := repaired[6].Content.([]any)
			if len(blocks) != 1 || blocks[0].(map[string]any)["text"] != "." {
				t.Fatalf("real text was removed with the tool call: %#v", blocks)
			}
			after, err := json.Marshal(messages)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("repair mutated the original history")
			}
			again, changed := tc.repair(repaired)
			if changed || !reflect.DeepEqual(again, repaired) {
				t.Fatal("repeated repair should leave repaired history unchanged")
			}
		})
	}
}

func TestHistoryAlternationDoesNotInjectVisibleReplies(t *testing.T) {
	ctx := NewTransformContext("claude-sonnet-4-6", nil)
	history := fixHistoryAlternation(ctx, []HistoryEntry{
		{Type: "user", User: &HistoryUserMessage{Content: "first"}},
		{Type: "user", User: &HistoryUserMessage{Content: "second"}},
	})
	if len(history) != 4 {
		t.Fatalf("history = %#v", history)
	}
	for i, entry := range history {
		if i%2 == 0 {
			if entry.Type != "user" {
				t.Fatalf("expected user at %d", i)
			}
			continue
		}
		if entry.Type != "assistant" || entry.Assistant == nil || entry.Assistant.Content == "" || strings.TrimSpace(entry.Assistant.Content) != "" {
			t.Fatalf("expected nonempty blank protocol padding at %d: %#v", i, entry)
		}
	}
}

func TestHistoryRepairMergesUserTurnsWithoutLosingToolResultsOrImages(t *testing.T) {
	req, err := ParseClaudeRequestFromJSON([]byte(`{"model":"claude-sonnet-4-6","system":"Keep the original instructions","messages":[
		{"role":"user","content":"read the file"},
		{"role":"assistant","content":[{"type":"tool_use","id":"paired","name":"read","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"paired","content":"file content"}]},
		{"role":"assistant","content":[{"type":"tool_use","id":"orphan","name":"read","input":{}}]},
		{"role":"user","content":[{"type":"text","text":"inspect image"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}}]}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Messages, _ = CleanOrphanToolUsesInClaudeMessages(req.Messages)
	cw, err := TransformClaudeToCodeWhisperer(req, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	history := cw.ConversationState.History
	if len(history) != 2 || history[0].User == nil || history[1].Assistant == nil {
		t.Fatalf("expected original user/tool pair: %#v", history)
	}
	if !strings.Contains(history[0].User.Content, "Keep the original instructions") || len(history[1].Assistant.ToolUses) != 1 || history[1].Assistant.ToolUses[0].ToolUseID != "paired" {
		t.Fatalf("system prompt or paired call lost: %#v", history)
	}
	current := cw.ConversationState.CurrentMessage.UserInputMessage
	if current.Content != "inspect image" || len(current.Images) != 1 || current.Images[0].Source.Bytes != "aW1hZ2U=" {
		t.Fatalf("user content or image lost: %#v", current)
	}
	if current.UserInputMessageContext == nil || len(current.UserInputMessageContext.ToolResults) != 1 {
		t.Fatalf("tool result lost: %#v", current)
	}
	result := current.UserInputMessageContext.ToolResults[0]
	if result.ToolUseID != "paired" || len(result.Content) != 1 || result.Content[0]["text"] != "file content" {
		t.Fatalf("tool result changed: %#v", result)
	}
}

func TestHistoryOrphanCleanupDoesNotInjectVisibleReply(t *testing.T) {
	history := cleanOrphanToolUses([]HistoryEntry{{
		Type:      "assistant",
		Assistant: &HistoryAssistantMessage{ToolUses: []ToolUseEntry{{ToolUseID: "orphan", Name: "read"}}},
	}}, &UnifiedMessage{Role: "user", TextParts: []string{"continue"}})
	if len(history) != 1 || len(history[0].Assistant.ToolUses) != 0 || history[0].Assistant.Content == "" || strings.TrimSpace(history[0].Assistant.Content) != "" {
		t.Fatalf("expected only blank protocol padding after orphan cleanup: %#v", history)
	}
}
