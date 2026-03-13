package kiro

import (
	"testing"
)

// ==================== mergeMessages Tests ====================

func TestMergeMessages_ToolUseDedup(t *testing.T) {
	t.Run("no duplicates", func(t *testing.T) {
		target := &UnifiedMessage{
			ToolUses: []ToolUseData{
				{ID: "t1", Name: "read_file", Input: map[string]any{"path": "/a.go"}},
			},
		}
		source := &UnifiedMessage{
			ToolUses: []ToolUseData{
				{ID: "t2", Name: "write_file", Input: map[string]any{"path": "/b.go"}},
			},
		}
		mergeMessages(target, source)
		if len(target.ToolUses) != 2 {
			t.Errorf("expected 2 tool_uses, got %d", len(target.ToolUses))
		}
	})

	t.Run("duplicate tool_use ID skipped", func(t *testing.T) {
		target := &UnifiedMessage{
			ToolUses: []ToolUseData{
				{ID: "t1", Name: "read_file", Input: map[string]any{"path": "/a.go"}},
			},
		}
		source := &UnifiedMessage{
			ToolUses: []ToolUseData{
				{ID: "t1", Name: "read_file", Input: map[string]any{"path": "/b.go"}},
				{ID: "t2", Name: "write_file", Input: map[string]any{"path": "/c.go"}},
			},
		}
		mergeMessages(target, source)
		if len(target.ToolUses) != 2 {
			t.Errorf("expected 2 tool_uses (t1 + t2), got %d", len(target.ToolUses))
		}
		// First should be original t1 (path /a.go)
		if target.ToolUses[0].ID != "t1" {
			t.Errorf("expected first tool_use ID t1, got %s", target.ToolUses[0].ID)
		}
		if target.ToolUses[0].Input["path"] != "/a.go" {
			t.Errorf("expected first t1 to retain original input /a.go, got %v", target.ToolUses[0].Input["path"])
		}
		// Second should be t2
		if target.ToolUses[1].ID != "t2" {
			t.Errorf("expected second tool_use ID t2, got %s", target.ToolUses[1].ID)
		}
	})

	t.Run("empty ID tool_use always appended", func(t *testing.T) {
		target := &UnifiedMessage{
			ToolUses: []ToolUseData{
				{ID: "", Name: "tool_a", Input: map[string]any{}},
			},
		}
		source := &UnifiedMessage{
			ToolUses: []ToolUseData{
				{ID: "", Name: "tool_b", Input: map[string]any{}},
			},
		}
		mergeMessages(target, source)
		if len(target.ToolUses) != 2 {
			t.Errorf("expected 2 empty-ID tool_uses (both kept), got %d", len(target.ToolUses))
		}
	})

	t.Run("multiple duplicates from source", func(t *testing.T) {
		target := &UnifiedMessage{
			ToolUses: []ToolUseData{
				{ID: "t1", Name: "cmd1"},
				{ID: "t2", Name: "cmd2"},
			},
		}
		source := &UnifiedMessage{
			ToolUses: []ToolUseData{
				{ID: "t1", Name: "cmd1_dup"},
				{ID: "t2", Name: "cmd2_dup"},
				{ID: "t3", Name: "cmd3"},
			},
		}
		mergeMessages(target, source)
		if len(target.ToolUses) != 3 {
			t.Errorf("expected 3 tool_uses (t1, t2, t3), got %d", len(target.ToolUses))
		}
		if target.ToolUses[2].ID != "t3" {
			t.Errorf("expected third tool_use to be t3, got %s", target.ToolUses[2].ID)
		}
	})
}

