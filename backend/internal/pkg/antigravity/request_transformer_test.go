package antigravity

import (
	"encoding/json"
	"testing"
)

// TestBuildParts_ThinkingBlockWithoutSignature 测试thinking block无signature时的处理
func TestBuildParts_ThinkingBlockWithoutSignature(t *testing.T) {
	tests := []struct {
		name              string
		content           string
		allowDummyThought bool
		expectedParts     int
		description       string
	}{
		{
			name: "Claude model - downgrade thinking to text when signature missing",
			content: `[
				{"type": "text", "text": "Hello"},
				{"type": "thinking", "thinking": "Let me think...", "signature": ""},
				{"type": "text", "text": "World"}
			]`,
			allowDummyThought: false,
			expectedParts:     3, // thinking 降级为 text（因为上游要求 signature 字段必须存在且有效）
			description:       "Claude模型缺少signature时将thinking降级为普通text（避免上游返回 'signature: Field required' 错误）",
		},
		{
			name: "Claude model - preserve thinking block with signature",
			content: `[
				{"type": "text", "text": "Hello"},
				{"type": "thinking", "thinking": "Let me think...", "signature": "sig_real_123456789012345678901234567890123456789012345678901234567890"},
				{"type": "text", "text": "World"}
			]`,
			allowDummyThought: false,
			expectedParts:     3,
			description:       "Claude模型应透传带 signature 的 thinking block（用于 Vertex 签名链路）",
		},
		{
			name: "Gemini model - use dummy signature",
			content: `[
				{"type": "text", "text": "Hello"},
				{"type": "thinking", "thinking": "Let me think...", "signature": ""},
				{"type": "text", "text": "World"}
			]`,
			allowDummyThought: true,
			expectedParts:     3, // 三个block都保留，thinking使用dummy signature
			description:       "Gemini模型应该为无signature的thinking block使用dummy signature",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toolIDToName := make(map[string]string)
			parts, _, err := buildParts(json.RawMessage(tt.content), toolIDToName, tt.allowDummyThought, 0)

			if err != nil {
				t.Fatalf("buildParts() error = %v", err)
			}

			if len(parts) != tt.expectedParts {
				t.Errorf("%s: got %d parts, want %d parts", tt.description, len(parts), tt.expectedParts)
			}

			switch tt.name {
			case "Claude model - preserve thinking block with signature":
				if len(parts) != 3 {
					t.Fatalf("expected 3 parts, got %d", len(parts))
				}
				if !parts[1].Thought || parts[1].ThoughtSignature != "sig_real_123456789012345678901234567890123456789012345678901234567890" {
					t.Fatalf("expected thought part with valid signature, got thought=%v signature=%q",
						parts[1].Thought, parts[1].ThoughtSignature)
				}
			case "Claude model - downgrade thinking to text when signature missing":
				if len(parts) != 3 {
					t.Fatalf("expected 3 parts, got %d", len(parts))
				}
				// 新行为：无有效签名时使用 dummy signature 兜底，不再降级
				if !parts[1].Thought {
					t.Fatalf("expected thinking block to remain as thought, got thought=%v", parts[1].Thought)
				}
				if parts[1].ThoughtSignature != dummyThoughtSignature {
					t.Fatalf("expected dummy signature fallback, got %q", parts[1].ThoughtSignature)
				}
				if parts[1].Text != "Let me think..." {
					t.Fatalf("expected text %q, got %q", "Let me think...", parts[1].Text)
				}
			case "Gemini model - use dummy signature":
				if len(parts) != 3 {
					t.Fatalf("expected 3 parts, got %d", len(parts))
				}
				if !parts[1].Thought || parts[1].ThoughtSignature != dummyThoughtSignature {
					t.Fatalf("expected dummy thought signature, got thought=%v signature=%q",
						parts[1].Thought, parts[1].ThoughtSignature)
				}
			}
		})
	}
}

