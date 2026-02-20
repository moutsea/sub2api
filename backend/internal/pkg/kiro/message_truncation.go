// Package kiro provides message truncation utilities for Kiro/CodeWhisperer API integration.
// This implements automatic message history truncation when context exceeds the limit.
package kiro

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"

	"github.com/gin-gonic/gin"
)

// TruncationConfig holds configuration for message truncation
type TruncationConfig struct {
	// TargetTokens is the target token count after truncation
	// Should be less than KiroContextPreCheckLimit to leave room for response
	TargetTokens int
	// MinMessagesToKeep is the minimum number of messages to keep (from the end)
	// This ensures we don't truncate too aggressively
	MinMessagesToKeep int
	// EnableLogging enables debug logging for truncation operations
	EnableLogging bool
}

// DefaultTruncationConfig returns the default truncation configuration
func DefaultTruncationConfig() TruncationConfig {
	return TruncationConfig{
		// Target 70% of the limit to leave generous room for estimation errors (±10-15%)
		TargetTokens:      int(float64(KiroContextWindowLimit) * 0.70), // 140k tokens
		MinMessagesToKeep: 4, // Keep at least 4 messages (2 turns)
		EnableLogging:     true,
	}
}

// TruncateMessagesIfNeeded truncates message history if estimated tokens exceed the limit.
// It removes messages from the beginning of the conversation while preserving:
// 1. The most recent messages (at least MinMessagesToKeep)
// 2. Tool use/result pairs (to maintain conversation coherence)
// 3. The system prompt (not part of messages)
//
// Performance: O(M*T) for one-time per-message tokenization + O(P) for truncation point
// search using precomputed suffix sums, where M=messages, T=tokenizer cost, P=truncation points.
// Previous implementation was O(P*M*T) due to re-scanning all messages per truncation attempt.
//
// Returns the truncated messages and whether truncation occurred.
func TruncateMessagesIfNeeded(req *ClaudeRequest, config TruncationConfig) ([]ClaudeMessage, bool) {
	if req == nil {
		return nil, false
	}
	if len(req.Messages) == 0 {
		return req.Messages, false
	}

	messages := req.Messages
	originalCount := len(messages)

	// Precompute per-message tokens once — O(M*T), the only expensive pass
	msgTokens := computeMessageTokens(messages)
	fixedTokens := estimateFixedTokens(req)

	// Build suffix sum for O(1) range queries: suffixSum[i] = tokens for messages[i:]
	suffixSum := computeSuffixSum(msgTokens)

	// Total tokens = fixed (system+tools+overhead) + all message tokens
	currentTokens := fixedTokens + suffixSum[0]
	if currentTokens <= KiroContextPreCheckLimit {
		return req.Messages, false
	}

	if config.EnableLogging {
		log.Printf("[kiro] message truncation triggered: estimated %d tokens, limit %d",
			currentTokens, KiroContextPreCheckLimit)
	}

	// Don't truncate if we have very few messages
	if originalCount <= config.MinMessagesToKeep {
		if config.EnableLogging {
			log.Printf("[kiro] skipping truncation: only %d messages (min: %d)",
				originalCount, config.MinMessagesToKeep)
		}
		return messages, false
	}

	// Find safe truncation points — O(M) scan
	truncationPoints := findSafeTruncationPoints(messages, config.MinMessagesToKeep)

	if len(truncationPoints) == 0 {
		if config.EnableLogging {
			log.Printf("[kiro] no safe truncation points found")
		}
		return messages, false
	}

	// tokenAt returns the total estimated tokens when keeping messages[point:]
	tokenAt := func(point int) int {
		return fixedTokens + suffixSum[point]
	}

	// Binary search: find the earliest (least aggressive) truncation point
	// where tokens <= TargetTokens. truncationPoints is in ascending order,
	// and tokenAt is monotonically decreasing as point increases.
	// We want the smallest index i such that tokenAt(truncationPoints[i]) <= target.
	targetIdx := sort.Search(len(truncationPoints), func(i int) bool {
		return tokenAt(truncationPoints[i]) <= config.TargetTokens
	})

	if targetIdx < len(truncationPoints) {
		point := truncationPoints[targetIdx]
		newTokens := tokenAt(point)
		if config.EnableLogging {
			log.Printf("[kiro] truncated %d messages (from %d to %d), estimated tokens: %d -> %d",
				point, originalCount, originalCount-point, currentTokens, newTokens)
		}
		return messages[point:], true
	}

	// Couldn't reach TargetTokens — try to at least get under KiroContextPreCheckLimit
	// Binary search for the earliest point where tokens <= KiroContextPreCheckLimit
	limitIdx := sort.Search(len(truncationPoints), func(i int) bool {
		return tokenAt(truncationPoints[i]) <= KiroContextPreCheckLimit
	})

	if limitIdx < len(truncationPoints) {
		point := truncationPoints[limitIdx]
		newTokens := tokenAt(point)
		if config.EnableLogging {
			log.Printf("[kiro] aggressive truncation: removed %d messages (from %d to %d), estimated tokens: %d -> %d",
				point, originalCount, originalCount-point, currentTokens, newTokens)
		}
		return messages[point:], true
	}

	// Even the most aggressive truncation doesn't help — return the most truncated version
	lastPoint := truncationPoints[len(truncationPoints)-1]
	if config.EnableLogging {
		newTokens := tokenAt(lastPoint)
		log.Printf("[kiro] max truncation applied but still over limit: removed %d messages (from %d to %d), estimated tokens: %d -> %d",
			lastPoint, originalCount, originalCount-lastPoint, currentTokens, newTokens)
	}

	return messages[lastPoint:], true
}

