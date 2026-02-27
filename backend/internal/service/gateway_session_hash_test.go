//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHashContent_Deterministic(t *testing.T) {
	t.Parallel()
	svc := &GatewayService{}

	h1 := svc.HashContent("hello")
	h2 := svc.HashContent("hello")
	require.Equal(t, h1, h2, "same input must produce same hash")
	require.Len(t, h1, 32, "hash should be 32 hex chars")
}

func TestHashContent_DifferentInputs(t *testing.T) {
	t.Parallel()
	svc := &GatewayService{}

	require.NotEqual(t, svc.HashContent("a"), svc.HashContent("b"))
}

func TestGenerateSessionHash_ConversationIDHighestPriority(t *testing.T) {
	t.Parallel()
	svc := &GatewayService{}

	parsed := &ParsedRequest{
		MetadataUserID: "session_123e4567-e89b-12d3-a456-426614174000",
		System:         []any{map[string]any{"text": "system prompt"}},
	}

	hash := svc.GenerateSessionHash(parsed, "my-conv-id")
	require.Equal(t, svc.HashContent("my-conv-id"), hash, "X-Conversation-ID should take highest priority")
}

func TestGenerateSessionHash_MetadataSessionID(t *testing.T) {
	t.Parallel()
	svc := &GatewayService{}

	parsed := &ParsedRequest{
		MetadataUserID: "user_session_123e4567-e89b-12d3-a456-426614174000_extra",
		System:         []any{map[string]any{"text": "system prompt"}},
	}

	hash := svc.GenerateSessionHash(parsed, "")
	require.Equal(t, "123e4567-e89b-12d3-a456-426614174000", hash, "should extract session UUID directly")
}

func TestGenerateSessionHash_CacheableContent(t *testing.T) {
	t.Parallel()
	svc := &GatewayService{}

	parsed := &ParsedRequest{
		System: []any{
			map[string]any{
				"text":          "cached system",
				"cache_control": map[string]any{"type": "ephemeral"},
			},
		},
		HasSystem: true,
	}

	hash := svc.GenerateSessionHash(parsed, "")
	require.Equal(t, svc.HashContent("cached system"), hash, "should use cacheable content")
}

func TestGenerateSessionHash_SystemPromptFallback(t *testing.T) {
	t.Parallel()
	svc := &GatewayService{}

	parsed := &ParsedRequest{
		System:    []any{map[string]any{"text": "my system prompt"}},
		HasSystem: true,
	}

	hash := svc.GenerateSessionHash(parsed, "")
	require.Equal(t, svc.HashContent("my system prompt"), hash, "should fall back to system prompt")
}

func TestGenerateSessionHash_FirstMessageFallback(t *testing.T) {
	t.Parallel()
	svc := &GatewayService{}

	parsed := &ParsedRequest{
		Messages: []any{
			map[string]any{"role": "user", "content": "hello world"},
		},
	}

	hash := svc.GenerateSessionHash(parsed, "")
	require.Equal(t, svc.HashContent("hello world"), hash, "should fall back to first message")
}

func TestGenerateSessionHash_EmptyRequest(t *testing.T) {
	t.Parallel()
	svc := &GatewayService{}

	require.Empty(t, svc.GenerateSessionHash(&ParsedRequest{}, ""), "empty request should return empty hash")
	require.Empty(t, svc.GenerateSessionHash(nil, ""), "nil request should return empty hash")
}
