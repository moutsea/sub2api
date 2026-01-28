package service

import (
	"context"
	"errors"
	"log"
	"strconv"
	"strings"
	"time"
)

const (
	antigravityTokenRefreshSkew = 3 * time.Minute
	antigravityTokenCacheSkew   = 5 * time.Minute
)

// AntigravityTokenCache Token 缓存接口（复用 GeminiTokenCache 接口定义）
type AntigravityTokenCache = GeminiTokenCache

// AntigravityTokenProvider 管理 Antigravity 账户的 access_token
type AntigravityTokenProvider struct {
	accountRepo             AccountRepository
	tokenCache              AntigravityTokenCache
	antigravityOAuthService *AntigravityOAuthService
}

func NewAntigravityTokenProvider(
	accountRepo AccountRepository,
	tokenCache AntigravityTokenCache,
	antigravityOAuthService *AntigravityOAuthService,
) *AntigravityTokenProvider {
	return &AntigravityTokenProvider{
		accountRepo:             accountRepo,
		tokenCache:              tokenCache,
		antigravityOAuthService: antigravityOAuthService,
	}
}

// GetAccessToken 获取有效的 access_token
func (p *AntigravityTokenProvider) GetAccessToken(ctx context.Context, account *Account) (string, error) {
	if account == nil {
		return "", errors.New("account is nil")
	}
	if account.Platform != PlatformAntigravity || account.Type != AccountTypeOAuth {
		return "", errors.New("not an antigravity oauth account")
	}

	cacheKey := AntigravityTokenCacheKey(account)

	// 1. 先尝试缓存
	if p.tokenCache != nil {
		if token, err := p.tokenCache.GetAccessToken(ctx, cacheKey); err == nil && strings.TrimSpace(token) != "" {
			return token, nil
		}
	}

	// 2. 如果即将过期则刷新
	expiresAt := account.GetCredentialAsTime("expires_at")
	needsRefresh := expiresAt == nil || time.Until(*expiresAt) <= antigravityTokenRefreshSkew
	if needsRefresh && p.tokenCache != nil {
		locked, err := p.tokenCache.AcquireRefreshLock(ctx, cacheKey, 30*time.Second)
		if err == nil && locked {
			defer func() { _ = p.tokenCache.ReleaseRefreshLock(ctx, cacheKey) }()

			// 拿到锁后再次检查缓存（另一个 worker 可能已刷新）
			if token, err := p.tokenCache.GetAccessToken(ctx, cacheKey); err == nil && strings.TrimSpace(token) != "" {
				return token, nil
			}

			// 从数据库获取最新账户信息
			fresh, err := p.accountRepo.GetByID(ctx, account.ID)
			if err == nil && fresh != nil {
				account = fresh
			}
			expiresAt = account.GetCredentialAsTime("expires_at")
			if expiresAt == nil || time.Until(*expiresAt) <= antigravityTokenRefreshSkew {
				if p.antigravityOAuthService == nil {
					return "", errors.New("antigravity oauth service not configured")
				}
				tokenInfo, err := p.antigravityOAuthService.RefreshAccountToken(ctx, account)
				if err != nil {
					return "", err
				}
				newCredentials := p.antigravityOAuthService.BuildAccountCredentials(tokenInfo)
				for k, v := range account.Credentials {
					if _, exists := newCredentials[k]; !exists {
						newCredentials[k] = v
					}
				}
				account.Credentials = newCredentials
				if updateErr := p.accountRepo.Update(ctx, account); updateErr != nil {
					log.Printf("[AntigravityTokenProvider] Failed to update account credentials: %v", updateErr)
				}
				expiresAt = account.GetCredentialAsTime("expires_at")
			}
		}
	}

	accessToken := account.GetCredential("access_token")
	if strings.TrimSpace(accessToken) == "" {
		return "", errors.New("access_token not found in credentials")
	}

	// 3. 存入缓存（检查 token 版本避免缓存竞态）
	if p.tokenCache != nil {
		// 检查当前内存中的 token 版本是否过时
		// 如果异步刷新服务已更新了数据库中的 token，则不应将旧 token 写入缓存
		var skipCache bool
		if p.accountRepo != nil {
			if dbAccount, err := p.accountRepo.GetByID(ctx, account.ID); err == nil && dbAccount != nil {
				dbVersion := dbAccount.GetTokenVersion()
				if account.IsTokenVersionStale(dbVersion) {
					log.Printf("[AntigravityTokenProvider] Skip cache write for stale token, account_id=%d, mem_version=%d, db_version=%d",
						account.ID, account.GetTokenVersion(), dbVersion)
					skipCache = true
				}
			}
		}

		if !skipCache {
			ttl := 30 * time.Minute
			if expiresAt != nil {
				until := time.Until(*expiresAt)
				switch {
				case until > antigravityTokenCacheSkew:
					ttl = until - antigravityTokenCacheSkew
				case until > 0:
					ttl = until
				default:
					ttl = time.Minute
				}
			}
			_ = p.tokenCache.SetAccessToken(ctx, cacheKey, accessToken, ttl)
		}
	}

	return accessToken, nil
}

func AntigravityTokenCacheKey(account *Account) string {
	projectID := strings.TrimSpace(account.GetCredential("project_id"))
	if projectID != "" {
		return "ag:" + projectID
	}
	return "ag:account:" + strconv.FormatInt(account.ID, 10)
}
