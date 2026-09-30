package service

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	mathrand "math/rand"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
	"github.com/gin-gonic/gin"
)

const (
	// ChatGPT internal API for OAuth accounts
	chatgptCodexURL = "https://chatgpt.com/backend-api/codex/responses"
	// OpenAI Platform API for API Key accounts (fallback)
	openaiPlatformAPIURL   = "https://api.openai.com/v1/responses"
	openaiStickySessionTTL = time.Hour // 粘性会话TTL

	// openAI7dQuotaExhaustedThreshold 周（7d）窗口的额度耗尽阈值。
	// 5h 窗口用 quotaHealthyThreshold（80%）做提前预判是安全的，因为最多几小时就恢复；
	// 但 7d 窗口的 reset 时间可长达一周，80% 就停止调度会让账号白白闲置剩余的 20%。
	// 因此周窗口只在真正打满（100%）时才标记，中间状态由上游 429 兜底。
	openAI7dQuotaExhaustedThreshold = 100.0
)

type openAIRequestPlatformContextKey struct{}

// WithOpenAIRequestPlatform scopes OpenAI-compatible account selection to a
// concrete channel. Grok uses this when it shares the OpenAI protocol routes;
// leaving the value unset preserves the historical OpenAI/Kiro mixed pool.
func WithOpenAIRequestPlatform(ctx context.Context, platform string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAIRequestPlatformContextKey{}, strings.TrimSpace(platform))
}

func openAIRequestPlatform(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	platform, _ := ctx.Value(openAIRequestPlatformContextKey{}).(string)
	return strings.TrimSpace(platform)
}

func openAIAccountMatchesRequestPlatform(ctx context.Context, account *Account) bool {
	if account == nil || !isOpenAICompatAccount(account) {
		return false
	}
	platform := openAIRequestPlatform(ctx)
	if platform != "" {
		return account.Platform == platform
	}
	// The unscoped pool is the historical OpenAI/Kiro compatibility pool.
	// Grok must be selected only for an explicitly Grok-scoped request; its
	// Responses and quota semantics are not interchangeable with OpenAI/Kiro.
	return account.Platform == PlatformOpenAI || account.Platform == PlatformKiro
}

// openaiSSEDataRe matches SSE data lines with optional whitespace after colon.
// Some upstream APIs return non-standard "data:" without space (should be "data: ").
var openaiSSEDataRe = regexp.MustCompile(`^data:\s*`)

// OpenAI allowed headers whitelist (for non-OAuth accounts)
var openaiAllowedHeaders = map[string]bool{
	"accept-language": true,
	"content-type":    true,
	"conversation_id": true,
	"user-agent":      true,
	"originator":      true,
	"session_id":      true,
}

const grokMaxNonStreamingBodySize = 10 << 20

func isOpenAICompatModelSupportedByAccount(account *Account, requestedModel string) bool {
	if account == nil || !isOpenAICompatAccount(account) {
		return false
	}
	if account.Platform == PlatformGrok && account.Type != AccountTypeOAuth && account.Type != AccountTypeAPIKey {
		return false
	}
	if requestedModel == "" {
		return true
	}
	if account.Platform == PlatformKiro {
		return IsKiroModelSupportedByAccount(account, requestedModel)
	}
	if account.Platform == PlatformGrok {
		return isGrokModelSupportedByAccount(account, requestedModel)
	}
	return account.IsModelSupported(requestedModel)
}

func isOpenAICompatPlatform(platform string) bool {
	return platform == PlatformOpenAI || platform == PlatformKiro || platform == PlatformGrok
}

func isOpenAICompatAccount(account *Account) bool {
	return account != nil && isOpenAICompatPlatform(account.Platform)
}

func (s *OpenAIGatewayService) listOpenAICompatAccountsForModelCheck(ctx context.Context, groupID *int64) ([]Account, error) {
	if s.accountRepo == nil {
		return nil, nil
	}
	if groupID != nil && *groupID > 0 {
		all, err := s.accountRepo.ListByGroup(ctx, *groupID)
		if err != nil {
			return nil, err
		}
		candidates := make([]Account, 0, len(all))
		for i := range all {
			if openAIAccountMatchesRequestPlatform(ctx, &all[i]) {
				candidates = append(candidates, all[i])
			}
		}
		return candidates, nil
	}

	candidates := make([]Account, 0)
	platforms := []string{PlatformOpenAI, PlatformKiro}
	if requested := openAIRequestPlatform(ctx); requested == PlatformGrok {
		platforms = []string{PlatformGrok}
	}
	for _, platform := range platforms {
		accounts, err := s.accountRepo.ListByPlatform(ctx, platform)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, accounts...)
	}
	return candidates, nil
}

func (s *OpenAIGatewayService) errIfModelNotSupportedInOpenAICompatAccounts(ctx context.Context, groupID *int64, requestedModel string) error {
	if requestedModel == "" {
		return nil
	}
	accounts, err := s.listOpenAICompatAccountsForModelCheck(ctx, groupID)
	if err != nil {
		log.Printf("[OpenAI ModelRouting] model-support check skipped: group=%d model=%s err=%v", derefGroupID(groupID), requestedModel, err)
		return nil
	}
	if len(accounts) == 0 {
		return nil
	}
	for i := range accounts {
		if isOpenAICompatModelSupportedByAccount(&accounts[i], requestedModel) {
			return nil
		}
	}
	return fmt.Errorf("%s: %w", requestedModel, ErrModelNotSupported)
}

// OpenAICodexUsageSnapshot represents Codex API usage limits from response headers
type OpenAICodexUsageSnapshot struct {
	PrimaryUsedPercent          *float64 `json:"primary_used_percent,omitempty"`
	PrimaryResetAfterSeconds    *int     `json:"primary_reset_after_seconds,omitempty"`
	PrimaryWindowMinutes        *int     `json:"primary_window_minutes,omitempty"`
	SecondaryUsedPercent        *float64 `json:"secondary_used_percent,omitempty"`
	SecondaryResetAfterSeconds  *int     `json:"secondary_reset_after_seconds,omitempty"`
	SecondaryWindowMinutes      *int     `json:"secondary_window_minutes,omitempty"`
	PrimaryOverSecondaryPercent *float64 `json:"primary_over_secondary_percent,omitempty"`
	UpdatedAt                   string   `json:"updated_at,omitempty"`
}

// OpenAIUsage represents OpenAI API response usage
type OpenAIUsage struct {
	InputTokens              int     `json:"input_tokens"`
	OutputTokens             int     `json:"output_tokens"`
	CacheCreationInputTokens int     `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int     `json:"cache_read_input_tokens,omitempty"`
	CacheCreation5mTokens    int     `json:"-"`
	CacheCreation1hTokens    int     `json:"-"`
	ImageOutputTokens        int     `json:"image_output_tokens,omitempty"`
	ServiceTier              *string `json:"-"`
	ServiceTierPresent       bool    `json:"-"`
	ImageCount               int     `json:"-"`
	ImageSize                string  `json:"-"`
}

// OpenAIForwardResult represents the result of forwarding
type OpenAIForwardResult struct {
	RequestID    string
	Usage        OpenAIUsage
	Model        string
	ServiceTier  *string
	Stream       bool
	Duration     time.Duration
	FirstTokenMs *int
	ImageCount   int
	ImageSize    string
}

// OpenAIGatewayService handles OpenAI API gateway operations
type OpenAIGatewayService struct {
	accountRepo         AccountRepository
	usageLogRepo        UsageLogRepository
	userRepo            UserRepository
	userSubRepo         UserSubscriptionRepository
	cache               GatewayCache
	cfg                 *config.Config
	schedulerSnapshot   *SchedulerSnapshotService
	concurrencyService  *ConcurrencyService
	billingService      *BillingService
	rateLimitService    *RateLimitService
	billingCacheService *BillingCacheService
	httpUpstream        HTTPUpstream
	deferredService     *DeferredService
	openAITokenProvider *OpenAITokenProvider
	grokTokenProvider   *GrokTokenProvider
	toolCorrector       *CodexToolCorrector
	tempAPIKeyRepo      TempAPIKeyRepository
	usageCache          *UsageCache
	apiKeyRepo          APIKeyRepository
	apiKeyCacheInval    APIKeyAuthCacheInvalidator
	grokSnapshotMu      sync.Mutex
	grokSnapshotWrites  map[int64]grokSnapshotWriteState
}

// NewOpenAIGatewayService creates a new OpenAIGatewayService
func NewOpenAIGatewayService(
	accountRepo AccountRepository,
	usageLogRepo UsageLogRepository,
	userRepo UserRepository,
	userSubRepo UserSubscriptionRepository,
	cache GatewayCache,
	cfg *config.Config,
	schedulerSnapshot *SchedulerSnapshotService,
	concurrencyService *ConcurrencyService,
	billingService *BillingService,
	rateLimitService *RateLimitService,
	billingCacheService *BillingCacheService,
	httpUpstream HTTPUpstream,
	deferredService *DeferredService,
	openAITokenProvider *OpenAITokenProvider,
	grokTokenProvider *GrokTokenProvider,
	tempAPIKeyRepo TempAPIKeyRepository,
	usageCache *UsageCache,
	apiKeyRepo APIKeyRepository,
	apiKeyCacheInval APIKeyAuthCacheInvalidator,
) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		accountRepo:         accountRepo,
		usageLogRepo:        usageLogRepo,
		userRepo:            userRepo,
		userSubRepo:         userSubRepo,
		cache:               cache,
		cfg:                 cfg,
		schedulerSnapshot:   schedulerSnapshot,
		concurrencyService:  concurrencyService,
		billingService:      billingService,
		rateLimitService:    rateLimitService,
		billingCacheService: billingCacheService,
		httpUpstream:        httpUpstream,
		deferredService:     deferredService,
		openAITokenProvider: openAITokenProvider,
		grokTokenProvider:   grokTokenProvider,
		toolCorrector:       NewCodexToolCorrector(),
		tempAPIKeyRepo:      tempAPIKeyRepo,
		usageCache:          usageCache,
		apiKeyRepo:          apiKeyRepo,
		apiKeyCacheInval:    apiKeyCacheInval,
		grokSnapshotWrites:  make(map[int64]grokSnapshotWriteState),
	}
}

// GenerateSessionHash generates a sticky-session hash for OpenAI requests.
//
// Priority:
//  1. Header: session_id
//  2. Header: conversation_id
//  3. Body:   prompt_cache_key (opencode)
func (s *OpenAIGatewayService) GenerateSessionHash(c *gin.Context, reqBody map[string]any) string {
	if c == nil {
		return ""
	}

	sessionID := strings.TrimSpace(c.GetHeader("session_id"))
	if sessionID == "" {
		sessionID = strings.TrimSpace(c.GetHeader("conversation_id"))
	}
	if sessionID == "" && reqBody != nil {
		if v, ok := reqBody["prompt_cache_key"].(string); ok {
			sessionID = strings.TrimSpace(v)
		}
	}
	if sessionID == "" {
		return ""
	}

	hash := sha256.Sum256([]byte(sessionID))
	return hex.EncodeToString(hash[:])
}

func extractOpenAIServiceTier(reqBody map[string]any) *string {
	if reqBody == nil {
		return nil
	}
	raw, ok := reqBody["service_tier"].(string)
	if !ok {
		return nil
	}
	return normalizeOpenAIServiceTier(raw)
}

func normalizeOpenAIServiceTier(raw string) *string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return nil
	}
	if value == "fast" {
		value = "priority"
	}
	switch value {
	case "priority", "flex":
		return &value
	default:
		return nil
	}
}

func extractOpenAIServiceTierFromJSON(body []byte) (*string, bool) {
	if len(body) == 0 {
		return nil, false
	}

	var envelope struct {
		ServiceTier json.RawMessage `json:"service_tier"`
		Response    struct {
			ServiceTier json.RawMessage `json:"service_tier"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, false
	}

	if len(envelope.Response.ServiceTier) > 0 {
		return normalizeOpenAIServiceTierFromRaw(envelope.Response.ServiceTier), true
	}
	if len(envelope.ServiceTier) > 0 {
		return normalizeOpenAIServiceTierFromRaw(envelope.ServiceTier), true
	}
	return nil, false
}

func normalizeOpenAIServiceTierFromRaw(raw json.RawMessage) *string {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	return normalizeOpenAIServiceTier(value)
}

// BindStickySession sets session -> account binding with standard TTL.
func (s *OpenAIGatewayService) BindStickySession(ctx context.Context, groupID *int64, sessionHash string, accountID int64) error {
	if sessionHash == "" || accountID <= 0 {
		return nil
	}
	return s.cache.SetSessionAccountID(ctx, derefGroupID(groupID), "openai:"+sessionHash, accountID, openaiStickySessionTTL)
}

// InvalidateStickySession removes the sticky session binding so the next
// request with the same session hash will not be pinned to a failed account.
func (s *OpenAIGatewayService) InvalidateStickySession(ctx context.Context, groupID *int64, sessionHash string) {
	if sessionHash == "" || s.cache == nil {
		return
	}
	_ = s.cache.DeleteSessionAccountID(ctx, derefGroupID(groupID), "openai:"+sessionHash)
}

// SelectAccount selects an OpenAI account with sticky session support
func (s *OpenAIGatewayService) SelectAccount(ctx context.Context, groupID *int64, sessionHash string) (*Account, error) {
	return s.SelectAccountForModel(ctx, groupID, sessionHash, "")
}

// SelectAccountForModel selects an account supporting the requested model
func (s *OpenAIGatewayService) SelectAccountForModel(ctx context.Context, groupID *int64, sessionHash string, requestedModel string) (*Account, error) {
	return s.SelectAccountForModelWithExclusions(ctx, groupID, sessionHash, requestedModel, nil)
}

// SelectAccountForModelWithExclusions selects an account supporting the requested model while excluding specified accounts.
func (s *OpenAIGatewayService) SelectAccountForModelWithExclusions(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}) (*Account, error) {
	// 1. Check sticky session
	if sessionHash != "" {
		accountID, err := s.cache.GetSessionAccountID(ctx, derefGroupID(groupID), "openai:"+sessionHash)
		if err == nil && accountID > 0 {
			if _, excluded := excludedIDs[accountID]; !excluded {
				account, err := s.getSchedulableAccount(ctx, accountID)
				if err == nil && account.IsSchedulable() && openAIAccountMatchesRequestPlatform(ctx, account) && isOpenAICompatModelSupportedByAccount(account, requestedModel) &&
					(account.Platform == PlatformGrok || s.usageCache == nil || s.usageCache.IsOpenAIQuotaAvailable(accountID)) {
					// Refresh sticky session TTL
					_ = s.cache.RefreshSessionTTL(ctx, derefGroupID(groupID), "openai:"+sessionHash, openaiStickySessionTTL)
					return account, nil
				}
			}
		}
	}

	// 2. Get schedulable OpenAI accounts
	accounts, err := s.listSchedulableAccounts(ctx, groupID)
	if err != nil {
		return nil, fmt.Errorf("query accounts failed: %w", err)
	}

	// 3. Select by priority + LRU (soft-filter: quota-exhausted accounts as fallback)
	var selected *Account
	var quotaFallback *Account // 额度已满的最优候选，作为后备
	for i := range accounts {
		acc := &accounts[i]
		if !openAIAccountMatchesRequestPlatform(ctx, acc) {
			continue
		}
		if _, excluded := excludedIDs[acc.ID]; excluded {
			continue
		}
		// Scheduler snapshots can be temporarily stale; re-check schedulability here to
		// avoid selecting accounts that were recently rate-limited/overloaded.
		if !acc.IsSchedulable() {
			continue
		}
		// Check model support
		if !isOpenAICompatModelSupportedByAccount(acc, requestedModel) {
			continue
		}
		// Check OpenAI quota availability — soft filter with fallback
		if acc.Platform != PlatformGrok && s.usageCache != nil && !s.usageCache.IsOpenAIQuotaAvailable(acc.ID) {
			// Track best quota-exhausted account as fallback
			if quotaFallback == nil || acc.Priority < quotaFallback.Priority {
				quotaFallback = acc
			}
			continue
		}
		if selected == nil {
			selected = acc
			continue
		}
		// Lower priority value means higher priority
		if acc.Priority < selected.Priority {
			selected = acc
		} else if acc.Priority == selected.Priority {
			switch {
			case acc.LastUsedAt == nil && selected.LastUsedAt != nil:
				selected = acc
			case acc.LastUsedAt != nil && selected.LastUsedAt == nil:
				// keep selected (never used is preferred)
			case acc.LastUsedAt == nil && selected.LastUsedAt == nil:
				// keep selected (both never used)
			default:
				// Same priority, select least recently used
				if acc.LastUsedAt.Before(*selected.LastUsedAt) {
					selected = acc
				}
			}
		}
	}

	// Fallback to quota-exhausted account if no healthy accounts available
	if selected == nil && quotaFallback != nil {
		selected = quotaFallback
	}

	if selected == nil {
		if requestedModel != "" {
			if notSupportedErr := s.errIfModelNotSupportedInOpenAICompatAccounts(ctx, groupID, requestedModel); notSupportedErr != nil {
				return nil, notSupportedErr
			}
			return nil, fmt.Errorf("no available OpenAI accounts supporting model: %s", requestedModel)
		}
		return nil, errors.New("no available OpenAI accounts")
	}

	// 4. Set sticky session
	if sessionHash != "" {
		_ = s.cache.SetSessionAccountID(ctx, derefGroupID(groupID), "openai:"+sessionHash, selected.ID, openaiStickySessionTTL)
	}

	return selected, nil
}

