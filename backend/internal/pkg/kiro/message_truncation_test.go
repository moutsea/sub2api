package kiro

import (
	"testing"
)

func TestExtractToolUseIDs(t *testing.T) {
	tests := []struct {
		name     string
		msg      ClaudeMessage
		expected []string
	}{
		{
			name: "message with tool_use",
			msg: ClaudeMessage{
				Role: "assistant",
				Content: []any{
					map[string]any{
						"type": "text",
						"text": "Let me help you.",
					},
					map[string]any{
						"type":  "tool_use",
						"id":    "tool_123",
						"name":  "read_file",
						"input": map[string]any{"path": "/test.txt"},
					},
				},
			},
			expected: []string{"tool_123"},
		},
		{
			name: "message with multiple tool_use",
			msg: ClaudeMessage{
				Role: "assistant",
				Content: []any{
					map[string]any{
						"type":  "tool_use",
						"id":    "tool_1",
						"name":  "read_file",
						"input": map[string]any{},
					},
					map[string]any{
						"type":  "tool_use",
						"id":    "tool_2",
						"name":  "write_file",
						"input": map[string]any{},
					},
				},
			},
			expected: []string{"tool_1", "tool_2"},
		},
		{
			name: "message without tool_use",
			msg: ClaudeMessage{
				Role:    "assistant",
				Content: "Just text content",
			},
			expected: []string{},
		},
		{
			name: "message with text blocks only",
			msg: ClaudeMessage{
				Role: "assistant",
				Content: []any{
					map[string]any{
						"type": "text",
						"text": "Hello",
					},
				},
			},
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractToolUseIDs(tt.msg)
			if len(result) != len(tt.expected) {
				t.Errorf("expected %d tool IDs, got %d", len(tt.expected), len(result))
				return
			}
			for i, id := range result {
				if id != tt.expected[i] {
					t.Errorf("expected ID %q at index %d, got %q", tt.expected[i], i, id)
				}
			}
		})
	}
}

func TestExtractToolResultIDs(t *testing.T) {
	tests := []struct {
		name     string
		msg      ClaudeMessage
		expected []string
	}{
		{
			name: "message with tool_result",
			msg: ClaudeMessage{
				Role: "user",
				Content: []any{
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": "tool_123",
						"content":     "File contents here",
					},
				},
			},
			expected: []string{"tool_123"},
		},
		{
			name: "message with multiple tool_result",
			msg: ClaudeMessage{
				Role: "user",
				Content: []any{
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": "tool_1",
						"content":     "Result 1",
					},
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": "tool_2",
						"content":     "Result 2",
					},
				},
			},
			expected: []string{"tool_1", "tool_2"},
		},
		{
			name: "message without tool_result",
			msg: ClaudeMessage{
				Role:    "user",
				Content: "Just a question",
			},
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractToolResultIDs(tt.msg)
			if len(result) != len(tt.expected) {
				t.Errorf("expected %d tool IDs, got %d", len(tt.expected), len(result))
				return
			}
			for i, id := range result {
				if id != tt.expected[i] {
					t.Errorf("expected ID %q at index %d, got %q", tt.expected[i], i, id)
				}
			}
		})
	}
}

