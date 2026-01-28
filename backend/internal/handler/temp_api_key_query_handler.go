package handler

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/gin-gonic/gin"
)

// TempAPIKeyQueryHandler handles public temp API key queries
type TempAPIKeyQueryHandler struct {
	repo *repository.TempAPIKeyRepo
}

// NewTempAPIKeyQueryHandler creates a new TempAPIKeyQueryHandler
func NewTempAPIKeyQueryHandler(repo *repository.TempAPIKeyRepo) *TempAPIKeyQueryHandler {
	return &TempAPIKeyQueryHandler{repo: repo}
}

// TempAPIKeyQueryResponse represents the public query response
type TempAPIKeyQueryResponse struct {
	Name               string  `json:"name"`
	GroupName          string  `json:"group_name,omitempty"`
	Status             string  `json:"status"`
	ValidDays          int     `json:"valid_days"`
	ActivatedAt        *string `json:"activated_at"`
	ExpiresAt          *string `json:"expires_at"`
	DailyLimit         int     `json:"daily_limit"`
	CurrentPeriodCount int     `json:"current_period_count"`
	TotalRequests      int64   `json:"total_requests"`
	RemainingRequests  int     `json:"remaining_requests"`
	IsExpired          bool    `json:"is_expired"`
	IsActivated        bool    `json:"is_activated"`
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
		ValidDays:          key.ValidDays,
		DailyLimit:         key.DailyLimit,
		CurrentPeriodCount: key.CurrentPeriodCount,
		TotalRequests:      key.TotalRequests,
		RemainingRequests:  key.RemainingRequests(),
		IsExpired:          key.IsExpired(),
		IsActivated:        key.IsActivated(),
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

	c.JSON(http.StatusOK, gin.H{"code": 0, "data": resp})
}
