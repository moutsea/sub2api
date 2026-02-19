package admin

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// TempAPIKeyHandler handles temp API key management
type TempAPIKeyHandler struct {
	repo *repository.TempAPIKeyRepo
}

// NewTempAPIKeyHandler creates a new TempAPIKeyHandler
func NewTempAPIKeyHandler(repo *repository.TempAPIKeyRepo) *TempAPIKeyHandler {
	return &TempAPIKeyHandler{repo: repo}
}

// CreateTempAPIKeyRequest represents the request to create temp API keys
type CreateTempAPIKeyRequest struct {
	Count      int     `json:"count" binding:"required,min=1,max=100"` // 批量创建数量
	NamePrefix string  `json:"name_prefix" binding:"required"`         // 名称前缀
	GroupID    int64   `json:"group_id" binding:"required"`            // 关联分组
	KeyType    string  `json:"key_type"`                               // 类型：time_limited（默认）、quota_only
	ValidDays  int     `json:"valid_days"`                             // 有效天数（time_limited 必填）
	DailyLimit int     `json:"daily_limit"`                            // 每日限制（time_limited 使用，默认1000）
	TotalQuota float64 `json:"total_quota"`                            // 总额度（quota_only 必填，单位 USD）
}

// UpdateTempAPIKeyRequest represents the request to update a temp API key
type UpdateTempAPIKeyRequest struct {
	Name       string   `json:"name"`
	Status     string   `json:"status" binding:"omitempty,oneof=active inactive exhausted"`
	ValidDays  *int     `json:"valid_days"`
	DailyLimit *int     `json:"daily_limit"`
	TotalQuota *float64 `json:"total_quota"`
}

// BatchUpdateRequest represents the request for batch operations
type BatchUpdateRequest struct {
	IDs        []int64 `json:"ids" binding:"required,min=1"`
	Status     *string `json:"status" binding:"omitempty,oneof=active inactive"`
	ValidDays  *int    `json:"valid_days"`
	DailyLimit *int    `json:"daily_limit"`
	NamePrefix *string `json:"name_prefix"`
}

// TempAPIKeyResponse represents the response for a temp API key
type TempAPIKeyResponse struct {
	ID                 int64   `json:"id"`
	Key                string  `json:"key"`
	Name               string  `json:"name"`
	GroupID            int64   `json:"group_id"`
	GroupName          string  `json:"group_name,omitempty"`
	KeyType            string  `json:"key_type"`
	TotalQuota         float64 `json:"total_quota"`
	TotalCost          float64 `json:"total_cost"`
	ValidDays          int     `json:"valid_days"`
	ActivatedAt        *string `json:"activated_at"`
	ExpiresAt          *string `json:"expires_at"`
	DailyLimit         int     `json:"daily_limit"`
	CurrentPeriodCount int     `json:"current_period_count"`
	TotalRequests      int64   `json:"total_requests"`
	Status             string  `json:"status"`
	CreatedBy          int64   `json:"created_by"`
	CreatorEmail       string  `json:"creator_email,omitempty"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
	RemainingRequests  int     `json:"remaining_requests"`
	IsExpired          bool    `json:"is_expired"`
	IsActivated        bool    `json:"is_activated"`
	IsExhausted        bool    `json:"is_exhausted"`
}

// List lists all temp API keys with pagination
func (h *TempAPIKeyHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	params := pagination.PaginationParams{
		Page:     page,
		PageSize: pageSize,
	}

	groupID, _ := strconv.ParseInt(c.Query("group_id"), 10, 64)
	filters := repository.TempAPIKeyListFilters{
		KeyType:   c.Query("key_type"),
		Status:    c.Query("status"),
		GroupID:   groupID,
		Search:    c.Query("search"),
		Activated: c.Query("activated"),
	}

	keys, paginationResult, err := h.repo.List(c.Request.Context(), params, filters)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	responses := make([]TempAPIKeyResponse, len(keys))
	for i, key := range keys {
		responses[i] = h.toResponse(key)
	}

	c.JSON(http.StatusOK, gin.H{
		"data":       responses,
		"pagination": paginationResult,
	})
}

// Create creates new temp API keys in batch
func (h *TempAPIKeyHandler) Create(c *gin.Context) {
	var req CreateTempAPIKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Get current admin user ID from context
	authSubject, exists := c.Get("user")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	userID := authSubject.(middleware.AuthSubject).UserID

	// 确定 key 类型
	keyType := req.KeyType
	if keyType == "" {
		keyType = service.TempAPIKeyTypeLimited // 默认限时限额
	}

	// 验证参数
	if keyType == service.TempAPIKeyTypeQuotaOnly {
		if req.TotalQuota <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "total_quota is required for quota_only type"})
			return
		}
	} else {
		if req.ValidDays <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "valid_days is required for time_limited type"})
			return
		}
	}

	dailyLimit := req.DailyLimit
	if dailyLimit <= 0 {
		dailyLimit = 1000 // Default
	}

	createdKeys := make([]*service.TempAPIKey, 0, req.Count)
	for i := 0; i < req.Count; i++ {
		key := &service.TempAPIKey{
			Key:           generateTempAPIKey(),
			Name:          req.NamePrefix + "-" + strconv.Itoa(i+1),
			GroupID:       req.GroupID,
			KeyType:       keyType,
			TotalQuotaUSD: req.TotalQuota,
			ValidDays:     req.ValidDays,
			DailyLimit:    dailyLimit,
			Status:        service.TempAPIKeyStatusActive,
			CreatedBy:     userID,
		}

		if err := h.repo.Create(c.Request.Context(), key); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":   err.Error(),
				"created": len(createdKeys),
			})
			return
		}
		createdKeys = append(createdKeys, key)
	}

	responses := make([]TempAPIKeyResponse, len(createdKeys))
	for i, key := range createdKeys {
		responses[i] = h.toResponse(key)
	}

	c.JSON(http.StatusCreated, gin.H{
		"data":    responses,
		"created": len(createdKeys),
	})
}

// GetByID gets a temp API key by ID
func (h *TempAPIKeyHandler) GetByID(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	key, err := h.repo.GetByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "temp API key not found"})
		return
	}

	c.JSON(http.StatusOK, h.toResponse(key))
}

// Update updates a temp API key
func (h *TempAPIKeyHandler) Update(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	var req UpdateTempAPIKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	key, err := h.repo.GetByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "temp API key not found"})
		return
	}

	if req.Name != "" {
		key.Name = req.Name
	}
	if req.Status != "" {
		key.Status = req.Status
	}
	if req.ValidDays != nil {
		key.ValidDays = *req.ValidDays
	}
	if req.DailyLimit != nil {
		key.DailyLimit = *req.DailyLimit
	}
	if req.TotalQuota != nil {
		key.TotalQuotaUSD = *req.TotalQuota
	}

	if err := h.repo.Update(c.Request.Context(), key); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, h.toResponse(key))
}

// Delete deletes a temp API key
func (h *TempAPIKeyHandler) Delete(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	if err := h.repo.Delete(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

// BatchDelete deletes multiple temp API keys
func (h *TempAPIKeyHandler) BatchDelete(c *gin.Context) {
	var req struct {
		IDs []int64 `json:"ids" binding:"required,min=1"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	deleted, err := h.repo.BatchDelete(c.Request.Context(), req.IDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"deleted": deleted})
}

