package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// Images handles OpenAI Images API requests.
// POST /v1/images/generations
// POST /v1/images/edits
func (h *OpenAIGatewayHandler) Images(c *gin.Context) {
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

	parsed, err := h.gatewayService.ParseOpenAIImagesRequest(c, body)
	if err != nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	if apiKey.Group != nil && apiKey.Group.Platform == service.PlatformGrok {
		if !parsed.ExplicitModel {
			parsed.Model = "grok-imagine-image-quality"
		} else if !xai.IsGrokImagineModel(parsed.Model) {
			h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Grok image requests require a Grok Imagine model")
			return
		}
	} else if xai.IsGrokImagineModel(parsed.Model) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Grok image models require a Grok group")
		return
	}
	if parsed.Multipart {
		setOpsRequestContext(c, parsed.Model, parsed.Stream, nil)
	} else {
		setOpsRequestContext(c, parsed.Model, parsed.Stream, body)
	}

	streamStarted := false
	subscription, _ := middleware2.GetSubscriptionFromContext(c)

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

	userReleaseFunc, err := h.concurrencyHelper.AcquireUserSlotWithWait(c, subject.UserID, subject.Concurrency, parsed.Stream, &streamStarted)
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

	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription); err != nil {
		log.Printf("Billing eligibility check failed after wait: %v", err)
		status, code, message := billingErrorDetails(err)
		h.handleStreamingAwareError(c, status, code, message, streamStarted)
		return
	}

	sessionHash := ""
	if !parsed.Multipart {
		var reqBody map[string]any
		if err := json.Unmarshal(body, &reqBody); err == nil {
			sessionHash = h.gatewayService.GenerateSessionHash(c, reqBody)
		}
	}

	const maxAccountSwitches = 3
	const maxTotalSwitches = 10
	switchCount := 0
	totalSwitchCount := 0
	failedAccountIDs := make(map[int64]struct{})
	lastFailoverStatus := 0
	lastFailoverMsg := ""

	selectionCtx := c.Request.Context()
	if apiKey.Group != nil && apiKey.Group.Platform == service.PlatformGrok {
		selectionCtx = service.WithOpenAIRequestPlatform(selectionCtx, service.PlatformGrok)
	}

	for {
		log.Printf("[OpenAI Images Handler] Selecting account: groupID=%v model=%s capability=%s", apiKey.GroupID, parsed.Model, parsed.RequiredCapability)
		selection, err := h.gatewayService.SelectAccountWithLoadAwareness(selectionCtx, apiKey.GroupID, sessionHash, parsed.Model, failedAccountIDs)
		if err != nil {
			log.Printf("[OpenAI Images Handler] SelectAccount failed: %v", err)
			if len(failedAccountIDs) == 0 {
				if isModelNotSupportedErr(err) {
					markOpsModelMismatch(c)
					h.handleStreamingAwareError(c, http.StatusBadRequest, "invalid_request_error", modelNotSupportedClientMessage(parsed.Model), streamStarted)
					return
				}
				h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", "No available accounts: "+err.Error(), streamStarted)
				return
			}
			h.gatewayService.InvalidateStickySession(c.Request.Context(), apiKey.GroupID, sessionHash)
			if lastFailoverStatus == 0 {
				h.handleStreamingAwareError(c, http.StatusServiceUnavailable, "api_error", "No available compatible OpenAI image accounts", streamStarted)
				return
			}
			h.handleFailoverExhausted(c, lastFailoverStatus, lastFailoverMsg, streamStarted)
			return
		}
		account := selection.Account
		log.Printf("[OpenAI Images Handler] Selected account: id=%d name=%s type=%s", account.ID, account.Name, account.Type)
		setOpsSelectedAccount(c, account.ID)

		effectiveParsed, ignoredOptions := parsed.NormalizeForAccount(account)
		if len(ignoredOptions) > 0 {
			log.Printf("[OpenAI Images Handler] Account %d ignoring unsupported OAuth image options: %v", account.ID, ignoredOptions)
		}

		if !account.SupportsOpenAIImageCapability(effectiveParsed.RequiredCapability) {
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			failedAccountIDs[account.ID] = struct{}{}
			log.Printf("[OpenAI Images Handler] Account %d is incompatible with capability %s", account.ID, effectiveParsed.RequiredCapability)
			continue
		}

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
				effectiveParsed.Stream,
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

		result, err := h.gatewayService.ForwardImages(c.Request.Context(), c, account, body, effectiveParsed)
		if accountReleaseFunc != nil {
			accountReleaseFunc()
		}
		if err != nil {
			if c.Writer.Written() {
				log.Printf("Account %d: ForwardImages failed after response committed: %v", account.ID, err)
				return
			}
			var failoverErr *service.UpstreamFailoverError
			if errors.As(err, &failoverErr) {
				failedAccountIDs[account.ID] = struct{}{}
				lastFailoverStatus = failoverErr.StatusCode
				lastFailoverMsg = failoverErr.Message
				if totalSwitchCount >= maxTotalSwitches {
					h.gatewayService.InvalidateStickySession(c.Request.Context(), apiKey.GroupID, sessionHash)
					h.handleFailoverExhausted(c, lastFailoverStatus, lastFailoverMsg, streamStarted)
					return
				}
				if failoverErr.StatusCode == http.StatusTooManyRequests {
					h.gatewayService.InvalidateStickySession(c.Request.Context(), apiKey.GroupID, sessionHash)
					totalSwitchCount++
					log.Printf("Account %d: upstream 429, rate-limit failover (total=%d/%d)", account.ID, totalSwitchCount, maxTotalSwitches)
				} else {
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

			log.Printf("Account %d: ForwardImages failed: %v", account.ID, err)
			if !c.Writer.Written() {
				h.handleStreamingAwareError(c, http.StatusBadGateway, "upstream_error", err.Error(), streamStarted)
			}
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
