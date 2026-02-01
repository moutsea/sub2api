package service

import "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"

// ParseClaudeRequestFromJSON parses a Claude request from JSON bytes
// This is a wrapper around kiro.ParseClaudeRequestFromJSON for use in handlers
func ParseClaudeRequestFromJSON(data []byte) (*kiro.ClaudeRequest, error) {
	return kiro.ParseClaudeRequestFromJSON(data)
}
