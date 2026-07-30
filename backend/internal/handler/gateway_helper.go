package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// claudeCodeValidator is a singleton validator for Claude Code client detection
var claudeCodeValidator = service.NewClaudeCodeValidator()

const (
	// OpenAI-compatible Kiro requests commit an initial SSE chunk early, so they
	// keep the wider budget used only to cap account failover duration.
	kiroOAuthFailoverTotalBudget       = 105 * time.Second
	kiroOAuthFailoverNextAttemptBuffer = 45 * time.Second

	// Non-streaming Claude Messages cannot use SSE ACK, so return a retryable
	// client error before Cloudflare's 120s window. Streaming Kiro OAuth requests
	// commit their SSE envelope in the service layer after upstream headers.
	kiroClaudeOAuthClientRetryDeadline    = 80 * time.Second
	kiroClaudeOAuthNextAttemptMinBudget   = 35 * time.Second
	kiroClaudeOAuthClientRetryAfterHeader = "3"
)

func isKiroInitialResponseTimeoutMessage(message string) bool {
	return strings.HasPrefix(strings.TrimSpace(message), "kiro_initial_response_timeout")
}

func isKiroInitialResponseTimeoutFailover(err *service.UpstreamFailoverError) bool {
	return err != nil &&
		err.StatusCode == http.StatusGatewayTimeout &&
		isKiroInitialResponseTimeoutMessage(err.Message)
}

func shouldStopKiroOAuthInitialFailover(account *service.Account, err *service.UpstreamFailoverError, startedAt time.Time) bool {
	return shouldStopKiroOAuthInitialFailoverWithBudget(account, err, startedAt, kiroOAuthFailoverTotalBudget, kiroOAuthFailoverNextAttemptBuffer)
}

func shouldStopKiroClaudeOAuthInitialFailover(account *service.Account, err *service.UpstreamFailoverError, startedAt time.Time) bool {
	return shouldStopKiroOAuthInitialFailoverWithBudget(account, err, startedAt, kiroClaudeOAuthClientRetryDeadline, kiroClaudeOAuthNextAttemptMinBudget)
}

func shouldStopKiroOAuthInitialFailoverWithBudget(account *service.Account, err *service.UpstreamFailoverError, startedAt time.Time, totalBudget, nextAttemptMinBudget time.Duration) bool {
	if account == nil || !account.IsKiro() || account.IsKiroApiKey() {
		return false
	}
	if !isKiroInitialResponseTimeoutFailover(err) || startedAt.IsZero() || totalBudget <= 0 {
		return false
	}
	elapsed := time.Since(startedAt)
	return elapsed >= totalBudget ||
		totalBudget-elapsed < nextAttemptMinBudget
}

func shouldBypassKiroClaudeOAuthClientRetryForAck(account *service.Account, err *service.UpstreamFailoverError, isStream bool) bool {
	return isStream &&
		account != nil &&
		account.IsKiro() &&
		!account.IsKiroApiKey() &&
		isKiroInitialResponseTimeoutFailover(err)
}

// SetClaudeCodeClientContext 检查请求是否来自 Claude Code 客户端，并设置到 context 中
// 返回更新后的 context
func SetClaudeCodeClientContext(c *gin.Context, body []byte) {
	// 解析请求体为 map
	var bodyMap map[string]any
	if len(body) > 0 {
		_ = json.Unmarshal(body, &bodyMap)
	}

	// 验证是否为 Claude Code 客户端
	isClaudeCode := claudeCodeValidator.Validate(c.Request, bodyMap)

	// 更新 request context
	ctx := service.SetClaudeCodeClient(c.Request.Context(), isClaudeCode)
	c.Request = c.Request.WithContext(ctx)
}

func applyKiroOpus47GroupDowngrade(group *service.Group, requestedModel string, body []byte) (string, []byte, bool, error) {
	effectiveModel, downgraded := service.DowngradeKiroOpus47Model(group, requestedModel)
	if !downgraded {
		return requestedModel, body, false, nil
	}
	rewritten, err := replaceTopLevelJSONModel(body, effectiveModel)
	if err != nil {
		return requestedModel, body, false, err
	}
	return effectiveModel, rewritten, true, nil
}

