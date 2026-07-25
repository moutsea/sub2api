package service

import (
	"net/http"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestNormalizeHeaderOverrideCredentials(t *testing.T) {
	credentials := map[string]any{
		credentialHeaderOverrideEnabled: true,
		credentialHeaderOverrides: map[string]any{
			" User-Agent ": " codex-custom ",
			"OpenAI-Beta":  "responses=experimental",
		},
	}

	require.NoError(t, NormalizeHeaderOverrideCredentials(credentials))
	require.Equal(t, map[string]any{
		"user-agent":  "codex-custom",
		"openai-beta": "responses=experimental",
	}, credentials[credentialHeaderOverrides])
}

func TestNormalizeHeaderOverrideCredentialsRejectsBlockedAndDuplicateNames(t *testing.T) {
	tests := []struct {
		name      string
		overrides map[string]any
	}{
		{name: "authorization", overrides: map[string]any{"Authorization": "Bearer attacker"}},
		{name: "content type", overrides: map[string]any{"Content-Type": "text/plain"}},
		{name: "OpenAI organization", overrides: map[string]any{"OpenAI-Organization": "org_other"}},
		{name: "OpenAI project", overrides: map[string]any{"OpenAI-Project": "proj_other"}},
		{name: "forwarded for", overrides: map[string]any{"X-Forwarded-For": "127.0.0.1"}},
		{name: "forwarded routing", overrides: map[string]any{"Forwarded": "for=127.0.0.1;proto=https"}},
		{name: "real ip", overrides: map[string]any{"X-Real-IP": "127.0.0.1"}},
		{name: "cdn client ip", overrides: map[string]any{"CF-Connecting-IP": "127.0.0.1"}},
		{name: "duplicate", overrides: map[string]any{"X-Custom": "one", "x-custom": "two"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			credentials := map[string]any{credentialHeaderOverrides: test.overrides}
			err := NormalizeHeaderOverrideCredentials(credentials)
			require.Error(t, err)
			require.Equal(t, "INVALID_HEADER_OVERRIDE", infraerrors.Reason(err))
		})
	}
}

func TestAccountApplyHeaderOverridesOnlyForOpenAIAPIKey(t *testing.T) {
	credentials := map[string]any{
		credentialHeaderOverrideEnabled: true,
		credentialHeaderOverrides: map[string]any{
			"user-agent":    "codex-custom",
			"openai-beta":   "responses=experimental",
			"authorization": "Bearer attacker",
			"x-empty":       "",
		},
	}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: credentials}
	headers := http.Header{
		"User-Agent":    []string{"client"},
		"user-agent":    []string{"duplicate"},
		"Authorization": []string{"Bearer real-token"},
	}

	account.ApplyHeaderOverrides(headers)

	require.Equal(t, "codex-custom", headers.Get("User-Agent"))
	require.Equal(t, []string{"codex-custom"}, headers.Values("User-Agent"))
	require.Equal(t, "responses=experimental", headers.Get("OpenAI-Beta"))
	require.Equal(t, "Bearer real-token", headers.Get("Authorization"))
	require.Empty(t, headers.Get("X-Empty"))

	oauthAccount := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: credentials}
	oauthHeaders := http.Header{"User-Agent": []string{"client"}}
	oauthAccount.ApplyHeaderOverrides(oauthHeaders)
	require.Equal(t, "client", oauthHeaders.Get("User-Agent"))
}