func TestFindSafeTruncationPoints(t *testing.T) {
	tests := []struct {
		name     string
		messages []ClaudeMessage
		minKeep  int
		expected []int
	}{
		{
			name: "simple conversation without tools",
			messages: []ClaudeMessage{
				{Role: "user", Content: "Hello"},
				{Role: "assistant", Content: "Hi there!"},
				{Role: "user", Content: "How are you?"},
				{Role: "assistant", Content: "I'm good!"},
				{Role: "user", Content: "Great"},
				{Role: "assistant", Content: "Thanks!"},
			},
			minKeep:  2,
			expected: []int{2, 4}, // Can truncate after message 1 (index 2) or after message 3 (index 4)
		},
		{
			name: "conversation with tool use",
			messages: []ClaudeMessage{
				{Role: "user", Content: "Read file"},
				{Role: "assistant", Content: []any{
					map[string]any{"type": "tool_use", "id": "t1", "name": "read"},
				}},
				{Role: "user", Content: []any{
					map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": "data"},
				}},
				{Role: "assistant", Content: "Here's the file content"},
				{Role: "user", Content: "Thanks"},
				{Role: "assistant", Content: "You're welcome"},
			},
			minKeep:  2,
			expected: []int{4}, // Can only truncate after the tool exchange is complete
		},
		{
			name:     "too few messages",
			messages: []ClaudeMessage{
				{Role: "user", Content: "Hello"},
				{Role: "assistant", Content: "Hi"},
			},
			minKeep:  2,
			expected: nil,
		},
		{
			name: "incomplete tool use at end",
			messages: []ClaudeMessage{
				{Role: "user", Content: "Hello"},
				{Role: "assistant", Content: "Hi"},
				{Role: "user", Content: "Read file"},
				{Role: "assistant", Content: []any{
					map[string]any{"type": "tool_use", "id": "t1", "name": "read"},
				}},
			},
			minKeep:  2,
			expected: []int{2}, // Can truncate before the incomplete tool use
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := findSafeTruncationPoints(tt.messages, tt.minKeep)
			if len(result) != len(tt.expected) {
				t.Errorf("expected %d truncation points, got %d: %v", len(tt.expected), len(result), result)
				return
			}
			for i, point := range result {
				if point != tt.expected[i] {
					t.Errorf("expected point %d at index %d, got %d", tt.expected[i], i, point)
				}
			}
		})
	}
}

func TestTruncateMessagesIfNeeded_NoTruncationNeeded(t *testing.T) {
	// Create a small request that doesn't need truncation
	req := &ClaudeRequest{
		Model: "claude-3-sonnet",
		Messages: []ClaudeMessage{
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi there!"},
		},
	}

	config := DefaultTruncationConfig()
	config.EnableLogging = false

	result, truncated := TruncateMessagesIfNeeded(req, config)

	if truncated {
		t.Error("expected no truncation for small request")
	}
	if len(result) != len(req.Messages) {
		t.Errorf("expected %d messages, got %d", len(req.Messages), len(result))
	}
}

func TestTruncateMessagesIfNeeded_TooFewMessages(t *testing.T) {
	// Create a request with few messages but large content
	largeContent := make([]byte, 200000) // ~50k tokens
	for i := range largeContent {
		largeContent[i] = 'a'
	}

	req := &ClaudeRequest{
		Model: "claude-3-sonnet",
		Messages: []ClaudeMessage{
			{Role: "user", Content: string(largeContent)},
			{Role: "assistant", Content: "Response"},
		},
	}

	config := DefaultTruncationConfig()
	config.EnableLogging = false
	config.MinMessagesToKeep = 4 // More than we have

	result, truncated := TruncateMessagesIfNeeded(req, config)

	if truncated {
		t.Error("expected no truncation when messages < MinMessagesToKeep")
	}
	if len(result) != len(req.Messages) {
		t.Errorf("expected %d messages, got %d", len(req.Messages), len(result))
	}
}

func TestTruncateAndRetry(t *testing.T) {
	// Test with nil request
	result, truncated := TruncateAndRetry(nil)
	if result != nil || truncated {
		t.Error("expected nil result and no truncation for nil request")
	}

	// Test with small request
	smallReq := &ClaudeRequest{
		Model: "claude-3-sonnet",
		Messages: []ClaudeMessage{
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi!"},
		},
	}

	result, truncated = TruncateAndRetry(smallReq)
	if truncated {
		t.Error("expected no truncation for small request")
	}
	if result != smallReq {
		t.Error("expected same request object when no truncation")
	}
}

func TestDefaultTruncationConfig(t *testing.T) {
	config := DefaultTruncationConfig()

	// Verify target is 80% of limit
	expectedTarget := int(float64(KiroContextWindowLimit) * 0.80)
	if config.TargetTokens != expectedTarget {
		t.Errorf("expected TargetTokens %d, got %d", expectedTarget, config.TargetTokens)
	}

	if config.MinMessagesToKeep != 4 {
		t.Errorf("expected MinMessagesToKeep 4, got %d", config.MinMessagesToKeep)
	}

	if !config.EnableLogging {
		t.Error("expected EnableLogging to be true by default")
	}
}
