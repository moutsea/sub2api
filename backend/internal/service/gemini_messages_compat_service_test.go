package service

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
)

// TestConvertClaudeToolsToGeminiTools_CustomType 测试custom类型工具转换
func TestConvertClaudeToolsToGeminiTools_CustomType(t *testing.T) {
	tests := []struct {
		name        string
		tools       any
		expectedLen int
		description string
	}{
		{
			name: "Standard tools",
			tools: []any{
				map[string]any{
					"name":         "get_weather",
					"description":  "Get weather info",
					"input_schema": map[string]any{"type": "object"},
				},
			},
			expectedLen: 1,
			description: "标准工具格式应该正常转换",
		},
		{
			name: "Custom type tool (MCP format)",
			tools: []any{
				map[string]any{
					"type": "custom",
					"name": "mcp_tool",
					"custom": map[string]any{
						"description":  "MCP tool description",
						"input_schema": map[string]any{"type": "object"},
					},
				},
			},
			expectedLen: 1,
			description: "Custom类型工具应该从custom字段读取",
		},
		{
			name: "Mixed standard and custom tools",
			tools: []any{
				map[string]any{
					"name":         "standard_tool",
					"description":  "Standard",
					"input_schema": map[string]any{"type": "object"},
				},
				map[string]any{
					"type": "custom",
					"name": "custom_tool",
					"custom": map[string]any{
						"description":  "Custom",
						"input_schema": map[string]any{"type": "object"},
					},
				},
			},
			expectedLen: 1,
			description: "混合工具应该都能正确转换",
		},
		{
			name: "Custom tool without custom field",
			tools: []any{
				map[string]any{
					"type": "custom",
					"name": "invalid_custom",
					// 缺少 custom 字段
				},
			},
			expectedLen: 0, // 应该被跳过
			description: "缺少custom字段的custom工具应该被跳过",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertClaudeToolsToGeminiTools(tt.tools)

			if tt.expectedLen == 0 {
				if result != nil {
					t.Errorf("%s: expected nil result, got %v", tt.description, result)
				}
				return
			}

			if result == nil {
				t.Fatalf("%s: expected non-nil result", tt.description)
			}

			if len(result) != 1 {
				t.Errorf("%s: expected 1 tool declaration, got %d", tt.description, len(result))
				return
			}

			toolDecl, ok := result[0].(map[string]any)
			if !ok {
				t.Fatalf("%s: result[0] is not map[string]any", tt.description)
			}

			funcDecls, ok := toolDecl["functionDeclarations"].([]any)
			if !ok {
				t.Fatalf("%s: functionDeclarations is not []any", tt.description)
			}

			toolsArr, _ := tt.tools.([]any)
			expectedFuncCount := 0
			for _, tool := range toolsArr {
				toolMap, _ := tool.(map[string]any)
				if toolMap["name"] != "" {
					// 检查是否为有效的custom工具
					if toolMap["type"] == "custom" {
						if toolMap["custom"] != nil {
							expectedFuncCount++
						}
					} else {
						expectedFuncCount++
					}
				}
			}

			if len(funcDecls) != expectedFuncCount {
				t.Errorf("%s: expected %d function declarations, got %d",
					tt.description, expectedFuncCount, len(funcDecls))
			}
		})
	}
}

func TestConvertClaudeMessagesToGeminiGenerateContent_AddsFunctionCallThoughtSignature(t *testing.T) {
	body := []byte(`{
		"model":"claude-test",
		"messages":[
			{"role":"user","content":"use tool"},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"lookup","input":{"q":"x"}}]}
		]
	}`)

	got, err := convertClaudeMessagesToGeminiGenerateContent(body)
	if err != nil {
		t.Fatalf("convertClaudeMessagesToGeminiGenerateContent error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal converted request: %v", err)
	}
	contents := parsed["contents"].([]any)
	assistant := contents[1].(map[string]any)
	parts := assistant["parts"].([]any)
	part := parts[0].(map[string]any)
	if part["thoughtSignature"] != geminiDummyThoughtSignature {
		t.Fatalf("thoughtSignature = %v, want dummy", part["thoughtSignature"])
	}
}

func TestEnsureGeminiFunctionCallThoughtSignatures_AddsMissingSignature(t *testing.T) {
	body := []byte(`{"contents":[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{}}}]}]}`)

	got := ensureGeminiFunctionCallThoughtSignatures(body)

	if !strings.Contains(string(got), `"thoughtSignature":"`+geminiDummyThoughtSignature+`"`) {
		t.Fatalf("expected dummy thoughtSignature in %s", got)
	}
}

func TestEnsureGeminiFunctionCallThoughtSignatures_PrettyJSON(t *testing.T) {
	body := []byte(`{
		"contents": [{
			"role": "model",
			"parts": [{
				"functionCall" : {"name": "lookup", "args": {}}
			}]
		}]
	}`)

	got := ensureGeminiFunctionCallThoughtSignatures(body)

	if !strings.Contains(string(got), `"thoughtSignature":"`+geminiDummyThoughtSignature+`"`) {
		t.Fatalf("expected dummy thoughtSignature in %s", got)
	}
}