// SelectAccountWithLoadAwareness selects an account with load-awareness and wait plan.
func (s *OpenAIGatewayService) SelectAccountWithLoadAwareness(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}) (*AccountSelectionResult, error) {
	cfg := s.schedulingConfig()
	var stickyAccountID int64
	if sessionHash != "" && s.cache != nil {
		if accountID, err := s.cache.GetSessionAccountID(ctx, derefGroupID(groupID), "openai:"+sessionHash); err == nil {
			stickyAccountID = accountID
		}
	}
	if s.concurrencyService == nil || !cfg.LoadBatchEnabled {
		account, err := s.SelectAccountForModelWithExclusions(ctx, groupID, sessionHash, requestedModel, excludedIDs)
		if err != nil {
			return nil, err
		}
		result, err := s.tryAcquireAccountSlot(ctx, account.ID, account.Concurrency)
		if err == nil && result.Acquired {
			return &AccountSelectionResult{
				Account:     account,
				Acquired:    true,
				ReleaseFunc: result.ReleaseFunc,
			}, nil
		}
		if stickyAccountID > 0 && stickyAccountID == account.ID && s.concurrencyService != nil {
			waitingCount, _ := s.concurrencyService.GetAccountWaitingCount(ctx, account.ID)
			if waitingCount < cfg.StickySessionMaxWaiting {
				return &AccountSelectionResult{
					Account: account,
					WaitPlan: &AccountWaitPlan{
						AccountID:      account.ID,
						MaxConcurrency: account.Concurrency,
						Timeout:        cfg.StickySessionWaitTimeout,
						MaxWaiting:     cfg.StickySessionMaxWaiting,
					},
				}, nil
			}
		}
		return &AccountSelectionResult{
			Account: account,
			WaitPlan: &AccountWaitPlan{
				AccountID:      account.ID,
				MaxConcurrency: account.Concurrency,
				Timeout:        cfg.FallbackWaitTimeout,
				MaxWaiting:     cfg.FallbackMaxWaiting,
			},
		}, nil
	}

	accounts, err := s.listSchedulableAccounts(ctx, groupID)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		if notSupportedErr := s.errIfModelNotSupportedInOpenAICompatAccounts(ctx, groupID, requestedModel); notSupportedErr != nil {
			return nil, notSupportedErr
		}
		return nil, errors.New("no available accounts")
	}

	isExcluded := func(accountID int64) bool {
		if excludedIDs == nil {
			return false
		}
		_, excluded := excludedIDs[accountID]
		return excluded
	}

	// ============ Layer 1: Sticky session ============
	if sessionHash != "" {
		accountID, err := s.cache.GetSessionAccountID(ctx, derefGroupID(groupID), "openai:"+sessionHash)
		if err == nil && accountID > 0 && !isExcluded(accountID) {
			account, err := s.getSchedulableAccount(ctx, accountID)
			if err == nil && account.IsSchedulable() && openAIAccountMatchesRequestPlatform(ctx, account) &&
				isOpenAICompatModelSupportedByAccount(account, requestedModel) &&
				(account.Platform == PlatformGrok || s.usageCache == nil || s.usageCache.IsOpenAIQuotaAvailable(accountID)) {
				result, err := s.tryAcquireAccountSlot(ctx, accountID, account.Concurrency)
				if err == nil && result.Acquired {
					_ = s.cache.RefreshSessionTTL(ctx, derefGroupID(groupID), "openai:"+sessionHash, openaiStickySessionTTL)
					return &AccountSelectionResult{
						Account:     account,
						Acquired:    true,
						ReleaseFunc: result.ReleaseFunc,
					}, nil
				}

				waitingCount, _ := s.concurrencyService.GetAccountWaitingCount(ctx, accountID)
				if waitingCount < cfg.StickySessionMaxWaiting {
					return &AccountSelectionResult{
						Account: account,
						WaitPlan: &AccountWaitPlan{
							AccountID:      accountID,
							MaxConcurrency: account.Concurrency,
							Timeout:        cfg.StickySessionWaitTimeout,
							MaxWaiting:     cfg.StickySessionMaxWaiting,
						},
					}, nil
				}
			}
		}
	}

	// ============ Layer 2: Load-aware selection ============
	candidates := make([]*Account, 0, len(accounts))
	var quotaExhaustedFallback []*Account // 额度已满的账号作为后备
	for i := range accounts {
		acc := &accounts[i]
		if !openAIAccountMatchesRequestPlatform(ctx, acc) {
			continue
		}
		if isExcluded(acc.ID) {
			continue
		}
		// Scheduler snapshots can be temporarily stale (bucket rebuild is throttled);
		// re-check schedulability here so recently rate-limited/overloaded accounts
		// are not selected again before the bucket is rebuilt.
		if !acc.IsSchedulable() {
			continue
		}
		if !isOpenAICompatModelSupportedByAccount(acc, requestedModel) {
			continue
		}
		// 软过滤：额度已满的账号放入 fallback 列表
		if acc.Platform != PlatformGrok && s.usageCache != nil && !s.usageCache.IsOpenAIQuotaAvailable(acc.ID) {
			quotaExhaustedFallback = append(quotaExhaustedFallback, acc)
			continue
		}
		candidates = append(candidates, acc)
	}

	// 如果所有健康账号不可用，使用额度已满的账号作为后备
	if len(candidates) == 0 && len(quotaExhaustedFallback) > 0 {
		candidates = quotaExhaustedFallback
	}

	if len(candidates) == 0 {
		if notSupportedErr := s.errIfModelNotSupportedInOpenAICompatAccounts(ctx, groupID, requestedModel); notSupportedErr != nil {
			return nil, notSupportedErr
		}
		return nil, errors.New("no available accounts")
	}

	accountLoads := make([]AccountWithConcurrency, 0, len(candidates))
	for _, acc := range candidates {
		accountLoads = append(accountLoads, AccountWithConcurrency{
			ID:             acc.ID,
			MaxConcurrency: acc.Concurrency,
		})
	}

	loadMap, err := s.concurrencyService.GetAccountsLoadBatch(ctx, accountLoads)
	if err != nil {
		ordered := append([]*Account(nil), candidates...)
		sortAccountsByPriorityAndLastUsedSimple(ordered, false)
		for _, acc := range ordered {
			result, err := s.tryAcquireAccountSlot(ctx, acc.ID, acc.Concurrency)
			if err == nil && result.Acquired {
				if sessionHash != "" {
					_ = s.cache.SetSessionAccountID(ctx, derefGroupID(groupID), "openai:"+sessionHash, acc.ID, openaiStickySessionTTL)
				}
				return &AccountSelectionResult{
					Account:     acc,
					Acquired:    true,
					ReleaseFunc: result.ReleaseFunc,
				}, nil
			}
		}
	} else {
		var available []accountWithLoad
		for _, acc := range candidates {
			loadInfo := loadMap[acc.ID]
			if loadInfo == nil {
				loadInfo = &AccountLoadInfo{AccountID: acc.ID}
			}
			if loadInfo.LoadRate < 100 {
				available = append(available, accountWithLoad{
					account:  acc,
					loadInfo: loadInfo,
				})
			}
		}

		if len(available) > 0 {
			s.sortOpenAIAccountWithLoadCandidates(available)

			for _, item := range available {
				result, err := s.tryAcquireAccountSlot(ctx, item.account.ID, item.account.Concurrency)
				if err == nil && result.Acquired {
					if sessionHash != "" {
						_ = s.cache.SetSessionAccountID(ctx, derefGroupID(groupID), "openai:"+sessionHash, item.account.ID, openaiStickySessionTTL)
					}
					return &AccountSelectionResult{
						Account:     item.account,
						Acquired:    true,
						ReleaseFunc: result.ReleaseFunc,
					}, nil
				}
			}
		}
	}

	// ============ Layer 3: Fallback wait ============
	sortAccountsByPriorityAndLastUsedSimple(candidates, false)
	for _, acc := range candidates {
		return &AccountSelectionResult{
			Account: acc,
			WaitPlan: &AccountWaitPlan{
				AccountID:      acc.ID,
				MaxConcurrency: acc.Concurrency,
				Timeout:        cfg.FallbackWaitTimeout,
				MaxWaiting:     cfg.FallbackMaxWaiting,
			},
		}, nil
	}

	return nil, errors.New("no available accounts")
}

func (s *OpenAIGatewayService) sortOpenAIAccountWithLoadCandidates(accounts []accountWithLoad) {
	if len(accounts) <= 1 {
		return
	}
	sort.SliceStable(accounts, func(i, j int) bool {
		a, b := accounts[i], accounts[j]
		if a.account.Priority != b.account.Priority {
			return a.account.Priority < b.account.Priority
		}
		return a.loadInfo.LoadRate < b.loadInfo.LoadRate
	})

	i := 0
	for i < len(accounts) {
		j := i + 1
		for j < len(accounts) && samePriorityLoadGroup(accounts[i], accounts[j]) {
			j++
		}
		s.orderOpenAIEqualPriorityLoadGroup(accounts[i:j])
		i = j
	}
}

func (s *OpenAIGatewayService) orderOpenAIEqualPriorityLoadGroup(accounts []accountWithLoad) {
	if len(accounts) <= 1 {
		return
	}
	if s.weightedShuffleOpenAIKiroCreditGroup(accounts) {
		return
	}
	sort.SliceStable(accounts, func(i, j int) bool {
		return s.openAIAccountResetAndLastUsedLess(accounts[i].account, accounts[j].account)
	})
	s.shuffleOpenAIWithinResetAndLastUsedGroups(accounts)
}

func (s *OpenAIGatewayService) weightedShuffleOpenAIKiroCreditGroup(accounts []accountWithLoad) bool {
	if len(accounts) <= 1 || s.usageCache == nil {
		return false
	}
	credits := make([]float64, len(accounts))
	creditsDiffer := false
	for i, item := range accounts {
		if item.account == nil || !item.account.IsKiro() {
			return false
		}
		credit := s.usageCache.GetKiroAvailableCredits(item.account.ID)
		if credit < 0 {
			return false
		}
		if i > 0 && credit != credits[0] {
			creditsDiffer = true
		}
		credits[i] = credit
	}
	if !creditsDiffer {
		return false
	}

	type weightedItem struct {
		item accountWithLoad
		key  float64
	}
	weighted := make([]weightedItem, len(accounts))
	for i, item := range accounts {
		weight := credits[i]
		if weight < 1 {
			weight = 1
		}
		u := mathrand.Float64()
		if u <= 0 {
			u = math.SmallestNonzeroFloat64
		}
		weighted[i] = weightedItem{
			item: item,
			key:  -math.Log(u) / weight,
		}
	}
	sort.Slice(weighted, func(i, j int) bool {
		return weighted[i].key < weighted[j].key
	})
	for i := range weighted {
		accounts[i] = weighted[i].item
	}
	return true
}

func (s *OpenAIGatewayService) openAIAccountResetAndLastUsedLess(a, b *Account) bool {
	aReset := s.openAIAccountResetTime(a.ID)
	bReset := s.openAIAccountResetTime(b.ID)
	switch {
	case aReset != nil && bReset != nil:
		if !aReset.Equal(*bReset) {
			return aReset.Before(*bReset)
		}
	case aReset != nil && bReset == nil:
		return true
	case aReset == nil && bReset != nil:
		return false
	}
	switch {
	case a.LastUsedAt == nil && b.LastUsedAt != nil:
		return true
	case a.LastUsedAt != nil && b.LastUsedAt == nil:
		return false
	case a.LastUsedAt == nil && b.LastUsedAt == nil:
		return false
	default:
		return a.LastUsedAt.Before(*b.LastUsedAt)
	}
}

func (s *OpenAIGatewayService) openAIAccountResetTime(accountID int64) *time.Time {
	if s.usageCache == nil {
		return nil
	}
	return s.usageCache.GetResetTime(accountID)
}

func (s *OpenAIGatewayService) shuffleOpenAIWithinResetAndLastUsedGroups(accounts []accountWithLoad) {
	if len(accounts) <= 1 {
		return
	}
	i := 0
	for i < len(accounts) {
		j := i + 1
		for j < len(accounts) && s.sameOpenAIResetAndLastUsedGroup(accounts[i].account, accounts[j].account) {
			j++
		}
		if j-i > 1 {
			mathrand.Shuffle(j-i, func(a, b int) {
				accounts[i+a], accounts[i+b] = accounts[i+b], accounts[i+a]
			})
		}
		i = j
	}
}

func (s *OpenAIGatewayService) sameOpenAIResetAndLastUsedGroup(a, b *Account) bool {
	aReset := s.openAIAccountResetTime(a.ID)
	bReset := s.openAIAccountResetTime(b.ID)
	switch {
	case aReset == nil && bReset == nil:
	case aReset == nil || bReset == nil:
		return false
	case !aReset.Equal(*bReset):
		return false
	}
	return sameLastUsedAt(a.LastUsedAt, b.LastUsedAt)
}

func (s *OpenAIGatewayService) listSchedulableAccounts(ctx context.Context, groupID *int64) ([]Account, error) {
	// Query both OpenAI and Kiro platform accounts.
	// Kiro accounts support OpenAI-compatible Chat Completions via format conversion.
	platforms := []string{PlatformOpenAI, PlatformKiro}
	if platform := openAIRequestPlatform(ctx); platform == PlatformGrok {
		platforms = []string{PlatformGrok}
	}

	var allAccounts []Account
	for _, platform := range platforms {
		var accounts []Account
		var err error
		if s.schedulerSnapshot != nil {
			accounts, _, err = s.schedulerSnapshot.ListSchedulableAccounts(ctx, groupID, platform, false)
			if err == nil && len(accounts) == 0 && platform == PlatformGrok && s.accountRepo != nil {
				accounts, err = s.listSchedulableGrokAccountsFromDB(ctx, groupID)
			}
		} else if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
			accounts, err = s.accountRepo.ListSchedulableByPlatform(ctx, platform)
		} else if groupID != nil {
			accounts, err = s.accountRepo.ListSchedulableByGroupIDAndPlatform(ctx, *groupID, platform)
		} else {
			accounts, err = s.accountRepo.ListSchedulableByPlatform(ctx, platform)
		}
		if err != nil {
			return nil, fmt.Errorf("query %s accounts failed: %w", platform, err)
		}
		allAccounts = append(allAccounts, accounts...)
	}
	return allAccounts, nil
}

func (s *OpenAIGatewayService) listSchedulableGrokAccountsFromDB(ctx context.Context, groupID *int64) ([]Account, error) {
	if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		return s.accountRepo.ListSchedulableByPlatform(ctx, PlatformGrok)
	}
	if groupID != nil && *groupID > 0 {
		return s.accountRepo.ListSchedulableByGroupIDAndPlatform(ctx, *groupID, PlatformGrok)
	}
	return s.accountRepo.ListSchedulableByPlatform(ctx, PlatformGrok)
}

func (s *OpenAIGatewayService) tryAcquireAccountSlot(ctx context.Context, accountID int64, maxConcurrency int) (*AcquireResult, error) {
	if s.concurrencyService == nil {
		return &AcquireResult{Acquired: true, ReleaseFunc: func() {}}, nil
	}
	return s.concurrencyService.AcquireAccountSlot(ctx, accountID, maxConcurrency)
}

func (s *OpenAIGatewayService) getSchedulableAccount(ctx context.Context, accountID int64) (*Account, error) {
	if s.schedulerSnapshot != nil {
		return s.schedulerSnapshot.GetAccount(ctx, accountID)
	}
	return s.accountRepo.GetByID(ctx, accountID)
}

func (s *OpenAIGatewayService) schedulingConfig() config.GatewaySchedulingConfig {
	if s.cfg != nil {
		return s.cfg.Gateway.Scheduling
	}
	return config.GatewaySchedulingConfig{
		StickySessionMaxWaiting:  3,
		StickySessionWaitTimeout: 45 * time.Second,
		FallbackWaitTimeout:      30 * time.Second,
		FallbackMaxWaiting:       100,
		LoadBatchEnabled:         true,
		SlotCleanupInterval:      30 * time.Second,
	}
}

// GetAccessToken gets the access token for an OpenAI account
func (s *OpenAIGatewayService) GetAccessToken(ctx context.Context, account *Account) (string, string, error) {
	if account == nil {
		return "", "", errors.New("account is nil")
	}
	if account.IsGrok() {
		if account.Type == AccountTypeAPIKey {
			apiKey := strings.TrimSpace(account.GetCredential("api_key"))
			if apiKey == "" {
				return "", "", errors.New("api_key not found in credentials")
			}
			return apiKey, "apikey", nil
		}
		if account.Type != AccountTypeOAuth {
			return "", "", fmt.Errorf("unsupported Grok account type: %s", account.Type)
		}
		if s.grokTokenProvider != nil {
			token, err := s.grokTokenProvider.GetAccessToken(ctx, account)
			if err != nil {
				return "", "", err
			}
			return token, "oauth", nil
		}
		token := strings.TrimSpace(account.GetGrokAccessToken())
		if token == "" {
			return "", "", errors.New("access_token not found in credentials")
		}
		return token, "oauth", nil
	}
	switch account.Type {
	case AccountTypeOAuth:
		// 使用 TokenProvider 获取缓存的 token
		if s.openAITokenProvider != nil {
			accessToken, err := s.openAITokenProvider.GetAccessToken(ctx, account)
			if err != nil {
				return "", "", err
			}
			return accessToken, "oauth", nil
		}
		// 降级：TokenProvider 未配置时直接从账号读取
		accessToken := account.GetOpenAIAccessToken()
		if accessToken == "" {
			return "", "", errors.New("access_token not found in credentials")
		}
		return accessToken, "oauth", nil
	case AccountTypeAPIKey:
		apiKey := account.GetOpenAIApiKey()
		if apiKey == "" {
			return "", "", errors.New("api_key not found in credentials")
		}
		return apiKey, "apikey", nil
	default:
		return "", "", fmt.Errorf("unsupported account type: %s", account.Type)
	}
}

func (s *OpenAIGatewayService) shouldFailoverUpstreamError(statusCode int) bool {
	switch statusCode {
	case 401, 402, 403, 429, 529:
		return true
	default:
		return statusCode >= 500
	}
}

