package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNormalizeCodexModel_RemapsLegacyGPT5FamiliesToGPT54Mini(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "gpt5", input: "gpt-5", want: "gpt-5.4-mini"},
		{name: "gpt5 mini", input: "gpt-5-mini", want: "gpt-5.4-mini"},
		{name: "gpt51", input: "gpt-5.1", want: "gpt-5.4-mini"},
		{name: "gpt51 high", input: "gpt-5.1-high", want: "gpt-5.4-mini"},
		{name: "gpt51 chat latest", input: "gpt-5.1-chat-latest", want: "gpt-5.4-mini"},
		{name: "gpt5 codex", input: "gpt-5-codex", want: "gpt-5.4-mini"},
		{name: "gpt5 codex fuzzy", input: "gpt 5 codex", want: "gpt-5.4-mini"},
		{name: "gpt51 codex", input: "gpt-5.1-codex", want: "gpt-5.4-mini"},
		{name: "gpt51 codex high", input: "gpt-5.1-codex-high", want: "gpt-5.4-mini"},
		{name: "gpt51 codex max", input: "gpt-5.1-codex-max", want: "gpt-5.4-mini"},
		{name: "deprecated gpt52", input: "gpt-5.2", want: "gpt-5.4-mini"},
		{name: "deprecated gpt53 codex", input: "gpt-5.3-codex", want: "gpt-5.4-mini"},
		{name: "gpt5 codex mini", input: "gpt-5-codex-mini", want: "gpt-5.4-mini"},
		{name: "codex mini latest", input: "codex-mini-latest", want: "gpt-5.4-mini"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, normalizeCodexModel(tt.input))
		})
	}
}

func TestNormalizeCodexModel_AcceptsGPT55Aliases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "gpt55", input: "gpt-5.5", want: "gpt-5.5"},
		{name: "gpt55 high", input: "gpt-5.5-high", want: "gpt-5.5"},
		{name: "gpt55 codex alias", input: "gpt-5.5-codex", want: "gpt-5.5"},
		{name: "provider gpt55 codex xhigh", input: "provider/gpt-5.5-codex-xhigh", want: "gpt-5.5"},
		{name: "gpt55 fuzzy", input: "gpt 5.5", want: "gpt-5.5"},
		{name: "codex auto review", input: "codex-auto-review", want: "codex-auto-review"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, normalizeCodexModel(tt.input))
		})
	}
}

func TestNormalizeCodexModel_AcceptsGPT56Aliases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "gpt56 alias", input: "gpt-5.6", want: "gpt-5.6"},
		{name: "gpt56 sol", input: "gpt-5.6-sol", want: "gpt-5.6-sol"},
		{name: "gpt56 sol high", input: "gpt-5.6-sol-high", want: "gpt-5.6-sol"},
		{name: "gpt56 terra", input: "gpt-5.6-terra", want: "gpt-5.6-terra"},
		{name: "gpt56 luna", input: "gpt-5.6-luna", want: "gpt-5.6-luna"},
		{name: "provider gpt56 luna xhigh", input: "provider/gpt-5.6-luna-xhigh", want: "gpt-5.6-luna"},
		{name: "gpt56 fuzzy", input: "gpt 5.6 terra", want: "gpt-5.6-terra"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, normalizeCodexModel(tt.input))
		})
	}
}

func TestNormalizeCodexModel_AcceptsGPT6AstraAliases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "gpt6 astra", input: "gpt-6-astra", want: "gpt-6-astra"},
		{name: "gpt6 astra high", input: "gpt-6-astra-high", want: "gpt-6-astra"},
		{name: "provider gpt6 astra", input: "provider/gpt-6-astra-20260904", want: "gpt-6-astra"},
		{name: "gpt6 astra fuzzy", input: "gpt 6 astra", want: "gpt-6-astra"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, normalizeCodexModel(tt.input))
		})
	}
}

func TestNormalizeCodexModel_AcceptsCurrentOfficialCodexModels(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "gpt54 mini", input: "gpt-5.4-mini", want: "gpt-5.4-mini"},
		{name: "gpt54 mini high", input: "gpt-5.4-mini-high", want: "gpt-5.4-mini"},
		{name: "provider gpt54 mini xhigh", input: "provider/gpt-5.4-mini-xhigh", want: "gpt-5.4-mini"},
		{name: "gpt54 mini fuzzy", input: "gpt 5.4 mini", want: "gpt-5.4-mini"},
		{name: "spark", input: "gpt-5.3-codex-spark", want: "gpt-5.3-codex-spark"},
		{name: "spark high", input: "gpt-5.3-codex-spark-high", want: "gpt-5.3-codex-spark"},
		{name: "provider spark xhigh", input: "provider/gpt-5.3-codex-spark-xhigh", want: "gpt-5.3-codex-spark"},
		{name: "spark fuzzy", input: "gpt 5.3 codex spark", want: "gpt-5.3-codex-spark"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, normalizeCodexModel(tt.input))
		})
	}
}

