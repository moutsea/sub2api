package service

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
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

const antigravityRecoveryInterval = 15 * time.Minute

// Start starts background recovery tasks
func (p *AntigravityTokenProvider) Start() {
	log.Println("[AntigravityToken] Starting background recovery tasks...")
	go p.recoveryLoop()
}

// Stop stops background recovery tasks
func (p *AntigravityTokenProvider) Stop() {
	log.Println("[AntigravityToken] Stopping background recovery tasks...")
}

func (p *AntigravityTokenProvider) recoveryLoop() {
	ticker := time.NewTicker(antigravityRecoveryInterval)
	defer ticker.Stop()
	for range ticker.C {
		p.recoverAccounts()
	}
}

func (p *AntigravityTokenProvider) recoverAccounts() {
	if p.antigravityOAuthService == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	errorAccounts, err := p.accountRepo.ListErrorByPlatform(ctx, PlatformAntigravity)
	if err != nil {
		log.Printf("[AntigravityToken] Failed to query error accounts: %v", err)
		errorAccounts = nil
	}
	deletedAccounts, err := p.accountRepo.ListDeletedByPlatform(ctx, PlatformAntigravity)
	if err != nil {
		log.Printf("[AntigravityToken] Failed to query deleted accounts: %v", err)
		deletedAccounts = nil
	}
	if len(errorAccounts) == 0 && len(deletedAccounts) == 0 {
		return
	}

	recovered, failed := 0, 0

	for i := range errorAccounts {
		account := &errorAccounts[i]
		if p.tryRecoverAccount(ctx, account, false) {
			recovered++
		} else {
			failed++
		}
	}
	for i := range deletedAccounts {
		account := &deletedAccounts[i]
		if p.tryRecoverAccount(ctx, account, true) {
			recovered++
		} else {
			failed++
		}
	}

	log.Printf("[AntigravityToken] Recovery complete: recovered=%d failed=%d (error=%d deleted=%d)",
		recovered, failed, len(errorAccounts), len(deletedAccounts))
}

func (p *AntigravityTokenProvider) tryRecoverAccount(ctx context.Context, account *Account, isDeleted bool) bool {
	if account.Type != AccountTypeOAuth {
		return false
	}

	// 1. Refresh token
	tokenInfo, err := p.antigravityOAuthService.RefreshAccountToken(ctx, account)
	if err != nil {
		log.Printf("[AntigravityToken] Account %d (%s) refresh failed: %v", account.ID, account.Name, err)
		return false
	}

	// 2. Test connection with refreshed token
	if err := p.testConnection(ctx, account, tokenInfo.AccessToken); err != nil {
		log.Printf("[AntigravityToken] Account %d (%s) test failed: %v", account.ID, account.Name, err)
		return false
	}

	// 3. Restore account + update credentials in one step
	newCredentials := p.antigravityOAuthService.BuildAccountCredentials(tokenInfo)
	for k, v := range account.Credentials {
		if _, exists := newCredentials[k]; !exists {
			newCredentials[k] = v
		}
	}
	account.Credentials = newCredentials

	if isDeleted {
		if err := p.accountRepo.RestoreAccount(ctx, account.ID); err != nil {
			log.Printf("[AntigravityToken] Account %d (%s) restore failed: %v", account.ID, account.Name, err)
			return false
		}
		// Update credentials after restore
		if err := p.accountRepo.Update(ctx, account); err != nil {
			log.Printf("[AntigravityToken] Account %d (%s) credential update failed: %v", account.ID, account.Name, err)
		}
	} else {
		account.Status = StatusActive
		account.ErrorMessage = ""
		if err := p.accountRepo.Update(ctx, account); err != nil {
			log.Printf("[AntigravityToken] Account %d (%s) update failed: %v", account.ID, account.Name, err)
			return false
		}
	}

	label := "error"
	if isDeleted {
		label = "deleted"
	}
	log.Printf("[AntigravityToken] Account %d (%s) recovered from %s state", account.ID, account.Name, label)
	return true
}

// testConnection sends a minimal request to verify the account is usable
func (p *AntigravityTokenProvider) testConnection(ctx context.Context, account *Account, accessToken string) error {
	projectID := strings.TrimSpace(account.GetCredential("project_id"))
	if projectID == "" {
		projectID = antigravityDefaultProjectID
	}

	payload := map[string]any{
		"contents": []map[string]any{
			{"role": "user", "parts": []map[string]any{{"text": "hi"}}},
		},
		"systemInstruction": map[string]any{
			"parts": []map[string]any{{"text": antigravity.GetDefaultIdentityPatch()}},
		},
	}
	payloadBytes, _ := json.Marshal(payload)

	wrapped := map[string]any{
		"project":     projectID,
		"requestId":   "recovery-test",
		"userAgent":   "antigravity",
		"requestType": "agent",
		"model":       "claude-sonnet-4-5",
		"request":     json.RawMessage(payloadBytes),
	}
	body, _ := json.Marshal(wrapped)

	req, err := antigravity.NewAPIRequest(ctx, "streamGenerateContent", accessToken, body)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 30 * time.Second}
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL := account.Proxy.URL()
		if proxyURL != "" {
			if pu, err := url.Parse(proxyURL); err == nil {
				client.Transport = &http.Transport{
					Proxy:           http.ProxyURL(pu),
					TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
				}
			}
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("upstream returned %d", resp.StatusCode)
	}
	return nil
}
