package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
)

const (
	kiroMaxRetries                     = 3
	kiroRetryBaseDelay                 = 1 * time.Second
	kiroRetryMaxDelay                  = 16 * time.Second
	defaultKiroStreamKeepaliveInterval = 15 * time.Second

	// kiroStreamCommitDeadline 是"上游迟迟不发首帧"时仍然提交 SSE 信封的上限。
	//
	// 权衡：提交越晚，可 failover 的窗口越大；但客户端与中间层
	// （Cloudflare 约 120s）在收到任何字节前会超时断开。取 20s 是因为
	// in-band exception / 空流几乎总在首帧就暴露，20s 足以覆盖，
	// 又远小于 120s 窗口。
	kiroStreamCommitDeadline = 20 * time.Second
)

// kiroEndpointConfig defines an upstream endpoint for Kiro requests
type kiroEndpointConfig struct {
	URL       string // Full endpoint URL
	Host      string // Host header value
	AmzTarget string // X-Amz-Target header (empty for AWSQ)
	Name      string // Endpoint name for logging
}

// getKiroEndpoints returns ordered endpoint list based on config + account.
//
// Endpoint family (global) selects which upstream domain family to use:
//   - "kiro"   (default): runtime.us-east-1.kiro.dev — no fallback, as AWS official
//     firewall docs no longer list codewhisperer.*.amazonaws.com.
//   - "legacy"          : q.us-east-1.amazonaws.com + codewhisperer.us-east-1.amazonaws.com
//     as fallback, preserving the previous dual-endpoint behavior.
//
// Per-account preferred_endpoint only applies to the legacy family (controls order
// between AWSQ and CodeWhisperer). The kiro family returns a single endpoint.
//
// Request routing always uses us-east-1 regardless of account's token region.
// Verified: EU accounts (e.g. eu-north-1 IdC) can send requests to us-east-1 endpoints.
func getKiroEndpoints(account *Account, cfg *config.Config) []kiroEndpointConfig {
	family := kiro.ServiceEndpointFamilyKiro
	if cfg != nil {
		family = kiro.NormalizeServiceEndpointFamily(cfg.Kiro.ServiceEndpointFamily)
	}

	// Kiro family: single endpoint, no fallback (codewhisperer.*.amazonaws.com deprecated).
	if family == kiro.ServiceEndpointFamilyKiro {
		host := kiro.RuntimeHost(kiro.DefaultRegion, family)
		return []kiroEndpointConfig{{
			URL:  kiro.GenerateAssistantResponseURL(kiro.DefaultRegion, family),
			Host: host,
			Name: "KiroRuntime",
		}}
	}

	// Legacy family: dual endpoint with account-level preference.
	// AWSQ supports thinking; CW does not. Default order is AWSQ first.
	awsq := kiroEndpointConfig{
		URL:  kiro.GenerateAssistantResponseURL(kiro.DefaultRegion, kiro.ServiceEndpointFamilyLegacy),
		Host: kiro.RuntimeHost(kiro.DefaultRegion, kiro.ServiceEndpointFamilyLegacy),
		Name: "AWSQ",
	}
	cw := kiroEndpointConfig{
		URL:       "https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse",
		Host:      "codewhisperer.us-east-1.amazonaws.com",
		AmzTarget: "AmazonCodeWhispererStreamingService.GenerateAssistantResponse",
		Name:      "CodeWhisperer",
	}

	// Explicit preferred_endpoint overrides default order
	switch account.GetKiroPreferredEndpoint() {
	case "awsq", "q", "cli":
		return []kiroEndpointConfig{awsq, cw}
	case "cw", "codewhisperer", "kiro":
		return []kiroEndpointConfig{cw, awsq}
	}

	// Default: AWSQ first (supports thinking), CW as fallback
	return []kiroEndpointConfig{awsq, cw}
}

// KiroGatewayService handles Kiro/CodeWhisperer platform API forwarding
type KiroGatewayService struct {
	accountRepo          AccountRepository
	tokenProvider        *KiroTokenProvider
	rateLimitService     *RateLimitService
	httpUpstream         HTTPUpstream
	settingService       *SettingService
	usageCache           *UsageCache
	cfg                  *config.Config
	proxyRepo            ProxyRepository    // proxy pool data source for Free-tier rotation
	proxyPool            []Proxy            // cached active proxy list
	proxyPoolMu          sync.RWMutex       // protects proxyPool
	proxyPoolUpdated     time.Time          // cache timestamp
	modelCapabilityCache sync.Map           // account+model -> dynamic capability state
	freeTierSF           singleflight.Group // dedup concurrent subscription type fetches per account

	// openAICommitDeadline / streamCommitDeadline 覆盖对应路径的信封提交上限，
	// 仅测试用。为 0 时取各自的默认常量。
	openAICommitDeadline time.Duration
	streamCommitDeadline time.Duration
}

// NewKiroGatewayService creates a new KiroGatewayService
func NewKiroGatewayService(
	accountRepo AccountRepository,
	tokenProvider *KiroTokenProvider,
	rateLimitService *RateLimitService,
	httpUpstream HTTPUpstream,
	settingService *SettingService,
	usageCache *UsageCache,
	proxyRepo ProxyRepository,
	cfg *config.Config,
) *KiroGatewayService {
	return &KiroGatewayService{
		accountRepo:      accountRepo,
		tokenProvider:    tokenProvider,
		rateLimitService: rateLimitService,
		httpUpstream:     httpUpstream,
		settingService:   settingService,
		usageCache:       usageCache,
		proxyRepo:        proxyRepo,
		cfg:              cfg,
	}
}

// GetTokenProvider returns the token provider
func (s *KiroGatewayService) GetTokenProvider() *KiroTokenProvider {
	return s.tokenProvider
}

func (s *KiroGatewayService) kiroStreamKeepaliveInterval() time.Duration {
	cfg := s.cfg
	if s.settingService != nil && s.settingService.cfg != nil {
		cfg = s.settingService.cfg
	}
	if cfg != nil && cfg.Gateway.StreamKeepaliveInterval > 0 {
		return time.Duration(cfg.Gateway.StreamKeepaliveInterval) * time.Second
	}
	return defaultKiroStreamKeepaliveInterval
}

// applyKiroConnectionHeader 按配置决定是否发送 Connection: close。
//
// 默认不发（keep-alive）。发送 close 会让每个请求都重建 TCP + TLS 连接，
// 单账号并发一高就出现握手风暴、临时端口/TIME_WAIT 堆积，
// 以及 MaxConnsPerHost 槽位排队 —— 这些失败请求根本到不了上游，
// 上游侧查无记录，正是排查 502 时"上游说没收到"的成因之一。
func (s *KiroGatewayService) applyKiroConnectionHeader(req *http.Request) {
	if s.cfg != nil && s.cfg.Kiro.UpstreamConnectionClose {
		req.Header.Set("Connection", "close")
	}
}

