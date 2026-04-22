package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// TempAPIKeyQueryHandler handles public API key queries (both TempAPIKey and standard APIKey)
type TempAPIKeyQueryHandler struct {
	repo         *repository.TempAPIKeyRepo
	apiKeyRepo   service.APIKeyRepository
	usageLogRepo service.UsageLogRepository
}

// NewTempAPIKeyQueryHandler creates a new TempAPIKeyQueryHandler
func NewTempAPIKeyQueryHandler(repo *repository.TempAPIKeyRepo, apiKeyRepo service.APIKeyRepository, usageLogRepo service.UsageLogRepository) *TempAPIKeyQueryHandler {
	return &TempAPIKeyQueryHandler{repo: repo, apiKeyRepo: apiKeyRepo, usageLogRepo: usageLogRepo}
}

// TempAPIKeyQueryResponse represents the public query response
type TempAPIKeyQueryResponse struct {
	Name               string          `json:"name"`
	GroupName          string          `json:"group_name,omitempty"`
	Status             string          `json:"status"`
	KeyType            string          `json:"key_type"`
	ValidDays          int             `json:"valid_days"`
	ActivatedAt        *string         `json:"activated_at"`
	ExpiresAt          *string         `json:"expires_at"`
	DailyLimit         int             `json:"daily_limit"`
	CurrentPeriodCount int             `json:"current_period_count"`
	TotalRequests      int64           `json:"total_requests"`
	RemainingRequests  int             `json:"remaining_requests"`
	IsExpired          bool            `json:"is_expired"`
	IsActivated        bool            `json:"is_activated"`
	// quota_only / standard 类型字段
	TotalQuotaUSD     float64 `json:"total_quota_usd,omitempty"`
	TotalCostUSD      float64 `json:"total_cost_usd,omitempty"`
	RemainingQuotaUSD float64 `json:"remaining_quota_usd,omitempty"`
	IsExhausted       bool    `json:"is_exhausted,omitempty"`
	// time_quota 类型专用字段
	DailyQuotaUSD          float64 `json:"daily_quota_usd,omitempty"`
	CurrentPeriodCostUSD   float64 `json:"current_period_cost_usd,omitempty"`
	RemainingDailyQuotaUSD float64 `json:"remaining_daily_quota_usd,omitempty"`
	// standard 类型：时间段消费统计
	CostToday float64 `json:"cost_today"`
	Cost30d   float64 `json:"cost_30d"`
	// 使用日志
	UsageLogs  []UsageLogEntry `json:"usage_logs,omitempty"`
	Pagination *PaginationInfo `json:"pagination,omitempty"`
}

// UsageLogEntry represents a single usage log entry for public display
type UsageLogEntry struct {
	ID                  int64   `json:"id"`
	Model               string  `json:"model"`
	InputTokens         int     `json:"input_tokens"`
	OutputTokens        int     `json:"output_tokens"`
	CacheCreationTokens int     `json:"cache_creation_tokens"`
	CacheReadTokens     int     `json:"cache_read_tokens"`
	TotalTokens         int     `json:"total_tokens"`
	ActualCost          float64 `json:"actual_cost"`
	Stream              bool    `json:"stream"`
	DurationMs          *int    `json:"duration_ms,omitempty"`
	CreatedAt           string  `json:"created_at"`
}

// PaginationInfo represents pagination information
type PaginationInfo struct {
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
	Total    int64 `json:"total"`
}

// Query queries an API key by key string (public endpoint)
// Supports both TempAPIKey and standard APIKey — tries TempAPIKey first, then falls back to APIKey.
func (h *TempAPIKeyQueryHandler) Query(c *gin.Context) {
	keyString := c.Query("key")
	if keyString == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": 1, "message": "key parameter is required"})
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "10"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 50 {
		pageSize = 10
	}

	// Try TempAPIKey first
	tempKey, err := h.repo.GetByKey(c.Request.Context(), keyString)
	if err == nil && tempKey != nil {
		h.respondTempAPIKey(c, tempKey, page, pageSize)
		return
	}

	// Fallback: try standard APIKey
	apiKey, err := h.apiKeyRepo.GetByKey(c.Request.Context(), keyString)
	if err == nil && apiKey != nil {
		h.respondAPIKey(c, apiKey, page, pageSize)
		return
	}

	c.JSON(http.StatusNotFound, gin.H{"code": 1, "message": "Key not found"})
}

