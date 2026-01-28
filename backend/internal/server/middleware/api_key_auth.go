package middleware

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// NewAPIKeyAuthMiddleware 创建 API Key 认证中间件
func NewAPIKeyAuthMiddleware(apiKeyService *service.APIKeyService, subscriptionService *service.SubscriptionService, tempAPIKeyRepo *repository.TempAPIKeyRepo, cfg *config.Config) APIKeyAuthMiddleware {
	return APIKeyAuthMiddleware(apiKeyAuthWithSubscription(apiKeyService, subscriptionService, tempAPIKeyRepo, cfg))
}

// apiKeyAuthWithSubscription API Key认证中间件（支持订阅验证）
func apiKeyAuthWithSubscription(apiKeyService *service.APIKeyService, subscriptionService *service.SubscriptionService, tempAPIKeyRepo *repository.TempAPIKeyRepo, cfg *config.Config) gin.HandlerFunc {
	// Create temp API key service
	var tempAPIKeyService *service.TempAPIKeyService
	if tempAPIKeyRepo != nil {
		tempAPIKeyService = service.NewTempAPIKeyService(tempAPIKeyRepo)
	}

	return func(c *gin.Context) {
		queryKey := strings.TrimSpace(c.Query("key"))
		queryApiKey := strings.TrimSpace(c.Query("api_key"))
		if queryKey != "" || queryApiKey != "" {
			AbortWithError(c, 400, "api_key_in_query_deprecated", "API key in query parameter is deprecated. Please use Authorization header instead.")
			return
		}

		// 尝试从Authorization header中提取API key (Bearer scheme)
		authHeader := c.GetHeader("Authorization")
		var apiKeyString string

		if authHeader != "" {
			// 验证Bearer scheme
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) == 2 && parts[0] == "Bearer" {
				apiKeyString = parts[1]
			}
		}

		// 如果Authorization header中没有，尝试从x-api-key header中提取
		if apiKeyString == "" {
			apiKeyString = c.GetHeader("x-api-key")
		}

		// 如果x-api-key header中没有，尝试从x-goog-api-key header中提取（Gemini CLI兼容）
		if apiKeyString == "" {
			apiKeyString = c.GetHeader("x-goog-api-key")
		}

		// 如果所有header都没有API key
		if apiKeyString == "" {
			AbortWithError(c, 401, "API_KEY_REQUIRED", "API key is required in Authorization header (Bearer scheme), x-api-key header, or x-goog-api-key header")
			return
		}

		// 检查是否为临时 API Key (sk-temp- 前缀)
		if service.IsTempAPIKey(apiKeyString) {
			handleTempAPIKey(c, apiKeyString, tempAPIKeyService, cfg)
			return
		}

		// 从数据库验证API key
		apiKey, err := apiKeyService.GetByKey(c.Request.Context(), apiKeyString)
		if err != nil {
			if errors.Is(err, service.ErrAPIKeyNotFound) {
				AbortWithError(c, 401, "INVALID_API_KEY", "Invalid API key")
				return
			}
			AbortWithError(c, 500, "INTERNAL_ERROR", "Failed to validate API key")
			return
		}

		// 检查API key是否激活
		if !apiKey.IsActive() {
			AbortWithError(c, 401, "API_KEY_DISABLED", "API key is disabled")
			return
		}

		// 检查 IP 限制（白名单/黑名单）
		// 注意：错误信息故意模糊，避免暴露具体的 IP 限制机制
		if len(apiKey.IPWhitelist) > 0 || len(apiKey.IPBlacklist) > 0 {
			clientIP := ip.GetClientIP(c)
			allowed, _ := ip.CheckIPRestriction(clientIP, apiKey.IPWhitelist, apiKey.IPBlacklist)
			if !allowed {
				AbortWithError(c, 403, "ACCESS_DENIED", "Access denied")
				return
			}
		}

		// 检查关联的用户
		if apiKey.User == nil {
			AbortWithError(c, 401, "USER_NOT_FOUND", "User associated with API key not found")
			return
		}

		// 检查用户状态
		if !apiKey.User.IsActive() {
			AbortWithError(c, 401, "USER_INACTIVE", "User account is not active")
			return
		}

		if cfg.RunMode == config.RunModeSimple {
			// 简易模式：跳过余额和订阅检查，但仍需设置必要的上下文
			c.Set(string(ContextKeyAPIKey), apiKey)
			c.Set(string(ContextKeyUser), AuthSubject{
				UserID:      apiKey.User.ID,
				Concurrency: apiKey.User.Concurrency,
			})
			c.Set(string(ContextKeyUserRole), apiKey.User.Role)
			setGroupContext(c, apiKey.Group)
			c.Next()
			return
		}

		// 判断计费方式：订阅模式 vs 余额模式
		isSubscriptionType := apiKey.Group != nil && apiKey.Group.IsSubscriptionType()

		if isSubscriptionType && subscriptionService != nil {
			// 订阅模式：验证订阅
			subscription, err := subscriptionService.GetActiveSubscription(
				c.Request.Context(),
				apiKey.User.ID,
				apiKey.Group.ID,
			)
			if err != nil {
				AbortWithError(c, 403, "SUBSCRIPTION_NOT_FOUND", "No active subscription found for this group")
				return
			}

			// 验证订阅状态（是否过期、暂停等）
			if err := subscriptionService.ValidateSubscription(c.Request.Context(), subscription); err != nil {
				AbortWithError(c, 403, "SUBSCRIPTION_INVALID", err.Error())
				return
			}

			// 激活滑动窗口（首次使用时）
			if err := subscriptionService.CheckAndActivateWindow(c.Request.Context(), subscription); err != nil {
				log.Printf("Failed to activate subscription windows: %v", err)
			}

			// 检查并重置过期窗口
			if err := subscriptionService.CheckAndResetWindows(c.Request.Context(), subscription); err != nil {
				log.Printf("Failed to reset subscription windows: %v", err)
			}

			// 预检查用量限制（使用0作为额外费用进行预检查）
			if err := subscriptionService.CheckUsageLimits(c.Request.Context(), subscription, apiKey.Group, 0); err != nil {
				AbortWithError(c, 429, "USAGE_LIMIT_EXCEEDED", err.Error())
				return
			}

			// 将订阅信息存入上下文
			c.Set(string(ContextKeySubscription), subscription)
		} else {
			// 余额模式：检查用户余额
			if apiKey.User.Balance <= 0 {
				AbortWithError(c, 403, "INSUFFICIENT_BALANCE", "Insufficient account balance")
				return
			}
		}

		// 将API key和用户信息存入上下文
		c.Set(string(ContextKeyAPIKey), apiKey)
		c.Set(string(ContextKeyUser), AuthSubject{
			UserID:      apiKey.User.ID,
			Concurrency: apiKey.User.Concurrency,
		})
		c.Set(string(ContextKeyUserRole), apiKey.User.Role)
		setGroupContext(c, apiKey.Group)

		c.Next()
	}
}

