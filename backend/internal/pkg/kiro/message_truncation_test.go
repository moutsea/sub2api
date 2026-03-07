package kiro

import (
	"encoding/json"
	"strings"
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

	// Verify target is 70% of limit
	expectedTarget := int(float64(KiroContextWindowLimit) * 0.70)
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

// padString creates a string of the given length by repeating a pattern
func padString(length int) string {
	unit := "The quick brown fox jumps over the lazy dog. "
	repeats := length/len(unit) + 1
	return strings.Repeat(unit, repeats)[:length]
}

// buildTestClaudeRequest creates a Claude request with N conversation turns,
// each with approximately charsPerMsg characters of content.
func buildTestClaudeRequest(turns int, charsPerMsg int) *ClaudeRequest {
	messages := make([]ClaudeMessage, 0, turns*2)
	for i := 0; i < turns; i++ {
		messages = append(messages, ClaudeMessage{
			Role:    "user",
			Content: padString(charsPerMsg),
		})
		messages = append(messages, ClaudeMessage{
			Role:    "assistant",
			Content: padString(charsPerMsg),
		})
	}
	// Add final user message
	messages = append(messages, ClaudeMessage{
		Role:    "user",
		Content: "What is the answer?",
	})

	return &ClaudeRequest{
		Model:    "claude-sonnet-4-6",
		Messages: messages,
		Tools: []ClaudeTool{
			{
				Name:        "read_file",
				Description: "Read a file from the filesystem",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
					},
					"required": []string{"path"},
				},
			},
		},
	}
}

func TestTruncateToFitBodySize_NoTruncationNeeded(t *testing.T) {
	// Small request that fits within limit
	req := buildTestClaudeRequest(3, 200)

	cwReq, body, err := TruncateToFitBodySize(req, "", nil, 800*1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cwReq == nil || body == nil {
		t.Fatal("expected non-nil result")
	}
	if len(body) > 800*1024 {
		t.Errorf("body size %d exceeds limit %d", len(body), 800*1024)
	}
}

func TestTruncateToFitBodySize_TruncatesLargeRequest(t *testing.T) {
	// Build a request that will exceed 50KB when transformed
	// 20 turns * 5000 chars each = ~100KB of content
	req := buildTestClaudeRequest(20, 5000)

	// Transform without truncation to get original size
	cwOriginal, err := TransformClaudeToCodeWhisperer(req, "", nil)
	if err != nil {
		t.Fatalf("transform failed: %v", err)
	}
	originalBody, _ := json.Marshal(cwOriginal)
	originalSize := len(originalBody)

	// Set a limit smaller than original
	limit := originalSize / 2
	if limit < 1024 {
		limit = 1024
	}

	cwReq, body, err := TruncateToFitBodySize(req, "", nil, limit)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(body) > limit {
		t.Errorf("truncated body size %d exceeds limit %d", len(body), limit)
	}

	// Verify the result is valid JSON
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Errorf("truncated body is not valid JSON: %v", err)
	}

	// Verify history was reduced
	originalHistoryLen := len(cwOriginal.ConversationState.History)
	truncatedHistoryLen := len(cwReq.ConversationState.History)
	if truncatedHistoryLen >= originalHistoryLen {
		t.Errorf("expected fewer history entries after truncation: original=%d truncated=%d",
			originalHistoryLen, truncatedHistoryLen)
	}

	t.Logf("original: %d bytes, %d history | truncated: %d bytes, %d history | limit: %d",
		originalSize, originalHistoryLen, len(body), truncatedHistoryLen, limit)
}

