package kiro

import (
	"strings"
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

func TestGenerateThinkingPrefix_AdaptiveMatchesKiroRS(t *testing.T) {
	req := &ClaudeRequest{
		Thinking: map[string]any{"type": "adaptive"},
		OutputConfig: map[string]any{
			"effort": "medium",
		},
	}

	got := generateThinkingPrefix(req)
	want := "<thinking_mode>adaptive</thinking_mode><thinking_effort>medium</thinking_effort>"
	if got != want {
		t.Fatalf("generateThinkingPrefix() = %q, want %q", got, want)
	}
	if strings.Contains(got, "<max_thinking_length>") {
		t.Fatalf("adaptive prefix should not contain max_thinking_length: %q", got)
	}
}

func TestGenerateThinkingPrefix_AdaptiveNormalizesEffort(t *testing.T) {
	req := &ClaudeRequest{
		Thinking: map[string]any{"type": "adaptive"},
		OutputConfig: map[string]any{
			"effort": "xhigh",
		},
	}

	got := generateThinkingPrefix(req)
	want := "<thinking_mode>adaptive</thinking_mode><thinking_effort>high</thinking_effort>"
	if got != want {
		t.Fatalf("generateThinkingPrefix() = %q, want %q", got, want)
	}
}

func TestNormalizeJSONSchema_AlignsKiroRSDefaults(t *testing.T) {
	input := map[string]any{
		"$schema":              nil,
		"type":                 nil,
		"properties":           nil,
		"required":             nil,
		"additionalProperties": nil,
	}

	got := normalizeJSONSchema(input)
	if got["$schema"] != jsonSchemaDraft07 {
		t.Fatalf("$schema = %v, want %s", got["$schema"], jsonSchemaDraft07)
	}
	if got["type"] != "object" {
		t.Fatalf("type = %v, want object", got["type"])
	}
	if _, ok := got["properties"].(map[string]any); !ok {
		t.Fatalf("properties = %T, want map[string]any", got["properties"])
	}
	if required, ok := got["required"].([]any); !ok || len(required) != 0 {
		t.Fatalf("required = %#v, want empty []any", got["required"])
	}
	if got["additionalProperties"] != true {
		t.Fatalf("additionalProperties = %v, want true", got["additionalProperties"])
	}
}

func TestProcessTools_PreservesCreatePlanSchemaConstraints(t *testing.T) {
	tools := []ClaudeTool{
		{
			Name:        "update_plan",
			Description: "",
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"plan": map[string]any{
						"type":     "array",
						"minItems": float64(1),
						"items": map[string]any{
							"type":                 "object",
							"additionalProperties": false,
							"properties": map[string]any{
								"step":   map[string]any{"type": "string", "minLength": float64(1)},
								"status": map[string]any{"type": "string", "enum": []any{"pending", "in_progress", "completed"}},
							},
							"required": []any{"step", "status"},
						},
					},
				},
				"required": []any{"plan"},
			},
		},
	}

	processed := processTools(tools, true, BuildToolNameMapFromClaudeTools(tools))
	if len(processed) != 1 || processed[0].Standard == nil {
		t.Fatalf("expected one standard tool, got %#v", processed)
	}
	spec := processed[0].Standard.ToolSpecification
	if !strings.HasPrefix(spec.Description, "Tool: update_plan") {
		t.Fatalf("description = %q, want fallback tool description", spec.Description)
	}
	schema := spec.InputSchema.JSON
	if schema["additionalProperties"] != false {
		t.Fatalf("top-level additionalProperties = %v, want false", schema["additionalProperties"])
	}
	plan := schema["properties"].(map[string]any)["plan"].(map[string]any)
	if plan["minItems"] != float64(1) {
		t.Fatalf("plan.minItems = %v, want 1", plan["minItems"])
	}
	item := plan["items"].(map[string]any)
	if item["additionalProperties"] != false {
		t.Fatalf("plan.items.additionalProperties = %v, want false", item["additionalProperties"])
	}
	step := item["properties"].(map[string]any)["step"].(map[string]any)
	if step["minLength"] != float64(1) {
		t.Fatalf("step.minLength = %v, want 1", step["minLength"])
	}
}

func TestDetermineChatTriggerType_AlwaysManualForToolChoice(t *testing.T) {
	for _, toolChoice := range []any{
		map[string]any{"type": "any"},
		map[string]any{"type": "tool", "name": "update_plan"},
		"required",
	} {
		got := determineChatTriggerType(&ClaudeRequest{
			Tools:      []ClaudeTool{{Name: "update_plan", InputSchema: map[string]any{"type": "object"}}},
			ToolChoice: toolChoice,
		})
		if got != "MANUAL" {
			t.Fatalf("determineChatTriggerType(%#v) = %q, want MANUAL", toolChoice, got)
		}
	}
}

