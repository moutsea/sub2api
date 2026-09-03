package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	pkgerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// GatewayHandler handles API gateway requests
type GatewayHandler struct {
	gatewayService            *service.GatewayService
	geminiCompatService       *service.GeminiMessagesCompatService
	antigravityGatewayService *service.AntigravityGatewayService
	kiroGatewayService        *service.KiroGatewayService
	openAIGatewayService      *service.OpenAIGatewayService
	userService               *service.UserService
	billingCacheService       *service.BillingCacheService
	concurrencyHelper         *ConcurrencyHelper
}

// NewGatewayHandler creates a new GatewayHandler
func NewGatewayHandler(
	gatewayService *service.GatewayService,
	geminiCompatService *service.GeminiMessagesCompatService,
	antigravityGatewayService *service.AntigravityGatewayService,
	kiroGatewayService *service.KiroGatewayService,
	openAIGatewayService *service.OpenAIGatewayService,
	userService *service.UserService,
	concurrencyService *service.ConcurrencyService,
	billingCacheService *service.BillingCacheService,
	cfg *config.Config,
) *GatewayHandler {
	pingInterval := time.Duration(0)
	if cfg != nil {
		pingInterval = time.Duration(cfg.Concurrency.PingInterval) * time.Second
	}
	return &GatewayHandler{
		gatewayService:            gatewayService,
		geminiCompatService:       geminiCompatService,
		antigravityGatewayService: antigravityGatewayService,
		kiroGatewayService:        kiroGatewayService,
		openAIGatewayService:      openAIGatewayService,
		userService:               userService,
		billingCacheService:       billingCacheService,
		concurrencyHelper:         NewConcurrencyHelper(concurrencyService, SSEPingFormatClaude, pingInterval),
	}
}

