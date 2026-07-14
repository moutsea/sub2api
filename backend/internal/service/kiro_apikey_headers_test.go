package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/require"
)

func TestBuildClaudeAPIHTTPRequestUsesCurrentClaudeCodeFingerprint(t *testing.T) {
	svc := &KiroGatewayService{}
	req, err := svc.buildClaudeAPIHTTPRequest(
		context.Background(),
		"https://example.com/v1/messages",
		"test-key",
		[]byte(`{"model":"claude-sonnet-4-6"}`),
		"test-session",
		"test-beta",
		0,
		claude.NewRequestHeaders(),
	)
	require.NoError(t, err)
	require.Equal(t, "2023-06-01", req.Header.Get("anthropic-version"))
	require.Equal(t, "claude-cli/2.1.209 (external, cli)", req.Header.Get("User-Agent"))
	require.Equal(t, "0.94.0", req.Header.Get("X-Stainless-Package-Version"))
	require.Equal(t, "v26.3.0", req.Header.Get("X-Stainless-Runtime-Version"))
	require.Equal(t, "test-beta", req.Header.Get("anthropic-beta"))
}