func TestIsToolChoiceRequired_ClaudeAnyAndTool(t *testing.T) {
	for _, toolChoice := range []any{
		"required",
		map[string]any{"type": "required"},
		map[string]any{"type": "any"},
		map[string]any{"type": "tool", "name": "update_plan"},
	} {
		if !isToolChoiceRequired(toolChoice) {
			t.Fatalf("isToolChoiceRequired(%#v) = false, want true", toolChoice)
		}
	}
	if isToolChoiceRequired(map[string]any{"type": "auto"}) {
		t.Fatal("isToolChoiceRequired(auto) = true, want false")
	}
}

func TestTransformClaudeToCodeWhisperer_MapsHistoryToolNames(t *testing.T) {
	longName := "mcp__very_long_server_name_for_testing__" + strings.Repeat("create_plan_segment_", 4)
	req := &ClaudeRequest{
		Model: "claude-sonnet-4-6",
		Messages: []ClaudeMessage{
			{Role: "user", Content: "make a plan"},
			{Role: "assistant", Content: []any{
				map[string]any{"type": "tool_use", "id": "toolu_1", "name": longName, "input": map[string]any{"plan": []any{}}},
			}},
			{Role: "user", Content: []any{
				map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "ok"},
			}},
		},
		Tools: []ClaudeTool{{
			Name:        longName,
			Description: "Create a plan",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		}},
	}

	cwReq, err := TransformClaudeToCodeWhisperer(req, "", nil)
	if err != nil {
		t.Fatalf("TransformClaudeToCodeWhisperer error: %v", err)
	}

	var historyToolName string
	for _, entry := range cwReq.ConversationState.History {
		if entry.Assistant != nil && len(entry.Assistant.ToolUses) > 0 {
			historyToolName = entry.Assistant.ToolUses[0].Name
			break
		}
	}
	if historyToolName == "" {
		t.Fatal("expected history tool_use")
	}
	if historyToolName == longName {
		t.Fatalf("history tool name was not shortened")
	}
	if len(historyToolName) > ToolNameLimit {
		t.Fatalf("history tool name length = %d, want <= %d", len(historyToolName), ToolNameLimit)
	}

	tools := cwReq.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools
	if len(tools) != 1 || tools[0].Standard == nil {
		t.Fatalf("expected one current tool, got %#v", tools)
	}
	currentToolName := tools[0].Standard.ToolSpecification.Name
	if currentToolName != historyToolName {
		t.Fatalf("current tool name = %q, history tool name = %q", currentToolName, historyToolName)
	}
	if original := BuildReverseMapFromClaudeTools(req.Tools)[currentToolName]; original != longName {
		t.Fatalf("reverse tool name = %q, want %q", original, longName)
	}
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

func TestNormalizeInternalTaskNotifications_StripsInternalMetadata(t *testing.T) {
	input := `<task-notification>
<task-id>a10d31dfec1e0c72a</task-id>
<tool-use-id>toolu_internal</tool-use-id>
<output-file>/private/tmp/internal.output</output-file>
<status>completed</status>
<summary>Agent "inspect chat" finished</summary>
<note>internal lifecycle metadata</note>
<result>Found the relevant request path.</result>
<usage><tool_uses>12</tool_uses></usage>
</task-notification>`

	got := normalizeInternalTaskNotifications(input)
	for _, forbidden := range []string{
		"<task-notification>",
		"a10d31dfec1e0c72a",
		"toolu_internal",
		"/private/tmp/internal.output",
		"internal lifecycle metadata",
		"<usage>",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("normalized notification leaked %q: %s", forbidden, got)
		}
	}
	if !strings.Contains(got, "Found the relevant request path.") {
		t.Fatalf("normalized notification lost result: %s", got)
	}
	if !strings.Contains(got, "do not quote or reproduce") {
		t.Fatalf("normalized notification missing non-echo instruction: %s", got)
	}
}

func TestNormalizeInternalTaskNotifications_PreservesXMLLikeResultContent(t *testing.T) {
	input := `<task-notification>
<task-id>internal-id</task-id>
<tool-use-id>internal-tool-id</tool-use-id>
<output-file>/private/tmp/internal.output</output-file>
<status>completed</status>
<summary>Checked literal </summary> and kept reading.</summary>
<result>Documented </result>, </status>, and </task-notification> as literal closing tags.</result>
<usage><tool_uses>1</tool_uses></usage>
</task-notification>`

	got := normalizeInternalTaskNotifications(input)
	for _, want := range []string{
		"Checked literal </summary> and kept reading.",
		"Documented </result>, </status>, and </task-notification> as literal closing tags.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("normalized notification lost %q: %s", want, got)
		}
	}
	for _, forbidden := range []string{"internal-id", "internal-tool-id", "/private/tmp/internal.output", "<usage>"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("normalized notification leaked %q: %s", forbidden, got)
		}
	}
}

