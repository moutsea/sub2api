package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
)

const (
	kiroOAuthIdentitySystemPrompt = "You are the requested AI model specified by the client request. Do not identify yourself as Kiro, AWS, Amazon Q, CodeWhisperer, or an IDE assistant. When asked about your identity or model, answer according to the requested model name and capabilities rather than the transport/provider integration. Otherwise, follow the user's instructions normally as a general-purpose coding and reasoning assistant."
	kiroOAuthIdentityPromptMarker = "Do not identify yourself as Kiro, AWS, Amazon Q, CodeWhisperer"
)

func injectKiroOAuthIdentitySystemPrompt(account *Account, req *kiro.ClaudeRequest) bool {
	if account == nil || req == nil || !account.IsKiro() || !account.IsOAuth() || account.IsKiroApiKey() {
		return false
	}
	if systemContainsText(req.System, kiroOAuthIdentityPromptMarker) {
		return false
	}

	switch system := req.System.(type) {
	case nil:
		req.System = kiroOAuthIdentitySystemPrompt
	case string:
		if strings.TrimSpace(system) == "" {
			req.System = kiroOAuthIdentitySystemPrompt
		} else {
			req.System = kiroOAuthIdentitySystemPrompt + "\n\n" + system
		}
	case []any:
		req.System = append([]any{map[string]any{"type": "text", "text": kiroOAuthIdentitySystemPrompt}}, system...)
	case []map[string]any:
		blocks := make([]any, 0, len(system)+1)
		blocks = append(blocks, map[string]any{"type": "text", "text": kiroOAuthIdentitySystemPrompt})
		for _, block := range system {
			blocks = append(blocks, block)
		}
		req.System = blocks
	default:
		req.System = []any{
			map[string]any{"type": "text", "text": kiroOAuthIdentitySystemPrompt},
			system,
		}
	}
	return true
}

func systemContainsText(system any, needle string) bool {
	if system == nil || needle == "" {
		return false
	}

	switch value := system.(type) {
	case string:
		return strings.Contains(value, needle)
	case []any:
		for _, item := range value {
			if systemContainsText(item, needle) {
				return true
			}
		}
	case []map[string]any:
		for _, item := range value {
			if systemContainsText(item, needle) {
				return true
			}
		}
	case map[string]any:
		if text, ok := value["text"].(string); ok && strings.Contains(text, needle) {
			return true
		}
		if content, ok := value["content"].(string); ok && strings.Contains(content, needle) {
			return true
		}
	}
	return false
}
