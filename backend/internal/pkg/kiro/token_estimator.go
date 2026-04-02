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
	// Default limit for models without 1M support (e.g., opus-4.5, sonnet-4.5, haiku-4.5)
	KiroContextWindowLimit = 200000
	// 1M context window limit for 4.6 series models (opus-4.6, sonnet-4.6)
	// These models natively support 1M context — no separate model ID or beta header needed.
	KiroContextWindowLimit1M = 1000000
	// Safety margin: trigger pre-check at 82.5% of the limit
	// Token estimation has ±10-15% error, CW format transformation adds overhead
	// (parameter hints, constraint text, history alternation padding), and
	// upstream may have stricter internal limits.
	KiroContextSafetyMargin = 0.825
	// Effective limit for pre-check (200k * 0.825 = 165k)
	KiroContextPreCheckLimit = int(float64(KiroContextWindowLimit) * KiroContextSafetyMargin)
	// Effective limit for pre-check 1M (1M * 0.825 = 825k)
	KiroContextPreCheckLimit1M = int(float64(KiroContextWindowLimit1M) * KiroContextSafetyMargin)

	// Input token inflation for client-side context compression trigger
	// Anthropic API max_tokens is typically 200k
	// By adding to actual input_tokens, client calculates higher usage
	// Client triggers auto-compression when usage >= 92%
	// Set to 0 to disable inflation
	InputTokenInflation = 0
)

// Is1MContext returns true if the model supports 1M context window.
// Currently only claude-opus-4.6 and claude-sonnet-4.6 (the 4.6 series) natively support 1M.
// Accepts both input formats: dashes (claude-opus-4-6) and dots (claude-opus-4.6).
func Is1MContext(model string) bool {
	m := strings.ToLower(model)
	return strings.Contains(m, "opus-4-6") || strings.Contains(m, "opus-4.6") ||
		strings.Contains(m, "sonnet-4-6") || strings.Contains(m, "sonnet-4.6")
}

// GetContextWindowLimit returns the context window limit for the given model.
// 4.6 series (opus-4.6, sonnet-4.6) → 1M; all others → 200K.
func GetContextWindowLimit(model string) int {
	if Is1MContext(model) {
		return KiroContextWindowLimit1M
	}
	return KiroContextWindowLimit
}

// GetContextPreCheckLimit returns the pre-check limit for the given model.
// This is the context window limit * safety margin (82.5%).
func GetContextPreCheckLimit(model string) int {
	if Is1MContext(model) {
		return KiroContextPreCheckLimit1M
	}
	return KiroContextPreCheckLimit
}

// EstimateInputTokens estimates the number of input tokens for a Claude request.
// This accounts for CW format overhead: tool documentation injection into system prompt,
// thinking prefix, tool_choice instruction, and system prompt wrapping.
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

	// 5. CW format overhead: tool documentation injected into system prompt
	// In CW format, tools with description > 500 chars get their full description
	// duplicated into the first user message as part of the system prompt.
	totalTokens += estimateToolDocTokens(req.Tools)

	// 6. Thinking prefix (~20 tokens when enabled)
	if req.Thinking != nil {
		if thinkingType, _ := req.Thinking["type"].(string); thinkingType != "" {
			totalTokens += 20
		}
	}

	// 7. System prompt wrapping tags (~10 tokens)
	if req.System != nil {
		totalTokens += 10
	}

	// 8. CW transformation overhead (parameter hints, constraint text, etc.)
	totalTokens += estimateCWTransformOverhead(req)

	return totalTokens
}

// estimateCWTransformOverhead estimates additional tokens introduced by the
// CodeWhisperer format transformation that are NOT present in Claude format:
//   - Parameter hints appended to each tool description by processTools
//   - Write/Edit constraint text (~70 tokens per tool)
//   - tool_choice instruction when required (~50 tokens)
//   - History alternation filler messages inserted by fixHistoryAlternation
//   - System prompt wrapping format ("--- SYSTEM PROMPT BEGIN/END ---")
func estimateCWTransformOverhead(req *ClaudeRequest) int {
	if req == nil {
		return 0
	}

	overhead := 0

	// Parameter hints: processTools adds "[Required: name (type), ...] [Optional: ...]"
	// to each tool description. Estimate ~3 tokens per schema property.
	for _, tool := range req.Tools {
		if tool.InputSchema != nil {
			if props, ok := tool.InputSchema["properties"].(map[string]any); ok {
				overhead += len(props)*3 + 5 // properties + hint structure overhead
			}
		}
		// Write/Edit tools get extra constraint + instruction text (~70 tokens each)
		// Actual: ~280 chars = ~65-75 tokens per tool
		if tool.Name == "Write" || tool.Name == "Edit" {
			overhead += 70
		}
	}

	// tool_choice=required adds a CRITICAL INSTRUCTION paragraph (~50 tokens)
	// Actual: ~195 chars = ~48 tokens
	if isToolChoiceRequiredForEstimation(req.ToolChoice) {
		overhead += 50
	}

	// History alternation filler messages: fixHistoryAlternation inserts
	// "Continue" (user) and "I understand." (assistant) messages to ensure
	// proper user/assistant alternation. Estimate ~6 tokens per filler.
	// Rough heuristic: check for adjacent same-role message pairs.
	if len(req.Messages) > 1 {
		fillerCount := 0
		for i := 1; i < len(req.Messages); i++ {
			if req.Messages[i].Role == req.Messages[i-1].Role {
				fillerCount++
			}
		}
		// Also account for potential start/end padding
		if len(req.Messages) > 0 && req.Messages[0].Role == "assistant" {
			fillerCount++
		}
		overhead += fillerCount * 6
	}

	// System prompt wrapping when no history: "--- SYSTEM PROMPT BEGIN/END ---" (~15 tokens)
	if len(req.Messages) <= 1 && req.System != nil {
		overhead += 15
	}

	return overhead
}

