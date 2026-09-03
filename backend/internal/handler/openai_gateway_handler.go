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
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// OpenAIGatewayHandler handles OpenAI API gateway requests
type OpenAIGatewayHandler struct {
	gatewayService      *service.OpenAIGatewayService
	kiroGatewayService  *service.KiroGatewayService
	billingCacheService *service.BillingCacheService
	concurrencyHelper   *ConcurrencyHelper
	imagePreviewJobs    *imagePreviewJobStore
}

// NewOpenAIGatewayHandler creates a new OpenAIGatewayHandler
func NewOpenAIGatewayHandler(
	gatewayService *service.OpenAIGatewayService,
	kiroGatewayService *service.KiroGatewayService,
	concurrencyService *service.ConcurrencyService,
	billingCacheService *service.BillingCacheService,
	cfg *config.Config,
) *OpenAIGatewayHandler {
	pingInterval := time.Duration(0)
	if cfg != nil {
		pingInterval = time.Duration(cfg.Concurrency.PingInterval) * time.Second
	}
	return &OpenAIGatewayHandler{
		gatewayService:      gatewayService,
		kiroGatewayService:  kiroGatewayService,
		billingCacheService: billingCacheService,
		concurrencyHelper:   NewConcurrencyHelper(concurrencyService, SSEPingFormatComment, pingInterval),
		imagePreviewJobs:    newImagePreviewJobStore(),
	}
}

