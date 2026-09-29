package kiro

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// OpenCode puts cache breakpoints on the last two messages of each request.
// Growing the conversation moves those markers without changing its history.
func openCodeCacheRequest(messageCount int, mark bool) *ClaudeRequest {
	req := &ClaudeRequest{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 64000,
		System:    strings.Repeat("system prompt ", 600),
	}
	for i := 0; i < messageCount; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		part := map[string]any{
			"type": "text",
			"text": fmt.Sprintf("turn %d ", i) + strings.Repeat("historical conversation content ", 300),
		}
		if mark && i >= messageCount-2 {
			part["cache_control"] = map[string]any{"type": "ephemeral"}
		}
		req.Messages = append(req.Messages, ClaudeMessage{Role: role, Content: []any{part}})
	}
	return req
}

func openCodeCacheContext(sessionID string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "opencode/test")
	c.Request.Header.Set("x-api-key", "test-key")
	c.Request.Header.Set("x-opencode-session", sessionID)
	return c
}

func TestCacheTrackerOpenCodeMovingCacheControl(t *testing.T) {
	first := openCodeCacheRequest(5, true)
	plain := openCodeCacheRequest(5, false)
	c := openCodeCacheContext("session")
	markedPayload, err := TransformClaudeToCodeWhisperer(first, "", c)
	require.NoError(t, err)
	plainPayload, err := TransformClaudeToCodeWhisperer(plain, "", c)
	require.NoError(t, err)
	require.Equal(t, plainPayload, markedPayload, "cache markers do not change the CW payload")

	for _, shortSystem := range []bool{false, true} {
		t.Run(fmt.Sprintf("short_system=%v", shortSystem), func(t *testing.T) {
			first := openCodeCacheRequest(5, true)
			next := openCodeCacheRequest(7, true)
			if shortSystem {
				first.System, next.System = "brief", "brief"
			}
			tracker := NewCacheTracker(DefaultCacheTTL)
			defer tracker.Stop()
			scope := CacheScope{AccountID: 1, Model: first.Model, SessionID: "session"}
			before, after := EstimateCache(first), EstimateCache(next)
			tracker.BeginPrefix(scope, first, before).Commit()
			original, err := json.Marshal(next)
			require.NoError(t, err)
			got := tracker.BeginPrefix(scope, next, after)
			read, write := after.SplitCacheTokens(got, 200000)
			t.Logf("hit=%s cache_read=%d cache_create=%d", got.HitKind, read, write)
			require.Equal(t, CacheHitFull, got.HitKind)
			require.Equal(t, before.CacheableTokens, read)
			require.Equal(t, after.CacheableTokens-before.CacheableTokens, write)
			unchanged, err := json.Marshal(next)
			require.NoError(t, err)
			require.Equal(t, original, unchanged, "hashing must not mutate the request")
		})
	}
}

func TestCacheTrackerSystemCacheControl(t *testing.T) {
	first, next := openCodeCacheRequest(5, false), openCodeCacheRequest(5, false)
	first.System = []any{map[string]any{
		"type": "text", "text": first.System,
		"cache_control": map[string]any{"type": "ephemeral"},
	}}
	next.System = []any{map[string]any{"type": "text", "text": next.System}}
	tracker := NewCacheTracker(DefaultCacheTTL)
	defer tracker.Stop()
	scope := CacheScope{AccountID: 1, Model: first.Model, SessionID: "session"}
	original, err := json.Marshal(first)
	require.NoError(t, err)
	tracker.BeginPrefix(scope, first, EstimateCache(first)).Commit()
	unchanged, err := json.Marshal(first)
	require.NoError(t, err)
	require.Equal(t, original, unchanged, "system cache markers must remain on the request")
	require.Equal(t, CacheHitFull, tracker.BeginPrefix(scope, next, EstimateCache(next)).HitKind)
}

func TestCacheTrackerPreservesCacheControlInToolData(t *testing.T) {
	for _, schema := range []bool{false, true} {
		t.Run(fmt.Sprintf("schema=%v", schema), func(t *testing.T) {
			makeRequest := func(value string) *ClaudeRequest {
				req := openCodeCacheRequest(5, false)
				req.Tools = []ClaudeTool{{Name: "test", InputSchema: map[string]any{"type": "object"}}}
				if schema {
					req.Tools[0].InputSchema["properties"] = map[string]any{
						"cache_control": map[string]any{"type": "string", "description": value},
					}
				} else {
					req.Messages[1].Content = []any{map[string]any{
						"type": "tool_use", "id": "call_1", "name": "test",
						"input": map[string]any{"cache_control": value},
					}}
				}
				return req
			}
			tracker := NewCacheTracker(DefaultCacheTTL)
			defer tracker.Stop()
			scope := CacheScope{AccountID: 1, Model: "claude-sonnet-4-6", SessionID: "session"}
			first, changed := makeRequest("original"), makeRequest("changed")
			tracker.BeginPrefix(scope, first, EstimateCache(first)).Commit()
			got := tracker.BeginPrefix(scope, changed, EstimateCache(changed))
			require.NotEqual(t, CacheHitFull, got.HitKind, "tool data changes must invalidate the history")
			if schema {
				require.False(t, got.Hit, "tool schema changes must invalidate the stable prefix")
			}
		})
	}
}

func TestCacheTrackerOpenCodeSessions(t *testing.T) {
	tracker := NewCacheTracker(DefaultCacheTTL)
	defer tracker.Stop()
	cA, cB := openCodeCacheContext("session-a"), openCodeCacheContext("session-b")
	scope := func(c *gin.Context) CacheScope {
		return CacheScope{AccountID: 1, Model: "claude-sonnet-4-6", SessionID: GenerateStableConversationID(c), ClientKey: ExtractAPIKey(c)}
	}
	firstA, firstB := openCodeCacheRequest(5, false), openCodeCacheRequest(5, false)
	firstB.System = "different session instructions " + firstB.System.(string)
	tracker.BeginPrefix(scope(cA), firstA, EstimateCache(firstA)).Commit()
	tracker.BeginPrefix(scope(cB), firstB, EstimateCache(firstB)).Commit()
	nextA := openCodeCacheRequest(7, false)
	after := EstimateCache(nextA)
	hit := tracker.BeginPrefix(scope(cA), nextA, after)
	read, write := after.SplitCacheTokens(hit, 200000)
	require.Equal(t, CacheHitFull, hit.HitKind, "another OpenCode session must not evict this session's prefix")
	require.Equal(t, EstimateCache(firstA).CacheableTokens, read)
	require.Equal(t, after.CacheableTokens-read, write)
	require.Equal(t, 2, tracker.Size())
}