func TestMergeMessages_ToolResultDedup(t *testing.T) {
	t.Run("no duplicates", func(t *testing.T) {
		target := &UnifiedMessage{
			ToolResults: []ToolResultData{
				{ToolUseID: "t1", Content: "result 1"},
			},
		}
		source := &UnifiedMessage{
			ToolResults: []ToolResultData{
				{ToolUseID: "t2", Content: "result 2"},
			},
		}
		mergeMessages(target, source)
		if len(target.ToolResults) != 2 {
			t.Errorf("expected 2 tool_results, got %d", len(target.ToolResults))
		}
	})

	t.Run("duplicate tool_result ToolUseID skipped", func(t *testing.T) {
		target := &UnifiedMessage{
			ToolResults: []ToolResultData{
				{ToolUseID: "t1", Content: "original result"},
			},
		}
		source := &UnifiedMessage{
			ToolResults: []ToolResultData{
				{ToolUseID: "t1", Content: "duplicate result"},
				{ToolUseID: "t2", Content: "new result"},
			},
		}
		mergeMessages(target, source)
		if len(target.ToolResults) != 2 {
			t.Errorf("expected 2 tool_results (t1 + t2), got %d", len(target.ToolResults))
		}
		// First should be original t1
		if target.ToolResults[0].Content != "original result" {
			t.Errorf("expected original result for t1, got %q", target.ToolResults[0].Content)
		}
		// Second should be t2
		if target.ToolResults[1].ToolUseID != "t2" {
			t.Errorf("expected second result to be t2, got %s", target.ToolResults[1].ToolUseID)
		}
	})

	t.Run("empty ToolUseID always appended", func(t *testing.T) {
		target := &UnifiedMessage{
			ToolResults: []ToolResultData{
				{ToolUseID: "", Content: "result a"},
			},
		}
		source := &UnifiedMessage{
			ToolResults: []ToolResultData{
				{ToolUseID: "", Content: "result b"},
			},
		}
		mergeMessages(target, source)
		if len(target.ToolResults) != 2 {
			t.Errorf("expected 2 empty-ID tool_results (both kept), got %d", len(target.ToolResults))
		}
	})
}

func TestMergeMessages_TextAndThinking(t *testing.T) {
	t.Run("text and images merged", func(t *testing.T) {
		target := &UnifiedMessage{
			TextParts: []string{"hello"},
		}
		source := &UnifiedMessage{
			TextParts: []string{"world"},
		}
		mergeMessages(target, source)
		if len(target.TextParts) != 2 {
			t.Errorf("expected 2 text parts, got %d", len(target.TextParts))
		}
	})

	t.Run("thinking from source overwrites", func(t *testing.T) {
		targetThinking := &ThinkingData{Content: "old"}
		sourceThinking := &ThinkingData{Content: "new"}
		target := &UnifiedMessage{Thinking: targetThinking}
		source := &UnifiedMessage{Thinking: sourceThinking}
		mergeMessages(target, source)
		if target.Thinking.Content != "new" {
			t.Errorf("expected thinking to be overwritten to 'new', got %q", target.Thinking.Content)
		}
	})
}

// ==================== buildAssistantHistoryEntry Tests ====================