func (s *OpenAIGatewayService) handleFailoverSideEffects(ctx context.Context, resp *http.Response, account *Account) {
	if s == nil || resp == nil || resp.Body == nil || s.rateLimitService == nil {
		return
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	s.rateLimitService.HandleUpstreamError(ctx, account, resp.StatusCode, resp.Header, body)
}

// Forward forwards request to OpenAI API
func (s *OpenAIGatewayService) Forward(ctx context.Context, c *gin.Context, account *Account, body []byte) (*OpenAIForwardResult, error) {
	startTime := time.Now()

	// Parse request body once (avoid multiple parse/serialize cycles)
	var reqBody map[string]any
	if err := json.Unmarshal(body, &reqBody); err != nil {
		return nil, fmt.Errorf("parse request: %w", err)
	}

	// Extract model and stream from parsed body
	reqModel, _ := reqBody["model"].(string)
	reqStream, _ := reqBody["stream"].(bool)
	if account != nil && account.IsGrok() {
		return s.forwardGrokResponses(ctx, c, account, body, reqModel, reqStream, startTime)
	}
	serviceTier := extractOpenAIServiceTier(reqBody)
	promptCacheKey := ""
	if v, ok := reqBody["prompt_cache_key"].(string); ok {
		promptCacheKey = strings.TrimSpace(v)
	}

	// Track if body needs re-serialization
	bodyModified := false
	originalModel := reqModel
	modelFallbackAttempted := false // OAuth 模型回退标记，防止无限重试
	injectedInstructions := ""
	if c != nil {
		if value, exists := c.Get(OpenAIInjectedInstructionsContextKey); exists {
			if instructions, ok := value.(string); ok {
				injectedInstructions = strings.TrimSpace(instructions)
			}
		}
	}

	isCodexCLI := openai.IsCodexCLIRequest(c.GetHeader("User-Agent"))
	apiKey := getAPIKeyFromGinContext(c)
	imageGenerationAllowed := GroupAllowsImageGeneration(nil)
	if apiKey != nil {
		imageGenerationAllowed = GroupAllowsImageGeneration(apiKey.Group)
	}
	codexImageGenerationBridgeEnabled := isCodexCLI && imageGenerationAllowed && s.isCodexImageGenerationBridgeEnabled(account)
	imageIntent := IsImageGenerationIntent(openAIResponsesEndpoint, reqModel, body)
	if imageIntent && !imageGenerationAllowed {
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"type": "permission_error", "message": ImageGenerationPermissionMessage()}})
		return nil, errors.New("image generation disabled for group")
	}
	imageBillingConfig := OpenAIResponsesImageBillingConfig{}

	// 对所有请求执行模型映射（包含 Codex CLI）。
	mappedModel := account.GetMappedModel(reqModel)
	if mappedModel != reqModel {
		log.Printf("[OpenAI] Model mapping applied: %s -> %s (account: %s, isCodexCLI: %v)", reqModel, mappedModel, account.Name, isCodexCLI)
		reqBody["model"] = mappedModel
		bodyModified = true
	}

	// 针对所有 OpenAI 账号执行 Codex 模型名规范化，确保上游识别一致。
	if model, ok := reqBody["model"].(string); ok {
		normalizedModel := normalizeCodexModel(model)
		if normalizedModel != "" && normalizedModel != model {
			// 从模型名后缀提取 reasoning effort（如 -xhigh），设置到 reasoning.effort 中。
			if effort := extractCodexModelEffort(model, normalizedModel); effort != "" {
				if _, hasReasoning := reqBody["reasoning"]; !hasReasoning {
					reqBody["reasoning"] = map[string]any{"effort": effort}
					bodyModified = true
				}
			}
			log.Printf("[OpenAI] Codex model normalization: %s -> %s (account: %s, type: %s, isCodexCLI: %v)",
				model, normalizedModel, account.Name, account.Type, isCodexCLI)
			reqBody["model"] = normalizedModel
			mappedModel = normalizedModel
			bodyModified = true
		}
	}

	// 规范化 reasoning.effort 参数（minimal -> none），与上游允许值对齐。
	if reasoning, ok := reqBody["reasoning"].(map[string]any); ok {
		if effort, ok := reasoning["effort"].(string); ok && effort == "minimal" {
			reasoning["effort"] = "none"
			bodyModified = true
			log.Printf("[OpenAI] Normalized reasoning.effort: minimal -> none (account: %s)", account.Name)
		}
	}

	if account.Type == AccountTypeOAuth && !isCodexCLI {
		codexResult := applyCodexOAuthTransform(reqBody)
		if codexResult.Modified {
			bodyModified = true
		}
		if codexResult.NormalizedModel != "" {
			mappedModel = codexResult.NormalizedModel
		}
		if codexResult.PromptCacheKey != "" {
			promptCacheKey = codexResult.PromptCacheKey
		}
		if codexResult.InjectedInstructions != "" {
			injectedInstructions = codexResult.InjectedInstructions
		}
	}

	// OAuth 账号走 ChatGPT internal API，该端点不再接受带 -codex 后缀的模型名。
	// 在所有规范化完成后统一去除 -codex，对 CodexCLI 和非 CodexCLI 均生效。
	if account.Type == AccountTypeOAuth {
		if model, ok := reqBody["model"].(string); ok {
			stripped := stripCodexModelSuffix(model)
			if stripped != model {
				log.Printf("[OpenAI] Strip -codex suffix for OAuth: %s -> %s (account: %s, isCodexCLI: %v)",
					model, stripped, account.Name, isCodexCLI)
				reqBody["model"] = stripped
				mappedModel = stripped
				bodyModified = true
			}
		}
	}

	// Strip unsupported fields that upstream APIs reject (e.g. Claude Code v2.1.22+ context_management)
	for _, unsupportedField := range []string{"context_management"} {
		if _, exists := reqBody[unsupportedField]; exists {
			delete(reqBody, unsupportedField)
			bodyModified = true
		}
	}

	if imageGenerationAllowed && (codexImageGenerationBridgeEnabled || openAIRequestBodyImageGenerationToolNeedsNormalization(body) || isOpenAIImageGenerationModel(mappedModel)) {
		if codexImageGenerationBridgeEnabled && ensureOpenAIResponsesImageGenerationTool(reqBody) {
			bodyModified = true
			log.Printf("[OpenAI] Injected /responses image_generation tool for Codex client")
		}
		if normalizeOpenAIResponsesImageGenerationTools(reqBody) {
			bodyModified = true
			log.Printf("[OpenAI] Normalized /responses image_generation tool payload")
		}
		if err := validateOpenAIResponsesImageModel(reqBody, mappedModel); err != nil {
			setOpsUpstreamError(c, http.StatusBadRequest, err.Error(), "")
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": err.Error(), "param": "model"}})
			return nil, err
		}
		if hasOpenAIImageGenerationTool(reqBody) {
			imageIntent = true
		}
		if codexImageGenerationBridgeEnabled && applyCodexImageGenerationBridgeInstructions(reqBody) {
			bodyModified = true
			log.Printf("[OpenAI] Added Codex image_generation bridge instructions")
		}
	}
	if imageIntent || hasOpenAIImageGenerationTool(reqBody) {
		if cfg, err := resolveOpenAIResponsesImageBillingConfigDetailed(reqBody, mappedModel); err == nil {
			imageBillingConfig = cfg
		}
	}
	// OAuth transform already handles its path; this also covers official OpenAI API key accounts.
	if shouldGuardOpenAIStatelessReasoning(account) {
		if ensureReasoningEncryptedContentInclude(reqBody) {
			bodyModified = true
		}
		if statelessErr := validateOpenAIStatelessInputReferences(reqBody); statelessErr != nil {
			writeOpenAIInvalidRequest(c, statelessErr.ClientMessage(), "input")
			return nil, statelessErr
		}
	}
	if normalizeOpenAIResponsesFunctionCallOutputImageURLs(reqBody) {
		bodyModified = true
		log.Printf("[OpenAI] Normalized invalid /responses function_call_output image_url payload")
	}

	// Handle max_output_tokens based on platform and account type
	if !isCodexCLI && account.Platform == PlatformOpenAI {
		if _, hasMaxOutputTokens := reqBody["max_output_tokens"]; hasMaxOutputTokens {
			// For OpenAI API Key, remove max_output_tokens (not supported).
			// OAuth accounts use the Responses API and keep it.
			if account.Type == AccountTypeAPIKey {
				delete(reqBody, "max_output_tokens")
				bodyModified = true
			}
		}

		// Also handle max_completion_tokens (similar logic)
		if _, hasMaxCompletionTokens := reqBody["max_completion_tokens"]; hasMaxCompletionTokens {
			if account.Type == AccountTypeAPIKey || account.Platform != PlatformOpenAI {
				delete(reqBody, "max_completion_tokens")
				bodyModified = true
			}
		}
	}

	// Re-serialize body only if modified
	if bodyModified {
		var err error
		body, err = json.Marshal(reqBody)
		if err != nil {
			return nil, fmt.Errorf("serialize request body: %w", err)
		}
	}

retryWithFallbackModel:
	preparedBody, err := prepareOpenAIEnvironmentContext(account, body, "input")
	if err != nil {
		writeOpenAIInvalidRequest(c, err.Error(), "instructions")
		return nil, err
	}
	body = preparedBody

	// Get access token
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}

	// Build upstream request
	requestCtx, releaseRequestCtx := ctx, func() {}
	if account.IsGrok() && !reqStream {
		requestCtx, releaseRequestCtx = grokUpstreamContext(ctx, false)
	}
	defer releaseRequestCtx()
	upstreamReq, err := s.buildUpstreamRequest(requestCtx, c, account, body, token, reqStream, promptCacheKey, isCodexCLI)
	if err != nil {
		return nil, err
	}

	// Get proxy URL
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	// Capture upstream request body for ops retry of this attempt.
	if c != nil {
		c.Set(OpsUpstreamRequestBodyKey, string(body))
	}

	// Send request
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		// Ensure the client receives an error response (handlers assume Forward writes on non-failover errors).
		safeErr := sanitizeUpstreamErrorMessage(err.Error())
		setOpsUpstreamError(c, 0, safeErr, "")
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: 0,
			Kind:               "request_error",
			Message:            safeErr,
		})
		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{
				"type":    "upstream_error",
				"message": "Upstream request failed",
			},
		})
		return nil, fmt.Errorf("upstream request failed: %s", safeErr)
	}
	if resp == nil || resp.Body == nil {
		err := errors.New("upstream request returned an empty response")
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{
			"type":    "upstream_error",
			"message": "Upstream request failed",
		}})
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	// Handle error response
	if resp.StatusCode >= 400 {
		if resp.StatusCode == http.StatusBadRequest {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			_ = resp.Body.Close()

			if capacityMsg := extractOpenAICapacityMessage(respBody); capacityMsg != "" {
				return nil, s.newOpenAICapacityFailoverError(
					ctx,
					c,
					account,
					resp.Header.Get("x-request-id"),
					capacityMsg,
					string(respBody),
				)
			}

			// OAuth 模型回退：上游返回 400 且错误信息指示模型不可用时，
			// 自动降级重试（同账号），如 gpt-5.4 → gpt-5.3，覆盖非 Plus 账号无高版本模型权限的场景。
			if account.Type == AccountTypeOAuth && !modelFallbackAttempted {
				upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(respBody))
				currentModel, _ := reqBody["model"].(string)
				fallbackModel := getOAuthModelFallback(currentModel)

				if fallbackModel != "" && isModelNotSupportedError(upstreamMsg) {
					// 对 OAuth 账号再做一次 -codex 剥离
					stripped := stripCodexModelSuffix(fallbackModel)
					log.Printf("[OpenAI] OAuth model fallback: %s -> %s (stripped: %s) upstream=%q (account: %s)",
						currentModel, fallbackModel, stripped, upstreamMsg, account.Name)

					reqBody["model"] = stripped
					mappedModel = stripped
					modelFallbackAttempted = true

					// 重新序列化并重试
					body, err = json.Marshal(reqBody)
					if err != nil {
						return nil, fmt.Errorf("serialize fallback request: %w", err)
					}
					goto retryWithFallbackModel
				}
			}

			resp.Body = io.NopCloser(bytes.NewReader(respBody))
			return s.handleErrorResponse(ctx, resp, c, account)
		}

		if s.shouldFailoverUpstreamError(resp.StatusCode) {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(respBody))

			upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(respBody))
			upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
			upstreamDetail := ""
			if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
				maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
				if maxBytes <= 0 {
					maxBytes = 2048
				}
				upstreamDetail = truncateString(string(respBody), maxBytes)
			}
			appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
				Platform:           account.Platform,
				AccountID:          account.ID,
				AccountName:        account.Name,
				UpstreamStatusCode: resp.StatusCode,
				UpstreamRequestID:  resp.Header.Get("x-request-id"),
				Kind:               "failover",
				Message:            upstreamMsg,
				Detail:             upstreamDetail,
			})

			s.handleFailoverSideEffects(ctx, resp, account)
			return nil, &UpstreamFailoverError{StatusCode: resp.StatusCode}
		}

		return s.handleErrorResponse(ctx, resp, c, account)
	}

	// Handle normal response

	var usage *OpenAIUsage
	var firstTokenMs *int
	if reqStream {
		streamResult, err := s.handleStreamingResponse(ctx, resp, c, account, startTime, originalModel, mappedModel, injectedInstructions)
		if err != nil {
			return nil, err
		}
		usage = streamResult.usage
		firstTokenMs = streamResult.firstTokenMs
	} else {
		usage, err = s.handleNonStreamingResponse(ctx, resp, c, account, originalModel, mappedModel, injectedInstructions)
		if err != nil {
			return nil, err
		}
	}
	resultModel := originalModel
	imageCount := usage.ImageCount
	imageSize := usage.ImageSize
	if imageCount > 0 {
		if strings.TrimSpace(imageBillingConfig.Model) != "" {
			resultModel = imageBillingConfig.Model
		}
		if strings.TrimSpace(imageBillingConfig.SizeTier) != "" {
			imageSize = imageBillingConfig.SizeTier
		}
	}

	// Extract and save Codex usage snapshot from response headers (for OAuth accounts)
	if account.Type == AccountTypeOAuth {
		if snapshot := extractCodexUsageHeaders(resp.Header); snapshot != nil {
			s.updateCodexUsageSnapshot(ctx, account.ID, snapshot)
		}
	}

	// Check rate limit headers for API Key accounts
	if account.Type == AccountTypeAPIKey {
		s.checkOpenAIRateLimitHeaders(account.ID, resp.Header)
	}

	return &OpenAIForwardResult{
		RequestID:    resp.Header.Get("x-request-id"),
		Usage:        *usage,
		Model:        resultModel,
		ServiceTier:  resolvedServiceTier(usage, serviceTier),
		Stream:       reqStream,
		Duration:     time.Since(startTime),
		FirstTokenMs: firstTokenMs,
		ImageCount:   imageCount,
		ImageSize:    imageSize,
	}, nil
}

func (s *OpenAIGatewayService) buildUpstreamRequest(ctx context.Context, c *gin.Context, account *Account, body []byte, token string, isStream bool, promptCacheKey string, isCodexCLI bool) (*http.Request, error) {
	// Determine target URL based on account type
	var targetURL string
	if account.IsGrok() {
		baseURL := account.GetGrokBaseURL()
		if s.cfg != nil {
			normalizedBaseURL, err := s.validateUpstreamBaseURL(baseURL)
			if err != nil {
				return nil, err
			}
			baseURL = normalizedBaseURL
		}
		targetURL = xai.BuildResponsesURL(baseURL)
	} else {
		switch account.Type {
		case AccountTypeOAuth:
			// OAuth accounts use ChatGPT internal API
			targetURL = chatgptCodexURL
		case AccountTypeAPIKey:
			// API Key accounts use Platform API or custom base URL
			baseURL := strings.TrimSpace(account.GetCredential("base_url"))
			if baseURL == "" {
				targetURL = openaiPlatformAPIURL
			} else {
				validatedURL, err := s.validateUpstreamBaseURL(baseURL)
				if err != nil {
					return nil, err
				}
				targetURL = validatedURL + "/responses"
			}
		default:
			targetURL = openaiPlatformAPIURL
		}
	}

	requestCtx := ctx
	if account.IsGrok() && isStream {
		requestCtx, _ = detachUpstreamContext(ctx)
	}
	req, err := http.NewRequestWithContext(requestCtx, "POST", targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	// Set authentication header
	req.Header.Set("authorization", "Bearer "+token)

	// Set headers specific to OpenAI OAuth accounts (ChatGPT internal API).
	// Grok OAuth also uses OAuth credentials, but targets the public xAI API.
	if account.Platform == PlatformOpenAI && account.Type == AccountTypeOAuth {
		// Required: set Host for ChatGPT API (must use req.Host, not Header.Set)
		req.Host = "chatgpt.com"
		// Required: set chatgpt-account-id header
		chatgptAccountID := account.GetChatGPTAccountID()
		if chatgptAccountID != "" {
			req.Header.Set("chatgpt-account-id", chatgptAccountID)
		}
	}
	if account.IsGrok() {
		if isStream {
			req.Header.Set("accept", "text/event-stream")
		} else {
			req.Header.Set("accept", "application/json")
		}
		if account.IsGrokOAuth() {
			xai.ApplyCLIProxyHeaders(req)
		}
	}

	// Whitelist passthrough headers
	if c != nil && c.Request != nil {
		for key, values := range c.Request.Header {
			lowerKey := strings.ToLower(key)
			if account.IsGrokOAuth() && lowerKey == "user-agent" {
				continue
			}
			if account.IsGrok() && (lowerKey == "session_id" || lowerKey == "conversation_id") {
				continue
			}
			if openaiAllowedHeaders[lowerKey] {
				for _, v := range values {
					req.Header.Add(key, v)
				}
			}
		}
	}
	if account.Platform == PlatformOpenAI && account.Type == AccountTypeOAuth {
		req.Header.Set("OpenAI-Beta", "responses=experimental")
		if isCodexCLI {
			req.Header.Set("originator", "codex_cli_rs")
		} else {
			req.Header.Set("originator", "opencode")
		}
		req.Header.Set("accept", "text/event-stream")
		if promptCacheKey != "" {
			req.Header.Set("conversation_id", promptCacheKey)
			req.Header.Set("session_id", promptCacheKey)
		}
	}

	// Apply custom User-Agent if configured. Grok OAuth must keep its CLI
	// identity headers final; forwarding a client/configured UA can make the xAI
	// CLI gateway reject an otherwise valid OAuth request.
	customUA := account.GetOpenAIUserAgent()
	if customUA != "" && !account.IsGrokOAuth() {
		req.Header.Set("user-agent", customUA)
	}

	// Ensure required headers exist
	if req.Header.Get("content-type") == "" {
		req.Header.Set("content-type", "application/json")
	}
	account.ApplyHeaderOverrides(req.Header)
	if account.IsGrokOAuth() {
		xai.ApplyCLIProxyHeaders(req)
	}

	return req, nil
}

func writeOpenAIInvalidRequest(c *gin.Context, message, param string) {
	if c == nil {
		return
	}
	payload := gin.H{
		"error": gin.H{
			"code":    nil,
			"message": message,
			"param":   param,
			"type":    "invalid_request_error",
		},
	}
	if c.Writer.Written() {
		c.SSEvent("error", payload)
		c.Writer.Flush()
		return
	}
	c.JSON(http.StatusBadRequest, payload)
}

func shouldGuardOpenAIStatelessReasoning(account *Account) bool {
	if account == nil {
		return true
	}
	if account.Platform != PlatformOpenAI {
		return false
	}
	if account.Type == AccountTypeOAuth {
		return true
	}
	if account.Type != AccountTypeAPIKey {
		return false
	}
	baseURL := strings.TrimSpace(account.GetOpenAIBaseURL())
	if baseURL == "" {
		return true
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Hostname(), "api.openai.com")
}

func (s *OpenAIGatewayService) handleErrorResponse(ctx context.Context, resp *http.Response, c *gin.Context, account *Account) (*OpenAIForwardResult, error) {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))

	upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(body))
	upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
	upstreamDetail := ""
	if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		upstreamDetail = truncateString(string(body), maxBytes)
	}
	setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)

	if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		log.Printf(
			"OpenAI upstream error %d (account=%d platform=%s type=%s): %s",
			resp.StatusCode,
			account.ID,
			account.Platform,
			account.Type,
			truncateForLog(body, s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes),
		)
	}

	// Check custom error codes
	if !account.ShouldHandleErrorCode(resp.StatusCode) {
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  resp.Header.Get("x-request-id"),
			Kind:               "http_error",
			Message:            upstreamMsg,
			Detail:             upstreamDetail,
		})
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": gin.H{
				"type":    "upstream_error",
				"message": "Upstream gateway error",
			},
		})
		if upstreamMsg == "" {
			return nil, fmt.Errorf("upstream error: %d (not in custom error codes)", resp.StatusCode)
		}
		return nil, fmt.Errorf("upstream error: %d (not in custom error codes) message=%s", resp.StatusCode, upstreamMsg)
	}

	// Handle upstream error (mark account status)
	shouldDisable := false
	if s.rateLimitService != nil {
		shouldDisable = s.rateLimitService.HandleUpstreamError(ctx, account, resp.StatusCode, resp.Header, body)
	}
	kind := "http_error"
	if shouldDisable {
		kind = "failover"
	}
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: resp.StatusCode,
		UpstreamRequestID:  resp.Header.Get("x-request-id"),
		Kind:               kind,
		Message:            upstreamMsg,
		Detail:             upstreamDetail,
	})
	if shouldDisable {
		return nil, &UpstreamFailoverError{StatusCode: resp.StatusCode}
	}

	// Return appropriate error response
	var errType, errMsg string
	var statusCode int

	switch resp.StatusCode {
	case 401:
		statusCode = http.StatusBadGateway
		errType = "upstream_error"
		errMsg = "Upstream authentication failed, please contact administrator"
	case 402:
		statusCode = http.StatusBadGateway
		errType = "upstream_error"
		errMsg = "Upstream payment required: insufficient balance or billing issue"
	case 403:
		statusCode = http.StatusBadGateway
		errType = "upstream_error"
		errMsg = "Upstream access forbidden, please contact administrator"
	case 429:
		statusCode = http.StatusTooManyRequests
		errType = "rate_limit_error"
		errMsg = "Upstream rate limit exceeded, please retry later"
	default:
		statusCode = http.StatusBadGateway
		errType = "upstream_error"
		errMsg = "Upstream request failed"
	}

	c.JSON(statusCode, gin.H{
		"error": gin.H{
			"type":    errType,
			"message": errMsg,
		},
	})

	if upstreamMsg == "" {
		return nil, fmt.Errorf("upstream error: %d", resp.StatusCode)
	}
	return nil, fmt.Errorf("upstream error: %d message=%s", resp.StatusCode, upstreamMsg)
}

