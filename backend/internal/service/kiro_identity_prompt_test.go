package service

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/stretchr/testify/require"
)

func TestInjectKiroOAuthIdentitySystemPromptNilSystem(t *testing.T) {
	req := &kiro.ClaudeRequest{Model: "claude-opus-4-8"}
	account := &Account{
		Platform:    PlatformKiro,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"auth_type": KiroAuthMethodSocial},
	}

	require.True(t, injectKiroOAuthIdentitySystemPrompt(account, req))
	require.Equal(t, kiroOAuthIdentitySystemPrompt, req.System)
}

func TestInjectKiroOAuthIdentitySystemPromptPrependsStringSystem(t *testing.T) {
	req := &kiro.ClaudeRequest{
		Model:  "claude-opus-4-8",
		System: "Follow the user's project conventions.",
	}
	account := &Account{
		Platform:    PlatformKiro,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"auth_type": KiroAuthMethodIdC},
	}

	require.True(t, injectKiroOAuthIdentitySystemPrompt(account, req))
	system, ok := req.System.(string)
	require.True(t, ok)
	require.True(t, strings.HasPrefix(system, kiroOAuthIdentitySystemPrompt))
	require.Contains(t, system, "Follow the user's project conventions.")

	require.False(t, injectKiroOAuthIdentitySystemPrompt(account, req))
	require.Equal(t, system, req.System)
}

func TestInjectKiroOAuthIdentitySystemPromptPrependsBlockSystem(t *testing.T) {
	req := &kiro.ClaudeRequest{
		Model: "claude-opus-4-8",
		System: []any{
			map[string]any{"type": "text", "text": "Existing system block."},
		},
	}
	account := &Account{
		Platform:    PlatformKiro,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"auth_type": KiroAuthMethodSocial},
	}

	require.True(t, injectKiroOAuthIdentitySystemPrompt(account, req))
	blocks, ok := req.System.([]any)
	require.True(t, ok)
	require.Len(t, blocks, 2)
	require.Equal(t, kiroOAuthIdentitySystemPrompt, blocks[0].(map[string]any)["text"])
	require.Equal(t, "Existing system block.", blocks[1].(map[string]any)["text"])
}

func TestInjectKiroOAuthIdentitySystemPromptConvertsTypedBlockSystem(t *testing.T) {
	req := &kiro.ClaudeRequest{
		Model: "claude-opus-4-8",
		System: []map[string]any{
			{"type": "text", "text": "Existing typed system block."},
		},
	}
	account := &Account{
		Platform:    PlatformKiro,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"auth_type": KiroAuthMethodSocial},
	}

	require.True(t, injectKiroOAuthIdentitySystemPrompt(account, req))
	blocks, ok := req.System.([]any)
	require.True(t, ok)
	require.Len(t, blocks, 2)
	require.Equal(t, kiroOAuthIdentitySystemPrompt, blocks[0].(map[string]any)["text"])
	require.Equal(t, "Existing typed system block.", blocks[1].(map[string]any)["text"])
}

func TestInjectKiroOAuthIdentitySystemPromptSkipsAPIKey(t *testing.T) {
	req := &kiro.ClaudeRequest{Model: "claude-opus-4-8"}
	account := &Account{
		Platform:    PlatformKiro,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"auth_type": KiroAuthMethodAPIKey},
	}

	require.False(t, injectKiroOAuthIdentitySystemPrompt(account, req))
	require.Nil(t, req.System)
}

func TestInjectKiroOAuthIdentitySystemPromptSkipsAPIKeyTypeWithoutAuthMarker(t *testing.T) {
	req := &kiro.ClaudeRequest{Model: "claude-opus-4-8"}
	account := &Account{
		Platform: PlatformKiro,
		Type:     AccountTypeAPIKey,
	}

	require.False(t, injectKiroOAuthIdentitySystemPrompt(account, req))
	require.Nil(t, req.System)
}

func TestInjectKiroOAuthIdentitySystemPromptSkipsNonKiro(t *testing.T) {
	req := &kiro.ClaudeRequest{Model: "claude-opus-4-8"}
	account := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
	}

	require.False(t, injectKiroOAuthIdentitySystemPrompt(account, req))
	require.Nil(t, req.System)
}
