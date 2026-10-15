package handler

import (
	"context"
	"log"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// recordForwardUsage records either a successful request or confirmed work
// from a failed continuation. Capture context values before launching the job.
func (h *GatewayHandler) recordForwardUsage(c *gin.Context, result *service.ForwardResult, apiKey *service.APIKey, account *service.Account, subscription *service.UserSubscription) {
	input := &service.RecordUsageInput{
		Result: result, APIKey: apiKey, User: apiKey.User, Account: account,
		Subscription: subscription, UserAgent: c.GetHeader("User-Agent"), IPAddress: ip.GetClientIP(c),
	}
	if tempKey, ok := middleware2.GetTempAPIKeyFromContext(c); ok && tempKey != nil {
		id := tempKey.ID
		input.TempAPIKeyID = &id
		input.TempAPIKey = tempKey
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := h.gatewayService.RecordUsage(ctx, input); err != nil {
			log.Printf("Record usage failed: %v", err)
		}
	}()
}