// Messages handles Claude API compatible messages endpoint
// POST /v1/messages
func (h *GatewayHandler) Messages(c *gin.Context) {
	// 从context获取apiKey和user（ApiKeyAuth中间件已设置）
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}

	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}

	// 读取请求体
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		if maxErr, ok := extractMaxBytesError(err); ok {
			h.errorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
			return
		}
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}

	if len(body) == 0 {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}

	// 检查是否为 Claude Code 客户端，设置到 context 中
	SetClaudeCodeClientContext(c, body)

	setOpsRequestContext(c, "", false, body)

	parsedReq, err := service.ParseGatewayRequest(body)
	if err != nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}
	reqModel := parsedReq.Model
	reqStream := parsedReq.Stream

	setOpsRequestContext(c, reqModel, reqStream, body)

	// 验证 model 必填
	if reqModel == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}

	// 归一化模型名上的 "[1m]" 上下文标记，必须early于任何模型范围校验。
	if effectiveModel, effectiveBody, normalized, rewriteErr := applyModelContextSuffix(c, reqModel, body); rewriteErr != nil {
		log.Printf("[Gateway] model_context_suffix rewrite failed model=%s error=%v", reqModel, rewriteErr)
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "failed to rewrite model context suffix")
		return
	} else if normalized {
		log.Printf("[Gateway] model_context_suffix model=%s -> %s (context-1m beta)", reqModel, effectiveModel)
		reqModel = effectiveModel
		parsedReq.Model = effectiveModel
		body = effectiveBody
		// Kiro/Antigravity 链路用局部 body 转发，OAuth 链路用 parsedReq.Body，两者都要同步。
		parsedReq.Body = effectiveBody
		setOpsRequestContext(c, reqModel, reqStream, body)
	}

	// 拦截：openai 分组不支持 claude 系列模型
	if isOpenAIGroupClaudeModelMismatch(apiKey.Group, reqModel) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("Model %q is not supported by the OpenAI group. Claude models require an Anthropic group. Please use an OpenAI-compatible model (e.g. gpt-5.5, gpt-5.4) or switch the API key group.", reqModel))
		return
	}

	if effectiveModel, effectiveBody, downgraded, rewriteErr := applyKiroOpus47GroupDowngrade(apiKey.Group, reqModel, body); rewriteErr != nil {
		log.Printf("[Gateway] kiro_opus_47_downgrade rewrite failed group_id=%d model=%s error=%v", apiKey.Group.ID, reqModel, rewriteErr)
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "failed to rewrite downgraded model request")
		return
	} else if downgraded {
		log.Printf("[Gateway] kiro_opus_47_downgrade group_id=%d model=%s -> %s", apiKey.Group.ID, reqModel, effectiveModel)
		reqModel = effectiveModel
		parsedReq.Model = effectiveModel
		body = effectiveBody
		setOpsRequestContext(c, reqModel, reqStream, body)
	}

	// Track if we've started streaming (for error handling)
	streamStarted := false
	requestStartedAt := time.Now()

	// 获取订阅信息（可能为nil）- 提前获取用于后续检查
	subscription, _ := middleware2.GetSubscriptionFromContext(c)

	// 0. 检查wait队列是否已满
	maxWait := service.CalculateMaxWait(subject.Concurrency)
	canWait, err := h.concurrencyHelper.IncrementWaitCount(c.Request.Context(), subject.UserID, maxWait)
	waitCounted := false
	if err != nil {
		log.Printf("Increment wait count failed: %v", err)
		// On error, allow request to proceed
	} else if !canWait {
		h.errorResponse(c, http.StatusTooManyRequests, "rate_limit_error", "Too many pending requests, please retry later")
		return
	}
	if err == nil && canWait {
		waitCounted = true
	}
	// Ensure we decrement if we exit before acquiring the user slot.
	defer func() {
		if waitCounted {
			h.concurrencyHelper.DecrementWaitCount(c.Request.Context(), subject.UserID)
		}
	}()

	// 1. 首先获取用户并发槽位
	userReleaseFunc, err := h.concurrencyHelper.AcquireUserSlotWithWait(c, subject.UserID, subject.Concurrency, reqStream, &streamStarted)
	if err != nil {
		log.Printf("User concurrency acquire failed: %v", err)
		h.handleConcurrencyError(c, err, "user", streamStarted)
		return
	}
	// User slot acquired: no longer waiting in the queue.
	if waitCounted {
		h.concurrencyHelper.DecrementWaitCount(c.Request.Context(), subject.UserID)
		waitCounted = false
	}
	// 在请求结束或 Context 取消时确保释放槽位，避免客户端断开造成泄漏
	userReleaseFunc = wrapReleaseOnDone(c.Request.Context(), userReleaseFunc)
	if userReleaseFunc != nil {
		defer userReleaseFunc()
	}

	// 2. 【新增】Wait后二次检查余额/订阅
	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription); err != nil {
		log.Printf("Billing eligibility check failed after wait: %v", err)
		status, code, message := billingErrorDetails(err)
		h.handleStreamingAwareError(c, status, code, message, streamStarted)
		return
	}

	// 计算粘性会话hash（优先使用 X-Conversation-ID header）
	conversationID := c.GetHeader("X-Conversation-ID")
	sessionHash := h.gatewayService.GenerateSessionHash(parsedReq, conversationID)
	// 兜底：当请求中无任何可识别的会话标识时，基于客户端特征生成稳定 ID
	if sessionHash == "" {
		sessionHash = h.gatewayService.HashContent(kiro.GenerateStableConversationID(c))
	}

	// 获取平台：优先使用强制平台（/antigravity 路由，中间件已设置 request.Context），否则使用分组平台
	platform := ""
	if forcePlatform, ok := middleware2.GetForcePlatformFromContext(c); ok {
		platform = forcePlatform
	} else if apiKey.Group != nil {
		platform = apiKey.Group.Platform
	}
	sessionKey := sessionHash
	if platform == service.PlatformGemini && sessionHash != "" {
		sessionKey = "gemini:" + sessionHash
	}
	selectionCtx := c.Request.Context()
	if platform == service.PlatformGrok {
		selectionCtx = service.WithOpenAIRequestPlatform(selectionCtx, service.PlatformGrok)
	}

	if platform == service.PlatformGemini {
		const maxAccountSwitches = 3
		switchCount := 0
		failedAccountIDs := make(map[int64]struct{})
		lastFailoverStatus := 0
		lastFailoverMsg := ""

		for {
			selection, err := h.gatewayService.SelectAccountWithLoadAwareness(c.Request.Context(), apiKey.GroupID, sessionKey, reqModel, failedAccountIDs, "") // Gemini 不使用会话限制
			if err != nil {
				if len(failedAccountIDs) == 0 {
					if isModelNotSupportedErr(err) {
						markOpsModelMismatch(c)
						h.handleStreamingAwareError(c, http.StatusBadRequest, "invalid_request_error", modelNotSupportedClientMessage(reqModel), streamStarted)
						return
					}
					h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", "No available accounts: "+err.Error(), streamStarted)
					return
				}
				h.handleFailoverExhausted(c, lastFailoverStatus, lastFailoverMsg, streamStarted)
				return
			}
			account := selection.Account
			setOpsSelectedAccount(c, account.ID)

			// 检查预热请求拦截（在账号选择后、转发前检查）
			if account.IsInterceptWarmupEnabled() && isWarmupRequest(body) {
				if selection.Acquired && selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
				if reqStream {
					sendMockWarmupStream(c, reqModel)
				} else {
					sendMockWarmupResponse(c, reqModel)
				}
				return
			}

			// 3. 获取账号并发槽位
			accountReleaseFunc := selection.ReleaseFunc
			if !selection.Acquired {
				if selection.WaitPlan == nil {
					h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", "No available accounts", streamStarted)
					return
				}
				accountWaitCounted := false
				canWait, err := h.concurrencyHelper.IncrementAccountWaitCount(c.Request.Context(), account.ID, selection.WaitPlan.MaxWaiting)
				if err != nil {
					log.Printf("Increment account wait count failed: %v", err)
				} else if !canWait {
					log.Printf("Account wait queue full: account=%d", account.ID)
					h.handleStreamingAwareError(c, http.StatusTooManyRequests, "rate_limit_error", "Too many pending requests, please retry later", streamStarted)
					return
				}
				if err == nil && canWait {
					accountWaitCounted = true
				}
				// Ensure the wait counter is decremented if we exit before acquiring the slot.
				defer func() {
					if accountWaitCounted {
						h.concurrencyHelper.DecrementAccountWaitCount(c.Request.Context(), account.ID)
					}
				}()

				accountReleaseFunc, err = h.concurrencyHelper.AcquireAccountSlotWithWaitTimeout(
					c,
					account.ID,
					selection.WaitPlan.MaxConcurrency,
					selection.WaitPlan.Timeout,
					reqStream,
					&streamStarted,
				)
				if err != nil {
					log.Printf("Account concurrency acquire failed: %v", err)
					h.handleConcurrencyError(c, err, "account", streamStarted)
					return
				}
				// Slot acquired: no longer waiting in queue.
				if accountWaitCounted {
					h.concurrencyHelper.DecrementAccountWaitCount(c.Request.Context(), account.ID)
					accountWaitCounted = false
				}
				if err := h.gatewayService.BindStickySession(c.Request.Context(), apiKey.GroupID, sessionKey, account.ID); err != nil {
					log.Printf("Bind sticky session failed: %v", err)
				}
			}
			// 账号槽位/等待计数需要在超时或断开时安全回收
			accountReleaseFunc = wrapReleaseOnDone(c.Request.Context(), accountReleaseFunc)

			// 转发请求 - 根据账号平台分流
			var result *service.ForwardResult
			if account.Platform == service.PlatformAntigravity {
				result, err = h.antigravityGatewayService.ForwardGemini(c.Request.Context(), c, account, reqModel, "generateContent", reqStream, body)
			} else {
				result, err = h.geminiCompatService.Forward(c.Request.Context(), c, account, body)
			}
			if accountReleaseFunc != nil {
				accountReleaseFunc()
			}
			if err != nil {
				var failoverErr *service.UpstreamFailoverError
				if errors.As(err, &failoverErr) {
					failedAccountIDs[account.ID] = struct{}{}
					lastFailoverStatus = failoverErr.StatusCode
					lastFailoverMsg = failoverErr.Message
					if switchCount >= maxAccountSwitches {
						h.handleFailoverExhausted(c, lastFailoverStatus, lastFailoverMsg, streamStarted)
						return
					}
					switchCount++
					log.Printf("Account %d: upstream error %d, switching account %d/%d", account.ID, failoverErr.StatusCode, switchCount, maxAccountSwitches)
					continue
				}
				// 错误响应已在Forward中处理，这里只记录日志
				log.Printf("Forward request failed: %v", err)
				return
			}

			// 捕获请求信息（用于异步记录，避免在 goroutine 中访问 gin.Context）
			userAgent := c.GetHeader("User-Agent")
			clientIP := ip.GetClientIP(c)
			var tempAPIKeyID *int64
			var tempAPIKey *service.TempAPIKey
			if tempKey, ok := middleware2.GetTempAPIKeyFromContext(c); ok && tempKey != nil {
				tempAPIKeyID = new(int64)
				*tempAPIKeyID = tempKey.ID
				tempAPIKey = tempKey
			}

			// 异步记录使用量（subscription已在函数开头获取）
			go func(result *service.ForwardResult, usedAccount *service.Account, ua, clientIP string, tempKeyID *int64, tempKey *service.TempAPIKey) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if err := h.gatewayService.RecordUsage(ctx, &service.RecordUsageInput{
					Result:       result,
					APIKey:       apiKey,
					User:         apiKey.User,
					Account:      usedAccount,
					Subscription: subscription,
					UserAgent:    ua,
					IPAddress:    clientIP,
					TempAPIKeyID: tempKeyID,
					TempAPIKey:   tempKey,
				}); err != nil {
					log.Printf("Record usage failed: %v", err)
				}
			}(result, account, userAgent, clientIP, tempAPIKeyID, tempAPIKey)
			return
		}
	}

	const maxAccountSwitches = 10
	const maxBadRequestSwitches = 3 // 400 errors are likely deterministic; limit retries
	switchCount := 0
	badRequestSwitchCount := 0
	failedAccountIDs := make(map[int64]struct{})
	lastFailoverStatus := 0
	lastFailoverMsg := ""

	for {
		// 选择支持该模型的账号
		selection, err := h.gatewayService.SelectAccountWithLoadAwareness(selectionCtx, apiKey.GroupID, sessionKey, reqModel, failedAccountIDs, parsedReq.MetadataUserID)
		if err != nil {
			if len(failedAccountIDs) == 0 {
				if isModelNotSupportedErr(err) {
					markOpsModelMismatch(c)
					h.handleStreamingAwareError(c, http.StatusBadRequest, "invalid_request_error", modelNotSupportedClientMessage(reqModel), streamStarted)
					return
				}
				h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", "No available accounts: "+err.Error(), streamStarted)
				return
			}
			h.handleFailoverExhausted(c, lastFailoverStatus, lastFailoverMsg, streamStarted)
			return
		}
		account := selection.Account
		setOpsSelectedAccount(c, account.ID)

		// 检查预热请求拦截（在账号选择后、转发前检查）
		if account.IsInterceptWarmupEnabled() && isWarmupRequest(body) {
			if selection.Acquired && selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			if reqStream {
				sendMockWarmupStream(c, reqModel)
			} else {
				sendMockWarmupResponse(c, reqModel)
			}
			return
		}

		// 3. 获取账号并发槽位
		accountReleaseFunc := selection.ReleaseFunc
		if !selection.Acquired {
			if selection.WaitPlan == nil {
				h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", "No available accounts", streamStarted)
				return
			}
			accountWaitCounted := false
			canWait, err := h.concurrencyHelper.IncrementAccountWaitCount(c.Request.Context(), account.ID, selection.WaitPlan.MaxWaiting)
			if err != nil {
				log.Printf("Increment account wait count failed: %v", err)
			} else if !canWait {
				log.Printf("Account wait queue full: account=%d", account.ID)
				h.handleStreamingAwareError(c, http.StatusTooManyRequests, "rate_limit_error", "Too many pending requests, please retry later", streamStarted)
				return
			}
			if err == nil && canWait {
				accountWaitCounted = true
			}
			defer func() {
				if accountWaitCounted {
					h.concurrencyHelper.DecrementAccountWaitCount(c.Request.Context(), account.ID)
				}
			}()

			accountReleaseFunc, err = h.concurrencyHelper.AcquireAccountSlotWithWaitTimeout(
				c,
				account.ID,
				selection.WaitPlan.MaxConcurrency,
				selection.WaitPlan.Timeout,
				reqStream,
				&streamStarted,
			)
			if err != nil {
				log.Printf("Account concurrency acquire failed: %v", err)
				h.handleConcurrencyError(c, err, "account", streamStarted)
				return
			}
			if accountWaitCounted {
				h.concurrencyHelper.DecrementAccountWaitCount(c.Request.Context(), account.ID)
				accountWaitCounted = false
			}
			if err := h.gatewayService.BindStickySession(c.Request.Context(), apiKey.GroupID, sessionKey, account.ID); err != nil {
				log.Printf("Bind sticky session failed: %v", err)
			}
		}
		// 账号槽位/等待计数需要在超时或断开时安全回收
		accountReleaseFunc = wrapReleaseOnDone(c.Request.Context(), accountReleaseFunc)

		// 转发请求 - 根据账号平台分流
		var result *service.ForwardResult
		switch account.Platform {
		case service.PlatformAntigravity:
			result, err = h.antigravityGatewayService.Forward(c.Request.Context(), c, account, body)
		case service.PlatformKiro:
			// Parse Claude request for web_search detection
			claudeReq, parseErr := service.ParseClaudeRequestFromJSON(body)
			if parseErr != nil {
				result, err = h.kiroGatewayService.Forward(c.Request.Context(), c, account, body)
			} else {
				// Use ForwardWithWebSearch for web_search agentic loop support
				result, err = h.kiroGatewayService.ForwardWithWebSearch(c.Request.Context(), c, account, body, claudeReq)
			}
		case service.PlatformOpenAI:
			result, err = h.openAIGatewayService.ForwardAsClaudeMessages(c.Request.Context(), c, account, body)
		case service.PlatformGrok:
			result, err = h.openAIGatewayService.ForwardAsClaudeMessages(c.Request.Context(), c, account, body)
		default:
			result, err = h.gatewayService.Forward(c.Request.Context(), c, account, parsedReq)
		}
		if accountReleaseFunc != nil {
			accountReleaseFunc()
		}
		if err != nil {
			var failoverErr *service.UpstreamFailoverError
			if errors.As(err, &failoverErr) {
				failedAccountIDs[account.ID] = struct{}{}
				lastFailoverStatus = failoverErr.StatusCode
				lastFailoverMsg = failoverErr.Message
				if !shouldBypassKiroClaudeOAuthClientRetryForAck(account, failoverErr, reqStream) &&
					shouldStopKiroClaudeOAuthInitialFailover(account, failoverErr, requestStartedAt) {
					h.gatewayService.InvalidateStickySession(c.Request.Context(), apiKey.GroupID, sessionKey)
					log.Printf("Account %d: kiro initial response failover budget exhausted after %s", account.ID, time.Since(requestStartedAt))
					h.handleFailoverExhausted(c, lastFailoverStatus, lastFailoverMsg, streamStarted)
					return
				}
				// 400 errors: stricter retry limit (likely deterministic, switching rarely helps)
				if failoverErr.StatusCode == http.StatusBadRequest {
					badRequestSwitchCount++
				}
				if switchCount >= maxAccountSwitches || badRequestSwitchCount >= maxBadRequestSwitches {
					h.handleFailoverExhausted(c, lastFailoverStatus, lastFailoverMsg, streamStarted)
					return
				}
				switchCount++
				log.Printf("Account %d: upstream error %d, switching account %d/%d", account.ID, failoverErr.StatusCode, switchCount, maxAccountSwitches)
				continue
			}
			// Handle context too long error - return error to client without failover
			var contextErr *service.ContextTooLongError
			if errors.As(err, &contextErr) {
				log.Printf("Account %d: context too long (estimated %d tokens, limit %d)", account.ID, contextErr.EstimatedTokens, contextErr.Limit)
				h.errorResponse(c, http.StatusBadRequest, "invalid_request_error",
					"Input context too long. Please reduce context length.")
				return
			}
			// 错误响应已在Forward中处理，这里只记录日志
			log.Printf("Account %d: Forward request failed: %v", account.ID, err)
			return
		}

		// 捕获请求信息（用于异步记录，避免在 goroutine 中访问 gin.Context）
		userAgent := c.GetHeader("User-Agent")
		clientIP := ip.GetClientIP(c)
		var tempAPIKeyID *int64
		var tempAPIKey *service.TempAPIKey
		if tempKey, ok := middleware2.GetTempAPIKeyFromContext(c); ok && tempKey != nil {
			tempAPIKeyID = new(int64)
			*tempAPIKeyID = tempKey.ID
			tempAPIKey = tempKey
		}

		// 异步记录使用量（subscription已在函数开头获取）
		go func(result *service.ForwardResult, usedAccount *service.Account, ua, clientIP string, tempKeyID *int64, tempKey *service.TempAPIKey) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := h.gatewayService.RecordUsage(ctx, &service.RecordUsageInput{
				Result:       result,
				APIKey:       apiKey,
				User:         apiKey.User,
				Account:      usedAccount,
				Subscription: subscription,
				UserAgent:    ua,
				IPAddress:    clientIP,
				TempAPIKeyID: tempKeyID,
				TempAPIKey:   tempKey,
			}); err != nil {
				log.Printf("Record usage failed: %v", err)
			}
		}(result, account, userAgent, clientIP, tempAPIKeyID, tempAPIKey)

		// 对 Antigravity 账号，请求完成后异步刷新配额并更新健康状态
		if account.Platform == service.PlatformAntigravity {
			h.gatewayService.RefreshAntigravityQuotaAsync(account.ID)
		}
		return
	}
}