func TestTruncateToFitBodySize_WithToolUsePairs(t *testing.T) {
	// Build request with tool_use/tool_result pairs
	messages := []ClaudeMessage{
		{Role: "user", Content: padString(3000)},
		{Role: "assistant", Content: []any{
			map[string]any{"type": "text", "text": "Let me read that file."},
			map[string]any{"type": "tool_use", "id": "t1", "name": "read_file", "input": map[string]any{"path": "/a.go"}},
		}},
		{Role: "user", Content: []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": padString(3000)},
		}},
		{Role: "assistant", Content: padString(3000)},
		{Role: "user", Content: padString(3000)},
		{Role: "assistant", Content: padString(3000)},
		{Role: "user", Content: padString(3000)},
		{Role: "assistant", Content: padString(3000)},
		{Role: "user", Content: "Final question"},
	}

	req := &ClaudeRequest{
		Model:    "claude-sonnet-4-6",
		Messages: messages,
		Tools: []ClaudeTool{
			{Name: "read_file", Description: "Read file", InputSchema: map[string]any{
				"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}},
			}},
		},
	}

	// Use a limit that forces truncation but still fits the minimum messages
	cwReq, body, err := TruncateToFitBodySize(req, "", nil, 15000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(body) > 15000 {
		t.Errorf("body size %d exceeds limit 15000", len(body))
	}

	// Verify result is valid
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Errorf("result is not valid JSON: %v", err)
	}
	_ = cwReq
}

func TestTruncateToFitBodySize_NilRequest(t *testing.T) {
	_, _, err := TruncateToFitBodySize(nil, "", nil, 800*1024)
	if err == nil {
		t.Error("expected error for nil request")
	}
}

func TestTruncateToFitBodySize_TooFewMessages(t *testing.T) {
	// Only 2 messages — below minKeep, no safe truncation points
	req := &ClaudeRequest{
		Model: "claude-sonnet-4-6",
		Messages: []ClaudeMessage{
			{Role: "user", Content: padString(100000)},
			{Role: "assistant", Content: padString(100000)},
		},
	}

	_, _, err := TruncateToFitBodySize(req, "", nil, 1024)
	if err == nil {
		t.Error("expected error when too few messages to truncate")
	}
}

func TestTruncateToFitBodySize_ImpossibleLimit(t *testing.T) {
	// Even after max truncation, body can't fit in 100 bytes
	req := buildTestClaudeRequest(10, 5000)

	_, _, err := TruncateToFitBodySize(req, "", nil, 100)
	if err == nil {
		t.Error("expected error for impossibly small limit")
	}
}

// ==================== Suffix Sum & Helper Tests ====================

func TestComputeMessageTokens(t *testing.T) {
	messages := []ClaudeMessage{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there!"},
		{Role: "user", Content: "How are you?"},
	}

	tokens := computeMessageTokens(messages)
	if len(tokens) != len(messages) {
		t.Fatalf("expected %d token counts, got %d", len(messages), len(tokens))
	}

	// Each should be > 0 (at least MessageOverhead)
	for i, tok := range tokens {
		if tok <= 0 {
			t.Errorf("message %d: expected positive token count, got %d", i, tok)
		}
	}

	// Verify consistency with estimateMessageTokens
	for i, msg := range messages {
		expected := estimateMessageTokens(msg)
		if tokens[i] != expected {
			t.Errorf("message %d: computeMessageTokens=%d != estimateMessageTokens=%d", i, tokens[i], expected)
		}
	}
}

func TestComputeSuffixSum(t *testing.T) {
	msgTokens := []int{100, 200, 300, 400, 50}
	ss := computeSuffixSum(msgTokens)

	// suffixSum should have len+1 entries
	if len(ss) != len(msgTokens)+1 {
		t.Fatalf("expected %d entries, got %d", len(msgTokens)+1, len(ss))
	}

	// ss[len] == 0
	if ss[len(msgTokens)] != 0 {
		t.Errorf("expected ss[%d]=0, got %d", len(msgTokens), ss[len(msgTokens)])
	}

	// ss[0] == total
	expectedTotal := 100 + 200 + 300 + 400 + 50
	if ss[0] != expectedTotal {
		t.Errorf("expected ss[0]=%d, got %d", expectedTotal, ss[0])
	}

	// Verify each suffix sum
	for i := 0; i < len(msgTokens); i++ {
		expected := 0
		for j := i; j < len(msgTokens); j++ {
			expected += msgTokens[j]
		}
		if ss[i] != expected {
			t.Errorf("suffixSum[%d]: expected %d, got %d", i, expected, ss[i])
		}
	}
}

