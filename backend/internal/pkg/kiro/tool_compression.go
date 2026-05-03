// Package kiro provides tool compression utilities for Kiro/CodeWhisperer API integration.
// This implements dynamic tool compression to reduce tool payload size when it exceeds
// the target threshold, preventing 500 errors from Kiro API.
package kiro

import (
	"crypto/sha256"
	"encoding/json"
	"log"
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
	if schemaURI, ok := schema["$schema"]; ok {
		simplified["$schema"] = schemaURI
	}
	if t, ok := schema["type"]; ok {
		simplified["type"] = t
	}
	if enum, ok := schema["enum"]; ok {
		simplified["enum"] = enum
	}
	if required, ok := schema["required"]; ok {
		simplified["required"] = required
	}
	if additionalProps, ok := schema["additionalProperties"]; ok {
		if additionalPropsMap, ok := additionalProps.(map[string]any); ok {
			simplified["additionalProperties"] = simplifyInputSchema(additionalPropsMap)
		} else {
			simplified["additionalProperties"] = additionalProps
		}
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

// ToolNameLimit is the maximum length for tool names (Kiro API limit).
// Aligned with kiro.rs TOOL_NAME_MAX_LEN.
const ToolNameLimit = 63

// ShortenToolName shortens a tool name to fit within the limit.
// Aligned with kiro.rs: deterministic prefix + "_" + 8-char SHA256 suffix.
func ShortenToolName(name string) string {
	if len(name) <= ToolNameLimit {
		return name
	}

	hash := sha256.Sum256([]byte(name))
	hashSuffix := fmtHex8(hash[:])
	prefixLimit := ToolNameLimit - 1 - len(hashSuffix)
	prefix := truncateUTF8Bytes(name, prefixLimit)
	return prefix + "_" + hashSuffix
}

func fmtHex8(data []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, 8)
	for i := 0; i < 4; i++ {
		out[i*2] = hex[data[i]>>4]
		out[i*2+1] = hex[data[i]&0x0f]
	}
	return string(out)
}

func truncateUTF8Bytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	if maxBytes <= 0 {
		return ""
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	if end <= 0 {
		return ""
	}
	return s[:end]
}

// BuildToolNameMap generates unique short names for a list of tool names.
// Returns a map of original name -> shortened name.
func BuildToolNameMap(names []string) map[string]string {
	m := make(map[string]string)

	for _, n := range names {
		m[n] = ShortenToolName(n)
	}
	return m
}

func BuildToolNameMapFromClaudeTools(tools []ClaudeTool) map[string]string {
	var toolNames []string
	for _, tool := range tools {
		if tool.Name == "" {
			continue
		}
		if isWebSearchToolName(tool.Name) || isWebSearchToolType(tool.Type) {
			continue
		}
		toolNames = append(toolNames, tool.Name)
	}
	return BuildToolNameMap(toolNames)
}

func mapToolName(name string, toolNameMap map[string]string) string {
	if name == "" {
		return name
	}
	if toolNameMap != nil {
		if mapped, ok := toolNameMap[name]; ok {
			return mapped
		}
		mapped := ShortenToolName(name)
		toolNameMap[name] = mapped
		return mapped
	}
	return ShortenToolName(name)
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
	forwardMap := BuildToolNameMapFromClaudeTools(tools)
	if len(forwardMap) == 0 {
		return nil
	}
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
