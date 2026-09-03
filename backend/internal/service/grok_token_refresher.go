package service

import (
	"context"
	"errors"
	"strings"
	"time"
)

type GrokTokenRefresher struct{ grokOAuthService *GrokOAuthService }

func NewGrokTokenRefresher(service *GrokOAuthService) *GrokTokenRefresher {
	return &GrokTokenRefresher{grokOAuthService: service}
}

func (r *GrokTokenRefresher) CanRefresh(account *Account) bool {
	return account != nil && account.IsGrokOAuth()
}

func (r *GrokTokenRefresher) NeedsRefresh(account *Account, refreshWindow time.Duration) bool {
	if account == nil || strings.TrimSpace(account.GetGrokRefreshToken()) == "" {
		return false
	}
	expiresAt := account.GetCredentialAsTime("expires_at")
	return expiresAt == nil || time.Until(*expiresAt) < refreshWindow
}

func (r *GrokTokenRefresher) Refresh(ctx context.Context, account *Account) (map[string]any, error) {
	if r == nil || r.grokOAuthService == nil {
		return nil, errors.New("grok oauth service is not configured")
	}
	info, err := r.grokOAuthService.RefreshAccountToken(ctx, account)
	if err != nil {
		return nil, err
	}
	creds := r.grokOAuthService.BuildAccountCredentials(info)
	for k, v := range account.Credentials {
		if _, ok := creds[k]; !ok {
			creds[k] = v
		}
	}
	return creds, nil
}

func (r *GrokTokenRefresher) CacheKey(account *Account) string { return GrokTokenCacheKey(account) }