func TestCleanGeminiNativeThoughtSignatures_ReplacesNestedSignatures(t *testing.T) {
	body := []byte(`{
		"contents":[{"parts":[{"text":"x","thoughtSignature":"old_1"},{"functionCall":{"name":"tool"},"thoughtSignature":"old_2"}]}],
		"signature":"keep_me"
	}`)

	got := CleanGeminiNativeThoughtSignatures(body)

	if strings.Contains(string(got), "old_1") || strings.Contains(string(got), "old_2") {
		t.Fatalf("old signatures should be removed: %s", got)
	}
	if !strings.Contains(string(got), `"thoughtSignature":"`+geminiDummyThoughtSignature+`"`) {
		t.Fatalf("dummy signature missing: %s", got)
	}
	if !strings.Contains(string(got), `"signature":"keep_me"`) {
		t.Fatalf("non-thought signature should be preserved: %s", got)
	}
}

func TestCollectGeminiSSE_MergesCumulativeTextChunks(t *testing.T) {
	stream := `data: {"response":{"candidates":[{"content":{"parts":[{"text":"hel"}]}}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1}}}` + "\n\n" +
		`data: {"response":{"candidates":[{"content":{"parts":[{"text":"lo"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":2}}}` + "\n\n" +
		"data: [DONE]\n\n"

	got, usage, err := collectGeminiSSE(strings.NewReader(stream), true)
	if err != nil {
		t.Fatalf("collectGeminiSSE error: %v", err)
	}
	if usage == nil || usage.InputTokens != 2 || usage.OutputTokens != 2 {
		t.Fatalf("usage = %#v, want input=2 output=2", usage)
	}

	parts := got["candidates"].([]any)[0].(map[string]any)["content"].(map[string]any)["parts"].([]any)
	text := parts[0].(map[string]any)["text"]
	if text != "hello" {
		t.Fatalf("merged text = %v, want hello", text)
	}
}

func TestCollectGeminiSSE_MergesCumulativeTextWithoutDuplication(t *testing.T) {
	stream := `data: {"response":{"candidates":[{"content":{"parts":[{"text":"hel"}]}}]}}` + "\n\n" +
		`data: {"response":{"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}]}}` + "\n\n" +
		"data: [DONE]\n\n"

	got, _, err := collectGeminiSSE(strings.NewReader(stream), true)
	if err != nil {
		t.Fatalf("collectGeminiSSE error: %v", err)
	}

	parts := got["candidates"].([]any)[0].(map[string]any)["content"].(map[string]any)["parts"].([]any)
	text := parts[0].(map[string]any)["text"]
	if text != "hello" {
		t.Fatalf("merged cumulative text = %v, want hello", text)
	}
}

func TestConvertClaudeToolsToGeminiTools_WebSearch(t *testing.T) {
	got := convertClaudeToolsToGeminiTools([]any{
		map[string]any{"type": "web_search_20250305", "name": "web_search"},
	})
	if len(got) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(got))
	}
	if _, ok := got[0].(map[string]any)["googleSearch"]; !ok {
		t.Fatalf("googleSearch tool missing: %#v", got)
	}

	body, _ := json.Marshal(map[string]any{"tools": got})
	normalized := normalizeGeminiRequestForAIStudio(body)
	if !strings.Contains(string(normalized), `"google_search"`) {
		t.Fatalf("AI Studio normalized google_search missing: %s", normalized)
	}
}

func TestExtractGeminiUsage_AdjustsCachedAndThinkingTokens(t *testing.T) {
	usage := extractGeminiUsage(map[string]any{
		"usageMetadata": map[string]any{
			"promptTokenCount":        float64(100),
			"cachedContentTokenCount": float64(30),
			"candidatesTokenCount":    float64(20),
			"thoughtsTokenCount":      float64(7),
		},
	})

	if usage == nil {
		t.Fatal("usage should not be nil")
	}
	if usage.InputTokens != 70 {
		t.Fatalf("InputTokens = %d, want 70", usage.InputTokens)
	}
	if usage.OutputTokens != 27 {
		t.Fatalf("OutputTokens = %d, want 27", usage.OutputTokens)
	}
	if usage.CacheReadInputTokens != 30 {
		t.Fatalf("CacheReadInputTokens = %d, want 30", usage.CacheReadInputTokens)
	}
}

func TestSetGeminiAPIKeyBearerForCustomBaseURL(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://proxy.example.test/v1beta/models", nil)
	if err != nil {
		t.Fatalf("NewRequest error: %v", err)
	}
	setGeminiAPIKeyBearerForCustomBaseURL(req, "test-key", "https://proxy.example.test", "https://proxy.example.test")
	if got := req.Header.Get("Authorization"); got != "Bearer test-key" {
		t.Fatalf("Authorization = %q, want Bearer", got)
	}

	req, err = http.NewRequest(http.MethodGet, geminicli.AIStudioBaseURL+"/v1beta/models", nil)
	if err != nil {
		t.Fatalf("NewRequest error: %v", err)
	}
	setGeminiAPIKeyBearerForCustomBaseURL(req, "test-key", geminicli.AIStudioBaseURL, geminicli.AIStudioBaseURL)
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want empty for official API", got)
	}
}
