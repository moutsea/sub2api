package service

import (
	"encoding/json"
	"log"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
)

// PurificationStrategy defines the strategy for purifying context history
type PurificationStrategy int

const (
	// PurificationNone keeps all thinking blocks unchanged
	PurificationNone PurificationStrategy = iota
	// PurificationSoft keeps thinking blocks only in the last 2 turns (4 messages)
	PurificationSoft
	// PurificationAggressive removes ALL thinking blocks from history
	PurificationAggressive
)

// ContextStats holds statistics about the context
type ContextStats struct {
	EstimatedTokens int
	MessageCount    int
	ThinkingBlocks  int
	ToolUseBlocks   int
}

// ContextManager handles context estimation and purification
// Inspired by Antigravity-Manager's context_manager.rs
type ContextManager struct{}

// NewContextManager creates a new ContextManager instance
func NewContextManager() *ContextManager {
	return &ContextManager{}
}

// EstimateTokens estimates the token count for a request body
// Uses approximately 3.5 characters per token (same as Antigravity-Manager)
func (cm *ContextManager) EstimateTokens(body []byte) int {
	return int(float64(len(body)) / 3.5)
}

// EstimateTokensDetailed provides detailed token estimation
func (cm *ContextManager) EstimateTokensDetailed(body []byte) ContextStats {
	stats := ContextStats{
		EstimatedTokens: cm.EstimateTokens(body),
	}

	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return stats
	}

	// Count messages
	if messages, ok := req["messages"].([]any); ok {
		stats.MessageCount = len(messages)

		// Count thinking and tool_use blocks
		for _, msg := range messages {
			if msgMap, ok := msg.(map[string]any); ok {
				if content, ok := msgMap["content"].([]any); ok {
					for _, block := range content {
						if blockMap, ok := block.(map[string]any); ok {
							blockType, _ := blockMap["type"].(string)
							switch blockType {
							case "thinking", "redacted_thinking":
								stats.ThinkingBlocks++
							case "tool_use":
								stats.ToolUseBlocks++
							}
						}
					}
				}
			}
		}
	}

	return stats
}

// DetermineStrategy determines the purification strategy based on token usage
// thresholds are based on common model limits (e.g., 200k tokens)
func (cm *ContextManager) DetermineStrategy(estimatedTokens int, limit int) PurificationStrategy {
	if limit <= 0 {
		limit = 200000 // Default limit for most Claude models
	}

	usageRatio := float64(estimatedTokens) / float64(limit)

	switch {
	case usageRatio >= 0.85:
		// Very high usage: aggressive cleanup
		return PurificationAggressive
	case usageRatio >= 0.70:
		// High usage: soft cleanup (keep recent thinking)
		return PurificationSoft
	default:
		// Normal usage: no cleanup needed
		return PurificationNone
	}
}

// PurifyHistory removes thinking blocks from message history based on strategy
// Returns the purified body and whether any modifications were made
func (cm *ContextManager) PurifyHistory(body []byte, strategy PurificationStrategy) ([]byte, bool) {
	if strategy == PurificationNone {
		return body, false
	}

	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body, false
	}

	messages, ok := req["messages"].([]any)
	if !ok || len(messages) == 0 {
		return body, false
	}

	// Determine protected range (for Soft strategy)
	protectedCount := 0
	if strategy == PurificationSoft {
		protectedCount = 4 // Protect last 4 messages (~2 turns)
	}
	startProtectionIdx := len(messages) - protectedCount
	if startProtectionIdx < 0 {
		startProtectionIdx = 0
	}

	modified := false
	newMessages := make([]any, 0, len(messages))

	for i, msg := range messages {
		msgMap, ok := msg.(map[string]any)
		if !ok {
			newMessages = append(newMessages, msg)
			continue
		}

		role, _ := msgMap["role"].(string)
		isProtected := i >= startProtectionIdx

		// Only process assistant messages outside the protected range
		if role == "assistant" && !isProtected {
			content, ok := msgMap["content"].([]any)
			if !ok {
				newMessages = append(newMessages, msg)
				continue
			}

			newContent := make([]any, 0, len(content))
			filteredThisMsg := false

			for _, block := range content {
				blockMap, ok := block.(map[string]any)
				if !ok {
					newContent = append(newContent, block)
					continue
				}

				blockType, _ := blockMap["type"].(string)

				// Filter out thinking blocks
				if blockType == "thinking" || blockType == "redacted_thinking" {
					filteredThisMsg = true
					modified = true
					continue
				}

				// Handle blocks without type but with "thinking" key
				if blockType == "" {
					if _, hasThinking := blockMap["thinking"]; hasThinking {
						filteredThisMsg = true
						modified = true
						continue
					}
				}

				newContent = append(newContent, block)
			}

			// If message becomes empty after filtering, add placeholder
			if filteredThisMsg && len(newContent) == 0 {
				newContent = append(newContent, map[string]any{
					"type": "text",
					"text": "...",
				})
			}

			if filteredThisMsg {
				msgMap["content"] = newContent
			}
		}

		newMessages = append(newMessages, msgMap)
	}

	if !modified {
		return body, false
	}

	req["messages"] = newMessages

	newBody, err := json.Marshal(req)
	if err != nil {
		log.Printf("[ContextManager] Failed to marshal purified request: %v", err)
		return body, false
	}

	log.Printf("[ContextManager] Purified history with strategy: %v (protected last %d msgs)", strategy, protectedCount)
	return newBody, true
}

