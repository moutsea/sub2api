// Package kiro provides token estimation utilities for Kiro/CodeWhisperer API integration.
package kiro

import (
	"encoding/json"
	"strings"
)

const (
	// MinCacheableTokens is Anthropic's minimum threshold for prompt caching
	MinCacheableTokens = 1024
)

// Token estimation constants (aligned with kiro4api)
const (
	// Base characters per token for English text
	CharsPerToken = 4
	// Base overhead per message (role + JSON structure)
	MessageOverhead = 3
	// System prompt overhead
	SystemOverhead = 2
	// Base request overhead
	BaseRequestOverhead = 4
	// Image token estimate
	ImageTokenEstimate = 1500
	// Tool overhead constants
	BaseToolsOverhead     = 100
	PerToolOverhead       = 120
	SingleToolOverhead    = 320
	LargeToolBaseOverhead = 180
	LargeToolPerOverhead  = 60

	// Context window limits for Kiro
	// Kiro upstream limit is 75% of 200k = 150k tokens
	KiroContextWindowLimit = 150000
	// Safety margin: trigger pre-check at 95% of the limit
	// This is a last-resort check to provide a cleaner error message
	// than the upstream API error. No compression is performed.
	KiroContextSafetyMargin = 0.95
	// Effective limit for pre-check (150k * 0.95 = 142.5k)
	KiroContextPreCheckLimit = int(float64(KiroContextWindowLimit) * KiroContextSafetyMargin)

	// Input token inflation for client-side context compression trigger
	// Anthropic API max_tokens is typically 200k
	// By adding 10k to actual input_tokens, client calculates higher usage:
	//   e.g., actual 130k + 10k = 140k, usage = 140k/200k = 70%
	// Client triggers auto-compression when usage >= 92%
	InputTokenInflation = 10000
)

// EstimateInputTokens estimates the number of input tokens for a Claude request.
// This is a fast estimation based on text length and structure overhead.
// The estimation aims for ±10% accuracy compared to official tokenizers.
func EstimateInputTokens(req *ClaudeRequest) int {
	if req == nil {
		return 0
	}

	totalTokens := 0

	// 1. System prompt
	totalTokens += estimateSystemTokens(req.System)

	// 2. Messages
	for _, msg := range req.Messages {
		totalTokens += estimateMessageTokens(msg)
	}

	// 3. Tools
	totalTokens += estimateToolsTokens(req.Tools)

	// 4. Base request overhead
	totalTokens += BaseRequestOverhead

	return totalTokens
}

// estimateSystemTokens estimates tokens for system prompt
func estimateSystemTokens(system any) int {
	if system == nil {
		return 0
	}

	totalTokens := 0

	switch sys := system.(type) {
	case string:
		if sys != "" {
			totalTokens += estimateTextTokens(sys)
			totalTokens += SystemOverhead
		}
	case []any:
		for _, sysMsg := range sys {
			if sysMsgMap, ok := sysMsg.(map[string]any); ok {
				if text, ok := sysMsgMap["text"].(string); ok && text != "" {
					totalTokens += estimateTextTokens(text)
					totalTokens += SystemOverhead
				}
			}
		}
	}

	return totalTokens
}

// estimateMessageTokens estimates tokens for a single message
func estimateMessageTokens(msg ClaudeMessage) int {
	totalTokens := MessageOverhead // Role overhead

	switch content := msg.Content.(type) {
	case string:
		totalTokens += estimateTextTokens(content)
	case []any:
		for _, block := range content {
			totalTokens += estimateContentBlockTokens(block)
		}
	case []ContentBlock:
		for _, block := range content {
			totalTokens += estimateTypedContentBlockTokens(&block)
		}
	default:
		// Unknown format: try JSON marshal
		if jsonBytes, err := json.Marshal(content); err == nil {
			totalTokens += len(jsonBytes) / CharsPerToken
		}
	}

	return totalTokens
}