// Responses handles OpenAI Responses API endpoint
// POST /openai/v1/responses
func (h *OpenAIGatewayHandler) Responses(c *gin.Context) {
	// Get apiKey and user from context (set by ApiKeyAuth middleware)
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

	// Read request body
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

	// Parse request body to map for potential modification
	var reqBody map[string]any
	if err := json.Unmarshal(body, &reqBody); err != nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}

	// Extract model and stream
	reqModel, _ := reqBody["model"].(string)
	reqStream, _ := reqBody["stream"].(bool)

	// 验证 model 必填
	if reqModel == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}

	// 拦截：openai 分组不支持 claude 系列模型
	if isOpenAIGroupClaudeModelMismatch(apiKey.Group, reqModel) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("Model %q is not supported by the OpenAI group. Claude models require an Anthropic group. Please use an OpenAI-compatible model (e.g. gpt-5.5, gpt-5.4) or switch the API key group.", reqModel))
		return
	}

	userAgent := c.GetHeader("User-Agent")
	if !openai.IsCodexCLIRequest(userAgent) {
		existingInstructions, _ := reqBody["instructions"].(string)
		if strings.TrimSpace(existingInstructions) == "" {
			if instructions := strings.TrimSpace(service.GetOpenCodeInstructions()); instructions != "" {
				reqBody["instructions"] = instructions
				c.Set(service.OpenAIInjectedInstructionsContextKey, instructions)
				// Re-serialize body
				body, err = json.Marshal(reqBody)
				if err != nil {
					h.errorResponse(c, http.StatusInternalServerError, "api_error", "Failed to process request")
					return
				}
			}
		}
	}

	setOpsRequestContext(c, reqModel, reqStream, body)

	// 提前校验 function_call_output 是否具备可关联上下文，避免上游 400。
	// 要求 previous_response_id，或 input 内存在带 call_id 的 tool_call/function_call，
	// 或带 id 且与 call_id 匹配的 item_reference。
	if service.HasFunctionCallOutput(reqBody) {
		previousResponseID, _ := reqBody["previous_response_id"].(string)
		if strings.TrimSpace(previousResponseID) == "" && !service.HasToolCallContext(reqBody) {
			if service.HasFunctionCallOutputMissingCallID(reqBody) {
				log.Printf("[OpenAI Handler] function_call_output 缺少 call_id: model=%s", reqModel)
				h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "function_call_output requires call_id or previous_response_id; if relying on history, ensure store=true and reuse previous_response_id")
				return
			}
			callIDs := service.FunctionCallOutputCallIDs(reqBody)
			if !service.HasItemReferenceForCallIDs(reqBody, callIDs) {
				log.Printf("[OpenAI Handler] function_call_output 缺少匹配的 item_reference: model=%s", reqModel)
				h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "function_call_output requires item_reference ids matching each call_id, or previous_response_id/tool_call context; if relying on history, ensure store=true and reuse previous_response_id")
				return
			}
		}
	}

	// Track if we've started streaming (for error handling)
	streamStarted := false

	// Get subscription info (may be nil)
	subscription, _ := middleware2.GetSubscriptionFromContext(c)

	// 0. Check if wait queue is full
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
	defer func() {
		if waitCounted {
			h.concurrencyHelper.DecrementWaitCount(c.Request.Context(), subject.UserID)
		}
	}()

	// 1. First acquire user concurrency slot
	userReleaseFunc, err := h.concurrencyHelper.AcquireUserSlotWithWait(c, subject.UserID, subject.Concurrency, reqStream, &streamStarted)
	if err != nil {
		log.Printf("User concurrency acquire failed: %v", err)
		h.handleConcurrencyError(c, err, "user", streamStarted)
		return
	}
	// User slot acquired: no longer waiting.
	if waitCounted {
		h.concurrencyHelper.DecrementWaitCount(c.Request.Context(), subject.UserID)
		waitCounted = false
	}
	// 确保请求取消时也会释放槽位，避免长连接被动中断造成泄漏
	userReleaseFunc = wrapReleaseOnDone(c.Request.Context(), userReleaseFunc)
	if userReleaseFunc != nil {
		defer userReleaseFunc()
	}

	// 2. Re-check billing eligibility after wait
	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription); err != nil {
		log.Printf("Billing eligibility check failed after wait: %v", err)
		status, code, message := billingErrorDetails(err)
		h.handleStreamingAwareError(c, status, code, message, streamStarted)
		return
	}

	// Generate session hash (header first; fallback to prompt_cache_key)
	sessionHash := h.gatewayService.GenerateSessionHash(c, reqBody)
	selectionCtx := c.Request.Context()
	if apiKey.Group != nil && apiKey.Group.Platform == service.PlatformGrok {
		selectionCtx = service.WithOpenAIRequestPlatform(selectionCtx, service.PlatformGrok)
	}

	const maxAccountSwitches = 3
	const maxTotalSwitches = 10
	switchCount := 0
	totalSwitchCount := 0
	failedAccountIDs := make(map[int64]struct{})
	lastFailoverStatus := 0
	lastFailoverMsg := ""

	for {
		// Select account supporting the requested model
		log.Printf("[OpenAI Handler] Selecting account: groupID=%v model=%s", apiKey.GroupID, reqModel)
		selection, err := h.gatewayService.SelectAccountWithLoadAwareness(selectionCtx, apiKey.GroupID, sessionHash, reqModel, failedAccountIDs)
		if err != nil {
			log.Printf("[OpenAI Handler] SelectAccount failed: %v", err)
			if len(failedAccountIDs) == 0 {
				if isModelNotSupportedErr(err) {
					markOpsModelMismatch(c)
					h.handleStreamingAwareError(c, http.StatusBadRequest, "invalid_request_error", modelNotSupportedClientMessage(reqModel), streamStarted)
					return
				}
				h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", "No available accounts: "+err.Error(), streamStarted)
				return
			}
			h.gatewayService.InvalidateStickySession(c.Request.Context(), apiKey.GroupID, sessionHash)
			h.handleFailoverExhausted(c, lastFailoverStatus, lastFailoverMsg, streamStarted)
			return
		}
		account := selection.Account
		log.Printf("[OpenAI Handler] Selected account: id=%d name=%s", account.ID, account.Name)
		setOpsSelectedAccount(c, account.ID)

		// Skip Kiro accounts — Responses API is not supported for Kiro platform
		if account.Platform == service.PlatformKiro {
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			failedAccountIDs[account.ID] = struct{}{}
			log.Printf("[OpenAI Handler] Account %d is Kiro platform, skipping (Responses API unsupported)", account.ID)
			continue
		}

		// 3. Acquire account concurrency slot
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
			if err := h.gatewayService.BindStickySession(c.Request.Context(), apiKey.GroupID, sessionHash, account.ID); err != nil {
				log.Printf("Bind sticky session failed: %v", err)
			}
		}
		// 账号槽位/等待计数需要在超时或断开时安全回收
		accountReleaseFunc = wrapReleaseOnDone(c.Request.Context(), accountReleaseFunc)

		// Forward request
		result, err := h.gatewayService.Forward(c.Request.Context(), c, account, body)
		if accountReleaseFunc != nil {
			accountReleaseFunc()
		}
		if err != nil {
			var failoverErr *service.UpstreamFailoverError
			if errors.As(err, &failoverErr) {
				failedAccountIDs[account.ID] = struct{}{}
				lastFailoverStatus = failoverErr.StatusCode
				lastFailoverMsg = failoverErr.Message
				// 全局硬上限：单请求内最多切换 10 次，避免过长链路。
				if totalSwitchCount >= maxTotalSwitches {
					h.gatewayService.InvalidateStickySession(c.Request.Context(), apiKey.GroupID, sessionHash)
					h.handleFailoverExhausted(c, lastFailoverStatus, lastFailoverMsg, streamStarted)
					return
				}
				if failoverErr.StatusCode == http.StatusTooManyRequests {
					// 429: 清除 sticky session，避免下次请求仍路由到同一账号
					h.gatewayService.InvalidateStickySession(c.Request.Context(), apiKey.GroupID, sessionHash)
					totalSwitchCount++
					log.Printf("Account %d: upstream 429, rate-limit failover (total=%d/%d)", account.ID, totalSwitchCount, maxTotalSwitches)
				} else {
					// 非 429 维持较低切换上限。
					if switchCount >= maxAccountSwitches {
						h.gatewayService.InvalidateStickySession(c.Request.Context(), apiKey.GroupID, sessionHash)
						h.handleFailoverExhausted(c, lastFailoverStatus, lastFailoverMsg, streamStarted)
						return
					}
					switchCount++
					totalSwitchCount++
					log.Printf("Account %d: upstream error %d, switching account %d/%d (total=%d/%d)", account.ID, failoverErr.StatusCode, switchCount, maxAccountSwitches, totalSwitchCount, maxTotalSwitches)
				}
				continue
			}
			// Error response already handled in Forward, just log
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

		// Async record usage
		go func(result *service.OpenAIForwardResult, usedAccount *service.Account, ua, ip string, tempKeyID *int64, tempKey *service.TempAPIKey) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := h.gatewayService.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
				Result:       result,
				APIKey:       apiKey,
				User:         apiKey.User,
				Account:      usedAccount,
				Subscription: subscription,
				UserAgent:    ua,
				IPAddress:    ip,
				TempAPIKeyID: tempKeyID,
				TempAPIKey:   tempKey,
			}); err != nil {
				log.Printf("Record usage failed: %v", err)
			}
		}(result, account, userAgent, clientIP, tempAPIKeyID, tempAPIKey)
		return
	}
}