func TestBuildParts_ToolUseSignatureHandling(t *testing.T) {
	// 有效签名（长度 >= 50）
	validSigContent := `[
		{"type": "tool_use", "id": "t1", "name": "Bash", "input": {"command": "ls"}, "signature": "sig_tool_abc123456789012345678901234567890123456789012345678901234567890"}
	]`
	// 无效签名（长度 < 50）
	invalidSigContent := `[
		{"type": "tool_use", "id": "t1", "name": "Bash", "input": {"command": "ls"}, "signature": "sig_tool_abc"}
	]`

	t.Run("Gemini uses dummy tool_use signature when signature invalid", func(t *testing.T) {
		toolIDToName := make(map[string]string)
		parts, _, err := buildParts(json.RawMessage(invalidSigContent), toolIDToName, true, 0)
		if err != nil {
			t.Fatalf("buildParts() error = %v", err)
		}
		if len(parts) != 1 || parts[0].FunctionCall == nil {
			t.Fatalf("expected 1 functionCall part, got %+v", parts)
		}
		if parts[0].ThoughtSignature != dummyThoughtSignature {
			t.Fatalf("expected dummy tool signature %q, got %q", dummyThoughtSignature, parts[0].ThoughtSignature)
		}
	})

	t.Run("Claude model - preserve valid signature for tool_use", func(t *testing.T) {
		toolIDToName := make(map[string]string)
		parts, _, err := buildParts(json.RawMessage(validSigContent), toolIDToName, false, 0)
		if err != nil {
			t.Fatalf("buildParts() error = %v", err)
		}
		if len(parts) != 1 || parts[0].FunctionCall == nil {
			t.Fatalf("expected 1 functionCall part, got %+v", parts)
		}
		// Claude 模型应透传有效的 signature（Vertex/Google 需要完整签名链路）
		if parts[0].ThoughtSignature != "sig_tool_abc123456789012345678901234567890123456789012345678901234567890" {
			t.Fatalf("expected preserved tool signature, got %q", parts[0].ThoughtSignature)
		}
	})

	t.Run("Claude model - dummy signature when tool_use lacks valid signature", func(t *testing.T) {
		contentWithoutSig := `[
			{"type": "tool_use", "id": "t1", "name": "Bash", "input": {"command": "ls"}}
		]`
		toolIDToName := make(map[string]string)
		parts, _, err := buildParts(json.RawMessage(contentWithoutSig), toolIDToName, false, 0)
		if err != nil {
			t.Fatalf("buildParts() error = %v", err)
		}
		if len(parts) != 1 || parts[0].FunctionCall == nil {
			t.Fatalf("expected 1 functionCall part, got %+v", parts)
		}
		// 新行为：缺少有效 signature 时使用 dummy signature 兜底
		if parts[0].ThoughtSignature != dummyThoughtSignature {
			t.Fatalf("expected dummy signature fallback, got %q", parts[0].ThoughtSignature)
		}
	})
}

// TestBuildTools_CustomTypeTools 测试custom类型工具转换
func TestBuildTools_CustomTypeTools(t *testing.T) {
	tests := []struct {
		name        string
		tools       []ClaudeTool
		expectedLen int
		description string
	}{
		{
			name: "Standard tool format",
			tools: []ClaudeTool{
				{
					Name:        "get_weather",
					Description: "Get weather information",
					InputSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"location": map[string]any{"type": "string"},
						},
					},
				},
			},
			expectedLen: 1,
			description: "标准工具格式应该正常转换",
		},
		{
			name: "Custom type tool (MCP format)",
			tools: []ClaudeTool{
				{
					Type: "custom",
					Name: "mcp_tool",
					Custom: &ClaudeCustomToolSpec{
						Description: "MCP tool description",
						InputSchema: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"param": map[string]any{"type": "string"},
							},
						},
					},
				},
			},
			expectedLen: 1,
			description: "Custom类型工具应该从Custom字段读取description和input_schema",
		},
		{
			name: "Mixed standard and custom tools",
			tools: []ClaudeTool{
				{
					Name:        "standard_tool",
					Description: "Standard tool",
					InputSchema: map[string]any{"type": "object"},
				},
				{
					Type: "custom",
					Name: "custom_tool",
					Custom: &ClaudeCustomToolSpec{
						Description: "Custom tool",
						InputSchema: map[string]any{"type": "object"},
					},
				},
			},
			expectedLen: 1, // 返回一个GeminiToolDeclaration，包含2个function declarations
			description: "混合标准和custom工具应该都能正确转换",
		},
		{
			name: "Invalid custom tool - nil Custom field",
			tools: []ClaudeTool{
				{
					Type: "custom",
					Name: "invalid_custom",
					// Custom 为 nil
				},
			},
			expectedLen: 0, // 应该被跳过
			description: "Custom字段为nil的custom工具应该被跳过",
		},
		{
			name: "Invalid custom tool - nil InputSchema",
			tools: []ClaudeTool{
				{
					Type: "custom",
					Name: "invalid_custom",
					Custom: &ClaudeCustomToolSpec{
						Description: "Invalid",
						// InputSchema 为 nil
					},
				},
			},
			expectedLen: 0, // 应该被跳过
			description: "InputSchema为nil的custom工具应该被跳过",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildTools(tt.tools)

			if len(result) != tt.expectedLen {
				t.Errorf("%s: got %d tool declarations, want %d", tt.description, len(result), tt.expectedLen)
			}

			// 验证function declarations存在
			if len(result) > 0 && result[0].FunctionDeclarations != nil {
				if len(result[0].FunctionDeclarations) != len(tt.tools) {
					t.Errorf("%s: got %d function declarations, want %d",
						tt.description, len(result[0].FunctionDeclarations), len(tt.tools))
				}
			}
		})
	}
}