// estimateContentBlockTokens estimates tokens for a content block (any type)
func estimateContentBlockTokens(block any) int {
	blockMap, ok := block.(map[string]any)
	if !ok {
		return 10 // Unknown format, conservative estimate
	}

	blockType, _ := blockMap["type"].(string)

	switch blockType {
	case "text":
		if text, ok := blockMap["text"].(string); ok {
			return estimateTextTokens(text)
		}
		return 10

	case "image":
		return ImageTokenEstimate

	case "document":
		return 500

	case "tool_use":
		return estimateToolUseBlockTokens(blockMap)

	case "tool_result":
		return estimateToolResultBlockTokens(blockMap)

	case "thinking":
		if text, ok := blockMap["thinking"].(string); ok {
			return estimateTextTokens(text)
		}
		return 10

	default:
		// Unknown type: JSON length estimate
		if jsonBytes, err := json.Marshal(block); err == nil {
			return len(jsonBytes) / CharsPerToken
		}
		return 10
	}
}

// estimateTypedContentBlockTokens estimates tokens for a typed ContentBlock
func estimateTypedContentBlockTokens(block *ContentBlock) int {
	switch block.Type {
	case "text":
		if block.Text != nil {
			return estimateTextTokens(*block.Text)
		}
		return 10

	case "image":
		return ImageTokenEstimate

	case "tool_use":
		toolName := ""
		if block.Name != nil {
			toolName = *block.Name
		}
		toolInput := make(map[string]any)
		if block.Input != nil {
			if input, ok := block.Input.(map[string]any); ok {
				toolInput = input
			}
		}
		return estimateToolUseTokens(toolName, toolInput)

	case "tool_result":
		switch content := block.Content.(type) {
		case string:
			return estimateTextTokens(content)
		case []any:
			total := 0
			for _, item := range content {
				total += estimateContentBlockTokens(item)
			}
			return total
		default:
			return 50
		}

	case "thinking":
		if block.Thinking != nil {
			return estimateTextTokens(*block.Thinking)
		}
		return 10

	default:
		return 10
	}
}

// estimateToolUseBlockTokens estimates tokens for a tool_use block from map
func estimateToolUseBlockTokens(blockMap map[string]any) int {
	toolName, _ := blockMap["name"].(string)
	toolInput := make(map[string]any)
	if input, ok := blockMap["input"].(map[string]any); ok {
		toolInput = input
	}
	return estimateToolUseTokens(toolName, toolInput)
}

// estimateToolResultBlockTokens estimates tokens for a tool_result block
func estimateToolResultBlockTokens(blockMap map[string]any) int {
	content := blockMap["content"]

	switch c := content.(type) {
	case string:
		return estimateTextTokens(c)
	case []any:
		total := 0
		for _, item := range c {
			total += estimateContentBlockTokens(item)
		}
		return total
	default:
		return 50
	}
}

// estimateToolUseTokens estimates tokens for a tool use block
// Structure: "type": "tool_use", "id": "...", "name": "...", "input": {...}
func estimateToolUseTokens(toolName string, toolInput map[string]any) int {
	totalTokens := 0

	// JSON structure overhead: "type": "tool_use" ≈ 3 tokens
	totalTokens += 3

	// "id": "toolu_01A09q90qw90lq917835lq9" ≈ 8 tokens
	totalTokens += 8

	// "name" key ≈ 1 token
	totalTokens += 1

	// Tool name
	totalTokens += estimateTextTokens(toolName)

	// "input" key ≈ 1 token
	totalTokens += 1

	// Input content
	if len(toolInput) > 0 {
		if jsonBytes, err := json.Marshal(toolInput); err == nil {
			totalTokens += len(jsonBytes) / CharsPerToken
		}
	} else {
		// Empty object {} ≈ 1 token
		totalTokens += 1
	}

	return totalTokens
}