// openaiStreamingResult streaming response result
type openaiStreamingResult struct {
	usage        *OpenAIUsage
	firstTokenMs *int
}

const OpenAIInjectedInstructionsContextKey = "openai_injected_instructions"

func (s *OpenAIGatewayService) handleStreamingResponse(ctx context.Context, resp *http.Response, c *gin.Context, account *Account, startTime time.Time, originalModel, mappedModel, injectedInstructions string) (*openaiStreamingResult, error) {
	if s.cfg != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.cfg.Security.ResponseHeaders)
	}

	// Set SSE response headers
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	// Pass through other headers
	if v := resp.Header.Get("x-request-id"); v != "" {
		c.Header("x-request-id", v)
	}

	w := c.Writer
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}

	usage := &OpenAIUsage{}
	imageCounter := newOpenAIImageOutputCounter()
	defer func() {
		applyOpenAIImageOutputAccounting(usage, imageCounter)
	}()
	var firstTokenMs *int
	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	scanner.Buffer(make([]byte, 64*1024), maxLineSize)

	// 独立 goroutine 读取上游，避免读取阻塞影响 keepalive/超时处理
	events := make(chan openAIStreamScanEvent, 16)
	done := make(chan struct{})
	sendEvent := func(ev openAIStreamScanEvent) bool {
		select {
		case events <- ev:
			return true
		case <-done:
			return false
		}
	}
	var lastReadAt int64
	atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
	go func() {
		defer close(events)
		for scanner.Scan() {
			atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
			if !sendEvent(openAIStreamScanEvent{line: scanner.Text()}) {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			_ = sendEvent(openAIStreamScanEvent{err: err})
		}
	}()
	defer close(done)

	streamInterval := time.Duration(0)
	configuredStreamInterval := 0
	if s.cfg != nil {
		configuredStreamInterval = s.cfg.Gateway.StreamDataIntervalTimeout
	}
	streamInterval = resolveGrokStreamIdleTimeout(configuredStreamInterval, account)
	if streamInterval == 0 && s.cfg != nil && s.cfg.Gateway.StreamDataIntervalTimeout > 0 {
		streamInterval = time.Duration(s.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}
	// 仅监控上游数据间隔超时，不被下游写入阻塞影响
	var intervalTicker *time.Ticker
	if streamInterval > 0 {
		intervalTicker = time.NewTicker(streamInterval)
		defer intervalTicker.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTicker != nil {
		intervalCh = intervalTicker.C
	}

	keepaliveInterval := time.Duration(0)
	if s.cfg != nil && s.cfg.Gateway.StreamKeepaliveInterval > 0 {
		keepaliveInterval = time.Duration(s.cfg.Gateway.StreamKeepaliveInterval) * time.Second
	}
	// 下游 keepalive 仅用于防止代理空闲断开
	var keepaliveTicker *time.Ticker
	if keepaliveInterval > 0 {
		keepaliveTicker = time.NewTicker(keepaliveInterval)
		defer keepaliveTicker.Stop()
	}
	var keepaliveCh <-chan time.Time
	if keepaliveTicker != nil {
		keepaliveCh = keepaliveTicker.C
	}
	// 记录上次收到上游数据的时间，用于控制 keepalive 发送频率
	lastDataAt := time.Now()

	// 仅发送一次错误事件，避免多次写入导致协议混乱（写失败时尽力通知客户端）
	errorEventSent := false
	sendErrorEvent := func(reason string) {
		if errorEventSent {
			return
		}
		errorEventSent = true
		_, _ = fmt.Fprintf(w, "event: error\ndata: {\"error\":\"%s\"}\n\n", reason)
		flusher.Flush()
	}

	responseModel := clientVisibleOpenAIModel(originalModel, mappedModel)
	currentEventType := ""
	processLine := func(line string) error {
		lastDataAt = time.Now()
		trimmedLine := strings.TrimSpace(line)
		if account != nil && account.IsGrok() && strings.HasPrefix(trimmedLine, "event:") {
			currentEventType = strings.TrimSpace(strings.TrimPrefix(trimmedLine, "event:"))
			return nil
		}

		if openaiSSEDataRe.MatchString(line) {
			line = s.sanitizeOpenAISSELine(line, mappedModel, responseModel, injectedInstructions)
			data := openaiSSEDataRe.ReplaceAllString(line, "")
			if account != nil && account.IsGrok() && currentEventType != "" && data != "" && data != "[DONE]" {
				var payload map[string]any
				if json.Unmarshal([]byte(data), &payload) == nil {
					if payloadType, _ := payload["type"].(string); strings.TrimSpace(payloadType) == "" {
						payload["type"] = currentEventType
						if encoded, err := json.Marshal(payload); err == nil {
							data = string(encoded)
							line = "data: " + data
						}
					}
				}
			}
			currentEventType = ""
			payloads := [][]byte{[]byte(data)}
			if account != nil && account.IsGrok() {
				var restoreErr error
				payloads, restoreErr = restoreGrokResponsesClientToolStreamPayload(c, []byte(data))
				if restoreErr != nil {
					return restoreErr
				}
			}
			for _, payload := range payloads {
				if len(payload) == 0 {
					continue
				}
				outputData := string(payload)
				if correctedData, corrected := s.toolCorrector.CorrectToolCallsInSSEData(outputData); corrected {
					outputData = correctedData
				}
				if _, err := fmt.Fprintf(w, "data: %s\n", outputData); err != nil {
					sendErrorEvent("write_failed")
					return err
				}
				flusher.Flush()

				if firstTokenMs == nil && outputData != "" && outputData != "[DONE]" {
					ms := int(time.Since(startTime).Milliseconds())
					firstTokenMs = &ms
				}
				imageCounter.AddSSEData([]byte(outputData))
				s.parseSSEUsage(outputData, usage, account != nil && account.IsGrok())
			}
			return nil
		}

		if _, err := fmt.Fprintf(w, "%s\n", line); err != nil {
			sendErrorEvent("write_failed")
			return err
		}
		flusher.Flush()
		return nil
	}

	if shouldPreReadOpenAICapacity(account) {
		bufferedEvents, capacityMsg, streamClosed, preErr := preReadOpenAIStreamEvents(ctx, events, streamInterval)
		switch {
		case errors.Is(preErr, errOpenAIInitialStreamTimeout):
			log.Printf("Stream data interval timeout: account=%d model=%s interval=%s", account.ID, originalModel, streamInterval)
			if s.rateLimitService != nil {
				s.rateLimitService.HandleStreamTimeout(ctx, account, originalModel)
			}
			sendErrorEvent("stream_timeout")
			if account != nil && account.IsGrok() {
				return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, grokStreamIdleFailoverError(account, streamInterval)
			}
			return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, preErr
		case preErr != nil:
			if errors.Is(preErr, bufio.ErrTooLong) {
				log.Printf("SSE line too long: account=%d max_size=%d error=%v", account.ID, maxLineSize, preErr)
				sendErrorEvent("response_too_large")
				return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, preErr
			}
			sendErrorEvent("stream_read_error")
			return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, fmt.Errorf("stream read error: %w", preErr)
		case capacityMsg != "":
			return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, s.newOpenAICapacityFailoverError(
				ctx,
				c,
				account,
				resp.Header.Get("x-request-id"),
				capacityMsg,
				capacityMsg,
			)
		case streamClosed:
			return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, nil
		default:
			for _, ev := range bufferedEvents {
				if err := processLine(ev.line); err != nil {
					return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, err
				}
			}
		}
	}

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, nil
			}
			if ev.err != nil {
				if errors.Is(ev.err, bufio.ErrTooLong) {
					log.Printf("SSE line too long: account=%d max_size=%d error=%v", account.ID, maxLineSize, ev.err)
					sendErrorEvent("response_too_large")
					return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, ev.err
				}
				sendErrorEvent("stream_read_error")
				return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, fmt.Errorf("stream read error: %w", ev.err)
			}
			if err := processLine(ev.line); err != nil {
				return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, err
			}

		case <-intervalCh:
			lastRead := time.Unix(0, atomic.LoadInt64(&lastReadAt))
			if time.Since(lastRead) < streamInterval {
				continue
			}
			log.Printf("Stream data interval timeout: account=%d model=%s interval=%s", account.ID, originalModel, streamInterval)
			// 处理流超时，可能标记账户为临时不可调度或错误状态
			if s.rateLimitService != nil {
				s.rateLimitService.HandleStreamTimeout(ctx, account, originalModel)
			}
			sendErrorEvent("stream_timeout")
			if account != nil && account.IsGrok() {
				return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, grokStreamIdleFailoverError(account, streamInterval)
			}
			return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, fmt.Errorf("stream data interval timeout")

		case <-keepaliveCh:
			if time.Since(lastDataAt) < keepaliveInterval {
				continue
			}
			if _, err := fmt.Fprint(w, ":\n\n"); err != nil {
				return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, err
			}
			flusher.Flush()
		}
	}

}

func (s *OpenAIGatewayService) replaceModelInSSELine(line, fromModel, toModel string) string {
	return s.sanitizeOpenAISSELine(line, fromModel, toModel, "")
}

func (s *OpenAIGatewayService) sanitizeOpenAISSELine(line, fromModel, toModel, injectedInstructions string) string {
	if !openaiSSEDataRe.MatchString(line) {
		return line
	}
	data := openaiSSEDataRe.ReplaceAllString(line, "")
	if data == "" || data == "[DONE]" {
		return line
	}

	var event map[string]any
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return line
	}

	changed := false
	if fromModel != "" && toModel != "" {
		if m, ok := event["model"].(string); ok && openAIResponseModelMatches(m, fromModel) {
			event["model"] = toModel
			changed = true
		}
	}
	if stripInjectedCodexInstructionsIfInjected(event, injectedInstructions) {
		changed = true
	}

	if response, ok := event["response"].(map[string]any); ok {
		if fromModel != "" && toModel != "" {
			if m, ok := response["model"].(string); ok && openAIResponseModelMatches(m, fromModel) {
				response["model"] = toModel
				changed = true
			}
		}
		if stripInjectedCodexInstructionsIfInjected(response, injectedInstructions) {
			changed = true
		}
	}

	if !changed {
		return line
	}

	newData, err := json.Marshal(event)
	if err != nil {
		return line
	}
	return "data: " + string(newData)
}

// correctToolCallsInResponseBody 修正响应体中的工具调用
func (s *OpenAIGatewayService) correctToolCallsInResponseBody(body []byte) []byte {
	if len(body) == 0 {
		return body
	}

	bodyStr := string(body)
	corrected, changed := s.toolCorrector.CorrectToolCallsInSSEData(bodyStr)
	if changed {
		return []byte(corrected)
	}
	return body
}

func (s *OpenAIGatewayService) parseSSEUsage(data string, usage *OpenAIUsage, independentReasoning ...bool) {
	s.parseSSEUsageBytes([]byte(data), usage, independentReasoning...)
}

func (s *OpenAIGatewayService) parseSSEUsageBytes(data []byte, usage *OpenAIUsage, independentReasoning ...bool) {
	if usage == nil || len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
		return
	}

	if parsedUsage, ok := extractOpenAIUsageFromJSONBytes(data, independentReasoning...); ok {
		usage.InputTokens = parsedUsage.InputTokens
		usage.OutputTokens = parsedUsage.OutputTokens
		usage.CacheCreationInputTokens = parsedUsage.CacheCreationInputTokens
		usage.CacheReadInputTokens = parsedUsage.CacheReadInputTokens
		usage.ImageOutputTokens = parsedUsage.ImageOutputTokens
	}

	if serviceTier, present := extractOpenAIServiceTierFromJSON(data); present {
		usage.ServiceTier = serviceTier
		usage.ServiceTierPresent = true
	}
}

func (s *OpenAIGatewayService) handleNonStreamingResponse(ctx context.Context, resp *http.Response, c *gin.Context, account *Account, originalModel, mappedModel, injectedInstructions string) (*OpenAIUsage, error) {
	reader := io.Reader(resp.Body)
	if account != nil && account.IsGrok() {
		reader = io.LimitReader(resp.Body, grokMaxNonStreamingBodySize+1)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if account != nil && account.IsGrok() && len(body) > grokMaxNonStreamingBodySize {
		return nil, fmt.Errorf("Grok non-streaming response exceeds %d bytes", grokMaxNonStreamingBodySize)
	}

	if account != nil && (account.Type == AccountTypeOAuth || account.IsGrok()) {
		bodyLooksLikeSSE := bytes.Contains(body, []byte("data:")) || bytes.Contains(body, []byte("event:"))
		if isEventStreamResponse(resp.Header) || bodyLooksLikeSSE {
			return s.handleOAuthSSEToJSON(ctx, resp, c, account, body, originalModel, mappedModel, injectedInstructions)
		}
	}
	if account != nil && account.IsGrok() {
		if restored, restoreErr := restoreGrokResponsesClientToolPayload(c, body); restoreErr == nil {
			body = restored
		}
	}

	usageValue, usageOK := extractOpenAIUsageFromJSONBytes(body, account != nil && account.IsGrok())
	if !usageOK {
		return nil, fmt.Errorf("parse response: invalid json response")
	}
	usage := &usageValue
	if serviceTier, present := extractOpenAIServiceTierFromJSON(body); present {
		usage.ServiceTier = serviceTier
		usage.ServiceTierPresent = true
	}

	body = s.stripInjectedInstructionsFromResponseBody(body, injectedInstructions)
	responseModel := clientVisibleOpenAIModel(originalModel, mappedModel)
	body = s.replaceModelInResponseBody(body, mappedModel, responseModel)
	applyOpenAIImageOutputAccountingFromJSON(usage, body)

	if s.cfg != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.cfg.Security.ResponseHeaders)
	}

	contentType := "application/json"
	if s.cfg != nil && !s.cfg.Security.ResponseHeaders.Enabled {
		if upstreamType := resp.Header.Get("Content-Type"); upstreamType != "" {
			contentType = upstreamType
		}
	}

	c.Data(resp.StatusCode, contentType, body)

	return usage, nil
}

func isEventStreamResponse(header http.Header) bool {
	contentType := strings.ToLower(header.Get("Content-Type"))
	return strings.Contains(contentType, "text/event-stream")
}

func (s *OpenAIGatewayService) handleOAuthSSEToJSON(ctx context.Context, resp *http.Response, c *gin.Context, account *Account, body []byte, originalModel, mappedModel, injectedInstructions string) (*OpenAIUsage, error) {
	bodyText := string(body)
	if capacityMsg := extractOpenAICapacityMessageFromSSEBody(bodyText); capacityMsg != "" {
		return nil, s.newOpenAICapacityFailoverError(
			ctx,
			c,
			account,
			resp.Header.Get("x-request-id"),
			capacityMsg,
			bodyText,
		)
	}
	finalResponse, ok := extractCodexFinalResponse(bodyText, injectedInstructions)

	usage := &OpenAIUsage{}
	if ok {
		if account != nil && account.IsGrok() {
			if restored, restoreErr := restoreGrokResponsesClientToolPayload(c, finalResponse); restoreErr == nil {
				finalResponse = restored
			}
		}
		if parsedUsage, parsed := extractOpenAIUsageFromJSONBytes(finalResponse, account != nil && account.IsGrok()); parsed {
			*usage = parsedUsage
		}
		if serviceTier, present := extractOpenAIServiceTierFromJSON(finalResponse); present {
			usage.ServiceTier = serviceTier
			usage.ServiceTierPresent = true
		}
		body = finalResponse
		body = s.replaceModelInResponseBody(body, mappedModel, clientVisibleOpenAIModel(originalModel, mappedModel))
		// Correct tool calls in final response
		body = s.correctToolCallsInResponseBody(body)
		applyOpenAIImageOutputAccountingFromJSON(usage, body)
	} else {
		usage = s.parseSSEUsageFromBody(bodyText, account != nil && account.IsGrok())
		bodyText = s.sanitizeOpenAISSEBody(bodyText, mappedModel, clientVisibleOpenAIModel(originalModel, mappedModel), injectedInstructions)
		body = []byte(bodyText)
	}

	if s.cfg != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.cfg.Security.ResponseHeaders)
	}

	contentType := "application/json; charset=utf-8"
	if !ok {
		contentType = resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "text/event-stream"
		}
	}
	c.Data(resp.StatusCode, contentType, body)

	return usage, nil
}