func TestEstimateFixedTokens(t *testing.T) {
	req := &ClaudeRequest{
		Model:  "claude-sonnet-4-6",
		System: "You are a helpful assistant.",
		Tools: []ClaudeTool{
			{Name: "read_file", Description: "Read a file", InputSchema: map[string]any{
				"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}},
			}},
		},
		Messages: []ClaudeMessage{
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi!"},
		},
	}

	fixed := estimateFixedTokens(req)
	if fixed <= 0 {
		t.Errorf("expected positive fixed tokens, got %d", fixed)
	}

	// fixed should equal system + tools + overhead, NOT include messages
	total := EstimateInputTokens(req)
	msgTokens := computeMessageTokens(req.Messages)
	msgTotal := 0
	for _, tok := range msgTokens {
		msgTotal += tok
	}

	expectedFixed := total - msgTotal
	if fixed != expectedFixed {
		t.Errorf("fixed=%d != total(%d) - messages(%d) = %d", fixed, total, msgTotal, expectedFixed)
	}
}

func TestSuffixSumConsistencyWithEstimateInputTokens(t *testing.T) {
	// Verify that fixedTokens + suffixSum[0] == EstimateInputTokens for various requests
	requests := []*ClaudeRequest{
		buildTestClaudeRequest(5, 500),
		buildTestClaudeRequest(10, 1000),
		{
			Model:  "claude-sonnet-4-6",
			System: "System prompt here",
			Messages: []ClaudeMessage{
				{Role: "user", Content: "Hello"},
				{Role: "assistant", Content: "Hi!"},
			},
			Tools: []ClaudeTool{
				{Name: "tool1", Description: "desc1", InputSchema: map[string]any{"type": "object"}},
				{Name: "tool2", Description: "desc2", InputSchema: map[string]any{"type": "object"}},
			},
		},
	}

	for i, req := range requests {
		expected := EstimateInputTokens(req)
		msgTokens := computeMessageTokens(req.Messages)
		ss := computeSuffixSum(msgTokens)
		fixed := estimateFixedTokens(req)
		actual := fixed + ss[0]

		if actual != expected {
			t.Errorf("request %d: fixedTokens(%d) + suffixSum[0](%d) = %d != EstimateInputTokens(%d)",
				i, fixed, ss[0], actual, expected)
		}
	}
}

// TestTruncateMessagesIfNeeded_LargeConversation tests truncation with many messages
// to verify the optimized implementation produces correct results.
func TestTruncateMessagesIfNeeded_LargeConversation(t *testing.T) {
	// Build a request large enough to exceed KiroContextPreCheckLimit (190k tokens).
	// BPE tokenizer is ~4.5 chars/token for English, so we need ~855k chars total.
	// 120 turns * 2 msgs * 4500 chars = 1,080,000 chars → ~240k tokens.
	req := buildTestClaudeRequest(120, 4500)

	tokens := EstimateInputTokens(req)
	t.Logf("Messages: %d, Estimated tokens: %d, Limit: %d", len(req.Messages), tokens, KiroContextPreCheckLimit)

	if tokens <= KiroContextPreCheckLimit {
		t.Skipf("request only has %d tokens (limit %d), not enough to trigger truncation — adjust test parameters",
			tokens, KiroContextPreCheckLimit)
	}

	config := TruncationConfig{
		TargetTokens:      160000,
		MinMessagesToKeep: 4,
		EnableLogging:     false,
	}

	result, truncated := TruncateMessagesIfNeeded(req, config)
	if !truncated {
		t.Fatal("expected truncation for large conversation")
	}

	// Verify result has fewer messages
	if len(result) >= len(req.Messages) {
		t.Errorf("expected fewer messages: original=%d result=%d", len(req.Messages), len(result))
	}

	// Verify result still has at least MinMessagesToKeep messages
	if len(result) < config.MinMessagesToKeep {
		t.Errorf("result has %d messages, less than MinMessagesToKeep=%d", len(result), config.MinMessagesToKeep)
	}

	// Verify the last message is preserved (it's the final user message)
	lastOriginal := req.Messages[len(req.Messages)-1]
	lastResult := result[len(result)-1]
	if lastOriginal.Content != lastResult.Content {
		t.Error("last message was not preserved after truncation")
	}

	// Verify estimated tokens are within target
	truncatedReq := &ClaudeRequest{
		Model:    req.Model,
		Messages: result,
		System:   req.System,
		Tools:    req.Tools,
	}
	truncatedTokens := EstimateInputTokens(truncatedReq)
	t.Logf("After truncation: messages=%d, tokens=%d", len(result), truncatedTokens)
	if truncatedTokens > config.TargetTokens {
		// It's OK if it's under KiroContextPreCheckLimit (second pass)
		if truncatedTokens > KiroContextPreCheckLimit {
			t.Errorf("truncated tokens %d exceeds KiroContextPreCheckLimit %d", truncatedTokens, KiroContextPreCheckLimit)
		}
	}
}