// estimateToolsTokens estimates tokens for tool definitions
func estimateToolsTokens(tools []ClaudeTool) int {
	toolCount := len(tools)
	if toolCount == 0 {
		return 0
	}

	totalTokens := 0

	// Determine overhead strategy based on tool count
	var baseOverhead, perToolOverhead int
	var schemaCharsPerToken float64

	switch {
	case toolCount == 1:
		baseOverhead = 0
		perToolOverhead = SingleToolOverhead
		schemaCharsPerToken = 1.9
	case toolCount <= 5:
		baseOverhead = BaseToolsOverhead
		perToolOverhead = PerToolOverhead
		schemaCharsPerToken = 2.2
	default:
		baseOverhead = LargeToolBaseOverhead
		perToolOverhead = LargeToolPerOverhead
		schemaCharsPerToken = 2.5
	}

	totalTokens += baseOverhead

	for _, tool := range tools {
		// Tool name (special handling for underscore-separated names)
		totalTokens += estimateToolNameTokens(tool.Name)

		// Tool description
		totalTokens += estimateTextTokens(tool.Description)

		// Tool schema
		if tool.InputSchema != nil {
			if jsonBytes, err := json.Marshal(tool.InputSchema); err == nil {
				schemaLen := len(jsonBytes)
				schemaTokens := int(float64(schemaLen)/schemaCharsPerToken + 0.5) // Round up

				// $schema field URL overhead
				if strings.Contains(string(jsonBytes), "$schema") {
					if toolCount == 1 {
						schemaTokens += 10
					} else {
						schemaTokens += 5
					}
				}

				// Minimum schema tokens
				minSchemaTokens := 50
				if toolCount > 5 {
					minSchemaTokens = 30
				}
				if schemaTokens < minSchemaTokens {
					schemaTokens = minSchemaTokens
				}

				totalTokens += schemaTokens
			}
		}

		totalTokens += perToolOverhead
	}

	return totalTokens
}

// estimateTextTokens estimates tokens for plain text
// Uses a simple heuristic: ~4 characters per token for English
func estimateTextTokens(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + CharsPerToken - 1) / CharsPerToken
}

// estimateToolNameTokens estimates tokens for a tool name
// Tool names with underscores may tokenize differently
func estimateToolNameTokens(name string) int {
	if name == "" {
		return 0
	}
	// Underscore-separated names tend to have more tokens
	// Count underscores and add extra tokens
	underscores := strings.Count(name, "_")
	baseTokens := estimateTextTokens(name)
	return baseTokens + underscores
}

// InflateInputTokens adds the inflation amount to input tokens for client-side
// context compression triggering. This makes the client think context usage is
// higher than actual, triggering auto-compression earlier.
func InflateInputTokens(actualTokens int) int {
	return actualTokens + InputTokenInflation
}

// CacheEstimation holds the breakdown of cacheable vs non-cacheable tokens
// for predicting Anthropic prompt cache behavior.
type CacheEstimation struct {
	CacheableTokens     int  // system + tools + history (except last msg)
	NonCacheableTokens  int  // last message only
	TotalInputTokens    int  // sum of above
	MeetsCacheThreshold bool // >= MinCacheableTokens (1024)
}

// EstimateCache separates cacheable from non-cacheable tokens.
// Cacheable: system prompt + tools + all messages except the last one
// Non-cacheable: last message only + base request overhead
// This matches Anthropic's prompt caching behavior.
func EstimateCache(req *ClaudeRequest) CacheEstimation {
	if req == nil {
		return CacheEstimation{}
	}

	result := CacheEstimation{}

	// 1. System prompt - cacheable
	result.CacheableTokens += estimateSystemTokens(req.System)

	// 2. Tools - cacheable
	result.CacheableTokens += estimateToolsTokens(req.Tools)

	// 3. Messages - all except last are cacheable
	msgCount := len(req.Messages)
	for i, msg := range req.Messages {
		tokens := estimateMessageTokens(msg)
		if i < msgCount-1 {
			result.CacheableTokens += tokens
		} else {
			result.NonCacheableTokens += tokens
		}
	}

	// 4. Base request overhead - non-cacheable (aligned with kiro4api)
	result.NonCacheableTokens += BaseRequestOverhead

	result.TotalInputTokens = result.CacheableTokens + result.NonCacheableTokens
	result.MeetsCacheThreshold = result.CacheableTokens >= MinCacheableTokens

	return result
}

// ExtractSystemPromptText extracts system prompt as string for cache key generation.
// Handles both string and array formats of system prompt.
// Uses simple concatenation (no separator) to match kiro4api behavior.
func ExtractSystemPromptText(system any) string {
	if system == nil {
		return ""
	}

	switch sys := system.(type) {
	case string:
		return sys
	case []any:
		var result string
		for _, sysMsg := range sys {
			if sysMsgMap, ok := sysMsg.(map[string]any); ok {
				if text, ok := sysMsgMap["text"].(string); ok {
					result += text
				}
			}
		}
		return result
	}
	return ""
}
