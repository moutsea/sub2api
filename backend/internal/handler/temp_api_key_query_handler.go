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

// TempAPIKeyQueryHandler handles public temp API key queries
type TempAPIKeyQueryHandler struct {
	repo         *repository.TempAPIKeyRepo
	usageLogRepo service.UsageLogRepository
}

// NewTempAPIKeyQueryHandler creates a new TempAPIKeyQueryHandler
func NewTempAPIKeyQueryHandler(repo *repository.TempAPIKeyRepo, usageLogRepo service.UsageLogRepository) *TempAPIKeyQueryHandler {
	return &TempAPIKeyQueryHandler{repo: repo, usageLogRepo: usageLogRepo}
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
	// quota_only 类型专用字段
	TotalQuotaUSD     float64 `json:"total_quota_usd,omitempty"`
	TotalCostUSD      float64 `json:"total_cost_usd,omitempty"`
	RemainingQuotaUSD float64 `json:"remaining_quota_usd,omitempty"`
	IsExhausted       bool    `json:"is_exhausted,omitempty"`
	UsageLogs         []UsageLogEntry `json:"usage_logs,omitempty"`
	Pagination        *PaginationInfo `json:"pagination,omitempty"`
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

// Query queries a temp API key by key string (public endpoint)
func (h *TempAPIKeyQueryHandler) Query(c *gin.Context) {
	keyString := c.Query("key")
	if keyString == "" {
		c.JSON(http.StatusBadRequest, gin.H{"code": 1, "message": "key parameter is required"})
		return
	}

	key, err := h.repo.GetByKey(c.Request.Context(), keyString)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"code": 1, "message": "Key not found"})
		return
	}

	resp := TempAPIKeyQueryResponse{
		Name:               key.Name,
		Status:             key.Status,
		KeyType:            key.KeyType,
		ValidDays:          key.ValidDays,
		DailyLimit:         key.DailyLimit,
		CurrentPeriodCount: key.CurrentPeriodCount,
		TotalRequests:      key.TotalRequests,
		RemainingRequests:  key.RemainingRequests(),
		IsExpired:          key.IsExpired(),
		IsActivated:        key.IsActivated(),
	}

	// quota_only 类型：填充额度相关字段
	if key.KeyType == service.TempAPIKeyTypeQuotaOnly {
		resp.TotalQuotaUSD = key.TotalQuotaUSD
		resp.TotalCostUSD = key.TotalCostUSD
		resp.RemainingQuotaUSD = key.RemainingQuotaUSD()
		resp.IsExhausted = key.IsExhausted()
	}

	if key.ActivatedAt != nil {
		t := key.ActivatedAt.Format("2006-01-02T15:04:05Z")
		resp.ActivatedAt = &t
	}
	if key.ExpiresAt != nil {
		t := key.ExpiresAt.Format("2006-01-02T15:04:05Z")
		resp.ExpiresAt = &t
	}
	if key.Group != nil {
		resp.GroupName = key.Group.Name
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "10"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 50 {
		pageSize = 10
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