// BatchUpdate updates multiple temp API keys
func (h *TempAPIKeyHandler) BatchUpdate(c *gin.Context) {
	var req BatchUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	updated := 0
	var lastErr error

	if req.Status != nil {
		n, err := h.repo.BatchUpdateStatus(c.Request.Context(), req.IDs, *req.Status)
		if err != nil {
			lastErr = err
		} else {
			updated = n
		}
	}

	if req.ValidDays != nil {
		n, err := h.repo.BatchUpdateValidDays(c.Request.Context(), req.IDs, *req.ValidDays)
		if err != nil {
			lastErr = err
		} else {
			updated = n
		}
	}

	if req.DailyLimit != nil {
		n, err := h.repo.BatchUpdateDailyLimit(c.Request.Context(), req.IDs, *req.DailyLimit)
		if err != nil {
			lastErr = err
		} else {
			updated = n
		}
	}

	if req.NamePrefix != nil && *req.NamePrefix != "" {
		n, err := h.repo.BatchUpdateNamePrefix(c.Request.Context(), req.IDs, *req.NamePrefix)
		if err != nil {
			lastErr = err
		} else {
			updated = n
		}
	}

	if lastErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": lastErr.Error(), "updated": updated})
		return
	}

	c.JSON(http.StatusOK, gin.H{"updated": updated})
}

// toResponse converts service model to response
func (h *TempAPIKeyHandler) toResponse(key *service.TempAPIKey) TempAPIKeyResponse {
	resp := TempAPIKeyResponse{
		ID:                 key.ID,
		Key:                key.Key,
		Name:               key.Name,
		GroupID:            key.GroupID,
		KeyType:            key.KeyType,
		TotalQuota:         key.TotalQuotaUSD,
		TotalCost:          key.TotalCostUSD,
		ValidDays:          key.ValidDays,
		DailyLimit:         key.DailyLimit,
		CurrentPeriodCount: key.CurrentPeriodUsed(),
		TotalRequests:      key.TotalRequests,
		Status:             key.Status,
		CreatedBy:          key.CreatedBy,
		CreatedAt:          key.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:          key.UpdatedAt.UTC().Format(time.RFC3339),
		RemainingRequests:  key.RemainingRequests(),
		IsExpired:          key.IsExpired(),
		IsActivated:        key.IsActivated(),
		IsExhausted:        key.IsExhausted(),
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
	if key.Creator != nil {
		resp.CreatorEmail = key.Creator.Email
	}

	return resp
}

// CleanupExpired deletes temp API keys that expired more than 1 day ago
func (h *TempAPIKeyHandler) CleanupExpired(c *gin.Context) {
	before := time.Now().Add(-24 * time.Hour)
	deleted, err := h.repo.DeleteExpiredBefore(c.Request.Context(), before)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": deleted})
}

// RecalculateCounts recalculates current_period_count for all active time_limited keys
// by counting actual usage_logs within each key's 24-hour period window.
func (h *TempAPIKeyHandler) RecalculateCounts(c *gin.Context) {
	updated, err := h.repo.RecalculatePeriodCounts(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"updated": updated})
}

// generateTempAPIKey generates a random temp API key
func generateTempAPIKey() string {
	bytes := make([]byte, 24)
	rand.Read(bytes)
	return "sk-cc-" + hex.EncodeToString(bytes)
}