// applyModelContextSuffix 剥掉模型名上的 "[1m]" 上下文标记，并把 1M 语义改用
// anthropic-beta header 表达。
//
// Claude Code CLI 会给派生请求（如 auto 模式的 Bash 安全分类器）带上这个后缀，
// 而带后缀的模型名不在任何账号的模型范围内，会被账号选择阶段判定为模型不可用。
// 归一化后基础模型名可正常参与调度与计费。
//
// beta header 直接改写到入站请求上，这样下游 OAuth 与 Kiro 两条链路各自读取
// c.GetHeader("anthropic-beta") 时都能拿到补齐后的值。
func applyModelContextSuffix(c *gin.Context, requestedModel string, body []byte) (string, []byte, bool, error) {
	baseModel, wants1M := claude.SplitModelContextSuffix(requestedModel)
	if !wants1M {
		return requestedModel, body, false, nil
	}
	rewritten, err := replaceTopLevelJSONModel(body, baseModel)
	if err != nil {
		return requestedModel, body, false, err
	}
	if c != nil && c.Request != nil {
		c.Request.Header.Set("anthropic-beta", claude.EnsureContext1MBeta(c.GetHeader("anthropic-beta")))
	}
	return baseModel, rewritten, true, nil
}

func replaceTopLevelJSONModel(body []byte, model string) ([]byte, error) {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("parse request body: %w", err)
	}
	modelBytes, err := json.Marshal(model)
	if err != nil {
		return nil, fmt.Errorf("serialize model: %w", err)
	}
	req["model"] = modelBytes
	newBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("serialize request body: %w", err)
	}
	return newBody, nil
}

// 并发槽位等待相关常量
//
// 性能优化说明：
// 原实现使用固定间隔（100ms）轮询并发槽位，存在以下问题：
// 1. 高并发时频繁轮询增加 Redis 压力
// 2. 固定间隔可能导致多个请求同时重试（惊群效应）
//
// 新实现使用指数退避 + 抖动算法：
// 1. 初始退避 100ms，每次乘以 1.5，最大 2s
// 2. 添加 ±20% 的随机抖动，分散重试时间点
// 3. 减少 Redis 压力，避免惊群效应
const (
	// maxConcurrencyWait 等待并发槽位的最大时间
	maxConcurrencyWait = 30 * time.Second
	// defaultPingInterval 流式响应等待时发送 ping 的默认间隔
	defaultPingInterval = 10 * time.Second
	// initialBackoff 初始退避时间
	initialBackoff = 100 * time.Millisecond
	// backoffMultiplier 退避时间乘数（指数退避）
	backoffMultiplier = 1.5
	// maxBackoff 最大退避时间
	maxBackoff = 2 * time.Second
)

// SSEPingFormat defines the format of SSE ping events for different platforms
type SSEPingFormat string

const (
	// SSEPingFormatClaude is the Claude/Anthropic SSE ping format
	SSEPingFormatClaude SSEPingFormat = "data: {\"type\": \"ping\"}\n\n"
	// SSEPingFormatNone indicates no ping should be sent (e.g., OpenAI has no ping spec)
	SSEPingFormatNone SSEPingFormat = ""
	// SSEPingFormatComment is an SSE comment ping for OpenAI/Codex CLI clients
	SSEPingFormatComment SSEPingFormat = ":\n\n"
)

// ConcurrencyError represents a concurrency limit error with context
type ConcurrencyError struct {
	SlotType  string
	IsTimeout bool
}

func (e *ConcurrencyError) Error() string {
	if e.IsTimeout {
		return fmt.Sprintf("timeout waiting for %s concurrency slot", e.SlotType)
	}
	return fmt.Sprintf("%s concurrency limit reached", e.SlotType)
}

// ConcurrencyHelper provides common concurrency slot management for gateway handlers
type ConcurrencyHelper struct {
	concurrencyService *service.ConcurrencyService
	pingFormat         SSEPingFormat
	pingInterval       time.Duration
}

