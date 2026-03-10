package service

import (
	"encoding/json"
	"testing"
)

func TestInjectCacheControlBreakpoints_Tools(t *testing.T) {
	// tools 数组中无 cache_control → 在最后一个 tool 上注入
	body := `{
		"model": "claude-sonnet-4-20250514",
		"tools": [
			{"name": "tool1", "description": "first"},
			{"name": "tool2", "description": "second"}
		],
		"messages": [{"role": "user", "content": "hi"}]
	}`

	result, injected := injectCacheControlBreakpoints([]byte(body))
	if !injected {
		t.Error("expected injected=true when no existing cache_control")
	}
	var data map[string]any
	if err := json.Unmarshal(result, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	tools := data["tools"].([]any)

	// 第一个 tool 不应有 cache_control
	tool0 := tools[0].(map[string]any)
	if _, has := tool0["cache_control"]; has {
		t.Error("tool[0] should not have cache_control")
	}

	// 最后一个 tool 应有 cache_control
	tool1 := tools[1].(map[string]any)
	cc, ok := tool1["cache_control"].(map[string]any)
	if !ok {
		t.Fatal("tool[1] should have cache_control")
	}
	if cc["type"] != "ephemeral" {
		t.Errorf("expected ephemeral, got %v", cc["type"])
	}
}

func TestInjectCacheControlBreakpoints_ToolsExistingCacheControl(t *testing.T) {
	// tools 数组中已有 cache_control → 全局不注入（方案 A）
	body := `{
		"tools": [
			{"name": "tool1", "cache_control": {"type": "ephemeral"}},
			{"name": "tool2"}
		],
		"messages": [{"role": "user", "content": "hi"}]
	}`

	result, injected := injectCacheControlBreakpoints([]byte(body))
	if injected {
		t.Error("expected injected=false when existing cache_control in tools")
	}
	var data map[string]any
	json.Unmarshal(result, &data)

	tools := data["tools"].([]any)
	tool1 := tools[1].(map[string]any)
	if _, has := tool1["cache_control"]; has {
		t.Error("should not inject when existing cache_control exists in tools region")
	}
}

func TestInjectCacheControlBreakpoints_System(t *testing.T) {
	// system 数组中无 cache_control → 在最后一个非 thinking block 上注入
	body := `{
		"system": [
			{"type": "text", "text": "You are helpful"},
			{"type": "text", "text": "Be concise"}
		],
		"messages": [{"role": "user", "content": "hi"}]
	}`

	result, injected := injectCacheControlBreakpoints([]byte(body))
	if !injected {
		t.Error("expected injected=true when no existing cache_control in system")
	}
	var data map[string]any
	json.Unmarshal(result, &data)

	system := data["system"].([]any)
	last := system[1].(map[string]any)
	cc, ok := last["cache_control"].(map[string]any)
	if !ok {
		t.Fatal("last system block should have cache_control")
	}
	if cc["type"] != "ephemeral" {
		t.Errorf("expected ephemeral, got %v", cc["type"])
	}
}

func TestInjectCacheControlBreakpoints_SystemSkipThinking(t *testing.T) {
	// system 最后一个是 thinking block → 注入到倒数第二个
	body := `{
		"system": [
			{"type": "text", "text": "system prompt"},
			{"type": "thinking", "thinking": "..."}
		],
		"messages": [{"role": "user", "content": "hi"}]
	}`

	result, injected := injectCacheControlBreakpoints([]byte(body))
	if !injected {
		t.Error("expected injected=true when system has non-thinking block to inject")
	}
	var data map[string]any
	json.Unmarshal(result, &data)

	system := data["system"].([]any)
	// text block should get cache_control
	textBlock := system[0].(map[string]any)
	if _, has := textBlock["cache_control"]; !has {
		t.Error("text block should have cache_control since thinking block was skipped")
	}

	// thinking block should NOT have cache_control
	thinkingBlock := system[1].(map[string]any)
	if _, has := thinkingBlock["cache_control"]; has {
		t.Error("thinking block should not have cache_control")
	}
}

func TestInjectCacheControlBreakpoints_Messages(t *testing.T) {
	// ≥2 个 user 消息 + 无 cache_control → 在倒数第二个 user 消息的最后一个 content block 上注入
	body := `{
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "first question"}]},
			{"role": "assistant", "content": [{"type": "text", "text": "answer"}]},
			{"role": "user", "content": [{"type": "text", "text": "second question"}]}
		]
	}`

	result, injected := injectCacheControlBreakpoints([]byte(body))
	if !injected {
		t.Error("expected injected=true when ≥2 user messages and no existing cache_control")
	}
	var data map[string]any
	json.Unmarshal(result, &data)

	messages := data["messages"].([]any)

	// 倒数第二个 user 消息（index 0）应有 cache_control
	msg0 := messages[0].(map[string]any)
	content0 := msg0["content"].([]any)
	block0 := content0[0].(map[string]any)
	if _, has := block0["cache_control"]; !has {
		t.Error("second-to-last user message content should have cache_control")
	}

	// 最后一个 user 消息（index 2）不应有 cache_control
	msg2 := messages[2].(map[string]any)
	content2 := msg2["content"].([]any)
	block2 := content2[0].(map[string]any)
	if _, has := block2["cache_control"]; has {
		t.Error("last user message should not have cache_control")
	}
}

func TestInjectCacheControlBreakpoints_MessagesStringContent(t *testing.T) {
	// user 消息 content 是纯字符串 → 转为数组形式后注入
	body := `{
		"messages": [
			{"role": "user", "content": "first"},
			{"role": "assistant", "content": "answer"},
			{"role": "user", "content": "second"}
		]
	}`

	result, injected := injectCacheControlBreakpoints([]byte(body))
	if !injected {
		t.Error("expected injected=true for string content conversion")
	}
	var data map[string]any
	json.Unmarshal(result, &data)

	messages := data["messages"].([]any)
	msg0 := messages[0].(map[string]any)
	// content should now be an array
	content0, ok := msg0["content"].([]any)
	if !ok {
		t.Fatal("string content should be converted to array")
	}
	block := content0[0].(map[string]any)
	if block["type"] != "text" {
		t.Error("converted block should be type text")
	}
	if block["text"] != "first" {
		t.Error("converted block should preserve original text")
	}
	if _, has := block["cache_control"]; !has {
		t.Error("converted block should have cache_control")
	}
}

func TestInjectCacheControlBreakpoints_SingleUserMessage(t *testing.T) {
	// 只有 1 个 user 消息 → 不注入 messages（无 tools/system 也无需注入）
	body := `{
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "hello"}]}
		]
	}`

	result, injected := injectCacheControlBreakpoints([]byte(body))
	if injected {
		t.Error("expected injected=false when only 1 user message and no tools/system")
	}
	var data map[string]any
	json.Unmarshal(result, &data)

	messages := data["messages"].([]any)
	msg0 := messages[0].(map[string]any)
	content0 := msg0["content"].([]any)
	block := content0[0].(map[string]any)
	if _, has := block["cache_control"]; has {
		t.Error("should not inject when only 1 user message")
	}
}

func TestInjectCacheControlBreakpoints_NoModificationOnEmptyArrays(t *testing.T) {
	body := `{"model": "claude-sonnet-4-20250514", "messages": [{"role": "user", "content": "hi"}]}`
	result, injected := injectCacheControlBreakpoints([]byte(body))
	if injected {
		t.Error("expected injected=false when nothing to inject")
	}

	// Should return valid JSON unchanged (no tools, no system, only 1 user message)
	var data map[string]any
	if err := json.Unmarshal(result, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
}

func TestInjectCacheControlBreakpoints_Combined(t *testing.T) {
	// tools + system + 2 user messages → all three regions should be injected
	body := `{
		"tools": [{"name": "t1"}],
		"system": [{"type": "text", "text": "sys"}],
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "q1"}]},
			{"role": "assistant", "content": [{"type": "text", "text": "a1"}]},
			{"role": "user", "content": [{"type": "text", "text": "q2"}]}
		]
	}`

	result, injected := injectCacheControlBreakpoints([]byte(body))
	if !injected {
		t.Error("expected injected=true when all regions need injection")
	}
	var data map[string]any
	json.Unmarshal(result, &data)

	// tools: last tool should have cache_control
	tools := data["tools"].([]any)
	if _, has := tools[0].(map[string]any)["cache_control"]; !has {
		t.Error("tool should have cache_control")
	}

	// system: last block should have cache_control
	system := data["system"].([]any)
	if _, has := system[0].(map[string]any)["cache_control"]; !has {
		t.Error("system block should have cache_control")
	}

	// messages: second-to-last user message should have cache_control
	messages := data["messages"].([]any)
	msg0 := messages[0].(map[string]any)
	content0 := msg0["content"].([]any)
	if _, has := content0[0].(map[string]any)["cache_control"]; !has {
		t.Error("second-to-last user message should have cache_control")
	}
}

func TestInjectCacheControlBreakpoints_Idempotent(t *testing.T) {
	// 调用两次不应产生重复的 cache_control
	body := `{
		"tools": [{"name": "t1"}],
		"system": [{"type": "text", "text": "sys"}],
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "q1"}]},
			{"role": "assistant", "content": [{"type": "text", "text": "a1"}]},
			{"role": "user", "content": [{"type": "text", "text": "q2"}]}
		]
	}`

	result1, injected1 := injectCacheControlBreakpoints([]byte(body))
	if !injected1 {
		t.Error("first call: expected injected=true")
	}
	// 第二次调用：所有区域已有 cache_control（由第一次注入），应返回 false
	result2, injected2 := injectCacheControlBreakpoints(result1)
	if injected2 {
		t.Error("second call: expected injected=false (first call's cache_control treated as user's)")
	}

	var data1, data2 map[string]any
	json.Unmarshal(result1, &data1)
	json.Unmarshal(result2, &data2)

	// Count total cache_control in each — should be identical
	count1 := countAllCacheControl(data1)
	count2 := countAllCacheControl(data2)
	if count1 != count2 {
		t.Errorf("idempotency violated: first pass=%d, second pass=%d cache_controls", count1, count2)
	}
}

func TestInjectCacheControlBreakpoints_SystemAllThinking(t *testing.T) {
	// system 全是 thinking block → 不应注入也不应 panic
	body := `{
		"system": [
			{"type": "thinking", "thinking": "..."},
			{"type": "thinking", "thinking": "..."}
		],
		"messages": [{"role": "user", "content": "hi"}]
	}`

	result, injected := injectCacheControlBreakpoints([]byte(body))
	if injected {
		t.Error("expected injected=false when system is all thinking blocks")
	}
	var data map[string]any
	json.Unmarshal(result, &data)

	system := data["system"].([]any)
	for i, item := range system {
		m := item.(map[string]any)
		if _, has := m["cache_control"]; has {
			t.Errorf("thinking block system[%d] should not have cache_control", i)
		}
	}
}

func TestInjectCacheControlBreakpoints_ThreeUserMessages(t *testing.T) {
	// 3 个 user 消息 → 倒数第二个（index 2 的 user msg）应被注入
	body := `{
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "q1"}]},
			{"role": "assistant", "content": [{"type": "text", "text": "a1"}]},
			{"role": "user", "content": [{"type": "text", "text": "q2"}]},
			{"role": "assistant", "content": [{"type": "text", "text": "a2"}]},
			{"role": "user", "content": [{"type": "text", "text": "q3"}]}
		]
	}`

	result, injected := injectCacheControlBreakpoints([]byte(body))
	if !injected {
		t.Error("expected injected=true for 3 user messages")
	}
	var data map[string]any
	json.Unmarshal(result, &data)

	messages := data["messages"].([]any)

	// q1 (user index 0) → no cache_control
	msg0 := messages[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if _, has := msg0["cache_control"]; has {
		t.Error("first user message should not have cache_control")
	}

	// q2 (user index 1, second-to-last user) → should have cache_control
	msg2 := messages[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if _, has := msg2["cache_control"]; !has {
		t.Error("second-to-last user message (q2) should have cache_control")
	}

	// q3 (user index 2, last user) → no cache_control
	msg4 := messages[4].(map[string]any)["content"].([]any)[0].(map[string]any)
	if _, has := msg4["cache_control"]; has {
		t.Error("last user message should not have cache_control")
	}
}

// --- 方案 A 混合区域测试 ---

func TestInjectCacheControlBreakpoints_MixedRegions_UserCacheInTools(t *testing.T) {
	// 方案 A：用户在 tools 有 cache_control，但 system/messages 没有 → 全局不注入
	body := `{
		"tools": [
			{"name": "tool1", "cache_control": {"type": "ephemeral"}},
			{"name": "tool2"}
		],
		"system": [{"type": "text", "text": "sys"}],
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "q1"}]},
			{"role": "assistant", "content": [{"type": "text", "text": "a1"}]},
			{"role": "user", "content": [{"type": "text", "text": "q2"}]}
		]
	}`

	result, injected := injectCacheControlBreakpoints([]byte(body))
	if injected {
		t.Error("expected injected=false: user has cache_control in tools, platform should not inject anywhere")
	}
	var data map[string]any
	json.Unmarshal(result, &data)

	// system 不应被注入
	system := data["system"].([]any)
	if _, has := system[0].(map[string]any)["cache_control"]; has {
		t.Error("system should not have cache_control when user has cache_control in tools")
	}

	// messages 不应被注入
	messages := data["messages"].([]any)
	msg0 := messages[0].(map[string]any)
	content0 := msg0["content"].([]any)
	if _, has := content0[0].(map[string]any)["cache_control"]; has {
		t.Error("messages should not have cache_control when user has cache_control in tools")
	}
}

func TestInjectCacheControlBreakpoints_MixedRegions_UserCacheInSystem(t *testing.T) {
	// 方案 A：用户在 system 有 cache_control → 全局不注入
	body := `{
		"tools": [{"name": "tool1"}],
		"system": [
			{"type": "text", "text": "sys", "cache_control": {"type": "ephemeral"}}
		],
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "q1"}]},
			{"role": "assistant", "content": [{"type": "text", "text": "a1"}]},
			{"role": "user", "content": [{"type": "text", "text": "q2"}]}
		]
	}`

	result, injected := injectCacheControlBreakpoints([]byte(body))
	if injected {
		t.Error("expected injected=false: user has cache_control in system")
	}
	var data map[string]any
	json.Unmarshal(result, &data)

	// tools 不应被注入
	tools := data["tools"].([]any)
	if _, has := tools[0].(map[string]any)["cache_control"]; has {
		t.Error("tools should not have cache_control when user has cache_control in system")
	}
}

func TestInjectCacheControlBreakpoints_MixedRegions_UserCacheInMessages(t *testing.T) {
	// 方案 A：用户在 messages 有 cache_control → 全局不注入
	body := `{
		"tools": [{"name": "tool1"}],
		"system": [{"type": "text", "text": "sys"}],
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "q1", "cache_control": {"type": "ephemeral"}}]},
			{"role": "assistant", "content": [{"type": "text", "text": "a1"}]},
			{"role": "user", "content": [{"type": "text", "text": "q2"}]}
		]
	}`

	result, injected := injectCacheControlBreakpoints([]byte(body))
	if injected {
		t.Error("expected injected=false: user has cache_control in messages")
	}
	var data map[string]any
	json.Unmarshal(result, &data)

	// tools 不应被注入
	tools := data["tools"].([]any)
	if _, has := tools[0].(map[string]any)["cache_control"]; has {
		t.Error("tools should not have cache_control when user has cache_control in messages")
	}

	// system 不应被注入
	system := data["system"].([]any)
	if _, has := system[0].(map[string]any)["cache_control"]; has {
		t.Error("system should not have cache_control when user has cache_control in messages")
	}
}

// countAllCacheControl counts all cache_control across tools, system, messages
// Skips thinking blocks to match production counting behavior
func countAllCacheControl(data map[string]any) int {
	count := 0
	if tools, ok := data["tools"].([]any); ok {
		for _, item := range tools {
			if m, ok := item.(map[string]any); ok {
				if _, has := m["cache_control"]; has {
					count++
				}
			}
		}
	}
	if system, ok := data["system"].([]any); ok {
		for _, item := range system {
			if m, ok := item.(map[string]any); ok {
				if blockType, _ := m["type"].(string); blockType == "thinking" {
					continue
				}
				if _, has := m["cache_control"]; has {
					count++
				}
			}
		}
	}
	if messages, ok := data["messages"].([]any); ok {
		for _, msg := range messages {
			if msgMap, ok := msg.(map[string]any); ok {
				if content, ok := msgMap["content"].([]any); ok {
					for _, item := range content {
						if m, ok := item.(map[string]any); ok {
							if blockType, _ := m["type"].(string); blockType == "thinking" {
								continue
							}
							if _, has := m["cache_control"]; has {
								count++
							}
						}
					}
				}
			}
		}
	}
	return count
}

// --- enforceCacheControlLimit tests ---

func TestEnforceCacheControlLimit_UnderLimit(t *testing.T) {
	// 3 breakpoints → no removal
	body := `{
		"tools": [{"name": "t1", "cache_control": {"type": "ephemeral"}}],
		"system": [{"type": "text", "text": "sys", "cache_control": {"type": "ephemeral"}}],
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "q1", "cache_control": {"type": "ephemeral"}}]}
		]
	}`
	result := enforceCacheControlLimit([]byte(body))
	var data map[string]any
	json.Unmarshal(result, &data)
	if countAllCacheControl(data) != 3 {
		t.Errorf("expected 3 cache_control blocks, got %d", countAllCacheControl(data))
	}
}

func TestEnforceCacheControlLimit_OverLimit_RemovesFromMessages(t *testing.T) {
	// 5 breakpoints → should remove from messages first
	body := `{
		"system": [{"type": "text", "text": "s1", "cache_control": {"type": "ephemeral"}}],
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "q1", "cache_control": {"type": "ephemeral"}},
				{"type": "text", "text": "q2", "cache_control": {"type": "ephemeral"}}
			]},
			{"role": "assistant", "content": [{"type": "text", "text": "a1"}]},
			{"role": "user", "content": [
				{"type": "text", "text": "q3", "cache_control": {"type": "ephemeral"}},
				{"type": "text", "text": "q4", "cache_control": {"type": "ephemeral"}}
			]}
		]
	}`
	result := enforceCacheControlLimit([]byte(body))
	var data map[string]any
	json.Unmarshal(result, &data)
	total := countAllCacheControl(data)
	if total > 4 {
		t.Errorf("expected ≤4 cache_control blocks, got %d", total)
	}
}

func TestEnforceCacheControlLimit_ToolsOverLimit(t *testing.T) {
	// 5 breakpoints all in tools → should remove from tools as last resort
	body := `{
		"tools": [
			{"name": "t1", "cache_control": {"type": "ephemeral"}},
			{"name": "t2", "cache_control": {"type": "ephemeral"}},
			{"name": "t3", "cache_control": {"type": "ephemeral"}},
			{"name": "t4", "cache_control": {"type": "ephemeral"}},
			{"name": "t5", "cache_control": {"type": "ephemeral"}}
		],
		"messages": [{"role": "user", "content": "hi"}]
	}`
	result := enforceCacheControlLimit([]byte(body))
	var data map[string]any
	json.Unmarshal(result, &data)
	total := countAllCacheControl(data)
	if total > 4 {
		t.Errorf("expected ≤4 cache_control blocks after tools enforcement, got %d", total)
	}
}

func TestEnforceCacheControlLimit_ThinkingBlockCleanup(t *testing.T) {
	// thinking block with cache_control (illegal) + under limit → should clean thinking but keep count
	body := `{
		"system": [
			{"type": "text", "text": "sys", "cache_control": {"type": "ephemeral"}},
			{"type": "thinking", "thinking": "...", "cache_control": {"type": "ephemeral"}}
		],
		"messages": [{"role": "user", "content": "hi"}]
	}`
	result := enforceCacheControlLimit([]byte(body))
	var data map[string]any
	json.Unmarshal(result, &data)

	// thinking block should have cache_control removed
	system := data["system"].([]any)
	thinkingBlock := system[1].(map[string]any)
	if _, has := thinkingBlock["cache_control"]; has {
		t.Error("thinking block should have cache_control removed")
	}

	// text block should keep cache_control
	textBlock := system[0].(map[string]any)
	if _, has := textBlock["cache_control"]; !has {
		t.Error("text block should keep cache_control")
	}
}