// Models handles listing available models
// GET /v1/models
// Returns models based on account configurations (model_mapping whitelist)
// Falls back to default models if no whitelist is configured
func (h *GatewayHandler) Models(c *gin.Context) {
	apiKey, _ := middleware2.GetAPIKeyFromContext(c)

	var groupID *int64
	var platform string

	if apiKey != nil && apiKey.Group != nil {
		groupID = &apiKey.Group.ID
		platform = apiKey.Group.Platform
	}

	// Grok shares the OpenAI-compatible endpoint but has a separate model
	// catalog. Preserve the historical mixed OpenAI/Kiro list for other groups.
	modelPlatform := ""
	if platform == service.PlatformGrok {
		modelPlatform = platform
	}
	availableModels := h.gatewayService.GetAvailableModels(c.Request.Context(), groupID, modelPlatform)

	if len(availableModels) > 0 {
		// Build model list from whitelist
		models := make([]claude.Model, 0, len(availableModels))
		for _, modelID := range availableModels {
			models = append(models, claude.Model{
				ID:          modelID,
				Type:        "model",
				DisplayName: modelID,
				CreatedAt:   "2024-01-01T00:00:00Z",
			})
		}
		c.JSON(http.StatusOK, gin.H{
			"object": "list",
			"data":   models,
		})
		return
	}

	// Fallback to default models
	if platform == service.PlatformOpenAI {
		c.JSON(http.StatusOK, gin.H{
			"object": "list",
			"data":   openai.DefaultModels,
		})
		return
	}

	if platform == service.PlatformGrok {
		c.JSON(http.StatusOK, gin.H{"object": "list", "data": xai.DefaultModels()})
		return
	}

	if platform == service.PlatformGemini {
		c.JSON(http.StatusOK, gin.H{
			"object": "list",
			"data":   geminicli.DefaultModels,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   claude.DefaultModels,
	})
}

// AntigravityModels 返回 Antigravity 支持的全部模型
// GET /antigravity/models
func (h *GatewayHandler) AntigravityModels(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   antigravity.DefaultModels(),
	})
}