// applyRequestJitter sleeps a random duration between configured min/max before an upstream request.
// This prevents thundering herd when multiple accounts fire requests simultaneously.
// No-op if max is 0 or config is nil.
func (s *KiroGatewayService) applyRequestJitter(ctx context.Context) {
	if s.cfg == nil {
		return
	}
	minMs := s.cfg.Gateway.RequestJitterMinMs
	maxMs := s.cfg.Gateway.RequestJitterMaxMs
	if maxMs <= 0 {
		return
	}
	if minMs > maxMs {
		minMs = maxMs
	}

	var delayMs int
	if minMs == maxMs {
		delayMs = maxMs
	} else {
		delayMs = minMs + rand.IntN(maxMs-minMs+1)
	}
	if delayMs <= 0 {
		return
	}

	t := time.NewTimer(time.Duration(delayMs) * time.Millisecond)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

type kiroInitialDeadline struct {
	start    time.Time
	timeout  time.Duration
	cancel   context.CancelFunc
	timer    *time.Timer
	timedOut atomic.Bool
}

type kiroInitialDeadlineContextKey struct{}

type kiroInitialDeadlineBody struct {
	io.ReadCloser
	deadline *kiroInitialDeadline
	once     sync.Once
	closeErr error
}

func (b *kiroInitialDeadlineBody) Close() error {
	b.once.Do(func() {
		if b.ReadCloser != nil {
			b.closeErr = b.ReadCloser.Close()
		}
		b.deadline.close()
	})
	return b.closeErr
}

func (s *KiroGatewayService) kiroOAuthInitialResponseTimeout(account *Account) time.Duration {
	return 0
}

func (s *KiroGatewayService) startKiroInitialDeadline(ctx context.Context, account *Account) (context.Context, *kiroInitialDeadline) {
	timeout := s.kiroOAuthInitialResponseTimeout(account)
	if timeout <= 0 {
		return ctx, nil
	}

	reqCtx, cancel := context.WithCancel(ctx)
	deadline := &kiroInitialDeadline{
		start:   time.Now(),
		timeout: timeout,
		cancel:  cancel,
	}
	deadline.timer = time.AfterFunc(timeout, func() {
		deadline.timedOut.Store(true)
		cancel()
	})
	return reqCtx, deadline
}

func (d *kiroInitialDeadline) stopHeaderTimer() {
	if d == nil || d.timer == nil {
		return
	}
	d.timer.Stop()
}

func (d *kiroInitialDeadline) close() {
	if d == nil {
		return
	}
	d.stopHeaderTimer()
	if d.cancel != nil {
		d.cancel()
	}
}

func (d *kiroInitialDeadline) isTimedOut() bool {
	return d != nil && d.timedOut.Load()
}

func (d *kiroInitialDeadline) isTimeoutError(err error) bool {
	if d == nil {
		return false
	}
	if d.isTimedOut() {
		return true
	}
	if d.timeout <= 0 || time.Since(d.start) < d.timeout {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (d *kiroInitialDeadline) remaining() time.Duration {
	if d == nil || d.timeout <= 0 {
		return 0
	}
	remaining := d.timeout - time.Since(d.start)
	if remaining <= 0 {
		return time.Nanosecond
	}
	return remaining
}

func (d *kiroInitialDeadline) streamOptions() kiroStreamOptions {
	if d == nil {
		return kiroStreamOptions{}
	}
	return kiroStreamOptions{initialResponseTimeout: d.remaining()}
}

func attachKiroInitialDeadline(resp *http.Response, deadline *kiroInitialDeadline) *kiroInitialDeadline {
	if resp == nil || deadline == nil {
		return deadline
	}
	if resp.Body != nil {
		resp.Body = &kiroInitialDeadlineBody{ReadCloser: resp.Body, deadline: deadline}
	}
	if resp.Request != nil {
		resp.Request = resp.Request.WithContext(context.WithValue(resp.Request.Context(), kiroInitialDeadlineContextKey{}, deadline))
	}
	return deadline
}

func getKiroInitialDeadline(resp *http.Response) *kiroInitialDeadline {
	if resp == nil || resp.Request == nil {
		return nil
	}
	deadline, _ := resp.Request.Context().Value(kiroInitialDeadlineContextKey{}).(*kiroInitialDeadline)
	return deadline
}

func kiroInitialResponseTimeoutMessage(phase string, timeout time.Duration) string {
	message := "kiro_initial_response_timeout"
	if phase = strings.TrimSpace(phase); phase != "" {
		message += ":" + phase
	}
	if timeout > 0 {
		message += ":timeout=" + timeout.String()
	}
	return message
}

func kiroInitialResponseFailover(phase string, timeout time.Duration) *UpstreamFailoverError {
	return &UpstreamFailoverError{
		StatusCode: http.StatusGatewayTimeout,
		Message:    kiroInitialResponseTimeoutMessage(phase, timeout),
	}
}

func isKiroInitialResponseTimeout(err error) (*kiroInitialResponseTimeoutError, bool) {
	var timeoutErr *kiroInitialResponseTimeoutError
	if errors.As(err, &timeoutErr) {
		return timeoutErr, true
	}
	return nil, false
}

const (
	// freeTierMachineIDRotatePercent is the probability (0-100) of using a random machine ID
	// for Free-tier accounts instead of the deterministic SHA256(refreshToken) one.
	freeTierMachineIDRotatePercent = 20

	// proxyPoolCacheTTL is how long the active proxy list is cached before refresh.
	proxyPoolCacheTTL = 5 * time.Minute
)

// getActiveProxyPool returns the cached list of active proxies, refreshing if stale.
// Fail-open: returns stale data if refresh fails; returns nil if no data at all.
func (s *KiroGatewayService) getActiveProxyPool(ctx context.Context) []Proxy {
	if s.proxyRepo == nil {
		return nil
	}

	// Fast path: read lock check
	s.proxyPoolMu.RLock()
	if time.Since(s.proxyPoolUpdated) < proxyPoolCacheTTL && len(s.proxyPool) > 0 {
		pool := s.proxyPool
		s.proxyPoolMu.RUnlock()
		return pool
	}
	s.proxyPoolMu.RUnlock()

	// Slow path: write lock refresh
	s.proxyPoolMu.Lock()
	defer s.proxyPoolMu.Unlock()

	// Double-check after acquiring write lock
	if time.Since(s.proxyPoolUpdated) < proxyPoolCacheTTL && len(s.proxyPool) > 0 {
		return s.proxyPool
	}

	proxies, err := s.proxyRepo.ListActive(ctx)
	if err != nil {
		log.Printf("[kiro-freeTier] proxy_pool_refresh failed: %v, using stale data (len=%d)", err, len(s.proxyPool))
		// Negative cache: avoid hammering DB on persistent failure. Retry in 30s.
		s.proxyPoolUpdated = time.Now().Add(-proxyPoolCacheTTL + 30*time.Second)
		return s.proxyPool // fail-open: return stale data
	}

	s.proxyPool = proxies
	s.proxyPoolUpdated = time.Now()
	return s.proxyPool
}

// resolveMachineID returns the machine ID for a request.
// For Free-tier (non-apikey) accounts, randomly generates a new machine ID
// with freeTierMachineIDRotatePercent probability.
func (s *KiroGatewayService) resolveMachineID(account *Account, freeTier bool) string {
	if freeTier && !account.IsKiroApiKey() {
		if rand.IntN(100) < freeTierMachineIDRotatePercent {
			mid := kiro.GenerateRandomMachineID()
			log.Printf("[kiro-freeTier] account=%s using random machine_id=%s...%s",
				account.Name, mid[:8], mid[len(mid)-8:])
			return mid
		}
	}
	return kiro.GenerateMachineID(account.GetKiroRefreshToken())
}

// resolveProxyURL returns the proxy URL for a request.
// For Free-tier (non-apikey) accounts, randomly selects from the active proxy pool.
// Falls back to account-bound proxy if pool is empty.
func (s *KiroGatewayService) resolveProxyURL(ctx context.Context, account *Account, freeTier bool) string {
	// Default: account-bound proxy
	defaultProxy := ""
	if account.ProxyID != nil && account.Proxy != nil {
		defaultProxy = account.Proxy.URL()
	}

	if !freeTier || account.IsKiroApiKey() {
		return defaultProxy
	}

	pool := s.getActiveProxyPool(ctx)
	if len(pool) == 0 {
		return defaultProxy // fallback to account-bound proxy
	}

	chosen := pool[rand.IntN(len(pool))]
	chosenURL := chosen.URL()
	log.Printf("[kiro-freeTier] account=%s proxy_rotation: using proxy=%s (pool_size=%d)",
		account.Name, chosen.Name, len(pool))
	return chosenURL
}

// isKiroFreeTier 判断 Kiro 账号是否为 Free 订阅类型。
// apikey 账号不存在等级概念，直接返回 false。
// 只读 Extra 和缓存，不触发同步 HTTP 调用。cache miss 时异步触发 fetch，当前请求返回 false（fail-open）。
func (s *KiroGatewayService) isKiroFreeTier(account *Account) bool {
	if account.IsKiroApiKey() {
		return false
	}

	// 1. 从 account.Extra 读取（持久化，零开销）
	subType := account.GetExtraString("kiro_subscription_type")

	// 2. 从缓存读取
	if subType == "" && s.usageCache != nil {
		subType = s.usageCache.GetKiroSubscriptionType(account.ID)
	}

	// 3. cache miss → 异步触发 fetch（不阻塞当前请求）
	if subType == "" {
		s.triggerAsyncSubscriptionFetch(account)
		return false
	}

	return strings.Contains(strings.ToUpper(subType), "FREE")
}

// triggerAsyncSubscriptionFetch 异步拉取订阅类型，用 singleflight 合并同一账号的并发请求。
// 结果写入 usageCache 和数据库（通过独立 account 副本，不 mutate 共享指针）。
func (s *KiroGatewayService) triggerAsyncSubscriptionFetch(account *Account) {
	if s.tokenProvider == nil {
		return
	}
	accountID := account.ID
	accountName := account.Name
	sfKey := fmt.Sprintf("kiro_sub_%d", accountID)

	// Capture proxy URL before goroutine to avoid reading shared account fields async
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	go func() {
		_, _, _ = s.freeTierSF.Do(sfKey, func() (any, error) {
			// Double-check cache inside singleflight to avoid redundant fetches
			if s.usageCache != nil {
				if cached := s.usageCache.GetKiroSubscriptionType(accountID); cached != "" {
					return cached, nil
				}
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			accessToken, err := s.tokenProvider.GetAccessToken(ctx, account)
			if err != nil || accessToken == "" {
				return nil, fmt.Errorf("get access token: %w", err)
			}

			fetcher := kiro.NewUsageLimitsFetcher(nil)
			usageProfileArn, err := resolveKiroProfileArn(ctx, account, s.tokenProvider, s.accountRepo, "[kiro-freeTier]")
			if err != nil {
				log.Printf("[kiro-freeTier] profile_arn missing for account %s: %v", accountName, err)
				return nil, err
			}
			limits, err := fetcher.FetchUsageLimits(ctx, accessToken, "us-east-1", proxyURL, "", usageProfileArn)
			if err != nil {
				log.Printf("[kiro-freeTier] async fetch failed for account %s: %v", accountName, err)
				return nil, err
			}

			creditsInfo := kiro.ExtractCreditsInfo(limits)
			if creditsInfo == nil || creditsInfo.SubscriptionType == "" {
				return nil, fmt.Errorf("no subscription type in response")
			}

			subType := creditsInfo.SubscriptionType

			// Write to cache only (not shared account.Extra)
			if s.usageCache != nil {
				s.usageCache.StoreKiroCredits(accountID, &KiroCreditsInfo{
					AvailableCredits: creditsInfo.AvailableCredits,
					UsedCredits:      creditsInfo.UsedCredits,
					TotalCredits:     creditsInfo.TotalCredits,
					DaysUntilReset:   creditsInfo.DaysUntilReset,
					NextResetAt:      creditsInfo.NextResetAt,
					UserEmail:        creditsInfo.UserEmail,
					SubscriptionType: subType,
				})
			}

			// Persist to DB via atomic JSONB merge (avoid read-modify-write race)
			persistCtx, persistCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer persistCancel()
			if err := s.accountRepo.UpdateExtra(persistCtx, accountID, map[string]any{
				"kiro_subscription_type": subType,
			}); err != nil {
				log.Printf("[kiro-freeTier] failed to persist subscription type for account %s: %v", accountName, err)
			}

			return subType, nil
		})
	}()
}

// remapModelForFreeTier 对 Free 订阅类型的账号，将不支持的模型自动降级到 Sonnet 4.5
// Free 账号允许 Kiro 官方标记为 Free 可用的模型，其余模型一律重定向到 Sonnet 4.5
// 返回 (映射后的模型名, 是否发生了映射)
func (s *KiroGatewayService) remapModelForFreeTier(account *Account, model string, freeTier bool) (string, bool) {
	if !freeTier {
		return model, false
	}
	if kiro.IsFreeTierModelAllowed(model) {
		return model, false
	}
	// 其余所有模型（opus、sonnet-4-6、sonnet-3.x、未知模型等）→ Sonnet 4.5
	return "claude-sonnet-4-5", true
}

// IsModelSupported checks if the model is supported by Kiro
// Kiro supports Claude and Kiro-native models through the OAuth runtime.
func (s *KiroGatewayService) IsModelSupported(requestedModel string) bool {
	return kiro.IsOAuthModelSupported(requestedModel)
}

// Forward handles Claude API requests and forwards to CodeWhisperer
func (s *KiroGatewayService) Forward(ctx context.Context, c *gin.Context, account *Account, body []byte) (*ForwardResult, error) {
	startTime := time.Now()
	prefix := fmt.Sprintf("[kiro-Forward] account=%s", account.Name)

	// Parse Claude request
	claudeReq, err := kiro.ParseClaudeRequestFromJSON(body)
	if err != nil {
		return nil, fmt.Errorf("parse claude request: %w", err)
	}
	if strings.TrimSpace(claudeReq.Model) == "" {
		return nil, fmt.Errorf("missing model")
	}

	// Clean orphan tool_uses that have no matching tool_result.
	// Claude API requires every tool_use to have a corresponding tool_result in the next user message.
	// Clients may send broken pairs (e.g., interrupted tool execution, client-side truncation).
	if kiro.CleanOrphanToolUsesInClaudeMessages(claudeReq.Messages) {
		log.Printf("%s cleaned orphan tool_use blocks from request", prefix)
		// Re-serialize body for apikey passthrough path
		if newBody, err := json.Marshal(claudeReq); err == nil {
			body = newBody
		}
	}

	// Deduplicate tool_use IDs (Claude API requires globally unique IDs)
	if kiro.DeduplicateToolUseIDsInClaudeMessages(claudeReq.Messages) {
		log.Printf("%s deduplicated tool_use ids in request", prefix)
		if newBody, err := json.Marshal(claudeReq); err == nil {
			body = newBody
		}
	}

	if !account.IsKiroApiKey() && kiro.ApplyThinkingDefaultsFromModelName(claudeReq) {
		log.Printf("%s enabled thinking mode from model alias: %s", prefix, claudeReq.Model)
		if newBody, err := json.Marshal(claudeReq); err == nil {
			body = newBody
		}
	}

	freeTier := s.isKiroFreeTier(account)

	// Free 订阅类型账号不支持 Opus，自动降级为 Sonnet 4.5
	if remapped, ok := s.remapModelForFreeTier(account, claudeReq.Model, freeTier); ok {
		log.Printf("%s free_tier_model_remap: %s -> %s", prefix, claudeReq.Model, remapped)
		claudeReq.Model = remapped
		// 同步更新 body 中的 model 字段
		if newBody, err := json.Marshal(claudeReq); err == nil {
			body = newBody
		}
	}

	originalModel := claudeReq.Model
	isAPIKeyAccount := account.IsKiroApiKey()
	if isAPIKeyAccount {
		mappedModel := account.GetMappedModel(originalModel)
		if mappedModel != originalModel {
			rewrittenBody, rewriteErr := rewriteTopLevelModelJSON(body, mappedModel)
			if rewriteErr != nil {
				return nil, rewriteErr
			}
			body = rewrittenBody
			claudeReq.Model = mappedModel
			log.Printf("%s apikey_model_mapping: %s -> %s", prefix, originalModel, mappedModel)
		}
	}
	activeUpstreamModel := ""
	mappedModel := ""
	contextWindowLimit := 0
	if !isAPIKeyAccount {
		if injectKiroOAuthIdentitySystemPrompt(account, claudeReq) {
			log.Printf("%s injected Kiro OAuth identity system prompt", prefix)
		}
		activeUpstreamModel = s.resolveKiroUpstreamModel(account, claudeReq.Model)
		mappedModel = kiro.GetModelID(activeUpstreamModel)
		contextWindowLimit = kiro.GetContextWindowLimit(claudeReq.Model)

		// AWSQ thinking mode shares the output token budget between thinking and text.
		claudeReq.MaxTokens = kiro.KiroFixedMaxTokens
	}

	estimatedTokens := 0
	if !isAPIKeyAccount {
		estimatedTokens = kiro.EstimateInputTokens(claudeReq)
	}

	cacheEstimation := kiro.CacheEstimation{}
	var cacheResult kiro.CacheResult

	// Get access token
	if s.tokenProvider == nil {
		return nil, errors.New("kiro token provider not configured")
	}
	accessToken, err := s.tokenProvider.GetAccessToken(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("get access_token failed: %w", err)
	}

	// Proxy URL (Free-tier: random from pool; others: account-bound)
	proxyURL := s.resolveProxyURL(ctx, account, freeTier)

	// apikey accounts: direct Claude API passthrough (no CodeWhisperer transform)
	if isAPIKeyAccount {
		// Override X-Session-ID with content-based fingerprint for apikey accounts.
		// This ensures CacheTracker and any downstream logic that reads X-Session-ID
		// uses a stable, content-based identifier instead of whatever the client sent.
		callerKey := kiro.ExtractAPIKey(c)
		if contentSessionID := kiro.GenerateContentBasedConversationID(callerKey, claudeReq.Model, claudeReq.Messages); contentSessionID != "" {
			c.Request.Header.Set("X-Session-ID", contentSessionID)
		}

		// apikey 渠道：不注入 cache_control，避免上游通过注入模式识别代理行为。
		// 仅剥离不支持的字段，保留用户自带的 cache_control 原样透传。
		body = stripUnsupportedClaudeFields(body)
		body = enforceCacheControlLimit(body)
		// 确保 metadata.user_id 格式合规（缺失或格式不对时基于 apiKey 生成确定性值）
		body = claude.EnsureMetadataUserID(body, accessToken)
		return s.forwardClaudeAPIRequest(ctx, c, account, claudeReq, body, accessToken, proxyURL, originalModel, startTime, false)
	}

	// Resolve URL-based images to base64 (CW only supports base64).
	// This handles both data: URLs and HTTP/HTTPS URLs.
	if kiro.ResolveURLImagesInRequest(claudeReq) {
		log.Printf("%s URL images resolved to base64", prefix)
	}

	// Compress oversized base64 images before CW transformation.
	// CW has a hard body size limit of ~810KB; uncompressed PNG screenshots
	// can easily exceed this. Compress before transform so that both the
	// token estimation and serialized body reflect actual sizes.
	// Skip for 1M context models (4.6 series) which have 4MB body limit.
	if !kiro.Is1MContext(originalModel) {
		if kiro.CompressImagesInRequest(claudeReq) {
			log.Printf("%s images compressed for body size reduction", prefix)
		}
	}
	if !isAPIKeyAccount {
		cacheEstimation = kiro.EstimateCache(claudeReq)
		if cacheEstimation.MeetsCacheThreshold {
			cacheResult = s.beginKiroOAuthCache(c, account, activeUpstreamModel, claudeReq, cacheEstimation)
			log.Printf("%s cache_estimation: cacheable=%d non_cacheable=%d stable=%d history=%d meets_threshold=%v cache_hit=%v prev_tokens=%d",
				prefix, cacheEstimation.CacheableTokens, cacheEstimation.NonCacheableTokens,
				cacheEstimation.StableTokens, cacheEstimation.HistoryTokens,
				cacheEstimation.MeetsCacheThreshold, cacheResult.Hit, cacheResult.PrevTokens)
		}
	}

	// Get profile ARN.
	// Priority: in-memory cache (synchronous with token refresh) > account snapshot > database.
	// The in-memory cache eliminates the race condition where async DB write from
	// token refresh hasn't completed yet when this request reads profileArn.
	profileArn, err := resolveKiroProfileArn(ctx, account, s.tokenProvider, s.accountRepo, prefix)
	if err != nil {
		return nil, err
	}

	// Transform Claude request to CodeWhisperer format
	cwReq, reqBody, err := s.prepareCodeWhispererPayload(claudeReq, profileArn, c, activeUpstreamModel)
	if err != nil {
		return nil, fmt.Errorf("prepare request: %w", err)
	}

	log.Printf("%s request_size=%d model=%s mapped_model=%s", prefix, len(reqBody), originalModel, mappedModel)
	if activeUpstreamModel != originalModel {
		log.Printf("%s dynamic_model_fallback: %s -> %s", prefix, originalModel, activeUpstreamModel)
	}

	// Build endpoint list (primary + fallback)
	endpoints := getKiroEndpoints(account, s.cfg)

	// Generate machine ID for User-Agent headers (Free-tier: may rotate randomly)
	machineID := s.resolveMachineID(account, freeTier)
	kiroVersion := "0.11.107"

	// Endpoint loop: try each endpoint, with retries per endpoint
	var resp *http.Response
	var respDeadline *kiroInitialDeadline
	var lastErr error
	tokenRefreshed := false // shared across endpoints to avoid redundant refresh

	// proxySuspect 表示上一次尝试是**传输层**失败，当前代理有嫌疑，下次尝试换一个。
	// 只在传输失败时置位：上游返回 5xx 说明请求已经到达上游，代理是无辜的，
	// 此时换代理会白白改掉 client 缓存键、丢掉已建好的连接池。
	proxySuspect := false

	{
		resp = nil
		lastErr = nil
		for epIdx, ep := range endpoints {
			attempt := 1
			for attempt <= kiroMaxRetries {
				select {
				case <-ctx.Done():
					log.Printf("%s status=context_canceled error=%v", prefix, ctx.Err())
					return nil, ctx.Err()
				default:
				}

				// Apply request jitter before the upstream call. Start the initial-response
				// deadline after jitter so queue smoothing does not consume the Kiro budget.
				s.applyRequestJitter(ctx)

				// 传输层失败后重新解析代理：之前代理只在循环外解析一次，
				// 一旦选中的代理不可用，三次重试会全部走同一个坏代理然后整体失败。
				// Free-tier 账号会从池里随机换一个；固定代理账号解析结果不变（无副作用）。
				// 注意用 = 而非 :=，避免 shadow 掉外层 proxyURL。
				if proxySuspect {
					proxySuspect = false
					if rotated := s.resolveProxyURL(ctx, account, freeTier); rotated != proxyURL {
						log.Printf("%s endpoint=%s proxy_rotated attempt=%d", prefix, ep.Name, attempt)
						proxyURL = rotated
					}
				}

				reqCtx, deadline := s.startKiroInitialDeadline(ctx, account)
				upstreamReq, err := http.NewRequestWithContext(reqCtx, "POST", ep.URL, bytes.NewReader(reqBody))
				if err != nil {
					deadline.close()
					return nil, fmt.Errorf("create request: %w", err)
				}

				// Set headers
				upstreamReq.Header.Set("Content-Type", "application/json")
				upstreamReq.Header.Set("Authorization", "Bearer "+accessToken)
				upstreamReq.Header.Set("Accept", "text/event-stream")
				upstreamReq.Header.Set("User-Agent", fmt.Sprintf("aws-sdk-js/1.0.27 ua/2.1 os/linux lang/js md/nodejs#22.12.0 api/codewhispererstreaming#1.0.27 m/E KiroIDE-%s-%s", kiroVersion, machineID))
				upstreamReq.Header.Set("x-amz-user-agent", fmt.Sprintf("aws-sdk-js/1.0.27 KiroIDE-%s-%s", kiroVersion, machineID))
				upstreamReq.Header.Set("x-amzn-kiro-agent-mode", "vibe")
				upstreamReq.Header.Set("x-amzn-codewhisperer-optout", "true")
				upstreamReq.Header.Set("Host", ep.Host)
				s.applyKiroConnectionHeader(upstreamReq)
				upstreamReq.Header.Set("amz-sdk-invocation-id", uuid.New().String())
				upstreamReq.Header.Set("amz-sdk-request", "attempt=1; max=3")
				if ep.AmzTarget != "" {
					upstreamReq.Header.Set("X-Amz-Target", ep.AmzTarget)
				}

				resp, err = s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
				deadline.stopHeaderTimer()
				if err != nil {
					if deadline.isTimeoutError(err) {
						deadline.close()
						log.Printf("%s endpoint=%s status=initial_response_timeout phase=response_headers timeout=%s", prefix, ep.Name, deadline.timeout)
						appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
							Platform:    account.Platform,
							AccountID:   account.ID,
							AccountName: account.Name,
							Kind:        "initial_response_timeout",
							Message:     kiroInitialResponseTimeoutMessage("response_headers", deadline.timeout),
						})
						return nil, kiroInitialResponseFailover("response_headers", deadline.timeout)
					}
					deadline.close()
					safeErr := sanitizeKiroClientErrorMessage(err.Error())
					appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
						Platform:           account.Platform,
						AccountID:          account.ID,
						AccountName:        account.Name,
						UpstreamStatusCode: 0,
						Kind:               "transport_error",
						Message:            safeErr,
						// 明确标注请求可能未到达上游，避免再和上游互相甩锅
						Detail: fmt.Sprintf("class=%s endpoint=%s attempt=%d note=request_may_not_have_reached_upstream",
							kiroFailureTransport, ep.Name, attempt),
					})
					// 传输层失败：当前代理有嫌疑，下一次尝试换一个
					proxySuspect = true

					if attempt < kiroMaxRetries {
						log.Printf("%s endpoint=%s status=request_failed retry=%d/%d error=%v", prefix, ep.Name, attempt, kiroMaxRetries, err)
						if !sleepKiroBackoffWithContext(ctx, attempt) {
							return nil, ctx.Err()
						}
						attempt++
						continue
					}
					log.Printf("%s endpoint=%s status=request_failed retries_exhausted error=%v", prefix, ep.Name, err)
					// B 类：传输层失败，请求可能根本没到上游（上游侧查不到记录）。
					// 必须包成 UpstreamFailoverError，否则 handler 的 errors.As 匹配不上，
					// 会直接 return 而不写任何响应体，客户端拿到空的 200。
					transportFailure := newKiroTransportFailure(
						"kiro_transport_error",
						http.StatusBadGateway,
						fmt.Sprintf("endpoint %s: %v", ep.Name, err),
					)
					lastErr = transportFailure.failoverError()
					break // try next endpoint
				}

				// Handle 429 rate limit — try next endpoint
				if resp.StatusCode == http.StatusTooManyRequests {
					respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
					_ = resp.Body.Close()
					deadline.close()

					upstreamMsg := extractKiroErrorMessage(respBody)
					upstreamMsg = sanitizeKiroClientErrorMessage(upstreamMsg)

					s.handleUpstreamError(ctx, prefix, account, resp.StatusCode, resp.Header, respBody)
					appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
						Platform:           account.Platform,
						AccountID:          account.ID,
						AccountName:        account.Name,
						UpstreamStatusCode: resp.StatusCode,
						UpstreamRequestID:  resp.Header.Get("x-amzn-requestid"),
						Kind:               "account_rate_limited",
						Message:            upstreamMsg,
					})
					log.Printf("%s endpoint=%s status=429 rate_limited, trying next endpoint", prefix, ep.Name)
					lastErr = &UpstreamFailoverError{StatusCode: resp.StatusCode}
					break // try next endpoint
				}

				// Handle 401: refresh token once, then retry
				if resp.StatusCode == http.StatusUnauthorized {
					respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
					_ = resp.Body.Close()
					deadline.close()

					upstreamMsg := extractKiroErrorMessage(respBody)

					if tokenRefreshed || s.tokenProvider == nil {
						log.Printf("%s endpoint=%s status=401 token_refresh_already_attempted, trying next endpoint msg=%s", prefix, ep.Name, upstreamMsg)
						lastErr = &UpstreamFailoverError{StatusCode: resp.StatusCode}
						break // try next endpoint
					}

					log.Printf("%s endpoint=%s status=401 token_expired, refreshing token... msg=%s", prefix, ep.Name, upstreamMsg)
					s.tokenProvider.InvalidateToken(account.ID)
					newToken, refreshErr := s.tokenProvider.GetAccessToken(ctx, account)
					if refreshErr != nil {
						log.Printf("%s endpoint=%s status=401 token_refresh_failed error=%v", prefix, ep.Name, refreshErr)
						lastErr = &UpstreamFailoverError{StatusCode: resp.StatusCode}
						break // try next endpoint
					}

					accessToken = newToken
					tokenRefreshed = true
					log.Printf("%s endpoint=%s status=401 token_refreshed, retrying", prefix, ep.Name)
					continue
				}

				// Handle 529 overloaded — try next endpoint
				if resp.StatusCode == 529 {
					respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
					_ = resp.Body.Close()
					deadline.close()
					s.handleUpstreamError(ctx, prefix, account, resp.StatusCode, resp.Header, respBody)
					log.Printf("%s endpoint=%s status=529 overloaded, trying next endpoint", prefix, ep.Name)
					lastErr = &UpstreamFailoverError{StatusCode: resp.StatusCode}
					break // try next endpoint
				}

				// Handle 400 bad request — return immediately (both endpoints share the same limits)
				if resp.StatusCode == http.StatusBadRequest {
					respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
					_ = resp.Body.Close()
					deadline.close()
					s.handleUpstreamError(ctx, prefix, account, resp.StatusCode, resp.Header, respBody)

					errorMsg := extractKiroErrorMessage(respBody)
					log.Printf("%s endpoint=%s status=400 bad_request body=%s", prefix, ep.Name, truncateForLog(respBody, 500))

					cwToolCount := 0
					if cwReq.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext != nil {
						cwToolCount = len(cwReq.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools)
					}
					log.Printf("%s endpoint=%s status=400 debug: request_body_size=%d model=%s messages=%d tools=%d history_entries=%d cw_tools=%d estimated_tokens=%d",
						prefix, ep.Name, len(reqBody), originalModel, len(claudeReq.Messages), len(claudeReq.Tools),
						len(cwReq.ConversationState.History), cwToolCount, estimatedTokens)

					if fallbackModel, ok := s.maybeFallbackUnsupportedKiroModel(account, originalModel, activeUpstreamModel, errorMsg); ok {
						log.Printf("%s endpoint=%s dynamic_model_fallback_retry: %s -> %s", prefix, ep.Name, activeUpstreamModel, fallbackModel)
						activeUpstreamModel = fallbackModel
						mappedModel = kiro.GetModelID(activeUpstreamModel)
						contextWindowLimit = kiro.GetContextWindowLimit(activeUpstreamModel)
						cwReq, reqBody, err = s.prepareCodeWhispererPayload(claudeReq, profileArn, c, activeUpstreamModel)
						if err != nil {
							return nil, fmt.Errorf("prepare fallback request: %w", err)
						}
						cacheResult = s.beginKiroOAuthCache(c, account, activeUpstreamModel, claudeReq, cacheEstimation)
						log.Printf("%s endpoint=%s request_rebuilt request_size=%d model=%s mapped_model=%s", prefix, ep.Name, len(reqBody), originalModel, mappedModel)
						continue
					}

					// Check if error is context/input size related
					// Use precise patterns to avoid false positives from unrelated 400 errors
					// (e.g., malformed request, unsupported content type in tool_result)
					errorMsgLower := strings.ToLower(errorMsg)
					if strings.Contains(errorMsgLower, "input is too long") ||
						strings.Contains(errorMsgLower, "input too long") ||
						strings.Contains(errorMsgLower, "context window") ||
						strings.Contains(errorMsgLower, "context length") ||
						strings.Contains(errorMsgLower, "too many tokens") ||
						strings.Contains(errorMsgLower, "token limit") ||
						strings.Contains(errorMsgLower, "content_length") ||
						strings.Contains(errorMsgLower, "exceeds the maximum number of tokens") ||
						strings.Contains(errorMsgLower, "maximum context") {
						log.Printf("%s status=context_error_detected error=%s", prefix, errorMsg)
						setOpsUpstreamError(c, resp.StatusCode, errorMsg, "")
						appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
							Platform:           account.Platform,
							AccountID:          account.ID,
							AccountName:        account.Name,
							UpstreamStatusCode: resp.StatusCode,
							UpstreamRequestID:  resp.Header.Get("x-amzn-requestid"),
							Kind:               "context_too_long",
							Message:            errorMsg,
						})
						return nil, &ContextTooLongError{
							EstimatedTokens: estimatedTokens,
							Limit:           contextWindowLimit,
						}
					}

					// 400 errors are usually deterministic — no point trying the other endpoint.
					// Exception: "profileArn is required" is endpoint-specific (AWSQ requires it,
					// CW may not). Failover to next endpoint for these cases.
					if strings.Contains(errorMsgLower, "profilearn is required") ||
						strings.Contains(errorMsgLower, "profilearn") {
						log.Printf("%s endpoint=%s status=400 profileArn_required, trying next endpoint", prefix, ep.Name)
						lastErr = &UpstreamFailoverError{StatusCode: resp.StatusCode}
						break // try next endpoint
					}

					return nil, s.writeMappedClaudeError(c, account, resp.StatusCode, resp.Header.Get("x-amzn-requestid"), respBody)
				}

				// Handle retryable errors (5xx)
				if resp.StatusCode >= 400 && s.shouldRetryUpstreamError(resp.StatusCode) {
					respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
					_ = resp.Body.Close()
					deadline.close()

					if attempt < kiroMaxRetries {
						upstreamMsg := extractKiroErrorMessage(respBody)
						upstreamMsg = sanitizeKiroClientErrorMessage(upstreamMsg)
						appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
							Platform:           account.Platform,
							AccountID:          account.ID,
							AccountName:        account.Name,
							UpstreamStatusCode: resp.StatusCode,
							UpstreamRequestID:  resp.Header.Get("x-amzn-requestid"),
							Kind:               "retry",
							Message:            upstreamMsg,
						})
						log.Printf("%s endpoint=%s status=%d retry=%d/%d body=%s", prefix, ep.Name, resp.StatusCode, attempt, kiroMaxRetries, truncateForLog(respBody, 500))
						if !sleepKiroBackoffWithContext(ctx, attempt) {
							return nil, ctx.Err()
						}
						attempt++
						continue
					}

					// Retries exhausted on this endpoint
					log.Printf("%s endpoint=%s status=%d retries_exhausted, trying next endpoint", prefix, ep.Name, resp.StatusCode)
					lastErr = &UpstreamFailoverError{StatusCode: resp.StatusCode}
					resp = &http.Response{
						StatusCode: resp.StatusCode,
						Header:     resp.Header.Clone(),
						Body:       io.NopCloser(bytes.NewReader(respBody)),
					}
					break // try next endpoint
				}

				// Success or non-retryable error — stop endpoint loop
				log.Printf("%s endpoint=%s status=%d", prefix, ep.Name, resp.StatusCode)
				respDeadline = attachKiroInitialDeadline(resp, deadline)
				goto endpointDone
			}

			// Reset resp before trying next endpoint to avoid using stale response
			resp = nil

			// Log endpoint switch
			if epIdx < len(endpoints)-1 {
				log.Printf("%s switching from endpoint %s to %s", prefix, ep.Name, endpoints[epIdx+1].Name)
			}
		}

		// All endpoints exhausted
		if resp == nil {
			if lastErr != nil {
				setOpsUpstreamError(c, 0, lastErr.Error(), "")
				return nil, lastErr
			}
			// 兜底也走 failover 语义，确保 handler 一定会写出响应
			return nil, newKiroTransportFailure(
				"kiro_all_endpoints_failed",
				http.StatusBadGateway,
				"all endpoints failed",
			).failoverError()
		}

	endpointDone:
		// 处理段裹进 IIFE：让 defer resp.Body.Close 在自然作用域内执行，
		// 避免响应处理提前返回时泄漏 resp.Body。
		result, err := func() (*ForwardResult, error) {
			defer func() {
				_ = resp.Body.Close()
				respDeadline.close()
			}()

			// Handle error response
			if resp.StatusCode >= 400 {
				respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
				requestID := resp.Header.Get("x-amzn-requestid")
				log.Printf("%s status=%d upstream_error request_id=%s body=%s", prefix, resp.StatusCode, requestID, truncateForLog(respBody, 1000))

				s.handleUpstreamError(ctx, prefix, account, resp.StatusCode, resp.Header, respBody)

				if s.shouldFailoverUpstreamError(resp.StatusCode) {
					upstreamMsg := extractKiroErrorMessage(respBody)
					upstreamMsg = sanitizeKiroClientErrorMessage(upstreamMsg)
					appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
						Platform:           account.Platform,
						AccountID:          account.ID,
						AccountName:        account.Name,
						UpstreamStatusCode: resp.StatusCode,
						UpstreamRequestID:  resp.Header.Get("x-amzn-requestid"),
						Kind:               "failover",
						Message:            upstreamMsg,
					})
					return nil, &UpstreamFailoverError{StatusCode: resp.StatusCode}
				}

				return nil, s.writeMappedClaudeError(c, account, resp.StatusCode, resp.Header.Get("x-amzn-requestid"), respBody)
			}

			requestID := resp.Header.Get("x-amzn-requestid")
			if requestID != "" {
				c.Header("x-request-id", requestID)
			}
			s.markKiroModelSupported(account, originalModel, activeUpstreamModel)

			// Estimate input tokens from the Claude request
			inputTokens := kiro.EstimateInputTokens(claudeReq)

			// Build tool name reverse map for restoring original names in response
			toolNameReverseMap := kiro.BuildReverseMapFromClaudeTools(claudeReq.Tools)

			var usage *ClaudeUsage
			var firstTokenMs *int
			var usageFromUpstream bool

			// Calculate cache tokens to pass to handlers (cache_read + cache_creation coexist)
			// CW path: cap to model-specific context window
			cacheReadTokens, cacheCreationTokens := cacheEstimation.SplitCacheTokens(cacheResult, contextWindowLimit)
			if !isAPIKeyAccount {
				cacheReadTokens, cacheCreationTokens = s.applyKiroSimulatedCacheReadRatio(cacheReadTokens, cacheCreationTokens)
			}
			thinkingEnabled := kiro.IsThinkingConfigEnabled(claudeReq)

			if claudeReq.Stream {
				// Streaming response
				streamRes, err := s.handleStreamingResponseWithOptions(
					c,
					resp,
					startTime,
					originalModel,
					inputTokens,
					toolNameReverseMap,
					cacheCreationTokens,
					cacheReadTokens,
					cacheEstimation.MeetsCacheThreshold,
					thinkingEnabled,
					respDeadline.streamOptions(),
				)
				if err != nil {
					if timeoutErr, ok := isKiroInitialResponseTimeout(err); ok {
						log.Printf("%s status=initial_response_timeout phase=%s timeout=%s", prefix, timeoutErr.Phase, timeoutErr.Timeout)
						appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
							Platform:    account.Platform,
							AccountID:   account.ID,
							AccountName: account.Name,
							Kind:        "initial_response_timeout",
							Message:     kiroInitialResponseTimeoutMessage(timeoutErr.Phase, timeoutErr.Timeout),
						})
						return nil, kiroInitialResponseFailover(timeoutErr.Phase, timeoutErr.Timeout)
					}
					log.Printf("%s status=stream_error error=%v", prefix, err)
					// A/B 类故障要落 ops（带 requestID + exception 类型），
					// 否则只能靠翻日志和上游对单。
					recordKiroFailureOps(c, account, err)
					return nil, err
				}
				// 流已提交后才失败：状态码改不了了，但要留下可查的 ops 记录
				if streamRes.failedAfterCommit {
					recordKiroPostCommitFailureOps(c, account, streamRes.inBandException, streamRes.upstreamRequestID)
				}
				usage = streamRes.usage
				firstTokenMs = streamRes.firstTokenMs
				usageFromUpstream = streamRes.usageFromUpstream
			} else {
				// Non-streaming response
				streamRes, err := s.handleNonStreamingResponseWithOptions(
					c,
					resp,
					startTime,
					originalModel,
					inputTokens,
					toolNameReverseMap,
					cacheCreationTokens,
					cacheReadTokens,
					cacheEstimation.MeetsCacheThreshold,
					thinkingEnabled,
					respDeadline.streamOptions(),
				)
				if err != nil {
					if timeoutErr, ok := isKiroInitialResponseTimeout(err); ok {
						log.Printf("%s status=initial_response_timeout phase=%s timeout=%s", prefix, timeoutErr.Phase, timeoutErr.Timeout)
						appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
							Platform:    account.Platform,
							AccountID:   account.ID,
							AccountName: account.Name,
							Kind:        "initial_response_timeout",
							Message:     kiroInitialResponseTimeoutMessage(timeoutErr.Phase, timeoutErr.Timeout),
						})
						return nil, kiroInitialResponseFailover(timeoutErr.Phase, timeoutErr.Timeout)
					}
					log.Printf("%s status=non_stream_error error=%v", prefix, err)
					recordKiroFailureOps(c, account, err)
					return nil, err
				}
				usage = streamRes.usage
				firstTokenMs = streamRes.firstTokenMs
				usageFromUpstream = streamRes.usageFromUpstream
			}

			// Apply cache token estimation only when upstream did not provide token usage.
			// Real Claude behavior: cache_read and cache_creation coexist.
			// cache_read = previously-cached prefix tokens, cache_creation = newly-added tokens.
			// input_tokens = total - cache_read - cache_creation (non-cached portion).
			if cacheEstimation.MeetsCacheThreshold && !usageFromUpstream {
				usage.CacheReadInputTokens = cacheReadTokens
				usage.CacheCreationInputTokens = cacheCreationTokens
				usage.InputTokens -= (cacheReadTokens + cacheCreationTokens)
				if usage.InputTokens < 0 {
					usage.InputTokens = 0
				}
			}

			// NOTE: cache_rate_adjustment 暂时注释，共存模型下不再需要挪 cache_read 到 input
			// if !usageFromUpstream && usage.CacheReadInputTokens > 0 {
			// 	totalInput := usage.InputTokens + usage.CacheReadInputTokens
			// 	if totalInput > 0 {
			// 		cacheHitRate := float64(usage.CacheReadInputTokens) * 100.0 / float64(totalInput)
			// 		if cacheHitRate >= 60.0 {
			// 			tier := int((cacheHitRate-60.0)/10.0) + 1
			// 			adjustPct := float64(tier) * 0.05
			// 			adjustTokens := int(float64(usage.CacheReadInputTokens) * adjustPct)
			// 			if adjustTokens > 0 {
			// 				usage.CacheReadInputTokens -= adjustTokens
			// 				usage.InputTokens += adjustTokens
			// 			}
			// 		}
			// 	}
			// }

			// CW 路径未注入 cache_control，透传真实 usage，不合并 cache tokens
			if activeUpstreamModel != originalModel {
				log.Printf("%s model_effective requested_model=%s effective_model=%s", prefix, originalModel, activeUpstreamModel)
			}

			if !isAPIKeyAccount {
				cacheResult.Commit()
			}
			return &ForwardResult{
				RequestID:    requestID,
				Usage:        *usage,
				Model:        activeUpstreamModel,
				Stream:       claudeReq.Stream,
				Duration:     time.Since(startTime),
				FirstTokenMs: firstTokenMs,
			}, nil
		}()

		if err != nil {
			return nil, err
		}
		return result, nil
	}
}