// findSafeTruncationPoints finds indices where it's safe to truncate messages.
// Safe points are:
// 1. After a complete user-assistant exchange (not in the middle of tool use)
// 2. Not within the last MinMessagesToKeep messages
//
// Returns indices in ascending order (earliest first).
func findSafeTruncationPoints(messages []ClaudeMessage, minKeep int) []int {
	if len(messages) <= minKeep {
		return nil
	}

	var points []int
	maxTruncateIndex := len(messages) - minKeep

	// Track tool use state
	pendingToolUses := make(map[string]bool)

	for i := 0; i < maxTruncateIndex; i++ {
		msg := messages[i]

		// Track tool_use blocks in assistant messages
		if msg.Role == "assistant" {
			toolUseIDs := extractToolUseIDs(msg)
			for _, id := range toolUseIDs {
				pendingToolUses[id] = true
			}
		}

		// Track tool_result blocks in user messages
		if msg.Role == "user" {
			toolResultIDs := extractToolResultIDs(msg)
			for _, id := range toolResultIDs {
				delete(pendingToolUses, id)
			}
		}

		// Safe to truncate after this message if:
		// 1. No pending tool uses (all tool_use have matching tool_result)
		// 2. Current message is from assistant (complete turn)
		// 3. Next message exists and is from user (start of new turn)
		if len(pendingToolUses) == 0 && msg.Role == "assistant" {
			if i+1 < len(messages) && messages[i+1].Role == "user" {
				// Truncation point is the index of the next message (first message to keep)
				points = append(points, i+1)
			}
		}
	}

	return points
}

// extractToolUseIDs extracts tool_use IDs from an assistant message
func extractToolUseIDs(msg ClaudeMessage) []string {
	var ids []string

	content, ok := msg.Content.([]any)
	if !ok {
		return ids
	}

	for _, block := range content {
		blockMap, ok := block.(map[string]any)
		if !ok {
			continue
		}

		blockType, _ := blockMap["type"].(string)
		if blockType == "tool_use" {
			if id, ok := blockMap["id"].(string); ok && id != "" {
				ids = append(ids, id)
			}
		}
	}

	return ids
}

// extractToolResultIDs extracts tool_result IDs from a user message
func extractToolResultIDs(msg ClaudeMessage) []string {
	var ids []string

	content, ok := msg.Content.([]any)
	if !ok {
		return ids
	}

	for _, block := range content {
		blockMap, ok := block.(map[string]any)
		if !ok {
			continue
		}

		blockType, _ := blockMap["type"].(string)
		if blockType == "tool_result" {
			if id, ok := blockMap["tool_use_id"].(string); ok && id != "" {
				ids = append(ids, id)
			}
		}
	}

	return ids
}

