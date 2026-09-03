package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

const grokTokenRefreshSkew = 5 * time.Minute

type GrokTokenCache = GeminiTokenCache

type GrokTokenProvider struct {
	accountRepo      AccountRepository
	tokenCache       GrokTokenCache
	grokOAuthService *GrokOAuthService
}

func NewGrokTokenProvider(accountRepo AccountRepository, tokenCache GrokTokenCache, oauthService *GrokOAuthService) *GrokTokenProvider {
	return &GrokTokenProvider{accountRepo: accountRepo, tokenCache: tokenCache, grokOAuthService: oauthService}
}

func usableGrokCachedToken(token string, account *Account) bool {
	if account == nil {
		return false
	}
	accountToken := strings.TrimSpace(account.GetGrokAccessToken())
	expiresAt := account.GetCredentialAsTime("expires_at")
	return strings.TrimSpace(token) != "" && accountToken != "" &&
		strings.TrimSpace(token) == accountToken && expiresAt != nil &&
		time.Until(*expiresAt) > grokTokenRefreshSkew
}

func (p *GrokTokenProvider) reloadAccount(ctx context.Context, account *Account) *Account {
	if account == nil || p == nil || p.accountRepo == nil {
		return account
	}
	if loaded, err := p.accountRepo.GetByID(ctx, account.ID); err == nil && loaded != nil {
		return loaded
	}
	return account
}

func (p *GrokTokenProvider) GetAccessToken(ctx context.Context, account *Account) (string, error) {
	if p == nil {
		return "", errors.New("grok token provider is not configured")
	}
	if account == nil || !account.IsGrokOAuth() {
		return "", errors.New("not a grok oauth account")
	}
	cacheKey := GrokTokenCacheKey(account)
	expiresAt := account.GetCredentialAsTime("expires_at")
	if p.tokenCache != nil {
		if token, err := p.tokenCache.GetAccessToken(ctx, cacheKey); err == nil {
			if usableGrokCachedToken(token, account) {
				return strings.TrimSpace(token), nil
			}
		}
	}

	needsRefresh := expiresAt == nil || time.Until(*expiresAt) <= grokTokenRefreshSkew
	if needsRefresh && strings.TrimSpace(account.GetGrokRefreshToken()) == "" && (expiresAt == nil || !time.Now().Before(*expiresAt)) {
		return "", errors.New("grok access_token expired and refresh_token is missing")
	}
	if needsRefresh && p.grokOAuthService != nil {
		shouldRefresh := p.tokenCache == nil
		if p.tokenCache != nil {
			locked, lockErr := p.tokenCache.AcquireRefreshLock(ctx, cacheKey, 30*time.Second)
			if lockErr != nil {
				// A cache outage should not make a still-valid token unusable. Fall
				// back to a single local refresh attempt, matching other providers.
				shouldRefresh = true
			} else if locked {
				shouldRefresh = true
				defer func() { _ = p.tokenCache.ReleaseRefreshLock(ctx, cacheKey) }()
				account = p.reloadAccount(ctx, account)
				expiresAt = account.GetCredentialAsTime("expires_at")
				if token, err := p.tokenCache.GetAccessToken(ctx, cacheKey); err == nil {
					if usableGrokCachedToken(token, account) {
						return strings.TrimSpace(token), nil
					}
				}
				needsRefresh = expiresAt == nil || time.Until(*expiresAt) <= grokTokenRefreshSkew
				shouldRefresh = needsRefresh
			} else {
				// Another worker is refreshing this account. Give it a brief chance
				// to publish the token rather than issuing a duplicate refresh.
				timer := time.NewTimer(100 * time.Millisecond)
				select {
				case <-timer.C:
				case <-ctx.Done():
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					return "", ctx.Err()
				}
				account = p.reloadAccount(ctx, account)
				expiresAt = account.GetCredentialAsTime("expires_at")
				if token, err := p.tokenCache.GetAccessToken(ctx, cacheKey); err == nil && usableGrokCachedToken(token, account) {
					return strings.TrimSpace(token), nil
				}
			}
		}
		if shouldRefresh {
			fresh := account
			if p.accountRepo != nil {
				if loaded, err := p.accountRepo.GetByID(ctx, account.ID); err == nil && loaded != nil {
					fresh = loaded
				}
			}
			if info, err := p.grokOAuthService.RefreshAccountToken(ctx, fresh); err == nil {
				creds := p.grokOAuthService.BuildAccountCredentials(info)
				for k, v := range fresh.Credentials {
					if _, ok := creds[k]; !ok {
						creds[k] = v
					}
				}
				fresh.Credentials = creds
				account = fresh
				expiresAt = account.GetCredentialAsTime("expires_at")
				if p.accountRepo != nil {
					_ = p.accountRepo.Update(ctx, account)
				}
			} else if expiresAt == nil || !time.Now().Before(*expiresAt) {
				return "", err
			}
		}
	}

	token := strings.TrimSpace(account.GetGrokAccessToken())
	if token == "" {
		return "", errors.New("access_token not found in credentials")
	}
	if expiresAt != nil && !time.Now().Before(*expiresAt) {
		return "", errors.New("grok access_token expired")
	}
	if p.tokenCache != nil {
		ttl := 30 * time.Minute
		if expiresAt != nil && time.Until(*expiresAt) > grokTokenRefreshSkew {
			ttl = time.Until(*expiresAt) - grokTokenRefreshSkew
		}
		_ = p.tokenCache.SetAccessToken(ctx, cacheKey, token, ttl)
	}
	return token, nil
}

func GrokTokenCacheKey(account *Account) string {
	if account == nil {
		return "grok:account:0"
	}
	return "grok:account:" + strconv.FormatInt(account.ID, 10)
}