// kiroStreamResult holds streaming result data
type kiroStreamResult struct {
	usage             *ClaudeUsage
	firstTokenMs      *int
	usageFromUpstream bool

	// 流已提交后才发生的失败无法再改 HTTP 状态码，函数会返回
	// (result, nil)（视为"部分成功"）。这些字段把故障信息带回调用方，
	// 由有 account 的一层落 ops —— 否则 ThrottlingException 这类最常见的
	// 上游限流会完全不进 ops，只剩日志，无法和上游对单。
	inBandException   string
	upstreamRequestID string
	failedAfterCommit bool
}

func isRenderableKiroStreamEvent(event kiro.StreamEvent) bool {
	switch event.Type {
	case kiro.EventTextDelta, kiro.EventThinkingDelta:
		return event.Text != ""
	case kiro.EventToolUseInputDelta:
		return event.PartialJSON != ""
	case kiro.EventContentBlockStart:
		return event.BlockType.Kind == kiro.BlockToolUse
	default:
		return false
	}
}

func isRenderableKiroCompleteResponse(resp *kiro.CompleteResponse) bool {
	return resp != nil && (resp.Text != "" || resp.Thinking != "" || len(resp.ToolCalls) > 0)
}

type kiroStreamOptions struct {
	initialResponseTimeout time.Duration
}

