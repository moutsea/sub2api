package kiro

import "log"

// DetectToolInputTruncation detects if tool input JSON was truncated by output token limit.
// Uses 4-layer detection:
//  1. Empty input -> truncated
//  2. Bracket mismatch ({/[ more than }/]) -> truncated
//  3. Unclosed string (odd number of unescaped quotes) -> truncated
//  4. Trailing character is not a valid JSON terminator -> truncated
func DetectToolInputTruncation(toolName, rawInput string) bool {
	// Layer 1: empty input
	if rawInput == "" {
		log.Printf("[kiro-truncation] tool=%s empty input detected", toolName)
		return true
	}

	// Layer 2: bracket mismatch
	braces := 0
	brackets := 0
	inString := false
	escaped := false
	for _, ch := range rawInput {
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inString {
			escaped = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch ch {
		case '{':
			braces++
		case '}':
			braces--
		case '[':
			brackets++
		case ']':
			brackets--
		}
	}
	if braces > 0 || brackets > 0 {
		log.Printf("[kiro-truncation] tool=%s bracket mismatch braces=%d brackets=%d", toolName, braces, brackets)
		return true
	}

	// Layer 3: unclosed string
	if inString {
		log.Printf("[kiro-truncation] tool=%s unclosed string detected", toolName)
		return true
	}

	// Layer 4: trailing character check
	last := rawInput[len(rawInput)-1]
	switch last {
	case '}', ']', '"', 't', 'e', 'l': // valid JSON terminators: } ] "string" true false null
		// Also accept digits for numbers
		return false
	}
	if last >= '0' && last <= '9' {
		return false
	}
	log.Printf("[kiro-truncation] tool=%s suspicious trailing char=%c", toolName, last)
	return true
}

// BuildSoftLimitInput constructs the SOFT_LIMIT_REACHED replacement input.
func BuildSoftLimitInput() map[string]any {
	return map[string]any{
		"_status":  "SOFT_LIMIT_REACHED",
		"_message": "Tool output was truncated due to output token limit. Split content into smaller chunks (max 300 lines per write). Re-read the target file before retrying.",
	}
}
