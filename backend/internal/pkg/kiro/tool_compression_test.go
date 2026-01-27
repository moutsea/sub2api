package kiro

import (
	"strings"
	"testing"
)

func TestSimplifyInputSchema(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		expected map[string]any
	}{
		{
			name:     "nil schema",
			input:    nil,
			expected: nil,
		},
		{
			name: "simple schema with type",
			input: map[string]any{
				"type":        "object",
				"description": "A complex schema",
				"$schema":     "http://json-schema.org/draft-07/schema#",
			},
			expected: map[string]any{
				"type": "object",
			},
		},
		{
			name: "schema with required and enum",
			input: map[string]any{
				"type":        "object",
				"required":    []any{"name", "value"},
				"description": "Should be removed",
				"properties": map[string]any{
					"status": map[string]any{
						"type":        "string",
						"enum":        []any{"active", "inactive"},
						"description": "Status description",
					},
				},
			},
			expected: map[string]any{
				"type":     "object",
				"required": []any{"name", "value"},
				"properties": map[string]any{
					"status": map[string]any{
						"type": "string",
						"enum": []any{"active", "inactive"},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := simplifyInputSchema(tt.input)
			if tt.expected == nil {
				if result != nil {
					t.Errorf("expected nil, got %v", result)
				}
				return
			}
			if result == nil {
				t.Errorf("expected %v, got nil", tt.expected)
				return
			}
			// Check type field
			if result["type"] != tt.expected["type"] {
				t.Errorf("type mismatch: expected %v, got %v", tt.expected["type"], result["type"])
			}
			// Check description is removed
			if _, ok := result["description"]; ok {
				t.Error("description should be removed")
			}
			// Check $schema is removed
			if _, ok := result["$schema"]; ok {
				t.Error("$schema should be removed")
			}
		})
	}
}

func TestCompressToolDescription(t *testing.T) {
	tests := []struct {
		name         string
		description  string
		targetLength int
		expectSuffix string
		maxLength    int
	}{
		{
			name:         "short description unchanged",
			description:  "A short description",
			targetLength: 100,
			expectSuffix: "",
			maxLength:    100,
		},
		{
			name:         "long description truncated",
			description:  "This is a very long description that needs to be truncated to fit within the target length limit and more text here",
			targetLength: 50,
			expectSuffix: "...",
			maxLength:    53, // 50 target length, but minimum is 50, so result is ~50 chars + "..."
		},
		{
			name:         "target below minimum uses minimum length",
			description:  "This is a description that will be truncated to minimum length because target is too small for this text",
			targetLength: 10,
			expectSuffix: "...",
			maxLength:    MinToolDescriptionLength, // Will use minimum length
		},
		{
			name:         "description shorter than minimum unchanged",
			description:  "Short text",
			targetLength: 10,
			expectSuffix: "",
			maxLength:    MinToolDescriptionLength, // Won't truncate if already short
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := compressToolDescription(tt.description, tt.targetLength)
			if len(result) > tt.maxLength && len(tt.description) > tt.maxLength {
				t.Errorf("result length %d exceeds max %d", len(result), tt.maxLength)
			}
			if tt.expectSuffix != "" && len(tt.description) > MinToolDescriptionLength && !strings.HasSuffix(result, tt.expectSuffix) {
				t.Errorf("expected suffix %q, got %q", tt.expectSuffix, result)
			}
		})
	}
}

func TestCalculateToolsSize(t *testing.T) {
	tools := []ToolItem{
		{
			Standard: &CodeWhispererTool{
				ToolSpecification: ToolSpecification{
					Name:        "test_tool",
					Description: "A test tool description",
					InputSchema: InputSchema{
						JSON: map[string]any{
							"type": "object",
						},
					},
				},
			},
		},
	}

	size := calculateToolsSize(tools)
	if size == 0 {
		t.Error("expected non-zero size")
	}

	emptySize := calculateToolsSize(nil)
	if emptySize != 0 {
		t.Errorf("expected 0 for empty tools, got %d", emptySize)
	}
}