// handleStreamingResponse handles streaming response from CodeWhisperer.
func (s *KiroGatewayService) handleStreamingResponse(c *gin.Context, resp *http.Response, startTime time.Time, originalModel string, inputTokens int, toolNameReverseMap map[string]string, cacheCreationTokens, cacheReadTokens int, cachingEnabled bool, thinkingEnabled bool) (*kiroStreamResult, error) {
	return s.handleStreamingResponseWithOptions(c, resp, startTime, originalModel, inputTokens, toolNameReverseMap, cacheCreationTokens, cacheReadTokens, cachingEnabled, thinkingEnabled, kiroStreamOptions{})
}

func (s *KiroGatewayService) handleStreamingResponseWithOptions(c *gin.Context, resp *http.Response, startTime time.Time, originalModel string, inputTokens int, toolNameReverseMap map[string]string, cacheCreationTokens, cacheReadTokens int, cachingEnabled bool, thinkingEnabled bool, opts kiroStreamOptions) (*kiroStreamResult, error) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}

	// Create message ID
	messageID := "msg_" + uuid.New().String()[:24]

	// Create AWS EventStream parser
	parser := kiro.NewAwsEventStreamParser(messageID, originalModel)
	parser.SetThinkingEnabled(thinkingEnabled)

	// Create stream event converter
	converter := kiro.NewStreamEventConverter(messageID, originalModel, inputTokens)
	// Set tool name reverse map for restoring original names
	if toolNameReverseMap != nil {
		converter.SetToolNameReverseMap(toolNameReverseMap)
	}
	// Set cache tokens for response
	if cacheCreationTokens > 0 || cacheReadTokens > 0 {
		converter.SetCacheTokens(cacheCreationTokens, cacheReadTokens)
	}

	webSearchEvents := GetWebSearchEvents(c)
	if len(webSearchEvents) > 0 {
		converter.SetContentBlockOffset(len(webSearchEvents) * 2)
	}

	// 上游 requestID 必须在所有分支都可用。之前只有 >=400 分支读它，
	// 导致"上游 200 + 空流/读错误"这类故障完全无法和上游对单。
	upstreamRequestID := resp.Header.Get("x-amzn-requestid")
	// 即使最终失败也把 requestID 透给客户端，便于用户报障时直接提供
	if upstreamRequestID != "" {
		c.Header("x-request-id", upstreamRequestID)
	}

	streamCommitted := false
	sawRenderableEvent := false
	streamFailed := false
	// inBandException 记录 AWS EventStream 里的 :exception-type。
	// 流已提交后无法再改 HTTP 状态码，但这个类型必须留下来进日志和 ops 记录。
	inBandException := ""

	writeFormattedClaudeEvents := func(events []kiro.ClaudeSSEEvent) error {
		for _, claudeEvent := range events {
			sseStr, err := kiro.FormatClaudeSSE(claudeEvent)
			if err != nil {
				continue
			}
			if _, err := c.Writer.Write([]byte(sseStr)); err != nil {
				return err
			}
		}
		return nil
	}

	commitStream := func() error {
		if streamCommitted {
			return nil
		}
		c.Header("Content-Type", "text/event-stream; charset=utf-8")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")
		c.Status(http.StatusOK)

		for _, event := range converter.BuildInitialEvents() {
			sseStr, err := kiro.FormatClaudeSSE(event)
			if err != nil {
				continue
			}
			if _, err := c.Writer.Write([]byte(sseStr)); err != nil {
				return err
			}
		}

		// Inject web search events (server_tool_use + web_search_tool_result) if present.
		blockIndex := 0
		for _, wsEvt := range webSearchEvents {
			wsSSEEvents := buildWebSearchSSEEvents(wsEvt, blockIndex)
			for _, evt := range wsSSEEvents {
				sseStr, err := kiro.FormatClaudeSSE(evt)
				if err != nil {
					continue
				}
				if _, err := c.Writer.Write([]byte(sseStr)); err != nil {
					return err
				}
			}
			blockIndex += 2 // each search produces 2 blocks (server_tool_use + web_search_tool_result)
		}

		streamCommitted = true
		flusher.Flush()
		return nil
	}

	// 有内容要写时才提交 SSE 信封（懒提交）。
	//
	// 之前是"上游响应头一到就立刻提交"，注释里把它记为一个权衡：牺牲提交后的
	// 账号 failover，换取抢在 Cloudflare 120s 窗口前给客户端发出第一个字节。
	// 但实际代价比记录的更大 —— 上游 HTTP 200 + in-band exception
	// （ThrottlingException 等）几乎总在首帧到达，而提交发生在读 body 之前，
	// 于是这类故障**永远**只能以 200 + SSE error 收场，既不能换账号重试，
	// 也让客户端把限流当成成功。
	//
	// 现在改为：真正有内容时提交；若上游迟迟不发首帧，则由下面的
	// commitTimer 在 kiroStreamCommitDeadline 到点时兜底提交，
	// 仍然远早于 120s 窗口。两头的好处都拿到了。
	writeClaudeEvents := func(events []kiro.ClaudeSSEEvent) error {
		if len(events) == 0 {
			return nil
		}
		if err := commitStream(); err != nil {
			return err
		}
		return writeFormattedClaudeEvents(events)
	}

	select {
	case <-c.Request.Context().Done():
		return nil, c.Request.Context().Err()
	default:
	}

	var firstTokenMs *int
	var credits float64
	var contextPct float64
	var tokenUsage *ClaudeUsage

	// Read and process stream using goroutine + channel to avoid blocking reads
	type streamChunk struct {
		data []byte
		err  error
	}
	chunkCh := make(chan streamChunk, 32)
	done := make(chan struct{})
	defer close(done)

	// Background goroutine reads from upstream; sends chunks to channel
	sendChunk := func(chunk streamChunk) bool {
		select {
		case chunkCh <- chunk:
			return true
		case <-done:
			return false
		}
	}
	go func() {
		defer close(chunkCh)
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				if !sendChunk(streamChunk{data: data}) {
					return
				}
			}
			if err != nil {
				_ = sendChunk(streamChunk{err: err})
				return
			}
		}
	}()

	// Stream interval timeout
	streamInterval := time.Duration(0)
	if s.settingService != nil && s.settingService.cfg != nil && s.settingService.cfg.Gateway.StreamDataIntervalTimeout > 0 {
		streamInterval = time.Duration(s.settingService.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
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

	// Keepalive: send SSE comment to prevent proxy idle disconnect
	keepaliveInterval := s.kiroStreamKeepaliveInterval()
	keepaliveTicker := time.NewTicker(keepaliveInterval)
	defer keepaliveTicker.Stop()

	// 信封提交上限：上游迟迟不发首帧时兜底提交，保住连接
	commitDeadline := kiroStreamCommitDeadline
	if s.streamCommitDeadline > 0 {
		commitDeadline = s.streamCommitDeadline
	}
	commitTimer := time.NewTimer(commitDeadline)
	defer commitTimer.Stop()

	// Track last data arrival for timeout and keepalive decisions
	lastDataAt := time.Now()

	// Error event tracking
	errorEventSent := false
	sendErrorEvent := func(reason string) {
		streamFailed = true
		if errorEventSent {
			return
		}
		if !streamCommitted {
			return
		}
		errorEventSent = true
		errEvent := kiro.BuildClaudeError("overloaded_error", reason)
		if sseStr, err := kiro.FormatClaudeSSE(errEvent); err == nil {
			_, _ = c.Writer.Write([]byte(sseStr))
			flusher.Flush()
		}
	}

	for {
		select {
		case <-c.Request.Context().Done():
			log.Printf("Stream context cancelled (kiro): %v", c.Request.Context().Err())
			if !streamCommitted {
				return nil, c.Request.Context().Err()
			}
			streamFailed = true
			goto finishStream

		case <-commitTimer.C:
			// 上游迟迟不发首帧：到点先提交信封保住连接，放弃 failover 机会。
			// 这是懒提交的安全阀，仍远早于 Cloudflare 的 120s 窗口。
			if !streamCommitted {
				log.Printf("Kiro stream commit deadline reached before first event: request_id=%s timeout=%s",
					upstreamRequestID, commitDeadline)
				if err := commitStream(); err != nil {
					return nil, err
				}
			}

		case chunk, ok := <-chunkCh:
			if !ok {
				// Channel closed — upstream done
				goto finishStream
			}
			if chunk.err != nil {
				if chunk.err == io.EOF {
					if parser.ParseErrorCount() > 0 {
						log.Printf("Stream EOF after Kiro parse errors: duration=%v parse_errors=%d",
							time.Since(startTime), parser.ParseErrorCount())
					}
					goto finishStream
				}
				log.Printf("Stream read error (kiro): request_id=%s error=%v", upstreamRequestID, chunk.err)
				if !streamCommitted {
					// 读流中断：上游已回 200 并开始传输，属于 B 类传输失败，
					// 但 requestID 已经拿到了，带上以便对单。
					failure := newKiroTransportFailure("kiro_stream_read_error", http.StatusBadGateway, chunk.err.Error())
					failure.RequestID = upstreamRequestID
					return nil, failure.failoverError()
				}
				sendErrorEvent("stream_read_error")
				goto finishStream
			}

			lastDataAt = time.Now()

			// Process chunk through parser
			events := parser.Process(chunk.data)
			for _, event := range events {
				if isRenderableKiroStreamEvent(event) {
					sawRenderableEvent = true
				}
				// Track first token time
				if firstTokenMs == nil && (event.Type == kiro.EventTextDelta || event.Type == kiro.EventThinkingDelta || event.Type == kiro.EventToolUseInputDelta) {
					ms := int(time.Since(startTime).Milliseconds())
					firstTokenMs = &ms
				}

				// Track usage
				if event.Type == kiro.EventBackendUsage {
					if event.Credits > 0 {
						credits = event.Credits
					}
					if event.ContextPercentage > 0 {
						contextPct = event.ContextPercentage
					}
					if event.HasTokenUsage {
						tokenUsage = &ClaudeUsage{
							InputTokens:              event.InputTokens,
							OutputTokens:             event.OutputTokens,
							CacheCreationInputTokens: event.CacheCreationInputTokens,
							CacheReadInputTokens:     event.CacheReadInputTokens,
							ContextUsagePercent:      event.ContextPercentage,
						}
					}
				}

				if event.Type == kiro.EventError {
					msg := sanitizeKiroClientErrorMessage(event.ErrorMessage)
					if msg == "" {
						msg = "kiro_stream_error"
					}
					// 保留 AWS 的 exception 类型：这是 A 类故障（上游 HTTP 200 +
					// in-band exception），上游侧记录的是 200，只有 exception 类型
					// 加 requestID 才能对单。
					inBandException = event.ErrorType
					log.Printf("Kiro in-band exception: type=%s request_id=%s committed=%v retryable=%v message=%s",
						event.ErrorType, upstreamRequestID, streamCommitted,
						isRetryableKiroException(event.ErrorType), msg)
					// 只有"可重试"的 exception（限流/过载）才在提交前 failover：
					// 换账号有机会成功，且客户端应看到 429/529 而不是 502。
					// 确定性错误（订阅不支持、参数非法）换账号只会撞同一面墙，
					// 应把上游那句可操作的原因原样透传给客户端。
					if !streamCommitted && isRetryableKiroException(event.ErrorType) {
						return nil, newKiroInBandFailure(event.ErrorType, msg, upstreamRequestID).failoverError()
					}
					// 确定性错误：提交信封后以 SSE error 事件透传上游原因
					if !streamCommitted {
						if err := commitStream(); err != nil {
							return nil, err
						}
					}
					sendErrorEvent(msg)
					goto finishStream
				}

				// Convert to Claude SSE events
				claudeEvents := converter.ConvertEvent(event)
				if err := writeClaudeEvents(claudeEvents); err != nil {
					if streamCommitted {
						sendErrorEvent("write_failed")
					}
					goto finishStream
				}
			}
			if streamCommitted {
				flusher.Flush()
			}

		case <-intervalCh:
			if time.Since(lastDataAt) < streamInterval {
				continue
			}
			log.Printf("Stream data interval timeout (kiro): no data for %v request_id=%s", streamInterval, upstreamRequestID)
			if !streamCommitted {
				// 上游已回 200 但迟迟不发数据：按超时归到 B 类，状态码用 504
				failure := newKiroTransportFailure("kiro_stream_timeout_before_first_event", http.StatusGatewayTimeout, "")
				failure.RequestID = upstreamRequestID
				return nil, failure.failoverError()
			}
			sendErrorEvent("stream_timeout")
			goto finishStream

		case <-keepaliveTicker.C:
			if !streamCommitted {
				continue
			}
			if time.Since(lastDataAt) < keepaliveInterval {
				continue
			}
			// Send SSE comment as keepalive to prevent proxy idle disconnect
			if _, err := c.Writer.Write([]byte(":\n\n")); err != nil {
				goto finishStream
			}
			flusher.Flush()
		}
	}

finishStream:
	// Finish parsing and get remaining events
	var finalParserEvents []kiro.StreamEvent
	if !streamFailed {
		finalParserEvents = parser.Finish()
	}
	for _, event := range finalParserEvents {
		if event.Type == kiro.EventError {
			msg := sanitizeKiroClientErrorMessage(event.ErrorMessage)
			if msg == "" {
				msg = "kiro_stream_error"
			}
			if inBandException == "" {
				inBandException = event.ErrorType
			}
			log.Printf("Kiro in-band exception (finish): type=%s request_id=%s committed=%v message=%s",
				event.ErrorType, upstreamRequestID, streamCommitted, msg)
			if !streamCommitted && isRetryableKiroException(event.ErrorType) {
				return nil, newKiroInBandFailure(event.ErrorType, msg, upstreamRequestID).failoverError()
			}
			if !streamCommitted {
				if err := commitStream(); err != nil {
					return nil, err
				}
			}
			sendErrorEvent(msg)
			break
		}
		if isRenderableKiroStreamEvent(event) {
			sawRenderableEvent = true
		}
		if firstTokenMs == nil && (event.Type == kiro.EventTextDelta || event.Type == kiro.EventThinkingDelta || event.Type == kiro.EventToolUseInputDelta) {
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}
		claudeEvents := converter.ConvertEvent(event)
		if err := writeClaudeEvents(claudeEvents); err != nil {
			if streamCommitted {
				sendErrorEvent("write_failed")
			}
			break
		}
	}
	if streamFailed {
		usage := tokenUsage
		if usage == nil {
			usage = &ClaudeUsage{
				InputTokens:         inputTokens,
				OutputTokens:        converter.TotalOutputTokens(),
				ContextUsagePercent: contextPct,
			}
		}
		return &kiroStreamResult{
			usage:             usage,
			firstTokenMs:      firstTokenMs,
			usageFromUpstream: tokenUsage != nil,
			inBandException:   inBandException,
			upstreamRequestID: upstreamRequestID,
			failedAfterCommit: true,
		}, nil
	}

	if !sawRenderableEvent {
		log.Printf("Kiro upstream returned empty stream: duration=%v parse_errors=%d message_stopped=%v stream_committed=%v request_id=%s exception=%s",
			time.Since(startTime), parser.ParseErrorCount(), parser.MessageStopped(), streamCommitted, upstreamRequestID, inBandException)
		if streamCommitted {
			sendErrorEvent("kiro_empty_stream")
			return &kiroStreamResult{
				usage:             &ClaudeUsage{},
				inBandException:   inBandException,
				upstreamRequestID: upstreamRequestID,
				failedAfterCommit: true,
			}, nil
		}
		// 上游 HTTP 200 但零可渲染内容 —— A 类。上游侧看到的是 200，
		// 所以必须带 requestID，且不能报 502（会让上游查无此单）。
		failure := newKiroEmptyFailure("kiro_empty_stream", upstreamRequestID)
		if inBandException != "" {
			failure = newKiroInBandFailure(inBandException, "", upstreamRequestID)
			failure.Reason = "kiro_empty_stream"
		}
		return nil, failure.failoverError()
	}

	// Set context percentage before building final events
	// This allows BuildFinalEvents to calculate accurate input_tokens
	if contextPct > 0 {
		converter.SetContextPercentage(contextPct)
	}

	// When upstream provides real token usage, override converter's local estimates
	// so that SSE output to downstream matches the values used for billing.
	if tokenUsage != nil {
		converter.SetUpstreamUsage(
			tokenUsage.InputTokens,
			tokenUsage.OutputTokens,
			tokenUsage.CacheCreationInputTokens,
			tokenUsage.CacheReadInputTokens,
		)
	}

	// Send final events
	finalEvents := converter.BuildFinalEvents()
	if err := writeClaudeEvents(finalEvents); err != nil {
		if streamCommitted {
			sendErrorEvent("write_failed")
		}
	}
	if streamCommitted {
		flusher.Flush()
	}

	// Calculate input tokens from context percentage if available
	// Note: Kiro's contextPct may only reflect current turn, not cumulative context size
	// To ensure client can correctly judge context size and trigger proactive compression,
	// use the larger value between estimated and calculated tokens
	// However, when caching is enabled, contextPct can be inflated, so we only use it
	// when caching is NOT active to avoid triggering compression too early
	accurateInputTokens := inputTokens
	if contextPct > 0 && !cachingEnabled {
		calculatedTokens := int(contextPct / 100.0 * float64(kiro.GetContextWindowLimit(originalModel)))
		if calculatedTokens > accurateInputTokens {
			accurateInputTokens = calculatedTokens
		}
	}

	usageFromUpstream := tokenUsage != nil
	var usage *ClaudeUsage
	if usageFromUpstream {
		usage = tokenUsage
		// Keep conservative fallback when upstream omits output token count.
		if usage.OutputTokens <= 0 {
			usage.OutputTokens = converter.TotalOutputTokens()
		}
		if usage.ContextUsagePercent <= 0 && contextPct > 0 {
			usage.ContextUsagePercent = contextPct
		}
	} else {
		// Build usage from local estimation fallback.
		usage = &ClaudeUsage{
			InputTokens:         accurateInputTokens,
			OutputTokens:        converter.TotalOutputTokens(),
			ContextUsagePercent: contextPct,
		}
	}

	// Log credits/context usage if available
	if credits > 0 || contextPct > 0 {
		log.Printf("[kiro-Forward] credits=%.4f context_pct=%.2f input_tokens=%d", credits, contextPct, accurateInputTokens)
	}

	return &kiroStreamResult{
		usage:             usage,
		firstTokenMs:      firstTokenMs,
		usageFromUpstream: usageFromUpstream,
	}, nil
}

// handleNonStreamingResponse handles non-streaming response from CodeWhisperer
func (s *KiroGatewayService) handleNonStreamingResponse(c *gin.Context, resp *http.Response, startTime time.Time, originalModel string, inputTokens int, toolNameReverseMap map[string]string, cacheCreationTokens, cacheReadTokens int, cachingEnabled bool, thinkingEnabled bool) (*kiroStreamResult, error) {
	return s.handleNonStreamingResponseWithOptions(c, resp, startTime, originalModel, inputTokens, toolNameReverseMap, cacheCreationTokens, cacheReadTokens, cachingEnabled, thinkingEnabled, kiroStreamOptions{})
}

func (s *KiroGatewayService) handleNonStreamingResponseWithOptions(c *gin.Context, resp *http.Response, startTime time.Time, originalModel string, inputTokens int, toolNameReverseMap map[string]string, cacheCreationTokens, cacheReadTokens int, cachingEnabled bool, thinkingEnabled bool, opts kiroStreamOptions) (*kiroStreamResult, error) {
	// Read entire response
	respBody, err := readKiroNonStreamingBodyWithTimeout(c, resp, opts.initialResponseTimeout)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var firstTokenMs *int
	ms := int(time.Since(startTime).Milliseconds())
	firstTokenMs = &ms

	// 与流式路径一致：非流式分支同样要带上 requestID 才能和上游对单
	upstreamRequestID := resp.Header.Get("x-amzn-requestid")
	if upstreamRequestID != "" {
		c.Header("x-request-id", upstreamRequestID)
	}

	// Parse complete response with tool name restoration
	messageID := "msg_" + uuid.New().String()[:24]
	parsedResp, err := kiro.ParseCompleteResponseWithNameRestoreAndThinkingStrict(respBody, toolNameReverseMap, thinkingEnabled)
	if err != nil {
		log.Printf("Kiro upstream returned invalid non-stream response: duration=%v request_id=%s error=%v",
			time.Since(startTime), upstreamRequestID, err)
		// 上游 200 但响应体无法解析 —— A 类
		failure := newKiroEmptyFailure("kiro_invalid_response", upstreamRequestID)
		failure.Message = sanitizeKiroClientErrorMessage(err.Error())
		return nil, failure.failoverError()
	}
	if !isRenderableKiroCompleteResponse(parsedResp) {
		log.Printf("Kiro upstream returned empty non-stream response before downstream commit: duration=%v body_size=%d request_id=%s",
			time.Since(startTime), len(respBody), upstreamRequestID)
		return nil, newKiroEmptyFailure("kiro_empty_response", upstreamRequestID).failoverError()
	}

	// Calculate input tokens from context percentage if available
	// Note: Kiro's contextPct may only reflect current turn, not cumulative context size
	// To ensure client can correctly judge context size and trigger proactive compression,
	// use the larger value between estimated and calculated tokens
	// However, when caching is enabled, contextPct can be inflated, so we only use it
	// when caching is NOT active to avoid triggering compression too early
	accurateInputTokens := inputTokens
	if parsedResp.ContextPct > 0 && !cachingEnabled {
		calculatedTokens := int(parsedResp.ContextPct / 100.0 * float64(kiro.GetContextWindowLimit(originalModel)))
		if calculatedTokens > accurateInputTokens {
			accurateInputTokens = calculatedTokens
		}
	}

	usageFromUpstream := parsedResp.HasTokenUsage
	// Set cache tokens for response only when upstream usage does not include token usage.
	if !usageFromUpstream {
		parsedResp.CacheCreationTokens = cacheCreationTokens
		parsedResp.CacheReadTokens = cacheReadTokens
	}

	// When upstream provides real token usage, use upstream input_tokens (+ cache)
	// so that the JSON response to downstream matches the values used for billing.
	responseInputTokens := accurateInputTokens
	if usageFromUpstream {
		responseInputTokens = parsedResp.InputTokens + parsedResp.CacheCreationTokens + parsedResp.CacheReadTokens
	}

	// Build Claude response with accurate input tokens
	claudeResp := kiro.BuildClaudeNonStreamResponse(messageID, originalModel, responseInputTokens, parsedResp)

	// Inject web search blocks (server_tool_use + web_search_tool_result) if present
	if wsEvents := GetWebSearchEvents(c); len(wsEvents) > 0 {
		injectWebSearchContentBlocks(claudeResp, wsEvents)
	}

	// Serialize and send
	respJSON, err := json.Marshal(claudeResp)
	if err != nil {
		return nil, fmt.Errorf("marshal response: %w", err)
	}

	c.Data(http.StatusOK, "application/json", respJSON)

	var usage *ClaudeUsage
	if usageFromUpstream {
		usage = &ClaudeUsage{
			InputTokens:              parsedResp.InputTokens,
			OutputTokens:             parsedResp.OutputTokens,
			CacheCreationInputTokens: parsedResp.CacheCreationTokens,
			CacheReadInputTokens:     parsedResp.CacheReadTokens,
			ContextUsagePercent:      parsedResp.ContextPct,
		}
		// Fallback when upstream omits output token count.
		if usage.OutputTokens <= 0 {
			usage.OutputTokens = (len(parsedResp.Text) + 3) / 4
		}
	} else {
		// Local estimation fallback.
		usage = &ClaudeUsage{
			InputTokens:         accurateInputTokens,
			OutputTokens:        (len(parsedResp.Text) + 3) / 4,
			ContextUsagePercent: parsedResp.ContextPct,
		}
	}

	return &kiroStreamResult{
		usage:             usage,
		firstTokenMs:      firstTokenMs,
		usageFromUpstream: usageFromUpstream,
	}, nil
}

func readKiroNonStreamingBodyWithTimeout(c *gin.Context, resp *http.Response, timeout time.Duration) ([]byte, error) {
	readBody := func() ([]byte, error) {
		return io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	}
	if timeout <= 0 {
		return readBody()
	}

	type readResult struct {
		body []byte
		err  error
	}
	resultCh := make(chan readResult, 1)
	go func() {
		body, err := readBody()
		resultCh <- readResult{body: body, err: err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var ctxDone <-chan struct{}
	if c != nil && c.Request != nil {
		ctxDone = c.Request.Context().Done()
	}

	select {
	case result := <-resultCh:
		return result.body, result.err
	case <-timer.C:
		_ = resp.Body.Close()
		return nil, newKiroInitialResponseTimeoutError("non_stream_response", timeout)
	case <-ctxDone:
		_ = resp.Body.Close()
		return nil, c.Request.Context().Err()
	}
}

// buildWebSearchSSEEvents builds SSE events for a single web search (server_tool_use + web_search_tool_result)
func buildWebSearchSSEEvents(evt WebSearchEvent, startIndex int) []kiro.ClaudeSSEEvent {
	toolUseIndex := startIndex
	resultIndex := startIndex + 1

	// Build search result content blocks
	resultContent := make([]map[string]any, 0)
	for _, r := range evt.Results {
		item := map[string]any{
			"type":  "web_search_result",
			"url":   r.URL,
			"title": r.Title,
		}
		if r.EncryptedContent != "" {
			item["encrypted_content"] = r.EncryptedContent
		}
		if r.PageContent != "" {
			item["page_content"] = r.PageContent
		}
		resultContent = append(resultContent, item)
	}

	inputJSON, _ := json.Marshal(map[string]string{"query": evt.Query})

	return []kiro.ClaudeSSEEvent{
		// content_block_start: server_tool_use
		{
			EventType: "content_block_start",
			Data: map[string]any{
				"type":  "content_block_start",
				"index": toolUseIndex,
				"content_block": map[string]any{
					"type":  "server_tool_use",
					"id":    evt.ID,
					"name":  "web_search",
					"input": map[string]any{},
				},
			},
		},
		// content_block_delta: input_json_delta
		{
			EventType: "content_block_delta",
			Data: map[string]any{
				"type":  "content_block_delta",
				"index": toolUseIndex,
				"delta": map[string]any{
					"type":         "input_json_delta",
					"partial_json": string(inputJSON),
				},
			},
		},
		// content_block_stop: server_tool_use
		{
			EventType: "content_block_stop",
			Data: map[string]any{
				"type":  "content_block_stop",
				"index": toolUseIndex,
			},
		},
		// content_block_start: web_search_tool_result
		{
			EventType: "content_block_start",
			Data: map[string]any{
				"type":  "content_block_start",
				"index": resultIndex,
				"content_block": map[string]any{
					"type":        "web_search_tool_result",
					"tool_use_id": evt.ID,
					"content":     resultContent,
				},
			},
		},
		// content_block_stop: web_search_tool_result
		{
			EventType: "content_block_stop",
			Data: map[string]any{
				"type":  "content_block_stop",
				"index": resultIndex,
			},
		},
	}
}

// injectWebSearchContentBlocks prepends server_tool_use + web_search_tool_result blocks
// into a non-streaming Claude response's content array.
func injectWebSearchContentBlocks(resp map[string]any, events []WebSearchEvent) {
	existingContent, _ := resp["content"].([]map[string]any)

	var injected []map[string]any
	for _, evt := range events {
		// server_tool_use block
		injected = append(injected, map[string]any{
			"type":  "server_tool_use",
			"id":    evt.ID,
			"name":  "web_search",
			"input": map[string]string{"query": evt.Query},
		})

		// web_search_tool_result block
		resultContent := make([]map[string]any, 0)
		for _, r := range evt.Results {
			item := map[string]any{
				"type":  "web_search_result",
				"url":   r.URL,
				"title": r.Title,
			}
			if r.EncryptedContent != "" {
				item["encrypted_content"] = r.EncryptedContent
			}
			if r.PageContent != "" {
				item["page_content"] = r.PageContent
			}
			resultContent = append(resultContent, item)
		}
		injected = append(injected, map[string]any{
			"type":        "web_search_tool_result",
			"tool_use_id": evt.ID,
			"content":     resultContent,
		})
	}

	// Prepend web search blocks before existing content
	combined := make([]map[string]any, 0, len(injected)+len(existingContent))
	combined = append(combined, injected...)
	combined = append(combined, existingContent...)
	resp["content"] = combined
}

func (s *KiroGatewayService) shouldRetryUpstreamError(statusCode int) bool {
	switch statusCode {
	case 500, 502, 503, 504:
		return true
	default:
		return false
	}
}

func (s *KiroGatewayService) shouldFailoverUpstreamError(statusCode int) bool {
	switch statusCode {
	case 401, 403, 429, 529:
		return true
	default:
		return statusCode >= 500
	}
}

func (s *KiroGatewayService) handleUpstreamError(ctx context.Context, prefix string, account *Account, statusCode int, headers http.Header, body []byte) {
	switch statusCode {
	case 429:
		// Rate limited - mark cooldown (skip for apikey accounts)
		if s.tokenProvider != nil && !account.IsKiroApiKey() {
			s.tokenProvider.MarkCooldown(account.ID, 30*time.Second)
		}
		log.Printf("%s status=429 rate_limited", prefix)

	case 401:
		errMsg := extractKiroErrorMessage(body)
		if !account.IsKiroApiKey() {
			// OAuth tokens may be stale, so invalidate the cache and let the next request refresh.
			if s.tokenProvider != nil {
				s.tokenProvider.InvalidateToken(account.ID)
			}
			log.Printf("%s status=401 token_invalidated msg=%s", prefix, errMsg)
			return
		}
		// API keys are static; track repeated authorization failures without touching OAuth token state.
		log.Printf("%s status=401 api_key_unauthorized msg=%s", prefix, errMsg)

	case 403:
		// Permission error — mark banned (skip for apikey accounts)
		errMsg := extractKiroErrorMessage(body)
		if strings.Contains(strings.ToLower(errMsg), "expired") ||
			strings.Contains(strings.ToLower(errMsg), "invalid") ||
			strings.Contains(strings.ToLower(errMsg), "forbidden") {
			if s.tokenProvider != nil && !account.IsKiroApiKey() {
				s.tokenProvider.MarkBanned(account.ID, errMsg)
			}
		}
		log.Printf("%s status=403 auth_error msg=%s", prefix, errMsg)

	case 400:
		// Bad request — log full body for debugging (context too long, malformed request, etc.)
		errMsg := extractKiroErrorMessage(body)
		log.Printf("%s status=400 bad_request msg=%s body=%s", prefix, errMsg, truncateForLog(body, 1000))

	case 529:
		// Overloaded - mark cooldown (skip for apikey accounts)
		if s.tokenProvider != nil && !account.IsKiroApiKey() {
			s.tokenProvider.MarkCooldown(account.ID, 60*time.Second)
		}
		log.Printf("%s status=529 overloaded", prefix)
	}

	// Use rate limit service for other handling
	if s.rateLimitService != nil {
		s.rateLimitService.HandleUpstreamError(ctx, account, statusCode, headers, body)
	}
}

func (s *KiroGatewayService) writeClaudeError(c *gin.Context, status int, errType, message string) error {
	c.JSON(status, gin.H{
		"type":  "error",
		"error": gin.H{"type": errType, "message": message},
	})
	return fmt.Errorf("%s", message)
}

func (s *KiroGatewayService) writeMappedClaudeError(c *gin.Context, account *Account, upstreamStatus int, upstreamRequestID string, body []byte) error {
	upstreamMsg := extractKiroErrorMessage(body)
	upstreamMsg = sanitizeKiroClientErrorMessage(upstreamMsg)

	setOpsUpstreamError(c, upstreamStatus, upstreamMsg, "")
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: upstreamStatus,
		UpstreamRequestID:  upstreamRequestID,
		Kind:               "http_error",
		Message:            upstreamMsg,
	})

	var statusCode int
	var errType, errMsg string

	switch upstreamStatus {
	case 400:
		statusCode = http.StatusBadRequest
		errType = "invalid_request_error"
		if upstreamMsg != "" {
			errMsg = "Invalid request: " + upstreamMsg
		} else {
			errMsg = "Invalid request"
		}
	case 401:
		statusCode = http.StatusBadGateway
		errType = "authentication_error"
		errMsg = "Upstream authentication failed"
	case 403:
		statusCode = http.StatusBadGateway
		errType = "permission_error"
		errMsg = "Upstream access forbidden"
	case 429:
		statusCode = http.StatusTooManyRequests
		errType = "rate_limit_error"
		errMsg = "Upstream rate limit exceeded"
	case 529:
		statusCode = http.StatusServiceUnavailable
		errType = "overloaded_error"
		errMsg = "Upstream service overloaded"
	default:
		statusCode = http.StatusBadGateway
		errType = "upstream_error"
		errMsg = "Upstream request failed"
	}

	c.JSON(statusCode, gin.H{
		"type":  "error",
		"error": gin.H{"type": errType, "message": errMsg},
	})

	if upstreamMsg == "" {
		return fmt.Errorf("upstream error: %d", upstreamStatus)
	}
	return fmt.Errorf("upstream error: %d message=%s", upstreamStatus, upstreamMsg)
}

// extractKiroErrorMessage extracts error message from Kiro/CodeWhisperer response
func extractKiroErrorMessage(body []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}

	// Try message field
	if msg, ok := payload["message"].(string); ok && strings.TrimSpace(msg) != "" {
		return msg
	}

	// Try error.message
	if errObj, ok := payload["error"].(map[string]any); ok {
		if msg, ok := errObj["message"].(string); ok && strings.TrimSpace(msg) != "" {
			return msg
		}
	}

	// Try __type for AWS errors
	if errType, ok := payload["__type"].(string); ok {
		return errType
	}

	return ""
}