// Usage handles getting account balance for CC Switch integration
// GET /v1/usage
func (h *GatewayHandler) Usage(c *gin.Context) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}

	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}

	// 订阅模式：返回订阅限额信息
	if apiKey.Group != nil && apiKey.Group.IsSubscriptionType() {
		subscription, ok := middleware2.GetSubscriptionFromContext(c)
		if !ok {
			h.errorResponse(c, http.StatusForbidden, "subscription_error", "No active subscription")
			return
		}

		remaining := h.calculateSubscriptionRemaining(apiKey.Group, subscription)
		c.JSON(http.StatusOK, gin.H{
			"isValid":   true,
			"planName":  apiKey.Group.Name,
			"remaining": remaining,
			"unit":      "USD",
		})
		return
	}

	// 余额模式：返回钱包余额
	latestUser, err := h.userService.GetByID(c.Request.Context(), subject.UserID)
	if err != nil {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "Failed to get user info")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"isValid":   true,
		"planName":  "钱包余额",
		"remaining": latestUser.Balance,
		"unit":      "USD",
	})
}

// calculateSubscriptionRemaining 计算订阅剩余可用额度
// 逻辑：
// 1. 如果日/周/月任一限额达到100%，返回0
// 2. 否则返回所有已配置周期中剩余额度的最小值
func (h *GatewayHandler) calculateSubscriptionRemaining(group *service.Group, sub *service.UserSubscription) float64 {
	var remainingValues []float64

	// 检查日限额
	if group.HasDailyLimit() {
		remaining := *group.DailyLimitUSD - sub.DailyUsageUSD
		if remaining <= 0 {
			return 0
		}
		remainingValues = append(remainingValues, remaining)
	}

	// 检查周限额
	if group.HasWeeklyLimit() {
		remaining := *group.WeeklyLimitUSD - sub.WeeklyUsageUSD
		if remaining <= 0 {
			return 0
		}
		remainingValues = append(remainingValues, remaining)
	}

	// 检查月限额
	if group.HasMonthlyLimit() {
		remaining := *group.MonthlyLimitUSD - sub.MonthlyUsageUSD
		if remaining <= 0 {
			return 0
		}
		remainingValues = append(remainingValues, remaining)
	}

	// 如果没有配置任何限额，返回-1表示无限制
	if len(remainingValues) == 0 {
		return -1
	}

	// 返回最小值
	min := remainingValues[0]
	for _, v := range remainingValues[1:] {
		if v < min {
			min = v
		}
	}
	return min
}