func resolvedServiceTier(usage *OpenAIUsage, requested *string) *string {
	if usage != nil && usage.ServiceTierPresent {
		return usage.ServiceTier
	}
	return requested
}

func extractCodexFinalResponse(body, injectedInstructions string) ([]byte, bool) {
	lines := strings.Split(body, "\n")
	outputItems := make(map[int]map[string]any)
	var finalResponse map[string]any

	for _, line := range lines {
		if !openaiSSEDataRe.MatchString(line) {
			continue
		}
		data := openaiSSEDataRe.ReplaceAllString(line, "")
		if data == "" || data == "[DONE]" {
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(data), &event) != nil {
			continue
		}

		eventType, _ := event["type"].(string)
		switch eventType {
		case "response.output_item.done":
			item, _ := event["item"].(map[string]any)
			if item == nil {
				continue
			}
			outputItems[jsonInt(event["output_index"])] = item
		case "response.done", "response.completed":
			response, _ := event["response"].(map[string]any)
			if response != nil {
				finalResponse = response
			}
		}
	}

	if finalResponse == nil {
		return nil, false
	}

	if shouldRebuildCodexResponseOutput(finalResponse["output"], len(outputItems)) {
		finalResponse["output"] = orderedCodexResponseOutputItems(outputItems)
	}
	stripInjectedCodexInstructions(finalResponse, injectedInstructions)

	finalBody, err := json.Marshal(finalResponse)
	if err != nil {
		return nil, false
	}

	return finalBody, true
}

func shouldRebuildCodexResponseOutput(existingOutput any, collectedCount int) bool {
	if collectedCount == 0 {
		return false
	}
	items, ok := existingOutput.([]any)
	return !ok || len(items) < collectedCount
}

func orderedCodexResponseOutputItems(outputItems map[int]map[string]any) []any {
	if len(outputItems) == 0 {
		return nil
	}

	indexes := make([]int, 0, len(outputItems))
	for index := range outputItems {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)

	ordered := make([]any, 0, len(indexes))
	for _, index := range indexes {
		ordered = append(ordered, outputItems[index])
	}
	return ordered
}

func stripInjectedCodexInstructions(response map[string]any, injectedInstructions string) {
	_ = stripInjectedCodexInstructionsIfInjected(response, injectedInstructions)
}

func stripInjectedCodexInstructionsIfInjected(response map[string]any, injectedInstructions string) bool {
	if response == nil {
		return false
	}
	injectedInstructions = strings.TrimSpace(injectedInstructions)
	if injectedInstructions == "" {
		return false
	}
	instructions, _ := response["instructions"].(string)
	instructions = strings.TrimSpace(instructions)
	if instructions == "" {
		return false
	}
	if instructions == injectedInstructions ||
		normalizeOpenAIEnvironmentText(instructions, "") == normalizeOpenAIEnvironmentText(injectedInstructions, "") {
		delete(response, "instructions")
		return true
	}
	return false
}

func (s *OpenAIGatewayService) stripInjectedInstructionsFromResponseBody(body []byte, injectedInstructions string) []byte {
	if strings.TrimSpace(injectedInstructions) == "" {
		return body
	}

	var response map[string]any
	if err := json.Unmarshal(body, &response); err != nil {
		return body
	}
	if !stripInjectedCodexInstructionsIfInjected(response, injectedInstructions) {
		return body
	}

	newBody, err := json.Marshal(response)
	if err != nil {
		return body
	}
	return newBody
}

func (s *OpenAIGatewayService) parseSSEUsageFromBody(body string, independentReasoning ...bool) *OpenAIUsage {
	usage := &OpenAIUsage{}
	imageCounter := newOpenAIImageOutputCounter()
	forEachOpenAISSEDataPayload(body, func(data []byte) {
		imageCounter.AddSSEData(data)
		s.parseSSEUsageBytes(data, usage, independentReasoning...)
	})
	applyOpenAIImageOutputAccounting(usage, imageCounter)
	return usage
}

func (s *OpenAIGatewayService) replaceModelInSSEBody(body, fromModel, toModel string) string {
	return s.sanitizeOpenAISSEBody(body, fromModel, toModel, "")
}

func (s *OpenAIGatewayService) sanitizeOpenAISSEBody(body, fromModel, toModel, injectedInstructions string) string {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if !openaiSSEDataRe.MatchString(line) {
			continue
		}
		lines[i] = s.sanitizeOpenAISSELine(line, fromModel, toModel, injectedInstructions)
	}
	return strings.Join(lines, "\n")
}

func (s *OpenAIGatewayService) validateUpstreamBaseURL(raw string) (string, error) {
	if s.cfg != nil && !s.cfg.Security.URLAllowlist.Enabled {
		normalized, err := urlvalidator.ValidateURLFormat(raw, s.cfg.Security.URLAllowlist.AllowInsecureHTTP)
		if err != nil {
			return "", fmt.Errorf("invalid base_url: %w", err)
		}
		return normalized, nil
	}
	normalized, err := urlvalidator.ValidateHTTPSURL(raw, urlvalidator.ValidationOptions{
		AllowedHosts:     s.cfg.Security.URLAllowlist.UpstreamHosts,
		RequireAllowlist: true,
		AllowPrivate:     s.cfg.Security.URLAllowlist.AllowPrivateHosts,
	})
	if err != nil {
		return "", fmt.Errorf("invalid base_url: %w", err)
	}
	return normalized, nil
}

func (s *OpenAIGatewayService) replaceModelInResponseBody(body []byte, fromModel, toModel string) []byte {
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		return body
	}

	model, ok := resp["model"].(string)
	if !ok || !openAIResponseModelMatches(model, fromModel) {
		return body
	}

	resp["model"] = toModel
	newBody, err := json.Marshal(resp)
	if err != nil {
		return body
	}

	return newBody
}

func clientVisibleOpenAIModel(originalModel, mappedModel string) string {
	if shouldExposeMappedOpenAIModel(originalModel, mappedModel) {
		return mappedModel
	}
	if originalModel != "" {
		return originalModel
	}
	return mappedModel
}

func shouldExposeMappedOpenAIModel(originalModel, mappedModel string) bool {
	originalModel = strings.ToLower(strings.TrimSpace(originalModel))
	mappedModel = strings.ToLower(strings.TrimSpace(mappedModel))
	if originalModel == "" || mappedModel == "" || originalModel == mappedModel {
		return false
	}
	return (strings.HasPrefix(mappedModel, "gpt-5.4-mini") && !strings.HasPrefix(originalModel, "gpt-5.4-mini")) ||
		(strings.HasPrefix(mappedModel, "gpt-5.2") && !strings.HasPrefix(originalModel, "gpt-5.2"))
}

func openAIResponseModelMatches(actualModel, expectedModel string) bool {
	actualModel = strings.TrimSpace(actualModel)
	expectedModel = strings.TrimSpace(expectedModel)
	if actualModel == "" || expectedModel == "" {
		return false
	}
	if actualModel == expectedModel {
		return true
	}
	normalizedActual := normalizeOpenAIResponseModel(actualModel)
	normalizedExpected := normalizeOpenAIResponseModel(expectedModel)
	if normalizedActual == expectedModel || (normalizedExpected != "" && normalizedActual == normalizedExpected) {
		return true
	}
	return (normalizedActual == "gpt-5.6-sol" && expectedModel == "gpt-5.6") ||
		(normalizedActual == "gpt-5.6" && expectedModel == "gpt-5.6-sol")
}

func normalizeOpenAIResponseModel(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return ""
	}
	switch {
	case hasOpenAIModelPrefix(model, "gpt-6.1-sol"):
		return "gpt-6.1-sol"
	case hasOpenAIModelPrefix(model, "gpt-6-astra"):
		return "gpt-6-astra"
	case hasOpenAIModelPrefix(model, "gpt-6-sol"):
		return "gpt-6-sol"
	case hasOpenAIModelPrefix(model, "gpt-6-luna"):
		return "gpt-6-luna"
	case hasOpenAIModelPrefix(model, "gpt-5.6-sol"):
		return "gpt-5.6-sol"
	case hasOpenAIModelPrefix(model, "gpt-5.6-terra"):
		return "gpt-5.6-terra"
	case hasOpenAIModelPrefix(model, "gpt-5.6-luna"):
		return "gpt-5.6-luna"
	case hasOpenAIModelPrefix(model, "gpt-5.6"):
		return "gpt-5.6"
	case hasOpenAIModelPrefix(model, "gpt-5.5"):
		return "gpt-5.5"
	case hasOpenAIModelPrefix(model, "gpt-5.4-mini"):
		return "gpt-5.4-mini"
	case hasOpenAIModelPrefix(model, "gpt-5.4"):
		return "gpt-5.4"
	case hasOpenAIModelPrefix(model, "gpt-5.3-codex-spark"):
		return "gpt-5.3-codex-spark"
	case hasOpenAIModelPrefix(model, "gpt-5.3-codex"):
		return "gpt-5.3-codex"
	case hasOpenAIModelPrefix(model, "gpt-5.3"):
		return "gpt-5.3-codex"
	case hasOpenAIModelPrefix(model, "gpt-5.2-codex"):
		return "gpt-5.2-codex"
	case hasOpenAIModelPrefix(model, "gpt-5.2"):
		return "gpt-5.2"
	case hasOpenAIModelPrefix(model, "gpt-5.1-codex-max"):
		return "gpt-5.2-codex"
	case hasOpenAIModelPrefix(model, "gpt-5.1-codex-mini"):
		return "gpt-5.2-codex"
	case hasOpenAIModelPrefix(model, "gpt-5.1-codex"):
		return "gpt-5.2-codex"
	case hasOpenAIModelPrefix(model, "gpt-5.1"):
		return "gpt-5.2"
	case hasOpenAIModelPrefix(model, "gpt-5-codex"):
		return "gpt-5.2-codex"
	case hasOpenAIModelPrefix(model, "gpt-5"):
		return "gpt-5.2"
	default:
		return ""
	}
}

func hasOpenAIModelPrefix(model, prefix string) bool {
	if !strings.HasPrefix(model, prefix) {
		return false
	}
	if len(model) == len(prefix) {
		return true
	}
	next := model[len(prefix)]
	switch next {
	case '-', '_', '/':
		return true
	default:
		return false
	}
}

// OpenAIRecordUsageInput input for recording usage
type OpenAIRecordUsageInput struct {
	Result       *OpenAIForwardResult
	APIKey       *APIKey
	User         *User
	Account      *Account
	Subscription *UserSubscription
	UserAgent    string // 请求的 User-Agent
	IPAddress    string // 请求的客户端 IP 地址
	TempAPIKeyID *int64 // 临时 API Key ID
	TempAPIKey   *TempAPIKey
}

// RecordUsage records usage and deducts balance
func (s *OpenAIGatewayService) RecordUsage(ctx context.Context, input *OpenAIRecordUsageInput) error {
	result := input.Result
	apiKey := input.APIKey
	user := input.User
	account := input.Account
	subscription := input.Subscription

	// Get rate multiplier: user custom > group default > global default
	multiplier := s.cfg.Default.RateMultiplier
	if apiKey.GroupID != nil && apiKey.Group != nil {
		multiplier = apiKey.Group.RateMultiplier
		// 用户自定义分组倍率覆盖
		if userRate, ok := user.GetGroupRateMultiplier(*apiKey.GroupID); ok {
			multiplier = userRate
		}
	}

	var (
		cost              *CostBreakdown
		actualInputTokens int
	)
	if result.ImageCount > 0 {
		var groupConfig *ImagePriceConfig
		if apiKey.Group != nil {
			groupConfig = &ImagePriceConfig{
				Price1K: apiKey.Group.ImagePrice1K,
				Price2K: apiKey.Group.ImagePrice2K,
				Price4K: apiKey.Group.ImagePrice4K,
			}
		}
		cost = s.billingService.CalculateImageCost(result.Model, result.ImageSize, result.ImageCount, groupConfig, multiplier)
	} else {
		actualInputTokens = openAIActualInputTokens(result, account)

		tokens := UsageTokens{
			InputTokens:           actualInputTokens,
			OutputTokens:          result.Usage.OutputTokens,
			CacheCreationTokens:   result.Usage.CacheCreationInputTokens,
			CacheReadTokens:       result.Usage.CacheReadInputTokens,
			CacheCreation5mTokens: result.Usage.CacheCreation5mTokens,
			CacheCreation1hTokens: result.Usage.CacheCreation1hTokens,
		}

		serviceTier := ""
		if result.ServiceTier != nil {
			serviceTier = strings.TrimSpace(*result.ServiceTier)
		}

		var err error
		cost, err = s.billingService.CalculateCostWithServiceTier(result.Model, tokens, multiplier, serviceTier)
		if err != nil {
			cost = &CostBreakdown{ActualCost: 0}
		}
	}

	// Determine billing type
	isSubscriptionBilling := subscription != nil && apiKey.Group != nil && apiKey.Group.IsSubscriptionType()
	billingType := BillingTypeBalance
	if isSubscriptionBilling {
		billingType = BillingTypeSubscription
	}

	// Create usage log
	durationMs := int(result.Duration.Milliseconds())
	accountRateMultiplier := account.BillingRateMultiplier()
	var apiKeyIDPtr *int64
	if apiKey.ID != 0 {
		apiKeyIDPtr = &apiKey.ID
	}
	var imageSize *string
	if result.ImageSize != "" {
		imageSize = &result.ImageSize
	}
	usageLog := &UsageLog{
		UserID:                user.ID,
		APIKeyID:              apiKeyIDPtr,
		AccountID:             account.ID,
		RequestID:             result.RequestID,
		Model:                 result.Model,
		InputTokens:           actualInputTokens,
		OutputTokens:          result.Usage.OutputTokens,
		CacheCreationTokens:   result.Usage.CacheCreationInputTokens,
		CacheCreation5mTokens: result.Usage.CacheCreation5mTokens,
		CacheCreation1hTokens: result.Usage.CacheCreation1hTokens,
		CacheReadTokens:       result.Usage.CacheReadInputTokens,
		InputCost:             cost.InputCost,
		OutputCost:            cost.OutputCost,
		CacheCreationCost:     cost.CacheCreationCost,
		CacheReadCost:         cost.CacheReadCost,
		TotalCost:             cost.TotalCost,
		ActualCost:            cost.ActualCost,
		RateMultiplier:        multiplier,
		AccountRateMultiplier: &accountRateMultiplier,
		BillingType:           billingType,
		Stream:                result.Stream,
		DurationMs:            &durationMs,
		FirstTokenMs:          result.FirstTokenMs,
		ImageCount:            result.ImageCount,
		ImageSize:             imageSize,
		CreatedAt:             time.Now(),
	}

	// 添加 UserAgent
	if input.UserAgent != "" {
		usageLog.UserAgent = &input.UserAgent
	}

	// 添加 IPAddress
	if input.IPAddress != "" {
		usageLog.IPAddress = &input.IPAddress
	}

	if apiKey.GroupID != nil {
		usageLog.GroupID = apiKey.GroupID
	}
	if subscription != nil {
		usageLog.SubscriptionID = &subscription.ID
	}
	if input.TempAPIKeyID != nil {
		usageLog.TempAPIKeyID = input.TempAPIKeyID
	}

	inserted, err := s.usageLogRepo.Create(ctx, usageLog)
	if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		log.Printf("[SIMPLE MODE] Usage recorded (not billed): user=%d, tokens=%d", usageLog.UserID, usageLog.TotalTokens())
		s.deferredService.ScheduleLastUsedUpdate(account.ID)
		return nil
	}

	shouldBill := inserted || err != nil

	// Deduct based on billing type
	if isSubscriptionBilling {
		if shouldBill && cost.TotalCost > 0 {
			_ = s.userSubRepo.IncrementUsage(ctx, subscription.ID, cost.TotalCost)
			s.billingCacheService.QueueUpdateSubscriptionUsage(user.ID, *apiKey.GroupID, cost.TotalCost)
		}
	} else {
		if shouldBill && cost.ActualCost > 0 {
			_ = s.userRepo.DeductBalance(ctx, user.ID, cost.ActualCost)
			s.billingCacheService.QueueDeductBalance(user.ID, cost.ActualCost)
		}
	}

	// 更新 quota_only 临时 API Key 的消费金额
	if input.TempAPIKey != nil && input.TempAPIKey.KeyType == TempAPIKeyTypeQuotaOnly && cost.ActualCost > 0 {
		if s.tempAPIKeyRepo != nil {
			if _, _, err := s.tempAPIKeyRepo.AddCostUSD(ctx, input.TempAPIKey.ID, cost.ActualCost); err != nil {
				log.Printf("Update temp API key cost failed: %v", err)
			}
		}
	}

	// 递增临时 API Key 的请求计数（current_period_count + total_requests）
	if input.TempAPIKey != nil && s.tempAPIKeyRepo != nil {
		if err := s.tempAPIKeyRepo.IncrementUsageCounters(ctx, input.TempAPIKey.ID); err != nil {
			log.Printf("Increment temp API key usage counters failed: %v", err)
		}
	}

	if cost.ActualCost > 0 && apiKey.QuotaLimitUSD != nil && s.apiKeyRepo != nil {
		if err := s.apiKeyRepo.IncrementQuotaUsed(ctx, apiKey.ID, cost.ActualCost); err != nil {
			log.Printf("Increment API key quota used failed: %v", err)
		} else if s.apiKeyCacheInval != nil && apiKey.Key != "" {
			s.apiKeyCacheInval.InvalidateAuthCacheByKey(ctx, apiKey.Key)
		}
	}

	// Schedule batch update for account last_used_at
	s.deferredService.ScheduleLastUsedUpdate(account.ID)

	return nil
}