// GetAPIKeyFromContext 从上下文中获取API key
func GetAPIKeyFromContext(c *gin.Context) (*service.APIKey, bool) {
	value, exists := c.Get(string(ContextKeyAPIKey))
	if !exists {
		return nil, false
	}
	apiKey, ok := value.(*service.APIKey)
	return apiKey, ok
}

// GetSubscriptionFromContext 从上下文中获取订阅信息
func GetSubscriptionFromContext(c *gin.Context) (*service.UserSubscription, bool) {
	value, exists := c.Get(string(ContextKeySubscription))
	if !exists {
		return nil, false
	}
	subscription, ok := value.(*service.UserSubscription)
	return subscription, ok
}

func setGroupContext(c *gin.Context, group *service.Group) {
	if !service.IsGroupContextValid(group) {
		return
	}
	if existing, ok := c.Request.Context().Value(ctxkey.Group).(*service.Group); ok && existing != nil && existing.ID == group.ID && service.IsGroupContextValid(existing) {
		return
	}
	ctx := context.WithValue(c.Request.Context(), ctxkey.Group, group)
	c.Request = c.Request.WithContext(ctx)
}

// handleTempAPIKey 处理临时 API Key 认证
func handleTempAPIKey(c *gin.Context, apiKeyString string, tempAPIKeyService *service.TempAPIKeyService, cfg *config.Config) {
	if tempAPIKeyService == nil {
		AbortWithError(c, 401, "INVALID_API_KEY", "Invalid API key")
		return
	}

	// 从数据库获取临时 API Key
	tempKey, err := tempAPIKeyService.GetByKey(c.Request.Context(), apiKeyString)
	if err != nil {
		AbortWithError(c, 401, "INVALID_API_KEY", "Invalid API key")
		return
	}

	// 验证并增加使用计数
	updatedKey, err := tempAPIKeyService.ValidateAndIncrement(c.Request.Context(), tempKey)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrTempAPIKeyExpired):
			AbortWithError(c, 401, "TEMP_API_KEY_EXPIRED", "Temporary API key has expired")
		case errors.Is(err, service.ErrTempAPIKeyInactive):
			AbortWithError(c, 401, "TEMP_API_KEY_INACTIVE", "Temporary API key is inactive")
		case errors.Is(err, service.ErrTempAPIKeyRateLimited):
			AbortWithError(c, 429, "TEMP_API_KEY_RATE_LIMITED", "Temporary API key daily limit exceeded")
		default:
			AbortWithError(c, 500, "INTERNAL_ERROR", "Failed to validate temporary API key")
		}
		return
	}

	// 检查关联的分组
	if updatedKey.Group == nil {
		AbortWithError(c, 401, "GROUP_NOT_FOUND", "Group associated with temporary API key not found")
		return
	}

	// 检查分组状态
	if updatedKey.Group.Status != service.StatusActive {
		AbortWithError(c, 401, "GROUP_INACTIVE", "Group is not active")
		return
	}

	// 检查创建者（用于计费）
	if updatedKey.Creator == nil {
		AbortWithError(c, 401, "CREATOR_NOT_FOUND", "Creator of temporary API key not found")
		return
	}

	// 检查创建者状态
	if !updatedKey.Creator.IsActive() {
		AbortWithError(c, 401, "CREATOR_INACTIVE", "Creator account is not active")
		return
	}

	// 非简易模式下，检查创建者余额（余额模式）
	if cfg.RunMode != config.RunModeSimple {
		isSubscriptionType := updatedKey.Group.IsSubscriptionType()
		if !isSubscriptionType && updatedKey.Creator.Balance <= 0 {
			AbortWithError(c, 403, "INSUFFICIENT_BALANCE", "Insufficient account balance")
			return
		}
	}

	// 创建一个虚拟的 APIKey 对象，使用创建者（admin）作为计费用户
	virtualAPIKey := &service.APIKey{
		ID:      0,
		UserID:  updatedKey.Creator.ID,
		Key:     updatedKey.Key,
		Name:    updatedKey.Name,
		GroupID: &updatedKey.GroupID,
		Status:  service.StatusActive,
		User:    updatedKey.Creator, // 使用创建者作为计费用户
		Group:   updatedKey.Group,
	}

	// 设置上下文 - 同时设置虚拟 APIKey 和临时 Key
	c.Set(string(ContextKeyAPIKey), virtualAPIKey)
	c.Set(string(ContextKeyTempAPIKey), updatedKey)
	c.Set(string(ContextKeyUser), AuthSubject{
		UserID:      updatedKey.Creator.ID,
		Concurrency: updatedKey.Creator.Concurrency,
	})
	setGroupContext(c, updatedKey.Group)

	c.Next()
}

// GetTempAPIKeyFromContext 从上下文中获取临时 API Key
func GetTempAPIKeyFromContext(c *gin.Context) (*service.TempAPIKey, bool) {
	value, exists := c.Get(string(ContextKeyTempAPIKey))
	if !exists {
		return nil, false
	}
	tempKey, ok := value.(*service.TempAPIKey)
	return tempKey, ok
}
