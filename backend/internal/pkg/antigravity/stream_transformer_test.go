package antigravity

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMCPXMLBridge(t *testing.T) {
	tests := []struct {
		name           string
		text           string
		expectToolCall bool
		expectedTool   string
	}{
		{
			name:           "Simple MCP XML tag",
			text:           `<mcp__filesystem__read_file>{"path":"/test.txt"}</mcp__filesystem__read_file>`,
			expectToolCall: true,
			expectedTool:   "mcp__filesystem__read_file",
		},
		{
			name:           "MCP XML with prefix text",
			text:           `Let me read that file: <mcp__filesystem__read_file>{"path":"/test.txt"}</mcp__filesystem__read_file>`,
			expectToolCall: true,
			expectedTool:   "mcp__filesystem__read_file",
		},
		{
			name:           "MCP XML with suffix text",
			text:           `<mcp__filesystem__read_file>{"path":"/test.txt"}</mcp__filesystem__read_file> Done!`,
			expectToolCall: true,
			expectedTool:   "mcp__filesystem__read_file",
		},
		{
			name:           "MCP search_files tool",
			text:           `<mcp__filesystem__search_files>{"pattern":"*.go","path":"."}</mcp__filesystem__search_files>`,
			expectToolCall: true,
			expectedTool:   "mcp__filesystem__search_files",
		},
		{
			name:           "Regular text without MCP",
			text:           "Hello, this is normal text",
			expectToolCall: false,
		},
		{
			name:           "Incomplete MCP tag",
			text:           `<mcp__filesystem__read_file>{"path":"/test.txt"}`,
			expectToolCall: false, // No closing tag, should buffer
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			processor := NewStreamingProcessor("test-model", 0)

			result := processor.processText(tt.text, "")

			if tt.expectToolCall {
				// Check that result contains tool_use
				resultStr := string(result)
				if !strings.Contains(resultStr, "tool_use") {
					t.Errorf("Expected tool_use in result, got: %s", resultStr)
				}
				if !strings.Contains(resultStr, tt.expectedTool) {
					t.Errorf("Expected tool name %s in result, got: %s", tt.expectedTool, resultStr)
				}
			} else if tt.name == "Incomplete MCP tag" {
				// Should be buffering
				if !processor.inMCPXML {
					t.Errorf("Expected processor to be in MCP XML buffering mode")
				}
				if processor.mcpXMLBuffer == "" {
					t.Errorf("Expected MCP XML buffer to contain text")
				}
			}
		})
	}
}

func TestMCPXMLBridgeMultiChunk(t *testing.T) {
	// Test that MCP XML Bridge can handle tags split across multiple chunks
	processor := NewStreamingProcessor("test-model", 0)

	// First chunk: partial opening tag
	result1 := processor.processText(`<mcp__filesystem__read_file>{"path":`, "")
	if result1 != nil {
		t.Errorf("Expected nil result for partial tag, got: %s", string(result1))
	}
	if !processor.inMCPXML {
		t.Errorf("Expected processor to be in MCP XML buffering mode")
	}

	// Second chunk: complete the tag
	result2 := processor.processText(`"/test.txt"}</mcp__filesystem__read_file>`, "")
	if result2 == nil {
		t.Errorf("Expected non-nil result after completing tag")
	}

	resultStr := string(result2)
	if !strings.Contains(resultStr, "tool_use") {
		t.Errorf("Expected tool_use in result, got: %s", resultStr)
	}
	if !strings.Contains(resultStr, "mcp__filesystem__read_file") {
		t.Errorf("Expected tool name in result, got: %s", resultStr)
	}

	// Processor should be reset
	if processor.inMCPXML {
		t.Errorf("Expected processor to exit MCP XML buffering mode after completing tag")
	}
}

func TestRemapFunctionCallArgs_MCPTools(t *testing.T) {
	tests := []struct {
		name         string
		toolName     string
		args         map[string]any
		expectedArgs map[string]any
	}{
		{
			name:     "MCP grep tool with query param",
			toolName: "mcp__filesystem__grep",
			args: map[string]any{
				"query": "searchPattern",
				"paths": []any{"/test/path"},
			},
			expectedArgs: map[string]any{
				"pattern": "searchPattern",
				"path":    "/test/path",
			},
		},
		{
			name:     "MCP search_files tool",
			toolName: "mcp__filesystem__search_files",
			args: map[string]any{
				"query": "searchPattern",
				"paths": []any{"."},
			},
			expectedArgs: map[string]any{
				"pattern": "searchPattern",
				"path":    ".",
			},
		},
		{
			name:     "MCP glob tool",
			toolName: "mcp__filesystem__glob",
			args: map[string]any{
				"query": "*.go",
			},
			expectedArgs: map[string]any{
				"pattern": "*.go",
			},
		},
		{
			name:     "MCP read tool",
			toolName: "mcp__filesystem__read",
			args: map[string]any{
				"path": "/test.txt",
			},
			expectedArgs: map[string]any{
				"file_path": "/test.txt",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Make a copy of args to avoid modifying test data
			args := make(map[string]any)
			for k, v := range tt.args {
				args[k] = v
			}

			remapFunctionCallArgs(tt.toolName, args)

			// Check expected args are present
			for k, v := range tt.expectedArgs {
				got, exists := args[k]
				if !exists {
					t.Errorf("Expected key %s not found in remapped args", k)
					continue
				}

				// Compare values (simple comparison for strings)
				gotStr, _ := json.Marshal(got)
				expectedStr, _ := json.Marshal(v)
				if string(gotStr) != string(expectedStr) {
					t.Errorf("For key %s: expected %s, got %s", k, string(expectedStr), string(gotStr))
				}
			}
		})
	}
}