// NewConcurrencyHelper creates a new ConcurrencyHelper
func NewConcurrencyHelper(concurrencyService *service.ConcurrencyService, pingFormat SSEPingFormat, pingInterval time.Duration) *ConcurrencyHelper {
	if pingInterval <= 0 {
		pingInterval = defaultPingInterval
	}
	return &ConcurrencyHelper{
		concurrencyService: concurrencyService,
		pingFormat:         pingFormat,
		pingInterval:       pingInterval,
	}
}

// wrapReleaseOnDone ensures release runs at most once and still triggers on context cancellation.
// 用于避免客户端断开或上游超时导致的并发槽位泄漏。
// 修复：添加 quit channel 确保 goroutine 及时退出，避免泄露
func wrapReleaseOnDone(ctx context.Context, releaseFunc func()) func() {
	if releaseFunc == nil {
		return nil
	}
	var once sync.Once
	quit := make(chan struct{})

	release := func() {
		once.Do(func() {
			releaseFunc()
			close(quit) // 通知监听 goroutine 退出
		})
	}

	go func() {
		select {
		case <-ctx.Done():
			// Context 取消时释放资源
			release()
		case <-quit:
			// 正常释放已完成，goroutine 退出
			return
		}
	}()

	return release
}

// IncrementWaitCount increments the wait count for a user
func (h *ConcurrencyHelper) IncrementWaitCount(ctx context.Context, userID int64, maxWait int) (bool, error) {
	return h.concurrencyService.IncrementWaitCount(ctx, userID, maxWait)
}

// DecrementWaitCount decrements the wait count for a user
func (h *ConcurrencyHelper) DecrementWaitCount(ctx context.Context, userID int64) {
	h.concurrencyService.DecrementWaitCount(ctx, userID)
}

// IncrementAccountWaitCount increments the wait count for an account
func (h *ConcurrencyHelper) IncrementAccountWaitCount(ctx context.Context, accountID int64, maxWait int) (bool, error) {
	return h.concurrencyService.IncrementAccountWaitCount(ctx, accountID, maxWait)
}

// DecrementAccountWaitCount decrements the wait count for an account
func (h *ConcurrencyHelper) DecrementAccountWaitCount(ctx context.Context, accountID int64) {
	h.concurrencyService.DecrementAccountWaitCount(ctx, accountID)
}

// AcquireUserSlotWithWait acquires a user concurrency slot, waiting if necessary.
// For streaming requests, sends ping events during the wait.
// streamStarted is updated if streaming response has begun.
func (h *ConcurrencyHelper) AcquireUserSlotWithWait(c *gin.Context, userID int64, maxConcurrency int, isStream bool, streamStarted *bool) (func(), error) {
	ctx := c.Request.Context()

	// Try to acquire immediately
	result, err := h.concurrencyService.AcquireUserSlot(ctx, userID, maxConcurrency)
	if err != nil {
		return nil, err
	}

	if result.Acquired {
		return result.ReleaseFunc, nil
	}

	// Need to wait - handle streaming ping if needed
	return h.waitForSlotWithPing(c, "user", userID, maxConcurrency, isStream, streamStarted)
}

// AcquireAccountSlotWithWait acquires an account concurrency slot, waiting if necessary.
// For streaming requests, sends ping events during the wait.
// streamStarted is updated if streaming response has begun.
func (h *ConcurrencyHelper) AcquireAccountSlotWithWait(c *gin.Context, accountID int64, maxConcurrency int, isStream bool, streamStarted *bool) (func(), error) {
	ctx := c.Request.Context()

	// Try to acquire immediately
	result, err := h.concurrencyService.AcquireAccountSlot(ctx, accountID, maxConcurrency)
	if err != nil {
		return nil, err
	}

	if result.Acquired {
		return result.ReleaseFunc, nil
	}

	// Need to wait - handle streaming ping if needed
	return h.waitForSlotWithPing(c, "account", accountID, maxConcurrency, isStream, streamStarted)
}

// waitForSlotWithPing waits for a concurrency slot, sending ping events for streaming requests.
// streamStarted pointer is updated when streaming begins (for proper error handling by caller).
func (h *ConcurrencyHelper) waitForSlotWithPing(c *gin.Context, slotType string, id int64, maxConcurrency int, isStream bool, streamStarted *bool) (func(), error) {
	return h.waitForSlotWithPingTimeout(c, slotType, id, maxConcurrency, maxConcurrencyWait, isStream, streamStarted)
}