// respondTempAPIKey builds and returns the response for a TempAPIKey
func (h *TempAPIKeyQueryHandler) respondTempAPIKey(c *gin.Context, key *service.TempAPIKey, page, pageSize int) {
	resp := TempAPIKeyQueryResponse{
		Name:               key.Name,
		Status:             key.Status,
		KeyType:            key.KeyType,
		ValidDays:          key.ValidDays,
		DailyLimit:         key.DailyLimit,
		CurrentPeriodCount: key.CurrentPeriodUsed(),
		TotalRequests:      key.TotalRequests,
		RemainingRequests:  key.RemainingRequests(),
		IsExpired:          key.IsExpired(),
		IsActivated:        key.IsActivated(),
	}

	if key.KeyType == service.TempAPIKeyTypeQuotaOnly {
		resp.TotalQuotaUSD = key.TotalQuotaUSD
		resp.TotalCostUSD = key.TotalCostUSD
		resp.RemainingQuotaUSD = key.RemainingQuotaUSD()
		resp.IsExhausted = key.IsExhausted()
	}

	if key.KeyType == service.TempAPIKeyTypeTimeQuota {
		resp.DailyQuotaUSD = key.DailyQuotaUSD
		resp.CurrentPeriodCostUSD = key.CurrentPeriodCostUSD
		resp.RemainingDailyQuotaUSD = key.RemainingDailyQuotaUSD()
	}

	if key.ActivatedAt != nil {
		t := key.ActivatedAt.UTC().Format(time.RFC3339)
		resp.ActivatedAt = &t
	}
	if key.ExpiresAt != nil {
		t := key.ExpiresAt.UTC().Format(time.RFC3339)
		resp.ExpiresAt = &t
	}
	if key.Group != nil {
		resp.GroupName = key.Group.Name
	}

	logs, paginationResult, err := h.usageLogRepo.ListByTempAPIKey(c.Request.Context(), key.ID, pagination.PaginationParams{
		Page:     page,
		PageSize: pageSize,
	})
	if err == nil && len(logs) > 0 {
		resp.UsageLogs = make([]UsageLogEntry, len(logs))
		for i, log := range logs {
			resp.UsageLogs[i] = UsageLogEntry{
				ID:                  log.ID,
				Model:               log.Model,
				InputTokens:         log.InputTokens,
				OutputTokens:        log.OutputTokens,
				CacheCreationTokens: log.CacheCreationTokens,
				CacheReadTokens:     log.CacheReadTokens,
				TotalTokens:         log.TotalTokens(),
				ActualCost:          log.ActualCost,
				Stream:              log.Stream,
				DurationMs:          log.DurationMs,
				CreatedAt:           log.CreatedAt.Format(time.RFC3339),
			}
		}
		if paginationResult != nil {
			resp.Pagination = &PaginationInfo{
				Page:     paginationResult.Page,
				PageSize: paginationResult.PageSize,
				Total:    paginationResult.Total,
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{"code": 0, "data": resp})
}

// respondAPIKey builds and returns the response for a standard APIKey
func (h *TempAPIKeyQueryHandler) respondAPIKey(c *gin.Context, key *service.APIKey, page, pageSize int) {
	resp := TempAPIKeyQueryResponse{
		Name:    key.Name,
		Status:  key.Status,
		KeyType: "standard",
	}

	if key.QuotaLimitUSD != nil && *key.QuotaLimitUSD > 0 {
		resp.TotalQuotaUSD = *key.QuotaLimitUSD
		resp.TotalCostUSD = key.QuotaUsedUSD
		resp.RemainingQuotaUSD = *key.QuotaLimitUSD - key.QuotaUsedUSD
		if resp.RemainingQuotaUSD < 0 {
			resp.RemainingQuotaUSD = 0
		}
		resp.IsExhausted = key.IsQuotaExceeded()
	}

	if key.Group != nil {
		resp.GroupName = key.Group.Name
	}

	createdAt := key.CreatedAt.UTC().Format(time.RFC3339)
	resp.ActivatedAt = &createdAt

	// 计算今日和近30天消费
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	days30Ago := now.AddDate(0, 0, -30)

	if todayLogs, _, err := h.usageLogRepo.ListByAPIKeyAndTimeRange(c.Request.Context(), key.ID, todayStart, now); err == nil {
		for _, log := range todayLogs {
			resp.CostToday += log.ActualCost
		}
	}
	if logs30d, _, err := h.usageLogRepo.ListByAPIKeyAndTimeRange(c.Request.Context(), key.ID, days30Ago, now); err == nil {
		for _, log := range logs30d {
			resp.Cost30d += log.ActualCost
		}
		resp.TotalRequests = int64(len(logs30d))
	}

	logs, paginationResult, err := h.usageLogRepo.ListByAPIKey(c.Request.Context(), key.ID, pagination.PaginationParams{
		Page:     page,
		PageSize: pageSize,
	})
	if err == nil && len(logs) > 0 {
		if paginationResult != nil {
			resp.TotalRequests = paginationResult.Total
		}
		resp.UsageLogs = make([]UsageLogEntry, len(logs))
		for i, log := range logs {
			resp.UsageLogs[i] = UsageLogEntry{
				ID:                  log.ID,
				Model:               log.Model,
				InputTokens:         log.InputTokens,
				OutputTokens:        log.OutputTokens,
				CacheCreationTokens: log.CacheCreationTokens,
				CacheReadTokens:     log.CacheReadTokens,
				TotalTokens:         log.TotalTokens(),
				ActualCost:          log.ActualCost,
				Stream:              log.Stream,
				DurationMs:          log.DurationMs,
				CreatedAt:           log.CreatedAt.Format(time.RFC3339),
			}
		}
		if paginationResult != nil {
			resp.Pagination = &PaginationInfo{
				Page:     paginationResult.Page,
				PageSize: paginationResult.PageSize,
				Total:    paginationResult.Total,
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{"code": 0, "data": resp})
}