// sleepKiroBackoffWithContext sleeps with exponential backoff, respecting context cancellation
func sleepKiroBackoffWithContext(ctx context.Context, attempt int) bool {
	delay := kiroRetryBaseDelay * time.Duration(1<<uint(attempt-1))
	if delay > kiroRetryMaxDelay {
		delay = kiroRetryMaxDelay
	}

	select {
	case <-ctx.Done():
		return false
	case <-time.After(delay):
		return true
	}
}

// TestConnection tests Kiro account connection
func (s *KiroGatewayService) TestConnection(ctx context.Context, account *Account, modelID string) (*TestConnectionResult, error) {
	// Get token
	if s.tokenProvider == nil {
		return nil, errors.New("kiro token provider not configured")
	}
	accessToken, err := s.tokenProvider.GetAccessToken(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("get access_token failed: %w", err)
	}

	// apikey accounts: test connection directly against base_url
	if account.IsKiroApiKey() {
		return s.testClaudeAPIConnection(ctx, account, accessToken, modelID)
	}

	// Get profile ARN (in-memory cache > snapshot > db)
	profileArn, err := resolveKiroProfileArn(ctx, account, s.tokenProvider, s.accountRepo, "[kiro-TestConnection]")
	if err != nil {
		return nil, err
	}

	// Build test request
	testModel := modelID
	if testModel == "" {
		testModel = "claude-3-5-sonnet-20241022"
	}

	testFreeTier := s.isKiroFreeTier(account)
	if remapped, ok := s.remapModelForFreeTier(account, testModel, testFreeTier); ok {
		log.Printf("[kiro-TestConnection] free_tier_model_remap: %s -> %s", testModel, remapped)
		testModel = remapped
	}
	testClaudeReq := &kiro.ClaudeRequest{
		Model: testModel,
		Messages: []kiro.ClaudeMessage{
			{
				Role:    "user",
				Content: "hi",
			},
		},
		MaxTokens: 10,
		Stream:    false,
	}

	activeTestModel := s.resolveKiroUpstreamModel(account, testModel)
	_, reqBody, err := s.prepareCodeWhispererPayload(testClaudeReq, profileArn, nil, activeTestModel)
	if err != nil {
		return nil, fmt.Errorf("prepare request: %w", err)
	}

	// Proxy URL (Free-tier: random from pool; others: account-bound)
	proxyURL := s.resolveProxyURL(ctx, account, testFreeTier)

	// Build endpoint list (test connection: small request)
	endpoints := getKiroEndpoints(account, s.cfg)

	// Generate machine ID for User-Agent headers (Free-tier: may rotate randomly)
	machineID := s.resolveMachineID(account, testFreeTier)
	kiroVersion := "0.11.107"

	// Try each endpoint
	var lastErr error
	for _, ep := range endpoints {
		for {
			req, err := http.NewRequestWithContext(ctx, "POST", ep.URL, bytes.NewReader(reqBody))
			if err != nil {
				return nil, fmt.Errorf("create request: %w", err)
			}

			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+accessToken)
			req.Header.Set("Accept", "text/event-stream")
			req.Header.Set("User-Agent", fmt.Sprintf("aws-sdk-js/1.0.27 ua/2.1 os/linux lang/js md/nodejs#22.12.0 api/codewhispererstreaming#1.0.27 m/E KiroIDE-%s-%s", kiroVersion, machineID))
			req.Header.Set("x-amz-user-agent", fmt.Sprintf("aws-sdk-js/1.0.27 KiroIDE-%s-%s", kiroVersion, machineID))
			req.Header.Set("x-amzn-kiro-agent-mode", "vibe")
			req.Header.Set("x-amzn-codewhisperer-optout", "true")
			req.Header.Set("Host", ep.Host)
			s.applyKiroConnectionHeader(req)
			req.Header.Set("amz-sdk-invocation-id", uuid.New().String())
			req.Header.Set("amz-sdk-request", "attempt=1; max=3")
			if ep.AmzTarget != "" {
				req.Header.Set("X-Amz-Target", ep.AmzTarget)
			}

			resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
			if err != nil {
				log.Printf("[kiro-TestConnection] endpoint=%s request_failed error=%v", ep.Name, err)
				lastErr = err
				break
			}

			respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			resp.Body.Close()
			if err != nil {
				return nil, fmt.Errorf("read response: %w", err)
			}

			log.Printf("[kiro-TestConnection] endpoint=%s response status=%d, body_len=%d, body_preview=%s",
				ep.Name, resp.StatusCode, len(respBody), truncateForLog(respBody, 500))

			if resp.StatusCode >= 400 {
				errorMsg := extractKiroErrorMessage(respBody)
				if fallbackModel, ok := s.maybeFallbackUnsupportedKiroModel(account, testModel, activeTestModel, errorMsg); ok {
					log.Printf("[kiro-TestConnection] endpoint=%s dynamic_model_fallback_retry: %s -> %s", ep.Name, activeTestModel, fallbackModel)
					activeTestModel = fallbackModel
					_, reqBody, err = s.prepareCodeWhispererPayload(testClaudeReq, profileArn, nil, activeTestModel)
					if err != nil {
						return nil, fmt.Errorf("prepare fallback request: %w", err)
					}
					continue
				}

				lastErr = fmt.Errorf("endpoint %s returned %d: %s", ep.Name, resp.StatusCode, string(respBody))
				log.Printf("[kiro-TestConnection] endpoint=%s failed, trying next", ep.Name)
				break
			}

			// Parse response to extract text
			parsedResp, parseErr := kiro.ParseCompleteResponseWithNameRestoreAndThinkingStrict(respBody, nil, false)
			if parseErr != nil {
				safeParseErr := sanitizeKiroClientErrorMessage(parseErr.Error())
				lastErr = fmt.Errorf("endpoint %s returned invalid event stream: %s", ep.Name, safeParseErr)
				log.Printf("[kiro-TestConnection] endpoint=%s invalid_event_stream error=%s, trying next", ep.Name, safeParseErr)
				break
			}
			if !isRenderableKiroCompleteResponse(parsedResp) {
				lastErr = fmt.Errorf("endpoint %s returned empty response", ep.Name)
				log.Printf("[kiro-TestConnection] endpoint=%s empty_response, trying next", ep.Name)
				break
			}

			s.markKiroModelSupported(account, testModel, activeTestModel)

			log.Printf("[kiro-TestConnection] endpoint=%s parsed text=%q, tool_calls=%d", ep.Name, parsedResp.Text, len(parsedResp.ToolCalls))

			return &TestConnectionResult{
				Text:        parsedResp.Text,
				MappedModel: kiro.GetModelID(activeTestModel),
			}, nil
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("all endpoints failed")
}

// buildClaudeAPIHTTPRequest constructs an HTTP request for the Claude API with standard headers.
// Used by forwardClaudeAPIRequest and its signature-error retry paths to avoid duplicating header setup.
// sessionID: if non-empty, sets the session_id header for upstream prompt cache optimization.
// anthropicBeta: if non-empty, sets the anthropic-beta header (passthrough from downstream).
// retryCount: sets X-Stainless-Retry-Count to match real SDK retry behavior (0 for first attempt).
// fingerprint: pre-generated request headers (from NewRequestHeaders) to ensure OS/Arch consistency across retries.
func (s *KiroGatewayService) buildClaudeAPIHTTPRequest(ctx context.Context, targetURL, apiKey string, body []byte, sessionID, anthropicBeta string, retryCount int, fingerprint map[string]string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	if sessionID != "" {
		req.Header.Set("session_id", sessionID)
	}
	if anthropicBeta != "" {
		req.Header.Set("anthropic-beta", anthropicBeta)
	}
	// Apply Claude Code CLI client fingerprint headers
	for key, value := range fingerprint {
		req.Header.Set(key, value)
	}
	// Override retry count to match real SDK behavior (increments on each retry)
	if retryCount > 0 {
		req.Header.Set("X-Stainless-Retry-Count", strconv.Itoa(retryCount))
	}
	return req, nil
}

func looksLikeClaudeToolSignatureError(message string) bool {
	message = strings.ToLower(message)
	return strings.Contains(message, "tool_use") ||
		strings.Contains(message, "tool_result") ||
		strings.Contains(message, "functioncall") || strings.Contains(message, "function_call") ||
		strings.Contains(message, "functionresponse") || strings.Contains(message, "function_response")
}

func (s *KiroGatewayService) retryClaudeAPISignatureError(
	ctx context.Context,
	resp *http.Response,
	account *Account,
	targetURL, apiKey string,
	body []byte,
	sessionID, anthropicBeta, proxyURL, prefix string,
	fingerprint map[string]string,
) *http.Response {
	if resp == nil || resp.StatusCode != http.StatusBadRequest {
		return resp
	}

	originalBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	_ = resp.Body.Close()
	if !isThinkingBlockSignatureError(originalBody) {
		resp.Body = io.NopCloser(bytes.NewReader(originalBody))
		return resp
	}

	log.Printf("%s detected thinking block signature error, attempting retry with filtered body", prefix)

	// Stage 1: filter thinking blocks (thinking→text, remove redacted_thinking, disable thinking).
	time.Sleep(500 * time.Millisecond)
	filteredBody := FilterThinkingBlocksForRetry(body)
	if retryReq, buildErr := s.buildClaudeAPIHTTPRequest(ctx, targetURL, apiKey, filteredBody, sessionID, anthropicBeta, 1, fingerprint); buildErr == nil {
		if retryResp, retryErr := s.httpUpstream.Do(retryReq, proxyURL, account.ID, account.Concurrency); retryErr == nil {
			if retryResp.StatusCode < 400 {
				log.Printf("%s signature error retry succeeded (thinking downgraded)", prefix)
				return retryResp
			}

			retryBody, retryReadErr := io.ReadAll(io.LimitReader(retryResp.Body, 2<<20))
			_ = retryResp.Body.Close()
			if retryReadErr == nil && retryResp.StatusCode == http.StatusBadRequest &&
				isThinkingBlockSignatureError(retryBody) &&
				looksLikeClaudeToolSignatureError(extractUpstreamErrorMessage(retryBody)) {
				log.Printf("%s signature retry still failing and tool-related, retrying with tool blocks downgraded", prefix)
				time.Sleep(500 * time.Millisecond)
				filteredToolBody := FilterSignatureSensitiveBlocksForRetry(body)
				if retryReq2, buildErr2 := s.buildClaudeAPIHTTPRequest(ctx, targetURL, apiKey, filteredToolBody, sessionID, anthropicBeta, 2, fingerprint); buildErr2 == nil {
					if retryResp2, retryErr2 := s.httpUpstream.Do(retryReq2, proxyURL, account.ID, account.Concurrency); retryErr2 == nil {
						if retryResp2.StatusCode < 400 {
							log.Printf("%s signature error retry succeeded (tools downgraded)", prefix)
							return retryResp2
						}
						_ = retryResp2.Body.Close()
					}
				}
			}
		}
	}

	log.Printf("%s signature error retries exhausted, falling back to failover", prefix)
	resp.Body = io.NopCloser(bytes.NewReader(originalBody))
	return resp
}

// forwardClaudeAPIRequest forwards Claude API requests directly to a base_url endpoint (apikey accounts).
// No CodeWhisperer transformation — request and response are Claude API format.
func (s *KiroGatewayService) forwardClaudeAPIRequest(
	ctx context.Context, c *gin.Context, account *Account,
	claudeReq *kiro.ClaudeRequest, body []byte,
	apiKey, proxyURL, originalModel string,
	startTime time.Time,
	hideCacheUsage bool,
) (*ForwardResult, error) {
	prefix := fmt.Sprintf("[kiro-apikey-Forward] account=%s", account.Name)

	baseURL := account.GetKiroBaseURL()
	if baseURL == "" {
		return nil, fmt.Errorf("base_url is empty for apikey account %d", account.ID)
	}
	targetURL := strings.TrimRight(baseURL, "/") + "/v1/messages"

	// Read stable session_id from X-Session-ID header (set by apikey branch in Forward()).
	stableSessionID := c.GetHeader("X-Session-ID")
	anthropicBeta := c.GetHeader("anthropic-beta")

	// Generate fingerprint once per request — OS/Arch stays consistent across retries
	fingerprint := claude.NewRequestHeaders()

	req, err := s.buildClaudeAPIHTTPRequest(ctx, targetURL, apiKey, body, stableSessionID, anthropicBeta, 0, fingerprint)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	log.Printf("%s url=%s model=%s stream=%v body_size=%d session_id=%s", prefix, targetURL, originalModel, claudeReq.Stream, len(body), stableSessionID)

	// Apply request jitter before upstream call to prevent thundering herd
	s.applyRequestJitter(ctx)

	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return nil, fmt.Errorf("upstream request failed: %w", err)
	}
	resp = s.retryClaudeAPISignatureError(
		ctx,
		resp,
		account,
		targetURL,
		apiKey,
		body,
		stableSessionID,
		anthropicBeta,
		proxyURL,
		prefix,
		fingerprint,
	)
	defer func() { _ = resp.Body.Close() }()

	// Handle error responses
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		log.Printf("%s status=%d body=%s", prefix, resp.StatusCode, truncateForLog(respBody, 1000))

		s.handleUpstreamError(ctx, prefix, account, resp.StatusCode, resp.Header, respBody)

		// apikey path: 400 errors also trigger failover (different apikey may have different config/limits)
		if resp.StatusCode == http.StatusBadRequest {
			upstreamMsg := extractKiroErrorMessage(respBody)
			upstreamMsg = sanitizeKiroClientErrorMessage(upstreamMsg)
			log.Printf("%s status=400 apikey bad_request msg=%s, triggering failover", prefix, upstreamMsg)
			return nil, &UpstreamFailoverError{StatusCode: resp.StatusCode, Message: upstreamMsg}
		}

		if s.shouldFailoverUpstreamError(resp.StatusCode) {
			return nil, &UpstreamFailoverError{StatusCode: resp.StatusCode}
		}
		return nil, s.writeMappedClaudeError(c, account, resp.StatusCode, resp.Header.Get("x-request-id"), respBody)
	}

	requestID := resp.Header.Get("x-request-id")
	if requestID == "" {
		requestID = resp.Header.Get("request-id")
	}
	if requestID != "" {
		c.Header("x-request-id", requestID)
	}

	inputTokens := kiro.EstimateInputTokens(claudeReq)

	if claudeReq.Stream {
		// Stream: pipe SSE line-by-line to client (with cache usage rewrite)
		c.Header("Content-Type", "text/event-stream; charset=utf-8")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")
		c.Status(http.StatusOK)

		flusher, ok := c.Writer.(http.Flusher)
		if !ok {
			return nil, errors.New("streaming not supported")
		}

		var outputTokens int
		var firstTokenMs *int
		upstreamUsage := &ClaudeUsage{}
		usageFromUpstream := false
		clientDisconnected := false
		var sseTail strings.Builder

		// Commit response bytes immediately after upstream headers arrive. This
		// prevents a slow first body chunk from leaving Cloudflare waiting for
		// downstream response data until its 524 timeout.
		if _, writeErr := c.Writer.Write([]byte(":\n\n")); writeErr != nil {
			clientDisconnected = true
		} else {
			flusher.Flush()
		}

		type claudeAPIStreamChunk struct {
			data []byte
			err  error
		}
		chunks := make(chan claudeAPIStreamChunk, 32)
		readerDone := make(chan struct{})
		defer close(readerDone)
		sendChunk := func(chunk claudeAPIStreamChunk) bool {
			select {
			case chunks <- chunk:
				return true
			case <-readerDone:
				return false
			}
		}
		go func() {
			defer close(chunks)
			buf := make([]byte, 4096)
			for {
				n, readErr := resp.Body.Read(buf)
				if n > 0 {
					data := make([]byte, n)
					copy(data, buf[:n])
					if !sendChunk(claudeAPIStreamChunk{data: data}) {
						return
					}
				}
				if readErr != nil {
					_ = sendChunk(claudeAPIStreamChunk{err: readErr})
					return
				}
			}
		}()

		keepaliveInterval := s.kiroStreamKeepaliveInterval()
		keepaliveTicker := time.NewTicker(keepaliveInterval)
		defer keepaliveTicker.Stop()
		lastDataAt := time.Now()

	streamLoop:
		for {
			select {
			case chunk, open := <-chunks:
				if !open || chunk.err != nil {
					break streamLoop
				}
				if len(chunk.data) == 0 {
					continue
				}
				lastDataAt = time.Now()
				if firstTokenMs == nil {
					ms := int(time.Since(startTime).Milliseconds())
					firstTokenMs = &ms
				}

				// Count output tokens from text deltas for estimation
				outputTokens += s.countClaudeSSEOutputTokens(chunk.data)
				// Reconstruct full SSE lines for usage parsing + cache rewrite.
				sseTail.Write(chunk.data)
				sseData := sseTail.String()
				lines := strings.Split(sseData, "\n")
				sseTail.Reset()
				if len(lines) > 0 {
					// Keep last partial line as tail.
					sseTail.WriteString(lines[len(lines)-1])
					for _, line := range lines[:len(lines)-1] {
						trimmed := strings.TrimSpace(line)

						// Parse upstream usage from data lines (internal tracking).
						if strings.HasPrefix(trimmed, "data: ") {
							data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data: "))
							if data != "" && data != "[DONE]" {
								if updateClaudeUsageFromSSEData(data, upstreamUsage) {
									usageFromUpstream = true
								}
							}
						}

						// Rewrite cache usage (only if platform injected) and write to client.
						if !clientDisconnected {
							clientLine := line
							if hideCacheUsage {
								clientLine = rewriteCacheUsageInSSELine(line)
							}
							if _, writeErr := fmt.Fprintf(c.Writer, "%s\n", clientLine); writeErr != nil {
								clientDisconnected = true
							}
						}
					}
				}

				if !clientDisconnected {
					flusher.Flush()
				}

			case <-keepaliveTicker.C:
				if clientDisconnected || time.Since(lastDataAt) < keepaliveInterval {
					continue
				}
				// The direct passthrough may currently be between an SSE event's
				// event/data lines. A single comment line keeps the connection active
				// without the blank line that would prematurely dispatch that event.
				if _, writeErr := c.Writer.Write([]byte(":\n")); writeErr != nil {
					clientDisconnected = true
					continue
				}
				flusher.Flush()
			}
		}

		// Flush any remaining data in sseTail (e.g., final line without trailing \n)
		if tail := sseTail.String(); tail != "" {
			trimmed := strings.TrimSpace(tail)
			if strings.HasPrefix(trimmed, "data: ") {
				data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data: "))
				if data != "" && data != "[DONE]" {
					if updateClaudeUsageFromSSEData(data, upstreamUsage) {
						usageFromUpstream = true
					}
				}
			}
			if !clientDisconnected {
				clientLine := tail
				if hideCacheUsage {
					clientLine = rewriteCacheUsageInSSELine(tail)
				}
				_, _ = fmt.Fprintf(c.Writer, "%s\n", clientLine)
				flusher.Flush()
			}
		}

		var usage *ClaudeUsage
		if usageFromUpstream {
			usage = upstreamUsage
			// Keep conservative fallback when upstream omits output token count.
			if usage.OutputTokens <= 0 {
				usage.OutputTokens = outputTokens
			}
			if usage.InputTokens <= 0 && usage.CacheCreationInputTokens == 0 && usage.CacheReadInputTokens == 0 {
				usage.InputTokens = inputTokens
			}
		} else {
			// Local estimation fallback.
			usage = &ClaudeUsage{
				InputTokens:  inputTokens,
				OutputTokens: outputTokens,
			}
		}

		return &ForwardResult{
			RequestID:    requestID,
			Usage:        *usage,
			Model:        originalModel,
			Stream:       true,
			Duration:     time.Since(startTime),
			FirstTokenMs: firstTokenMs,
		}, nil
	}

	// Non-stream: read and pipe JSON response
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	ms := int(time.Since(startTime).Milliseconds())

	// 先提取真实 usage（用于内部计费），再根据注入情况决定是否重写 cache tokens
	upstreamUsage, usageFromUpstream := extractClaudeUsageFromJSON(respBody)

	// 仅平台注入时隐藏 cache tokens；用户自带的透传原始响应
	if hideCacheUsage {
		c.Data(http.StatusOK, "application/json", rewriteCacheUsageInJSON(respBody))
	} else {
		c.Data(http.StatusOK, "application/json", respBody)
	}
	var usage *ClaudeUsage
	if usageFromUpstream {
		usage = upstreamUsage
		// Conservative fallback when output token usage is missing.
		if usage.OutputTokens <= 0 {
			usage.OutputTokens = s.estimateClaudeJSONOutputTokens(respBody)
		}
		if usage.InputTokens <= 0 && usage.CacheCreationInputTokens == 0 && usage.CacheReadInputTokens == 0 {
			usage.InputTokens = inputTokens
		}
	} else {
		// Local estimation fallback.
		outputTokens := s.estimateClaudeJSONOutputTokens(respBody)
		usage = &ClaudeUsage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
		}
	}

	return &ForwardResult{
		RequestID:    requestID,
		Usage:        *usage,
		Model:        originalModel,
		Stream:       false,
		Duration:     time.Since(startTime),
		FirstTokenMs: &ms,
	}, nil
}