func TestNormalizeInternalTaskNotifications_LeavesOrdinaryTextUntouched(t *testing.T) {
	tests := []string{
		"Please explain what a <task-notification> block means.",
		`<task-notification>
<status>completed</status>
<summary>Example supplied by the user</summary>
<result>Please analyze this envelope literally.</result>
</task-notification>`,
	}
	for _, input := range tests {
		if got := normalizeInternalTaskNotifications(input); got != input {
			t.Fatalf("ordinary text changed: %q", got)
		}
	}
}

func TestTransformClaudeToCodeWhisperer_NormalizesCurrentTaskNotification(t *testing.T) {
	req := &ClaudeRequest{
		Model: "claude-opus-4-8",
		Messages: []ClaudeMessage{{
			Role: "user",
			Content: `<task-notification>
<task-id>internal-id</task-id>
<tool-use-id>internal-tool-id</tool-use-id>
<output-file>/private/tmp/internal.output</output-file>
<status>completed</status>
<summary>Background inspection finished</summary>
<result>Use the request transformer.</result>
</task-notification>`,
		}},
	}

	cwReq, err := TransformClaudeToCodeWhisperer(req, "", nil)
	if err != nil {
		t.Fatalf("TransformClaudeToCodeWhisperer error: %v", err)
	}
	content := cwReq.ConversationState.CurrentMessage.UserInputMessage.Content
	if strings.Contains(content, "internal-id") || strings.Contains(content, "/private/tmp/internal.output") {
		t.Fatalf("current message leaked internal metadata: %s", content)
	}
	if !strings.Contains(content, "Use the request transformer.") {
		t.Fatalf("current message lost task result: %s", content)
	}
}

func TestCleanOrphanToolUses_FiltersCurrentResultsAndUnpairedUses(t *testing.T) {
	history := []HistoryEntry{
		{
			Type: "assistant",
			Assistant: &HistoryAssistantMessage{
				Content: "working",
				ToolUses: []ToolUseEntry{
					{ToolUseID: "t1", Name: "one"},
					{ToolUseID: "t2", Name: "two"},
					{ToolUseID: "t3", Name: "three"},
				},
			},
		},
		{
			Type: "user",
			User: &HistoryUserMessage{UserInputMessageContext: &UserInputMessageContext{
				ToolResults: []ToolResult{{ToolUseID: "t2"}},
			}},
		},
	}
	current := &UnifiedMessage{ToolResults: []ToolResultData{
		{ToolUseID: "t1", Content: "valid"},
		{ToolUseID: "t2", Content: "already paired"},
		{ToolUseID: "missing", Content: "orphan"},
		{ToolUseID: "t1", Content: "duplicate"},
		{ToolUseID: "", Content: "empty id"},
	}}

	cleaned := cleanOrphanToolUses(history, current)
	if len(current.ToolResults) != 1 || current.ToolResults[0].ToolUseID != "t1" {
		t.Fatalf("current tool results = %#v, want only t1", current.ToolResults)
	}
	toolUses := cleaned[0].Assistant.ToolUses
	if len(toolUses) != 2 || toolUses[0].ToolUseID != "t1" || toolUses[1].ToolUseID != "t2" {
		t.Fatalf("history tool uses = %#v, want paired t1 and t2", toolUses)
	}
}

func TestCleanOrphanToolUses_RemovesCurrentResultWithoutHistoryUse(t *testing.T) {
	current := &UnifiedMessage{ToolResults: []ToolResultData{{ToolUseID: "missing", Content: "orphan"}}}
	cleaned := cleanOrphanToolUses(nil, current)
	if len(cleaned) != 0 {
		t.Fatalf("history = %#v, want empty", cleaned)
	}
	if len(current.ToolResults) != 0 {
		t.Fatalf("current tool results = %#v, want empty", current.ToolResults)
	}
}

func TestTransformClaudeToCodeWhisperer_RemovesOrphanOnlyCurrentResult(t *testing.T) {
	req := &ClaudeRequest{
		Model: "claude-opus-4-8",
		Messages: []ClaudeMessage{{
			Role: "user",
			Content: []any{
				map[string]any{"type": "tool_result", "tool_use_id": "missing", "content": "orphan"},
			},
		}},
	}

	cwReq, err := TransformClaudeToCodeWhisperer(req, "", nil)
	if err != nil {
		t.Fatalf("TransformClaudeToCodeWhisperer error: %v", err)
	}
	context := cwReq.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if context != nil && len(context.ToolResults) != 0 {
		t.Fatalf("current tool results = %#v, want none", context.ToolResults)
	}
}
