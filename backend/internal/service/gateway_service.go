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
	"os"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/gin-gonic/gin"
)

const (
	claudeAPIURL            = "https://api.anthropic.com/v1/messages?beta=true"
	claudeAPICountTokensURL = "https://api.anthropic.com/v1/messages/count_tokens?beta=true"
	stickySessionTTL        = time.Hour // 粘性会话TTL
	defaultMaxLineSize      = 40 * 1024 * 1024
	claudeCodeSystemPrompt  = "You are Claude Code, Anthropic's official CLI for Claude."
	maxCacheControlBlocks   = 4 // Anthropic API 允许的最大 cache_control 块数量
)

func (s *GatewayService) debugModelRoutingEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("SUB2API_DEBUG_MODEL_ROUTING")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// accountTypePriority 返回账号类型的优先级（数值越小优先级越高）
// OAuth = SetupToken > API Key
func accountTypePriority(accountType string) int {
	switch accountType {
	case AccountTypeOAuth:
		return 1
	case AccountTypeSetupToken:
		return 1 // 与 OAuth 平级
	case AccountTypeAPIKey:
		return 2
	default:
		return 999 // 未知类型最低优先级
	}
}

func shortSessionHash(sessionHash string) string {
	if sessionHash == "" {
		return ""
	}
	if len(sessionHash) <= 8 {
		return sessionHash
	}
	return sessionHash[:8]
}

// sseDataRe matches SSE data lines with optional whitespace after colon.
// Some upstream APIs return non-standard "data:" without space (should be "data: ").
var (
	sseDataRe            = regexp.MustCompile(`^data:\s*`)
	sessionIDRegex       = regexp.MustCompile(`session_([a-f0-9-]{36})`)
	claudeCliUserAgentRe = regexp.MustCompile(`^claude-cli/\d+\.\d+\.\d+`)

	// claudeCodePromptPrefixes 用于检测 Claude Code 系统提示词的前缀列表
	// 支持多种变体：标准版、Agent SDK 版、Explore Agent 版、Compact 版等
	// 注意：前缀之间不应存在包含关系，否则会导致冗余匹配
	claudeCodePromptPrefixes = []string{
		"You are Claude Code, Anthropic's official CLI for Claude",             // 标准版 & Agent SDK 版（含 running within...）
		"You are a Claude agent, built on Anthropic's Claude Agent SDK",        // Agent SDK 变体
		"You are a file search specialist for Claude Code",                     // Explore Agent 版
		"You are a helpful AI assistant tasked with summarizing conversations", // Compact 版
	}
)

// ErrClaudeCodeOnly 表示分组仅允许 Claude Code 客户端访问
var ErrClaudeCodeOnly = errors.New("this group only allows Claude Code clients")

// ErrModelNotSupported 表示请求的模型不被分组内任何账号支持（用户用错模型，
// 例如在 OpenAI 分组调用 Claude 模型，或请求了第三方不支持的模型）。
// 这与「账号都在限流/忙」的临时性枯竭不同：后者仍返回 no available accounts（503），
// 前者是客户端请求错误，应返回 400 且不计入运维错误监控。
var ErrModelNotSupported = errors.New("requested model is not supported by any account in this group")

// allowedHeaders 白名单headers（参考CRS项目）
var allowedHeaders = map[string]bool{
	"accept":                                    true,
	"x-stainless-retry-count":                   true,
	"x-stainless-timeout":                       true,
	"x-stainless-lang":                          true,
	"x-stainless-package-version":               true,
	"x-stainless-os":                            true,
	"x-stainless-arch":                          true,
	"x-stainless-runtime":                       true,
	"x-stainless-runtime-version":               true,
	"x-stainless-helper-method":                 true,
	"anthropic-dangerous-direct-browser-access": true,
	"anthropic-version":                         true,
	"x-app":                                     true,
	"anthropic-beta":                            true,
	"accept-language":                           true,
	"sec-fetch-mode":                            true,
	"user-agent":                                true,
	"content-type":                              true,
}

// GatewayCache defines cache operations for gateway service
type GatewayCache interface {
	GetSessionAccountID(ctx context.Context, groupID int64, sessionHash string) (int64, error)
	SetSessionAccountID(ctx context.Context, groupID int64, sessionHash string, accountID int64, ttl time.Duration) error
	RefreshSessionTTL(ctx context.Context, groupID int64, sessionHash string, ttl time.Duration) error
	DeleteSessionAccountID(ctx context.Context, groupID int64, sessionHash string) error
}

// derefGroupID safely dereferences *int64 to int64, returning 0 if nil
func derefGroupID(groupID *int64) int64 {
	if groupID == nil {
		return 0
	}
	return *groupID
}

type AccountWaitPlan struct {
	AccountID      int64
	MaxConcurrency int
	Timeout        time.Duration
	MaxWaiting     int
}

type AccountSelectionResult struct {
	Account     *Account
	Acquired    bool
	ReleaseFunc func()
	WaitPlan    *AccountWaitPlan // nil means no wait allowed
}