func TestBuildAssistantHistoryEntry_ToolUseDedup(t *testing.T) {
	t.Run("no duplicates", func(t *testing.T) {
		ctx := NewTransformContext("claude-sonnet-4-6", nil)
		msg := &UnifiedMessage{
			TextParts: []string{"ok"},
			ToolUses: []ToolUseData{
				{ID: "t1", Name: "read_file", Input: map[string]any{"path": "/a.go"}},
				{ID: "t2", Name: "write_file", Input: map[string]any{"path": "/b.go"}},
			},
		}
		entry := buildAssistantHistoryEntry(ctx, msg)
		if len(entry.Assistant.ToolUses) != 2 {
			t.Errorf("expected 2 tool_uses, got %d", len(entry.Assistant.ToolUses))
		}
		// Both should be registered
		if ctx.ToolUseIDMap["t1"] != "read_file" {
			t.Errorf("expected t1 registered as read_file, got %q", ctx.ToolUseIDMap["t1"])
		}
		if ctx.ToolUseIDMap["t2"] != "write_file" {
			t.Errorf("expected t2 registered as write_file, got %q", ctx.ToolUseIDMap["t2"])
		}
	})

	t.Run("duplicate tool_use ID keeps first only", func(t *testing.T) {
		ctx := NewTransformContext("claude-sonnet-4-6", nil)
		msg := &UnifiedMessage{
			TextParts: []string{"ok"},
			ToolUses: []ToolUseData{
				{ID: "t1", Name: "read_file", Input: map[string]any{"path": "/a.go"}},
				{ID: "t1", Name: "read_file", Input: map[string]any{"path": "/b.go"}},
				{ID: "t2", Name: "write_file", Input: map[string]any{"path": "/c.go"}},
			},
		}
		entry := buildAssistantHistoryEntry(ctx, msg)
		if len(entry.Assistant.ToolUses) != 2 {
			t.Errorf("expected 2 tool_uses (t1 first + t2), got %d", len(entry.Assistant.ToolUses))
		}
		// First should be t1
		if entry.Assistant.ToolUses[0].ToolUseID != "t1" {
			t.Errorf("expected first tool_use to be t1, got %s", entry.Assistant.ToolUses[0].ToolUseID)
		}
		// Second should be t2
		if entry.Assistant.ToolUses[1].ToolUseID != "t2" {
			t.Errorf("expected second tool_use to be t2, got %s", entry.Assistant.ToolUses[1].ToolUseID)
		}
		// Only first t1 registered
		if ctx.ToolUseIDMap["t1"] != "read_file" {
			t.Errorf("expected t1 registered as read_file, got %q", ctx.ToolUseIDMap["t1"])
		}
	})

	t.Run("all duplicates keeps first", func(t *testing.T) {
		ctx := NewTransformContext("claude-sonnet-4-6", nil)
		msg := &UnifiedMessage{
			TextParts: []string{"thinking"},
			ToolUses: []ToolUseData{
				{ID: "t1", Name: "cmd", Input: map[string]any{}},
				{ID: "t1", Name: "cmd", Input: map[string]any{}},
				{ID: "t1", Name: "cmd", Input: map[string]any{}},
			},
		}
		entry := buildAssistantHistoryEntry(ctx, msg)
		if len(entry.Assistant.ToolUses) != 1 {
			t.Errorf("expected 1 tool_use after dedup, got %d", len(entry.Assistant.ToolUses))
		}
	})

	t.Run("empty ID tool_use passes through", func(t *testing.T) {
		ctx := NewTransformContext("claude-sonnet-4-6", nil)
		msg := &UnifiedMessage{
			TextParts: []string{"ok"},
			ToolUses: []ToolUseData{
				{ID: "", Name: "tool_a", Input: map[string]any{}},
				{ID: "", Name: "tool_b", Input: map[string]any{}},
			},
		}
		entry := buildAssistantHistoryEntry(ctx, msg)
		// Both empty-ID entries should pass through (not deduped)
		if len(entry.Assistant.ToolUses) != 2 {
			t.Errorf("expected 2 empty-ID tool_uses (both kept), got %d", len(entry.Assistant.ToolUses))
		}
	})

	t.Run("no tool_uses", func(t *testing.T) {
		ctx := NewTransformContext("claude-sonnet-4-6", nil)
		msg := &UnifiedMessage{
			TextParts: []string{"just text"},
		}
		entry := buildAssistantHistoryEntry(ctx, msg)
		if len(entry.Assistant.ToolUses) != 0 {
			t.Errorf("expected 0 tool_uses, got %d", len(entry.Assistant.ToolUses))
		}
	})

	t.Run("empty text gets space backfill", func(t *testing.T) {
		ctx := NewTransformContext("claude-sonnet-4-6", nil)
		msg := &UnifiedMessage{
			ToolUses: []ToolUseData{
				{ID: "t1", Name: "cmd", Input: map[string]any{}},
			},
		}
		entry := buildAssistantHistoryEntry(ctx, msg)
		if entry.Assistant.Content != " " {
			t.Errorf("expected space backfill for empty text, got %q", entry.Assistant.Content)
		}
	})
}