func openAIActualInputTokens(result *OpenAIForwardResult, account *Account) int {
	if result == nil {
		return 0
	}
	// Anthropic usage already reports uncached input separately from cache reads
	// and writes. Kiro API Key forwards that usage without transformation.
	if account != nil && account.IsKiroApiKey() {
		return max(result.Usage.InputTokens, 0)
	}
	actualInputTokens := result.Usage.InputTokens - result.Usage.CacheReadInputTokens
	if account != nil && account.IsKiro() && !account.IsKiroApiKey() {
		actualInputTokens -= result.Usage.CacheCreationInputTokens
	}
	if actualInputTokens < 0 {
		return 0
	}
	return actualInputTokens
}

// extractCodexUsageHeaders extracts Codex usage limits from response headers
func extractCodexUsageHeaders(headers http.Header) *OpenAICodexUsageSnapshot {
	snapshot := &OpenAICodexUsageSnapshot{}
	hasData := false

	// Helper to parse float64 from header
	parseFloat := func(key string) *float64 {
		if v := headers.Get(key); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				return &f
			}
		}
		return nil
	}

	// Helper to parse int from header
	parseInt := func(key string) *int {
		if v := headers.Get(key); v != "" {
			if i, err := strconv.Atoi(v); err == nil {
				return &i
			}
		}
		return nil
	}

	// Primary (weekly) limits
	if v := parseFloat("x-codex-primary-used-percent"); v != nil {
		snapshot.PrimaryUsedPercent = v
		hasData = true
	}
	if v := parseInt("x-codex-primary-reset-after-seconds"); v != nil {
		snapshot.PrimaryResetAfterSeconds = v
		hasData = true
	}
	if v := parseInt("x-codex-primary-window-minutes"); v != nil {
		snapshot.PrimaryWindowMinutes = v
		hasData = true
	}

	// Secondary (5h) limits
	if v := parseFloat("x-codex-secondary-used-percent"); v != nil {
		snapshot.SecondaryUsedPercent = v
		hasData = true
	}
	if v := parseInt("x-codex-secondary-reset-after-seconds"); v != nil {
		snapshot.SecondaryResetAfterSeconds = v
		hasData = true
	}
	if v := parseInt("x-codex-secondary-window-minutes"); v != nil {
		snapshot.SecondaryWindowMinutes = v
		hasData = true
	}

	// Overflow ratio
	if v := parseFloat("x-codex-primary-over-secondary-limit-percent"); v != nil {
		snapshot.PrimaryOverSecondaryPercent = v
		hasData = true
	}

	if !hasData {
		return nil
	}

	snapshot.UpdatedAt = time.Now().Format(time.RFC3339)
	return snapshot
}

// updateCodexUsageSnapshot saves the Codex usage snapshot to account's Extra field
func (s *OpenAIGatewayService) updateCodexUsageSnapshot(ctx context.Context, accountID int64, snapshot *OpenAICodexUsageSnapshot) {
	if snapshot == nil {
		return
	}

	// Convert snapshot to map for merging into Extra
	updates := make(map[string]any)
	if snapshot.PrimaryUsedPercent != nil {
		updates["codex_primary_used_percent"] = *snapshot.PrimaryUsedPercent
	}
	if snapshot.PrimaryResetAfterSeconds != nil {
		updates["codex_primary_reset_after_seconds"] = *snapshot.PrimaryResetAfterSeconds
	}
	if snapshot.PrimaryWindowMinutes != nil {
		updates["codex_primary_window_minutes"] = *snapshot.PrimaryWindowMinutes
	}
	if snapshot.SecondaryUsedPercent != nil {
		updates["codex_secondary_used_percent"] = *snapshot.SecondaryUsedPercent
	}
	if snapshot.SecondaryResetAfterSeconds != nil {
		updates["codex_secondary_reset_after_seconds"] = *snapshot.SecondaryResetAfterSeconds
	}
	if snapshot.SecondaryWindowMinutes != nil {
		updates["codex_secondary_window_minutes"] = *snapshot.SecondaryWindowMinutes
	}
	if snapshot.PrimaryOverSecondaryPercent != nil {
		updates["codex_primary_over_secondary_percent"] = *snapshot.PrimaryOverSecondaryPercent
	}
	updates["codex_usage_updated_at"] = snapshot.UpdatedAt

	// Normalize to canonical 5h/7d fields based on window_minutes
	// This fixes the issue where OpenAI's primary/secondary naming is reversed
	// Strategy: Compare the two windows and assign the smaller one to 5h, larger one to 7d

	// IMPORTANT: We can only reliably determine window type from window_minutes field
	// The reset_after_seconds is remaining time, not window size, so it cannot be used for comparison

	var primaryWindowMins, secondaryWindowMins int
	var hasPrimaryWindow, hasSecondaryWindow bool

	// Only use window_minutes for reliable window size comparison
	if snapshot.PrimaryWindowMinutes != nil {
		primaryWindowMins = *snapshot.PrimaryWindowMinutes
		hasPrimaryWindow = true
	}

	if snapshot.SecondaryWindowMinutes != nil {
		secondaryWindowMins = *snapshot.SecondaryWindowMinutes
		hasSecondaryWindow = true
	}

	// Determine which is 5h and which is 7d
	var use5hFromPrimary, use7dFromPrimary bool
	var use5hFromSecondary, use7dFromSecondary bool

	if hasPrimaryWindow && hasSecondaryWindow {
		// Both window sizes known: compare and assign smaller to 5h, larger to 7d
		if primaryWindowMins < secondaryWindowMins {
			use5hFromPrimary = true
			use7dFromSecondary = true
		} else {
			use5hFromSecondary = true
			use7dFromPrimary = true
		}
	} else if hasPrimaryWindow {
		// Only primary window size known: classify by absolute threshold
		if primaryWindowMins <= 360 {
			use5hFromPrimary = true
		} else {
			use7dFromPrimary = true
		}
	} else if hasSecondaryWindow {
		// Only secondary window size known: classify by absolute threshold
		if secondaryWindowMins <= 360 {
			use5hFromSecondary = true
		} else {
			use7dFromSecondary = true
		}
	} else {
		// No window_minutes available: cannot reliably determine window types
		// Fall back to legacy assumption (may be incorrect)
		// Assume primary=7d, secondary=5h based on historical observation
		if snapshot.SecondaryUsedPercent != nil || snapshot.SecondaryResetAfterSeconds != nil || snapshot.SecondaryWindowMinutes != nil {
			use5hFromSecondary = true
		}
		if snapshot.PrimaryUsedPercent != nil || snapshot.PrimaryResetAfterSeconds != nil || snapshot.PrimaryWindowMinutes != nil {
			use7dFromPrimary = true
		}
	}

	// Write canonical 5h fields
	if use5hFromPrimary {
		if snapshot.PrimaryUsedPercent != nil {
			updates["codex_5h_used_percent"] = *snapshot.PrimaryUsedPercent
		}
		if snapshot.PrimaryResetAfterSeconds != nil {
			updates["codex_5h_reset_after_seconds"] = *snapshot.PrimaryResetAfterSeconds
		}
		if snapshot.PrimaryWindowMinutes != nil {
			updates["codex_5h_window_minutes"] = *snapshot.PrimaryWindowMinutes
		}
	} else if use5hFromSecondary {
		if snapshot.SecondaryUsedPercent != nil {
			updates["codex_5h_used_percent"] = *snapshot.SecondaryUsedPercent
		}
		if snapshot.SecondaryResetAfterSeconds != nil {
			updates["codex_5h_reset_after_seconds"] = *snapshot.SecondaryResetAfterSeconds
		}
		if snapshot.SecondaryWindowMinutes != nil {
			updates["codex_5h_window_minutes"] = *snapshot.SecondaryWindowMinutes
		}
	}

	// Write canonical 7d fields
	if use7dFromPrimary {
		if snapshot.PrimaryUsedPercent != nil {
			updates["codex_7d_used_percent"] = *snapshot.PrimaryUsedPercent
		}
		if snapshot.PrimaryResetAfterSeconds != nil {
			updates["codex_7d_reset_after_seconds"] = *snapshot.PrimaryResetAfterSeconds
		}
		if snapshot.PrimaryWindowMinutes != nil {
			updates["codex_7d_window_minutes"] = *snapshot.PrimaryWindowMinutes
		}
	} else if use7dFromSecondary {
		if snapshot.SecondaryUsedPercent != nil {
			updates["codex_7d_used_percent"] = *snapshot.SecondaryUsedPercent
		}
		if snapshot.SecondaryResetAfterSeconds != nil {
			updates["codex_7d_reset_after_seconds"] = *snapshot.SecondaryResetAfterSeconds
		}
		if snapshot.SecondaryWindowMinutes != nil {
			updates["codex_7d_window_minutes"] = *snapshot.SecondaryWindowMinutes
		}
	}

	// Update account's Extra field asynchronously
	go func() {
		updateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.accountRepo.UpdateExtra(updateCtx, accountID, updates)
	}()

	// Update OpenAI quota availability based on canonical 5h/7d usage
	if s.usageCache != nil {
		var maxResetSeconds int
		var anyOverThreshold bool

		// Check codex_5h_used_percent
		if v, ok := updates["codex_5h_used_percent"]; ok {
			if pct, ok := v.(float64); ok && pct >= quotaHealthyThreshold {
				anyOverThreshold = true
				if rs, ok := updates["codex_5h_reset_after_seconds"]; ok {
					if sec, ok := rs.(int); ok && sec > maxResetSeconds {
						maxResetSeconds = sec
					}
				}
			}
		}
		// Check codex_7d_used_percent
		// 周窗口不做 80% 预判：7d 重置时间最长可达 7 天，一旦提前标记会把账号
		// 长时间排除在调度之外。只有周额度真正用尽（100%）时才标记，其余情况
		// 交给上游 429 响应（ratelimit_service）兜底。
		if v, ok := updates["codex_7d_used_percent"]; ok {
			if pct, ok := v.(float64); ok && pct >= openAI7dQuotaExhaustedThreshold {
				anyOverThreshold = true
				if rs, ok := updates["codex_7d_reset_after_seconds"]; ok {
					if sec, ok := rs.(int); ok && sec > maxResetSeconds {
						maxResetSeconds = sec
					}
				}
			}
		}

		if anyOverThreshold && maxResetSeconds > 0 {
			resetAt := time.Now().Add(time.Duration(maxResetSeconds) * time.Second)
			s.usageCache.SetOpenAIQuotaReset(accountID, resetAt)
			log.Printf("[OpenAI Quota] Account %d marked quota exhausted, reset at %s (in %ds)", accountID, resetAt.Format(time.RFC3339), maxResetSeconds)
		} else if anyOverThreshold {
			// Over threshold but no reset time available — use fallback
			resetAt := time.Now().Add(openAI429Fallback)
			s.usageCache.SetOpenAIQuotaReset(accountID, resetAt)
			log.Printf("[OpenAI Quota] Account %d marked quota exhausted (no reset time), fallback reset in %s", accountID, openAI429Fallback)
		} else {
			s.usageCache.ClearOpenAIQuotaReset(accountID)
		}
	}
}

// checkOpenAIRateLimitHeaders 检查 OpenAI API Key 响应 header 中的速率限制信息
// 用于 API Key 类型账号的额度预判（OAuth 账号通过 Codex usage snapshot 处理）
func (s *OpenAIGatewayService) checkOpenAIRateLimitHeaders(accountID int64, headers http.Header) {
	if s.usageCache == nil {
		return
	}

	remaining := strings.TrimSpace(headers.Get("x-ratelimit-remaining-requests"))
	if remaining == "" {
		return
	}

	if remaining == "0" {
		// 请求配额已耗尽，解析重置时间
		resetRaw := strings.TrimSpace(headers.Get("x-ratelimit-reset-requests"))
		if resetRaw != "" {
			if resetAt, ok := parseOpenAIReset(resetRaw, time.Now()); ok {
				s.usageCache.SetOpenAIQuotaReset(accountID, resetAt)
				log.Printf("[OpenAI Quota] API Key account %d requests exhausted, reset at %s", accountID, resetAt.Format(time.RFC3339))
				return
			}
		}
		// 无法解析重置时间，使用 fallback
		s.usageCache.SetOpenAIQuotaReset(accountID, time.Now().Add(openAI429Fallback))
		log.Printf("[OpenAI Quota] API Key account %d requests exhausted, fallback reset in %s", accountID, openAI429Fallback)
	} else {
		// 还有剩余配额，清除标记（如果之前被标记过）
		s.usageCache.ClearOpenAIQuotaReset(accountID)
	}
}

// sortAccountsByPriorityAndLastUsedSimple 按优先级和最后使用时间排序账号
// 这是一个简化版本，不考虑配额重置时间（OpenAI 账号没有配额重置时间）
func sortAccountsByPriorityAndLastUsedSimple(accounts []*Account, preferOAuth bool) {
	sort.SliceStable(accounts, func(i, j int) bool {
		a, b := accounts[i], accounts[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		switch {
		case a.LastUsedAt == nil && b.LastUsedAt != nil:
			return true
		case a.LastUsedAt != nil && b.LastUsedAt == nil:
			return false
		case a.LastUsedAt == nil && b.LastUsedAt == nil:
			if preferOAuth && a.Type != b.Type {
				return a.Type == AccountTypeOAuth
			}
			return false
		default:
			return a.LastUsedAt.Before(*b.LastUsedAt)
		}
	})
	shuffleWithinPriorityAndLastUsed(accounts)
}

// ForwardChatCompletions forwards a Chat Completions request to the upstream OpenAI API.
// Only API Key accounts are supported; OAuth accounts should be rejected by the handler.
func (s *OpenAIGatewayService) ForwardChatCompletions(ctx context.Context, c *gin.Context, account *Account, body []byte) (*OpenAIForwardResult, error) {
	startTime := time.Now()

	var reqBody map[string]any
	if err := json.Unmarshal(body, &reqBody); err != nil {
		return nil, fmt.Errorf("parse request: %w", err)
	}

	reqModel, _ := reqBody["model"].(string)
	reqStream, _ := reqBody["stream"].(bool)
	serviceTier := extractOpenAIServiceTier(reqBody)
	originalModel := reqModel
	bodyModified := false
	grokCacheKey := ""

	// Model mapping only
	mappedModel := account.GetMappedModel(reqModel)
	if account.IsGrok() {
		mappedModel = resolveGrokRequestModel(account, reqModel)
		if err := validateGrokTextModel(c, mappedModel); err != nil {
			return nil, err
		}
	}
	if mappedModel != reqModel {
		log.Printf("[OpenAI CC] Model mapping applied: %s -> %s (account: %s)", reqModel, mappedModel, account.Name)
		reqBody["model"] = mappedModel
		bodyModified = true
	}

	// Strip unsupported fields that upstream APIs reject (e.g. Claude Code v2.1.22+ context_management)
	for _, unsupportedField := range []string{"context_management"} {
		if _, exists := reqBody[unsupportedField]; exists {
			delete(reqBody, unsupportedField)
			bodyModified = true
		}
	}

	if account.IsGrok() {
		// xAI only returns the terminal usage chunk when requested explicitly.
		// Always enable it for streamed API-key requests so billing does not
		// silently record zero tokens for otherwise valid responses.
		if reqStream {
			streamOptions, ok := reqBody["stream_options"].(map[string]any)
			if !ok || streamOptions == nil {
				streamOptions = make(map[string]any)
				reqBody["stream_options"] = streamOptions
			}
			if includeUsage, ok := streamOptions["include_usage"].(bool); !ok || !includeUsage {
				streamOptions["include_usage"] = true
				bodyModified = true
			}
		}
		if normalizeGrokChatReasoning(reqBody, mappedModel) {
			bodyModified = true
		}
		cacheBody, err := applyGrokPromptCacheKey(body, c, body, mappedModel)
		if err != nil {
			return nil, fmt.Errorf("apply Grok prompt cache key: %w", err)
		}
		var cacheRequest map[string]any
		if err := json.Unmarshal(cacheBody, &cacheRequest); err != nil {
			return nil, fmt.Errorf("parse Grok cache request: %w", err)
		}
		if apiKey := getAPIKeyFromGinContext(c); apiKey != nil && apiKey.ID > 0 {
			if cacheKey, ok := cacheRequest["prompt_cache_key"].(string); ok {
				grokCacheKey = strings.TrimSpace(cacheKey)
			}
		}
		if _, exists := reqBody["prompt_cache_key"]; exists {
			delete(reqBody, "prompt_cache_key")
			bodyModified = true
		}
	}

	if bodyModified {
		var err error
		body, err = json.Marshal(reqBody)
		if err != nil {
			return nil, fmt.Errorf("serialize request body: %w", err)
		}
	}

	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}

	requestCtx, releaseRequestCtx := ctx, func() {}
	if account.IsGrok() && !reqStream {
		requestCtx, releaseRequestCtx = grokUpstreamContext(ctx, false)
	}
	defer releaseRequestCtx()
	body, err = prepareOpenAIEnvironmentContext(account, body, "messages")
	if err != nil {
		return nil, err
	}
	upstreamReq, err := s.buildChatCompletionsRequest(requestCtx, c, account, body, token, grokCacheKey)
	if err != nil {
		return nil, err
	}

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	if c != nil {
		c.Set(OpsUpstreamRequestBodyKey, string(body))
	}

	if account.IsGrok() {
		s.applyGrokRequestJitter(ctx)
	}
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		safeErr := sanitizeUpstreamErrorMessage(err.Error())
		setOpsUpstreamError(c, 0, safeErr, "")
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: 0,
			Kind:               "request_error",
			Message:            safeErr,
		})
		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{
				"type":    "upstream_error",
				"message": "Upstream request failed",
			},
		})
		return nil, fmt.Errorf("upstream request failed: %s", safeErr)
	}
	if resp == nil || resp.Body == nil {
		err := errors.New("upstream request returned an empty response")
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{
			"type":    "upstream_error",
			"message": "Upstream request failed",
		}})
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		if account.IsGrok() {
			s.updateGrokUsageSnapshot(ctx, account.ID, xai.ParseQuotaHeaders(resp.Header, resp.StatusCode))
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(respBody))
			if s.shouldFailoverGrokUpstreamError(resp.StatusCode, respBody) {
				if s.rateLimitService != nil {
					s.rateLimitService.HandleUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody)
				}
				return nil, &UpstreamFailoverError{StatusCode: resp.StatusCode, Message: sanitizeUpstreamErrorMessage(extractUpstreamErrorMessage(respBody))}
			}
		} else if s.shouldFailoverUpstreamError(resp.StatusCode) {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(respBody))

			upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(respBody)))
			upstreamDetail := ""
			if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
				maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
				if maxBytes <= 0 {
					maxBytes = 2048
				}
				upstreamDetail = truncateString(string(respBody), maxBytes)
			}
			appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
				Platform:           account.Platform,
				AccountID:          account.ID,
				AccountName:        account.Name,
				UpstreamStatusCode: resp.StatusCode,
				UpstreamRequestID:  resp.Header.Get("x-request-id"),
				Kind:               "failover",
				Message:            upstreamMsg,
				Detail:             upstreamDetail,
			})
			s.handleFailoverSideEffects(ctx, resp, account)
			return nil, &UpstreamFailoverError{StatusCode: resp.StatusCode}
		}
		return s.handleErrorResponse(ctx, resp, c, account)
	}

	var usage *OpenAIUsage
	var firstTokenMs *int
	needModelReplace := originalModel != mappedModel

	if reqStream {
		if account.IsGrok() {
			maxLineSize := defaultMaxLineSize
			if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
				maxLineSize = s.cfg.Gateway.MaxLineSize
			}
			resp.Body = newGrokResponsesPingFilterBody(resp.Body, account, maxLineSize)
		}
		streamResult, err := s.handleCCStreamingResponse(resp, c, account, startTime, needModelReplace, mappedModel, originalModel)
		if err != nil {
			return nil, err
		}
		usage = streamResult.usage
		firstTokenMs = streamResult.firstTokenMs
	} else {
		usage, err = s.handleCCNonStreamingResponse(resp, c, account, needModelReplace, mappedModel, originalModel)
		if err != nil {
			return nil, err
		}
	}

	// Check OpenAI rate-limit headers only for native OpenAI API-key accounts.
	// Grok uses xAI quota headers and must not populate the OpenAI quota cache.
	if account.IsOpenAI() && account.Type == AccountTypeAPIKey {
		s.checkOpenAIRateLimitHeaders(account.ID, resp.Header)
	}
	if account.IsGrok() {
		s.updateGrokUsageSnapshot(ctx, account.ID, xai.ParseQuotaHeaders(resp.Header, resp.StatusCode))
	}
	resultModel := originalModel
	if account.IsGrok() {
		resultModel = mappedModel
	}

	return &OpenAIForwardResult{
		RequestID:    resp.Header.Get("x-request-id"),
		Usage:        *usage,
		Model:        resultModel,
		ServiceTier:  resolvedServiceTier(usage, serviceTier),
		Stream:       reqStream,
		Duration:     time.Since(startTime),
		FirstTokenMs: firstTokenMs,
	}, nil
}

