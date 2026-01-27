// Package kiro provides tool compression utilities for Kiro/CodeWhisperer API integration.
// This implements dynamic tool compression to reduce tool payload size when it exceeds
// the target threshold, preventing 500 errors from Kiro API.
package kiro

import (
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Tool compression constants (aligned with CLIProxyAPIPlus)
const (
	// ToolCompressionTargetSize is the target total size for compressed tools (20KB).
	// If tools exceed this size, compression will be applied.
	ToolCompressionTargetSize = 20 * 1024 // 20KB

	// MinToolDescriptionLength is the minimum description length after compression.
	// Descriptions will not be shortened below this length.
	MinToolDescriptionLength = 50

	// KiroMaxToolDescLen is the maximum description length for a single Kiro API tool.
	// Kiro API limit is 10240 bytes, leave room for "..."
	KiroMaxToolDescLen = 10237
)

// calculateToolsSize calculates the JSON serialized size of the tools list.
// Returns the size in bytes.
func calculateToolsSize(tools []ToolItem) int {
	if len(tools) == 0 {
		return 0
	}
	data, err := json.Marshal(tools)
	if err != nil {
		return 0
	}
	return len(data)
}

// simplifyInputSchema simplifies the input_schema by keeping only essential fields:
// type, enum, required. Recursively processes nested properties.
func simplifyInputSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}

	simplified := make(map[string]any)

	// Keep essential fields
	if t, ok := schema["type"]; ok {
		simplified["type"] = t
	}
	if enum, ok := schema["enum"]; ok {
		simplified["enum"] = enum
	}
	if required, ok := schema["required"]; ok {
		simplified["required"] = required
	}

	// Recursively process properties
	if properties, ok := schema["properties"].(map[string]any); ok {
		simplifiedProps := make(map[string]any)
		for key, value := range properties {
			if propMap, ok := value.(map[string]any); ok {
				simplifiedProps[key] = simplifyInputSchema(propMap)
			} else {
				simplifiedProps[key] = value
			}
		}
		simplified["properties"] = simplifiedProps
	}

	// Process items for array types
	if items, ok := schema["items"].(map[string]any); ok {
		simplified["items"] = simplifyInputSchema(items)
	}

	// Process additionalProperties if present
	if additionalProps, ok := schema["additionalProperties"].(map[string]any); ok {
		simplified["additionalProperties"] = simplifyInputSchema(additionalProps)
	}

	// Process anyOf, oneOf, allOf
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if arr, ok := schema[key].([]any); ok {
			simplifiedArr := make([]any, len(arr))
			for i, item := range arr {
				if itemMap, ok := item.(map[string]any); ok {
					simplifiedArr[i] = simplifyInputSchema(itemMap)
				} else {
					simplifiedArr[i] = item
				}
			}
			simplified[key] = simplifiedArr
		}
	}

	return simplified
}

// compressToolDescription compresses a description to the target length.
// Ensures the result is at least MinToolDescriptionLength characters.
// Uses UTF-8 safe truncation.
func compressToolDescription(description string, targetLength int) string {
	if targetLength < MinToolDescriptionLength {
		targetLength = MinToolDescriptionLength
	}

	if len(description) <= targetLength {
		return description
	}

	// Find a safe truncation point (UTF-8 boundary)
	truncLen := targetLength - 3 // Leave room for "..."

	// Ensure we don't cut in the middle of a UTF-8 character
	for truncLen > 0 && !utf8.RuneStart(description[truncLen]) {
		truncLen--
	}

	if truncLen <= 0 {
		// If truncLen is 0 or negative, return as much as we can up to MinToolDescriptionLength
		if len(description) <= MinToolDescriptionLength {
			return description
		}
		return description[:MinToolDescriptionLength]
	}

	return description[:truncLen] + "..."
}