func TestNormalizeCodexModel_DoesNotConfuseFutureVersionWithGPT51(t *testing.T) {
	tests := []string{
		"gpt-5.10",
		"gpt-5.10-codex",
		"provider/gpt-5.10-codex",
		"gpt 5.10 codex",
	}

	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			require.Empty(t, normalizeCodexModel(input))
		})
	}
}

func TestGetOAuthModelFallback_PrefersGPT54BeforeOlderFallbacks(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{model: "gpt-5.5", want: "gpt-5.4"},
		{model: "gpt-5.5-codex", want: "gpt-5.4"},
		{model: "gpt-5.4-mini", want: ""},
		{model: "gpt-5.4", want: "gpt-5.4-mini"},
		{model: "gpt-5.3-codex-spark", want: "gpt-5.4-mini"},
		{model: "gpt-5.3-codex", want: "gpt-5.4-mini"},
		{model: "gpt-5.2", want: "gpt-5.4-mini"},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			require.Equal(t, tt.want, getOAuthModelFallback(tt.model))
		})
	}
}

func TestApplyCodexOAuthTransform_ToolContinuationPreservesInput(t *testing.T) {
	// 续链场景：保留 item_reference 与 id，但不再强制 store=true。
	setupCodexCache(t)

	reqBody := map[string]any{
		"model": "gpt-5.2",
		"input": []any{
			map[string]any{"type": "item_reference", "id": "ref1", "text": "x"},
			map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "ok", "id": "o1"},
		},
		"tool_choice": "auto",
	}

	applyCodexOAuthTransform(reqBody)

	// 未显式设置 store=true，默认为 false。
	store, ok := reqBody["store"].(bool)
	require.True(t, ok)
	require.False(t, store)

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 2)

	// 校验 input[0] 为 map，避免断言失败导致测试中断。
	first, ok := input[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "item_reference", first["type"])
	require.Equal(t, "ref1", first["id"])

	// 校验 input[1] 为 map，确保后续字段断言安全。
	second, ok := input[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "o1", second["id"])
}

func TestApplyCodexOAuthTransform_ExplicitStoreFalsePreserved(t *testing.T) {
	// 续链场景：显式 store=false 不再强制为 true，保持 false。
	setupCodexCache(t)

	reqBody := map[string]any{
		"model": "gpt-5.1",
		"store": false,
		"input": []any{
			map[string]any{"type": "function_call_output", "call_id": "call_1"},
		},
		"tool_choice": "auto",
	}

	applyCodexOAuthTransform(reqBody)

	store, ok := reqBody["store"].(bool)
	require.True(t, ok)
	require.False(t, store)
}

func TestApplyCodexOAuthTransform_StoreFalseAddsReasoningEncryptedContentInclude(t *testing.T) {
	setupCodexCache(t)

	reqBody := map[string]any{
		"model":   "gpt-5.1",
		"include": []any{"file_search_call.results"},
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": "hi"},
		},
	}

	applyCodexOAuthTransform(reqBody)

	include, ok := reqBody["include"].([]any)
	require.True(t, ok)
	require.Contains(t, include, "file_search_call.results")
	require.Contains(t, include, "reasoning.encrypted_content")
}

func TestEnsureReasoningEncryptedContentInclude_PreservesExistingValue(t *testing.T) {
	reqBody := map[string]any{
		"store":   false,
		"include": []any{"reasoning.encrypted_content"},
	}

	modified := ensureReasoningEncryptedContentInclude(reqBody)

	require.False(t, modified)
	require.Equal(t, []any{"reasoning.encrypted_content"}, reqBody["include"])
}

func TestApplyCodexOAuthTransform_GPT55CodexAliasMapsToStableModelAndEffort(t *testing.T) {
	setupCodexCache(t)

	reqBody := map[string]any{
		"model": "gpt-5.5-codex-high",
		"input": []any{
			map[string]any{"type": "text", "text": "hi"},
		},
	}

	applyCodexOAuthTransform(reqBody)

	require.Equal(t, "gpt-5.5", reqBody["model"])

	reasoning, ok := reqBody["reasoning"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "high", reasoning["effort"])
}