func (s *OpenAIGatewayService) buildChatCompletionsRequest(ctx context.Context, c *gin.Context, account *Account, body []byte, token string, grokCacheKeys ...string) (*http.Request, error) {
	var targetURL string
	if account.IsGrok() {
		baseURL := account.GetGrokBaseURL()
		if s.cfg != nil {
			normalizedBaseURL, err := s.validateUpstreamBaseURL(baseURL)
			if err != nil {
				return nil, err
			}
			baseURL = normalizedBaseURL
		}
		targetURL = xai.BuildChatCompletionsURL(baseURL)
	} else {
		switch account.Type {
		case AccountTypeOAuth:
			// OAuth accounts use ChatGPT internal API (same as native path)
			targetURL = chatgptCodexURL
		case AccountTypeAPIKey:
			baseURL := account.GetCredential("base_url")
			if baseURL == "" {
				targetURL = "https://api.openai.com/v1/chat/completions"
			} else {
				validatedURL, err := s.validateUpstreamBaseURL(baseURL)
				if err != nil {
					return nil, err
				}
				targetURL = validatedURL + "/chat/completions"
			}
		default:
			targetURL = "https://api.openai.com/v1/chat/completions"
		}
	}

	requestCtx := ctx
	streamRequest := false
	var streamBody struct {
		Stream bool `json:"stream"`
	}
	if json.Unmarshal(body, &streamBody) == nil {
		streamRequest = streamBody.Stream
	}
	if account.IsGrok() && streamRequest {
		requestCtx, _ = detachUpstreamContext(ctx)
	}
	req, err := http.NewRequestWithContext(requestCtx, "POST", targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+token)

	if c != nil && c.Request != nil {
		for key, values := range c.Request.Header {
			lowerKey := strings.ToLower(key)
			if account.IsGrok() && (lowerKey == "session_id" || lowerKey == "conversation_id") {
				continue
			}
			if openaiAllowedHeaders[lowerKey] {
				for _, v := range values {
					req.Header.Add(key, v)
				}
			}
		}
	}

	customUA := account.GetOpenAIUserAgent()
	if customUA != "" && !account.IsGrokOAuth() {
		req.Header.Set("User-Agent", customUA)
	}

	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	account.ApplyHeaderOverrides(req.Header)
	if account.IsGrokOAuth() {
		xai.ApplyCLIProxyHeaders(req)
	}
	if account.IsGrok() && len(grokCacheKeys) > 0 {
		applyGrokCacheHeaders(req.Header, grokCacheKeys[0])
	}

	return req, nil
}

func (s *OpenAIGatewayService) handleCCStreamingResponse(resp *http.Response, c *gin.Context, account *Account, startTime time.Time, needModelReplace bool, mappedModel, originalModel string) (*openaiStreamingResult, error) {
	if s.cfg != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.cfg.Security.ResponseHeaders)
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	if v := resp.Header.Get("x-request-id"); v != "" {
		c.Header("x-request-id", v)
	}

	w := c.Writer
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}

	usage := &OpenAIUsage{}
	var firstTokenMs *int
	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	scanner.Buffer(make([]byte, 64*1024), maxLineSize)

	events := make(chan openAIStreamScanEvent, 16)
	done := make(chan struct{})
	sendEvent := func(ev openAIStreamScanEvent) bool {
		select {
		case events <- ev:
			return true
		case <-done:
			return false
		}
	}
	var lastReadAt int64
	atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
	go func() {
		defer close(events)
		for scanner.Scan() {
			atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
			if !sendEvent(openAIStreamScanEvent{line: scanner.Text()}) {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			_ = sendEvent(openAIStreamScanEvent{err: err})
		}
	}()
	defer close(done)

	streamInterval := time.Duration(0)
	configuredStreamInterval := 0
	if s.cfg != nil {
		configuredStreamInterval = s.cfg.Gateway.StreamDataIntervalTimeout
	}
	streamInterval = resolveGrokStreamIdleTimeout(configuredStreamInterval, account)
	if streamInterval == 0 && s.cfg != nil && s.cfg.Gateway.StreamDataIntervalTimeout > 0 {
		streamInterval = time.Duration(s.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}
	var intervalTicker *time.Ticker
	if streamInterval > 0 {
		intervalTicker = time.NewTicker(streamInterval)
		defer intervalTicker.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTicker != nil {
		intervalCh = intervalTicker.C
	}

	keepaliveInterval := time.Duration(0)
	if s.cfg != nil && s.cfg.Gateway.StreamKeepaliveInterval > 0 {
		keepaliveInterval = time.Duration(s.cfg.Gateway.StreamKeepaliveInterval) * time.Second
	}
	var keepaliveTicker *time.Ticker
	if keepaliveInterval > 0 {
		keepaliveTicker = time.NewTicker(keepaliveInterval)
		defer keepaliveTicker.Stop()
	}
	var keepaliveCh <-chan time.Time
	if keepaliveTicker != nil {
		keepaliveCh = keepaliveTicker.C
	}
	lastDataAt := time.Now()

	errorEventSent := false
	sendErrorEvent := func(reason string) {
		if errorEventSent {
			return
		}
		errorEventSent = true
		_, _ = fmt.Fprintf(w, "event: error\ndata: {\"error\":\"%s\"}\n\n", reason)
		flusher.Flush()
	}

	processLine := func(line string) error {
		lastDataAt = time.Now()

		if openaiSSEDataRe.MatchString(line) {
			data := openaiSSEDataRe.ReplaceAllString(line, "")

			if needModelReplace {
				line = s.replaceModelInSSELine(line, mappedModel, originalModel)
			}

			if _, err := fmt.Fprintf(w, "%s\n", line); err != nil {
				sendErrorEvent("write_failed")
				return err
			}
			flusher.Flush()

			if firstTokenMs == nil && data != "" && data != "[DONE]" {
				ms := int(time.Since(startTime).Milliseconds())
				firstTokenMs = &ms
			}
			s.parseCCUsage(data, usage, account != nil && account.IsGrok())
			return nil
		}

		if _, err := fmt.Fprintf(w, "%s\n", line); err != nil {
			sendErrorEvent("write_failed")
			return err
		}
		flusher.Flush()
		return nil
	}

	if shouldPreReadOpenAICapacity(account) {
		bufferedEvents, capacityMsg, streamClosed, preErr := preReadOpenAIStreamEvents(c.Request.Context(), events, streamInterval)
		switch {
		case errors.Is(preErr, errOpenAIInitialStreamTimeout):
			log.Printf("Stream data interval timeout: account=%d model=%s interval=%s", account.ID, originalModel, streamInterval)
			if s.rateLimitService != nil {
				s.rateLimitService.HandleStreamTimeout(c.Request.Context(), account, originalModel)
			}
			sendErrorEvent("stream_timeout")
			if account != nil && account.IsGrok() {
				return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, grokStreamIdleFailoverError(account, streamInterval)
			}
			return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, preErr
		case preErr != nil:
			if errors.Is(preErr, bufio.ErrTooLong) {
				log.Printf("SSE line too long: account=%d max_size=%d error=%v", account.ID, maxLineSize, preErr)
				sendErrorEvent("response_too_large")
				return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, preErr
			}
			sendErrorEvent("stream_read_error")
			return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, fmt.Errorf("stream read error: %w", preErr)
		case capacityMsg != "":
			return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, s.newOpenAICapacityFailoverError(
				c.Request.Context(),
				c,
				account,
				resp.Header.Get("x-request-id"),
				capacityMsg,
				capacityMsg,
			)
		case streamClosed:
			return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, nil
		default:
			for _, ev := range bufferedEvents {
				if err := processLine(ev.line); err != nil {
					return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, err
				}
			}
		}
	}

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, nil
			}
			if ev.err != nil {
				if errors.Is(ev.err, bufio.ErrTooLong) {
					log.Printf("SSE line too long: account=%d max_size=%d error=%v", account.ID, maxLineSize, ev.err)
					sendErrorEvent("response_too_large")
					return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, ev.err
				}
				sendErrorEvent("stream_read_error")
				return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, fmt.Errorf("stream read error: %w", ev.err)
			}
			if err := processLine(ev.line); err != nil {
				return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, err
			}

		case <-intervalCh:
			lastRead := time.Unix(0, atomic.LoadInt64(&lastReadAt))
			if time.Since(lastRead) < streamInterval {
				continue
			}
			log.Printf("Stream data interval timeout: account=%d model=%s interval=%s", account.ID, originalModel, streamInterval)
			if s.rateLimitService != nil {
				s.rateLimitService.HandleStreamTimeout(c.Request.Context(), account, originalModel)
			}
			sendErrorEvent("stream_timeout")
			if account != nil && account.IsGrok() {
				return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, grokStreamIdleFailoverError(account, streamInterval)
			}
			return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, fmt.Errorf("stream data interval timeout")

		case <-keepaliveCh:
			if time.Since(lastDataAt) < keepaliveInterval {
				continue
			}
			if _, err := fmt.Fprint(w, ":\n\n"); err != nil {
				return &openaiStreamingResult{usage: usage, firstTokenMs: firstTokenMs}, err
			}
			flusher.Flush()
		}
	}
}

func (s *OpenAIGatewayService) handleCCNonStreamingResponse(resp *http.Response, c *gin.Context, account *Account, needModelReplace bool, mappedModel, originalModel string) (*OpenAIUsage, error) {
	reader := io.Reader(resp.Body)
	if account != nil && account.IsGrok() {
		reader = io.LimitReader(resp.Body, grokMaxNonStreamingBodySize+1)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if account != nil && account.IsGrok() && len(body) > grokMaxNonStreamingBodySize {
		return nil, fmt.Errorf("Grok non-streaming response exceeds %d bytes", grokMaxNonStreamingBodySize)
	}

	usage := &OpenAIUsage{}
	s.parseCCUsage(string(body), usage, account != nil && account.IsGrok())

	if needModelReplace {
		body = s.replaceModelInResponseBody(body, mappedModel, originalModel)
	}

	if s.cfg != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.cfg.Security.ResponseHeaders)
	}

	contentType := "application/json"
	if s.cfg != nil && !s.cfg.Security.ResponseHeaders.Enabled {
		if upstreamType := resp.Header.Get("Content-Type"); upstreamType != "" {
			contentType = upstreamType
		}
	}

	c.Data(resp.StatusCode, contentType, body)
	return usage, nil
}

