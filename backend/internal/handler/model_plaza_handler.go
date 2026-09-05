package handler

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type ModelPlazaHandler struct {
	groupRepo      service.GroupRepository
	gatewayService *service.GatewayService
	billingService *service.BillingService
	modelCacheMu   sync.RWMutex
	modelCache     map[modelCacheKey]modelCacheEntry
}

type modelCacheKey struct {
	groupID  int64
	platform string
}

type modelCacheEntry struct {
	models    []string
	expiresAt time.Time
}

const modelCacheTTL = 30 * time.Second

type supportedModelsUpdater interface {
	UpdateSupportedModels(ctx context.Context, groupID int64, models []string) error
}

type PublicModelPrice struct {
	InputPricePerMTok  float64 `json:"input_price_per_mtok"`
	OutputPricePerMTok float64 `json:"output_price_per_mtok"`
}

type PublicModelChannel struct {
	ID              int64                       `json:"id"`
	Name            string                      `json:"name"`
	Description     string                      `json:"description"`
	Platform        string                      `json:"platform"`
	RateMultiplier  float64                     `json:"rate_multiplier"`
	SupportedModels []string                    `json:"supported_models"`
	ModelPrices     map[string]PublicModelPrice `json:"model_prices"`
}

type modelRequest struct {
	Model        string `json:"model"`
	CurrentModel string `json:"current_model"`
}

func NewModelPlazaHandler(groupRepo service.GroupRepository, gatewayService *service.GatewayService, billingService *service.BillingService) *ModelPlazaHandler {
	return &ModelPlazaHandler{
		groupRepo:      groupRepo,
		gatewayService: gatewayService,
		billingService: billingService,
		modelCache:     make(map[modelCacheKey]modelCacheEntry),
	}
}

func (h *ModelPlazaHandler) Public(c *gin.Context) {
	groups, err := h.groupRepo.ListActive(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	channels := make([]PublicModelChannel, 0, len(groups))
	for i := range groups {
		group := &groups[i]
		if group.IsExclusive {
			continue
		}
		models := h.effectiveModels(c, group)
		channels = append(channels, PublicModelChannel{
			ID:              group.ID,
			Name:            group.Name,
			Description:     group.Description,
			Platform:        group.Platform,
			RateMultiplier:  group.RateMultiplier,
			SupportedModels: models,
			ModelPrices:     h.modelPrices(models, group.RateMultiplier),
		})
	}

	response.Success(c, channels)
}

func (h *ModelPlazaHandler) modelPrices(models []string, rateMultiplier float64) map[string]PublicModelPrice {
	prices := make(map[string]PublicModelPrice)
	if h.billingService == nil {
		return prices
	}
	if rateMultiplier < 0 {
		rateMultiplier = 1
	}
	for _, model := range models {
		pricing, err := h.billingService.GetModelPricingForDisplay(model)
		if err != nil || pricing == nil {
			continue
		}
		prices[model] = PublicModelPrice{
			InputPricePerMTok:  pricing.InputPricePerToken * 1_000_000 * rateMultiplier,
			OutputPricePerMTok: pricing.OutputPricePerToken * 1_000_000 * rateMultiplier,
		}
	}
	return prices
}

func (h *ModelPlazaHandler) List(c *gin.Context) {
	group, ok := h.getGroup(c)
	if !ok {
		return
	}
	response.Success(c, h.effectiveModels(c, group))
}

func (h *ModelPlazaHandler) Create(c *gin.Context) {
	group, ok := h.getGroup(c)
	if !ok {
		return
	}

	var req modelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	model, ok := validateModelName(c, req.Model)
	if !ok {
		return
	}

	models := configuredModels(group)
	if len(models) == 0 && containsModel(h.effectiveModels(c, group), model) {
		response.Error(c, http.StatusConflict, "Model already exists")
		return
	}
	if containsModel(models, model) {
		response.Error(c, http.StatusConflict, "Model already exists")
		return
	}
	models = append(models, model)
	if err := h.updateSupportedModels(c.Request.Context(), group, models); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"group_id": group.ID, "model": model})
}