// ClaudeUsage 表示Claude API返回的usage信息
type ClaudeUsage struct {
	InputTokens              int     `json:"input_tokens"`
	OutputTokens             int     `json:"output_tokens"`
	CacheCreationInputTokens int     `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int     `json:"cache_read_input_tokens"`
	CacheCreation5mTokens    int     `json:"-"`
	CacheCreation1hTokens    int     `json:"-"`
	ContextUsagePercent      float64 `json:"context_usage_percent,omitempty"`
}

// ForwardResult 转发结果
type ForwardResult struct {
	RequestID        string
	Usage            ClaudeUsage
	Model            string
	Stream           bool
	Duration         time.Duration
	FirstTokenMs     *int // 首字时间（流式请求）
	ClientDisconnect bool // 客户端是否在流式传输过程中断开

	// 图片生成计费字段（仅 gemini-3-pro-image 使用）
	ImageCount int    // 生成的图片数量
	ImageSize  string // 图片尺寸 "1K", "2K", "4K"
}

// UpstreamFailoverError indicates an upstream error that should trigger account failover.
type UpstreamFailoverError struct {
	StatusCode int
	Message    string // upstream error message (sanitized), preserved across failover for final response
}

func (e *UpstreamFailoverError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("upstream error: %d message=%s (failover)", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("upstream error: %d (failover)", e.StatusCode)
}

// ContextTooLongError indicates the input context exceeds the platform's limit.
// This error is used to reject requests before sending to upstream to avoid 400 errors.
type ContextTooLongError struct {
	EstimatedTokens int
	Limit           int
}

func (e *ContextTooLongError) Error() string {
	return fmt.Sprintf("context too long: estimated %d tokens exceeds limit of %d", e.EstimatedTokens, e.Limit)
}

// GatewayService handles API gateway operations
type GatewayService struct {
	accountRepo         AccountRepository
	groupRepo           GroupRepository
	usageLogRepo        UsageLogRepository
	userRepo            UserRepository
	userSubRepo         UserSubscriptionRepository
	cache               GatewayCache
	cfg                 *config.Config
	schedulerSnapshot   *SchedulerSnapshotService
	billingService      *BillingService
	rateLimitService    *RateLimitService
	billingCacheService *BillingCacheService
	identityService     *IdentityService
	httpUpstream        HTTPUpstream
	deferredService     *DeferredService
	concurrencyService  *ConcurrencyService
	claudeTokenProvider *ClaudeTokenProvider
	kiroTokenProvider   *KiroTokenProvider         // Kiro token provider for checking runtime status
	sessionLimitCache   SessionLimitCache          // 会话数量限制缓存（仅 Anthropic OAuth/SetupToken）
	usageCache          *UsageCache                // 用量缓存，用于账号选择时检查配额
	accountUsageService *AccountUsageService       // 账号用量服务，用于主动刷新配额
	tempAPIKeyRepo      TempAPIKeyRepository       // 临时 API Key 仓库，用于更新 quota_only 消费金额
	apiKeyRepo          APIKeyRepository           // API Key 仓库，用于更新 quota_used_usd
	apiKeyCacheInval    APIKeyAuthCacheInvalidator // API Key 认证缓存失效器
}

// NewGatewayService creates a new GatewayService
func NewGatewayService(
	accountRepo AccountRepository,
	groupRepo GroupRepository,
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
	identityService *IdentityService,
	httpUpstream HTTPUpstream,
	deferredService *DeferredService,
	claudeTokenProvider *ClaudeTokenProvider,
	kiroTokenProvider *KiroTokenProvider,
	sessionLimitCache SessionLimitCache,
	usageCache *UsageCache,
	accountUsageService *AccountUsageService,
	tempAPIKeyRepo TempAPIKeyRepository,
	apiKeyRepo APIKeyRepository,
	apiKeyCacheInval APIKeyAuthCacheInvalidator,
) *GatewayService {
	return &GatewayService{
		accountRepo:         accountRepo,
		groupRepo:           groupRepo,
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
		identityService:     identityService,
		httpUpstream:        httpUpstream,
		deferredService:     deferredService,
		claudeTokenProvider: claudeTokenProvider,
		kiroTokenProvider:   kiroTokenProvider,
		sessionLimitCache:   sessionLimitCache,
		usageCache:          usageCache,
		accountUsageService: accountUsageService,
		tempAPIKeyRepo:      tempAPIKeyRepo,
		apiKeyRepo:          apiKeyRepo,
		apiKeyCacheInval:    apiKeyCacheInval,
	}
}

// systemBlockFilterPrefixes 需要从 system 中过滤的文本前缀列表
// OAuth/SetupToken 账号转发时，匹配这些前缀的 system 元素会被移除
var systemBlockFilterPrefixes = []string{
	"x-anthropic-billing-header",
}

// GenerateSessionHash 从预解析请求计算粘性会话 hash
// conversationID: 可选，来自请求 header X-Conversation-ID，优先级最高
func (s *GatewayService) GenerateSessionHash(parsed *ParsedRequest, conversationID string) string {
	if parsed == nil {
		return ""
	}

	// 0. 最高优先级：使用请求 header 中的 X-Conversation-ID
	if conversationID != "" {
		return s.HashContent(conversationID)
	}

	// 1. 次高优先级：从 metadata.user_id 提取 session_xxx
	if parsed.MetadataUserID != "" {
		if match := sessionIDRegex.FindStringSubmatch(parsed.MetadataUserID); len(match) > 1 {
			return match[1]
		}
	}

	// 2. 提取带 cache_control: {type: "ephemeral"} 的内容
	cacheableContent := s.extractCacheableContent(parsed)
	if cacheableContent != "" {
		return s.HashContent(cacheableContent)
	}

	// 3. Fallback: 使用 system 内容
	if parsed.System != nil {
		systemText := s.extractTextFromSystem(parsed.System)
		if systemText != "" {
			return s.HashContent(systemText)
		}
	}

	// 4. 最后 fallback: 使用第一条消息
	if len(parsed.Messages) > 0 {
		if firstMsg, ok := parsed.Messages[0].(map[string]any); ok {
			msgText := s.extractTextFromContent(firstMsg["content"])
			if msgText != "" {
				return s.HashContent(msgText)
			}
		}
	}

	return ""
}

// BindStickySession sets session -> account binding with standard TTL.
func (s *GatewayService) BindStickySession(ctx context.Context, groupID *int64, sessionHash string, accountID int64) error {
	if sessionHash == "" || accountID <= 0 || s.cache == nil {
		return nil
	}
	return s.cache.SetSessionAccountID(ctx, derefGroupID(groupID), sessionHash, accountID, stickySessionTTL)
}

// GetCachedSessionAccountID returns the sticky account currently bound to a session.
func (s *GatewayService) GetCachedSessionAccountID(ctx context.Context, groupID *int64, sessionHash string) (int64, error) {
	if sessionHash == "" || s.cache == nil {
		return 0, nil
	}
	return s.cache.GetSessionAccountID(ctx, derefGroupID(groupID), sessionHash)
}

// InvalidateStickySession removes the sticky session binding so the next
// request with the same session hash will not be pinned to a failed account.
func (s *GatewayService) InvalidateStickySession(ctx context.Context, groupID *int64, sessionHash string) {
	if sessionHash == "" || s.cache == nil {
		return
	}
	_ = s.cache.DeleteSessionAccountID(ctx, derefGroupID(groupID), sessionHash)
}

func (s *GatewayService) extractCacheableContent(parsed *ParsedRequest) string {
	if parsed == nil {
		return ""
	}

	var builder strings.Builder

	// 检查 system 中的 cacheable 内容
	if system, ok := parsed.System.([]any); ok {
		for _, part := range system {
			if partMap, ok := part.(map[string]any); ok {
				if cc, ok := partMap["cache_control"].(map[string]any); ok {
					if cc["type"] == "ephemeral" {
						if text, ok := partMap["text"].(string); ok {
							_, _ = builder.WriteString(text)
						}
					}
				}
			}
		}
	}
	systemText := builder.String()

	// 检查 messages 中的 cacheable 内容
	for _, msg := range parsed.Messages {
		if msgMap, ok := msg.(map[string]any); ok {
			if msgContent, ok := msgMap["content"].([]any); ok {
				for _, part := range msgContent {
					if partMap, ok := part.(map[string]any); ok {
						if cc, ok := partMap["cache_control"].(map[string]any); ok {
							if cc["type"] == "ephemeral" {
								return s.extractTextFromContent(msgMap["content"])
							}
						}
					}
				}
			}
		}
	}

	return systemText
}

func (s *GatewayService) extractTextFromSystem(system any) string {
	switch v := system.(type) {
	case string:
		return v
	case []any:
		var texts []string
		for _, part := range v {
			if partMap, ok := part.(map[string]any); ok {
				if text, ok := partMap["text"].(string); ok {
					texts = append(texts, text)
				}
			}
		}
		return strings.Join(texts, "")
	}
	return ""
}

func (s *GatewayService) extractTextFromContent(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var texts []string
		for _, part := range v {
			if partMap, ok := part.(map[string]any); ok {
				if partMap["type"] == "text" {
					if text, ok := partMap["text"].(string); ok {
						texts = append(texts, text)
					}
				}
			}
		}
		return strings.Join(texts, "")
	}
	return ""
}

// HashContent computes a truncated SHA256 hash (32 hex chars) for session affinity keys.
func (s *GatewayService) HashContent(content string) string {
	hash := sha256.Sum256([]byte(content))
	return hex.EncodeToString(hash[:16]) // 32字符
}

// replaceModelInBody 替换请求体中的model字段
// 使用 json.RawMessage 保留其他字段的原始字节，避免 thinking 块等内容被修改
func (s *GatewayService) replaceModelInBody(body []byte, newModel string) []byte {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}
	modelBytes, err := json.Marshal(newModel)
	if err != nil {
		return body
	}
	req["model"] = modelBytes
	newBody, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return newBody
}

// SelectAccount 选择账号（粘性会话+优先级）
func (s *GatewayService) SelectAccount(ctx context.Context, groupID *int64, sessionHash string) (*Account, error) {
	return s.SelectAccountForModel(ctx, groupID, sessionHash, "")
}

// SelectAccountForModel 选择支持指定模型的账号（粘性会话+优先级+模型映射）
func (s *GatewayService) SelectAccountForModel(ctx context.Context, groupID *int64, sessionHash string, requestedModel string) (*Account, error) {
	return s.SelectAccountForModelWithExclusions(ctx, groupID, sessionHash, requestedModel, nil)
}

// SelectAccountForModelWithExclusions selects an account supporting the requested model while excluding specified accounts.
func (s *GatewayService) SelectAccountForModelWithExclusions(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}) (*Account, error) {
	// 优先检查 context 中的强制平台（/antigravity 路由）
	var platform string
	forcePlatform, hasForcePlatform := ctx.Value(ctxkey.ForcePlatform).(string)
	if hasForcePlatform && forcePlatform != "" {
		platform = forcePlatform
	} else if groupID != nil {
		group, resolvedGroupID, err := s.resolveGatewayGroup(ctx, groupID)
		if err != nil {
			return nil, err
		}
		groupID = resolvedGroupID
		ctx = s.withGroupContext(ctx, group)
		platform = group.Platform
	} else {
		// 无分组时只使用原生 anthropic 平台
		platform = PlatformAnthropic
	}

	// anthropic/gemini 分组支持混合调度（包含启用了 mixed_scheduling 的 antigravity 账户）
	// 注意：强制平台模式不走混合调度
	if (platform == PlatformAnthropic || platform == PlatformGemini) && !hasForcePlatform {
		return s.selectAccountWithMixedScheduling(ctx, groupID, sessionHash, requestedModel, excludedIDs, platform)
	}

	// antigravity 分组、强制平台模式或无分组使用单平台选择
	// 注意：强制平台模式也必须遵守分组限制，不再回退到全平台查询
	return s.selectAccountForModelWithPlatform(ctx, groupID, sessionHash, requestedModel, excludedIDs, platform)
}

// accountWithLoad wraps Account with its load information for load-aware sorting.
type accountWithLoad struct {
	account  *Account
	loadInfo *AccountLoadInfo
}

// SelectAccountWithLoadAwareness selects account with load-awareness and wait plan.
// metadataUserID: 原始 metadata.user_id 字段（用于提取会话 UUID 进行会话数量限制）
func (s *GatewayService) SelectAccountWithLoadAwareness(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}, metadataUserID string) (*AccountSelectionResult, error) {
	cfg := s.schedulingConfig()
	// 提取会话 UUID（用于会话数量限制）
	sessionUUID := extractSessionUUID(metadataUserID)

	var stickyAccountID int64
	if sessionHash != "" && s.cache != nil {
		if accountID, err := s.cache.GetSessionAccountID(ctx, derefGroupID(groupID), sessionHash); err == nil {
			stickyAccountID = accountID
		}
	}

	// 检查 Claude Code 客户端限制（可能会替换 groupID 为降级分组）
	group, groupID, err := s.checkClaudeCodeRestriction(ctx, groupID)
	if err != nil {
		return nil, err
	}
	ctx = s.withGroupContext(ctx, group)

	if s.debugModelRoutingEnabled() && requestedModel != "" {
		groupPlatform := ""
		if group != nil {
			groupPlatform = group.Platform
		}
		log.Printf("[ModelRoutingDebug] select entry: group_id=%v group_platform=%s model=%s session=%s sticky_account=%d load_batch=%v concurrency=%v",
			derefGroupID(groupID), groupPlatform, requestedModel, shortSessionHash(sessionHash), stickyAccountID, cfg.LoadBatchEnabled, s.concurrencyService != nil)
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

	platform, hasForcePlatform, err := s.resolvePlatform(ctx, groupID, group)
	if err != nil {
		return nil, err
	}
	preferOAuth := platform == PlatformGemini
	if s.debugModelRoutingEnabled() && platform == PlatformAnthropic && requestedModel != "" {
		log.Printf("[ModelRoutingDebug] load-aware enabled: group_id=%v model=%s session=%s platform=%s", derefGroupID(groupID), requestedModel, shortSessionHash(sessionHash), platform)
	}

	accounts, useMixed, err := s.listSchedulableAccounts(ctx, groupID, platform, hasForcePlatform)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		if notSupportedErr := s.errIfModelNotSupportedInGroup(ctx, groupID, platform, hasForcePlatform, requestedModel); notSupportedErr != nil {
			return nil, notSupportedErr
		}
		return nil, errors.New("no available accounts")
	}

	// Filter out Kiro accounts that are not available (cooldown/banned/exhausted)
	if s.kiroTokenProvider != nil {
		filteredAccounts := make([]Account, 0, len(accounts))
		var unavailableKiroAccounts []Account
		for i := range accounts {
			acc := &accounts[i]
			if acc.IsKiro() {
				if !s.kiroTokenProvider.IsAvailable(acc.ID) {
					unavailableKiroAccounts = append(unavailableKiroAccounts, accounts[i])
					continue // Skip unavailable Kiro accounts
				}
			}
			filteredAccounts = append(filteredAccounts, accounts[i])
		}
		accounts = filteredAccounts
		if len(accounts) == 0 && len(unavailableKiroAccounts) > 0 {
			// All Kiro accounts are unavailable, attempt force recovery
			log.Printf("[GatewayService] All %d Kiro accounts unavailable, attempting force refresh recovery...", len(unavailableKiroAccounts))
			recovered, err := s.kiroTokenProvider.ForceRefreshWithRetry(ctx, unavailableKiroAccounts, 3)
			if err != nil {
				return nil, fmt.Errorf("no available accounts (force refresh recovery failed): %w", err)
			}
			accounts = []Account{*recovered}
		} else if len(accounts) == 0 {
			return nil, errors.New("no available accounts")
		}
	}

	isExcluded := func(accountID int64) bool {
		if excludedIDs == nil {
			return false
		}
		_, excluded := excludedIDs[accountID]
		return excluded
	}

	// 提前构建 accountByID（供 Layer 1 和 Layer 1.5 使用）
	accountByID := make(map[int64]*Account, len(accounts))
	for i := range accounts {
		accountByID[accounts[i].ID] = &accounts[i]
	}

	// 获取模型路由配置（仅 anthropic 平台）
	var routingAccountIDs []int64
	if group != nil && requestedModel != "" && group.Platform == PlatformAnthropic {
		routingAccountIDs = group.GetRoutingAccountIDs(requestedModel)
		if s.debugModelRoutingEnabled() {
			log.Printf("[ModelRoutingDebug] context group routing: group_id=%d model=%s enabled=%v rules=%d matched_ids=%v session=%s sticky_account=%d",
				group.ID, requestedModel, group.ModelRoutingEnabled, len(group.ModelRouting), routingAccountIDs, shortSessionHash(sessionHash), stickyAccountID)
			if len(routingAccountIDs) == 0 && group.ModelRoutingEnabled && len(group.ModelRouting) > 0 {
				keys := make([]string, 0, len(group.ModelRouting))
				for k := range group.ModelRouting {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				const maxKeys = 20
				if len(keys) > maxKeys {
					keys = keys[:maxKeys]
				}
				log.Printf("[ModelRoutingDebug] context group routing miss: group_id=%d model=%s patterns(sample)=%v", group.ID, requestedModel, keys)
			}
		}
	}

	// ============ Layer 1: 模型路由优先选择（优先级高于粘性会话） ============
	if len(routingAccountIDs) > 0 && s.concurrencyService != nil {
		// 1. 过滤出路由列表中可调度的账号
		var routingCandidates []*Account
		var quotaUnhealthyCandidates []*Account // 配额不健康但其他条件满足的账号（用于兜底）
		var filteredExcluded, filteredMissing, filteredUnsched, filteredPlatform, filteredModelScope, filteredModelMapping, filteredWindowCost, filteredQuota int
		for _, routingAccountID := range routingAccountIDs {
			if isExcluded(routingAccountID) {
				filteredExcluded++
				continue
			}
			account, ok := accountByID[routingAccountID]
			if !ok || !account.IsSchedulable() {
				if !ok {
					filteredMissing++
				} else {
					filteredUnsched++
				}
				continue
			}
			if !s.isAccountAllowedForPlatform(account, platform, useMixed) {
				filteredPlatform++
				continue
			}
			if !account.IsSchedulableForModel(requestedModel) {
				filteredModelScope++
				continue
			}
			if requestedModel != "" && !s.isModelSupportedByAccount(account, requestedModel) {
				filteredModelMapping++
				continue
			}
			// 窗口费用检查（非粘性会话路径）
			if !s.isAccountSchedulableForWindowCost(ctx, account, false) {
				filteredWindowCost++
				continue
			}
			// 配额检查（Antigravity 账号）- 软过滤，不健康的账号保留用于兜底
			if !s.isAccountQuotaAvailable(account) {
				filteredQuota++
				quotaUnhealthyCandidates = append(quotaUnhealthyCandidates, account)
				continue
			}
			routingCandidates = append(routingCandidates, account)
		}

		// 如果所有健康账号都被过滤掉，使用配额不健康的账号作为兜底
		if len(routingCandidates) == 0 && len(quotaUnhealthyCandidates) > 0 {
			routingCandidates = quotaUnhealthyCandidates
			if s.debugModelRoutingEnabled() {
				log.Printf("[ModelRoutingDebug] routed fallback to quota-unhealthy accounts: group_id=%v model=%s count=%d",
					derefGroupID(groupID), requestedModel, len(quotaUnhealthyCandidates))
			}
		}

		if s.debugModelRoutingEnabled() {
			log.Printf("[ModelRoutingDebug] routed candidates: group_id=%v model=%s routed=%d candidates=%d filtered(excluded=%d missing=%d unsched=%d platform=%d model_scope=%d model_mapping=%d window_cost=%d quota=%d)",
				derefGroupID(groupID), requestedModel, len(routingAccountIDs), len(routingCandidates),
				filteredExcluded, filteredMissing, filteredUnsched, filteredPlatform, filteredModelScope, filteredModelMapping, filteredWindowCost, filteredQuota)
		}

		if len(routingCandidates) > 0 {
			// 1.5. 在路由账号范围内检查粘性会话
			if sessionHash != "" && s.cache != nil {
				stickyAccountID, err := s.cache.GetSessionAccountID(ctx, derefGroupID(groupID), sessionHash)
				if err == nil && stickyAccountID > 0 && containsInt64(routingAccountIDs, stickyAccountID) && !isExcluded(stickyAccountID) {
					// 粘性账号在路由列表中，优先使用
					if stickyAccount, ok := accountByID[stickyAccountID]; ok {
						if stickyAccount.IsSchedulable() &&
							s.isAccountAllowedForPlatform(stickyAccount, platform, useMixed) &&
							stickyAccount.IsSchedulableForModel(requestedModel) &&
							(requestedModel == "" || s.isModelSupportedByAccount(stickyAccount, requestedModel)) &&
							s.isAccountSchedulableForWindowCost(ctx, stickyAccount, true) && // 粘性会话窗口费用检查
							s.isAccountQuotaAvailable(stickyAccount) { // 配额检查
							result, err := s.tryAcquireAccountSlot(ctx, stickyAccountID, stickyAccount.Concurrency)
							if err == nil && result.Acquired {
								// 会话数量限制检查
								if !s.checkAndRegisterSession(ctx, stickyAccount, sessionUUID) {
									result.ReleaseFunc() // 释放槽位
									// 继续到负载感知选择
								} else {
									_ = s.cache.RefreshSessionTTL(ctx, derefGroupID(groupID), sessionHash, stickySessionTTL)
									if s.debugModelRoutingEnabled() {
										log.Printf("[ModelRoutingDebug] routed sticky hit: group_id=%v model=%s session=%s account=%d", derefGroupID(groupID), requestedModel, shortSessionHash(sessionHash), stickyAccountID)
									}
									return &AccountSelectionResult{
										Account:     stickyAccount,
										Acquired:    true,
										ReleaseFunc: result.ReleaseFunc,
									}, nil
								}
							}

							waitingCount, _ := s.concurrencyService.GetAccountWaitingCount(ctx, stickyAccountID)
							if waitingCount < cfg.StickySessionMaxWaiting {
								return &AccountSelectionResult{
									Account: stickyAccount,
									WaitPlan: &AccountWaitPlan{
										AccountID:      stickyAccountID,
										MaxConcurrency: stickyAccount.Concurrency,
										Timeout:        cfg.StickySessionWaitTimeout,
										MaxWaiting:     cfg.StickySessionMaxWaiting,
									},
								}, nil
							}
							// 粘性账号槽位满且等待队列已满，继续使用负载感知选择
						}
					}
				}
			}

			// 2. 批量获取负载信息
			routingLoads := make([]AccountWithConcurrency, 0, len(routingCandidates))
			for _, acc := range routingCandidates {
				routingLoads = append(routingLoads, AccountWithConcurrency{
					ID:             acc.ID,
					MaxConcurrency: acc.Concurrency,
				})
			}
			routingLoadMap, _ := s.concurrencyService.GetAccountsLoadBatch(ctx, routingLoads)

			// 3. 按负载感知排序
			var routingAvailable []accountWithLoad
			for _, acc := range routingCandidates {
				loadInfo := routingLoadMap[acc.ID]
				if loadInfo == nil {
					loadInfo = &AccountLoadInfo{AccountID: acc.ID}
				}
				if loadInfo.LoadRate < 100 {
					routingAvailable = append(routingAvailable, accountWithLoad{account: acc, loadInfo: loadInfo})
				}
			}

			if len(routingAvailable) > 0 {
				// 排序：优先级 > 负载率 > [Kiro积分加权随机] > 重置时间（临近重置优先） > 最后使用时间
				s.sortAccountWithLoadCandidates(routingAvailable, false)

				// 4. 尝试获取槽位
				for _, item := range routingAvailable {
					result, err := s.tryAcquireAccountSlot(ctx, item.account.ID, item.account.Concurrency)
					if err == nil && result.Acquired {
						// 会话数量限制检查
						if !s.checkAndRegisterSession(ctx, item.account, sessionUUID) {
							result.ReleaseFunc() // 释放槽位，继续尝试下一个账号
							continue
						}
						if sessionHash != "" && s.cache != nil {
							_ = s.cache.SetSessionAccountID(ctx, derefGroupID(groupID), sessionHash, item.account.ID, stickySessionTTL)
						}
						if s.debugModelRoutingEnabled() {
							log.Printf("[ModelRoutingDebug] routed select: group_id=%v model=%s session=%s account=%d", derefGroupID(groupID), requestedModel, shortSessionHash(sessionHash), item.account.ID)
						}
						return &AccountSelectionResult{
							Account:     item.account,
							Acquired:    true,
							ReleaseFunc: result.ReleaseFunc,
						}, nil
					}
				}

				// 5. 所有路由账号槽位满，返回等待计划（选择负载最低的）
				acc := routingAvailable[0].account
				if s.debugModelRoutingEnabled() {
					log.Printf("[ModelRoutingDebug] routed wait: group_id=%v model=%s session=%s account=%d", derefGroupID(groupID), requestedModel, shortSessionHash(sessionHash), acc.ID)
				}
				return &AccountSelectionResult{
					Account: acc,
					WaitPlan: &AccountWaitPlan{
						AccountID:      acc.ID,
						MaxConcurrency: acc.Concurrency,
						Timeout:        cfg.StickySessionWaitTimeout,
						MaxWaiting:     cfg.StickySessionMaxWaiting,
					},
				}, nil
			}
			// 路由列表中的账号都不可用（负载率 >= 100），继续到 Layer 2 回退
			log.Printf("[ModelRouting] All routed accounts unavailable for model=%s, falling back to normal selection", requestedModel)
		}
	}

	// ============ Layer 1.5: 粘性会话（仅在无模型路由配置时生效） ============
	if len(routingAccountIDs) == 0 && sessionHash != "" && s.cache != nil {
		accountID, err := s.cache.GetSessionAccountID(ctx, derefGroupID(groupID), sessionHash)
		if err == nil && accountID > 0 && !isExcluded(accountID) {
			account, ok := accountByID[accountID]
			if ok && s.isAccountInGroup(account, groupID) &&
				s.isAccountAllowedForPlatform(account, platform, useMixed) &&
				account.IsSchedulableForModel(requestedModel) &&
				(requestedModel == "" || s.isModelSupportedByAccount(account, requestedModel)) &&
				s.isAccountSchedulableForWindowCost(ctx, account, true) && // 粘性会话窗口费用检查
				s.isAccountQuotaAvailable(account) { // 配额检查
				result, err := s.tryAcquireAccountSlot(ctx, accountID, account.Concurrency)
				if err == nil && result.Acquired {
					// 会话数量限制检查
					if !s.checkAndRegisterSession(ctx, account, sessionUUID) {
						result.ReleaseFunc() // 释放槽位，继续到 Layer 2
					} else {
						_ = s.cache.RefreshSessionTTL(ctx, derefGroupID(groupID), sessionHash, stickySessionTTL)
						return &AccountSelectionResult{
							Account:     account,
							Acquired:    true,
							ReleaseFunc: result.ReleaseFunc,
						}, nil
					}
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

	// ============ Layer 2: 负载感知选择 ============
	candidates := make([]*Account, 0, len(accounts))
	var layer2QuotaUnhealthy []*Account // 配额不健康但其他条件满足的账号（用于兜底）
	for i := range accounts {
		acc := &accounts[i]
		if isExcluded(acc.ID) {
			continue
		}
		// Scheduler snapshots can be temporarily stale (bucket rebuild is throttled);
		// re-check schedulability here so recently rate-limited/overloaded accounts
		// are not selected again before the bucket is rebuilt.
		if !acc.IsSchedulable() {
			continue
		}
		if !s.isAccountAllowedForPlatform(acc, platform, useMixed) {
			continue
		}
		if !acc.IsSchedulableForModel(requestedModel) {
			continue
		}
		if requestedModel != "" && !s.isModelSupportedByAccount(acc, requestedModel) {
			continue
		}
		// 窗口费用检查（非粘性会话路径）
		if !s.isAccountSchedulableForWindowCost(ctx, acc, false) {
			continue
		}
		// 配额检查（Antigravity 账号）- 软过滤，不健康的账号保留用于兜底
		if !s.isAccountQuotaAvailable(acc) {
			layer2QuotaUnhealthy = append(layer2QuotaUnhealthy, acc)
			continue
		}
		candidates = append(candidates, acc)
	}

	// 如果所有健康账号都被过滤掉，使用配额不健康的账号作为兜底
	if len(candidates) == 0 && len(layer2QuotaUnhealthy) > 0 {
		candidates = layer2QuotaUnhealthy
		if s.debugModelRoutingEnabled() {
			log.Printf("[ModelRoutingDebug] layer2 fallback to quota-unhealthy accounts: group_id=%v model=%s count=%d",
				derefGroupID(groupID), requestedModel, len(layer2QuotaUnhealthy))
		}
	}

	if len(candidates) == 0 {
		if notSupportedErr := s.errIfModelNotSupportedInGroup(ctx, groupID, platform, hasForcePlatform, requestedModel); notSupportedErr != nil {
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
		if result, ok := s.tryAcquireByLegacyOrder(ctx, candidates, groupID, sessionHash, preferOAuth, sessionUUID); ok {
			return result, nil
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
			s.sortAccountWithLoadCandidates(available, preferOAuth)

			for _, item := range available {
				result, err := s.tryAcquireAccountSlot(ctx, item.account.ID, item.account.Concurrency)
				if err == nil && result.Acquired {
					// 会话数量限制检查
					if !s.checkAndRegisterSession(ctx, item.account, sessionUUID) {
						result.ReleaseFunc() // 释放槽位，继续尝试下一个账号
						continue
					}
					if sessionHash != "" && s.cache != nil {
						_ = s.cache.SetSessionAccountID(ctx, derefGroupID(groupID), sessionHash, item.account.ID, stickySessionTTL)
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

	// ============ Layer 3: 兜底排队 ============
	s.sortAccountsByPriorityAndLastUsed(candidates, preferOAuth)
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

func (s *GatewayService) tryAcquireByLegacyOrder(ctx context.Context, candidates []*Account, groupID *int64, sessionHash string, preferOAuth bool, sessionUUID string) (*AccountSelectionResult, bool) {
	ordered := append([]*Account(nil), candidates...)
	s.sortAccountsByPriorityAndLastUsed(ordered, preferOAuth)

	for _, acc := range ordered {
		result, err := s.tryAcquireAccountSlot(ctx, acc.ID, acc.Concurrency)
		if err == nil && result.Acquired {
			// 会话数量限制检查
			if !s.checkAndRegisterSession(ctx, acc, sessionUUID) {
				result.ReleaseFunc() // 释放槽位，继续尝试下一个账号
				continue
			}
			if sessionHash != "" && s.cache != nil {
				_ = s.cache.SetSessionAccountID(ctx, derefGroupID(groupID), sessionHash, acc.ID, stickySessionTTL)
			}
			return &AccountSelectionResult{
				Account:     acc,
				Acquired:    true,
				ReleaseFunc: result.ReleaseFunc,
			}, true
		}
	}

	return nil, false
}

func (s *GatewayService) schedulingConfig() config.GatewaySchedulingConfig {
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

func (s *GatewayService) withGroupContext(ctx context.Context, group *Group) context.Context {
	if !IsGroupContextValid(group) {
		return ctx
	}
	if existing, ok := ctx.Value(ctxkey.Group).(*Group); ok && existing != nil && existing.ID == group.ID && IsGroupContextValid(existing) {
		return ctx
	}
	return context.WithValue(ctx, ctxkey.Group, group)
}

func (s *GatewayService) groupFromContext(ctx context.Context, groupID int64) *Group {
	if group, ok := ctx.Value(ctxkey.Group).(*Group); ok && IsGroupContextValid(group) && group.ID == groupID {
		return group
	}
	return nil
}

func (s *GatewayService) resolveGroupByID(ctx context.Context, groupID int64) (*Group, error) {
	if group := s.groupFromContext(ctx, groupID); group != nil {
		return group, nil
	}
	group, err := s.groupRepo.GetByIDLite(ctx, groupID)
	if err != nil {
		return nil, fmt.Errorf("get group failed: %w", err)
	}
	return group, nil
}

func (s *GatewayService) routingAccountIDsForRequest(ctx context.Context, groupID *int64, requestedModel string, platform string) []int64 {
	if groupID == nil || requestedModel == "" || platform != PlatformAnthropic {
		return nil
	}
	group, err := s.resolveGroupByID(ctx, *groupID)
	if err != nil || group == nil {
		if s.debugModelRoutingEnabled() {
			log.Printf("[ModelRoutingDebug] resolve group failed: group_id=%v model=%s platform=%s err=%v", derefGroupID(groupID), requestedModel, platform, err)
		}
		return nil
	}
	// Preserve existing behavior: model routing only applies to anthropic groups.
	if group.Platform != PlatformAnthropic {
		if s.debugModelRoutingEnabled() {
			log.Printf("[ModelRoutingDebug] skip: non-anthropic group platform: group_id=%d group_platform=%s model=%s", group.ID, group.Platform, requestedModel)
		}
		return nil
	}
	ids := group.GetRoutingAccountIDs(requestedModel)
	if s.debugModelRoutingEnabled() {
		log.Printf("[ModelRoutingDebug] routing lookup: group_id=%d model=%s enabled=%v rules=%d matched_ids=%v",
			group.ID, requestedModel, group.ModelRoutingEnabled, len(group.ModelRouting), ids)
	}
	return ids
}

func (s *GatewayService) resolveGatewayGroup(ctx context.Context, groupID *int64) (*Group, *int64, error) {
	if groupID == nil {
		return nil, nil, nil
	}

	currentID := *groupID
	visited := map[int64]struct{}{}
	for {
		if _, seen := visited[currentID]; seen {
			return nil, nil, fmt.Errorf("fallback group cycle detected")
		}
		visited[currentID] = struct{}{}

		group, err := s.resolveGroupByID(ctx, currentID)
		if err != nil {
			return nil, nil, err
		}

		if !group.ClaudeCodeOnly || IsClaudeCodeClient(ctx) {
			return group, &currentID, nil
		}

		if group.FallbackGroupID == nil {
			return nil, nil, ErrClaudeCodeOnly
		}
		currentID = *group.FallbackGroupID
	}
}

// checkClaudeCodeRestriction 检查分组的 Claude Code 客户端限制
// 如果分组启用了 claude_code_only 且请求不是来自 Claude Code 客户端：
//   - 有降级分组：返回降级分组的 ID
//   - 无降级分组：返回 ErrClaudeCodeOnly 错误
func (s *GatewayService) checkClaudeCodeRestriction(ctx context.Context, groupID *int64) (*Group, *int64, error) {
	if groupID == nil {
		return nil, groupID, nil
	}

	// 强制平台模式不检查 Claude Code 限制
	if _, hasForcePlatform := ctx.Value(ctxkey.ForcePlatform).(string); hasForcePlatform {
		return nil, groupID, nil
	}

	group, resolvedID, err := s.resolveGatewayGroup(ctx, groupID)
	if err != nil {
		return nil, nil, err
	}

	return group, resolvedID, nil
}

func (s *GatewayService) resolvePlatform(ctx context.Context, groupID *int64, group *Group) (string, bool, error) {
	forcePlatform, hasForcePlatform := ctx.Value(ctxkey.ForcePlatform).(string)
	if hasForcePlatform && forcePlatform != "" {
		return forcePlatform, true, nil
	}
	if group != nil {
		return group.Platform, false, nil
	}
	if groupID != nil {
		group, err := s.resolveGroupByID(ctx, *groupID)
		if err != nil {
			return "", false, err
		}
		return group.Platform, false, nil
	}
	return PlatformAnthropic, false, nil
}

func (s *GatewayService) listSchedulableAccounts(ctx context.Context, groupID *int64, platform string, hasForcePlatform bool) ([]Account, bool, error) {
	if s.schedulerSnapshot != nil {
		return s.schedulerSnapshot.ListSchedulableAccounts(ctx, groupID, platform, hasForcePlatform)
	}
	useMixed := (platform == PlatformAnthropic || platform == PlatformGemini) && !hasForcePlatform
	if useMixed {
		platforms := []string{platform, PlatformAntigravity}
		if platform == PlatformAnthropic {
			platforms = append(platforms, PlatformOpenAI)
		}
		var accounts []Account
		var err error
		if groupID != nil {
			accounts, err = s.accountRepo.ListSchedulableByGroupIDAndPlatforms(ctx, *groupID, platforms)
		} else {
			accounts, err = s.accountRepo.ListSchedulableByPlatforms(ctx, platforms)
		}
		if err != nil {
			return nil, useMixed, err
		}
		filtered := make([]Account, 0, len(accounts))
		for _, acc := range accounts {
			if acc.Platform == PlatformAntigravity && !acc.IsMixedSchedulingEnabled() {
				continue
			}
			filtered = append(filtered, acc)
		}
		return filtered, useMixed, nil
	}

	var accounts []Account
	var err error
	if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		accounts, err = s.accountRepo.ListSchedulableByPlatform(ctx, platform)
	} else if groupID != nil {
		accounts, err = s.accountRepo.ListSchedulableByGroupIDAndPlatform(ctx, *groupID, platform)
		// 分组内无账号则返回空列表，由上层处理错误，不再回退到全平台查询
	} else {
		accounts, err = s.accountRepo.ListSchedulableByPlatform(ctx, platform)
	}
	if err != nil {
		return nil, useMixed, err
	}
	return accounts, useMixed, nil
}

func (s *GatewayService) isAccountAllowedForPlatform(account *Account, platform string, useMixed bool) bool {
	if account == nil {
		return false
	}
	if platform == PlatformGrok && account.Type != AccountTypeOAuth && account.Type != AccountTypeAPIKey {
		return false
	}
	if useMixed {
		if account.Platform == platform {
			return true
		}
		if account.Platform == PlatformOpenAI {
			return platform == PlatformAnthropic
		}
		return account.Platform == PlatformAntigravity && account.IsMixedSchedulingEnabled()
	}
	return account.Platform == platform
}

// isAccountInGroup checks if the account belongs to the specified group.
// Returns true if groupID is nil (no group restriction) or account belongs to the group.
func (s *GatewayService) isAccountInGroup(account *Account, groupID *int64) bool {
	if groupID == nil {
		return true // 无分组限制
	}
	if account == nil {
		return false
	}
	for _, ag := range account.AccountGroups {
		if ag.GroupID == *groupID {
			return true
		}
	}
	return false
}

func (s *GatewayService) tryAcquireAccountSlot(ctx context.Context, accountID int64, maxConcurrency int) (*AcquireResult, error) {
	if s.concurrencyService == nil {
		return &AcquireResult{Acquired: true, ReleaseFunc: func() {}}, nil
	}
	return s.concurrencyService.AcquireAccountSlot(ctx, accountID, maxConcurrency)
}

// isAccountSchedulableForWindowCost 检查账号是否可根据窗口费用进行调度
// 仅适用于 Anthropic OAuth/SetupToken 账号
// 返回 true 表示可调度，false 表示不可调度
func (s *GatewayService) isAccountSchedulableForWindowCost(ctx context.Context, account *Account, isSticky bool) bool {
	// 只检查 Anthropic OAuth/SetupToken 账号
	if !account.IsAnthropicOAuthOrSetupToken() {
		return true
	}

	limit := account.GetWindowCostLimit()
	if limit <= 0 {
		return true // 未启用窗口费用限制
	}

	// 尝试从缓存获取窗口费用
	var currentCost float64
	if s.sessionLimitCache != nil {
		if cost, hit, err := s.sessionLimitCache.GetWindowCost(ctx, account.ID); err == nil && hit {
			currentCost = cost
			goto checkSchedulability
		}
	}

	// 缓存未命中，从数据库查询
	{
		// 使用统一的窗口开始时间计算逻辑（考虑窗口过期情况）
		startTime := account.GetCurrentWindowStartTime()

		stats, err := s.usageLogRepo.GetAccountWindowStats(ctx, account.ID, startTime)
		if err != nil {
			// 失败开放：查询失败时允许调度
			return true
		}

		// 使用标准费用（不含账号倍率）
		currentCost = stats.StandardCost

		// 设置缓存（忽略错误）
		if s.sessionLimitCache != nil {
			_ = s.sessionLimitCache.SetWindowCost(ctx, account.ID, currentCost)
		}
	}

checkSchedulability:
	schedulability := account.CheckWindowCostSchedulability(currentCost)

	switch schedulability {
	case WindowCostSchedulable:
		return true
	case WindowCostStickyOnly:
		return isSticky
	case WindowCostNotSchedulable:
		return false
	}
	return true
}

// checkAndRegisterSession 检查并注册会话，用于会话数量限制
// 仅适用于 Anthropic OAuth/SetupToken 账号
// 返回 true 表示允许（在限制内或会话已存在），false 表示拒绝（超出限制且是新会话）
func (s *GatewayService) checkAndRegisterSession(ctx context.Context, account *Account, sessionUUID string) bool {
	// 只检查 Anthropic OAuth/SetupToken 账号
	if !account.IsAnthropicOAuthOrSetupToken() {
		return true
	}

	maxSessions := account.GetMaxSessions()
	if maxSessions <= 0 || sessionUUID == "" {
		return true // 未启用会话限制或无会话ID
	}

	if s.sessionLimitCache == nil {
		return true // 缓存不可用时允许通过
	}

	idleTimeout := time.Duration(account.GetSessionIdleTimeoutMinutes()) * time.Minute

	allowed, err := s.sessionLimitCache.RegisterSession(ctx, account.ID, sessionUUID, maxSessions, idleTimeout)
	if err != nil {
		// 失败开放：缓存错误时允许通过
		return true
	}
	return allowed
}

// extractSessionUUID 从 metadata.user_id 中提取会话 UUID
// 格式: user_{64位hex}_account__session_{uuid}
func extractSessionUUID(metadataUserID string) string {
	if metadataUserID == "" {
		return ""
	}
	if match := sessionIDRegex.FindStringSubmatch(metadataUserID); len(match) > 1 {
		return match[1]
	}
	return ""
}

func (s *GatewayService) getSchedulableAccount(ctx context.Context, accountID int64) (*Account, error) {
	if s.schedulerSnapshot != nil {
		return s.schedulerSnapshot.GetAccount(ctx, accountID)
	}
	return s.accountRepo.GetByID(ctx, accountID)
}

func (s *GatewayService) sortAccountsByPriorityAndLastUsed(accounts []*Account, preferOAuth bool) {
	sort.SliceStable(accounts, func(i, j int) bool {
		a, b := accounts[i], accounts[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		// 重置时间比较：临近重置的账号优先使用
		aReset := s.getAccountResetTime(a.ID)
		bReset := s.getAccountResetTime(b.ID)
		switch {
		case aReset != nil && bReset != nil:
			if !aReset.Equal(*bReset) {
				return aReset.Before(*bReset) // 更早重置的优先
			}
		case aReset != nil && bReset == nil:
			return true // 有重置时间的优先
		case aReset == nil && bReset != nil:
			return false
		}
		// 最后使用时间比较
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

func (s *GatewayService) sortAccountWithLoadCandidates(accounts []accountWithLoad, preferOAuth bool) {
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
		s.orderEqualPriorityLoadGroup(accounts[i:j], preferOAuth)
		i = j
	}
}

func samePriorityLoadGroup(a, b accountWithLoad) bool {
	if a.account.Priority != b.account.Priority {
		return false
	}
	return a.loadInfo.LoadRate == b.loadInfo.LoadRate
}

func (s *GatewayService) orderEqualPriorityLoadGroup(accounts []accountWithLoad, preferOAuth bool) {
	if len(accounts) <= 1 {
		return
	}
	if s.weightedShuffleKiroCreditGroup(accounts) {
		return
	}
	sort.SliceStable(accounts, func(i, j int) bool {
		return s.accountResetAndLastUsedLess(accounts[i].account, accounts[j].account, preferOAuth)
	})
	s.shuffleWithinResetAndLastUsedGroups(accounts)
}

func (s *GatewayService) weightedShuffleKiroCreditGroup(accounts []accountWithLoad) bool {
	if len(accounts) <= 1 {
		return false
	}
	credits := make([]float64, len(accounts))
	creditsDiffer := false
	for i, item := range accounts {
		if item.account == nil || !item.account.IsKiro() {
			return false
		}
		credit := s.getKiroAvailableCredits(item.account.ID)
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

func (s *GatewayService) accountResetAndLastUsedLess(a, b *Account, preferOAuth bool) bool {
	aReset := s.getAccountResetTime(a.ID)
	bReset := s.getAccountResetTime(b.ID)
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
		if preferOAuth && a.Type != b.Type {
			return a.Type == AccountTypeOAuth
		}
		return false
	default:
		return a.LastUsedAt.Before(*b.LastUsedAt)
	}
}

func (s *GatewayService) shuffleWithinResetAndLastUsedGroups(accounts []accountWithLoad) {
	if len(accounts) <= 1 {
		return
	}
	i := 0
	for i < len(accounts) {
		j := i + 1
		for j < len(accounts) && s.sameResetAndLastUsedGroup(accounts[i].account, accounts[j].account) {
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

func (s *GatewayService) sameResetAndLastUsedGroup(a, b *Account) bool {
	aReset := s.getAccountResetTime(a.ID)
	bReset := s.getAccountResetTime(b.ID)
	switch {
	case aReset == nil && bReset == nil:
	case aReset == nil || bReset == nil:
		return false
	case !aReset.Equal(*bReset):
		return false
	}
	return sameLastUsedAt(a.LastUsedAt, b.LastUsedAt)
}

// shuffleWithinSortGroups 对排序后的 accountWithLoad 切片，按 (Priority, LoadRate, LastUsedAt) 分组后组内随机打乱。
// 防止并发请求读取同一快照时，确定性排序导致所有请求命中相同账号（惊群效应）。
func shuffleWithinSortGroups(accounts []accountWithLoad) {
	if len(accounts) <= 1 {
		return
	}
	i := 0
	for i < len(accounts) {
		j := i + 1
		for j < len(accounts) && sameAccountWithLoadGroup(accounts[i], accounts[j]) {
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

// sameAccountWithLoadGroup 判断两个 accountWithLoad 是否属于同一排序组
func sameAccountWithLoadGroup(a, b accountWithLoad) bool {
	if a.account.Priority != b.account.Priority {
		return false
	}
	if a.loadInfo.LoadRate != b.loadInfo.LoadRate {
		return false
	}
	return sameLastUsedAt(a.account.LastUsedAt, b.account.LastUsedAt)
}

// shuffleWithinPriorityAndLastUsed 对排序后的 []*Account 切片，按 (Priority, LastUsedAt) 分组后组内随机打乱。
func shuffleWithinPriorityAndLastUsed(accounts []*Account) {
	if len(accounts) <= 1 {
		return
	}
	i := 0
	for i < len(accounts) {
		j := i + 1
		for j < len(accounts) && sameAccountGroup(accounts[i], accounts[j]) {
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

// sameAccountGroup 判断两个 Account 是否属于同一排序组（Priority + LastUsedAt）
func sameAccountGroup(a, b *Account) bool {
	if a.Priority != b.Priority {
		return false
	}
	return sameLastUsedAt(a.LastUsedAt, b.LastUsedAt)
}

// sameLastUsedAt 判断两个 LastUsedAt 是否相同（精度到秒）
func sameLastUsedAt(a, b *time.Time) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return a.Unix() == b.Unix()
	}
}

// selectAccountForModelWithPlatform 选择单平台账户（完全隔离）
func (s *GatewayService) selectAccountForModelWithPlatform(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}, platform string) (*Account, error) {
	// Anthropic 和 Gemini 平台都支持账号类型优先级选择
	preferAccountType := platform == PlatformGemini || platform == PlatformAnthropic
	routingAccountIDs := s.routingAccountIDsForRequest(ctx, groupID, requestedModel, platform)

	var accounts []Account
	accountsLoaded := false

	// ============ Model Routing (legacy path): apply before sticky session ============
	// When load-awareness is disabled (e.g. concurrency service not configured), we still honor model routing
	// so switching model can switch upstream account within the same sticky session.
	if len(routingAccountIDs) > 0 {
		if s.debugModelRoutingEnabled() {
			log.Printf("[ModelRoutingDebug] legacy routed begin: group_id=%v model=%s platform=%s session=%s routed_ids=%v",
				derefGroupID(groupID), requestedModel, platform, shortSessionHash(sessionHash), routingAccountIDs)
		}
		// 1) Sticky session only applies if the bound account is within the routing set.
		if sessionHash != "" && s.cache != nil {
			accountID, err := s.cache.GetSessionAccountID(ctx, derefGroupID(groupID), sessionHash)
			if err == nil && accountID > 0 && containsInt64(routingAccountIDs, accountID) {
				if _, excluded := excludedIDs[accountID]; !excluded {
					account, err := s.getSchedulableAccount(ctx, accountID)
					// 检查账号分组归属和平台匹配（确保粘性会话不会跨分组或跨平台）
					if err == nil && s.isAccountInGroup(account, groupID) && s.isAccountAllowedForPlatform(account, platform, false) && account.IsSchedulableForModel(requestedModel) && (requestedModel == "" || s.isModelSupportedByAccount(account, requestedModel)) && s.isAccountQuotaAvailable(account) {
						if err := s.cache.RefreshSessionTTL(ctx, derefGroupID(groupID), sessionHash, stickySessionTTL); err != nil {
							log.Printf("refresh session ttl failed: session=%s err=%v", sessionHash, err)
						}
						if s.debugModelRoutingEnabled() {
							log.Printf("[ModelRoutingDebug] legacy routed sticky hit: group_id=%v model=%s session=%s account=%d", derefGroupID(groupID), requestedModel, shortSessionHash(sessionHash), accountID)
						}
						return account, nil
					}
				}
			}
		}

		// 2) Select an account from the routed candidates.
		forcePlatform, hasForcePlatform := ctx.Value(ctxkey.ForcePlatform).(string)
		if hasForcePlatform && forcePlatform == "" {
			hasForcePlatform = false
		}
		var err error
		accounts, _, err = s.listSchedulableAccounts(ctx, groupID, platform, hasForcePlatform)
		if err != nil {
			return nil, fmt.Errorf("query accounts failed: %w", err)
		}
		accountsLoaded = true

		routingSet := make(map[int64]struct{}, len(routingAccountIDs))
		for _, id := range routingAccountIDs {
			if id > 0 {
				routingSet[id] = struct{}{}
			}
		}

		var selected *Account
		for i := range accounts {
			acc := &accounts[i]
			if _, ok := routingSet[acc.ID]; !ok {
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
			if !acc.IsSchedulableForModel(requestedModel) {
				continue
			}
			if requestedModel != "" && !s.isModelSupportedByAccount(acc, requestedModel) {
				continue
			}
			// 配额检查（Antigravity 账号）
			if !s.isAccountQuotaAvailable(acc) {
				continue
			}
			if selected == nil {
				selected = acc
				continue
			}
			if acc.Priority < selected.Priority {
				selected = acc
			} else if acc.Priority == selected.Priority {
				switch {
				case acc.LastUsedAt == nil && selected.LastUsedAt != nil:
					selected = acc
				case acc.LastUsedAt != nil && selected.LastUsedAt == nil:
					// keep selected (never used is preferred)
				case acc.LastUsedAt == nil && selected.LastUsedAt == nil:
					// 账号类型优先级：OAuth = SetupToken > API Key
					if preferAccountType && acc.Type != selected.Type {
						if accountTypePriority(acc.Type) < accountTypePriority(selected.Type) {
							selected = acc
						}
					}
				default:
					if acc.LastUsedAt.Before(*selected.LastUsedAt) {
						selected = acc
					}
				}
			}
		}

		if selected != nil {
			if sessionHash != "" && s.cache != nil {
				if err := s.cache.SetSessionAccountID(ctx, derefGroupID(groupID), sessionHash, selected.ID, stickySessionTTL); err != nil {
					log.Printf("set session account failed: session=%s account_id=%d err=%v", sessionHash, selected.ID, err)
				}
			}
			if s.debugModelRoutingEnabled() {
				log.Printf("[ModelRoutingDebug] legacy routed select: group_id=%v model=%s session=%s account=%d", derefGroupID(groupID), requestedModel, shortSessionHash(sessionHash), selected.ID)
			}
			return selected, nil
		}
		log.Printf("[ModelRouting] No routed accounts available for model=%s, falling back to normal selection", requestedModel)
	}

	// 1. 查询粘性会话
	if sessionHash != "" && s.cache != nil {
		accountID, err := s.cache.GetSessionAccountID(ctx, derefGroupID(groupID), sessionHash)
		if err == nil && accountID > 0 {
			if _, excluded := excludedIDs[accountID]; !excluded {
				account, err := s.getSchedulableAccount(ctx, accountID)
				// 检查账号分组归属和平台匹配（确保粘性会话不会跨分组或跨平台）
				if err == nil && s.isAccountInGroup(account, groupID) && s.isAccountAllowedForPlatform(account, platform, false) && account.IsSchedulableForModel(requestedModel) && (requestedModel == "" || s.isModelSupportedByAccount(account, requestedModel)) && s.isAccountQuotaAvailable(account) {
					if err := s.cache.RefreshSessionTTL(ctx, derefGroupID(groupID), sessionHash, stickySessionTTL); err != nil {
						log.Printf("refresh session ttl failed: session=%s err=%v", sessionHash, err)
					}
					return account, nil
				}
			}
		}
	}

	// 2. 获取可调度账号列表（单平台）
	if !accountsLoaded {
		forcePlatform, hasForcePlatform := ctx.Value(ctxkey.ForcePlatform).(string)
		if hasForcePlatform && forcePlatform == "" {
			hasForcePlatform = false
		}
		var err error
		accounts, _, err = s.listSchedulableAccounts(ctx, groupID, platform, hasForcePlatform)
		if err != nil {
			return nil, fmt.Errorf("query accounts failed: %w", err)
		}
	}

	// 3. 按优先级+最久未用选择（考虑模型支持）
	var selected *Account
	var singlePlatformQuotaUnhealthy *Account // 配额不健康但其他条件满足的账号（用于兜底）
	for i := range accounts {
		acc := &accounts[i]
		if _, excluded := excludedIDs[acc.ID]; excluded {
			continue
		}
		// Scheduler snapshots can be temporarily stale; re-check schedulability here to
		// avoid selecting accounts that were recently rate-limited/overloaded.
		if !acc.IsSchedulable() {
			continue
		}
		if !acc.IsSchedulableForModel(requestedModel) {
			continue
		}
		if requestedModel != "" && !s.isModelSupportedByAccount(acc, requestedModel) {
			continue
		}
		// 配额检查（Antigravity 账号）- 软过滤，不健康的账号保留用于兜底
		if !s.isAccountQuotaAvailable(acc) {
			// 仅保留一个最优的不健康账号用于兜底
			if singlePlatformQuotaUnhealthy == nil {
				singlePlatformQuotaUnhealthy = acc
			} else if acc.Priority < singlePlatformQuotaUnhealthy.Priority {
				singlePlatformQuotaUnhealthy = acc
			} else if acc.Priority == singlePlatformQuotaUnhealthy.Priority {
				if acc.LastUsedAt == nil && singlePlatformQuotaUnhealthy.LastUsedAt != nil {
					singlePlatformQuotaUnhealthy = acc
				} else if acc.LastUsedAt != nil && singlePlatformQuotaUnhealthy.LastUsedAt != nil && acc.LastUsedAt.Before(*singlePlatformQuotaUnhealthy.LastUsedAt) {
					singlePlatformQuotaUnhealthy = acc
				}
			}
			continue
		}
		if selected == nil {
			selected = acc
			continue
		}
		if acc.Priority < selected.Priority {
			selected = acc
		} else if acc.Priority == selected.Priority {
			switch {
			case acc.LastUsedAt == nil && selected.LastUsedAt != nil:
				selected = acc
			case acc.LastUsedAt != nil && selected.LastUsedAt == nil:
				// keep selected (never used is preferred)
			case acc.LastUsedAt == nil && selected.LastUsedAt == nil:
				// 账号类型优先级：OAuth = SetupToken > API Key
				if preferAccountType && acc.Type != selected.Type {
					if accountTypePriority(acc.Type) < accountTypePriority(selected.Type) {
						selected = acc
					}
				}
			default:
				if acc.LastUsedAt.Before(*selected.LastUsedAt) {
					selected = acc
				}
			}
		}
	}

	// 如果没有健康账号，使用不健康账号作为兜底
	if selected == nil && singlePlatformQuotaUnhealthy != nil {
		selected = singlePlatformQuotaUnhealthy
		log.Printf("[ModelRouting] Fallback to quota-unhealthy account: account=%d model=%s", selected.ID, requestedModel)
	}

	if selected == nil {
		if requestedModel != "" {
			// 区分「模型无人支持（用户用错模型）」与「账号都在限流/忙」：
			// 前者返回 ErrModelNotSupported，由 handler 转 400 且不计入错误监控。
			if notSupportedErr := s.errIfModelNotSupportedInGroup(ctx, groupID, platform, true, requestedModel); notSupportedErr != nil {
				return nil, notSupportedErr
			}
			return nil, fmt.Errorf("no available accounts supporting model: %s", requestedModel)
		}
		return nil, errors.New("no available accounts")
	}

	// 4. 建立粘性绑定
	if sessionHash != "" && s.cache != nil {
		if err := s.cache.SetSessionAccountID(ctx, derefGroupID(groupID), sessionHash, selected.ID, stickySessionTTL); err != nil {
			log.Printf("set session account failed: session=%s account_id=%d err=%v", sessionHash, selected.ID, err)
		}
	}

	return selected, nil
}

// selectAccountWithMixedScheduling 选择账户（支持混合调度）
// 查询原生平台账户 + 启用 mixed_scheduling 的 antigravity 账户
// anthropic 混合调度额外包含 openai 账户
func (s *GatewayService) selectAccountWithMixedScheduling(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}, nativePlatform string) (*Account, error) {
	// Anthropic 和 Gemini 平台都支持账号类型优先级选择
	preferAccountType := nativePlatform == PlatformGemini || nativePlatform == PlatformAnthropic
	routingAccountIDs := s.routingAccountIDsForRequest(ctx, groupID, requestedModel, nativePlatform)

	var accounts []Account
	accountsLoaded := false

	// ============ Model Routing (legacy path): apply before sticky session ============
	if len(routingAccountIDs) > 0 {
		if s.debugModelRoutingEnabled() {
			log.Printf("[ModelRoutingDebug] legacy mixed routed begin: group_id=%v model=%s platform=%s session=%s routed_ids=%v",
				derefGroupID(groupID), requestedModel, nativePlatform, shortSessionHash(sessionHash), routingAccountIDs)
		}
		// 1) Sticky session only applies if the bound account is within the routing set.
		if sessionHash != "" && s.cache != nil {
			accountID, err := s.cache.GetSessionAccountID(ctx, derefGroupID(groupID), sessionHash)
			if err == nil && accountID > 0 && containsInt64(routingAccountIDs, accountID) {
				if _, excluded := excludedIDs[accountID]; !excluded {
					account, err := s.getSchedulableAccount(ctx, accountID)
					// 检查账号分组归属和有效性：原生平台直接匹配，openai（仅 anthropic 混合）直接匹配，antigravity 需要启用混合调度
					if err == nil && s.isAccountInGroup(account, groupID) && account.IsSchedulableForModel(requestedModel) && (requestedModel == "" || s.isModelSupportedByAccount(account, requestedModel)) && s.isAccountQuotaAvailable(account) {
						if account.Platform == nativePlatform ||
							(nativePlatform == PlatformAnthropic && account.Platform == PlatformOpenAI) ||
							(account.Platform == PlatformAntigravity && account.IsMixedSchedulingEnabled()) {
							if err := s.cache.RefreshSessionTTL(ctx, derefGroupID(groupID), sessionHash, stickySessionTTL); err != nil {
								log.Printf("refresh session ttl failed: session=%s err=%v", sessionHash, err)
							}
							if s.debugModelRoutingEnabled() {
								log.Printf("[ModelRoutingDebug] legacy mixed routed sticky hit: group_id=%v model=%s session=%s account=%d", derefGroupID(groupID), requestedModel, shortSessionHash(sessionHash), accountID)
							}
							return account, nil
						}
					}
				}
			}
		}

		// 2) Select an account from the routed candidates.
		var err error
		accounts, _, err = s.listSchedulableAccounts(ctx, groupID, nativePlatform, false)
		if err != nil {
			return nil, fmt.Errorf("query accounts failed: %w", err)
		}
		accountsLoaded = true

		routingSet := make(map[int64]struct{}, len(routingAccountIDs))
		for _, id := range routingAccountIDs {
			if id > 0 {
				routingSet[id] = struct{}{}
			}
		}

		var selected *Account
		for i := range accounts {
			acc := &accounts[i]
			if _, ok := routingSet[acc.ID]; !ok {
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
			// 过滤：原生平台直接通过，antigravity 需要启用混合调度
			if acc.Platform == PlatformAntigravity && !acc.IsMixedSchedulingEnabled() {
				continue
			}
			if !acc.IsSchedulableForModel(requestedModel) {
				continue
			}
			if requestedModel != "" && !s.isModelSupportedByAccount(acc, requestedModel) {
				continue
			}
			// 配额检查（Antigravity 账号）
			if !s.isAccountQuotaAvailable(acc) {
				continue
			}
			if selected == nil {
				selected = acc
				continue
			}
			if acc.Priority < selected.Priority {
				selected = acc
			} else if acc.Priority == selected.Priority {
				switch {
				case acc.LastUsedAt == nil && selected.LastUsedAt != nil:
					selected = acc
				case acc.LastUsedAt != nil && selected.LastUsedAt == nil:
					// keep selected (never used is preferred)
				case acc.LastUsedAt == nil && selected.LastUsedAt == nil:
					// 账号类型优先级：OAuth = SetupToken > API Key
					// 仅对同平台账号进行类型优先级比较
					if preferAccountType && acc.Platform == nativePlatform && selected.Platform == nativePlatform && acc.Type != selected.Type {
						if accountTypePriority(acc.Type) < accountTypePriority(selected.Type) {
							selected = acc
						}
					}
				default:
					if acc.LastUsedAt.Before(*selected.LastUsedAt) {
						selected = acc
					}
				}
			}
		}

		if selected != nil {
			if sessionHash != "" && s.cache != nil {
				if err := s.cache.SetSessionAccountID(ctx, derefGroupID(groupID), sessionHash, selected.ID, stickySessionTTL); err != nil {
					log.Printf("set session account failed: session=%s account_id=%d err=%v", sessionHash, selected.ID, err)
				}
			}
			if s.debugModelRoutingEnabled() {
				log.Printf("[ModelRoutingDebug] legacy mixed routed select: group_id=%v model=%s session=%s account=%d", derefGroupID(groupID), requestedModel, shortSessionHash(sessionHash), selected.ID)
			}
			return selected, nil
		}
		log.Printf("[ModelRouting] No routed accounts available for model=%s, falling back to normal selection", requestedModel)
	}

	// 1. 查询粘性会话
	if sessionHash != "" && s.cache != nil {
		accountID, err := s.cache.GetSessionAccountID(ctx, derefGroupID(groupID), sessionHash)
		if err == nil && accountID > 0 {
			if _, excluded := excludedIDs[accountID]; !excluded {
				account, err := s.getSchedulableAccount(ctx, accountID)
				// 检查账号分组归属和有效性：原生平台直接匹配，openai（仅 anthropic 混合）直接匹配，antigravity 需要启用混合调度
				if err == nil && s.isAccountInGroup(account, groupID) && account.IsSchedulableForModel(requestedModel) && (requestedModel == "" || s.isModelSupportedByAccount(account, requestedModel)) && s.isAccountQuotaAvailable(account) {
					if account.Platform == nativePlatform ||
						(nativePlatform == PlatformAnthropic && account.Platform == PlatformOpenAI) ||
						(account.Platform == PlatformAntigravity && account.IsMixedSchedulingEnabled()) {
						if err := s.cache.RefreshSessionTTL(ctx, derefGroupID(groupID), sessionHash, stickySessionTTL); err != nil {
							log.Printf("refresh session ttl failed: session=%s err=%v", sessionHash, err)
						}
						return account, nil
					}
				}
			}
		}
	}

	// 2. 获取可调度账号列表
	if !accountsLoaded {
		var err error
		accounts, _, err = s.listSchedulableAccounts(ctx, groupID, nativePlatform, false)
		if err != nil {
			return nil, fmt.Errorf("query accounts failed: %w", err)
		}
	}

	// 3. 按优先级+最久未用选择（考虑模型支持和混合调度）
	var selected *Account
	var mixedQuotaUnhealthy *Account // 配额不健康但其他条件满足的账号（用于兜底）
	for i := range accounts {
		acc := &accounts[i]
		if _, excluded := excludedIDs[acc.ID]; excluded {
			continue
		}
		// Scheduler snapshots can be temporarily stale; re-check schedulability here to
		// avoid selecting accounts that were recently rate-limited/overloaded.
		if !acc.IsSchedulable() {
			continue
		}
		// 过滤：原生平台直接通过，antigravity 需要启用混合调度
		if acc.Platform == PlatformAntigravity && !acc.IsMixedSchedulingEnabled() {
			continue
		}
		if !acc.IsSchedulableForModel(requestedModel) {
			continue
		}
		if requestedModel != "" && !s.isModelSupportedByAccount(acc, requestedModel) {
			continue
		}
		// 配额检查（Antigravity 账号）- 软过滤，不健康的账号保留用于兜底
		if !s.isAccountQuotaAvailable(acc) {
			// 仅保留一个最优的不健康账号用于兜底
			if mixedQuotaUnhealthy == nil {
				mixedQuotaUnhealthy = acc
			} else if acc.Priority < mixedQuotaUnhealthy.Priority {
				mixedQuotaUnhealthy = acc
			} else if acc.Priority == mixedQuotaUnhealthy.Priority {
				if acc.LastUsedAt == nil && mixedQuotaUnhealthy.LastUsedAt != nil {
					mixedQuotaUnhealthy = acc
				} else if acc.LastUsedAt != nil && mixedQuotaUnhealthy.LastUsedAt != nil && acc.LastUsedAt.Before(*mixedQuotaUnhealthy.LastUsedAt) {
					mixedQuotaUnhealthy = acc
				}
			}
			continue
		}
		if selected == nil {
			selected = acc
			continue
		}
		if acc.Priority < selected.Priority {
			selected = acc
		} else if acc.Priority == selected.Priority {
			switch {
			case acc.LastUsedAt == nil && selected.LastUsedAt != nil:
				selected = acc
			case acc.LastUsedAt != nil && selected.LastUsedAt == nil:
				// keep selected (never used is preferred)
			case acc.LastUsedAt == nil && selected.LastUsedAt == nil:
				// 账号类型优先级：OAuth = SetupToken > API Key
				// 仅对同平台账号进行类型优先级比较
				if preferAccountType && acc.Platform == nativePlatform && selected.Platform == nativePlatform && acc.Type != selected.Type {
					if accountTypePriority(acc.Type) < accountTypePriority(selected.Type) {
						selected = acc
					}
				}
			default:
				if acc.LastUsedAt.Before(*selected.LastUsedAt) {
					selected = acc
				}
			}
		}
	}

	// 如果没有健康账号，使用不健康账号作为兜底
	if selected == nil && mixedQuotaUnhealthy != nil {
		selected = mixedQuotaUnhealthy
		log.Printf("[ModelRouting] Mixed fallback to quota-unhealthy account: account=%d model=%s", selected.ID, requestedModel)
	}

	if selected == nil {
		if requestedModel != "" {
			// 区分「模型无人支持（用户用错模型）」与「账号都在限流/忙」：
			// 前者返回 ErrModelNotSupported，由 handler 转 400 且不计入错误监控。
			if notSupportedErr := s.errIfModelNotSupportedInGroup(ctx, groupID, nativePlatform, false, requestedModel); notSupportedErr != nil {
				return nil, notSupportedErr
			}
			return nil, fmt.Errorf("no available accounts supporting model: %s", requestedModel)
		}
		return nil, errors.New("no available accounts")
	}

	// 4. 建立粘性绑定
	if sessionHash != "" && s.cache != nil {
		if err := s.cache.SetSessionAccountID(ctx, derefGroupID(groupID), sessionHash, selected.ID, stickySessionTTL); err != nil {
			log.Printf("set session account failed: session=%s account_id=%d err=%v", sessionHash, selected.ID, err)
		}
	}

	return selected, nil
}

// isModelSupportedByAccount 根据账户平台检查模型支持
func (s *GatewayService) isModelSupportedByAccount(account *Account, requestedModel string) bool {
	if account.Platform == PlatformAntigravity {
		// Antigravity 平台使用专门的模型支持检查
		return IsAntigravityModelSupported(requestedModel)
	}
	if account.Platform == PlatformKiro {
		return IsKiroModelSupportedByAccount(account, requestedModel)
	}
	if account.Platform == PlatformGrok {
		return isGrokModelSupportedByAccount(account, requestedModel)
	}
	if account.Platform == PlatformOpenAI {
		// OpenAI 平台在混合调度中接收 claude-* 请求，通过协议转换支持
		// 所有 claude- 前缀的模型都能通过 ConvertClaudeToOpenAI 映射
		return strings.HasPrefix(requestedModel, "claude-") || account.IsModelSupported(requestedModel)
	}
	// 其他平台使用账户的模型支持检查
	return account.IsModelSupported(requestedModel)
}

// groupHasAnyAccountSupportingModel 判断账号集合中是否存在任意一个账号在其平台规则下支持该模型。
// 仅看模型支持能力（平台映射/前缀），刻意忽略 schedulable/quota 等临时状态——
// 那些是瞬时不可用，不代表「用错模型」。集合应为该请求实际可路由到的账号全集
// （单平台或混合调度过滤后的列表），以避免把跨平台账号误判为不支持。
func (s *GatewayService) groupHasAnyAccountSupportingModel(accounts []Account, requestedModel string) bool {
	if requestedModel == "" {
		// 未指定模型时不做此判定，交由原有逻辑处理。
		return true
	}
	for i := range accounts {
		if s.isModelSupportedByAccount(&accounts[i], requestedModel) {
			return true
		}
	}
	return false
}

// errIfModelNotSupportedInGroup 在选号失败后做二次判定：扫描分组内「全部账号」
// （刻意不经 schedulable/限流过滤，避免把临时限流误判为模型不支持），
// 若无任何账号在其平台规则下支持该模型，则返回包装了 ErrModelNotSupported 的错误，
// 供 handler 识别后返回 400（而非误导性的 503 no available accounts）。
// 判定不成立（确有账号支持，只是当前都不可调度）或无法判定时返回 nil，由调用方维持原错误。
//
// 仅在 cold path（selected==nil）调用，多一次轻量分组账号查询，不影响正常选号热路径。
func (s *GatewayService) errIfModelNotSupportedInGroup(ctx context.Context, groupID *int64, platform string, hasForcePlatform bool, requestedModel string) error {
	if requestedModel == "" {
		return nil
	}
	// 无分组模式不做此判定（无法低成本枚举候选账号集合），维持原有 503 行为。
	if groupID == nil || *groupID <= 0 {
		return nil
	}
	if s.accountRepo == nil {
		return nil
	}

	// 拉取分组内全部活跃账号（不过滤调度态），与 listSchedulableAccounts 使用一致的平台展开规则后判定。
	all, err := s.accountRepo.ListByGroup(ctx, *groupID)
	if err != nil {
		// 查询失败时不臆断，维持原错误（fail-open）。记录日志以便排查。
		log.Printf("[ModelRouting] model-support check skipped: ListByGroup failed: group=%d model=%s err=%v", *groupID, requestedModel, err)
		return nil
	}

	useMixed := (platform == PlatformAnthropic || platform == PlatformGemini) && !hasForcePlatform
	candidates := make([]Account, 0, len(all))
	for i := range all {
		if platform != "" && !s.isAccountAllowedForPlatform(&all[i], platform, useMixed) {
			continue
		}
		candidates = append(candidates, all[i])
	}

	// 分组内压根没有该平台的账号，属于配置/路由问题而非模型用错，维持原 503。
	if len(candidates) == 0 {
		return nil
	}

	if s.groupHasAnyAccountSupportingModel(candidates, requestedModel) {
		return nil
	}
	return fmt.Errorf("%s: %w", requestedModel, ErrModelNotSupported)
}

// IsAntigravityModelSupported 检查 Antigravity 平台是否支持指定模型
// 所有 claude- 和 gemini- 前缀的模型都能通过映射或透传支持
func IsAntigravityModelSupported(requestedModel string) bool {
	return strings.HasPrefix(requestedModel, "claude-") ||
		strings.HasPrefix(requestedModel, "gemini-")
}

// IsKiroModelSupported 检查 Kiro OAuth runtime 是否支持指定模型。
func IsKiroModelSupported(requestedModel string) bool {
	return kiro.IsOAuthModelSupported(requestedModel)
}

// IsKiroModelSupportedByAccount checks Kiro model support with the account auth type.
func IsKiroModelSupportedByAccount(account *Account, requestedModel string) bool {
	if account == nil || !account.IsKiro() {
		return false
	}
	if !account.IsKiroApiKey() && isClaudeOpus55Model(requestedModel) {
		return false
	}
	mapping := account.GetModelMapping()
	if len(mapping) > 0 {
		_, ok := mapping[requestedModel]
		return ok
	}
	if account.IsKiroApiKey() {
		// API Key accounts are custom Anthropic Messages-compatible endpoints.
		// Without an explicit mapping, model capability is determined by the upstream.
		return true
	}
	return IsKiroModelSupported(requestedModel)
}

// GetAccessToken 获取账号凭证
func (s *GatewayService) GetAccessToken(ctx context.Context, account *Account) (string, string, error) {
	switch account.Type {
	case AccountTypeOAuth, AccountTypeSetupToken:
		// Both oauth and setup-token use OAuth token flow
		return s.getOAuthToken(ctx, account)
	case AccountTypeAPIKey:
		apiKey := account.GetCredential("api_key")
		if apiKey == "" {
			return "", "", errors.New("api_key not found in credentials")
		}
		return apiKey, "apikey", nil
	default:
		return "", "", fmt.Errorf("unsupported account type: %s", account.Type)
	}
}

func (s *GatewayService) getOAuthToken(ctx context.Context, account *Account) (string, string, error) {
	// 对于 Anthropic OAuth 账号，使用 ClaudeTokenProvider 获取缓存的 token
	if account.Platform == PlatformAnthropic && account.Type == AccountTypeOAuth && s.claudeTokenProvider != nil {
		accessToken, err := s.claudeTokenProvider.GetAccessToken(ctx, account)
		if err != nil {
			return "", "", err
		}
		return accessToken, "oauth", nil
	}

	// 其他情况（Gemini 有自己的 TokenProvider，setup-token 类型等）直接从账号读取
	accessToken := account.GetCredential("access_token")
	if accessToken == "" {
		return "", "", errors.New("access_token not found in credentials")
	}
	// Token刷新由后台 TokenRefreshService 处理，此处只返回当前token
	return accessToken, "oauth", nil
}

// 重试相关常量
const (
	// 最大尝试次数（包含首次请求）。过多重试会导致请求堆积与资源耗尽。
	maxRetryAttempts = 5

	// 指数退避：第 N 次失败后的等待 = retryBaseDelay * 2^(N-1)，并且上限为 retryMaxDelay。
	retryBaseDelay = 300 * time.Millisecond
	retryMaxDelay  = 3 * time.Second

	// 最大重试耗时（包含请求本身耗时 + 退避等待时间）。
	// 用于防止极端情况下 goroutine 长时间堆积导致资源耗尽。
	maxRetryElapsed = 10 * time.Second
)

func (s *GatewayService) shouldRetryUpstreamError(account *Account, statusCode int) bool {
	// 429/529 限流错误：不在同一账号上重试，直接 failover 到其他账号
	// 这类错误表示当前账号已被限流，继续重试没有意义
	if statusCode == 429 || statusCode == 529 {
		return false
	}

	// OAuth/Setup Token 账号：仅 403 重试
	if account.IsOAuth() {
		return statusCode == 403
	}

	// API Key 账号：未配置的错误码重试
	return !account.ShouldHandleErrorCode(statusCode)
}

// shouldFailoverUpstreamError determines whether an upstream error should trigger account failover.
func (s *GatewayService) shouldFailoverUpstreamError(statusCode int) bool {
	switch statusCode {
	case 401, 403, 429, 529:
		return true
	default:
		return statusCode >= 500
	}
}

func retryBackoffDelay(attempt int) time.Duration {
	// attempt 从 1 开始，表示第 attempt 次请求刚失败，需要等待后进行第 attempt+1 次请求。
	if attempt <= 0 {
		return retryBaseDelay
	}
	delay := retryBaseDelay * time.Duration(1<<(attempt-1))
	if delay > retryMaxDelay {
		return retryMaxDelay
	}
	return delay
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// isClaudeCodeClient 判断请求是否来自 Claude Code 客户端
// 简化判断：User-Agent 匹配 + metadata.user_id 存在
func isClaudeCodeClient(userAgent string, metadataUserID string) bool {
	if metadataUserID == "" {
		return false
	}
	return claudeCliUserAgentRe.MatchString(userAgent)
}

// systemIncludesClaudeCodePrompt 检查 system 中是否已包含 Claude Code 提示词
// 使用前缀匹配支持多种变体（标准版、Agent SDK 版等）
func systemIncludesClaudeCodePrompt(system any) bool {
	switch v := system.(type) {
	case string:
		return hasClaudeCodePrefix(v)
	case []any:
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if text, ok := m["text"].(string); ok && hasClaudeCodePrefix(text) {
					return true
				}
			}
		}
	}
	return false
}

// hasClaudeCodePrefix 检查文本是否以 Claude Code 提示词的特征前缀开头
func hasClaudeCodePrefix(text string) bool {
	for _, prefix := range claudeCodePromptPrefixes {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

// matchesFilterPrefix 检查文本是否匹配任一过滤前缀
func matchesFilterPrefix(text string) bool {
	for _, prefix := range systemBlockFilterPrefixes {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

// filterSystemBlocksByPrefix 从 body 的 system 中移除文本匹配 systemBlockFilterPrefixes 前缀的元素
func filterSystemBlocksByPrefix(body []byte) []byte {
	sys := gjson.GetBytes(body, "system")
	if !sys.Exists() {
		return body
	}

	switch {
	case sys.Type == gjson.String:
		if matchesFilterPrefix(sys.Str) {
			result, err := sjson.DeleteBytes(body, "system")
			if err != nil {
				return body
			}
			return result
		}
	case sys.IsArray():
		var parsed []any
		if err := json.Unmarshal([]byte(sys.Raw), &parsed); err != nil {
			return body
		}
		filtered := make([]any, 0, len(parsed))
		changed := false
		for _, item := range parsed {
			if m, ok := item.(map[string]any); ok {
				if text, ok := m["text"].(string); ok && matchesFilterPrefix(text) {
					changed = true
					continue
				}
			}
			filtered = append(filtered, item)
		}
		if changed {
			result, err := sjson.SetBytes(body, "system", filtered)
			if err != nil {
				return body
			}
			return result
		}
	}
	return body
}

// injectClaudeCodePrompt 在 system 开头注入 Claude Code 提示词
// 处理 null、字符串、数组三种格式
func injectClaudeCodePrompt(body []byte, system any) []byte {
	claudeCodeBlock := map[string]any{
		"type":          "text",
		"text":          claudeCodeSystemPrompt,
		"cache_control": map[string]string{"type": "ephemeral"},
	}

	var newSystem []any

	switch v := system.(type) {
	case nil:
		newSystem = []any{claudeCodeBlock}
	case string:
		if v == "" || v == claudeCodeSystemPrompt {
			newSystem = []any{claudeCodeBlock}
		} else {
			newSystem = []any{claudeCodeBlock, map[string]any{"type": "text", "text": v}}
		}
	case []any:
		newSystem = make([]any, 0, len(v)+1)
		newSystem = append(newSystem, claudeCodeBlock)
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if text, ok := m["text"].(string); ok && text == claudeCodeSystemPrompt {
					continue
				}
			}
			newSystem = append(newSystem, item)
		}
	default:
		newSystem = []any{claudeCodeBlock}
	}

	result, err := sjson.SetBytes(body, "system", newSystem)
	if err != nil {
		log.Printf("Warning: failed to inject Claude Code prompt: %v", err)
		return body
	}
	return result
}

var unsupportedClaudeFields = []string{
	"context_management",
}

func stripUnsupportedClaudeFields(body []byte) []byte {
	for _, field := range unsupportedClaudeFields {
		if gjson.GetBytes(body, field).Exists() {
			if stripped, err := sjson.DeleteBytes(body, field); err == nil {
				body = stripped
			}
		}
	}
	return body
}

// injectCacheControlBreakpoints 自动注入 cache_control 断点以利用 Anthropic prompt cache。
// 返回 (处理后的 body, 是否发生了注入)。第二个返回值为 true 时表示平台主动注入了
// cache_control，调用方应隐藏响应中的 cache tokens 并按 input 单价计费。
// 为 false 时表示用户已自带 cache_control 或无需注入，应透传 cache tokens。

// 策略（方案 A：用户有任何 cache_control 则完全不注入）：
//  0. 前置检查：若 tools/system/messages 任一区域已有 cache_control，直接返回 false
//  1. tools：在最后一个 tool 上添加 {type: "ephemeral"}
//  2. system：在最后一个非 thinking block 上添加
//  3. messages：若有 ≥2 个 user 消息，在倒数第二个 user 消息的最后一个非 thinking content block 上添加
func injectCacheControlBreakpoints(body []byte) ([]byte, bool) {
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return body, false
	}

	// --- 前置检查：用户在任一区域自带 cache_control 则完全不注入 ---
	if tools, ok := data["tools"].([]any); ok && regionHasCacheControl(tools) {
		return body, false
	}
	if system, ok := data["system"].([]any); ok && regionHasCacheControl(system) {
		return body, false
	}
	if messages, ok := data["messages"].([]any); ok && messagesHaveCacheControl(messages) {
		return body, false
	}

	modified := false

	// 每处注入使用独立的 map 实例，避免共享引用导致的潜在副作用
	newEphemeral := func() map[string]any { return map[string]any{"type": "ephemeral"} }

	// --- 1. tools ---
	if tools, ok := data["tools"].([]any); ok && len(tools) > 0 {
		if last, ok := tools[len(tools)-1].(map[string]any); ok {
			last["cache_control"] = newEphemeral()
			modified = true
		}
	}

	// --- 2. system ---
	if system, ok := data["system"].([]any); ok && len(system) > 0 {
		// 从尾部找第一个非 thinking block
		for i := len(system) - 1; i >= 0; i-- {
			if m, ok := system[i].(map[string]any); ok {
				if blockType, _ := m["type"].(string); blockType == "thinking" {
					continue
				}
				m["cache_control"] = newEphemeral()
				modified = true
				break
			}
		}
	}

	// --- 3. messages ---
	if messages, ok := data["messages"].([]any); ok {
		// 统计 user 消息
		var userMsgIndices []int
		for i, msg := range messages {
			if msgMap, ok := msg.(map[string]any); ok {
				if role, _ := msgMap["role"].(string); role == "user" {
					userMsgIndices = append(userMsgIndices, i)
				}
			}
		}

		// 3a. 长对话中间点 breakpoint：user 消息 >10 条时，在中间位置的 user 消息注入
		// 使中间点之前的内容可被缓存，后续请求只需传增量部分
		if len(userMsgIndices) > 10 {
			midIdx := userMsgIndices[len(userMsgIndices)/2]
			if midMsg, ok := messages[midIdx].(map[string]any); ok {
				if content, ok := midMsg["content"].([]any); ok && len(content) > 0 {
					for j := len(content) - 1; j >= 0; j-- {
						if block, ok := content[j].(map[string]any); ok {
							if blockType, _ := block["type"].(string); blockType == "thinking" {
								continue
							}
							block["cache_control"] = newEphemeral()
							modified = true
							break
						}
					}
				} else if content, ok := midMsg["content"].(string); ok && content != "" {
					// content 是纯字符串时，转换为数组形式以支持 cache_control
					midMsg["content"] = []any{
						map[string]any{
							"type":          "text",
							"text":          content,
							"cache_control": newEphemeral(),
						},
					}
					modified = true
				}
			}
		}

		// 3b. 倒数第二个 user 消息 breakpoint
		if len(userMsgIndices) >= 2 {
			targetIdx := userMsgIndices[len(userMsgIndices)-2]
			if targetMsg, ok := messages[targetIdx].(map[string]any); ok {
				if content, ok := targetMsg["content"].([]any); ok && len(content) > 0 {
					for j := len(content) - 1; j >= 0; j-- {
						if block, ok := content[j].(map[string]any); ok {
							if blockType, _ := block["type"].(string); blockType == "thinking" {
								continue
							}
							block["cache_control"] = newEphemeral()
							modified = true
							break
						}
					}
				} else if content, ok := targetMsg["content"].(string); ok && content != "" {
					// content 是纯字符串时，转换为数组形式以支持 cache_control
					targetMsg["content"] = []any{
						map[string]any{
							"type":          "text",
							"text":          content,
							"cache_control": newEphemeral(),
						},
					}
					modified = true
				}
			}
		}
	}

	if !modified {
		return body, false
	}

	result, err := json.Marshal(data)
	if err != nil {
		return body, false
	}
	return result, true
}

// regionHasCacheControl 检查 tools 或 system 数组中是否已有 cache_control
func regionHasCacheControl(items []any) bool {
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			if _, has := m["cache_control"]; has {
				return true
			}
		}
	}
	return false
}

// messagesHaveCacheControl 检查 messages 中是否有任何 content block 带 cache_control
func messagesHaveCacheControl(messages []any) bool {
	for _, msg := range messages {
		msgMap, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		if content, ok := msgMap["content"].([]any); ok {
			for _, item := range content {
				if m, ok := item.(map[string]any); ok {
					if _, has := m["cache_control"]; has {
						return true
					}
				}
			}
		}
	}
	return false
}

// enforceCacheControlLimit 强制执行 cache_control 块数量限制（最多 4 个）
// 超限时优先从 messages 中移除 cache_control，保护 system 中的缓存控制
func enforceCacheControlLimit(body []byte) []byte {
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return body
	}

	// 清理 thinking 块中的非法 cache_control（thinking 块不支持该字段）
	thinkingCleaned := removeCacheControlFromThinkingBlocks(data)

	// 计算当前 cache_control 块数量
	count := countCacheControlBlocks(data)
	if count <= maxCacheControlBlocks {
		// 即使数量未超限，thinking 块清理也需要序列化返回
		if thinkingCleaned {
			result, err := json.Marshal(data)
			if err != nil {
				return body
			}
			return result
		}
		return body
	}

	// 超限：优先从 messages 中移除，再从 system 中移除，最后从 tools 中移除
	for count > maxCacheControlBlocks {
		if removeCacheControlFromMessages(data) {
			count--
			continue
		}
		if removeCacheControlFromSystem(data) {
			count--
			continue
		}
		if removeCacheControlFromTools(data) {
			count--
			continue
		}
		break
	}

	result, err := json.Marshal(data)
	if err != nil {
		return body
	}
	return result
}

// countCacheControlBlocks 统计 tools、system 和 messages 中的 cache_control 块数量
// 注意：thinking 块不支持 cache_control，统计时跳过
func countCacheControlBlocks(data map[string]any) int {
	count := 0

	// 统计 tools 中的块
	if tools, ok := data["tools"].([]any); ok {
		for _, item := range tools {
			if m, ok := item.(map[string]any); ok {
				if _, has := m["cache_control"]; has {
					count++
				}
			}
		}
	}

	// 统计 system 中的块
	if system, ok := data["system"].([]any); ok {
		for _, item := range system {
			if m, ok := item.(map[string]any); ok {
				// thinking 块不支持 cache_control，跳过
				if blockType, _ := m["type"].(string); blockType == "thinking" {
					continue
				}
				if _, has := m["cache_control"]; has {
					count++
				}
			}
		}
	}

	// 统计 messages 中的块
	if messages, ok := data["messages"].([]any); ok {
		for _, msg := range messages {
			if msgMap, ok := msg.(map[string]any); ok {
				if content, ok := msgMap["content"].([]any); ok {
					for _, item := range content {
						if m, ok := item.(map[string]any); ok {
							// thinking 块不支持 cache_control，跳过
							if blockType, _ := m["type"].(string); blockType == "thinking" {
								continue
							}
							if _, has := m["cache_control"]; has {
								count++
							}
						}
					}
				}
			}
		}
	}

	return count
}

// removeCacheControlFromMessages 从 messages 中移除一个 cache_control（从头开始）
// 返回 true 表示成功移除，false 表示没有可移除的
// 注意：跳过 thinking 块（它不支持 cache_control）
func removeCacheControlFromMessages(data map[string]any) bool {
	messages, ok := data["messages"].([]any)
	if !ok {
		return false
	}

	for _, msg := range messages {
		msgMap, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		content, ok := msgMap["content"].([]any)
		if !ok {
			continue
		}
		for _, item := range content {
			if m, ok := item.(map[string]any); ok {
				// thinking 块不支持 cache_control，跳过
				if blockType, _ := m["type"].(string); blockType == "thinking" {
					continue
				}
				if _, has := m["cache_control"]; has {
					delete(m, "cache_control")
					return true
				}
			}
		}
	}
	return false
}

// removeCacheControlFromSystem 从 system 中移除一个 cache_control（从尾部开始，保护注入的 prompt）
// 返回 true 表示成功移除，false 表示没有可移除的
// 注意：跳过 thinking 块（它不支持 cache_control）
func removeCacheControlFromSystem(data map[string]any) bool {
	system, ok := data["system"].([]any)
	if !ok {
		return false
	}

	// 从尾部开始移除，保护开头注入的 Claude Code prompt
	for i := len(system) - 1; i >= 0; i-- {
		if m, ok := system[i].(map[string]any); ok {
			// thinking 块不支持 cache_control，跳过
			if blockType, _ := m["type"].(string); blockType == "thinking" {
				continue
			}
			if _, has := m["cache_control"]; has {
				delete(m, "cache_control")
				return true
			}
		}
	}
	return false
}

// removeCacheControlFromTools 从 tools 中移除一个 cache_control（从头开始）
// 作为 enforcement 的最后手段——优先从 messages/system 移除，tools 是最后防线
// 返回 true 表示成功移除，false 表示没有可移除的
func removeCacheControlFromTools(data map[string]any) bool {
	tools, ok := data["tools"].([]any)
	if !ok {
		return false
	}

	for _, item := range tools {
		if m, ok := item.(map[string]any); ok {
			if _, has := m["cache_control"]; has {
				delete(m, "cache_control")
				return true
			}
		}
	}
	return false
}

// removeCacheControlFromThinkingBlocks 强制清理所有 thinking 块中的非法 cache_control
// thinking 块不支持 cache_control 字段，这个函数确保所有 thinking 块都不含该字段
// 返回 true 表示有清理操作
func removeCacheControlFromThinkingBlocks(data map[string]any) bool {
	cleaned := false

	// 清理 system 中的 thinking 块
	if system, ok := data["system"].([]any); ok {
		for _, item := range system {
			if m, ok := item.(map[string]any); ok {
				if blockType, _ := m["type"].(string); blockType == "thinking" {
					if _, has := m["cache_control"]; has {
						delete(m, "cache_control")
						cleaned = true
						log.Printf("[Warning] Removed illegal cache_control from thinking block in system")
					}
				}
			}
		}
	}

	// 清理 messages 中的 thinking 块
	if messages, ok := data["messages"].([]any); ok {
		for msgIdx, msg := range messages {
			if msgMap, ok := msg.(map[string]any); ok {
				if content, ok := msgMap["content"].([]any); ok {
					for contentIdx, item := range content {
						if m, ok := item.(map[string]any); ok {
							if blockType, _ := m["type"].(string); blockType == "thinking" {
								if _, has := m["cache_control"]; has {
									delete(m, "cache_control")
									cleaned = true
									log.Printf("[Warning] Removed illegal cache_control from thinking block in messages[%d].content[%d]", msgIdx, contentIdx)
								}
							}
						}
					}
				}
			}
		}
	}

	return cleaned
}

// Forward 转发请求到Claude API
func (s *GatewayService) Forward(ctx context.Context, c *gin.Context, account *Account, parsed *ParsedRequest) (*ForwardResult, error) {
	startTime := time.Now()
	if parsed == nil {
		return nil, fmt.Errorf("parse request: empty request")
	}

	body := parsed.Body
	reqModel := parsed.Model
	reqStream := parsed.Stream

	// 智能注入 Claude Code 系统提示词（仅 OAuth/SetupToken 账号需要）
	// 条件：1) OAuth/SetupToken 账号  2) 不是 Claude Code 客户端  3) 不是 Haiku 模型  4) system 中还没有 Claude Code 提示词
	if account.IsOAuth() &&
		!isClaudeCodeClient(c.GetHeader("User-Agent"), parsed.MetadataUserID) &&
		!strings.Contains(strings.ToLower(reqModel), "haiku") &&
		!systemIncludesClaudeCodePrompt(parsed.System) {
		body = injectClaudeCodePrompt(body, parsed.System)
	}

	// OAuth/SetupToken 账号：移除黑名单前缀匹配的 system 元素（如客户端注入的计费元数据）
	if account.IsOAuth() {
		body = filterSystemBlocksByPrefix(body)
	}

	// 自动注入 cache_control 断点以利用 Anthropic prompt cache
	// cacheInjected=true 表示平台主动注入了，响应中需隐藏 cache tokens
	body, cacheInjected := injectCacheControlBreakpoints(body)
	// 强制执行 cache_control 块数量限制（最多 4 个）
	body = enforceCacheControlLimit(body)

	// 应用模型映射（仅对apikey类型账号）
	originalModel := reqModel
	if account.Type == AccountTypeAPIKey {
		mappedModel := account.GetMappedModel(reqModel)
		if mappedModel != reqModel {
			// 替换请求体中的模型名
			body = s.replaceModelInBody(body, mappedModel)
			reqModel = mappedModel
			log.Printf("Model mapping applied: %s -> %s (account: %s)", originalModel, mappedModel, account.Name)
		}
	}

	// 剥离上游 API 不支持的字段（如 Claude Code v2.1.22+ 引入的 context_management）
	if gjson.GetBytes(body, "context_management").Exists() {
		if stripped, err := sjson.DeleteBytes(body, "context_management"); err == nil {
			body = stripped
		}
	}

	// 获取凭证
	token, tokenType, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}

	// 获取代理URL
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	// 重试循环
	var resp *http.Response
	retryStart := time.Now()
	for attempt := 1; attempt <= maxRetryAttempts; attempt++ {
		// 构建上游请求（每次重试需要重新构建，因为请求体需要重新读取）
		upstreamReq, err := s.buildUpstreamRequest(ctx, c, account, body, token, tokenType, reqModel)
		// Capture upstream request body for ops retry of this attempt.
		c.Set(OpsUpstreamRequestBodyKey, string(body))

		if err != nil {
			return nil, err
		}

		// 发送请求
		resp, err = s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
		if err != nil {
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
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
				"type": "error",
				"error": gin.H{
					"type":    "upstream_error",
					"message": "Upstream request failed",
				},
			})
			return nil, fmt.Errorf("upstream request failed: %s", safeErr)
		}

		// 优先检测thinking block签名错误（400）并重试一次
		if resp.StatusCode == 400 {
			respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			if readErr == nil {
				_ = resp.Body.Close()

				if isThinkingBlockSignatureError(respBody) {
					appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
						Platform:           account.Platform,
						AccountID:          account.ID,
						AccountName:        account.Name,
						UpstreamStatusCode: resp.StatusCode,
						UpstreamRequestID:  resp.Header.Get("x-request-id"),
						Kind:               "signature_error",
						Message:            extractUpstreamErrorMessage(respBody),
						Detail: func() string {
							if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
								return truncateString(string(respBody), s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes)
							}
							return ""
						}(),
					})

					looksLikeToolSignatureError := func(msg string) bool {
						m := strings.ToLower(msg)
						return strings.Contains(m, "tool_use") ||
							strings.Contains(m, "tool_result") ||
							strings.Contains(m, "functioncall") ||
							strings.Contains(m, "function_call") ||
							strings.Contains(m, "functionresponse") ||
							strings.Contains(m, "function_response")
					}

					// 避免在重试预算已耗尽时再发起额外请求
					if time.Since(retryStart) >= maxRetryElapsed {
						resp.Body = io.NopCloser(bytes.NewReader(respBody))
						break
					}
					log.Printf("Account %d: detected thinking block signature error, retrying with filtered thinking blocks", account.ID)

					// 🔥 新增：重试前等待 500ms，避免触发 rate limit
					time.Sleep(500 * time.Millisecond)

					// Conservative two-stage fallback:
					// 1) Disable thinking + thinking->text (preserve content)
					// 2) Only if upstream still errors AND error message points to tool/function signature issues:
					//    also downgrade tool_use/tool_result blocks to text.

					filteredBody := FilterThinkingBlocksForRetry(body)
					retryReq, buildErr := s.buildUpstreamRequest(ctx, c, account, filteredBody, token, tokenType, reqModel)
					if buildErr == nil {
						retryResp, retryErr := s.httpUpstream.Do(retryReq, proxyURL, account.ID, account.Concurrency)
						if retryErr == nil {
							if retryResp.StatusCode < 400 {
								log.Printf("Account %d: signature error retry succeeded (thinking downgraded)", account.ID)
								resp = retryResp
								break
							}

							retryRespBody, retryReadErr := io.ReadAll(io.LimitReader(retryResp.Body, 2<<20))
							_ = retryResp.Body.Close()
							if retryReadErr == nil && retryResp.StatusCode == 400 && isThinkingBlockSignatureError(retryRespBody) {
								appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
									Platform:           account.Platform,
									AccountID:          account.ID,
									AccountName:        account.Name,
									UpstreamStatusCode: retryResp.StatusCode,
									UpstreamRequestID:  retryResp.Header.Get("x-request-id"),
									Kind:               "signature_retry_thinking",
									Message:            extractUpstreamErrorMessage(retryRespBody),
									Detail: func() string {
										if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
											return truncateString(string(retryRespBody), s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes)
										}
										return ""
									}(),
								})
								msg2 := extractUpstreamErrorMessage(retryRespBody)
								if looksLikeToolSignatureError(msg2) && time.Since(retryStart) < maxRetryElapsed {
									log.Printf("Account %d: signature retry still failing and looks tool-related, retrying with tool blocks downgraded", account.ID)

									// 🔥 新增：第二次重试前再等待 500ms
									time.Sleep(500 * time.Millisecond)

									filteredBody2 := FilterSignatureSensitiveBlocksForRetry(body)
									retryReq2, buildErr2 := s.buildUpstreamRequest(ctx, c, account, filteredBody2, token, tokenType, reqModel)
									if buildErr2 == nil {
										retryResp2, retryErr2 := s.httpUpstream.Do(retryReq2, proxyURL, account.ID, account.Concurrency)
										if retryErr2 == nil {
											resp = retryResp2
											break
										}
										if retryResp2 != nil && retryResp2.Body != nil {
											_ = retryResp2.Body.Close()
										}
										appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
											Platform:           account.Platform,
											AccountID:          account.ID,
											AccountName:        account.Name,
											UpstreamStatusCode: 0,
											Kind:               "signature_retry_tools_request_error",
											Message:            sanitizeUpstreamErrorMessage(retryErr2.Error()),
										})
										log.Printf("Account %d: tool-downgrade signature retry failed: %v", account.ID, retryErr2)
									} else {
										log.Printf("Account %d: tool-downgrade signature retry build failed: %v", account.ID, buildErr2)
									}
								}
							}

							// Fall back to the original retry response context.
							resp = &http.Response{
								StatusCode: retryResp.StatusCode,
								Header:     retryResp.Header.Clone(),
								Body:       io.NopCloser(bytes.NewReader(retryRespBody)),
							}
							break
						}
						if retryResp != nil && retryResp.Body != nil {
							_ = retryResp.Body.Close()
						}
						log.Printf("Account %d: signature error retry failed: %v", account.ID, retryErr)
					} else {
						log.Printf("Account %d: signature error retry build request failed: %v", account.ID, buildErr)
					}

					// Retry failed: restore original response body and continue handling.
					resp.Body = io.NopCloser(bytes.NewReader(respBody))
					break
				}
				// 不是thinking签名错误，恢复响应体
				resp.Body = io.NopCloser(bytes.NewReader(respBody))
			}
		}

		// 检查是否需要通用重试（排除400，因为400已经在上面特殊处理过了）
		if resp.StatusCode >= 400 && resp.StatusCode != 400 && s.shouldRetryUpstreamError(account, resp.StatusCode) {
			if attempt < maxRetryAttempts {
				elapsed := time.Since(retryStart)
				if elapsed >= maxRetryElapsed {
					break
				}

				delay := retryBackoffDelay(attempt)
				remaining := maxRetryElapsed - elapsed
				if delay > remaining {
					delay = remaining
				}
				if delay <= 0 {
					break
				}

				respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
				_ = resp.Body.Close()
				appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
					Platform:           account.Platform,
					AccountID:          account.ID,
					AccountName:        account.Name,
					UpstreamStatusCode: resp.StatusCode,
					UpstreamRequestID:  resp.Header.Get("x-request-id"),
					Kind:               "retry",
					Message:            extractUpstreamErrorMessage(respBody),
					Detail: func() string {
						if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
							return truncateString(string(respBody), s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes)
						}
						return ""
					}(),
				})
				log.Printf("Account %d: upstream error %d, retry %d/%d after %v (elapsed=%v/%v)",
					account.ID, resp.StatusCode, attempt, maxRetryAttempts, delay, elapsed, maxRetryElapsed)
				if err := sleepWithContext(ctx, delay); err != nil {
					return nil, err
				}
				continue
			}
			// 最后一次尝试也失败，跳出循环处理重试耗尽
			break
		}

		// 不需要重试（成功或不可重试的错误），跳出循环
		// DEBUG: 输出响应 headers（用于检测 rate limit 信息）
		if account.Platform == PlatformGemini && resp.StatusCode < 400 {
			log.Printf("[DEBUG] Gemini API Response Headers for account %d:", account.ID)
			for k, v := range resp.Header {
				log.Printf("[DEBUG]   %s: %v", k, v)
			}
		}
		break
	}
	if resp == nil || resp.Body == nil {
		return nil, errors.New("upstream request failed: empty response")
	}
	defer func() { _ = resp.Body.Close() }()

	// 处理重试耗尽的情况
	if resp.StatusCode >= 400 && s.shouldRetryUpstreamError(account, resp.StatusCode) {
		if s.shouldFailoverUpstreamError(resp.StatusCode) {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(respBody))

			s.handleRetryExhaustedSideEffects(ctx, resp, account)
			appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
				Platform:           account.Platform,
				AccountID:          account.ID,
				AccountName:        account.Name,
				UpstreamStatusCode: resp.StatusCode,
				UpstreamRequestID:  resp.Header.Get("x-request-id"),
				Kind:               "retry_exhausted_failover",
				Message:            extractUpstreamErrorMessage(respBody),
				Detail: func() string {
					if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
						return truncateString(string(respBody), s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes)
					}
					return ""
				}(),
			})
			return nil, &UpstreamFailoverError{StatusCode: resp.StatusCode}
		}
		return s.handleRetryExhaustedError(ctx, resp, c, account)
	}

	// 处理可切换账号的错误
	if resp.StatusCode >= 400 && s.shouldFailoverUpstreamError(resp.StatusCode) {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(respBody))

		s.handleFailoverSideEffects(ctx, resp, account)
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  resp.Header.Get("x-request-id"),
			Kind:               "failover",
			Message:            extractUpstreamErrorMessage(respBody),
			Detail: func() string {
				if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
					return truncateString(string(respBody), s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes)
				}
				return ""
			}(),
		})
		return nil, &UpstreamFailoverError{StatusCode: resp.StatusCode}
	}

	// 处理错误响应（不可重试的错误）
	if resp.StatusCode >= 400 {
		// 可选：对部分 400 触发 failover（默认关闭以保持语义）
		if resp.StatusCode == 400 && s.cfg != nil && s.cfg.Gateway.FailoverOn400 {
			respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			if readErr != nil {
				// ReadAll failed, fall back to normal error handling without consuming the stream
				return s.handleErrorResponse(ctx, resp, c, account)
			}
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(respBody))

			if s.shouldFailoverOn400(respBody) {
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
					Kind:               "failover_on_400",
					Message:            upstreamMsg,
					Detail:             upstreamDetail,
				})

				if s.cfg.Gateway.LogUpstreamErrorBody {
					log.Printf(
						"Account %d: 400 error, attempting failover: %s",
						account.ID,
						truncateForLog(respBody, s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes),
					)
				} else {
					log.Printf("Account %d: 400 error, attempting failover", account.ID)
				}
				s.handleFailoverSideEffects(ctx, resp, account)
				return nil, &UpstreamFailoverError{StatusCode: resp.StatusCode}
			}
		}
		return s.handleErrorResponse(ctx, resp, c, account)
	}

	// 处理正常响应
	var usage *ClaudeUsage
	var firstTokenMs *int
	var clientDisconnect bool
	if reqStream {
		streamResult, err := s.handleStreamingResponse(ctx, resp, c, account, startTime, originalModel, reqModel, cacheInjected)
		if err != nil {
			if err.Error() == "have error in stream" {
				return nil, &UpstreamFailoverError{
					StatusCode: 403,
				}
			}
			return nil, err
		}
		usage = streamResult.usage
		firstTokenMs = streamResult.firstTokenMs
		clientDisconnect = streamResult.clientDisconnect
	} else {
		usage, err = s.handleNonStreamingResponse(ctx, resp, c, account, originalModel, reqModel, cacheInjected)
		if err != nil {
			return nil, err
		}
	}

	return &ForwardResult{
		RequestID:        resp.Header.Get("x-request-id"),
		Usage:            *usage,
		Model:            originalModel, // 使用原始模型用于计费和日志
		Stream:           reqStream,
		Duration:         time.Since(startTime),
		FirstTokenMs:     firstTokenMs,
		ClientDisconnect: clientDisconnect,
	}, nil
}

// normalizeThinkingParam 将 thinking 参数中的 adaptive 类型转换为 Anthropic API 支持的 enabled 类型
// Claude Code V2.1.63 发送 thinking: {type: "adaptive"}
// Anthropic API 仅支持 thinking: {type: "enabled", budget_tokens: N}
func normalizeThinkingParam(body []byte) []byte {
	thinkingType := gjson.GetBytes(body, "thinking.type").String()
	if thinkingType != "adaptive" {
		return body
	}

	// adaptive → enabled + budget_tokens
	budgetTokens := gjson.GetBytes(body, "thinking.budget_tokens").Int()
	if budgetTokens <= 0 {
		budgetTokens = 10000
	}

	result, err := sjson.SetBytes(body, "thinking", map[string]any{
		"type":          "enabled",
		"budget_tokens": budgetTokens,
	})
	if err != nil {
		return body
	}

	return result
}

func (s *GatewayService) buildUpstreamRequest(ctx context.Context, c *gin.Context, account *Account, body []byte, token, tokenType, modelID string) (*http.Request, error) {
	// 确定目标URL
	targetURL := claudeAPIURL
	if account.Type == AccountTypeAPIKey {
		baseURL := account.GetBaseURL()
		if baseURL != "" {
			validatedURL, err := s.validateUpstreamBaseURL(baseURL)
			if err != nil {
				return nil, err
			}
			targetURL = validatedURL + "/v1/messages"
		}
	}

	// Anthropic 平台：将 thinking.type=adaptive 转换为 enabled + budget_tokens
	if account.Platform == PlatformAnthropic {
		body = normalizeThinkingParam(body)
	}

	// OAuth账号：应用统一指纹
	var fingerprint *Fingerprint
	if account.IsOAuth() && s.identityService != nil {
		// 1. 获取或创建指纹（包含随机生成的ClientID）
		fp, err := s.identityService.GetOrCreateFingerprint(ctx, account.ID, c.Request.Header)
		if err != nil {
			log.Printf("Warning: failed to get fingerprint for account %d: %v", account.ID, err)
			// 失败时降级为透传原始headers
		} else {
			fingerprint = fp

			// 2. 重写metadata.user_id（需要指纹中的ClientID和账号的account_uuid）
			accountUUID := account.GetExtraString("account_uuid")
			if accountUUID != "" && fp.ClientID != "" {
				// 缓存作用域 key：X-Conversation-ID > apiKeyID > sessionUUID（回退）
				scopeKey := c.GetHeader("X-Conversation-ID")
				if scopeKey == "" {
					if val, exists := c.Get("api_key"); exists {
						if apiKey, ok := val.(*APIKey); ok && apiKey.ID != 0 {
							scopeKey = fmt.Sprintf("apikey_%d", apiKey.ID)
						}
					}
				}
				if newBody, err := s.identityService.RewriteUserID(body, account.ID, accountUUID, fp.ClientID, scopeKey); err == nil && len(newBody) > 0 {
					body = newBody
				}
			}
		}
	}

	req, err := http.NewRequestWithContext(ctx, "POST", targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	// 设置认证头
	if tokenType == "oauth" {
		req.Header.Set("authorization", "Bearer "+token)
	} else {
		req.Header.Set("x-api-key", token)
		// Anthropic-compatible upstreams may require Authorization even when x-api-key is present.
		if account.Platform == PlatformAnthropic {
			req.Header.Set("authorization", "Bearer "+token)
		}
	}

	// 白名单透传headers
	for key, values := range c.Request.Header {
		lowerKey := strings.ToLower(key)
		if allowedHeaders[lowerKey] {
			for _, v := range values {
				req.Header.Add(key, v)
			}
		}
	}

	// OAuth账号：应用缓存的指纹到请求头（覆盖白名单透传的头）
	if fingerprint != nil {
		s.identityService.ApplyFingerprint(req, fingerprint)
	}

	// 确保必要的headers存在
	if req.Header.Get("content-type") == "" {
		req.Header.Set("content-type", "application/json")
	}
	if req.Header.Get("anthropic-version") == "" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}

	// 处理anthropic-beta header（OAuth账号需要特殊处理）
	if tokenType == "oauth" {
		req.Header.Set("anthropic-beta", s.getBetaHeader(modelID, c.GetHeader("anthropic-beta")))
	} else if s.cfg != nil && s.cfg.Gateway.InjectBetaForAPIKey && req.Header.Get("anthropic-beta") == "" {
		// API-key：仅在请求显式使用 beta 特性且客户端未提供时，按需补齐（默认关闭）
		if requestNeedsBetaFeatures(body) {
			if beta := defaultAPIKeyBetaHeader(body); beta != "" {
				req.Header.Set("anthropic-beta", beta)
			}
		}
	}

	return req, nil
}

// getBetaHeader 处理anthropic-beta header
// 对于OAuth账号，需要确保包含 oauth-2025-04-20 和 prompt-caching-scope-2026-01-05
func (s *GatewayService) getBetaHeader(modelID string, clientBetaHeader string) string {
	// 如果客户端传了anthropic-beta，确保必须包含的 beta flags 都在
	if clientBetaHeader != "" {
		result := clientBetaHeader

		// 确保包含 oauth beta
		if !strings.Contains(result, claude.BetaOAuth) {
			parts := strings.Split(result, ",")
			for i, p := range parts {
				parts[i] = strings.TrimSpace(p)
			}

			// 在claude-code-20250219后面插入oauth beta
			claudeCodeIdx := -1
			for i, p := range parts {
				if p == claude.BetaClaudeCode {
					claudeCodeIdx = i
					break
				}
			}

			if claudeCodeIdx >= 0 {
				newParts := make([]string, 0, len(parts)+1)
				newParts = append(newParts, parts[:claudeCodeIdx+1]...)
				newParts = append(newParts, claude.BetaOAuth)
				newParts = append(newParts, parts[claudeCodeIdx+1:]...)
				result = strings.Join(newParts, ",")
			} else {
				result = claude.BetaOAuth + "," + result
			}
		}

		// 确保包含 prompt-caching-scope beta（缓存作用域隔离）
		if !strings.Contains(result, claude.BetaPromptCachingScope) {
			result = result + "," + claude.BetaPromptCachingScope
		}

		return result
	}

	// 客户端没传，根据模型生成
	// haiku 模型不需要 claude-code beta
	if strings.Contains(strings.ToLower(modelID), "haiku") {
		return claude.HaikuBetaHeader
	}

	return claude.DefaultBetaHeader
}

func requestNeedsBetaFeatures(body []byte) bool {
	tools := gjson.GetBytes(body, "tools")
	if tools.Exists() && tools.IsArray() && len(tools.Array()) > 0 {
		return true
	}
	thinkingType := gjson.GetBytes(body, "thinking.type").String()
	if strings.EqualFold(thinkingType, "enabled") || strings.EqualFold(thinkingType, "adaptive") {
		return true
	}
	return false
}

func defaultAPIKeyBetaHeader(body []byte) string {
	modelID := gjson.GetBytes(body, "model").String()
	if strings.Contains(strings.ToLower(modelID), "haiku") {
		return claude.APIKeyHaikuBetaHeader
	}
	return claude.APIKeyBetaHeader
}

func truncateForLog(b []byte, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = 2048
	}
	if len(b) > maxBytes {
		b = b[:maxBytes]
	}
	s := string(b)
	// 保持一行，避免污染日志格式
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	return s
}

// isThinkingBlockSignatureError 检测是否是thinking block相关错误
// 这类错误可以通过过滤thinking blocks并重试来解决
func isThinkingBlockSignatureError(respBody []byte) bool {
	msg := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(respBody)))
	if msg == "" {
		return false
	}

	// Log for debugging
	log.Printf("[SignatureCheck] Checking error message: %s", msg)

	// 检测signature相关的错误（更宽松的匹配）
	// 例如: "Invalid `signature` in `thinking` block", "***.signature" 等
	if strings.Contains(msg, "signature") {
		log.Printf("[SignatureCheck] Detected signature error")
		return true
	}

	// 检测 thinking block 顺序/类型错误
	// 例如: "Expected `thinking` or `redacted_thinking`, but found `text`"
	if strings.Contains(msg, "expected") && (strings.Contains(msg, "thinking") || strings.Contains(msg, "redacted_thinking")) {
		log.Printf("[SignatureCheck] Detected thinking block type error")
		return true
	}

	// 检测 thinking block 被修改的错误
	// 例如: "thinking or redacted_thinking blocks in the latest assistant message cannot be modified"
	if strings.Contains(msg, "cannot be modified") && (strings.Contains(msg, "thinking") || strings.Contains(msg, "redacted_thinking")) {
		log.Printf("[SignatureCheck] Detected thinking block modification error")
		return true
	}

	// 检测空消息内容错误（可能是过滤 thinking blocks 后导致的）
	// 例如: "all messages must have non-empty content"
	if strings.Contains(msg, "non-empty content") || strings.Contains(msg, "empty content") {
		log.Printf("[SignatureCheck] Detected empty content error")
		return true
	}

	return false
}

func (s *GatewayService) shouldFailoverOn400(respBody []byte) bool {
	// 只对“可能是兼容性差异导致”的 400 允许切换，避免无意义重试。
	// 默认保守：无法识别则不切换。
	msg := strings.ToLower(strings.TrimSpace(extractUpstreamErrorMessage(respBody)))
	if msg == "" {
		return false
	}

	// 缺少/错误的 beta header：换账号/链路可能成功（尤其是混合调度时）。
	// 更精确匹配 beta 相关的兼容性问题，避免误触发切换。
	if strings.Contains(msg, "anthropic-beta") ||
		strings.Contains(msg, "beta feature") ||
		strings.Contains(msg, "requires beta") {
		return true
	}

	// thinking/tool streaming 等兼容性约束（常见于中间转换链路）
	if strings.Contains(msg, "thinking") || strings.Contains(msg, "thought_signature") || strings.Contains(msg, "signature") {
		return true
	}
	if strings.Contains(msg, "tool_use") || strings.Contains(msg, "tool_result") || strings.Contains(msg, "tools") {
		return true
	}

	return false
}

func extractUpstreamErrorMessage(body []byte) string {
	// Claude 风格：{"type":"error","error":{"type":"...","message":"..."}}
	if m := gjson.GetBytes(body, "error.message").String(); strings.TrimSpace(m) != "" {
		inner := strings.TrimSpace(m)
		// 有些上游会把完整 JSON 作为字符串塞进 message
		if strings.HasPrefix(inner, "{") {
			if innerMsg := gjson.Get(inner, "error.message").String(); strings.TrimSpace(innerMsg) != "" {
				return innerMsg
			}
		}
		return m
	}

	// 兜底：尝试顶层 message
	return gjson.GetBytes(body, "message").String()
}

func (s *GatewayService) handleErrorResponse(ctx context.Context, resp *http.Response, c *gin.Context, account *Account) (*ForwardResult, error) {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))

	upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(body))
	upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)

	// Enrich Ops error logs with upstream status + message, and optionally a truncated body snippet.
	upstreamDetail := ""
	if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		upstreamDetail = truncateString(string(body), maxBytes)
	}
	setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		UpstreamStatusCode: resp.StatusCode,
		UpstreamRequestID:  resp.Header.Get("x-request-id"),
		Kind:               "http_error",
		Message:            upstreamMsg,
		Detail:             upstreamDetail,
	})

	// 处理上游错误，标记账号状态
	shouldDisable := false
	if s.rateLimitService != nil {
		shouldDisable = s.rateLimitService.HandleUpstreamError(ctx, account, resp.StatusCode, resp.Header, body)
	}
	if shouldDisable {
		return nil, &UpstreamFailoverError{StatusCode: resp.StatusCode}
	}

	// 记录上游错误响应体摘要便于排障（可选：由配置控制；不回显到客户端）
	if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		log.Printf(
			"Upstream error %d (account=%d platform=%s type=%s): %s",
			resp.StatusCode,
			account.ID,
			account.Platform,
			account.Type,
			truncateForLog(body, s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes),
		)
	}

	// 根据状态码返回适当的自定义错误响应（不透传上游详细信息）
	var errType, errMsg string
	var statusCode int

	switch resp.StatusCode {
	case 400:
		c.Data(http.StatusBadRequest, "application/json", body)
		summary := upstreamMsg
		if summary == "" {
			summary = truncateForLog(body, 512)
		}
		if summary == "" {
			return nil, fmt.Errorf("upstream error: %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("upstream error: %d message=%s", resp.StatusCode, summary)
	case 401:
		statusCode = http.StatusBadGateway
		errType = "upstream_error"
		errMsg = "Upstream authentication failed, please contact administrator"
	case 403:
		statusCode = http.StatusBadGateway
		errType = "upstream_error"
		errMsg = "Upstream access forbidden, please contact administrator"
	case 429:
		statusCode = http.StatusTooManyRequests
		errType = "rate_limit_error"
		errMsg = "Upstream rate limit exceeded, please retry later"
	case 529:
		statusCode = http.StatusServiceUnavailable
		errType = "overloaded_error"
		errMsg = "Upstream service overloaded, please retry later"
	case 500, 502, 503, 504:
		statusCode = http.StatusBadGateway
		errType = "upstream_error"
		errMsg = "Upstream service temporarily unavailable"
	default:
		statusCode = http.StatusBadGateway
		errType = "upstream_error"
		errMsg = "Upstream request failed"
	}

	// 返回自定义错误响应
	c.JSON(statusCode, gin.H{
		"type": "error",
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

func (s *GatewayService) handleRetryExhaustedSideEffects(ctx context.Context, resp *http.Response, account *Account) {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	statusCode := resp.StatusCode

	// OAuth/Setup Token 账号的 403：标记账号异常
	if account.IsOAuth() && statusCode == 403 {
		s.rateLimitService.HandleUpstreamError(ctx, account, statusCode, resp.Header, body)
		log.Printf("Account %d: marked as error after %d retries for status %d", account.ID, maxRetryAttempts, statusCode)
	} else {
		// API Key 未配置错误码：不标记账号状态
		log.Printf("Account %d: upstream error %d after %d retries (not marking account)", account.ID, statusCode, maxRetryAttempts)
	}
}

func (s *GatewayService) handleFailoverSideEffects(ctx context.Context, resp *http.Response, account *Account) {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	s.rateLimitService.HandleUpstreamError(ctx, account, resp.StatusCode, resp.Header, body)
}

// handleRetryExhaustedError 处理重试耗尽后的错误
// OAuth 403：标记账号异常
// API Key 未配置错误码：仅返回错误，不标记账号
func (s *GatewayService) handleRetryExhaustedError(ctx context.Context, resp *http.Response, c *gin.Context, account *Account) (*ForwardResult, error) {
	// Capture upstream error body before side-effects consume the stream.
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(respBody))

	s.handleRetryExhaustedSideEffects(ctx, resp, account)

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
	setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		UpstreamStatusCode: resp.StatusCode,
		UpstreamRequestID:  resp.Header.Get("x-request-id"),
		Kind:               "retry_exhausted",
		Message:            upstreamMsg,
		Detail:             upstreamDetail,
	})

	if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		log.Printf(
			"Upstream error %d retries_exhausted (account=%d platform=%s type=%s): %s",
			resp.StatusCode,
			account.ID,
			account.Platform,
			account.Type,
			truncateForLog(respBody, s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes),
		)
	}

	// 返回统一的重试耗尽错误响应
	c.JSON(http.StatusBadGateway, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    "upstream_error",
			"message": "Upstream request failed after retries",
		},
	})

	if upstreamMsg == "" {
		return nil, fmt.Errorf("upstream error: %d (retries exhausted)", resp.StatusCode)
	}
	return nil, fmt.Errorf("upstream error: %d (retries exhausted) message=%s", resp.StatusCode, upstreamMsg)
}

// streamingResult 流式响应结果
type streamingResult struct {
	usage            *ClaudeUsage
	firstTokenMs     *int
	clientDisconnect bool // 客户端是否在流式传输过程中断开
}

func (s *GatewayService) handleStreamingResponse(ctx context.Context, resp *http.Response, c *gin.Context, account *Account, startTime time.Time, originalModel, mappedModel string, hideCacheUsage bool) (*streamingResult, error) {
	// 更新5h窗口状态
	s.rateLimitService.UpdateSessionWindow(ctx, account, resp.Header)

	// 成功响应：异步清除限流状态（如果之前被限流）
	if account.IsRateLimited() {
		go func(accountID int64) {
			clearCtx := context.Background()
			if err := s.rateLimitService.ClearRateLimit(clearCtx, accountID); err != nil {
				log.Printf("[RateLimit] Failed to clear rate limit for account %d: %v", accountID, err)
			}
		}(account.ID)
	}

	if s.cfg != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.cfg.Security.ResponseHeaders)
	}

	// 设置SSE响应头
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	// 透传其他响应头
	if v := resp.Header.Get("x-request-id"); v != "" {
		c.Header("x-request-id", v)
	}

	w := c.Writer
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}

	usage := &ClaudeUsage{}
	var firstTokenMs *int
	scanner := bufio.NewScanner(resp.Body)
	// 设置更大的buffer以处理长行
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	scanner.Buffer(make([]byte, 64*1024), maxLineSize)

	type scanEvent struct {
		line string
		err  error
	}
	// 独立 goroutine 读取上游，避免读取阻塞导致超时/keepalive无法处理
	events := make(chan scanEvent, 16)
	done := make(chan struct{})
	sendEvent := func(ev scanEvent) bool {
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
			if !sendEvent(scanEvent{line: scanner.Text()}) {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			_ = sendEvent(scanEvent{err: err})
		}
	}()
	defer close(done)

	streamInterval := time.Duration(0)
	if s.cfg != nil && s.cfg.Gateway.StreamDataIntervalTimeout > 0 {
		streamInterval = time.Duration(s.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}
	// 仅监控上游数据间隔超时，避免下游写入阻塞导致误判
	var intervalTicker *time.Ticker
	if streamInterval > 0 {
		intervalTicker = time.NewTicker(streamInterval)
		defer intervalTicker.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTicker != nil {
		intervalCh = intervalTicker.C
	}

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

	needModelReplace := originalModel != mappedModel
	clientDisconnected := false // 客户端断开标志，断开后继续读取上游以获取完整usage

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				// 上游完成，返回结果
				return &streamingResult{usage: usage, firstTokenMs: firstTokenMs, clientDisconnect: clientDisconnected}, nil
			}
			if ev.err != nil {
				// 检测 context 取消（客户端断开会导致 context 取消，进而影响上游读取）
				if errors.Is(ev.err, context.Canceled) || errors.Is(ev.err, context.DeadlineExceeded) {
					log.Printf("Context canceled during streaming, returning collected usage")
					return &streamingResult{usage: usage, firstTokenMs: firstTokenMs, clientDisconnect: true}, nil
				}
				// 客户端已通过写入失败检测到断开，上游也出错了，返回已收集的 usage
				if clientDisconnected {
					log.Printf("Upstream read error after client disconnect: %v, returning collected usage", ev.err)
					return &streamingResult{usage: usage, firstTokenMs: firstTokenMs, clientDisconnect: true}, nil
				}
				// 客户端未断开，正常的错误处理
				if errors.Is(ev.err, bufio.ErrTooLong) {
					log.Printf("SSE line too long: account=%d max_size=%d error=%v", account.ID, maxLineSize, ev.err)
					sendErrorEvent("response_too_large")
					return &streamingResult{usage: usage, firstTokenMs: firstTokenMs}, ev.err
				}
				sendErrorEvent("stream_read_error")
				return &streamingResult{usage: usage, firstTokenMs: firstTokenMs}, fmt.Errorf("stream read error: %w", ev.err)
			}
			line := ev.line
			if line == "event: error" {
				// 上游返回错误事件，如果客户端已断开仍返回已收集的 usage
				if clientDisconnected {
					return &streamingResult{usage: usage, firstTokenMs: firstTokenMs, clientDisconnect: true}, nil
				}
				return nil, errors.New("have error in stream")
			}

			// Extract data from SSE line (supports both "data: " and "data:" formats)
			var data string
			if sseDataRe.MatchString(line) {
				data = sseDataRe.ReplaceAllString(line, "")
				// 如果有模型映射，替换响应中的model字段
				if needModelReplace {
					line = s.replaceModelInSSELine(line, mappedModel, originalModel)
				}
			}

			// 无论客户端是否断开，都先解析真实 usage（仅对 data 行）
			if data != "" {
				if firstTokenMs == nil && data != "[DONE]" {
					ms := int(time.Since(startTime).Milliseconds())
					firstTokenMs = &ms
				}
				s.parseSSEUsage(data, usage)
			}

			// 隐藏 cache tokens：仅平台注入时合并入 input_tokens 后再发给客户端
			clientLine := line
			if hideCacheUsage {
				clientLine = rewriteCacheUsageInSSELine(line)
			}

			// 写入客户端（统一处理 data 行和非 data 行）
			if !clientDisconnected {
				if _, err := fmt.Fprintf(w, "%s\n", clientLine); err != nil {
					clientDisconnected = true
					log.Printf("Client disconnected during streaming, continuing to drain upstream for billing")
				} else {
					flusher.Flush()
				}
			}

		case <-intervalCh:
			lastRead := time.Unix(0, atomic.LoadInt64(&lastReadAt))
			if time.Since(lastRead) < streamInterval {
				continue
			}
			if clientDisconnected {
				// 客户端已断开，上游也超时了，返回已收集的 usage
				log.Printf("Upstream timeout after client disconnect, returning collected usage")
				return &streamingResult{usage: usage, firstTokenMs: firstTokenMs, clientDisconnect: true}, nil
			}
			log.Printf("Stream data interval timeout: account=%d model=%s interval=%s", account.ID, originalModel, streamInterval)
			// 处理流超时，可能标记账户为临时不可调度或错误状态
			if s.rateLimitService != nil {
				s.rateLimitService.HandleStreamTimeout(ctx, account, originalModel)
			}
			sendErrorEvent("stream_timeout")
			return &streamingResult{usage: usage, firstTokenMs: firstTokenMs}, fmt.Errorf("stream data interval timeout")
		}
	}

}

// replaceModelInSSELine 替换SSE数据行中的model字段
func (s *GatewayService) replaceModelInSSELine(line, fromModel, toModel string) string {
	if !sseDataRe.MatchString(line) {
		return line
	}
	data := sseDataRe.ReplaceAllString(line, "")
	if data == "" || data == "[DONE]" {
		return line
	}

	var event map[string]any
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return line
	}

	// 只替换 message_start 事件中的 message.model
	if event["type"] != "message_start" {
		return line
	}

	msg, ok := event["message"].(map[string]any)
	if !ok {
		return line
	}

	model, ok := msg["model"].(string)
	if !ok || model != fromModel {
		return line
	}

	msg["model"] = toModel
	newData, err := json.Marshal(event)
	if err != nil {
		return line
	}

	return "data: " + string(newData)
}

func (s *GatewayService) parseSSEUsage(data string, usage *ClaudeUsage) {
	// 解析message_start获取input tokens（标准Claude API格式）
	var msgStart struct {
		Type    string `json:"type"`
		Message struct {
			Usage ClaudeUsage `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal([]byte(data), &msgStart) == nil && msgStart.Type == "message_start" {
		usage.InputTokens = msgStart.Message.Usage.InputTokens
		usage.CacheCreationInputTokens = msgStart.Message.Usage.CacheCreationInputTokens
		usage.CacheReadInputTokens = msgStart.Message.Usage.CacheReadInputTokens
	}

	// 解析message_delta获取tokens（兼容GLM等把所有usage放在delta中的API）
	var msgDelta struct {
		Type  string `json:"type"`
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal([]byte(data), &msgDelta) == nil && msgDelta.Type == "message_delta" {
		// output_tokens 总是从 message_delta 获取
		usage.OutputTokens = msgDelta.Usage.OutputTokens

		// 如果 message_start 中没有值，则从 message_delta 获取（兼容GLM等API）
		if usage.InputTokens == 0 {
			usage.InputTokens = msgDelta.Usage.InputTokens
		}
		if usage.CacheCreationInputTokens == 0 {
			usage.CacheCreationInputTokens = msgDelta.Usage.CacheCreationInputTokens
		}
		if usage.CacheReadInputTokens == 0 {
			usage.CacheReadInputTokens = msgDelta.Usage.CacheReadInputTokens
		}
	}
}

// ---------------------------------------------------------------------------
// Cache usage rewrite helpers (隐藏 cache tokens，合并入 input_tokens)
// ---------------------------------------------------------------------------

// mergeCacheTokensInUsage 在 map[string]any 中合并 cache tokens 到 input_tokens。
// 同时删除 cache 字段（包括零值），确保客户端看不到 cache 相关信息。
// 返回 true 表示有修改。
func mergeCacheTokensInUsage(usageMap map[string]any) bool {
	if usageMap == nil {
		return false
	}

	_, hasCreate := usageMap["cache_creation_input_tokens"]
	_, hasRead := usageMap["cache_read_input_tokens"]
	if !hasCreate && !hasRead {
		return false
	}

	cacheCreate := intFromAny(usageMap["cache_creation_input_tokens"])
	cacheRead := intFromAny(usageMap["cache_read_input_tokens"])

	if cacheCreate != 0 || cacheRead != 0 {
		inputTokens := intFromAny(usageMap["input_tokens"])
		// 保持 float64 类型一致性（json.Unmarshal 产出 float64）
		usageMap["input_tokens"] = float64(inputTokens + cacheCreate + cacheRead)
	}
	delete(usageMap, "cache_creation_input_tokens")
	delete(usageMap, "cache_read_input_tokens")
	return true
}

// rewriteCacheUsageInJSON 改写 JSON 响应体中的 usage，将 cache tokens 合并入 input_tokens。
func rewriteCacheUsageInJSON(body []byte) []byte {
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return body
	}
	usageMap, ok := data["usage"].(map[string]any)
	if !ok {
		return body
	}
	if !mergeCacheTokensInUsage(usageMap) {
		return body
	}
	result, err := json.Marshal(data)
	if err != nil {
		return body
	}
	return result
}