// compressToolsIfNeeded compresses tools if their total size exceeds the target threshold.
// Compression strategy:
// 1. First, check if compression is needed (size > ToolCompressionTargetSize)
// 2. Step 1: Simplify input_schema (keep only type/enum/required)
// 3. Step 2: Proportionally compress descriptions (minimum MinToolDescriptionLength chars)
// Returns the compressed tools list.
func compressToolsIfNeeded(tools []ToolItem, enableLogging bool) []ToolItem {
	if len(tools) == 0 {
		return tools
	}

	originalSize := calculateToolsSize(tools)
	if originalSize <= ToolCompressionTargetSize {
		return tools
	}

	if enableLogging {
		log.Printf("[kiro] tools size %d bytes exceeds target %d bytes, starting compression",
			originalSize, ToolCompressionTargetSize)
	}

	// Create a copy of tools to avoid modifying the original
	compressedTools := make([]ToolItem, len(tools))
	for i, tool := range tools {
		if tool.WebSearch != nil {
			compressedTools[i] = ToolItem{WebSearch: tool.WebSearch}
			continue
		}
		if tool.Standard != nil {
			compressedTools[i] = ToolItem{
				Standard: &CodeWhispererTool{
					ToolSpecification: ToolSpecification{
						Name:        tool.Standard.ToolSpecification.Name,
						Description: tool.Standard.ToolSpecification.Description,
						InputSchema: InputSchema{
							JSON: copyMap(tool.Standard.ToolSpecification.InputSchema.JSON),
						},
					},
				},
			}
		}
	}

	// Step 1: Simplify input_schema
	for i := range compressedTools {
		if compressedTools[i].Standard != nil {
			compressedTools[i].Standard.ToolSpecification.InputSchema.JSON =
				simplifyInputSchema(compressedTools[i].Standard.ToolSpecification.InputSchema.JSON)
		}
	}

	sizeAfterSchemaSimplification := calculateToolsSize(compressedTools)

	// Check if we're within target after schema simplification
	if sizeAfterSchemaSimplification <= ToolCompressionTargetSize {
		if enableLogging {
			log.Printf("[kiro] compression complete after schema simplification, final size: %d bytes",
				sizeAfterSchemaSimplification)
		}
		return compressedTools
	}

	// Step 2: Compress descriptions proportionally
	sizeToReduce := float64(sizeAfterSchemaSimplification - ToolCompressionTargetSize)
	var totalDescLen float64
	for _, tool := range compressedTools {
		if tool.Standard != nil {
			totalDescLen += float64(len(tool.Standard.ToolSpecification.Description))
		}
	}

	if totalDescLen > 0 {
		// Assume size reduction comes primarily from descriptions.
		keepRatio := 1.0 - (sizeToReduce / totalDescLen)
		if keepRatio > 1.0 {
			keepRatio = 1.0
		} else if keepRatio < 0 {
			keepRatio = 0
		}

		for i := range compressedTools {
			if compressedTools[i].Standard != nil {
				desc := compressedTools[i].Standard.ToolSpecification.Description
				targetLen := int(float64(len(desc)) * keepRatio)
				compressedTools[i].Standard.ToolSpecification.Description = compressToolDescription(desc, targetLen)
			}
		}
	}

	finalSize := calculateToolsSize(compressedTools)
	if enableLogging {
		reduction := float64(originalSize-finalSize) / float64(originalSize) * 100
		log.Printf("[kiro] compression complete, original: %d bytes, final: %d bytes (%.1f%% reduction)",
			originalSize, finalSize, reduction)
	}

	return compressedTools
}

// copyMap creates a deep copy of a map[string]any
func copyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	cp := make(map[string]any)
	for k, v := range m {
		switch val := v.(type) {
		case map[string]any:
			cp[k] = copyMap(val)
		case []any:
			cp[k] = copySlice(val)
		default:
			cp[k] = v
		}
	}
	return cp
}

// ==================== Tool Name Shortening ====================

// ToolNameLimit is the maximum length for tool names (Kiro API limit)
const ToolNameLimit = 64