// ChatCompletions handles OpenAI Chat Completions API endpoint
// POST /v1/chat/completions
// Supports both API Key accounts (direct passthrough) and OAuth accounts (CC ↔ Responses API conversion).
func (h *OpenAIGatewayHandler) ChatCompletions(c *gin.Context) {
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

	var reqBody map[string]any
	if err := json.Unmarshal(body, &reqBody); err != nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}

	reqModel, _ := reqBody["model"].(string)
	reqStream, _ := reqBody["stream"].(bool)

	if reqModel == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}

	// 拦截：openai 分组不支持 claude 系列模型
	if isOpenAIGroupClaudeModelMismatch(apiKey.Group, reqModel) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("Model %q is not supported by the OpenAI group. Claude models require an Anthropic group. Please use an OpenAI-compatible model (e.g. gpt-5.5, gpt-5.4) or switch the API key group.", reqModel))
		return
	}

	if effectiveModel, effectiveBody, downgraded, rewriteErr := applyKiroOpus47GroupDowngrade(apiKey.Group, reqModel, body); rewriteErr != nil {
		log.Printf("[OpenAI CC Handler] kiro_opus_47_downgrade rewrite failed group_id=%d model=%s error=%v", apiKey.Group.ID, reqModel, rewriteErr)
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "failed to rewrite downgraded model request")
		return
	} else if downgraded {
		log.Printf("[OpenAI CC Handler] kiro_opus_47_downgrade group_id=%d model=%s -> %s", apiKey.Group.ID, reqModel, effectiveModel)
		reqModel = effectiveModel
		reqBody["model"] = effectiveModel
		body = effectiveBody
	}

	setOpsRequestContext(c, reqModel, reqStream, body)

	streamStarted := false
	requestStartedAt := time.Now()
	subscription, _ := middleware2.GetSubscriptionFromContext(c)

	// Check wait queue
	maxWait := service.CalculateMaxWait(subject.Concurrency)
	canWait, err := h.concurrencyHelper.IncrementWaitCount(c.Request.Context(), subject.UserID, maxWait)
	waitCounted := false
	if err != nil {
		log.Printf("Increment wait count failed: %v", err)
	} else if !canWait {
		h.errorResponse(c, http.StatusTooManyRequests, "rate_limit_error", "Too many pending requests, please retry later")
		return
	}
	if err == nil && canWait {
		waitCounted = true
	}
	defer func() {
		if waitCounted {
			h.concurrencyHelper.DecrementWaitCount(c.Request.Context(), subject.UserID)
		}
	}()

	// Acquire user concurrency slot
	userReleaseFunc, err := h.concurrencyHelper.AcquireUserSlotWithWait(c, subject.UserID, subject.Concurrency, reqStream, &streamStarted)
	if err != nil {
		log.Printf("User concurrency acquire failed: %v", err)
		h.handleConcurrencyError(c, err, "user", streamStarted)
		return
	}
	if waitCounted {
		h.concurrencyHelper.DecrementWaitCount(c.Request.Context(), subject.UserID)
		waitCounted = false
	}
	userReleaseFunc = wrapReleaseOnDone(c.Request.Context(), userReleaseFunc)
	if userReleaseFunc != nil {
		defer userReleaseFunc()
	}

	// Re-check billing eligibility
	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription); err != nil {
		log.Printf("Billing eligibility check failed after wait: %v", err)
		status, code, message := billingErrorDetails(err)
		h.handleStreamingAwareError(c, status, code, message, streamStarted)
		return
	}

	sessionHash := h.gatewayService.GenerateSessionHash(c, reqBody)
	selectionCtx := c.Request.Context()
	if apiKey.Group != nil && apiKey.Group.Platform == service.PlatformGrok {
		selectionCtx = service.WithOpenAIRequestPlatform(selectionCtx, service.PlatformGrok)
	}

	const maxAccountSwitches = 3
	const maxTotalSwitches = 10
	switchCount := 0
	totalSwitchCount := 0
	failedAccountIDs := make(map[int64]struct{})
	lastFailoverStatus := 0
	lastFailoverMsg := ""

	for {
		log.Printf("[OpenAI CC Handler] Selecting account: groupID=%v model=%s", apiKey.GroupID, reqModel)
		selection, err := h.gatewayService.SelectAccountWithLoadAwareness(selectionCtx, apiKey.GroupID, sessionHash, reqModel, failedAccountIDs)
		if err != nil {
			log.Printf("[OpenAI CC Handler] SelectAccount failed: %v", err)
			if len(failedAccountIDs) == 0 {
				if isModelNotSupportedErr(err) {
					markOpsModelMismatch(c)
					h.handleStreamingAwareError(c, http.StatusBadRequest, "invalid_request_error", modelNotSupportedClientMessage(reqModel), streamStarted)
					return
				}
				h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", "No available accounts: "+err.Error(), streamStarted)
				return
			}
			h.gatewayService.InvalidateStickySession(c.Request.Context(), apiKey.GroupID, sessionHash)
			if lastFailoverStatus == 0 {
				h.handleStreamingAwareError(c, http.StatusBadRequest, "invalid_request_error",
					"No available accounts for Chat Completions endpoint", streamStarted)
				return
			}
			h.handleFailoverExhausted(c, lastFailoverStatus, lastFailoverMsg, streamStarted)
			return
		}
		account := selection.Account
		log.Printf("[OpenAI CC Handler] Selected account: id=%d name=%s type=%s", account.ID, account.Name, account.Type)
		setOpsSelectedAccount(c, account.ID)

		// Dispatch based on account platform/type
		if account.Platform != service.PlatformKiro &&
			account.Type != service.AccountTypeAPIKey && account.Type != service.AccountTypeOAuth {
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			failedAccountIDs[account.ID] = struct{}{}
			log.Printf("[OpenAI CC Handler] Account %d is %s/%s, skipping (unsupported)", account.ID, account.Platform, account.Type)
			continue
		}

		// Acquire account concurrency slot
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
			if err := h.gatewayService.BindStickySession(c.Request.Context(), apiKey.GroupID, sessionHash, account.ID); err != nil {
				log.Printf("Bind sticky session failed: %v", err)
			}
		}
		accountReleaseFunc = wrapReleaseOnDone(c.Request.Context(), accountReleaseFunc)

		// Forward request: dispatch based on account platform/type
		var result *service.OpenAIForwardResult
		if account.Platform == service.PlatformKiro {
			result, err = h.kiroGatewayService.ForwardChatCompletions(c.Request.Context(), c, account, body)
		} else if account.IsGrok() {
			// Grok exposes a native Chat Completions endpoint. Preserve the
			// client's Chat semantics instead of forcing every OAuth request
			// through a lossy Chat→Responses conversion. Responses-shaped
			// compatibility requests still use the existing bridge.
			if _, hasMessages := reqBody["messages"]; hasMessages {
				result, err = h.gatewayService.ForwardChatCompletions(c.Request.Context(), c, account, body)
			} else {
				result, err = h.gatewayService.ForwardChatCompletionsViaResponses(c.Request.Context(), c, account, body, reqStream)
			}
		} else if account.Type == service.AccountTypeOAuth {
			result, err = h.gatewayService.ForwardChatCompletionsViaResponses(c.Request.Context(), c, account, body, reqStream)
		} else {
			result, err = h.gatewayService.ForwardChatCompletions(c.Request.Context(), c, account, body)
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
				if shouldStopKiroOAuthInitialFailover(account, failoverErr, requestStartedAt) {
					h.gatewayService.InvalidateStickySession(c.Request.Context(), apiKey.GroupID, sessionHash)
					log.Printf("Account %d: kiro initial response failover budget exhausted after %s", account.ID, time.Since(requestStartedAt))
					h.handleFailoverExhausted(c, lastFailoverStatus, lastFailoverMsg, streamStarted)
					return
				}
				// 全局硬上限：单请求内最多切换 10 次，避免过长链路。
				if totalSwitchCount >= maxTotalSwitches {
					h.gatewayService.InvalidateStickySession(c.Request.Context(), apiKey.GroupID, sessionHash)
					h.handleFailoverExhausted(c, lastFailoverStatus, lastFailoverMsg, streamStarted)
					return
				}
				if failoverErr.StatusCode == http.StatusTooManyRequests {
					// 429: 清除 sticky session，避免下次请求仍路由到同一账号
					h.gatewayService.InvalidateStickySession(c.Request.Context(), apiKey.GroupID, sessionHash)
					totalSwitchCount++
					log.Printf("Account %d: upstream 429, rate-limit failover (total=%d/%d)", account.ID, totalSwitchCount, maxTotalSwitches)
				} else {
					// 非 429 维持较低切换上限。
					if switchCount >= maxAccountSwitches {
						h.gatewayService.InvalidateStickySession(c.Request.Context(), apiKey.GroupID, sessionHash)
						h.handleFailoverExhausted(c, lastFailoverStatus, lastFailoverMsg, streamStarted)
						return
					}
					switchCount++
					totalSwitchCount++
					log.Printf("Account %d: upstream error %d, switching account %d/%d (total=%d/%d)", account.ID, failoverErr.StatusCode, switchCount, maxAccountSwitches, totalSwitchCount, maxTotalSwitches)
				}
				continue
			}
			log.Printf("Account %d: ForwardChatCompletions failed: %v", account.ID, err)
			h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}

		userAgent := c.GetHeader("User-Agent")
		clientIP := ip.GetClientIP(c)
		var tempAPIKeyID *int64
		var tempAPIKey *service.TempAPIKey
		if tempKey, ok := middleware2.GetTempAPIKeyFromContext(c); ok && tempKey != nil {
			tempAPIKeyID = new(int64)
			*tempAPIKeyID = tempKey.ID
			tempAPIKey = tempKey
		}

		go func(result *service.OpenAIForwardResult, usedAccount *service.Account, ua, cip string, tempKeyID *int64, tempKey *service.TempAPIKey) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := h.gatewayService.RecordUsage(ctx, &service.OpenAIRecordUsageInput{
				Result:       result,
				APIKey:       apiKey,
				User:         apiKey.User,
				Account:      usedAccount,
				Subscription: subscription,
				UserAgent:    ua,
				IPAddress:    cip,
				TempAPIKeyID: tempKeyID,
				TempAPIKey:   tempKey,
			}); err != nil {
				log.Printf("Record usage failed: %v", err)
			}
		}(result, account, userAgent, clientIP, tempAPIKeyID, tempAPIKey)
		return
	}
}