// rewriteCacheUsageInSSELine 改写 SSE data 行中的 usage，将 cache tokens 合并入 input_tokens。
// 处理 message_start（usage 在 message.usage 中）和 message_delta（usage 在 usage 中）。
func rewriteCacheUsageInSSELine(line string) string {
	if !sseDataRe.MatchString(line) {
		return line
	}
	data := sseDataRe.ReplaceAllString(line, "")
	if data == "" || data == "[DONE]" {
		return line
	}

	var event map[string]any
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return line
	}

	modified := false
	eventType, _ := event["type"].(string)

	switch eventType {
	case "message_start":
		if msg, ok := event["message"].(map[string]any); ok {
			if usageMap, ok := msg["usage"].(map[string]any); ok {
				if mergeCacheTokensInUsage(usageMap) {
					modified = true
				}
			}
		}
	case "message_delta":
		if usageMap, ok := event["usage"].(map[string]any); ok {
			if mergeCacheTokensInUsage(usageMap) {
				modified = true
			}
		}
	}

	if !modified {
		return line
	}

	newData, err := json.Marshal(event)
	if err != nil {
		return line
	}
	return "data: " + string(newData)
}

// mergeCacheTokensToInput 在 ForwardResult.Usage 中合并 cache tokens 到 input_tokens。
// 用于确保下游计费按 input 单价计算所有 token。
func mergeCacheTokensToInput(usage *ClaudeUsage) {
	if usage == nil {
		return
	}
	usage.InputTokens += usage.CacheCreationInputTokens + usage.CacheReadInputTokens
	usage.CacheCreationInputTokens = 0
	usage.CacheReadInputTokens = 0
}