// handleConcurrencyError handles concurrency-related errors with proper 429 response
func (h *GatewayHandler) handleConcurrencyError(c *gin.Context, err error, slotType string, streamStarted bool) {
	h.handleStreamingAwareError(c, http.StatusTooManyRequests, "rate_limit_error",
		fmt.Sprintf("Concurrency limit exceeded for %s, please retry later", slotType), streamStarted)
}

func (h *GatewayHandler) handleFailoverExhausted(c *gin.Context, statusCode int, lastMessage string, streamStarted bool) {
	status, errType, errMsg := h.mapUpstreamError(statusCode, lastMessage)
	if !streamStarted && isKiroInitialResponseTimeoutMessage(lastMessage) {
		c.Header("Retry-After", kiroClaudeOAuthClientRetryAfterHeader)
	}
	h.handleStreamingAwareError(c, status, errType, errMsg, streamStarted)
}

func (h *GatewayHandler) mapUpstreamError(statusCode int, lastMessage string) (int, string, string) {
	switch statusCode {
	case 400:
		msg := "Invalid request"
		if lastMessage != "" {
			msg = "Invalid request: " + lastMessage
		}
		return http.StatusBadRequest, "invalid_request_error", msg
	case 401:
		return http.StatusBadGateway, "upstream_error", "Upstream authentication failed, please contact administrator"
	case 403:
		return http.StatusBadGateway, "upstream_error", "Upstream access forbidden, please contact administrator"
	case 429:
		return http.StatusTooManyRequests, "rate_limit_error", "Upstream rate limit exceeded, please retry later"
	case 529:
		return http.StatusServiceUnavailable, "overloaded_error", "Upstream service overloaded, please retry later"
	case 504:
		if isKiroInitialResponseTimeoutMessage(lastMessage) {
			return http.StatusTooManyRequests, "rate_limit_error", "Kiro upstream initial response timeout, please retry later"
		}
		return http.StatusBadGateway, "upstream_error", "Upstream service temporarily unavailable"
	case 500, 502, 503:
		return http.StatusBadGateway, "upstream_error", "Upstream service temporarily unavailable"
	default:
		return http.StatusBadGateway, "upstream_error", "Upstream request failed"
	}
}