// handleConcurrencyError handles concurrency-related errors with proper 429 response
func (h *OpenAIGatewayHandler) handleConcurrencyError(c *gin.Context, err error, slotType string, streamStarted bool) {
	h.handleStreamingAwareError(c, http.StatusTooManyRequests, "rate_limit_error",
		fmt.Sprintf("Concurrency limit exceeded for %s, please retry later", slotType), streamStarted)
}

func (h *OpenAIGatewayHandler) handleFailoverExhausted(c *gin.Context, statusCode int, lastMessage string, streamStarted bool) {
	status, errType, errMsg := h.mapUpstreamError(statusCode, lastMessage)
	if !streamStarted && isKiroInitialResponseTimeoutMessage(lastMessage) {
		c.Header("Retry-After", kiroClaudeOAuthClientRetryAfterHeader)
	}
	h.handleStreamingAwareError(c, status, errType, errMsg, streamStarted)
}

func (h *OpenAIGatewayHandler) mapUpstreamError(statusCode int, lastMessage string) (int, string, string) {
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
		return http.StatusServiceUnavailable, "upstream_error", "Upstream access forbidden, please contact administrator"
	case 429:
		return http.StatusTooManyRequests, "rate_limit_error", "Upstream rate limit exceeded, please retry later"
	case 529:
		return http.StatusServiceUnavailable, "upstream_error", "Upstream service overloaded, please retry later"
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
func (h *OpenAIGatewayHandler) handleStreamingAwareError(c *gin.Context, status int, errType, message string, streamStarted bool) {
	if streamStarted {
		// Stream already started, send error as SSE event then close
		flusher, ok := c.Writer.(http.Flusher)
		if ok {
			// Send error event in OpenAI SSE format
			errorEvent := fmt.Sprintf(`event: error`+"\n"+`data: {"error": {"type": "%s", "message": "%s"}}`+"\n\n", errType, message)
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

// errorResponse returns OpenAI API format error response
func (h *OpenAIGatewayHandler) errorResponse(c *gin.Context, status int, errType, message string) {
	c.JSON(status, gin.H{
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
}

// isOpenAIGroupClaudeModelMismatch 检测是否将 claude 系列模型误发到 openai 分组。
// openai 分组的账号仅支持 OpenAI 原生模型（gpt-* / o-*），
// 将 claude-* 请求派发至 openai 分组必然无可用账号，提前拦截给出明确提示。
func isOpenAIGroupClaudeModelMismatch(group *service.Group, model string) bool {
	if group == nil || group.Platform != service.PlatformOpenAI {
		return false
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "claude-")
}
