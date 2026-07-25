package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

func TestOpenAIOAuthService_BuildAccountCredentials_PreservesRefreshTokenWhenEmpty(t *testing.T) {
	svc := NewOpenAIOAuthService(nil, nil)
	creds := svc.BuildAccountCredentials(&OpenAITokenInfo{
		AccessToken:  "access-token",
		RefreshToken: "",
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
		ClientID:     openai.ChatGPTMobileClientID,
		PlanType:     "plus",
	})

	require.Equal(t, "access-token", creds["access_token"])
	require.NotContains(t, creds, "refresh_token")
	require.Equal(t, openai.ChatGPTMobileClientID, creds["client_id"])
	require.Equal(t, "plus", creds["plan_type"])
}

func TestSelectChatGPTAccountPlanType(t *testing.T) {
	accounts := map[string]any{
		"org-free": map[string]any{
			"account": map[string]any{
				"plan_type":  "free",
				"is_default": true,
			},
		},
		"org-plus": map[string]any{
			"account": map[string]any{
				"plan_type": "plus",
			},
		},
	}

	require.Equal(t, "plus", selectChatGPTAccountPlanType(accounts, "org-plus"))
	require.Equal(t, "free", selectChatGPTAccountPlanType(accounts, ""))
}

func TestSelectChatGPTAccountPlanType_EntitlementFallback(t *testing.T) {
	accounts := map[string]any{
		"acct": map[string]any{
			"account": map[string]any{
				"is_default": true,
			},
			"entitlement": map[string]any{
				"subscription_plan": "team",
			},
		},
	}

	require.Equal(t, "team", selectChatGPTAccountPlanType(accounts, "acct"))
}

func TestSelectChatGPTAccountPlanTypeSkipsInactiveWorkspaces(t *testing.T) {
	accounts := map[string]any{
		"expired-workspace": map[string]any{
			"account": map[string]any{
				"plan_type":  "self_serve_business_usage_based",
				"is_default": true,
			},
			"entitlement": map[string]any{
				"expires_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
			},
		},
		"deactivated-workspace": map[string]any{
			"account": map[string]any{
				"plan_type":      "team",
				"is_deactivated": true,
			},
		},
		"personal": map[string]any{
			"account": map[string]any{
				"plan_type": "pro",
			},
		},
	}

	require.Equal(t, "pro", selectChatGPTAccountPlanType(accounts, "expired-workspace"))
	require.Equal(t, "pro", selectChatGPTAccountPlanType(accounts, "deactivated-workspace"))
}

func TestOpenAIOAuthServiceExchangeCodePrefersIDTokenPlanType(t *testing.T) {
	svc := NewOpenAIOAuthService(nil, &fakeOpenAIOAuthClient{
		exchangeResp: &openai.TokenResponse{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			IDToken: makeOpenAIIDToken(t, map[string]any{
				"chatgpt_account_id": "account-id",
				"chatgpt_plan_type":  "pro",
			}),
			ExpiresIn: 3600,
		},
	})
	accountInfoCalls := 0
	svc.accountInfoClientFactory = func(proxyURL string) (*req.Client, error) {
		accountInfoCalls++
		return req.C(), nil
	}
	svc.sessionStore.Set("session-id", &openai.OAuthSession{
		CodeVerifier: "code-verifier",
		State:        "oauth-state",
		ClientID:     openai.ClientID,
		RedirectURI:  openai.DefaultRedirectURI,
		CreatedAt:    time.Now(),
	})

	tokenInfo, err := svc.ExchangeCode(context.Background(), &OpenAIExchangeCodeInput{
		SessionID: "session-id",
		Code:      "code",
		State:     "oauth-state",
	})

	require.NoError(t, err)
	require.Equal(t, "pro", tokenInfo.PlanType)
	require.Zero(t, accountInfoCalls)
}

func TestOpenAIOAuthService_ExchangeCodeEnrichesPlanType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Errorf("Authorization header = %q", got)
		}
		if got := r.Header.Get("Origin"); got != "https://chatgpt.com" {
			t.Errorf("Origin header = %q", got)
		}
		if got := r.Header.Get("Referer"); got != "https://chatgpt.com/" {
			t.Errorf("Referer header = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept header = %q", got)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"accounts":{"acct":{"account":{"plan_type":"plus","is_default":true}}}}`)
	}))
	defer server.Close()

	oldURL := chatGPTAccountsCheckURL
	chatGPTAccountsCheckURL = server.URL
	defer func() {
		chatGPTAccountsCheckURL = oldURL
	}()

	svc := NewOpenAIOAuthService(nil, &fakeOpenAIOAuthClient{
		exchangeResp: &openai.TokenResponse{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			ExpiresIn:    3600,
		},
	})
	svc.accountInfoClientFactory = func(proxyURL string) (*req.Client, error) {
		require.Equal(t, "http://proxy.local:8080", proxyURL)
		return req.C(), nil
	}
	svc.sessionStore.Set("session-id", &openai.OAuthSession{
		CodeVerifier: "code-verifier",
		State:        "oauth-state",
		ClientID:     openai.ChatGPTMobileClientID,
		RedirectURI:  openai.DefaultRedirectURI,
		ProxyURL:     "http://proxy.local:8080",
		CreatedAt:    time.Now(),
	})

	tokenInfo, err := svc.ExchangeCode(context.Background(), &OpenAIExchangeCodeInput{
		SessionID: "session-id",
		Code:      "code",
		State:     "oauth-state",
	})

	require.NoError(t, err)
	require.Equal(t, "plus", tokenInfo.PlanType)
}

func TestOpenAIOAuthService_ExchangeCodeRejectsStateMismatch(t *testing.T) {
	svc := NewOpenAIOAuthService(nil, &fakeOpenAIOAuthClient{
		exchangeResp: &openai.TokenResponse{AccessToken: "access-token", ExpiresIn: 3600},
	})
	svc.sessionStore.Set("session-id", &openai.OAuthSession{
		CodeVerifier: "code-verifier",
		State:        "expected-state",
		ClientID:     openai.ClientID,
		RedirectURI:  openai.DefaultRedirectURI,
		CreatedAt:    time.Now(),
	})

	_, err := svc.ExchangeCode(context.Background(), &OpenAIExchangeCodeInput{
		SessionID: "session-id",
		Code:      "code",
		State:     "wrong-state",
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "state mismatch")
}

type fakeOpenAIOAuthClient struct {
	exchangeResp *openai.TokenResponse
	refreshResp  *openai.TokenResponse
	err          error
}

func makeOpenAIIDToken(t *testing.T, authClaims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"email":                       "user@example.com",
		"https://api.openai.com/auth": authClaims,
	})
	require.NoError(t, err)
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func (c *fakeOpenAIOAuthClient) ExchangeCode(ctx context.Context, code, codeVerifier, redirectURI, proxyURL, clientID string) (*openai.TokenResponse, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.exchangeResp, nil
}

func (c *fakeOpenAIOAuthClient) RefreshToken(ctx context.Context, refreshToken, proxyURL string) (*openai.TokenResponse, error) {
	return c.RefreshTokenWithClientID(ctx, refreshToken, proxyURL, "")
}

func (c *fakeOpenAIOAuthClient) RefreshTokenWithClientID(ctx context.Context, refreshToken, proxyURL, clientID string) (*openai.TokenResponse, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.refreshResp, nil
}