// handleStreamingAwareError handles errors that may occur after streaming has started
func (h *GatewayHandler) handleStreamingAwareError(c *gin.Context, status int, errType, message string, streamStarted bool) {
	if streamStarted {
		// Stream already started, send error as SSE event then close
		flusher, ok := c.Writer.(http.Flusher)
		if ok {
			// Send error event in SSE format with proper JSON marshaling
			errorData := map[string]any{
				"type": "error",
				"error": map[string]string{
					"type":    errType,
					"message": message,
				},
			}
			jsonBytes, err := json.Marshal(errorData)
			if err != nil {
				_ = c.Error(err)
				return
			}
			errorEvent := fmt.Sprintf("data: %s\n\n", string(jsonBytes))
			if _, err := fmt.Fprint(c.Writer, errorEvent); err != nil {
				_ = c.Error(err)
			}
			flusher.Flush()
		}
		return
	}

	// Normal case: return JSON response with proper status code
	h.errorResponse(c, status, errType, message)
}

// errorResponse 返回Claude API格式的错误响应
func (h *GatewayHandler) errorResponse(c *gin.Context, status int, errType, message string) {
	c.JSON(status, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
}

// CountTokens handles token counting endpoint
// POST /v1/messages/count_tokens
// 特点：校验订阅/余额，但不计算并发、不记录使用量
func (h *GatewayHandler) CountTokens(c *gin.Context) {
	// 从context获取apiKey和user（ApiKeyAuth中间件已设置）
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}

	_, ok = middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	if apiKey.Group != nil && apiKey.Group.Platform == service.PlatformGrok {
		c.JSON(http.StatusNotFound, gin.H{
			"type": "error",
			"error": gin.H{
				"type":    "not_found_error",
				"message": "count_tokens is not supported for Grok groups",
			},
		})
		return
	}

	// 读取请求体
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		if maxErr, ok := extractMaxBytesError(err); ok {
			h.errorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
			return
		}
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}

	if len(body) == 0 {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}

	setOpsRequestContext(c, "", false, body)

	parsedReq, err := service.ParseGatewayRequest(body)
	if err != nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}

	// 验证 model 必填
	if parsedReq.Model == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}

	// 归一化模型名上的 "[1m]" 上下文标记，必须early于账号选择。
	if effectiveModel, effectiveBody, normalized, rewriteErr := applyModelContextSuffix(c, parsedReq.Model, body); rewriteErr != nil {
		log.Printf("[CountTokens] model_context_suffix rewrite failed model=%s error=%v", parsedReq.Model, rewriteErr)
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "failed to rewrite model context suffix")
		return
	} else if normalized {
		parsedReq.Model = effectiveModel
		body = effectiveBody
		// ForwardCountTokens 读 parsedReq.Body，必须一起改写。
		parsedReq.Body = effectiveBody
	}

	setOpsRequestContext(c, parsedReq.Model, parsedReq.Stream, body)

	// 获取订阅信息（可能为nil）
	subscription, _ := middleware2.GetSubscriptionFromContext(c)

	// 校验 billing eligibility（订阅/余额）
	// 【注意】不计算并发，但需要校验订阅/余额
	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription); err != nil {
		status, code, message := billingErrorDetails(err)
		h.errorResponse(c, status, code, message)
		return
	}

	// 计算粘性会话 hash（优先使用 X-Conversation-ID header）
	conversationID := c.GetHeader("X-Conversation-ID")
	sessionHash := h.gatewayService.GenerateSessionHash(parsedReq, conversationID)

	// 选择支持该模型的账号
	account, err := h.gatewayService.SelectAccountForModel(c.Request.Context(), apiKey.GroupID, sessionHash, parsedReq.Model)
	if err != nil {
		if isModelNotSupportedErr(err) {
			markOpsModelMismatch(c)
			h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", modelNotSupportedClientMessage(parsedReq.Model))
			return
		}
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "No available accounts: "+err.Error())
		return
	}
	setOpsSelectedAccount(c, account.ID)

	// 转发请求（不记录使用量）
	if err := h.gatewayService.ForwardCountTokens(c.Request.Context(), c, account, parsedReq); err != nil {
		log.Printf("Forward count_tokens request failed: %v", err)
		// 错误响应已在 ForwardCountTokens 中处理
		return
	}
}

