package routes

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/gin-gonic/gin"
)

// RegisterCommonRoutes 注册通用路由（健康检查、状态等）
func RegisterCommonRoutes(r *gin.Engine, h *handler.Handlers) {
	// 健康检查
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// API Key 公开查询（支持 TempAPIKey 和普通 APIKey）
	r.GET("/api/v1/keys/query", h.TempAPIKeyQuery.Query)
	// 兼容旧路由
	r.GET("/api/v1/temp-api-keys/query", h.TempAPIKeyQuery.Query)

	// Stripe 支付回调（Stripe 通过签名头鉴权，不使用 JWT）
	r.POST("/api/v1/payments/stripe/webhook", h.Payment.StripeWebhook)

	// Claude Code 遥测日志（忽略，直接返回200）
	r.POST("/api/event_logging/batch", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	// Setup status endpoint (always returns needs_setup: false in normal mode)
	// This is used by the frontend to detect when the service has restarted after setup
	r.GET("/setup/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"code": 0,
			"data": gin.H{
				"needs_setup": false,
				"step":        "completed",
			},
		})
	})
}
