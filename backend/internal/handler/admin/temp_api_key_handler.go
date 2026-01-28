package admin

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"

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
	Count      int    `json:"count" binding:"required,min=1,max=100"` // 批量创建数量
	NamePrefix string `json:"name_prefix" binding:"required"`         // 名称前缀
	GroupID    int64  `json:"group_id" binding:"required"`            // 关联分组
	ValidDays  int    `json:"valid_days" binding:"required,min=1"`    // 有效天数
	DailyLimit int    `json:"daily_limit"`                            // 每日限制（默认1000）
}

// UpdateTempAPIKeyRequest represents the request to update a temp API key
type UpdateTempAPIKeyRequest struct {
	Name       string `json:"name"`
	Status     string `json:"status" binding:"omitempty,oneof=active inactive"`
	ValidDays  *int   `json:"valid_days"`
	DailyLimit *int   `json:"daily_limit"`
}

// BatchUpdateRequest represents the request for batch operations
type BatchUpdateRequest struct {
	IDs        []int64 `json:"ids" binding:"required,min=1"`
	Status     *string `json:"status" binding:"omitempty,oneof=active inactive"`
	ValidDays  *int    `json:"valid_days"`
	DailyLimit *int    `json:"daily_limit"`
}

// TempAPIKeyResponse represents the response for a temp API key
type TempAPIKeyResponse struct {
	ID                 int64   `json:"id"`
	Key                string  `json:"key"`
	Name               string  `json:"name"`
	GroupID            int64   `json:"group_id"`
	GroupName          string  `json:"group_name,omitempty"`
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
}

// List lists all temp API keys with pagination
func (h *TempAPIKeyHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	params := pagination.PaginationParams{
		Page:     page,
		PageSize: pageSize,
	}

	keys, paginationResult, err := h.repo.List(c.Request.Context(), params)
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

	dailyLimit := req.DailyLimit
	if dailyLimit <= 0 {
		dailyLimit = 1000 // Default
	}

	createdKeys := make([]*service.TempAPIKey, 0, req.Count)
	for i := 0; i < req.Count; i++ {
		key := &service.TempAPIKey{
			Key:        generateTempAPIKey(),
			Name:       req.NamePrefix + "-" + strconv.Itoa(i+1),
			GroupID:    req.GroupID,
			ValidDays:  req.ValidDays,
			DailyLimit: dailyLimit,
			Status:     service.TempAPIKeyStatusActive,
			CreatedBy:  userID,
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
		ValidDays:          key.ValidDays,
		DailyLimit:         key.DailyLimit,
		CurrentPeriodCount: key.CurrentPeriodCount,
		TotalRequests:      key.TotalRequests,
		Status:             key.Status,
		CreatedBy:          key.CreatedBy,
		CreatedAt:          key.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:          key.UpdatedAt.Format("2006-01-02T15:04:05Z"),
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
	if key.Creator != nil {
		resp.CreatorEmail = key.Creator.Email
	}

	return resp
}

// generateTempAPIKey generates a random temp API key
func generateTempAPIKey() string {
	bytes := make([]byte, 24)
	rand.Read(bytes)
	return "sk-temp-" + hex.EncodeToString(bytes)
}
