package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

type fakeGrokOAuthClient struct {
	exchangeCalls int
	exchangeErr   error
	idToken       string
}

func (c *fakeGrokOAuthClient) ExchangeCode(context.Context, string, string, string, string, string, string) (*xai.TokenResponse, error) {
	c.exchangeCalls++
	if c.exchangeErr != nil {
		return nil, c.exchangeErr
	}
	return &xai.TokenResponse{
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		IDToken:      c.idToken,
		ExpiresIn:    3600,
		TokenType:    "Bearer",
	}, nil
}

func (c *fakeGrokOAuthClient) RefreshToken(context.Context, string, string, string) (*xai.TokenResponse, error) {
	return &xai.TokenResponse{AccessToken: "refreshed-token", ExpiresIn: 3600}, nil
}

func TestGrokOAuthExchangeConsumesSessionBeforeUpstreamCall(t *testing.T) {
	client := &fakeGrokOAuthClient{exchangeErr: errors.New("temporary exchange failure")}
	service := NewGrokOAuthService(nil, client)
	t.Cleanup(service.Stop)

	authURL, err := service.GenerateAuthURL(context.Background(), nil, "")
	if err != nil {
		t.Fatalf("GenerateAuthURL() error = %v", err)
	}
	session, ok := service.sessionStore.Get(authURL.SessionID)
	if !ok || session.Nonce == "" {
		t.Fatal("expected OAuth session nonce")
	}
	input := &GrokExchangeCodeInput{SessionID: authURL.SessionID, Code: "authorization-code", State: authURL.State}

	if _, err := service.ExchangeCode(context.Background(), input); err == nil {
		t.Fatal("ExchangeCode() unexpectedly succeeded")
	}

	client.exchangeErr = nil
	if _, err := service.ExchangeCode(context.Background(), input); err == nil {
		t.Fatal("ExchangeCode() replay unexpectedly succeeded after failed exchange")
	}
	if client.exchangeCalls != 1 {
		t.Fatalf("exchange calls = %d, want 1", client.exchangeCalls)
	}
}

func TestGrokOAuthExchangeValidatesNonce(t *testing.T) {
	client := &fakeGrokOAuthClient{}
	service := NewGrokOAuthService(nil, client)
	t.Cleanup(service.Stop)
	authURL, err := service.GenerateAuthURL(context.Background(), nil, "")
	if err != nil {
		t.Fatalf("GenerateAuthURL() error = %v", err)
	}
	session, ok := service.sessionStore.Get(authURL.SessionID)
	if !ok {
		t.Fatal("expected OAuth session")
	}
	claims, _ := json.Marshal(map[string]string{"nonce": session.Nonce})
	client.idToken = "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
	info, err := service.ExchangeCode(context.Background(), &GrokExchangeCodeInput{SessionID: authURL.SessionID, Code: "authorization-code", State: authURL.State})
	if err != nil {
		t.Fatalf("ExchangeCode() error = %v", err)
	}
	if info == nil || info.AccessToken != "access-token" {
		t.Fatalf("ExchangeCode() info = %#v", info)
	}
}