// isWarmupRequest 检测是否为预热请求（标题生成、Warmup等）
func isWarmupRequest(body []byte) bool {
	// 快速检查：如果body不包含关键字，直接返回false
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "title") && !strings.Contains(bodyStr, "Warmup") {
		return false
	}

	// 解析完整请求
	var req struct {
		Messages []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
		System []struct {
			Text string `json:"text"`
		} `json:"system"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return false
	}

	// 检查 messages 中的标题提示模式
	for _, msg := range req.Messages {
		for _, content := range msg.Content {
			if content.Type == "text" {
				if strings.Contains(content.Text, "Please write a 5-10 word title for the following conversation:") ||
					content.Text == "Warmup" {
					return true
				}
			}
		}
	}

	// 检查 system 中的标题提取模式
	for _, system := range req.System {
		if strings.Contains(system.Text, "nalyze if this message indicates a new conversation topic. If it does, extract a 2-3 word title") {
			return true
		}
	}

	return false
}

// sendMockWarmupStream 发送流式 mock 响应（用于预热请求拦截）
func sendMockWarmupStream(c *gin.Context, model string) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	// Build message_start event with proper JSON marshaling
	messageStart := map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            "msg_mock_warmup",
			"type":          "message",
			"role":          "assistant",
			"model":         model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]int{
				"input_tokens":  10,
				"output_tokens": 0,
			},
		},
	}
	messageStartJSON, _ := json.Marshal(messageStart)

	events := []string{
		`event: message_start` + "\n" + `data: ` + string(messageStartJSON),
		`event: content_block_start` + "\n" + `data: {"content_block":{"text":"","type":"text"},"index":0,"type":"content_block_start"}`,
		`event: content_block_delta` + "\n" + `data: {"delta":{"text":"New","type":"text_delta"},"index":0,"type":"content_block_delta"}`,
		`event: content_block_delta` + "\n" + `data: {"delta":{"text":" Conversation","type":"text_delta"},"index":0,"type":"content_block_delta"}`,
		`event: content_block_stop` + "\n" + `data: {"index":0,"type":"content_block_stop"}`,
		`event: message_delta` + "\n" + `data: {"delta":{"stop_reason":"end_turn","stop_sequence":null},"type":"message_delta","usage":{"input_tokens":10,"output_tokens":2}}`,
		`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
	}

	for _, event := range events {
		_, _ = c.Writer.WriteString(event + "\n\n")
		c.Writer.Flush()
		time.Sleep(20 * time.Millisecond)
	}
}

// sendMockWarmupResponse 发送非流式 mock 响应（用于预热请求拦截）
func sendMockWarmupResponse(c *gin.Context, model string) {
	c.JSON(http.StatusOK, gin.H{
		"id":          "msg_mock_warmup",
		"type":        "message",
		"role":        "assistant",
		"model":       model,
		"content":     []gin.H{{"type": "text", "text": "New Conversation"}},
		"stop_reason": "end_turn",
		"usage": gin.H{
			"input_tokens":  10,
			"output_tokens": 2,
		},
	})
}

func billingErrorDetails(err error) (status int, code, message string) {
	if errors.Is(err, service.ErrBillingServiceUnavailable) {
		msg := pkgerrors.Message(err)
		if msg == "" {
			msg = "Billing service temporarily unavailable. Please retry later."
		}
		return http.StatusServiceUnavailable, "billing_service_error", msg
	}
	msg := pkgerrors.Message(err)
	if msg == "" {
		msg = err.Error()
	}
	return http.StatusForbidden, "billing_error", msg
}