func TestApplyCodexOAuthTransform_ExplicitStoreTrueForcedFalse(t *testing.T) {
	// 显式 store=true 也会强制为 false。
	setupCodexCache(t)

	reqBody := map[string]any{
		"model": "gpt-5.1",
		"store": true,
		"input": []any{
			map[string]any{"type": "function_call_output", "call_id": "call_1"},
		},
		"tool_choice": "auto",
	}

	applyCodexOAuthTransform(reqBody)

	store, ok := reqBody["store"].(bool)
	require.True(t, ok)
	require.False(t, store)
}

func TestApplyCodexOAuthTransform_NonContinuationDefaultsStoreFalseAndStripsIDs(t *testing.T) {
	// 非续链场景：未设置 store 时默认 false，并移除 input 中的 id。
	setupCodexCache(t)

	reqBody := map[string]any{
		"model": "gpt-5.1",
		"input": []any{
			map[string]any{"type": "text", "id": "t1", "text": "hi"},
		},
	}

	applyCodexOAuthTransform(reqBody)

	store, ok := reqBody["store"].(bool)
	require.True(t, ok)
	require.False(t, store)

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 1)
	// 校验 input[0] 为 map，避免类型不匹配触发 errcheck。
	item, ok := input[0].(map[string]any)
	require.True(t, ok)
	_, hasID := item["id"]
	require.False(t, hasID)
}

func TestFilterCodexInput_RemovesItemReferenceWhenNotPreserved(t *testing.T) {
	input := []any{
		map[string]any{"type": "item_reference", "id": "ref1"},
		map[string]any{"type": "text", "id": "t1", "text": "hi"},
	}

	filtered := filterCodexInput(input, false)
	require.Len(t, filtered, 1)
	// 校验 filtered[0] 为 map，确保字段检查可靠。
	item, ok := filtered[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "text", item["type"])
	_, hasID := item["id"]
	require.False(t, hasID)
}

func TestFilterCodexInput_PreservesCallIDForSupportedTypes(t *testing.T) {
	supportedTypes := []string{
		"function_call",
		"function_call_output",
		"computer_call",
		"computer_call_output",
		"local_shell_call",
		"shell_call",
		"shell_call_output",
		"apply_patch_call",
		"apply_patch_call_output",
		"custom_tool_call",
		"custom_tool_call_output",
		"tool_search_call",
		"tool_search_output",
		"program",
		"program_output",
	}

	for _, itemType := range supportedTypes {
		t.Run(itemType, func(t *testing.T) {
			filtered := filterCodexInput([]any{map[string]any{
				"type":    itemType,
				"id":      "item_1",
				"call_id": "call_1",
			}}, true)

			require.Len(t, filtered, 1)
			item, ok := filtered[0].(map[string]any)
			require.True(t, ok)
			require.Equal(t, "call_1", item["call_id"])
		})
	}
}

func TestFilterCodexInput_DoesNotAddCallIDToUnsupportedTypes(t *testing.T) {
	unsupportedTypes := []string{
		"file_search_call",
		"web_search_call",
		"code_interpreter_call",
		"image_generation_call",
		"mcp_call",
		"local_shell_call_output",
		"item_reference",
		"message",
		"reasoning",
	}

	for _, itemType := range unsupportedTypes {
		t.Run(itemType, func(t *testing.T) {
			filtered := filterCodexInput([]any{map[string]any{
				"type": itemType,
				"id":   "item_1",
			}}, true)

			require.Len(t, filtered, 1)
			item, ok := filtered[0].(map[string]any)
			require.True(t, ok)
			_, hasCallID := item["call_id"]
			require.False(t, hasCallID)
			require.Equal(t, "item_1", item["id"])
		})
	}
}

func TestFilterCodexInput_RemovesCallIDFromUnsupportedTypesDuringContinuation(t *testing.T) {
	unsupportedTypes := []string{
		"file_search_call",
		"web_search_call",
		"code_interpreter_call",
		"image_generation_call",
		"mcp_call",
		"local_shell_call_output",
		"item_reference",
		"message",
		"reasoning",
	}

	for _, itemType := range unsupportedTypes {
		t.Run(itemType, func(t *testing.T) {
			filtered := filterCodexInput([]any{map[string]any{
				"type":    itemType,
				"id":      "item_1",
				"call_id": "invalid_call_id",
			}}, true)

			require.Len(t, filtered, 1)
			item, ok := filtered[0].(map[string]any)
			require.True(t, ok)
			_, hasCallID := item["call_id"]
			require.False(t, hasCallID)
		})
	}
}

