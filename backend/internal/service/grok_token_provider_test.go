package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

type grokProviderTokenCacheStub struct {
	mu      sync.Mutex
	tokens  map[string]string
	lock    bool
	lockErr error
}

func newGrokProviderTokenCacheStub(lock bool) *grokProviderTokenCacheStub {
	return &grokProviderTokenCacheStub{tokens: make(map[string]string), lock: lock}
}

func (s *grokProviderTokenCacheStub) GetAccessToken(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens[key], nil
}

func (s *grokProviderTokenCacheStub) SetAccessToken(_ context.Context, key, token string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[key] = token
	return nil
}

func (s *grokProviderTokenCacheStub) DeleteAccessToken(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, key)
	return nil
}

func (s *grokProviderTokenCacheStub) AcquireRefreshLock(context.Context, string, time.Duration) (bool, error) {
	return s.lock, s.lockErr
}

func (s *grokProviderTokenCacheStub) ReleaseRefreshLock(context.Context, string) error {
	return nil
}

type grokProviderOAuthClientStub struct {
	refreshCalls int
}

func (s *grokProviderOAuthClientStub) ExchangeCode(context.Context, string, string, string, string, string, string) (*xai.TokenResponse, error) {
	return nil, nil
}

func (s *grokProviderOAuthClientStub) RefreshToken(context.Context, string, string, string) (*xai.TokenResponse, error) {
	s.refreshCalls++
	return &xai.TokenResponse{AccessToken: "refreshed-token", RefreshToken: "refresh-token", ExpiresIn: 3600}, nil
}

func grokProviderAccount(expiresIn time.Duration) *Account {
	return &Account{
		ID:       701,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "db-token",
			"refresh_token": "refresh-token",
			"expires_at":    time.Now().Add(expiresIn).Format(time.RFC3339),
		},
	}
}

func TestGrokTokenProviderRejectsStaleTokenAfterRefreshLock(t *testing.T) {
	cache := newGrokProviderTokenCacheStub(true)
	account := grokProviderAccount(time.Minute)
	cache.tokens[GrokTokenCacheKey(account)] = "stale-token"
	oauthClient := &grokProviderOAuthClientStub{}
	oauthService := NewGrokOAuthService(nil, oauthClient)
	t.Cleanup(oauthService.Stop)
	provider := NewGrokTokenProvider(nil, cache, oauthService)

	token, err := provider.GetAccessToken(context.Background(), account)
	if err != nil {
		t.Fatalf("GetAccessToken() error = %v", err)
	}
	if token != "refreshed-token" {
		t.Fatalf("GetAccessToken() = %q, want refreshed token", token)
	}
	if oauthClient.refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want 1", oauthClient.refreshCalls)
	}
}

func TestGrokTokenProviderDoesNotReturnStaleTokenDuringRefreshRace(t *testing.T) {
	cache := newGrokProviderTokenCacheStub(false)
	account := grokProviderAccount(time.Minute)
	cache.tokens[GrokTokenCacheKey(account)] = "stale-token"
	provider := NewGrokTokenProvider(nil, cache, nil)

	token, err := provider.GetAccessToken(context.Background(), account)
	if err != nil {
		t.Fatalf("GetAccessToken() error = %v", err)
	}
	if token != "db-token" {
		t.Fatalf("GetAccessToken() = %q, want current account token", token)
	}
}