// TestTruncateMessagesIfNeeded_AllToolChain tests with a conversation that is
// entirely tool_use/tool_result pairs with no safe truncation points.
func TestTruncateMessagesIfNeeded_AllToolChain(t *testing.T) {
	// Build a long tool chain: assistant sends tool_use, user sends tool_result, repeat
	// No clean break points because tool_use is always pending
	messages := make([]ClaudeMessage, 0)
	for i := 0; i < 50; i++ {
		toolID := "tool_" + padString(5)
		messages = append(messages, ClaudeMessage{
			Role: "assistant",
			Content: []any{
				map[string]any{"type": "tool_use", "id": toolID, "name": "read_file",
					"input": map[string]any{"path": padString(2000)}},
			},
		})
		messages = append(messages, ClaudeMessage{
			Role: "user",
			Content: []any{
				map[string]any{"type": "tool_result", "tool_use_id": toolID, "content": padString(2000)},
			},
		})
		// Immediately start next tool use without a clean assistant text response
	}
	// Prepend initial user message
	messages = append([]ClaudeMessage{{Role: "user", Content: "Start"}}, messages...)

	req := &ClaudeRequest{
		Model:    "claude-sonnet-4-6",
		Messages: messages,
	}

	config := TruncationConfig{
		TargetTokens:      10000, // Very low target to force truncation attempt
		MinMessagesToKeep: 4,
		EnableLogging:     false,
	}

	// This should find truncation points where tool chains complete
	result, truncated := TruncateMessagesIfNeeded(req, config)

	// Whether truncation happens depends on whether safe points exist
	// The key thing is it doesn't panic or hang
	if truncated {
		if len(result) < config.MinMessagesToKeep {
			t.Errorf("result has %d messages, less than MinMessagesToKeep=%d", len(result), config.MinMessagesToKeep)
		}
	}
}

// ==================== Benchmarks ====================

// BenchmarkTruncateMessagesIfNeeded_200Messages benchmarks truncation with ~240 messages
func BenchmarkTruncateMessagesIfNeeded_200Messages(b *testing.B) {
	// 120 turns * 2 messages + 1 final = 241 messages, ~4500 chars each → ~240k tokens
	req := buildTestClaudeRequest(120, 4500)
	config := TruncationConfig{
		TargetTokens:      160000,
		MinMessagesToKeep: 4,
		EnableLogging:     false,
	}

	// Warm up tokenizer
	_ = EstimateInputTokens(req)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		TruncateMessagesIfNeeded(req, config)
	}
}