func (h *ModelPlazaHandler) Update(c *gin.Context) {
	group, ok := h.getGroup(c)
	if !ok {
		return
	}

	var req modelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	currentRaw := req.CurrentModel
	if currentRaw == "" {
		currentRaw = strings.TrimPrefix(c.Param("model"), "/")
	}
	currentModel, ok := validateModelName(c, currentRaw)
	if !ok {
		return
	}
	newModel, ok := validateModelName(c, req.Model)
	if !ok {
		return
	}

	models := configuredModels(group)
	currentIndex := -1
	for i, model := range models {
		if model == currentModel {
			currentIndex = i
			break
		}
	}
	if currentIndex < 0 {
		response.NotFound(c, "Model not found")
		return
	}
	if currentModel != newModel && containsModel(models, newModel) {
		response.Error(c, http.StatusConflict, "Model already exists")
		return
	}

	models[currentIndex] = newModel
	if err := h.updateSupportedModels(c.Request.Context(), group, models); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"group_id": group.ID, "model": newModel})
}

func (h *ModelPlazaHandler) Delete(c *gin.Context) {
	group, ok := h.getGroup(c)
	if !ok {
		return
	}
	modelRaw := c.Query("model")
	if modelRaw == "" {
		modelRaw = strings.TrimPrefix(c.Param("model"), "/")
	}
	model, ok := validateModelName(c, modelRaw)
	if !ok {
		return
	}

	models := configuredModels(group)
	updated := make([]string, 0, len(models))
	found := false
	for _, current := range models {
		if current == model {
			found = true
			continue
		}
		updated = append(updated, current)
	}
	if !found {
		response.NotFound(c, "Model not found")
		return
	}

	if err := h.updateSupportedModels(c.Request.Context(), group, updated); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "Model deleted successfully"})
}

func (h *ModelPlazaHandler) getGroup(c *gin.Context) (*service.Group, bool) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || groupID <= 0 {
		response.BadRequest(c, "Invalid group ID")
		return nil, false
	}
	group, err := h.groupRepo.GetByID(c.Request.Context(), groupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return nil, false
	}
	return group, true
}

func (h *ModelPlazaHandler) effectiveModels(c *gin.Context, group *service.Group) []string {
	models := configuredModels(group)
	if len(models) > 0 || h.gatewayService == nil {
		return models
	}

	key := modelCacheKey{groupID: group.ID, platform: group.Platform}
	now := time.Now()
	h.modelCacheMu.RLock()
	entry, ok := h.modelCache[key]
	h.modelCacheMu.RUnlock()
	if ok && now.Before(entry.expiresAt) {
		return append([]string(nil), entry.models...)
	}

	models = service.NormalizeSupportedModels(h.gatewayService.GetAvailableModels(c.Request.Context(), &group.ID, group.Platform))
	h.modelCacheMu.Lock()
	if h.modelCache == nil {
		h.modelCache = make(map[modelCacheKey]modelCacheEntry)
	}
	h.modelCache[key] = modelCacheEntry{models: append([]string(nil), models...), expiresAt: now.Add(modelCacheTTL)}
	h.modelCacheMu.Unlock()
	return models
}

func (h *ModelPlazaHandler) updateSupportedModels(ctx context.Context, group *service.Group, models []string) error {
	models = service.NormalizeSupportedModels(models)
	if updater, ok := h.groupRepo.(supportedModelsUpdater); ok {
		if err := updater.UpdateSupportedModels(ctx, group.ID, models); err != nil {
			return err
		}
	} else {
		group.SupportedModels = models
		if err := h.groupRepo.Update(ctx, group); err != nil {
			return err
		}
	}
	group.SupportedModels = models
	h.invalidateModelCache(group)
	return nil
}

func (h *ModelPlazaHandler) invalidateModelCache(group *service.Group) {
	if group == nil {
		return
	}
	h.modelCacheMu.Lock()
	delete(h.modelCache, modelCacheKey{groupID: group.ID, platform: group.Platform})
	h.modelCacheMu.Unlock()
}

func configuredModels(group *service.Group) []string {
	if group == nil {
		return nil
	}
	return service.NormalizeSupportedModels(group.SupportedModels)
}

func validateModelName(c *gin.Context, raw string) (string, bool) {
	model := strings.TrimSpace(raw)
	if model == "" {
		response.BadRequest(c, "Model name is required")
		return "", false
	}
	if len(model) > 200 {
		response.BadRequest(c, "Model name must not exceed 200 characters")
		return "", false
	}
	return model, true
}

func containsModel(models []string, target string) bool {
	for _, model := range models {
		if model == target {
			return true
		}
	}
	return false
}