func TestCompressToolsIfNeeded(t *testing.T) {
	// Create tools that exceed the threshold
	largeDescription := strings.Repeat("This is a long tool description. ", 200)

	tools := make([]ToolItem, 10)
	for i := 0; i < 10; i++ {
		tools[i] = ToolItem{
			Standard: &CodeWhispererTool{
				ToolSpecification: ToolSpecification{
					Name:        "test_tool",
					Description: largeDescription,
					InputSchema: InputSchema{
						JSON: map[string]any{
							"type":        "object",
							"description": "Should be removed during compression",
							"$schema":     "http://json-schema.org/draft-07/schema#",
							"properties": map[string]any{
								"param1": map[string]any{
									"type":        "string",
									"description": "This description should be removed",
								},
							},
						},
					},
				},
			},
		}
	}

	originalSize := calculateToolsSize(tools)
	t.Logf("Original tools size: %d bytes", originalSize)

	if originalSize <= ToolCompressionTargetSize {
		t.Skip("Test tools don't exceed threshold, skipping compression test")
	}

	compressed := compressToolsIfNeeded(tools, false)
	compressedSize := calculateToolsSize(compressed)
	t.Logf("Compressed tools size: %d bytes", compressedSize)

	if compressedSize >= originalSize {
		t.Errorf("compression did not reduce size: original=%d, compressed=%d", originalSize, compressedSize)
	}

	// Verify compression targets are respected
	if compressedSize > ToolCompressionTargetSize*2 {
		t.Logf("Warning: compressed size %d still exceeds 2x target %d", compressedSize, ToolCompressionTargetSize)
	}
}

func TestCompressToolsIfNeeded_NoCompressionNeeded(t *testing.T) {
	tools := []ToolItem{
		{
			Standard: &CodeWhispererTool{
				ToolSpecification: ToolSpecification{
					Name:        "small_tool",
					Description: "A small description",
					InputSchema: InputSchema{
						JSON: map[string]any{
							"type": "object",
						},
					},
				},
			},
		},
	}

	originalSize := calculateToolsSize(tools)
	compressed := compressToolsIfNeeded(tools, false)
	compressedSize := calculateToolsSize(compressed)

	// For small tools, compression should not change much
	if compressedSize > originalSize {
		t.Errorf("compression increased size: original=%d, compressed=%d", originalSize, compressedSize)
	}
}

func TestCompressToolsIfNeeded_WebSearchTool(t *testing.T) {
	tools := []ToolItem{
		{
			WebSearch: &WebSearchTool{
				Type: "web_search",
			},
		},
		{
			Standard: &CodeWhispererTool{
				ToolSpecification: ToolSpecification{
					Name:        "test_tool",
					Description: "A test tool",
					InputSchema: InputSchema{
						JSON: map[string]any{"type": "object"},
					},
				},
			},
		},
	}

	compressed := compressToolsIfNeeded(tools, false)

	// Verify WebSearch tool is preserved
	if compressed[0].WebSearch == nil {
		t.Error("WebSearch tool was not preserved")
	}
	if compressed[0].WebSearch.Type != "web_search" {
		t.Errorf("WebSearch type changed: expected 'web_search', got %q", compressed[0].WebSearch.Type)
	}
}

func TestCopyMap(t *testing.T) {
	original := map[string]any{
		"key1": "value1",
		"key2": map[string]any{
			"nested": "value",
		},
		"key3": []any{"a", "b", "c"},
	}

	copied := copyMap(original)

	// Modify original
	original["key1"] = "modified"
	original["key2"].(map[string]any)["nested"] = "modified"

	// Verify copy is unchanged
	if copied["key1"] != "value1" {
		t.Error("copy was modified when original changed")
	}
	if copied["key2"].(map[string]any)["nested"] != "value" {
		t.Error("nested copy was modified when original changed")
	}
}