// BenchmarkTruncateMessagesIfNeeded_1000Messages benchmarks with 1000 messages
func BenchmarkTruncateMessagesIfNeeded_1000Messages(b *testing.B) {
	// 500 turns * 2 messages + 1 final = 1001 messages, ~2000 chars each
	req := buildTestClaudeRequest(500, 2000)
	config := TruncationConfig{
		TargetTokens:      160000,
		MinMessagesToKeep: 4,
		EnableLogging:     false,
	}

	// Warm up tokenizer
	_ = EstimateInputTokens(req)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		TruncateMessagesIfNeeded(req, config)
	}
}

// BenchmarkEstimateInputTokens_1000Messages benchmarks token estimation alone
func BenchmarkEstimateInputTokens_1000Messages(b *testing.B) {
	req := buildTestClaudeRequest(500, 2000)

	// Warm up tokenizer
	_ = EstimateInputTokens(req)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EstimateInputTokens(req)
	}
}

// BenchmarkComputeMessageTokens_1000Messages benchmarks per-message token computation
func BenchmarkComputeMessageTokens_1000Messages(b *testing.B) {
	req := buildTestClaudeRequest(500, 2000)

	// Warm up tokenizer
	_ = computeMessageTokens(req.Messages)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		computeMessageTokens(req.Messages)
	}
}

// ==================== CW Overhead & Tool Doc Tests ====================

func TestEstimateToolDocTokens_NoTools(t *testing.T) {
	tokens := estimateToolDocTokens(nil)
	if tokens != 0 {
		t.Errorf("expected 0 for nil tools, got %d", tokens)
	}

	tokens = estimateToolDocTokens([]ClaudeTool{})
	if tokens != 0 {
		t.Errorf("expected 0 for empty tools, got %d", tokens)
	}
}

func TestEstimateToolDocTokens_ShortDescriptions(t *testing.T) {
	// All descriptions <= 500 chars — no tool doc injection
	tools := []ClaudeTool{
		{Name: "tool1", Description: "Short description"},
		{Name: "tool2", Description: padString(499)},
	}

	tokens := estimateToolDocTokens(tools)
	if tokens != 0 {
		t.Errorf("expected 0 for short descriptions, got %d", tokens)
	}
}

func TestEstimateToolDocTokens_LongDescriptions(t *testing.T) {
	// Mix of short and long descriptions
	tools := []ClaudeTool{
		{Name: "short_tool", Description: "Short"},
		{Name: "long_tool_1", Description: padString(1000)},
		{Name: "long_tool_2", Description: padString(2000)},
	}

	tokens := estimateToolDocTokens(tools)
	if tokens <= 0 {
		t.Fatal("expected positive tokens for long descriptions")
	}

	// Should include header (25 tokens) + 2 long tools' name+description
	// Verify it's at least the header
	if tokens < 25 {
		t.Errorf("expected at least 25 tokens (header), got %d", tokens)
	}

	t.Logf("Tool doc tokens for 2 long tools: %d", tokens)
}

func TestEstimateToolDocTokens_MatchesBuildToolDocumentation(t *testing.T) {
	// Verify estimateToolDocTokens roughly matches the actual buildToolDocumentation output
	tools := []ClaudeTool{
		{Name: "read_file", Description: padString(800)},
		{Name: "write_file", Description: padString(1200)},
		{Name: "search", Description: "Short desc"},
		{Name: "execute_command", Description: padString(2000)},
	}

	// Build actual tool documentation string
	actualDoc := buildToolDocumentation(tools)
	actualTokens := CountTokens(actualDoc)

	// Our estimate
	estimatedTokens := estimateToolDocTokens(tools)

	// Allow 20% margin
	margin := float64(actualTokens) * 0.20
	diff := float64(estimatedTokens) - float64(actualTokens)
	if diff < -margin || diff > margin {
		t.Errorf("estimate %d differs from actual %d by more than 20%% (diff=%.0f, margin=%.0f)",
			estimatedTokens, actualTokens, diff, margin)
	}

	t.Logf("Tool doc: actual=%d tokens, estimated=%d tokens, diff=%.1f%%",
		actualTokens, estimatedTokens, (diff/float64(actualTokens))*100)
}