// waitForSlotWithPingTimeout waits for a concurrency slot with a custom timeout.
func (h *ConcurrencyHelper) waitForSlotWithPingTimeout(c *gin.Context, slotType string, id int64, maxConcurrency int, timeout time.Duration, isStream bool, streamStarted *bool) (func(), error) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
	defer cancel()

	// Try immediate acquire first (avoid unnecessary wait)
	var result *service.AcquireResult
	var err error
	if slotType == "user" {
		result, err = h.concurrencyService.AcquireUserSlot(ctx, id, maxConcurrency)
	} else {
		result, err = h.concurrencyService.AcquireAccountSlot(ctx, id, maxConcurrency)
	}
	if err != nil {
		return nil, err
	}
	if result.Acquired {
		return result.ReleaseFunc, nil
	}

	// Determine if ping is needed (streaming + ping format defined)
	needPing := isStream && h.pingFormat != ""

	var flusher http.Flusher
	if needPing {
		var ok bool
		flusher, ok = c.Writer.(http.Flusher)
		if !ok {
			return nil, fmt.Errorf("streaming not supported")
		}
	}

	// Only create ping ticker if ping is needed
	var pingCh <-chan time.Time
	if needPing {
		pingTicker := time.NewTicker(h.pingInterval)
		defer pingTicker.Stop()
		pingCh = pingTicker.C
	}

	backoff := initialBackoff
	timer := time.NewTimer(backoff)
	defer timer.Stop()
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	for {
		select {
		case <-ctx.Done():
			return nil, &ConcurrencyError{
				SlotType:  slotType,
				IsTimeout: true,
			}

		case <-pingCh:
			// Send ping to keep connection alive
			if !*streamStarted {
				c.Header("Content-Type", "text/event-stream")
				c.Header("Cache-Control", "no-cache")
				c.Header("Connection", "keep-alive")
				c.Header("X-Accel-Buffering", "no")
				*streamStarted = true
			}
			if _, err := fmt.Fprint(c.Writer, string(h.pingFormat)); err != nil {
				return nil, err
			}
			flusher.Flush()

		case <-timer.C:
			// Try to acquire slot
			var result *service.AcquireResult
			var err error

			if slotType == "user" {
				result, err = h.concurrencyService.AcquireUserSlot(ctx, id, maxConcurrency)
			} else {
				result, err = h.concurrencyService.AcquireAccountSlot(ctx, id, maxConcurrency)
			}

			if err != nil {
				return nil, err
			}

			if result.Acquired {
				return result.ReleaseFunc, nil
			}
			backoff = nextBackoff(backoff, rng)
			timer.Reset(backoff)
		}
	}
}

// AcquireAccountSlotWithWaitTimeout acquires an account slot with a custom timeout (keeps SSE ping).
func (h *ConcurrencyHelper) AcquireAccountSlotWithWaitTimeout(c *gin.Context, accountID int64, maxConcurrency int, timeout time.Duration, isStream bool, streamStarted *bool) (func(), error) {
	return h.waitForSlotWithPingTimeout(c, "account", accountID, maxConcurrency, timeout, isStream, streamStarted)
}

// nextBackoff 计算下一次退避时间
// 性能优化：使用指数退避 + 随机抖动，避免惊群效应
// current: 当前退避时间
// rng: 随机数生成器（可为 nil，此时不添加抖动）
// 返回值：下一次退避时间（100ms ~ 2s 之间）
func nextBackoff(current time.Duration, rng *rand.Rand) time.Duration {
	// 指数退避：当前时间 * 1.5
	next := time.Duration(float64(current) * backoffMultiplier)
	if next > maxBackoff {
		next = maxBackoff
	}
	if rng == nil {
		return next
	}
	// 添加 ±20% 的随机抖动（jitter 范围 0.8 ~ 1.2）
	// 抖动可以分散多个请求的重试时间点，避免同时冲击 Redis
	jitter := 0.8 + rng.Float64()*0.4
	jittered := time.Duration(float64(next) * jitter)
	if jittered < initialBackoff {
		return initialBackoff
	}
	if jittered > maxBackoff {
		return maxBackoff
	}
	return jittered
}