func TestFilterCodexInput_DoesNotAddCallIDToUnsupportedTypesWithoutContinuation(t *testing.T) {
	unsupportedTypes := []string{
		"file_search_call",
		"web_search_call",
		"code_interpreter_call",
		"image_generation_call",
		"mcp_call",
		"local_shell_call_output",
		"message",
		"reasoning",
	}

	for _, itemType := range unsupportedTypes {
		t.Run(itemType, func(t *testing.T) {
			filtered := filterCodexInput([]any{map[string]any{
				"type": itemType,
				"id":   "item_1",
			}}, false)

			require.Len(t, filtered, 1)
			item, ok := filtered[0].(map[string]any)
			require.True(t, ok)
			_, hasCallID := item["call_id"]
			require.False(t, hasCallID)
			_, hasID := item["id"]
			require.False(t, hasID)
		})
	}
}

func TestFilterCodexInput_RemovesUnknownCallID(t *testing.T) {
	filtered := filterCodexInput([]any{map[string]any{
		"type":    "future_tool_call",
		"id":      "item_1",
		"call_id": "call_1",
	}}, true)

	require.Len(t, filtered, 1)
	item, ok := filtered[0].(map[string]any)
	require.True(t, ok)
	_, hasCallID := item["call_id"]
	require.False(t, hasCallID)
}

func TestFilterCodexInput_DoesNotSynthesizeCallIDFromItemID(t *testing.T) {
	for _, itemType := range []string{"function_call_output", "local_shell_call_output"} {
		t.Run(itemType, func(t *testing.T) {
			filtered := filterCodexInput([]any{map[string]any{
				"type": itemType,
				"id":   "fc_1",
			}}, true)

			require.Len(t, filtered, 1)
			item, ok := filtered[0].(map[string]any)
			require.True(t, ok)
			_, hasCallID := item["call_id"]
			require.False(t, hasCallID)
		})
	}
}

func TestApplyCodexOAuthTransform_NormalizeCodexTools_PreservesResponsesFunctionTools(t *testing.T) {
	setupCodexCache(t)

	reqBody := map[string]any{
		"model": "gpt-5.1",
		"tools": []any{
			map[string]any{
				"type":        "function",
				"name":        "bash",
				"description": "desc",
				"parameters":  map[string]any{"type": "object"},
			},
			map[string]any{
				"type":     "function",
				"function": nil,
			},
		},
	}

	applyCodexOAuthTransform(reqBody)

	tools, ok := reqBody["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)

	first, ok := tools[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "function", first["type"])
	require.Equal(t, "bash", first["name"])
}

func TestApplyCodexOAuthTransform_EmptyInput(t *testing.T) {
	// 空 input 应保持为空且不触发异常。
	setupCodexCache(t)

	reqBody := map[string]any{
		"model": "gpt-5.1",
		"input": []any{},
	}

	applyCodexOAuthTransform(reqBody)

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 0)
}

func TestApplyCodexOAuthTransform_RemapsLegacyGPT51HighToGPT54MiniWithEffort(t *testing.T) {
	setupCodexCache(t)

	reqBody := map[string]any{
		"model": "gpt-5.1-high",
		"input": "hi",
	}

	applyCodexOAuthTransform(reqBody)

	require.Equal(t, "gpt-5.4-mini", reqBody["model"])
	reasoning, ok := reqBody["reasoning"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "high", reasoning["effort"])
}

func TestApplyCodexOAuthTransform_RemapsLegacyGPT51CodexMaxToGPT54MiniXHigh(t *testing.T) {
	setupCodexCache(t)

	reqBody := map[string]any{
		"model": "gpt-5.1-codex-max",
		"input": "hi",
	}

	applyCodexOAuthTransform(reqBody)

	require.Equal(t, "gpt-5.4-mini", reqBody["model"])
	reasoning, ok := reqBody["reasoning"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "xhigh", reasoning["effort"])
}

func setupCodexCache(t *testing.T) {
	t.Helper()

	// 使用临时 HOME 避免触发网络拉取 header。
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)

	cacheDir := filepath.Join(tempDir, ".opencode", "cache")
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "opencode-codex-header.txt"), []byte("header"), 0o644))

	meta := map[string]any{
		"etag":        "",
		"lastFetch":   time.Now().UTC().Format(time.RFC3339),
		"lastChecked": time.Now().UnixMilli(),
	}
	data, err := json.Marshal(meta)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(cacheDir, "opencode-codex-header-meta.json"), data, 0o644))
}