func updateClaudeUsageFromSSEData(data string, usage *ClaudeUsage) bool {
	if usage == nil || data == "" {
		return false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return false
	}

	eventType, _ := payload["type"].(string)
	switch eventType {
	case "message_start":
		if message, ok := payload["message"].(map[string]any); ok && message != nil {
			if usageMap, ok := message["usage"].(map[string]any); ok && usageMap != nil {
				return applyClaudeUsageMap(usageMap, usage, false)
			}
		}
	case "message_delta":
		if usageMap, ok := payload["usage"].(map[string]any); ok && usageMap != nil {
			return applyClaudeUsageMap(usageMap, usage, true)
		}
	}
	return false
}

func extractClaudeUsageFromJSON(body []byte) (*ClaudeUsage, bool) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, false
	}
	usageMap, ok := payload["usage"].(map[string]any)
	if !ok || usageMap == nil {
		return nil, false
	}
	usage := &ClaudeUsage{}
	if !applyClaudeUsageMap(usageMap, usage, true) {
		return nil, false
	}
	return usage, true
}

func applyClaudeUsageMap(usageMap map[string]any, usage *ClaudeUsage, overwrite bool) bool {
	if usageMap == nil || usage == nil {
		return false
	}
	applied := false

	setIf := func(target *int, source map[string]any, keys ...string) {
		value, present := usageIntByKeys(source, keys...)
		if !present || value < 0 {
			return
		}
		if overwrite || *target == 0 {
			*target = value
			if value > 0 {
				applied = true
			}
		}
	}
	setIfFloat := func(target *float64, value float64) {
		if value <= 0 {
			return
		}
		if overwrite || *target == 0 {
			*target = value
			applied = true
		}
	}

	setIf(&usage.InputTokens, usageMap, "input_tokens", "inputTokens", "prompt_tokens", "promptTokens")
	setIf(&usage.OutputTokens, usageMap, "output_tokens", "outputTokens", "completion_tokens", "completionTokens")
	setIf(&usage.CacheCreationInputTokens, usageMap, "cache_creation_input_tokens", "cacheCreationInputTokens")
	setIf(&usage.CacheReadInputTokens, usageMap, "cache_read_input_tokens", "cacheReadInputTokens", "cached_tokens", "cachedTokens")
	if usage.CacheReadInputTokens == 0 {
		if details, ok := usageMap["input_tokens_details"].(map[string]any); ok && details != nil {
			setIf(&usage.CacheReadInputTokens, details, "cached_tokens", "cachedTokens", "cache_read_input_tokens", "cacheReadInputTokens")
		}
	}
	if usage.CacheReadInputTokens == 0 {
		if details, ok := usageMap["prompt_tokens_details"].(map[string]any); ok && details != nil {
			setIf(&usage.CacheReadInputTokens, details, "cached_tokens", "cachedTokens", "cache_read_input_tokens", "cacheReadInputTokens")
		}
	}
	if value, ok := usageFloatByKeys(usageMap, "context_usage_percent", "contextUsagePercentage", "contextUsagePercent"); ok {
		setIfFloat(&usage.ContextUsagePercent, value)
	}

	return applied
}

