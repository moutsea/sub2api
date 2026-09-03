package repository

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type grokOAuthClient struct{ tokenURL string }

func NewGrokOAuthClient() service.GrokOAuthClient {
	return &grokOAuthClient{tokenURL: xai.EffectiveTokenURL()}
}

func (c *grokOAuthClient) ExchangeCode(ctx context.Context, code, codeVerifier, codeChallenge, redirectURI, proxyURL, clientID string) (*xai.TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", effectiveGrokClientID(clientID))
	form.Set("code", code)
	form.Set("redirect_uri", xai.EffectiveRedirectURI(redirectURI))
	form.Set("code_verifier", codeVerifier)
	form.Set("code_challenge", codeChallenge)
	form.Set("code_challenge_method", "S256")
	return c.post(ctx, form, proxyURL, "grok oauth code exchange")
}

func (c *grokOAuthClient) RefreshToken(ctx context.Context, refreshToken, proxyURL, clientID string) (*xai.TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", effectiveGrokClientID(clientID))
	form.Set("refresh_token", refreshToken)
	return c.post(ctx, form, proxyURL, "grok oauth token refresh")
}

func (c *grokOAuthClient) post(ctx context.Context, form url.Values, proxyURL, operation string) (*xai.TokenResponse, error) {
	client := getSharedReqClient(reqClientOptions{ProxyURL: proxyURL, Timeout: 60 * time.Second})
	var token xai.TokenResponse
	resp, err := client.R().SetContext(ctx).SetHeader("User-Agent", "sub2api-grok-oauth/1.0").SetFormDataFromValues(form).SetSuccessResult(&token).Post(c.tokenURL)
	if err != nil {
		return nil, fmt.Errorf("%s request failed: %w", operation, err)
	}
	if resp == nil || !resp.IsSuccessState() {
		status := 0
		body := ""
		if resp != nil {
			status, body = resp.StatusCode, resp.String()
		}
		return nil, fmt.Errorf("%s failed: status %d body: %s", operation, status, body)
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return nil, fmt.Errorf("%s returned empty access_token", operation)
	}
	return &token, nil
}

func effectiveGrokClientID(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return xai.EffectiveClientID()
}