// intFromAny 从 any 中提取 int（兼容 json.Unmarshal 的 float64、json.Number 和原生整数）
func intFromAny(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
		return 0
	case int:
		return n
	case int64:
		return int(n)
	default:
		return 0
	}
}

func (s *GatewayService) handleNonStreamingResponse(ctx context.Context, resp *http.Response, c *gin.Context, account *Account, originalModel, mappedModel string, hideCacheUsage bool) (*ClaudeUsage, error) {
	// 更新5h窗口状态
	s.rateLimitService.UpdateSessionWindow(ctx, account, resp.Header)

	// 成功响应：异步清除限流状态（如果之前被限流）
	if account.IsRateLimited() {
		go func(accountID int64) {
			clearCtx := context.Background()
			if err := s.rateLimitService.ClearRateLimit(clearCtx, accountID); err != nil {
				log.Printf("[RateLimit] Failed to clear rate limit for account %d: %v", accountID, err)
			}
		}(account.ID)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// 解析usage
	var response struct {
		Usage ClaudeUsage `json:"usage"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		// 打印上游返回的实际内容（截取前 500 字节用于调试）
		bodyPreview := string(body)
		if len(bodyPreview) > 500 {
			bodyPreview = bodyPreview[:500] + "..."
		}
		log.Printf("Account %d: failed to parse upstream response (status=%d): %s", account.ID, resp.StatusCode, bodyPreview)
		return nil, fmt.Errorf("parse response: %w", err)
	}

	// 如果有模型映射，替换响应中的model字段
	if originalModel != mappedModel {
		body = s.replaceModelInResponseBody(body, mappedModel, originalModel)
	}

	responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.cfg.Security.ResponseHeaders)

	contentType := "application/json"
	if s.cfg != nil && !s.cfg.Security.ResponseHeaders.Enabled {
		if upstreamType := resp.Header.Get("Content-Type"); upstreamType != "" {
			contentType = upstreamType
		}
	}

	// 仅平台注入时隐藏 cache tokens：合并入 input_tokens 后再发给用户
	if hideCacheUsage {
		body = rewriteCacheUsageInJSON(body)
	}

	// 写入响应
	c.Data(resp.StatusCode, contentType, body)

	return &response.Usage, nil
}

// replaceModelInResponseBody 替换响应体中的model字段
func (s *GatewayService) replaceModelInResponseBody(body []byte, fromModel, toModel string) []byte {
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		return body
	}

	model, ok := resp["model"].(string)
	if !ok || model != fromModel {
		return body
	}

	resp["model"] = toModel
	newBody, err := json.Marshal(resp)
	if err != nil {
		return body
	}

	return newBody
}

// RecordUsageInput 记录使用量的输入参数
type RecordUsageInput struct {
	Result       *ForwardResult
	APIKey       *APIKey
	User         *User
	Account      *Account
	Subscription *UserSubscription // 可选：订阅信息
	UserAgent    string            // 请求的 User-Agent
	IPAddress    string            // 请求的客户端 IP 地址
	TempAPIKeyID *int64            // 临时 API Key ID
	TempAPIKey   *TempAPIKey       // 临时 API Key 对象（用于 quota_only 类型更新消费金额）
}

// RecordUsage 记录使用量并扣费（或更新订阅用量）
func (s *GatewayService) RecordUsage(ctx context.Context, input *RecordUsageInput) error {
	result := input.Result
	apiKey := input.APIKey
	user := input.User
	account := input.Account
	subscription := input.Subscription

	// 获取费率倍数：用户自定义 > 分组默认 > 全局默认
	multiplier := s.cfg.Default.RateMultiplier
	if apiKey.GroupID != nil && apiKey.Group != nil {
		multiplier = apiKey.Group.RateMultiplier
		// 用户自定义分组倍率覆盖
		if userRate, ok := user.GetGroupRateMultiplier(*apiKey.GroupID); ok {
			multiplier = userRate
		}
	}

	var cost *CostBreakdown

	// 根据请求类型选择计费方式
	if result.ImageCount > 0 {
		// 图片生成计费
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
		// Token 计费
		tokens := UsageTokens{
			InputTokens:           result.Usage.InputTokens,
			OutputTokens:          result.Usage.OutputTokens,
			CacheCreationTokens:   result.Usage.CacheCreationInputTokens,
			CacheReadTokens:       result.Usage.CacheReadInputTokens,
			CacheCreation5mTokens: result.Usage.CacheCreation5mTokens,
			CacheCreation1hTokens: result.Usage.CacheCreation1hTokens,
		}
		var err error
		cost, err = s.billingService.CalculateCost(result.Model, tokens, multiplier)
		if err != nil {
			log.Printf("Calculate cost failed: %v", err)
			cost = &CostBreakdown{ActualCost: 0}
		}
	}

	// 判断计费方式：订阅模式 vs 余额模式
	isSubscriptionBilling := subscription != nil && apiKey.Group != nil && apiKey.Group.IsSubscriptionType()
	billingType := BillingTypeBalance
	if isSubscriptionBilling {
		billingType = BillingTypeSubscription
	}

	// 创建使用日志
	durationMs := int(result.Duration.Milliseconds())
	var imageSize *string
	if result.ImageSize != "" {
		imageSize = &result.ImageSize
	}
	accountRateMultiplier := account.BillingRateMultiplier()
	var apiKeyIDPtr *int64
	if apiKey.ID != 0 {
		apiKeyIDPtr = &apiKey.ID
	}
	usageLog := &UsageLog{
		UserID:                user.ID,
		APIKeyID:              apiKeyIDPtr,
		AccountID:             account.ID,
		RequestID:             result.RequestID,
		Model:                 result.Model,
		InputTokens:           result.Usage.InputTokens,
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

	// 添加分组和订阅关联
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
	if err != nil {
		log.Printf("Create usage log failed: %v", err)
	}

	if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		log.Printf("[SIMPLE MODE] Usage recorded (not billed): user=%d, tokens=%d", usageLog.UserID, usageLog.TotalTokens())
		s.deferredService.ScheduleLastUsedUpdate(account.ID)
		return nil
	}

	shouldBill := inserted || err != nil

	// 根据计费类型执行扣费
	if isSubscriptionBilling {
		// 订阅模式：更新订阅用量（使用 TotalCost 原始费用，不考虑倍率）
		if shouldBill && cost.TotalCost > 0 {
			if err := s.userSubRepo.IncrementUsage(ctx, subscription.ID, cost.TotalCost); err != nil {
				log.Printf("Increment subscription usage failed: %v", err)
			}
			// 异步更新订阅缓存
			s.billingCacheService.QueueUpdateSubscriptionUsage(user.ID, *apiKey.GroupID, cost.TotalCost)
		}
	} else {
		// 余额模式：扣除用户余额（使用 ActualCost 考虑倍率后的费用）
		if shouldBill && cost.ActualCost > 0 {
			if err := s.userRepo.DeductBalance(ctx, user.ID, cost.ActualCost); err != nil {
				log.Printf("Deduct balance failed: %v", err)
			}
			// 异步更新余额缓存
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

	// 更新 time_quota 临时 API Key 的每日消费金额
	if input.TempAPIKey != nil && input.TempAPIKey.KeyType == TempAPIKeyTypeTimeQuota && cost.ActualCost > 0 {
		if s.tempAPIKeyRepo != nil {
			if _, _, err := s.tempAPIKeyRepo.AddDailyCostUSD(ctx, input.TempAPIKey.ID, cost.ActualCost); err != nil {
				log.Printf("Update temp API key daily cost failed: %v", err)
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

// ForwardCountTokens 转发 count_tokens 请求到上游 API
// 特点：不记录使用量、仅支持非流式响应
func (s *GatewayService) ForwardCountTokens(ctx context.Context, c *gin.Context, account *Account, parsed *ParsedRequest) error {
	if parsed == nil {
		s.countTokensError(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return fmt.Errorf("parse request: empty request")
	}

	body := parsed.Body
	reqModel := parsed.Model

	// Antigravity 账户不支持 count_tokens 转发，直接返回空值
	if account.Platform == PlatformAntigravity {
		c.JSON(http.StatusOK, gin.H{"input_tokens": 0})
		return nil
	}

	// Kiro 账户使用本地 token 估算（Kiro 的 AWS token 无法调用 Anthropic count_tokens API）
	if account.Platform == PlatformKiro {
		claudeReq, err := kiro.ParseClaudeRequestFromJSON(body)
		if err != nil {
			s.countTokensError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request")
			return fmt.Errorf("parse claude request for count_tokens: %w", err)
		}
		estimatedTokens := kiro.EstimateInputTokens(claudeReq)
		c.JSON(http.StatusOK, gin.H{"input_tokens": estimatedTokens})
		return nil
	}

	// 应用模型映射（仅对 apikey 类型账号）
	if account.Type == AccountTypeAPIKey {
		if reqModel != "" {
			mappedModel := account.GetMappedModel(reqModel)
			if mappedModel != reqModel {
				body = s.replaceModelInBody(body, mappedModel)
				reqModel = mappedModel
				log.Printf("CountTokens model mapping applied: %s -> %s (account: %s)", parsed.Model, mappedModel, account.Name)
			}
		}
	}

	// 获取凭证
	token, tokenType, err := s.GetAccessToken(ctx, account)
	if err != nil {
		s.countTokensError(c, http.StatusBadGateway, "upstream_error", "Failed to get access token")
		return err
	}

	// 构建上游请求
	upstreamReq, err := s.buildCountTokensRequest(ctx, c, account, body, token, tokenType, reqModel)
	if err != nil {
		s.countTokensError(c, http.StatusInternalServerError, "api_error", "Failed to build request")
		return err
	}

	// 获取代理URL
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	// 发送请求
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		setOpsUpstreamError(c, 0, sanitizeUpstreamErrorMessage(err.Error()), "")
		s.countTokensError(c, http.StatusBadGateway, "upstream_error", "Request failed")
		return fmt.Errorf("upstream request failed: %w", err)
	}

	// 读取响应体
	respBody, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		s.countTokensError(c, http.StatusBadGateway, "upstream_error", "Failed to read response")
		return err
	}

	// 检测 thinking block 签名错误（400）并重试一次（过滤 thinking blocks）
	if resp.StatusCode == 400 && isThinkingBlockSignatureError(respBody) {
		log.Printf("Account %d: detected thinking block signature error on count_tokens, retrying with filtered thinking blocks", account.ID)

		filteredBody := FilterThinkingBlocksForRetry(body)
		retryReq, buildErr := s.buildCountTokensRequest(ctx, c, account, filteredBody, token, tokenType, reqModel)
		if buildErr == nil {
			retryResp, retryErr := s.httpUpstream.Do(retryReq, proxyURL, account.ID, account.Concurrency)
			if retryErr == nil {
				resp = retryResp
				respBody, err = io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if err != nil {
					s.countTokensError(c, http.StatusBadGateway, "upstream_error", "Failed to read response")
					return err
				}
			}
		}
	}

	// 处理错误响应
	if resp.StatusCode >= 400 {
		// 标记账号状态（429/529等）
		s.rateLimitService.HandleUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody)

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
		setOpsUpstreamError(c, resp.StatusCode, upstreamMsg, upstreamDetail)

		// 记录上游错误摘要便于排障（不回显请求内容）
		if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
			log.Printf(
				"count_tokens upstream error %d (account=%d platform=%s type=%s): %s",
				resp.StatusCode,
				account.ID,
				account.Platform,
				account.Type,
				truncateForLog(respBody, s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes),
			)
		}

		// 返回简化的错误响应
		errMsg := "Upstream request failed"
		switch resp.StatusCode {
		case 429:
			errMsg = "Rate limit exceeded"
		case 529:
			errMsg = "Service overloaded"
		}
		s.countTokensError(c, resp.StatusCode, "upstream_error", errMsg)
		if upstreamMsg == "" {
			return fmt.Errorf("upstream error: %d", resp.StatusCode)
		}
		return fmt.Errorf("upstream error: %d message=%s", resp.StatusCode, upstreamMsg)
	}

	// 透传成功响应
	c.Data(resp.StatusCode, "application/json", respBody)
	return nil
}

// buildCountTokensRequest 构建 count_tokens 上游请求
func (s *GatewayService) buildCountTokensRequest(ctx context.Context, c *gin.Context, account *Account, body []byte, token, tokenType, modelID string) (*http.Request, error) {
	// 确定目标 URL
	targetURL := claudeAPICountTokensURL
	if account.Type == AccountTypeAPIKey {
		baseURL := account.GetBaseURL()
		if baseURL != "" {
			validatedURL, err := s.validateUpstreamBaseURL(baseURL)
			if err != nil {
				return nil, err
			}
			targetURL = validatedURL + "/v1/messages/count_tokens"
		}
	}

	// OAuth 账号：应用统一指纹和重写 userID
	if account.IsOAuth() && s.identityService != nil {
		fp, err := s.identityService.GetOrCreateFingerprint(ctx, account.ID, c.Request.Header)
		if err == nil {
			accountUUID := account.GetExtraString("account_uuid")
			if accountUUID != "" && fp.ClientID != "" {
				scopeKey := c.GetHeader("X-Conversation-ID")
				if scopeKey == "" {
					if val, exists := c.Get("api_key"); exists {
						if apiKey, ok := val.(*APIKey); ok && apiKey.ID != 0 {
							scopeKey = fmt.Sprintf("apikey_%d", apiKey.ID)
						}
					}
				}
				if newBody, err := s.identityService.RewriteUserID(body, account.ID, accountUUID, fp.ClientID, scopeKey); err == nil && len(newBody) > 0 {
					body = newBody
				}
			}
		}
	}

	req, err := http.NewRequestWithContext(ctx, "POST", targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	// 设置认证头
	if tokenType == "oauth" {
		req.Header.Set("authorization", "Bearer "+token)
	} else {
		req.Header.Set("x-api-key", token)
		// Anthropic-compatible upstreams may require Authorization even when x-api-key is present.
		if account.Platform == PlatformAnthropic {
			req.Header.Set("authorization", "Bearer "+token)
		}
	}

	// 白名单透传 headers
	for key, values := range c.Request.Header {
		lowerKey := strings.ToLower(key)
		if allowedHeaders[lowerKey] {
			for _, v := range values {
				req.Header.Add(key, v)
			}
		}
	}

	// OAuth 账号：应用指纹到请求头
	if account.IsOAuth() && s.identityService != nil {
		fp, _ := s.identityService.GetOrCreateFingerprint(ctx, account.ID, c.Request.Header)
		if fp != nil {
			s.identityService.ApplyFingerprint(req, fp)
		}
	}

	// 确保必要的 headers 存在
	if req.Header.Get("content-type") == "" {
		req.Header.Set("content-type", "application/json")
	}
	if req.Header.Get("anthropic-version") == "" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}

	// OAuth 账号：处理 anthropic-beta header
	if tokenType == "oauth" {
		req.Header.Set("anthropic-beta", s.getBetaHeader(modelID, c.GetHeader("anthropic-beta")))
	} else if s.cfg != nil && s.cfg.Gateway.InjectBetaForAPIKey && req.Header.Get("anthropic-beta") == "" {
		// API-key：与 messages 同步的按需 beta 注入（默认关闭）
		if requestNeedsBetaFeatures(body) {
			if beta := defaultAPIKeyBetaHeader(body); beta != "" {
				req.Header.Set("anthropic-beta", beta)
			}
		}
	}

	return req, nil
}

// countTokensError 返回 count_tokens 错误响应
func (s *GatewayService) countTokensError(c *gin.Context, status int, errType, message string) {
	c.JSON(status, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
}

func (s *GatewayService) validateUpstreamBaseURL(raw string) (string, error) {
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

// GetAvailableModels returns the list of models available for a group
// It aggregates model_mapping keys from all schedulable accounts in the group
func (s *GatewayService) GetAvailableModels(ctx context.Context, groupID *int64, platform string) []string {
	var accounts []Account
	var err error

	if groupID != nil {
		accounts, err = s.accountRepo.ListSchedulableByGroupID(ctx, *groupID)
	} else {
		accounts, err = s.accountRepo.ListSchedulable(ctx)
	}

	if err != nil || len(accounts) == 0 {
		return nil
	}

	// Filter by platform if specified
	if platform != "" {
		filtered := make([]Account, 0)
		for _, acc := range accounts {
			if acc.Platform == platform {
				filtered = append(filtered, acc)
			}
		}
		accounts = filtered
	}

	// Collect unique models from all accounts
	modelSet := make(map[string]struct{})
	hasAnyMapping := false

	for _, acc := range accounts {
		mapping := acc.GetModelMapping()
		if len(mapping) > 0 {
			hasAnyMapping = true
			for model := range mapping {
				if acc.Platform == PlatformKiro && !acc.IsKiroApiKey() && isClaudeOpus55Model(model) {
					continue
				}
				modelSet[model] = struct{}{}
			}
			continue
		}
		if acc.Platform == PlatformOpenAI && (platform == "" || platform == PlatformOpenAI) {
			hasAnyMapping = true
			for _, model := range openai.DefaultModelsForAccount(acc.IsOAuth()) {
				modelSet[model.ID] = struct{}{}
			}
			continue
		}
		if acc.Platform == PlatformKiro && acc.IsKiroApiKey() {
			hasAnyMapping = true
			for _, model := range KiroAPIKeyDefaultModels() {
				modelSet[model.ID] = struct{}{}
			}
			continue
		}
		if acc.Platform == PlatformKiro && !acc.IsKiroApiKey() {
			hasAnyMapping = true
			for _, model := range kiro.DefaultModelIDs() {
				modelSet[model] = struct{}{}
			}
			continue
		}
		if acc.Platform == PlatformGrok {
			hasAnyMapping = true
			for _, model := range xai.DefaultModelIDs() {
				modelSet[model] = struct{}{}
			}
		}
	}

	// If no account has an explicit model mapping or a built-in model catalog, return nil.
	if !hasAnyMapping {
		return nil
	}

	// Convert to slice
	models := make([]string, 0, len(modelSet))
	for model := range modelSet {
		models = append(models, model)
	}
	sort.Strings(models)

	return models
}

// 配额检查阈值常量
const (
	// defaultQuotaThreshold 默认配额阈值，超过此值的账号将被过滤（百分比，80 表示 80%）
	// 注意：此值应与 account_usage_service.go 中的 quotaHealthyThreshold 保持一致
	defaultQuotaThreshold = 80
)

// isAccountQuotaAvailable 检查账号配额是否可用
// 对于 Antigravity 账号，检查缓存中的配额使用率是否超过阈值
// 返回 true 表示配额可用，false 表示配额已超过阈值
func (s *GatewayService) isAccountQuotaAvailable(account *Account) bool {
	if account == nil {
		return false
	}

	// 仅对 Antigravity 账号进行配额检查
	if account.Platform != PlatformAntigravity {
		return true
	}

	// 如果没有配置 usageCache，跳过配额检查
	if s.usageCache == nil {
		return true
	}

	// 首先检查配额健康状态（由请求完成后的刷新逻辑标记）
	if !s.usageCache.IsQuotaHealthy(account.ID) {
		return false
	}

	// 从缓存中获取配额信息
	cached, ok := s.usageCache.antigravityCache.Load(account.ID)
	if !ok {
		// 没有缓存数据，允许使用（后续请求会触发配额获取）
		return true
	}

	cache, ok := cached.(*antigravityUsageCache)
	if !ok {
		return true
	}

	// 检查缓存是否过期（3 分钟 TTL）
	if time.Since(cache.timestamp) >= apiCacheTTL {
		// 缓存过期，允许使用
		return true
	}

	usageInfo := cache.usageInfo
	if usageInfo == nil || usageInfo.FiveHour == nil {
		return true
	}

	// 检查 FiveHour 使用率是否超过阈值
	return usageInfo.FiveHour.Utilization < defaultQuotaThreshold
}

// getAccountResetTime 获取账号的配额重置时间
// 用于排序时优先选择临近重置的账号（重置时间越近越优先）
func (s *GatewayService) getAccountResetTime(accountID int64) *time.Time {
	if s.usageCache == nil {
		return nil
	}
	return s.usageCache.GetResetTime(accountID)
}

// getKiroAvailableCredits 获取 Kiro 账号的可用积分余额
// 返回：可用积分数，-1 表示无法获取
func (s *GatewayService) getKiroAvailableCredits(accountID int64) float64 {
	if s.usageCache == nil {
		return -1
	}
	return s.usageCache.GetKiroAvailableCredits(accountID)
}

// RefreshAntigravityQuotaAsync 异步刷新 Antigravity 账号配额并更新健康状态
// 用于粘性会话场景：请求完成后刷新当前账号配额
func (s *GatewayService) RefreshAntigravityQuotaAsync(accountID int64) {
	if s.accountUsageService == nil || s.usageCache == nil {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		healthy, err := s.accountUsageService.RefreshAndCheckQuotaHealth(ctx, accountID)
		if err != nil {
			log.Printf("[QuotaRefresh] account=%d refresh failed: %v", accountID, err)
			return
		}

		if !healthy {
			s.usageCache.MarkQuotaUnhealthy(accountID)
			log.Printf("[QuotaRefresh] account=%d marked unhealthy (utilization >= %.0f%%)", accountID, quotaHealthyThreshold)
		} else {
			// 如果之前不健康，现在恢复了
			if !s.usageCache.IsQuotaHealthy(accountID) {
				s.usageCache.MarkQuotaHealthy(accountID)
				log.Printf("[QuotaRefresh] account=%d recovered to healthy", accountID)
			}
		}
	}()
}

// StartQuotaRecoveryTask 启动后台配额恢复任务
// 每 30 分钟检查配额不健康的账号，刷新配额并尝试恢复
func (s *GatewayService) StartQuotaRecoveryTask(ctx context.Context) {
	if s.accountUsageService == nil || s.usageCache == nil {
		return
	}

	ticker := time.NewTicker(30 * time.Minute)
	go func() {
		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				return
			case <-ticker.C:
				s.recoverUnhealthyAccounts(ctx)
			}
		}
	}()
}

// recoverUnhealthyAccounts 恢复配额不健康的账号
func (s *GatewayService) recoverUnhealthyAccounts(ctx context.Context) {
	unhealthyIDs := s.usageCache.GetUnhealthyAccountIDs()
	if len(unhealthyIDs) == 0 {
		return
	}

	log.Printf("[QuotaRecovery] checking %d unhealthy accounts", len(unhealthyIDs))

	healthMap := s.accountUsageService.RefreshQuotaBatch(ctx, unhealthyIDs)
	recovered := 0
	for accountID, healthy := range healthMap {
		if healthy {
			s.usageCache.MarkQuotaHealthy(accountID)
			recovered++
		}
	}

	if recovered > 0 {
		log.Printf("[QuotaRecovery] recovered %d/%d accounts", recovered, len(unhealthyIDs))
	}
}