// isToolChoiceRequiredForEstimation checks if tool_choice is "required"
// Duplicated from request_transformer to avoid import cycle.
func isToolChoiceRequiredForEstimation(toolChoice any) bool {
	if toolChoice == nil {
		return false
	}
	switch tc := toolChoice.(type) {
	case string:
		return tc == "required"
	case map[string]any:
		if tcType, ok := tc["type"].(string); ok {
			return tcType == "required"
		}
	}
	return false
}

// estimateToolDocTokens estimates the extra tokens from tool documentation
// that gets injected into the system prompt in CW format.
// Tools with description > ToolDocThresholdLength (500 chars) have their
// full description duplicated into the first user message.
func estimateToolDocTokens(tools []ClaudeTool) int {
	if len(tools) == 0 {
		return 0
	}

	totalTokens := 0
	hasLongDesc := false

	for _, tool := range tools {
		if len(tool.Description) > 500 { // ToolDocThresholdLength
			hasLongDesc = true
			// "## Tool: {name}\n\n{description}" header + full description
			totalTokens += estimateTextTokens(tool.Name) + 5 // header overhead
			totalTokens += estimateTextTokens(tool.Description)
		}
	}

	if hasLongDesc {
		// Header: "---\n# Tool Documentation\nThe following tools have detailed documentation...\n\n"
		totalTokens += 25
	}

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
		return estimateImageBlockTokens(blockMap)

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
		if block.Source != nil && block.Source.Data != "" {
			return estimateImageDataTokens(len(block.Source.Data))
		}
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

// estimateImageBlockTokens estimates tokens for an image content block (map[string]any).
// If the image has base64 data, estimate based on actual data size.
// Otherwise fall back to the fixed ImageTokenEstimate.
func estimateImageBlockTokens(blockMap map[string]any) int {
	if source, ok := blockMap["source"].(map[string]any); ok {
		if data, ok := source["data"].(string); ok && data != "" {
			return estimateImageDataTokens(len(data))
		}
	}
	return ImageTokenEstimate
}

// estimateImageDataTokens estimates tokens based on base64 data length.
// base64 encodes 3 bytes into 4 chars, so raw bytes ≈ len * 3/4.
// In CW JSON serialization, the base64 string is embedded as-is,
// so the serialized cost is approximately len(base64) bytes.
// We use a conservative ratio: 1 token per 4 bytes of base64 data,
// with a minimum of ImageTokenEstimate to avoid underestimating small images.
func estimateImageDataTokens(base64Len int) int {
	// Each base64 char ≈ 1 byte in JSON serialization
	// Conservative: 4 bytes per token (same as CharsPerToken)
	estimated := base64Len / CharsPerToken
	if estimated < ImageTokenEstimate {
		return ImageTokenEstimate
	}
	return estimated
}

// estimateTextTokens estimates tokens for plain text
// Uses official Anthropic tokenizer for accurate counting
func estimateTextTokens(text string) int {
	return CountTokens(text)
}

// estimateToolNameTokens estimates tokens for a tool name
// Uses official Anthropic tokenizer for accurate counting
func estimateToolNameTokens(name string) int {
	return CountTokens(name)
}

// InflateInputTokens adds the inflation amount to input tokens for client-side
// context compression triggering. This makes the client think context usage is
// higher than actual, triggering auto-compression earlier.
func InflateInputTokens(actualTokens int) int {
	return actualTokens + InputTokenInflation
}

// CacheEstimation holds the breakdown of cacheable vs non-cacheable tokens
// for predicting Anthropic prompt cache behavior.
//
// Real Claude prompt caching uses multiple cache breakpoints. On a cache hit,
// the previously-cached prefix is reported as cache_read, while the new tokens
// added since the last request are reported as cache_creation. Both fields
// coexist in the same response.
type CacheEstimation struct {
	CacheableTokens     int  // system + tools + history (except last msg)
	StableTokens        int  // system + tools only (always cached once warm)
	HistoryTokens       int  // history messages (except last msg)
	NonCacheableTokens  int  // last message only
	TotalInputTokens    int  // sum of above
	MeetsCacheThreshold bool // >= MinCacheableTokens (1024)
}

// SplitCacheTokens computes cache_read and cache_creation token counts
// based on the previous cacheable token count from CacheTracker.
//
// Real Claude behavior: on a cache hit the previously-cached prefix is
// cache_read, and the newly-added tokens since last request are cache_creation.
// On a cache miss everything goes to cache_creation.
//
// contextLimit caps the total cache tokens to the upstream context window size.
// Pass GetContextWindowLimit(model) for CW paths, or 0 to disable capping
// (e.g. apikey paths where upstream supports 1M context).
func (ce CacheEstimation) SplitCacheTokens(cacheResult CacheResult, contextLimit int) (cacheRead, cacheCreation int) {
	if !ce.MeetsCacheThreshold {
		return 0, 0
	}
	if !cacheResult.Hit {
		// First request or TTL expired: everything is cache_creation
		cacheCreation = ce.CacheableTokens
		if contextLimit > 0 && cacheCreation > contextLimit {
			cacheCreation = contextLimit
		}
		return 0, cacheCreation
	}
	// Cache hit: previous tokens are cache_read, delta is cache_creation
	prevTokens := cacheResult.PrevTokens
	if prevTokens > ce.CacheableTokens {
		prevTokens = ce.CacheableTokens
	}
	cacheRead = prevTokens
	cacheCreation = ce.CacheableTokens - prevTokens

	// Apply context limit cap when specified
	if contextLimit > 0 {
		if cacheRead > contextLimit {
			cacheRead = contextLimit
		}
		if cacheCreation > contextLimit {
			cacheCreation = contextLimit
		}
		// Total cache tokens (read + creation) also cannot exceed context limit
		if cacheRead+cacheCreation > contextLimit {
			// Prefer preserving cache_creation (more expensive), trim cache_read
			cacheRead = contextLimit - cacheCreation
			if cacheRead < 0 {
				cacheRead = 0
			}
		}
	}
	return cacheRead, cacheCreation
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

	// 1. System prompt - stable (always cached once warm)
	systemTokens := estimateSystemTokens(req.System)
	result.StableTokens += systemTokens
	result.CacheableTokens += systemTokens

	// 2. Tools - stable (always cached once warm)
	toolsTokens := estimateToolsTokens(req.Tools)
	result.StableTokens += toolsTokens
	result.CacheableTokens += toolsTokens

	// 3. Messages - all except last are cacheable (history)
	msgCount := len(req.Messages)
	for i, msg := range req.Messages {
		tokens := estimateMessageTokens(msg)
		if i < msgCount-1 {
			result.HistoryTokens += tokens
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

// estimateFixedTokens calculates the token count for parts of a request that
// don't change during message truncation: system prompt + tools + CW overhead.
// This avoids redundant recomputation when trying multiple truncation points.
// Note: CW transformation overhead is included here as a conservative estimate.
// Some CW overhead (alternation padding) is technically message-dependent, but
// including it in fixed tokens leads to more aggressive truncation which is desired.
func estimateFixedTokens(req *ClaudeRequest) int {
	if req == nil {
		return 0
	}
	tokens := estimateSystemTokens(req.System) + estimateToolsTokens(req.Tools) + BaseRequestOverhead

	// CW format overhead: tool documentation injection
	tokens += estimateToolDocTokens(req.Tools)

	// Thinking prefix
	if req.Thinking != nil {
		if thinkingType, _ := req.Thinking["type"].(string); thinkingType != "" {
			tokens += 20
		}
	}

	// System prompt wrapping tags
	if req.System != nil {
		tokens += 10
	}

	// CW transformation overhead (parameter hints, constraint text, etc.)
	tokens += estimateCWTransformOverhead(req)

	return tokens
}

// computeMessageTokens returns a slice where each element is the estimated
// token count for the corresponding message. This enables O(1) range queries
// via suffix sums instead of re-scanning all messages per truncation attempt.
func computeMessageTokens(messages []ClaudeMessage) []int {
	tokens := make([]int, len(messages))
	for i := range messages {
		tokens[i] = estimateMessageTokens(messages[i])
	}
	return tokens
}

// computeSuffixSum builds a suffix sum array from per-message token counts.
// suffixSum[i] = sum of msgTokens[i:], so the total tokens for messages[point:]
// is simply suffixSum[point]. suffixSum has len(msgTokens)+1 entries, where
// suffixSum[len(msgTokens)] == 0.
func computeSuffixSum(msgTokens []int) []int {
	n := len(msgTokens)
	ss := make([]int, n+1)
	for i := n - 1; i >= 0; i-- {
		ss[i] = ss[i+1] + msgTokens[i]
	}
	return ss
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
