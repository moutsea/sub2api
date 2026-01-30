// Package kiro provides tokenizer utilities using the official Anthropic tokenizer.
package kiro

import (
	"log"
	"sync"

	tokenizer "github.com/qhenkart/anthropic-tokenizer-go"
)

var (
	globalTokenizer  *tokenizer.Tokenizer
	tokenizerOnce    sync.Once
	tokenizerInitErr error
)

// GetTokenizer returns the global tokenizer instance (singleton pattern).
// Note: tokenizer initialization is expensive (~150,000 iterations), must be reused.
func GetTokenizer() *tokenizer.Tokenizer {
	tokenizerOnce.Do(func() {
		globalTokenizer, tokenizerInitErr = tokenizer.New()
		if tokenizerInitErr != nil {
			log.Printf("[kiro-tokenizer] Failed to initialize official tokenizer: %v", tokenizerInitErr)
		} else {
			log.Printf("[kiro-tokenizer] Official tokenizer initialized successfully")
		}
	})
	return globalTokenizer
}

// CountTokens uses the official tokenizer to count tokens in text.
// Falls back to simple estimation if tokenizer is unavailable.
func CountTokens(text string) int {
	if text == "" {
		return 0
	}
	t := GetTokenizer()
	if t == nil {
		// Fallback to simple estimation
		return (len(text) + CharsPerToken - 1) / CharsPerToken
	}
	return t.Tokens(text)
}

// IsTokenizerAvailable checks if the tokenizer is available.
func IsTokenizerAvailable() bool {
	return GetTokenizer() != nil
}