// parseCCUsage parses Chat Completions usage from a JSON string into OpenAIUsage.
// Works for both streaming (last chunk with usage) and non-streaming responses.
func (s *OpenAIGatewayService) parseCCUsage(data string, usage *OpenAIUsage, independentReasoning ...bool) {
	if data == "" || data == "[DONE]" {
		return
	}
	if parsed, ok := extractOpenAIUsageFromJSONBytes([]byte(data), independentReasoning...); ok {
		*usage = parsed
		return
	}
	var resp struct {
		Usage *struct {
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			PromptTokensDetails *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal([]byte(data), &resp) == nil && resp.Usage != nil {
		usage.InputTokens = resp.Usage.PromptTokens
		usage.OutputTokens = resp.Usage.CompletionTokens
		if resp.Usage.PromptTokensDetails != nil {
			usage.CacheReadInputTokens = resp.Usage.PromptTokensDetails.CachedTokens
		}
	}
}

// ForwardChatCompletionsViaResponses forwards a Chat Completions request through
// the Responses API (for OAuth accounts). It converts CC → Responses format,
// sends to upstream, and converts the response back to CC format.
func (s *OpenAIGatewayService) ForwardChatCompletionsViaResponses(ctx context.Context, c *gin.Context, account *Account, body []byte, wantStream bool) (*OpenAIForwardResult, error) {
	startTime := time.Now()

	var ccReqBody map[string]any
	if err := json.Unmarshal(body, &ccReqBody); err != nil {
		return nil, fmt.Errorf("parse request: %w", err)
	}

	originalModel, _ := ccReqBody["model"].(string)
	modelFallbackAttempted := false // OAuth 模型回退标记
	injectedInstructions := ""

	// Convert CC request to Responses API format. Some Grok-compatible clients
	// send a Responses-shaped body through this endpoint; preserve that body
	// instead of dropping its input field during CC conversion.
	responsesBody := convertCCRequestToResponses(ccReqBody)
	if account.IsGrok() {
		if _, hasInput := ccReqBody["input"]; hasInput {
			responsesBody = make(map[string]any, len(ccReqBody))
			for key, value := range ccReqBody {
				responsesBody[key] = value
			}
		}
	}
	if account.IsGrok() {
		// Preserve the client's stream preference. xAI Responses supports both
		// JSON and SSE; forcing SSE breaks non-streaming Chat Completions callers.
		responsesBody["stream"] = wantStream
		responsesBody["store"] = false
	}
	serviceTier := extractOpenAIServiceTier(responsesBody)

	// Apply model mapping
	mappedModel := account.GetMappedModel(originalModel)
	if account.IsGrok() {
		mappedModel = resolveGrokRequestModel(account, originalModel)
		if err := validateGrokTextModel(c, mappedModel); err != nil {
			return nil, err
		}
	}
	if mappedModel != originalModel {
		log.Printf("[OpenAI CC→Responses] Model mapping applied: %s -> %s (account: %s)", originalModel, mappedModel, account.Name)
		responsesBody["model"] = mappedModel
	}
	if account.IsGrok() {
		// The generic CC→Responses converter intentionally copies only fields
		// shared by OpenAI Responses. Preserve Grok's optional reasoning effort
		// before applying xAI-specific capability normalization.
		if value, exists := ccReqBody["reasoning_effort"]; exists {
			responsesBody["reasoning_effort"] = value
		} else if value, exists := ccReqBody["reasoningEffort"]; exists {
			responsesBody["reasoningEffort"] = value
		} else if value, exists := ccReqBody["reasoning"]; exists {
			responsesBody["reasoning"] = value
		}
		normalizeGrokResponsesReasoning(responsesBody, mappedModel)
		encodedBody, err := json.Marshal(responsesBody)
		if err != nil {
			return nil, fmt.Errorf("serialize Grok Responses body: %w", err)
		}
		clearGrokResponsesClientToolMapping(c)
		encodedBody, _, err = patchGrokResponsesBodyWithClientTools(encodedBody, mappedModel)
		if err != nil {
			return nil, fmt.Errorf("sanitize Grok Responses body: %w", err)
		}
		if err := json.Unmarshal(encodedBody, &responsesBody); err != nil {
			return nil, fmt.Errorf("parse sanitized Grok Responses body: %w", err)
		}
		encodedBody, err = applyGrokPromptCacheKey(encodedBody, c, body, mappedModel)
		if err != nil {
			return nil, fmt.Errorf("apply Grok prompt cache key: %w", err)
		}
		if err := json.Unmarshal(encodedBody, &responsesBody); err != nil {
			return nil, fmt.Errorf("parse Grok cache request: %w", err)
		}
	}

	// Apply OAuth transform (store=false, stream=true, model normalization, tool normalization, instructions, input filtering)
	isCodexCLI := openai.IsCodexCLIRequest(c.GetHeader("User-Agent"))
	if !isCodexCLI && account.Platform == PlatformOpenAI {
		codexResult := applyCodexOAuthTransform(responsesBody)
		if codexResult.NormalizedModel != "" {
			mappedModel = codexResult.NormalizedModel
		}
		if codexResult.InjectedInstructions != "" {
			injectedInstructions = codexResult.InjectedInstructions
		}
	} else if account.Platform == PlatformOpenAI {
		// For Codex CLI, still need store=false and stream=true for OAuth
		responsesBody["store"] = false
		responsesBody["stream"] = true
		normalizedModel := normalizeCodexModel(mappedModel)
		if normalizedModel != "" && normalizedModel != mappedModel {
			if effort := extractCodexModelEffort(mappedModel, normalizedModel); effort != "" {
				if _, hasReasoning := responsesBody["reasoning"]; !hasReasoning {
					responsesBody["reasoning"] = map[string]any{"effort": effort}
				}
			}
			responsesBody["model"] = normalizedModel
			mappedModel = normalizedModel
		}
	}

	// OAuth 账号走 ChatGPT internal API，该端点不再接受带 -codex 后缀的模型名。
	if account.Platform == PlatformOpenAI {
		if model, ok := responsesBody["model"].(string); ok {
			stripped := stripCodexModelSuffix(model)
			if stripped != model {
				log.Printf("[OpenAI CC→Responses] Strip -codex suffix for OAuth: %s -> %s (account: %s, isCodexCLI: %v)",
					model, stripped, account.Name, isCodexCLI)
				responsesBody["model"] = stripped
				mappedModel = stripped
			}
		}
	}

	if shouldGuardOpenAIStatelessReasoning(account) {
		if statelessErr := validateOpenAIStatelessInputReferences(responsesBody); statelessErr != nil {
			writeOpenAIInvalidRequest(c, statelessErr.ClientMessage(), "input")
			return nil, statelessErr
		}
		ensureReasoningEncryptedContentInclude(responsesBody)
	}

	// Serialize the converted body
	convertedBody, err := json.Marshal(responsesBody)
	if err != nil {
		return nil, fmt.Errorf("serialize converted request: %w", err)
	}

retryWithCCFallbackModel:
	convertedBody, err = prepareOpenAIEnvironmentContext(account, convertedBody, "input")
	if err != nil {
		return nil, err
	}

	// Get access token
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}

	// Build upstream request (reuse existing OAuth request builder targeting chatgpt.com/backend-api/codex/responses)
	promptCacheKey := ""
	if v, ok := responsesBody["prompt_cache_key"].(string); ok {
		promptCacheKey = strings.TrimSpace(v)
	}
	requestCtx, releaseRequestCtx := ctx, func() {}
	if account.IsGrok() && !wantStream {
		requestCtx, releaseRequestCtx = grokUpstreamContext(ctx, false)
	}
	defer releaseRequestCtx()
	upstreamReq, err := s.buildUpstreamRequest(requestCtx, c, account, convertedBody, token, !account.IsGrok() || wantStream, promptCacheKey, isCodexCLI)
	if err != nil {
		return nil, err
	}

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	if c != nil {
		c.Set(OpsUpstreamRequestBodyKey, string(convertedBody))
	}

	// Send request
	if account.IsGrok() {
		s.applyGrokRequestJitter(ctx)
	}
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		safeErr := sanitizeUpstreamErrorMessage(err.Error())
		setOpsUpstreamError(c, 0, safeErr, "")
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: 0,
			Kind:               "request_error",
			Message:            safeErr,
		})
		c.JSON(http.StatusBadGateway, gin.H{
			"error": gin.H{
				"type":    "upstream_error",
				"message": "Upstream request failed",
			},
		})
		return nil, fmt.Errorf("upstream request failed: %s", safeErr)
	}
	if resp == nil || resp.Body == nil {
		err := errors.New("upstream request returned an empty response")
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{
			"type":    "upstream_error",
			"message": "Upstream request failed",
		}})
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	// Handle error response
	if resp.StatusCode >= 400 {
		if account.IsGrok() {
			s.updateGrokUsageSnapshot(ctx, account.ID, xai.ParseQuotaHeaders(resp.Header, resp.StatusCode))
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(respBody))
			if s.shouldFailoverGrokUpstreamError(resp.StatusCode, respBody) {
				if s.rateLimitService != nil {
					s.rateLimitService.HandleUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody)
				}
				return nil, &UpstreamFailoverError{StatusCode: resp.StatusCode, Message: sanitizeUpstreamErrorMessage(extractUpstreamErrorMessage(respBody))}
			}
			return s.handleErrorResponse(ctx, resp, c, account)
		}
		if resp.StatusCode == http.StatusBadRequest {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			_ = resp.Body.Close()

			if capacityMsg := extractOpenAICapacityMessage(respBody); capacityMsg != "" {
				return nil, s.newOpenAICapacityFailoverError(
					ctx,
					c,
					account,
					resp.Header.Get("x-request-id"),
					capacityMsg,
					string(respBody),
				)
			}

			// OAuth 模型回退：上游返回 400 且模型不可用时，降级重试（如 gpt-5.4 → gpt-5.3）
			if account.Type == AccountTypeOAuth && !modelFallbackAttempted {
				upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(respBody))
				currentModel, _ := responsesBody["model"].(string)
				fallbackModel := getOAuthModelFallback(currentModel)

				if fallbackModel != "" && isModelNotSupportedError(upstreamMsg) {
					stripped := stripCodexModelSuffix(fallbackModel)
					log.Printf("[OpenAI CC→Responses] OAuth model fallback: %s -> %s (stripped: %s) upstream=%q (account: %s)",
						currentModel, fallbackModel, stripped, upstreamMsg, account.Name)

					responsesBody["model"] = stripped
					mappedModel = stripped
					modelFallbackAttempted = true

					convertedBody, err = json.Marshal(responsesBody)
					if err != nil {
						return nil, fmt.Errorf("serialize fallback request: %w", err)
					}
					goto retryWithCCFallbackModel
				}
			}

			resp.Body = io.NopCloser(bytes.NewReader(respBody))
			return s.handleErrorResponse(ctx, resp, c, account)
		}

		if s.shouldFailoverUpstreamError(resp.StatusCode) {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(respBody))

			upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(respBody)))
			upstreamDetail := ""
			if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
				maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
				if maxBytes <= 0 {
					maxBytes = 2048
				}
				upstreamDetail = truncateString(string(respBody), maxBytes)
			}
			appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
				Platform:           account.Platform,
				AccountID:          account.ID,
				AccountName:        account.Name,
				UpstreamStatusCode: resp.StatusCode,
				UpstreamRequestID:  resp.Header.Get("x-request-id"),
				Kind:               "failover",
				Message:            upstreamMsg,
				Detail:             upstreamDetail,
			})
			s.handleFailoverSideEffects(ctx, resp, account)
			return nil, &UpstreamFailoverError{StatusCode: resp.StatusCode}
		}

		return s.handleErrorResponse(ctx, resp, c, account)
	}

	// Handle response based on client's stream preference
	var usage *OpenAIUsage
	var firstTokenMs *int

	responseModel := clientVisibleOpenAIModel(originalModel, mappedModel)
	if wantStream {
		if account.IsGrok() {
			maxLineSize := defaultMaxLineSize
			if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
				maxLineSize = s.cfg.Gateway.MaxLineSize
			}
			resp.Body = newGrokResponsesPingFilterBody(resp.Body, account, maxLineSize)
		}
		streamResult, err := s.handleCCViaResponsesStreamingResponse(ctx, resp, c, account, startTime, responseModel)
		if err != nil {
			return nil, err
		}
		usage = streamResult.usage
		firstTokenMs = streamResult.firstTokenMs
	} else {
		usage, err = s.handleCCViaResponsesNonStreamingResponse(ctx, resp, c, account, responseModel, injectedInstructions)
		if err != nil {
			return nil, err
		}
	}

	// Persist the provider-specific quota snapshot from xAI. OpenAI OAuth
	// accounts continue to use the Codex usage headers.
	if account.IsGrok() {
		s.updateGrokUsageSnapshot(ctx, account.ID, xai.ParseQuotaHeaders(resp.Header, resp.StatusCode))
	} else if snapshot := extractCodexUsageHeaders(resp.Header); snapshot != nil {
		s.updateCodexUsageSnapshot(ctx, account.ID, snapshot)
	}

	resultModel := originalModel
	if account.IsGrok() {
		resultModel = mappedModel
	}

	return &OpenAIForwardResult{
		RequestID:    resp.Header.Get("x-request-id"),
		Usage:        *usage,
		Model:        resultModel,
		ServiceTier:  resolvedServiceTier(usage, serviceTier),
		Stream:       wantStream,
		Duration:     time.Since(startTime),
		FirstTokenMs: firstTokenMs,
	}, nil
}

// handleCCViaResponsesStreamingResponse reads upstream Responses API SSE events
// and converts them to Chat Completions SSE chunks for the client.
func (s *OpenAIGatewayService) handleCCViaResponsesStreamingResponse(ctx context.Context, resp *http.Response, c *gin.Context, account *Account, startTime time.Time, originalModel string) (*openaiStreamingResult, error) {
	if s.cfg != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.cfg.Security.ResponseHeaders)
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	if v := resp.Header.Get("x-request-id"); v != "" {
		c.Header("x-request-id", v)
	}

	w := c.Writer
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}

	converter := newResponsesToCCStreamConverter(originalModel, account != nil && account.IsGrok())
	var firstTokenMs *int
	var currentEventType string

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	scanner.Buffer(make([]byte, 64*1024), maxLineSize)

	events := make(chan openAIStreamScanEvent, 16)
	done := make(chan struct{})
	sendEvent := func(ev openAIStreamScanEvent) bool {
		select {
		case events <- ev:
			return true
		case <-done:
			return false
		}
	}
	var lastReadAt int64
	atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
	go func() {
		defer close(events)
		for scanner.Scan() {
			atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
			if !sendEvent(openAIStreamScanEvent{line: scanner.Text()}) {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			_ = sendEvent(openAIStreamScanEvent{err: err})
		}
	}()
	defer close(done)

	streamInterval := time.Duration(0)
	configuredStreamInterval := 0
	if s.cfg != nil {
		configuredStreamInterval = s.cfg.Gateway.StreamDataIntervalTimeout
	}
	streamInterval = resolveGrokStreamIdleTimeout(configuredStreamInterval, account)
	if streamInterval == 0 && s.cfg != nil && s.cfg.Gateway.StreamDataIntervalTimeout > 0 {
		streamInterval = time.Duration(s.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}
	var intervalTicker *time.Ticker
	if streamInterval > 0 {
		intervalTicker = time.NewTicker(streamInterval)
		defer intervalTicker.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTicker != nil {
		intervalCh = intervalTicker.C
	}

	keepaliveInterval := time.Duration(0)
	if s.cfg != nil && s.cfg.Gateway.StreamKeepaliveInterval > 0 {
		keepaliveInterval = time.Duration(s.cfg.Gateway.StreamKeepaliveInterval) * time.Second
	}
	var keepaliveTicker *time.Ticker
	if keepaliveInterval > 0 {
		keepaliveTicker = time.NewTicker(keepaliveInterval)
		defer keepaliveTicker.Stop()
	}
	var keepaliveCh <-chan time.Time
	if keepaliveTicker != nil {
		keepaliveCh = keepaliveTicker.C
	}
	lastDataAt := time.Now()

	errorEventSent := false
	sendErrorEvent := func(reason string) {
		if errorEventSent {
			return
		}
		errorEventSent = true
		_, _ = fmt.Fprintf(w, "event: error\ndata: {\"error\":\"%s\"}\n\n", reason)
		flusher.Flush()
	}

	processLine := func(line string) error {
		lastDataAt = time.Now()

		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "event:") {
			currentEventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			return nil
		}
		if !openaiSSEDataRe.MatchString(line) {
			return nil
		}
		data := openaiSSEDataRe.ReplaceAllString(line, "")
		if data == "" || data == "[DONE]" {
			currentEventType = ""
			return nil
		}

		// Some Responses-compatible upstreams put the event name only in the
		// SSE `event:` field and omit `type` from the JSON payload. The converter
		// consumes the payload type, so restore it before conversion.
		if currentEventType != "" {
			var payload map[string]any
			if json.Unmarshal([]byte(data), &payload) == nil {
				if payloadType, _ := payload["type"].(string); strings.TrimSpace(payloadType) == "" {
					payload["type"] = currentEventType
					if encoded, err := json.Marshal(payload); err == nil {
						data = string(encoded)
					}
				}
			}
		}
		currentEventType = ""

		if firstTokenMs == nil {
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}

		ccLines := converter.convertEvent(data)
		for _, ccLine := range ccLines {
			if _, err := fmt.Fprintf(w, "%s\n\n", ccLine); err != nil {
				sendErrorEvent("write_failed")
				return err
			}
			flusher.Flush()
		}
		return nil
	}

	if shouldPreReadOpenAICapacity(account) {
		bufferedEvents, capacityMsg, streamClosed, preErr := preReadOpenAIStreamEvents(ctx, events, streamInterval)
		switch {
		case errors.Is(preErr, errOpenAIInitialStreamTimeout):
			log.Printf("Stream data interval timeout: account=%d model=%s interval=%s", account.ID, originalModel, streamInterval)
			if s.rateLimitService != nil {
				s.rateLimitService.HandleStreamTimeout(ctx, account, originalModel)
			}
			sendErrorEvent("stream_timeout")
			if account != nil && account.IsGrok() {
				return &openaiStreamingResult{usage: converter.usage, firstTokenMs: firstTokenMs}, grokStreamIdleFailoverError(account, streamInterval)
			}
			return &openaiStreamingResult{usage: converter.usage, firstTokenMs: firstTokenMs}, preErr
		case preErr != nil:
			if errors.Is(preErr, bufio.ErrTooLong) {
				log.Printf("SSE line too long: account=%d max_size=%d error=%v", account.ID, maxLineSize, preErr)
				sendErrorEvent("response_too_large")
				return &openaiStreamingResult{usage: converter.usage, firstTokenMs: firstTokenMs}, preErr
			}
			sendErrorEvent("stream_read_error")
			return &openaiStreamingResult{usage: converter.usage, firstTokenMs: firstTokenMs}, fmt.Errorf("stream read error: %w", preErr)
		case capacityMsg != "":
			return &openaiStreamingResult{usage: converter.usage, firstTokenMs: firstTokenMs}, s.newOpenAICapacityFailoverError(
				ctx,
				c,
				account,
				resp.Header.Get("x-request-id"),
				capacityMsg,
				capacityMsg,
			)
		case streamClosed:
			return &openaiStreamingResult{usage: converter.usage, firstTokenMs: firstTokenMs}, nil
		default:
			for _, ev := range bufferedEvents {
				if err := processLine(ev.line); err != nil {
					return &openaiStreamingResult{usage: converter.usage, firstTokenMs: firstTokenMs}, err
				}
			}
		}
	}

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return &openaiStreamingResult{usage: converter.usage, firstTokenMs: firstTokenMs}, nil
			}
			if ev.err != nil {
				if errors.Is(ev.err, bufio.ErrTooLong) {
					log.Printf("SSE line too long: account=%d max_size=%d error=%v", account.ID, maxLineSize, ev.err)
					sendErrorEvent("response_too_large")
					return &openaiStreamingResult{usage: converter.usage, firstTokenMs: firstTokenMs}, ev.err
				}
				sendErrorEvent("stream_read_error")
				return &openaiStreamingResult{usage: converter.usage, firstTokenMs: firstTokenMs}, fmt.Errorf("stream read error: %w", ev.err)
			}
			if err := processLine(ev.line); err != nil {
				return &openaiStreamingResult{usage: converter.usage, firstTokenMs: firstTokenMs}, err
			}

		case <-intervalCh:
			lastRead := time.Unix(0, atomic.LoadInt64(&lastReadAt))
			if time.Since(lastRead) < streamInterval {
				continue
			}
			log.Printf("Stream data interval timeout: account=%d model=%s interval=%s", account.ID, originalModel, streamInterval)
			if s.rateLimitService != nil {
				s.rateLimitService.HandleStreamTimeout(ctx, account, originalModel)
			}
			sendErrorEvent("stream_timeout")
			if account != nil && account.IsGrok() {
				return &openaiStreamingResult{usage: converter.usage, firstTokenMs: firstTokenMs}, grokStreamIdleFailoverError(account, streamInterval)
			}
			return &openaiStreamingResult{usage: converter.usage, firstTokenMs: firstTokenMs}, fmt.Errorf("stream data interval timeout")

		case <-keepaliveCh:
			if time.Since(lastDataAt) < keepaliveInterval {
				continue
			}
			if _, err := fmt.Fprint(w, ":\n\n"); err != nil {
				return &openaiStreamingResult{usage: converter.usage, firstTokenMs: firstTokenMs}, err
			}
			flusher.Flush()
		}
	}
}

// handleCCViaResponsesNonStreamingResponse reads the full upstream Responses API SSE,
// extracts the final response, converts it to CC format, and writes JSON to the client.
func (s *OpenAIGatewayService) handleCCViaResponsesNonStreamingResponse(ctx context.Context, resp *http.Response, c *gin.Context, account *Account, responseModel, injectedInstructions string) (*OpenAIUsage, error) {
	reader := io.Reader(resp.Body)
	if account != nil && account.IsGrok() {
		reader = io.LimitReader(resp.Body, grokMaxNonStreamingBodySize+1)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if account != nil && account.IsGrok() && len(body) > grokMaxNonStreamingBodySize {
		return nil, fmt.Errorf("Grok non-streaming response exceeds %d bytes", grokMaxNonStreamingBodySize)
	}

	// The upstream always returns SSE for OAuth; extract the final response JSON.
	bodyText := string(body)
	if capacityMsg := extractOpenAICapacityMessageFromSSEBody(bodyText); capacityMsg != "" {
		return nil, s.newOpenAICapacityFailoverError(
			ctx,
			c,
			account,
			resp.Header.Get("x-request-id"),
			capacityMsg,
			bodyText,
		)
	}

	// Grok's Responses endpoint returns a regular JSON document when stream is
	// disabled. Convert it directly instead of passing the Responses shape
	// through to a Chat Completions client.
	var responseObject map[string]any
	if json.Unmarshal(body, &responseObject) == nil {
		if _, hasOutput := responseObject["output"]; hasOutput {
			ccBody, usage := convertResponsesJSONToCC(body, responseModel, resp.Header.Get("x-request-id"), account != nil && account.IsGrok())
			if account != nil && account.IsGrok() {
				if parsedUsage, parsed := extractOpenAIUsageFromJSONBytes(body, true); parsed {
					usage = &parsedUsage
				}
			}
			if s.cfg != nil {
				responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.cfg.Security.ResponseHeaders)
			}
			c.Data(http.StatusOK, "application/json; charset=utf-8", ccBody)
			return usage, nil
		}
	}

	finalResponse, ok := extractCodexFinalResponse(bodyText, injectedInstructions)

	if !ok {
		// Fallback: try to parse usage from SSE body
		usage := s.parseSSEUsageFromBody(bodyText, account != nil && account.IsGrok())
		// Return the raw body as-is (best effort)
		if s.cfg != nil {
			responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.cfg.Security.ResponseHeaders)
		}
		c.Data(resp.StatusCode, "application/json; charset=utf-8", body)
		return usage, nil
	}

	// Convert the Responses API JSON to CC format
	ccBody, usage := convertResponsesJSONToCC(finalResponse, responseModel, resp.Header.Get("x-request-id"), account != nil && account.IsGrok())
	if account != nil && account.IsGrok() {
		if parsedUsage, parsed := extractOpenAIUsageFromJSONBytes(finalResponse, true); parsed {
			usage = &parsedUsage
		}
	}

	if s.cfg != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.cfg.Security.ResponseHeaders)
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", ccBody)

	return usage, nil
}