func TestEstimateInputTokens_CWOverhead(t *testing.T) {
	// Request with long tool descriptions + thinking — should include CW overhead
	reqWithOverhead := &ClaudeRequest{
		Model:  "claude-sonnet-4-6",
		System: "You are a helpful assistant.",
		Messages: []ClaudeMessage{
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi!"},
		},
		Tools: []ClaudeTool{
			{Name: "read_file", Description: padString(1000), InputSchema: map[string]any{"type": "object"}},
			{Name: "write_file", Description: padString(1500), InputSchema: map[string]any{"type": "object"}},
		},
		Thinking: map[string]any{"type": "enabled", "budget_tokens": float64(10000)},
	}

	// Same request without long tools and thinking
	reqWithoutOverhead := &ClaudeRequest{
		Model:  "claude-sonnet-4-6",
		System: "You are a helpful assistant.",
		Messages: []ClaudeMessage{
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi!"},
		},
		Tools: []ClaudeTool{
			{Name: "read_file", Description: "Short", InputSchema: map[string]any{"type": "object"}},
		},
	}

	tokensWithOverhead := EstimateInputTokens(reqWithOverhead)
	tokensWithoutOverhead := EstimateInputTokens(reqWithoutOverhead)

	// The overhead version should be significantly larger due to:
	// - tool doc injection (~500+ tokens for 2 long descriptions)
	// - thinking prefix (20 tokens)
	// - system wrapping (10 tokens)
	diff := tokensWithOverhead - tokensWithoutOverhead
	t.Logf("With CW overhead: %d tokens, without: %d tokens, diff: %d", tokensWithOverhead, tokensWithoutOverhead, diff)

	// Tool doc alone should add at least 200 tokens (2 tools * ~1000-1500 chars / 4.5)
	if diff < 200 {
		t.Errorf("CW overhead too small: expected at least 200 token difference, got %d", diff)
	}
}

func TestEstimateFixedTokens_IncludesCWOverhead(t *testing.T) {
	req := &ClaudeRequest{
		Model:  "claude-sonnet-4-6",
		System: "System prompt",
		Messages: []ClaudeMessage{
			{Role: "user", Content: "Hello"},
		},
		Tools: []ClaudeTool{
			{Name: "tool1", Description: padString(1000), InputSchema: map[string]any{"type": "object"}},
		},
		Thinking: map[string]any{"type": "enabled", "budget_tokens": float64(10000)},
	}

	fixed := estimateFixedTokens(req)
	total := EstimateInputTokens(req)
	msgTokens := computeMessageTokens(req.Messages)
	msgTotal := 0
	for _, tok := range msgTokens {
		msgTotal += tok
	}

	// fixed + messages should equal total
	expectedFixed := total - msgTotal
	if fixed != expectedFixed {
		t.Errorf("fixed=%d != total(%d) - messages(%d) = %d", fixed, total, msgTotal, expectedFixed)
	}

	// fixed should include tool doc tokens
	toolDocTokens := estimateToolDocTokens(req.Tools)
	if toolDocTokens <= 0 {
		t.Fatal("expected positive tool doc tokens")
	}

	// Verify fixed is larger than just system+tools+overhead (i.e. CW overhead is included)
	baseFixed := estimateSystemTokens(req.System) + estimateToolsTokens(req.Tools) + BaseRequestOverhead
	if fixed <= baseFixed {
		t.Errorf("fixed(%d) should be larger than base(%d) due to CW overhead", fixed, baseFixed)
	}

	t.Logf("fixed=%d, base=%d, CW overhead=%d (toolDoc=%d, thinking=20, wrapping=10)",
		fixed, baseFixed, fixed-baseFixed, toolDocTokens)
}