func usageIntByKeys(m map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		raw, exists := m[key]
		if !exists {
			continue
		}
		if value, ok := usageIntFromAny(raw); ok {
			return value, true
		}
	}
	return 0, false
}

func usageFloatByKeys(m map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		if v, ok := usageFloatFromAny(m[key]); ok {
			return v, true
		}
	}
	return 0, false
}

func usageIntFromAny(v any) (int, bool) {
	f, ok := usageFloatFromAny(v)
	if !ok {
		return 0, false
	}
	return int(f), true
}

func usageFloatFromAny(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	case json.Number:
		if f, err := n.Float64(); err == nil {
			return f, true
		}
	case string:
		s := strings.TrimSpace(n)
		if s == "" {
			return 0, false
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

// countClaudeSSEOutputTokens estimates output tokens from Claude SSE stream chunks
// by extracting text deltas and tool input deltas, using (len+3)/4 estimation
// consistent with the OAuth/CW path.
func (s *KiroGatewayService) countClaudeSSEOutputTokens(chunk []byte) int {
	tokens := 0
	lines := bytes.Split(chunk, []byte("\n"))
	for _, line := range lines {
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		data := line[6:]
		var raw map[string]json.RawMessage
		if json.Unmarshal(data, &raw) != nil {
			continue
		}
		var eventType string
		if json.Unmarshal(raw["type"], &eventType) != nil {
			continue
		}
		switch eventType {
		case "content_block_delta":
			var delta struct {
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			if json.Unmarshal(data, &delta) == nil {
				switch delta.Delta.Type {
				case "text_delta":
					tokens += (len(delta.Delta.Text) + 3) / 4
				case "input_json_delta":
					tokens += (len(delta.Delta.PartialJSON) + 3) / 4
				}
			}
		}
	}
	return tokens
}

// estimateClaudeJSONOutputTokens estimates output tokens from a Claude API JSON response
// using (len+3)/4 estimation consistent with the OAuth/CW path.
func (s *KiroGatewayService) estimateClaudeJSONOutputTokens(body []byte) int {
	var resp struct {
		Content []struct {
			Type  string `json:"type"`
			Text  string `json:"text"`
			Input any    `json:"input"`
		} `json:"content"`
	}
	if json.Unmarshal(body, &resp) != nil {
		return 0
	}
	tokens := 0
	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			tokens += (len(block.Text) + 3) / 4
		case "tool_use":
			if inputBytes, err := json.Marshal(block.Input); err == nil {
				tokens += (len(inputBytes) + 3) / 4
			}
		}
	}
	return tokens
}

// testClaudeAPIConnection tests connection for apikey accounts by sending a minimal request to base_url.
func (s *KiroGatewayService) testClaudeAPIConnection(ctx context.Context, account *Account, apiKey string, modelID string) (*TestConnectionResult, error) {
	baseURL := account.GetKiroBaseURL()
	if baseURL == "" {
		return nil, fmt.Errorf("base_url is empty for apikey account %d", account.ID)
	}
	targetURL := strings.TrimRight(baseURL, "/") + "/v1/messages"

	// Pick a test model: use provided modelID, or first key from model_mapping, or default
	testModel := modelID
	if testModel == "" {
		if mapping := account.GetModelMapping(); len(mapping) > 0 {
			for k := range mapping {
				testModel = k
				break
			}
		}
	}
	if testModel == "" {
		testModel = "claude-sonnet-4-20250514"
	}
	requestedTestModel := testModel
	testModel = account.GetMappedModel(testModel)
	if testModel != requestedTestModel {
		log.Printf("[kiro-apikey-TestConnection] model_mapping: %s -> %s", requestedTestModel, testModel)
	}

	testReq := map[string]any{
		"model":      testModel,
		"max_tokens": 10,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
	}
	reqBody, err := json.Marshal(testReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	req, err := http.NewRequestWithContext(ctx, "POST", targetURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	// Apply Claude Code CLI client fingerprint headers (with randomized OS/Arch)
	for key, value := range claude.NewRequestHeaders() {
		req.Header.Set(key, value)
	}

	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, truncateForLog(respBody, 500))
	}

	// Extract text from Claude API response
	var claudeResp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	text := string(respBody)
	if json.Unmarshal(respBody, &claudeResp) == nil && len(claudeResp.Content) > 0 {
		text = claudeResp.Content[0].Text
	}

	return &TestConnectionResult{
		Text:        text,
		MappedModel: testModel,
	}, nil
}