// ShortenToolName shortens a tool name to fit within the limit.
// For mcp__ prefixed names, it preserves the prefix and last segment.
func ShortenToolName(name string) string {
	if len(name) <= ToolNameLimit {
		return name
	}

	// Special handling for MCP tools (mcp__server__toolname)
	if strings.HasPrefix(name, "mcp__") {
		idx := strings.LastIndex(name, "__")
		if idx > 0 {
			candidate := "mcp__" + name[idx+2:]
			if len(candidate) > ToolNameLimit {
				return candidate[:ToolNameLimit]
			}
			return candidate
		}
	}

	return name[:ToolNameLimit]
}

// BuildToolNameMap generates unique short names for a list of tool names.
// Returns a map of original name -> shortened name.
func BuildToolNameMap(names []string) map[string]string {
	used := make(map[string]struct{})
	m := make(map[string]string)

	baseCandidate := func(n string) string {
		if len(n) <= ToolNameLimit {
			return n
		}
		if strings.HasPrefix(n, "mcp__") {
			idx := strings.LastIndex(n, "__")
			if idx > 0 {
				cand := "mcp__" + n[idx+2:]
				if len(cand) > ToolNameLimit {
					return cand[:ToolNameLimit]
				}
				return cand
			}
		}
		return n[:ToolNameLimit]
	}

	makeUnique := func(cand string) string {
		if _, ok := used[cand]; !ok {
			return cand
		}
		base := cand
		for i := 1; ; i++ {
			suffix := "_" + strconv.Itoa(i)
			allowed := ToolNameLimit - len(suffix)
			if allowed < 0 {
				allowed = 0
			}
			tmp := base
			if len(tmp) > allowed {
				tmp = tmp[:allowed]
			}
			tmp = tmp + suffix
			if _, ok := used[tmp]; !ok {
				return tmp
			}
		}
	}

	for _, n := range names {
		cand := baseCandidate(n)
		uniq := makeUnique(cand)
		used[uniq] = struct{}{}
		m[n] = uniq
	}
	return m
}

// BuildReverseToolNameMap builds a reverse map (short -> original) from the forward map.
func BuildReverseToolNameMap(forwardMap map[string]string) map[string]string {
	reverse := make(map[string]string, len(forwardMap))
	for original, short := range forwardMap {
		reverse[short] = original
	}
	return reverse
}

// BuildReverseMapFromClaudeTools builds a reverse map (short -> original) from Claude tools.
// This is used in response transformation to restore original tool names.
// Note: web_search tools are excluded since they are not shortened.
func BuildReverseMapFromClaudeTools(tools []ClaudeTool) map[string]string {
	var toolNames []string
	for _, tool := range tools {
		if tool.Name == "" {
			continue
		}
		// Exclude web_search tools (they are not shortened)
		if isWebSearchToolName(tool.Name) || isWebSearchToolType(tool.Type) {
			continue
		}
		toolNames = append(toolNames, tool.Name)
	}
	if len(toolNames) == 0 {
		return nil
	}
	forwardMap := BuildToolNameMap(toolNames)
	return BuildReverseToolNameMap(forwardMap)
}

// isWebSearchToolName checks if the tool name indicates a web search tool
func isWebSearchToolName(name string) bool {
	return name == "web_search" ||
		name == "web_search_20250305" ||
		strings.HasPrefix(name, "web_search_")
}

// isWebSearchToolType checks if the tool type indicates a web search tool
func isWebSearchToolType(toolType string) bool {
	if toolType == "" {
		return false
	}
	return strings.HasPrefix(toolType, "web_search")
}

// copySlice creates a deep copy of a []any
func copySlice(s []any) []any {
	if s == nil {
		return nil
	}
	cp := make([]any, len(s))
	for i, v := range s {
		switch val := v.(type) {
		case map[string]any:
			cp[i] = copyMap(val)
		case []any:
			cp[i] = copySlice(val)
		default:
			cp[i] = v
		}
	}
	return cp
}