// PrepareRequest applies context management before sending a request
// This is the main entry point that combines estimation and purification
func (cm *ContextManager) PrepareRequest(body []byte, tokenLimit int) []byte {
	// 1. Estimate tokens
	stats := cm.EstimateTokensDetailed(body)

	// 2. Determine strategy
	strategy := cm.DetermineStrategy(stats.EstimatedTokens, tokenLimit)

	// 3. Log if we're taking action
	if strategy != PurificationNone {
		log.Printf("[ContextManager] Context analysis: tokens=%d, messages=%d, thinking_blocks=%d, strategy=%v",
			stats.EstimatedTokens, stats.MessageCount, stats.ThinkingBlocks, strategy)
	}

	// 4. Apply comprehensive request preparation (includes all transformations)
	// This handles: message merging, thinking block sorting, cache_control cleanup, and purification
	preparedBody := PrepareRequestForClaude(body, strategy)

	// 5. Log if modifications were made
	if strategy != PurificationNone {
		newTokens := cm.EstimateTokens(preparedBody)
		if newTokens != stats.EstimatedTokens {
			log.Printf("[ContextManager] Tokens reduced: %d -> %d (saved %d)",
				stats.EstimatedTokens, newTokens, stats.EstimatedTokens-newTokens)
		}
	}

	return preparedBody
}

// PrepareRequestForClaude applies comprehensive request preparation
// This includes: thinking block sanitization, message merging, and purification
func PrepareRequestForClaude(body []byte, strategy PurificationStrategy) []byte {
	cm := NewContextManager()

	// 1. Parse the request
	var req antigravity.ClaudeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		log.Printf("[ContextManager] Failed to parse request: %v", err)
		return body
	}

	// 2. Apply sanitization (clean cache_control, flatten thinking blocks)
	// This is always needed regardless of purification strategy
	sanitizeThinkingBlocks(&req)

	// 3. Re-marshal for message merging
	sanitizedBody, err := json.Marshal(req)
	if err != nil {
		log.Printf("[ContextManager] Failed to marshal sanitized request: %v", err)
		return body
	}

	// 4. CRITICAL: Merge consecutive messages with same role
	// This is required for Gemini API compatibility (strict user/assistant alternation)
	mergedBody, wasMerged := cm.MergeConsecutiveMessages(sanitizedBody)
	if wasMerged {
		sanitizedBody = mergedBody
	}

	// 5. Apply purification if needed
	if strategy != PurificationNone {
		// Apply purification
		purifiedBody, modified := cm.PurifyHistory(sanitizedBody, strategy)
		if modified {
			return purifiedBody
		}
		return sanitizedBody
	}

	// 6. Return sanitized + merged body (no purification)
	return sanitizedBody
}

// GetModelTokenLimit returns the token limit for a given model
// This can be extended to support more models
func GetModelTokenLimit(model string) int {
	// Most Claude models support 200k tokens
	// Claude 3.5 Sonnet and Opus support up to 200k
	// Haiku models have similar limits
	return 200000
}

// MergeConsecutiveMessages merges consecutive messages with the same role
// This is CRITICAL for Gemini API compatibility - it strictly requires user/assistant alternation
// Reference: Antigravity-Manager merge_consecutive_messages
func (cm *ContextManager) MergeConsecutiveMessages(body []byte) ([]byte, bool) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body, false
	}

	messages, ok := req["messages"].([]any)
	if !ok || len(messages) <= 1 {
		return body, false
	}

	merged := make([]any, 0, len(messages))
	var current map[string]any
	modified := false

	for i, msg := range messages {
		msgMap, ok := msg.(map[string]any)
		if !ok {
			merged = append(merged, msg)
			continue
		}

		role, _ := msgMap["role"].(string)

		if i == 0 {
			// First message - just set as current
			current = msgMap
			continue
		}

		currentRole, _ := current["role"].(string)

		if currentRole == role {
			// Same role - merge content
			modified = true
			currentContent := current["content"]
			msgContent := msgMap["content"]

			mergedContent := mergeContent(currentContent, msgContent)
			current["content"] = mergedContent
		} else {
			// Different role - save current and start new
			merged = append(merged, current)
			current = msgMap
		}
	}

	// Don't forget the last message
	if current != nil {
		merged = append(merged, current)
	}

	if !modified {
		return body, false
	}

	req["messages"] = merged

	newBody, err := json.Marshal(req)
	if err != nil {
		log.Printf("[ContextManager] Failed to marshal merged request: %v", err)
		return body, false
	}

	log.Printf("[ContextManager] Merged consecutive messages: %d -> %d messages", len(messages), len(merged))
	return newBody, true
}

// mergeContent merges two content values (supports string, array, mixed combinations)
// Handles: Array + Array, String + String, Array + String, String + Array
func mergeContent(content1, content2 any) any {
	// Case 1: Both are strings
	str1, isStr1 := content1.(string)
	str2, isStr2 := content2.(string)
	if isStr1 && isStr2 {
		return str1 + "\n\n" + str2
	}

	// Case 2: Both are arrays
	arr1, isArr1 := content1.([]any)
	arr2, isArr2 := content2.([]any)
	if isArr1 && isArr2 {
		result := make([]any, 0, len(arr1)+len(arr2))
		result = append(result, arr1...)
		result = append(result, arr2...)
		return result
	}

	// Case 3: Array + String - convert string to text block and append
	if isArr1 && isStr2 {
		result := make([]any, 0, len(arr1)+1)
		result = append(result, arr1...)
		result = append(result, map[string]any{
			"type": "text",
			"text": str2,
		})
		return result
	}

	// Case 4: String + Array - convert string to text block and prepend
	if isStr1 && isArr2 {
		result := make([]any, 0, len(arr2)+1)
		result = append(result, map[string]any{
			"type": "text",
			"text": str1,
		})
		result = append(result, arr2...)
		return result
	}

	// Fallback: return first content if types don't match expectations
	return content1
}
