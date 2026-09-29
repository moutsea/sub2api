//go:build unit

package kiro

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func newTestContext(headers map[string]string) *gin.Context {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	return c
}

func TestGenerateStableConversationID_XSessionID(t *testing.T) {
	t.Parallel()
	c := newTestContext(map[string]string{"X-Session-ID": "my-session-123"})
	require.Equal(t, "my-session-123", GenerateStableConversationID(c))
}

func TestGenerateStableConversationID_XConversationID(t *testing.T) {
	t.Parallel()
	c := newTestContext(map[string]string{"X-Conversation-ID": "conv-abc"})
	require.Equal(t, "conv-abc", GenerateStableConversationID(c))
}

func TestGenerateStableConversationID_OpenCode(t *testing.T) {
	t.Parallel()
	c := newTestContext(map[string]string{"X-OpenCode-Session": "opencode-session"})
	require.Equal(t, "opencode-session", GenerateStableConversationID(c))

	c.Request.Header.Set("X-Conversation-ID", "conversation-wins")
	require.Equal(t, "conversation-wins", GenerateStableConversationID(c))
	c.Request.Header.Set("X-Session-ID", "session-wins")
	require.Equal(t, "session-wins", GenerateStableConversationID(c))
}

func TestGenerateStableConversationID_SessionIDTakesPriority(t *testing.T) {
	t.Parallel()
	c := newTestContext(map[string]string{
		"X-Session-ID":      "session-wins",
		"X-Conversation-ID": "conv-loses",
	})
	require.Equal(t, "session-wins", GenerateStableConversationID(c))
}

func TestGenerateStableConversationID_FallbackDeterministic(t *testing.T) {
	t.Parallel()
	c1 := newTestContext(map[string]string{
		"User-Agent":    "test-agent",
		"Authorization": "Bearer sk-test-key",
	})
	c2 := newTestContext(map[string]string{
		"User-Agent":    "test-agent",
		"Authorization": "Bearer sk-test-key",
	})

	id1 := GenerateStableConversationID(c1)
	id2 := GenerateStableConversationID(c2)

	require.Equal(t, id1, id2, "same client characteristics should produce same ID")
	require.Contains(t, id1, "conv-", "fallback ID should have conv- prefix")
}

func TestGenerateStableConversationID_DifferentClients(t *testing.T) {
	t.Parallel()
	c1 := newTestContext(map[string]string{
		"User-Agent":    "agent-a",
		"Authorization": "Bearer sk-key-a",
	})
	c2 := newTestContext(map[string]string{
		"User-Agent":    "agent-b",
		"Authorization": "Bearer sk-key-b",
	})

	require.NotEqual(t, GenerateStableConversationID(c1), GenerateStableConversationID(c2))
}

func TestGenerateStableConversationID_NilContext(t *testing.T) {
	t.Parallel()
	id := GenerateStableConversationID(nil)
	require.NotEmpty(t, id, "nil context should return a random UUID")
}

func TestGenerateStableAgentContinuationID_CustomHeader(t *testing.T) {
	t.Parallel()
	c := newTestContext(map[string]string{"X-Agent-Continuation-ID": "agent-123"})
	require.Equal(t, "agent-123", GenerateStableAgentContinuationID(c))
}

func TestGenerateStableAgentContinuationID_FallbackDeterministic(t *testing.T) {
	t.Parallel()
	c1 := newTestContext(map[string]string{
		"User-Agent":    "test-agent",
		"Authorization": "Bearer sk-test-key",
	})
	c2 := newTestContext(map[string]string{
		"User-Agent":    "test-agent",
		"Authorization": "Bearer sk-test-key",
	})

	id1 := GenerateStableAgentContinuationID(c1)
	id2 := GenerateStableAgentContinuationID(c2)

	require.Equal(t, id1, id2, "same client should produce same agent continuation ID")
	require.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, id1, "should be UUID format")
}