func TestCleanOrphanToolUsesInClaudeMessages(t *testing.T) {
	t.Run("no messages", func(t *testing.T) {
		modified := CleanOrphanToolUsesInClaudeMessages(nil)
		if modified {
			t.Error("expected no modification for nil messages")
		}
	})

	t.Run("no tool_use blocks", func(t *testing.T) {
		messages := []ClaudeMessage{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "hi there"},
		}
		modified := CleanOrphanToolUsesInClaudeMessages(messages)
		if modified {
			t.Error("expected no modification when no tool_use blocks exist")
		}
	})

	t.Run("all tool_use paired", func(t *testing.T) {
		messages := []ClaudeMessage{
			{Role: "user", Content: "do something"},
			{Role: "assistant", Content: []any{
				map[string]any{"type": "text", "text": "ok"},
				map[string]any{"type": "tool_use", "id": "t1", "name": "read_file", "input": map[string]any{"path": "/a.go"}},
			}},
			{Role: "user", Content: []any{
				map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": "file content"},
			}},
			{Role: "assistant", Content: "done"},
		}
		modified := CleanOrphanToolUsesInClaudeMessages(messages)
		if modified {
			t.Error("expected no modification when all tool_use blocks are paired")
		}
	})

	t.Run("removes orphan tool_use", func(t *testing.T) {
		messages := []ClaudeMessage{
			{Role: "user", Content: "do something"},
			{Role: "assistant", Content: []any{
				map[string]any{"type": "text", "text": "let me try"},
				map[string]any{"type": "tool_use", "id": "t1", "name": "read_file", "input": map[string]any{"path": "/a.go"}},
			}},
			// Missing tool_result for t1 — orphan!
			{Role: "user", Content: "never mind, do something else"},
			{Role: "assistant", Content: "ok"},
		}
		modified := CleanOrphanToolUsesInClaudeMessages(messages)
		if !modified {
			t.Fatal("expected modification when orphan tool_use exists")
		}

		// Check that tool_use was removed
		content, ok := messages[1].Content.([]any)
		if !ok {
			t.Fatal("expected content to be []any")
		}
		for _, block := range content {
			blockMap, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if blockMap["type"] == "tool_use" {
				t.Error("orphan tool_use should have been removed")
			}
		}
		// Text block should still be there
		if len(content) != 1 {
			t.Errorf("expected 1 remaining block, got %d", len(content))
		}
	})

	t.Run("backfills empty content after removing all blocks", func(t *testing.T) {
		messages := []ClaudeMessage{
			{Role: "user", Content: "do something"},
			{Role: "assistant", Content: []any{
				map[string]any{"type": "tool_use", "id": "t1", "name": "read_file", "input": map[string]any{}},
			}},
			// No tool_result — entire assistant content becomes empty after cleanup
			{Role: "user", Content: "hello again"},
		}
		modified := CleanOrphanToolUsesInClaudeMessages(messages)
		if !modified {
			t.Fatal("expected modification")
		}

		content, ok := messages[1].Content.([]any)
		if !ok {
			t.Fatal("expected content to be []any")
		}
		if len(content) != 1 {
			t.Fatalf("expected 1 placeholder block, got %d", len(content))
		}
		blockMap, ok := content[0].(map[string]any)
		if !ok {
			t.Fatal("expected block to be map")
		}
		if blockMap["type"] != "text" {
			t.Errorf("expected placeholder type=text, got %v", blockMap["type"])
		}
		text, _ := blockMap["text"].(string)
		if text == "" {
			t.Error("placeholder text should not be empty")
		}
	})

	t.Run("multiple orphans across messages", func(t *testing.T) {
		messages := []ClaudeMessage{
			{Role: "user", Content: "step 1"},
			{Role: "assistant", Content: []any{
				map[string]any{"type": "tool_use", "id": "t1", "name": "read_file", "input": map[string]any{}},
				map[string]any{"type": "tool_use", "id": "t2", "name": "write_file", "input": map[string]any{}},
			}},
			{Role: "user", Content: []any{
				map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": "ok"},
				// t2 has NO tool_result
			}},
			{Role: "assistant", Content: []any{
				map[string]any{"type": "tool_use", "id": "t3", "name": "run_cmd", "input": map[string]any{}},
			}},
			// t3 has NO tool_result
			{Role: "user", Content: "forget it"},
		}
		modified := CleanOrphanToolUsesInClaudeMessages(messages)
		if !modified {
			t.Fatal("expected modification")
		}

		// t2 should be removed from messages[1], t1 kept
		content1, _ := messages[1].Content.([]any)
		if len(content1) != 1 {
			t.Fatalf("messages[1] should have 1 block (t1 kept), got %d", len(content1))
		}
		blockMap, _ := content1[0].(map[string]any)
		if blockMap["id"] != "t1" {
			t.Errorf("expected t1 to be kept, got id=%v", blockMap["id"])
		}

		// t3 should be removed from messages[3], replaced with placeholder
		content3, _ := messages[3].Content.([]any)
		if len(content3) != 1 {
			t.Fatalf("messages[3] should have 1 placeholder block, got %d", len(content3))
		}
		placeholderMap, _ := content3[0].(map[string]any)
		if placeholderMap["type"] != "text" {
			t.Errorf("expected placeholder text block, got type=%v", placeholderMap["type"])
		}
	})

	t.Run("preserves non-tool blocks in mixed content", func(t *testing.T) {
		messages := []ClaudeMessage{
			{Role: "user", Content: "go"},
			{Role: "assistant", Content: []any{
				map[string]any{"type": "text", "text": "thinking..."},
				map[string]any{"type": "tool_use", "id": "orphan1", "name": "cmd", "input": map[string]any{}},
				map[string]any{"type": "text", "text": "done thinking"},
			}},
			{Role: "user", Content: "ok"},
		}
		modified := CleanOrphanToolUsesInClaudeMessages(messages)
		if !modified {
			t.Fatal("expected modification")
		}

		content, _ := messages[1].Content.([]any)
		if len(content) != 2 {
			t.Fatalf("expected 2 text blocks remaining, got %d", len(content))
		}
		for _, block := range content {
			bm, _ := block.(map[string]any)
			if bm["type"] != "text" {
				t.Errorf("expected only text blocks, got type=%v", bm["type"])
			}
		}
	})

	t.Run("string content assistant message unchanged", func(t *testing.T) {
		messages := []ClaudeMessage{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
			{Role: "user", Content: []any{
				map[string]any{"type": "tool_result", "tool_use_id": "nonexistent", "content": "x"},
			}},
		}
		modified := CleanOrphanToolUsesInClaudeMessages(messages)
		if modified {
			t.Error("should not modify when no tool_use blocks exist")
		}
	})

	t.Run("roundtrip JSON preserves cleanup", func(t *testing.T) {
		req := &ClaudeRequest{
			Model: "claude-sonnet-4-6",
			Messages: []ClaudeMessage{
				{Role: "user", Content: "do something"},
				{Role: "assistant", Content: []any{
					map[string]any{"type": "tool_use", "id": "t1", "name": "cmd", "input": map[string]any{"cmd": "ls"}},
				}},
				{Role: "user", Content: "cancelled"},
			},
			MaxTokens: 1024,
			Stream:    true,
		}

		modified := CleanOrphanToolUsesInClaudeMessages(req.Messages)
		if !modified {
			t.Fatal("expected modification")
		}

		// Re-serialize and verify valid JSON
		body, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("json.Marshal failed: %v", err)
		}

		// Parse back and verify no tool_use blocks
		var parsed ClaudeRequest
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("json.Unmarshal failed: %v", err)
		}

		for _, msg := range parsed.Messages {
			if msg.Role != "assistant" {
				continue
			}
			content, ok := msg.Content.([]any)
			if !ok {
				continue
			}
			for _, block := range content {
				bm, ok := block.(map[string]any)
				if !ok {
					continue
				}
				if bm["type"] == "tool_use" {
					t.Error("tool_use should not exist after cleanup + roundtrip")
				}
			}
		}
	})
}