// TruncateToFitBodySize truncates Claude message history until the serialized
// CodeWhisperer request body fits within maxBodySize bytes.
// It iteratively removes the earliest complete conversation turns (respecting
// tool_use/tool_result pairs) and re-transforms until the body is small enough.
//
// Performance: Uses precomputed suffix sums to skip truncation points that are
// obviously too large (token estimate > maxBodySize/2 heuristic), avoiding
// expensive Transform+Marshal for hopeless candidates. Then binary searches
// among remaining candidates.
//
// Returns the truncated CW request, the serialized body, or an error.
func TruncateToFitBodySize(req *ClaudeRequest, profileArn string, ginCtx *gin.Context, maxBodySize int) (*CodeWhispererRequest, []byte, error) {
	if req == nil {
		return nil, nil, fmt.Errorf("request is nil")
	}

	messages := req.Messages
	minKeep := 4 // Keep at least 4 messages (2 turns)

	// Find all safe truncation points
	truncationPoints := findSafeTruncationPoints(messages, minKeep)
	if len(truncationPoints) == 0 {
		return nil, nil, fmt.Errorf("no safe truncation points found (messages=%d, minKeep=%d)", len(messages), minKeep)
	}

	// Precompute per-message tokens and suffix sums for cheap pre-filtering.
	// Heuristic: ~4 bytes per token in serialized JSON (conservative).
	// If estimated tokens * 4 > maxBodySize, the truncation point is hopeless.
	msgTokens := computeMessageTokens(messages)
	fixedTokens := estimateFixedTokens(req)
	suffixSum := computeSuffixSum(msgTokens)

	// Rough bytes-per-token ratio for pre-filtering (conservative: real ratio is ~3-5)
	const bytesPerTokenEstimate = 4
	maxTokensHeuristic := maxBodySize / bytesPerTokenEstimate

	// Try each truncation point from least aggressive to most aggressive
	// (keep as much history as possible while fitting within the limit)
	for _, point := range truncationPoints {
		// Pre-filter: skip if token estimate is way over budget
		estimatedTokens := fixedTokens + suffixSum[point]
		if estimatedTokens > maxTokensHeuristic*2 {
			// Even with generous margin, this won't fit — skip expensive transform
			continue
		}

		truncatedMessages := messages[point:]

		truncatedClaudeReq := &ClaudeRequest{
			Model:       req.Model,
			Messages:    truncatedMessages,
			System:      req.System,
			Tools:       req.Tools,
			ToolChoice:  req.ToolChoice,
			MaxTokens:   req.MaxTokens,
			Temperature: req.Temperature,
			Stream:      req.Stream,
			Thinking:    req.Thinking,
		}

		cwReq, err := TransformClaudeToCodeWhisperer(truncatedClaudeReq, profileArn, ginCtx)
		if err != nil {
			log.Printf("[kiro] TruncateToFitBodySize: transform failed at point %d: %v", point, err)
			continue
		}

		body, err := json.Marshal(cwReq)
		if err != nil {
			log.Printf("[kiro] TruncateToFitBodySize: marshal failed at point %d: %v", point, err)
			continue
		}

		if len(body) <= maxBodySize {
			log.Printf("[kiro] TruncateToFitBodySize: success, removed %d/%d messages, body=%d bytes (limit=%d)",
				point, len(messages), len(body), maxBodySize)
			return cwReq, body, nil
		}
	}

	return nil, nil, fmt.Errorf("cannot fit body within %d bytes even after maximum truncation", maxBodySize)
}

// TruncateAndRetry is a helper that truncates messages and returns a modified request.
// This is the main entry point for the truncation feature.
func TruncateAndRetry(req *ClaudeRequest) (*ClaudeRequest, bool) {
	if req == nil {
		return nil, false
	}

	config := DefaultTruncationConfig()
	truncatedMessages, truncated := TruncateMessagesIfNeeded(req, config)

	if !truncated {
		return req, false
	}

	// Create a new request with truncated messages
	newReq := &ClaudeRequest{
		Model:       req.Model,
		Messages:    truncatedMessages,
		System:      req.System,
		Tools:       req.Tools,
		ToolChoice:  req.ToolChoice,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stream:      req.Stream,
		Thinking:    req.Thinking,
	}

	return newReq, true
}
