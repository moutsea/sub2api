package service

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const (
	// errorRecoveryInterval is how often we check error-state OAuth accounts
	// and attempt to recover them by refreshing their tokens.
	errorRecoveryInterval = 10 * time.Minute
)

// TokenRefreshService OAuth token自动刷新服务
// 定期检查并刷新即将过期的token
type TokenRefreshService struct {
	accountRepo      AccountRepository
	refreshers       []TokenRefresher
	cfg              *config.TokenRefreshConfig
	cacheInvalidator TokenCacheInvalidator

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewTokenRefreshService 创建token刷新服务
func NewTokenRefreshService(
	accountRepo AccountRepository,
	oauthService *OAuthService,
	openaiOAuthService *OpenAIOAuthService,
	geminiOAuthService *GeminiOAuthService,
	antigravityOAuthService *AntigravityOAuthService,
	cacheInvalidator TokenCacheInvalidator,
	cfg *config.Config,
	grokOAuthServices ...*GrokOAuthService,
) *TokenRefreshService {
	s := &TokenRefreshService{
		accountRepo:      accountRepo,
		cfg:              &cfg.TokenRefresh,
		cacheInvalidator: cacheInvalidator,
		stopCh:           make(chan struct{}),
	}

	// 注册平台特定的刷新器
	s.refreshers = []TokenRefresher{
		NewClaudeTokenRefresher(oauthService),
		NewOpenAITokenRefresher(openaiOAuthService),
		NewGeminiTokenRefresher(geminiOAuthService),
		NewAntigravityTokenRefresher(antigravityOAuthService),
	}
	if len(grokOAuthServices) > 0 && grokOAuthServices[0] != nil {
		s.refreshers = append(s.refreshers, NewGrokTokenRefresher(grokOAuthServices[0]))
	}

	return s
}

// Start 启动后台刷新服务
func (s *TokenRefreshService) Start() {
	if !s.cfg.Enabled {
		log.Println("[TokenRefresh] Service disabled by configuration")
		return
	}

	s.wg.Add(2)
	go s.refreshLoop()
	go s.errorRecoveryLoop()

	log.Printf("[TokenRefresh] Service started (check every %d minutes, refresh %v hours before expiry, error recovery every %v)",
		s.cfg.CheckIntervalMinutes, s.cfg.RefreshBeforeExpiryHours, errorRecoveryInterval)
}

// Stop 停止刷新服务
func (s *TokenRefreshService) Stop() {
	close(s.stopCh)
	s.wg.Wait()
	log.Println("[TokenRefresh] Service stopped")
}

// refreshLoop 刷新循环
func (s *TokenRefreshService) refreshLoop() {
	defer s.wg.Done()

	// 计算检查间隔
	checkInterval := time.Duration(s.cfg.CheckIntervalMinutes) * time.Minute
	if checkInterval < time.Minute {
		checkInterval = 5 * time.Minute
	}

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	// 启动时立即执行一次检查
	s.processRefresh()

	for {
		select {
		case <-ticker.C:
			s.processRefresh()
		case <-s.stopCh:
			return
		}
	}
}

// processRefresh 执行一次刷新检查
func (s *TokenRefreshService) processRefresh() {
	ctx := context.Background()

	// 计算刷新窗口
	refreshWindow := time.Duration(s.cfg.RefreshBeforeExpiryHours * float64(time.Hour))

	// 获取所有active状态的账号
	accounts, err := s.listActiveAccounts(ctx)
	if err != nil {
		log.Printf("[TokenRefresh] Failed to list accounts: %v", err)
		return
	}

	totalAccounts := len(accounts)
	oauthAccounts := 0 // 可刷新的OAuth账号数
	needsRefresh := 0  // 需要刷新的账号数
	refreshed, failed := 0, 0

	for i := range accounts {
		account := &accounts[i]

		// 遍历所有刷新器，找到能处理此账号的
		for _, refresher := range s.refreshers {
			if !refresher.CanRefresh(account) {
				continue
			}

			oauthAccounts++

			// 检查是否需要刷新
			if !refresher.NeedsRefresh(account, refreshWindow) {
				break // 不需要刷新，跳过
			}

			needsRefresh++

			// 执行刷新
			if err := s.refreshWithRetry(ctx, account, refresher); err != nil {
				log.Printf("[TokenRefresh] Account %d (%s) failed: %v", account.ID, account.Name, err)
				failed++
			} else {
				log.Printf("[TokenRefresh] Account %d (%s) refreshed successfully", account.ID, account.Name)
				refreshed++
			}

			// 每个账号只由一个refresher处理
			break
		}
	}

	// 始终打印周期日志，便于跟踪服务运行状态
	log.Printf("[TokenRefresh] Cycle complete: total=%d, oauth=%d, needs_refresh=%d, refreshed=%d, failed=%d",
		totalAccounts, oauthAccounts, needsRefresh, refreshed, failed)
}

// listActiveAccounts 获取所有active状态的账号
// 使用ListActive确保刷新所有活跃账号的token（包括临时禁用的）
func (s *TokenRefreshService) listActiveAccounts(ctx context.Context) ([]Account, error) {
	return s.accountRepo.ListActive(ctx)
}

// refreshWithRetry 带重试的刷新
func (s *TokenRefreshService) refreshWithRetry(ctx context.Context, account *Account, refresher TokenRefresher) error {
	var lastErr error

	for attempt := 1; attempt <= s.cfg.MaxRetries; attempt++ {
		newCredentials, err := refresher.Refresh(ctx, account)
		if err == nil {
			// 刷新成功，设置 token 版本号（毫秒级时间戳）用于防止缓存竞态
			newCredentials[TokenVersionKey] = strconv.FormatInt(time.Now().UnixMilli(), 10)

			// 更新账号 credentials
			account.Credentials = newCredentials
			if err := s.accountRepo.Update(ctx, account); err != nil {
				return fmt.Errorf("failed to save credentials: %w", err)
			}
			// 对所有 OAuth 账号调用缓存失效（InvalidateToken 内部根据平台判断是否需要处理）
			// 注意：先保存到数据库，再清除缓存，确保新版本号已持久化
			if s.cacheInvalidator != nil && account.Type == AccountTypeOAuth {
				if err := s.cacheInvalidator.InvalidateToken(ctx, account); err != nil {
					log.Printf("[TokenRefresh] Failed to invalidate token cache for account %d: %v", account.ID, err)
				} else {
					log.Printf("[TokenRefresh] Token cache invalidated for account %d", account.ID)
				}
			}
			return nil
		}

		// Antigravity 账户：不可重试错误直接标记 error 状态并返回
		if account.Platform == PlatformAntigravity && isNonRetryableRefreshError(err) {
			errorMsg := fmt.Sprintf("Token refresh failed (non-retryable): %v", err)
			if setErr := s.accountRepo.SetError(ctx, account.ID, errorMsg); setErr != nil {
				log.Printf("[TokenRefresh] Failed to set error status for account %d: %v", account.ID, setErr)
			}
			return err
		}

		lastErr = err
		log.Printf("[TokenRefresh] Account %d attempt %d/%d failed: %v",
			account.ID, attempt, s.cfg.MaxRetries, err)

		// 如果还有重试机会，等待后重试
		if attempt < s.cfg.MaxRetries {
			// 指数退避：2^(attempt-1) * baseSeconds
			backoff := time.Duration(s.cfg.RetryBackoffSeconds) * time.Second * time.Duration(1<<(attempt-1))
			time.Sleep(backoff)
		}
	}

	// Antigravity 账户：其他错误仅记录日志，不标记 error（可能是临时网络问题）
	// 其他平台账户：重试失败后标记 error
	if account.Platform == PlatformAntigravity {
		log.Printf("[TokenRefresh] Account %d: refresh failed after %d retries: %v", account.ID, s.cfg.MaxRetries, lastErr)
	} else {
		errorMsg := fmt.Sprintf("Token refresh failed after %d retries: %v", s.cfg.MaxRetries, lastErr)
		if err := s.accountRepo.SetError(ctx, account.ID, errorMsg); err != nil {
			log.Printf("[TokenRefresh] Failed to set error status for account %d: %v", account.ID, err)
		}
	}

	return lastErr
}

// isNonRetryableRefreshError 判断是否为不可重试的刷新错误
// 这些错误通常表示凭证已失效，需要用户重新授权
func isNonRetryableRefreshError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	nonRetryable := []string{
		"invalid_grant",       // refresh_token 已失效
		"invalid_client",      // 客户端配置错误
		"unauthorized_client", // 客户端未授权
		"access_denied",       // 访问被拒绝
	}
	for _, needle := range nonRetryable {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// errorRecoveryLoop periodically checks error-state OAuth accounts and attempts
// to recover them by refreshing their tokens. This prevents accounts from being
// permanently stuck in error state due to transient 401 errors.
// Modeled after Kiro's bannedRecoveryLoop.
func (s *TokenRefreshService) errorRecoveryLoop() {
	defer s.wg.Done()

	ticker := time.NewTicker(errorRecoveryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.recoverErrorAccounts()
		case <-s.stopCh:
			return
		}
	}
}

// recoverErrorAccounts queries error-state OAuth accounts from the database
// and attempts to refresh their tokens. On success, restores the account to active.
func (s *TokenRefreshService) recoverErrorAccounts() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Collect error accounts from all OAuth platforms
	platforms := []string{PlatformAnthropic, PlatformOpenAI, PlatformGemini, PlatformGrok}
	var errorAccounts []Account

	for _, platform := range platforms {
		accounts, err := s.accountRepo.ListErrorByPlatform(ctx, platform)
		if err != nil {
			log.Printf("[TokenRefresh] Failed to query error accounts for %s: %v", platform, err)
			continue
		}
		// Include OAuth and setup-token accounts (both use refresh_token for recovery)
		for _, a := range accounts {
			if a.Type == AccountTypeOAuth || a.Type == AccountTypeSetupToken {
				errorAccounts = append(errorAccounts, a)
			}
		}
	}

	if len(errorAccounts) == 0 {
		return
	}

	log.Printf("[TokenRefresh] Found %d error OAuth accounts, attempting recovery...", len(errorAccounts))
	recovered, failed, skipped := 0, 0, 0

	for i := range errorAccounts {
		account := &errorAccounts[i]

		// Find the matching refresher
		var refresher TokenRefresher
		for _, r := range s.refreshers {
			if r.CanRefresh(account) {
				refresher = r
				break
			}
		}
		if refresher == nil {
			skipped++
			continue
		}

		// Attempt refresh (single attempt, no retry — we'll try again next cycle)
		newCredentials, err := refresher.Refresh(ctx, account)
		if err != nil {
			if isNonRetryableRefreshError(err) {
				log.Printf("[TokenRefresh] Account %d (%s) recovery skipped (non-retryable): %v",
					account.ID, account.Name, err)
			} else {
				log.Printf("[TokenRefresh] Account %d (%s) recovery failed: %v",
					account.ID, account.Name, err)
			}
			failed++
			continue
		}

		// Refresh succeeded — update credentials and restore to active
		newCredentials[TokenVersionKey] = strconv.FormatInt(time.Now().UnixMilli(), 10)
		account.Credentials = newCredentials
		account.Status = StatusActive
		account.ErrorMessage = ""

		if err := s.accountRepo.Update(ctx, account); err != nil {
			log.Printf("[TokenRefresh] Account %d (%s) recovery update failed: %v",
				account.ID, account.Name, err)
			failed++
			continue
		}

		// Invalidate token cache so next request picks up the new token
		if s.cacheInvalidator != nil {
			if err := s.cacheInvalidator.InvalidateToken(ctx, account); err != nil {
				log.Printf("[TokenRefresh] Account %d cache invalidation failed: %v", account.ID, err)
			}
		}

		recovered++
		log.Printf("[TokenRefresh] Account %d (%s) recovered from error state", account.ID, account.Name)
	}

	log.Printf("[TokenRefresh] Error recovery complete: recovered=%d, failed=%d, skipped=%d, total=%d",
		recovered, failed, skipped, len(errorAccounts))
}
