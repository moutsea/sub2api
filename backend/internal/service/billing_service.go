package service

import (
	"context"
	"fmt"

	"log"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

// BillingCache defines cache operations for billing service
type BillingCache interface {
	// Balance operations
	GetUserBalance(ctx context.Context, userID int64) (float64, error)
	SetUserBalance(ctx context.Context, userID int64, balance float64) error
	DeductUserBalance(ctx context.Context, userID int64, amount float64) error
	InvalidateUserBalance(ctx context.Context, userID int64) error

	// Subscription operations
	GetSubscriptionCache(ctx context.Context, userID, groupID int64) (*SubscriptionCacheData, error)
	SetSubscriptionCache(ctx context.Context, userID, groupID int64, data *SubscriptionCacheData) error
	UpdateSubscriptionUsage(ctx context.Context, userID, groupID int64, cost float64) error
	InvalidateSubscriptionCache(ctx context.Context, userID, groupID int64) error
}

// ModelPricing 模型价格配置（per-token价格，与LiteLLM格式一致）
type ModelPricing struct {
	InputPricePerToken             float64 // 每token输入价格 (USD)
	InputPricePerTokenPriority     float64 // priority service tier 下每token输入价格 (USD)
	OutputPricePerToken            float64 // 每token输出价格 (USD)
	OutputPricePerTokenPriority    float64 // priority service tier 下每token输出价格 (USD)
	CacheCreationPricePerToken     float64 // 缓存创建每token价格 (USD)
	CacheReadPricePerToken         float64 // 缓存读取每token价格 (USD)
	CacheReadPricePerTokenPriority float64 // priority service tier 下缓存读取每token价格 (USD)
	CacheCreation5mPrice           float64 // 5分钟缓存创建价格（每百万token）- 仅用于硬编码回退
	CacheCreation1hPrice           float64 // 1小时缓存创建价格（每百万token）- 仅用于硬编码回退
	SupportsCacheBreakdown         bool    // 是否支持详细的缓存分类
	LongContextInputThreshold      int     // 超过阈值后按整次会话提升输入价格
	LongContextThresholdInclusive  bool    // true 时阈值本身也按长上下文计价
	LongContextInputMultiplier     float64 // 长上下文整次会话输入倍率
	LongContextOutputMultiplier    float64 // 长上下文整次会话输出倍率
}

const (
	openAIGPT54LongContextInputThreshold   = 272000
	openAIGPT54LongContextInputMultiplier  = 2.0
	openAIGPT54LongContextOutputMultiplier = 1.5
)

func normalizeBillingServiceTier(serviceTier string) string {
	return strings.ToLower(strings.TrimSpace(serviceTier))
}

func usePriorityServiceTierPricing(serviceTier string, pricing *ModelPricing) bool {
	if pricing == nil || normalizeBillingServiceTier(serviceTier) != "priority" {
		return false
	}
	return pricing.InputPricePerTokenPriority > 0 ||
		pricing.OutputPricePerTokenPriority > 0 ||
		pricing.CacheReadPricePerTokenPriority > 0
}

// serviceTierCostMultiplier returns a generic cost multiplier for service tiers
// that do NOT have dedicated per-token pricing fields in ModelPricing.
// This is only applied when usePriorityServiceTierPricing returns false —
// i.e., the tier is not "priority" or the model lacks priority-specific prices.
// For "flex" tier, OpenAI charges 50% of base price across all token types.
func serviceTierCostMultiplier(serviceTier string) float64 {
	switch normalizeBillingServiceTier(serviceTier) {
	case "priority":
		return 2.0
	case "flex":
		return 0.5
	default:
		return 1.0
	}
}

// UsageTokens 使用的token数量
type UsageTokens struct {
	InputTokens           int
	OutputTokens          int
	CacheCreationTokens   int
	CacheReadTokens       int
	CacheCreation5mTokens int
	CacheCreation1hTokens int
}

// CostBreakdown 费用明细
type CostBreakdown struct {
	InputCost         float64
	OutputCost        float64
	CacheCreationCost float64
	CacheReadCost     float64
	TotalCost         float64
	ActualCost        float64 // 应用倍率后的实际费用
}

// BillingService 计费服务
type BillingService struct {
	cfg            *config.Config
	pricingService *PricingService
	fallbackPrices map[string]*ModelPricing // 硬编码回退价格
}

// NewBillingService 创建计费服务实例
func NewBillingService(cfg *config.Config, pricingService *PricingService) *BillingService {
	s := &BillingService{
		cfg:            cfg,
		pricingService: pricingService,
		fallbackPrices: make(map[string]*ModelPricing),
	}

	// 初始化硬编码回退价格（当动态价格不可用时使用）
	s.initFallbackPricing()

	return s
}

// initFallbackPricing 初始化硬编码回退价格（当动态价格不可用时使用）
// 价格单位：USD per token（与LiteLLM格式一致）
func (s *BillingService) initFallbackPricing() {
	// Claude Opus 5 (same pricing as Opus 4.8)
	s.fallbackPrices["claude-opus-5"] = &ModelPricing{
		InputPricePerToken:         5e-6,
		OutputPricePerToken:        25e-6,
		CacheCreationPricePerToken: 6.25e-6,
		CacheReadPricePerToken:     0.5e-6,
		SupportsCacheBreakdown:     false,
	}

	// Claude Opus 4.8
	s.fallbackPrices["claude-opus-4-8"] = &ModelPricing{
		InputPricePerToken:         5e-6,
		OutputPricePerToken:        25e-6,
		CacheCreationPricePerToken: 6.25e-6,
		CacheReadPricePerToken:     0.5e-6,
		SupportsCacheBreakdown:     false,
	}

	// Claude Opus 4.6
	s.fallbackPrices["claude-opus-4-6"] = &ModelPricing{
		InputPricePerToken:         5e-6,    // $5 per MTok
		OutputPricePerToken:        25e-6,   // $25 per MTok
		CacheCreationPricePerToken: 6.25e-6, // $6.25 per MTok
		CacheReadPricePerToken:     0.5e-6,  // $0.50 per MTok
		SupportsCacheBreakdown:     false,
	}

	// Claude Opus 4.6 (1M context, input tokens > 200K)
	s.fallbackPrices["claude-opus-4-6-1m"] = &ModelPricing{
		InputPricePerToken:         10e-6,   // $10 per MTok
		OutputPricePerToken:        37.5e-6, // $37.5 per MTok
		CacheCreationPricePerToken: 12.5e-6, // $12.5 per MTok (2x standard)
		CacheReadPricePerToken:     1e-6,    // $1.0 per MTok (2x standard)
		SupportsCacheBreakdown:     false,
	}

	// Claude Sonnet 4.6
	s.fallbackPrices["claude-sonnet-4-6"] = &ModelPricing{
		InputPricePerToken:         3e-6,    // $3 per MTok
		OutputPricePerToken:        15e-6,   // $15 per MTok
		CacheCreationPricePerToken: 3.75e-6, // $3.75 per MTok
		CacheReadPricePerToken:     0.3e-6,  // $0.30 per MTok
		SupportsCacheBreakdown:     false,
	}

	// Claude Sonnet 5
	s.fallbackPrices["claude-sonnet-5"] = &ModelPricing{
		InputPricePerToken:         3e-6,    // $3 per MTok
		OutputPricePerToken:        15e-6,   // $15 per MTok
		CacheCreationPricePerToken: 3.75e-6, // $3.75 per MTok
		CacheReadPricePerToken:     0.3e-6,  // $0.30 per MTok
		SupportsCacheBreakdown:     false,
	}

	// Claude Sonnet 4.6 (1M context)
	s.fallbackPrices["claude-sonnet-4-6-1m"] = &ModelPricing{
		InputPricePerToken:         6e-6,    // $6 per MTok
		OutputPricePerToken:        22.5e-6, // $22.5 per MTok
		CacheCreationPricePerToken: 7.5e-6,  // $7.5 per MTok (2x standard)
		CacheReadPricePerToken:     0.6e-6,  // $0.60 per MTok (2x standard)
		SupportsCacheBreakdown:     false,
	}

	// Claude 4.5 Opus
	s.fallbackPrices["claude-opus-4.5"] = &ModelPricing{
		InputPricePerToken:         5e-6,    // $5 per MTok
		OutputPricePerToken:        25e-6,   // $25 per MTok
		CacheCreationPricePerToken: 6.25e-6, // $6.25 per MTok
		CacheReadPricePerToken:     0.5e-6,  // $0.50 per MTok
		SupportsCacheBreakdown:     false,
	}

	// Claude 4 Sonnet
	s.fallbackPrices["claude-sonnet-4"] = &ModelPricing{
		InputPricePerToken:         3e-6,    // $3 per MTok
		OutputPricePerToken:        15e-6,   // $15 per MTok
		CacheCreationPricePerToken: 3.75e-6, // $3.75 per MTok
		CacheReadPricePerToken:     0.3e-6,  // $0.30 per MTok
		SupportsCacheBreakdown:     false,
	}

	// Claude 3.5 Sonnet
	s.fallbackPrices["claude-3-5-sonnet"] = &ModelPricing{
		InputPricePerToken:         3e-6,    // $3 per MTok
		OutputPricePerToken:        15e-6,   // $15 per MTok
		CacheCreationPricePerToken: 3.75e-6, // $3.75 per MTok
		CacheReadPricePerToken:     0.3e-6,  // $0.30 per MTok
		SupportsCacheBreakdown:     false,
	}

	// Claude 3.5 Haiku
	s.fallbackPrices["claude-3-5-haiku"] = &ModelPricing{
		InputPricePerToken:         1e-6,    // $1 per MTok
		OutputPricePerToken:        5e-6,    // $5 per MTok
		CacheCreationPricePerToken: 1.25e-6, // $1.25 per MTok
		CacheReadPricePerToken:     0.1e-6,  // $0.10 per MTok
		SupportsCacheBreakdown:     false,
	}

	// Claude 3 Opus
	s.fallbackPrices["claude-3-opus"] = &ModelPricing{
		InputPricePerToken:         15e-6,    // $15 per MTok
		OutputPricePerToken:        75e-6,    // $75 per MTok
		CacheCreationPricePerToken: 18.75e-6, // $18.75 per MTok
		CacheReadPricePerToken:     1.5e-6,   // $1.50 per MTok
		SupportsCacheBreakdown:     false,
	}

	// Claude 3 Haiku
	s.fallbackPrices["claude-3-haiku"] = &ModelPricing{
		InputPricePerToken:         0.25e-6, // $0.25 per MTok
		OutputPricePerToken:        1.25e-6, // $1.25 per MTok
		CacheCreationPricePerToken: 0.3e-6,  // $0.30 per MTok
		CacheReadPricePerToken:     0.03e-6, // $0.03 per MTok
		SupportsCacheBreakdown:     false,
	}

	// xAI Grok 4.3 and Grok 4.20 use the same public text-model card:
	// $1.25 input / $0.20 cached input / $2.50 output per million tokens.
	s.fallbackPrices["grok-4.3"] = &ModelPricing{
		InputPricePerToken:            1.25e-6,
		OutputPricePerToken:           2.5e-6,
		CacheReadPricePerToken:        0.2e-6,
		SupportsCacheBreakdown:        false,
		LongContextInputThreshold:     200000,
		LongContextThresholdInclusive: true,
		LongContextInputMultiplier:    2,
		LongContextOutputMultiplier:   2,
	}
	// Grok 4.5: $2 input / $0.30 cached input / $6 output per million tokens;
	// long-context requests (>=200K input, including cache tokens) are 2x.
	s.fallbackPrices["grok-4.5"] = &ModelPricing{
		InputPricePerToken:            2e-6,
		OutputPricePerToken:           6e-6,
		CacheReadPricePerToken:        0.3e-6,
		SupportsCacheBreakdown:        false,
		LongContextInputThreshold:     200000,
		LongContextThresholdInclusive: true,
		LongContextInputMultiplier:    2,
		LongContextOutputMultiplier:   2,
	}
	// Grok 4.6: $2 input / $0.50 cached input / $6 output per million tokens;
	// long-context requests (>=200K input, including cache tokens) are 2x.
	s.fallbackPrices["grok-4.6"] = &ModelPricing{
		InputPricePerToken:            2e-6,
		OutputPricePerToken:           6e-6,
		CacheReadPricePerToken:        0.5e-6,
		SupportsCacheBreakdown:        false,
		LongContextInputThreshold:     200000,
		LongContextThresholdInclusive: true,
		LongContextInputMultiplier:    2,
		LongContextOutputMultiplier:   2,
	}
	s.fallbackPrices["grok-3-mini"] = &ModelPricing{
		InputPricePerToken: 0.30e-6, OutputPricePerToken: 0.50e-6,
		CacheReadPricePerToken: 0.075e-6, SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["grok-3-mini-fast"] = &ModelPricing{
		InputPricePerToken: 0.60e-6, OutputPricePerToken: 4e-6,
		CacheReadPricePerToken: 0.15e-6, SupportsCacheBreakdown: false,
	}
	s.fallbackPrices["grok-4.20"] = &ModelPricing{
		InputPricePerToken:            1.25e-6,
		OutputPricePerToken:           2.5e-6,
		CacheReadPricePerToken:        0.2e-6,
		SupportsCacheBreakdown:        false,
		LongContextInputThreshold:     200000,
		LongContextThresholdInclusive: true,
		LongContextInputMultiplier:    2,
		LongContextOutputMultiplier:   2,
	}
	// Grok Build 0.1 (including its short aliases):
	// $1 input / $0.20 cached input / $2 output per million tokens.
	s.fallbackPrices["grok-build-0.1"] = &ModelPricing{
		InputPricePerToken:            1e-6,
		OutputPricePerToken:           2e-6,
		CacheReadPricePerToken:        0.2e-6,
		SupportsCacheBreakdown:        false,
		LongContextInputThreshold:     200000,
		LongContextThresholdInclusive: true,
		LongContextInputMultiplier:    2,
		LongContextOutputMultiplier:   2,
	}

	s.fallbackPrices["gemini-3.8-flash"] = &ModelPricing{
		InputPricePerToken:             7.5e-07,
		InputPricePerTokenPriority:     1.35e-06,
		OutputPricePerToken:            3.75e-06,
		OutputPricePerTokenPriority:    6.75e-06,
		CacheReadPricePerToken:         7.5e-08,
		CacheReadPricePerTokenPriority: 1.35e-07,
		SupportsCacheBreakdown:         false,
	}
}

// getFallbackPricing 根据模型系列获取回退价格
func (s *BillingService) getFallbackPricing(model string) *ModelPricing {
	modelLower := strings.ToLower(strings.TrimSpace(xai.StripProviderPrefix(model)))
	modelLower = strings.TrimPrefix(modelLower, "models/")
	if modelLower == "gemini-3.8-flash" || strings.HasSuffix(modelLower, "/gemini-3.8-flash") {
		return s.fallbackPrices["gemini-3.8-flash"]
	}

	// Grok is an OpenAI-compatible channel, so its usage is billed by the
	// model sent in the request rather than by the Claude compatibility name.
	// Keep this branch ahead of the Claude family checks because Grok model IDs
	// do not contain Claude family markers.
	switch {
	case modelLower == "grok-4.6", modelLower == "grok-4.6-latest", strings.HasPrefix(modelLower, "grok-4.6-"):
		return s.fallbackPrices["grok-4.6"]
	case modelLower == "grok-4.5", modelLower == "grok-4.5-latest", strings.HasPrefix(modelLower, "grok-4.5-"):
		return s.fallbackPrices["grok-4.5"]
	case modelLower == "grok-3-mini-fast", strings.HasPrefix(modelLower, "grok-3-mini-fast-"):
		return s.fallbackPrices["grok-3-mini-fast"]
	case modelLower == "grok-3-mini", strings.HasPrefix(modelLower, "grok-3-mini-"):
		return s.fallbackPrices["grok-3-mini"]
	case modelLower == "grok", modelLower == "grok-latest":
		return s.fallbackPrices["grok-4.6"]
	case modelLower == "grok-4.3", strings.HasPrefix(modelLower, "grok-4.3-"), modelLower == "grok-4-3", strings.HasPrefix(modelLower, "grok-4-3-"):
		return s.fallbackPrices["grok-4.3"]
	case modelLower == "grok-4.20", strings.HasPrefix(modelLower, "grok-4.20-"):
		return s.fallbackPrices["grok-4.20"]
	case modelLower == "grok-build", modelLower == "grok-build-latest", modelLower == "grok-build-0.1", strings.HasPrefix(modelLower, "grok-build-0.1-"),
		modelLower == "grok-composer", modelLower == "grok-composer-2.5-fast", modelLower == "composer-2.5":
		return s.fallbackPrices["grok-build-0.1"]
	case strings.HasPrefix(modelLower, "grok-"):
		// Preserve billing for newly released xAI model IDs until their
		// provider-specific price is added to the remote catalog. Use the
		// current default text card rather than an older model's price.
		log.Printf("[Billing] Unknown Grok model %q using %s fallback pricing", model, xai.DefaultTextModel)
		return s.fallbackPrices["grok-4.6"]
	}
	if !strings.Contains(modelLower, "claude") &&
		!strings.Contains(modelLower, "opus") &&
		!strings.Contains(modelLower, "sonnet") &&
		!strings.Contains(modelLower, "haiku") {
		return nil
	}

	// 按模型系列匹配
	if strings.Contains(modelLower, "opus") {
		if isClaudeOpus5Model(modelLower) {
			return s.fallbackPrices["claude-opus-5"]
		}
		if isClaudeOpus48Model(modelLower) {
			return s.fallbackPrices["claude-opus-4-8"]
		}
		if strings.Contains(modelLower, "4.6") || strings.Contains(modelLower, "4-6") {
			return s.fallbackPrices["claude-opus-4-6"]
		}
		if strings.Contains(modelLower, "4.5") || strings.Contains(modelLower, "4-5") {
			return s.fallbackPrices["claude-opus-4.5"]
		}
		return s.fallbackPrices["claude-3-opus"]
	}
	if strings.Contains(modelLower, "sonnet") {
		if isClaudeSonnet5Model(modelLower) {
			return s.fallbackPrices["claude-sonnet-5"]
		}
		if strings.Contains(modelLower, "4.6") || strings.Contains(modelLower, "4-6") {
			return s.fallbackPrices["claude-sonnet-4-6"]
		}
		if strings.Contains(modelLower, "4") && !strings.Contains(modelLower, "3") {
			return s.fallbackPrices["claude-sonnet-4"]
		}
		return s.fallbackPrices["claude-3-5-sonnet"]
	}
	if strings.Contains(modelLower, "haiku") {
		if strings.Contains(modelLower, "3-5") || strings.Contains(modelLower, "3.5") {
			return s.fallbackPrices["claude-3-5-haiku"]
		}
		return s.fallbackPrices["claude-3-haiku"]
	}

	// 默认使用Sonnet价格
	return s.fallbackPrices["claude-sonnet-4"]
}

func isClaudeSonnet5Model(model string) bool {
	return strings.Contains(strings.ToLower(model), "sonnet-5")
}

func isClaudeOpus5Model(model string) bool {
	return containsModelFamilyToken(model, "opus-5")
}

func isClaudeOpus48Model(model string) bool {
	return containsModelFamilyToken(model, "opus-4-8") || containsModelFamilyToken(model, "opus-4.8")
}

func containsModelFamilyToken(model, family string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	family = strings.ToLower(strings.TrimSpace(family))
	if model == "" || family == "" {
		return false
	}

	for searchFrom := 0; searchFrom < len(model); {
		relativeIndex := strings.Index(model[searchFrom:], family)
		if relativeIndex < 0 {
			return false
		}
		index := searchFrom + relativeIndex
		end := index + len(family)
		beforeBoundary := index == 0 || !isASCIIAlphaNumeric(model[index-1])
		afterBoundary := end == len(model) || !isASCIIAlphaNumeric(model[end])
		if beforeBoundary && afterBoundary {
			return true
		}
		searchFrom = index + 1
	}
	return false
}

func isASCIIAlphaNumeric(char byte) bool {
	return char >= 'a' && char <= 'z' || char >= '0' && char <= '9'
}

// GetModelPricing 获取模型价格配置
func (s *BillingService) GetModelPricing(model string) (*ModelPricing, error) {
	// 标准化模型名称（转小写）
	model = strings.ToLower(model)

	if isClaudeSonnet5Model(model) {
		if fallback := s.getFallbackPricing(model); fallback != nil {
			return applyModelSpecificPricingPolicy(model, fallback), nil
		}
	}

	// 1. 优先从动态价格服务获取
	if s.pricingService != nil {
		litellmPricing := s.pricingService.GetModelPricing(model)
		if litellmPricing != nil {
			return applyModelSpecificPricingPolicy(model, modelPricingFromLiteLLM(litellmPricing)), nil
		}
	}

	// 2. 使用硬编码回退价格
	fallback := s.getFallbackPricing(model)
	if fallback != nil {
		log.Printf("[Billing] Using fallback pricing for model: %s", model)
		return applyModelSpecificPricingPolicy(model, fallback), nil
	}

	return nil, fmt.Errorf("pricing not found for model: %s", model)
}

// GetModelPricingForDisplay returns a price only when it is explicitly known.
// Unlike GetModelPricing, it does not use broad provider fallbacks intended to
// keep billing operational for newly released model IDs.
func (s *BillingService) GetModelPricingForDisplay(model string) (*ModelPricing, error) {
	if s == nil {
		return nil, fmt.Errorf("billing service unavailable")
	}
	model = strings.ToLower(strings.TrimSpace(model))
	if s.pricingService != nil {
		if litellmPricing := s.pricingService.GetModelPricingExact(model); litellmPricing != nil {
			return applyModelSpecificPricingPolicy(model, modelPricingFromLiteLLM(litellmPricing)), nil
		}
	}

	for _, candidate := range []string{
		model,
		normalizeModelNameForPricing(model),
		strings.TrimPrefix(model, "models/"),
		lastSegment(model),
	} {
		if pricing, ok := s.fallbackPrices[candidate]; ok {
			return applyModelSpecificPricingPolicy(model, pricing), nil
		}
	}
	return nil, fmt.Errorf("display pricing not found for model: %s", model)
}

func modelPricingFromLiteLLM(pricing *LiteLLMModelPricing) *ModelPricing {
	if pricing == nil {
		return nil
	}
	return &ModelPricing{
		InputPricePerToken:             pricing.InputCostPerToken,
		InputPricePerTokenPriority:     pricing.InputCostPerTokenPriority,
		OutputPricePerToken:            pricing.OutputCostPerToken,
		OutputPricePerTokenPriority:    pricing.OutputCostPerTokenPriority,
		CacheCreationPricePerToken:     pricing.CacheCreationInputTokenCost,
		CacheReadPricePerToken:         pricing.CacheReadInputTokenCost,
		CacheReadPricePerTokenPriority: pricing.CacheReadInputTokenCostPriority,
		SupportsCacheBreakdown:         false,
		LongContextInputThreshold:      pricing.LongContextInputTokenThreshold,
		LongContextInputMultiplier:     pricing.LongContextInputCostMultiplier,
		LongContextOutputMultiplier:    pricing.LongContextOutputCostMultiplier,
	}
}

func applyModelSpecificPricingPolicy(model string, pricing *ModelPricing) *ModelPricing {
	if pricing == nil {
		return nil
	}

	normalized := *pricing
	if hasOpenAINoSeparateCacheCreationPrice(model) {
		normalized.CacheCreationPricePerToken = 0
		normalized.CacheCreation5mPrice = 0
		normalized.CacheCreation1hPrice = 0
		normalized.SupportsCacheBreakdown = false
	}
	if isOpenAIGPT54Model(model) {
		if normalized.LongContextInputThreshold <= 0 {
			normalized.LongContextInputThreshold = openAIGPT54LongContextInputThreshold
		}
		if normalized.LongContextInputMultiplier <= 0 {
			normalized.LongContextInputMultiplier = openAIGPT54LongContextInputMultiplier
		}
		if normalized.LongContextOutputMultiplier <= 0 {
			normalized.LongContextOutputMultiplier = openAIGPT54LongContextOutputMultiplier
		}
		if pricing.LongContextInputThreshold <= 0 &&
			pricing.LongContextInputMultiplier <= 0 &&
			pricing.LongContextOutputMultiplier <= 0 {
			normalized.InputPricePerTokenPriority = 0
			normalized.OutputPricePerTokenPriority = 0
			normalized.CacheReadPricePerTokenPriority = 0
		}
	}

	return &normalized
}

func hasOpenAINoSeparateCacheCreationPrice(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, "gpt-") && !strings.Contains(model, "codex")
}

// isOpenAIGPT54Model checks if the model normalizes to the base "gpt-5.4" family
// (non-codex). Uses normalizeCodexModel as the canonical normalization.
func isOpenAIGPT54Model(model string) bool {
	trimmed := strings.TrimSpace(strings.ToLower(model))
	if !strings.Contains(trimmed, "gpt-5.4") {
		return false
	}
	return normalizeCodexModel(trimmed) == "gpt-5.4"
}

func isGrokBillingModel(model string) bool {
	model = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(model)), "models/")
	return xai.IsGrokModelID(xai.StripProviderPrefix(model))
}

func shouldApplySessionLongContextPricing(model string, tokens UsageTokens, pricing *ModelPricing) bool {
	if pricing == nil || pricing.LongContextInputThreshold <= 0 {
		return false
	}
	if pricing.LongContextInputMultiplier <= 1 && pricing.LongContextOutputMultiplier <= 1 {
		return false
	}
	totalInputTokens := tokens.InputTokens + tokens.CacheReadTokens
	if isGrokBillingModel(model) {
		totalInputTokens += tokens.CacheCreationTokens + tokens.CacheCreation5mTokens + tokens.CacheCreation1hTokens
	}
	if pricing.LongContextThresholdInclusive {
		return totalInputTokens >= pricing.LongContextInputThreshold
	}
	return totalInputTokens > pricing.LongContextInputThreshold
}

func shouldIgnorePriorityTierForLongContext(model string, tokens UsageTokens, pricing *ModelPricing, serviceTier string) bool {
	return isOpenAIGPT54Model(model) &&
		normalizeBillingServiceTier(serviceTier) == "priority" &&
		shouldApplySessionLongContextPricing(model, tokens, pricing)
}

// opus46LargeContextThreshold is the input token threshold for opus-4-6 1M pricing.
// When total input (including cache) exceeds this, use the higher 1M context pricing.
const opus46LargeContextThreshold = 200000

// CalculateCost 计算使用费用
func (s *BillingService) CalculateCost(model string, tokens UsageTokens, rateMultiplier float64) (*CostBreakdown, error) {
	return s.CalculateCostWithServiceTier(model, tokens, rateMultiplier, "")
}

// CalculateCostWithServiceTier 计算使用费用，并按 service_tier 应用差异定价。
func (s *BillingService) CalculateCostWithServiceTier(model string, tokens UsageTokens, rateMultiplier float64, serviceTier string) (*CostBreakdown, error) {
	pricing, err := s.GetModelPricing(model)
	if err != nil {
		return nil, err
	}

	breakdown := &CostBreakdown{}

	inputPrice := pricing.InputPricePerToken
	outputPrice := pricing.OutputPricePerToken
	cacheCreationPrice := pricing.CacheCreationPricePerToken
	cacheReadPrice := pricing.CacheReadPricePerToken
	tierMultiplier := 1.0
	longContextInputMultiplier := 1.0
	applyLongContext := shouldApplySessionLongContextPricing(model, tokens, pricing)
	isGrokModel := isGrokBillingModel(model)

	if shouldIgnorePriorityTierForLongContext(model, tokens, pricing, serviceTier) {
		serviceTier = ""
	}

	if usePriorityServiceTierPricing(serviceTier, pricing) {
		if pricing.InputPricePerTokenPriority > 0 {
			inputPrice = pricing.InputPricePerTokenPriority
		}
		if pricing.OutputPricePerTokenPriority > 0 {
			outputPrice = pricing.OutputPricePerTokenPriority
		}
		if pricing.CacheReadPricePerTokenPriority > 0 {
			cacheReadPrice = pricing.CacheReadPricePerTokenPriority
		}
	} else {
		tierMultiplier = serviceTierCostMultiplier(serviceTier)
	}

	if applyLongContext {
		if pricing.LongContextInputMultiplier > 0 {
			if isGrokModel {
				longContextInputMultiplier = pricing.LongContextInputMultiplier
			}
			inputPrice *= pricing.LongContextInputMultiplier
			if isGrokModel {
				cacheCreationPrice *= pricing.LongContextInputMultiplier
			}
			cacheReadPrice *= pricing.LongContextInputMultiplier
		}
		if pricing.LongContextOutputMultiplier > 0 {
			outputPrice *= pricing.LongContextOutputMultiplier
		}
	}
	if tokens.CacheReadTokens > 0 && cacheReadPrice <= 0 {
		cacheReadPrice = inputPrice
	}

	// 计算输入token费用（使用per-token价格）
	breakdown.InputCost = float64(tokens.InputTokens) * inputPrice

	// 计算输出token费用
	breakdown.OutputCost = float64(tokens.OutputTokens) * outputPrice

	// 计算缓存费用
	if pricing.SupportsCacheBreakdown && (pricing.CacheCreation5mPrice > 0 || pricing.CacheCreation1hPrice > 0) {
		// 支持详细缓存分类的模型（5分钟/1小时缓存）
		breakdown.CacheCreationCost = float64(tokens.CacheCreation5mTokens)/1_000_000*pricing.CacheCreation5mPrice*longContextInputMultiplier +
			float64(tokens.CacheCreation1hTokens)/1_000_000*pricing.CacheCreation1hPrice*longContextInputMultiplier
	} else {
		// 标准缓存创建价格（per-token）
		breakdown.CacheCreationCost = float64(tokens.CacheCreationTokens) * cacheCreationPrice
	}

	breakdown.CacheReadCost = float64(tokens.CacheReadTokens) * cacheReadPrice

	if tierMultiplier != 1.0 {
		breakdown.InputCost *= tierMultiplier
		breakdown.OutputCost *= tierMultiplier
		breakdown.CacheCreationCost *= tierMultiplier
		breakdown.CacheReadCost *= tierMultiplier
	}

	// 计算总费用
	breakdown.TotalCost = breakdown.InputCost + breakdown.OutputCost +
		breakdown.CacheCreationCost + breakdown.CacheReadCost

	// 应用倍率计算实际费用
	if rateMultiplier < 0 {
		rateMultiplier = 1.0
	}
	breakdown.ActualCost = breakdown.TotalCost * rateMultiplier

	return breakdown, nil
}

// CalculateCostWithConfig 使用配置中的默认倍率计算费用
func (s *BillingService) CalculateCostWithConfig(model string, tokens UsageTokens) (*CostBreakdown, error) {
	multiplier := s.cfg.Default.RateMultiplier
	if multiplier <= 0 {
		multiplier = 1.0
	}
	return s.CalculateCost(model, tokens, multiplier)
}

// ListSupportedModels 列出所有支持的模型（现在总是返回true，因为有模糊匹配）
func (s *BillingService) ListSupportedModels() []string {
	models := make([]string, 0)
	// 返回回退价格支持的模型系列
	for model := range s.fallbackPrices {
		models = append(models, model)
	}
	return models
}

// IsModelSupported 检查模型是否支持（现在总是返回true，因为有模糊匹配回退）
func (s *BillingService) IsModelSupported(model string) bool {
	// 所有Claude模型都有回退价格支持
	modelLower := strings.ToLower(strings.TrimSpace(model))
	nativeGrokModel := strings.TrimPrefix(modelLower, "models/")
	nativeGrokModel = strings.ToLower(strings.TrimSpace(xai.StripProviderPrefix(nativeGrokModel)))
	return strings.Contains(modelLower, "claude") ||
		strings.Contains(modelLower, "opus") ||
		strings.Contains(modelLower, "sonnet") ||
		strings.Contains(modelLower, "haiku") ||
		xai.IsGrokModelID(nativeGrokModel)
}

// GetEstimatedCost 估算费用（用于前端展示）
func (s *BillingService) GetEstimatedCost(model string, estimatedInputTokens, estimatedOutputTokens int) (float64, error) {
	tokens := UsageTokens{
		InputTokens:  estimatedInputTokens,
		OutputTokens: estimatedOutputTokens,
	}

	breakdown, err := s.CalculateCostWithConfig(model, tokens)
	if err != nil {
		return 0, err
	}

	return breakdown.ActualCost, nil
}

// GetPricingServiceStatus 获取价格服务状态
func (s *BillingService) GetPricingServiceStatus() map[string]any {
	if s.pricingService != nil {
		return s.pricingService.GetStatus()
	}
	return map[string]any{
		"model_count":  len(s.fallbackPrices),
		"last_updated": "using fallback",
		"local_hash":   "N/A",
	}
}

// ForceUpdatePricing 强制更新价格数据
func (s *BillingService) ForceUpdatePricing() error {
	if s.pricingService != nil {
		return s.pricingService.ForceUpdate()
	}
	return fmt.Errorf("pricing service not initialized")
}

// ImagePriceConfig 图片计费配置
type ImagePriceConfig struct {
	Price1K *float64 // 1K 尺寸价格（nil 表示使用默认值）
	Price2K *float64 // 2K 尺寸价格（nil 表示使用默认值）
	Price4K *float64 // 4K 尺寸价格（nil 表示使用默认值）
}

const (
	defaultGrokImagineImagePrice1K        = 0.02
	defaultGrokImagineImagePrice2K        = 0.02
	defaultGrokImagineImageQualityPrice1K = 0.05
	defaultGrokImagineImageQualityPrice2K = 0.07
	defaultGrokImagineImage20Price1K      = 0.06
	defaultGrokImagineImage20Price2K      = 0.08
)

// CalculateImageCost 计算图片生成费用
// model: 请求的模型名称（用于获取 LiteLLM 默认价格）
// imageSize: 图片尺寸 "1K", "2K", "4K"
// imageCount: 生成的图片数量
// groupConfig: 分组配置的价格（可能为 nil，表示使用默认值）
// rateMultiplier: 费率倍数
func (s *BillingService) CalculateImageCost(model string, imageSize string, imageCount int, groupConfig *ImagePriceConfig, rateMultiplier float64) *CostBreakdown {
	if imageCount <= 0 {
		return &CostBreakdown{}
	}

	// 获取单价
	unitPrice := s.getImageUnitPrice(model, imageSize, groupConfig)

	// 计算总费用
	totalCost := unitPrice * float64(imageCount)

	// 应用倍率
	if rateMultiplier < 0 {
		rateMultiplier = 1.0
	}
	actualCost := totalCost * rateMultiplier

	return &CostBreakdown{
		TotalCost:  totalCost,
		ActualCost: actualCost,
	}
}

// getImageUnitPrice 获取图片单价
func (s *BillingService) getImageUnitPrice(model string, imageSize string, groupConfig *ImagePriceConfig) float64 {
	// 优先使用分组配置的价格
	if groupConfig != nil {
		switch imageSize {
		case "1K":
			if groupConfig.Price1K != nil {
				return *groupConfig.Price1K
			}
		case "2K":
			if groupConfig.Price2K != nil {
				return *groupConfig.Price2K
			}
		case "4K":
			if groupConfig.Price4K != nil {
				return *groupConfig.Price4K
			}
		}
	}

	// 回退到 LiteLLM 默认价格
	return s.getDefaultImagePrice(model, imageSize)
}

// getDefaultImagePrice 获取 LiteLLM 默认图片价格
func (s *BillingService) getDefaultImagePrice(model string, imageSize string) float64 {
	if price, ok := getDefaultGrokImagineImagePrice(model, imageSize); ok {
		return price
	}
	basePrice := 0.0

	// 从 PricingService 获取 output_cost_per_image
	if s.pricingService != nil {
		pricing := s.pricingService.GetModelPricing(model)
		if pricing != nil && pricing.OutputCostPerImage > 0 {
			basePrice = pricing.OutputCostPerImage
		}
	}

	// OpenAI gpt-image 模型使用 token 计价，没有 output_cost_per_image 字段。
	// 回退到基于官方 image output token 价格的估算值（per image, 1K tier）。
	// gpt-image-1: ~$0.040, gpt-image-1.5: ~$0.050, gpt-image-2/2.5: ~$0.060
	if basePrice <= 0 && isOpenAIImageBillingModel(model) {
		basePrice = getOpenAIImageDefaultPrice(model)
	}

	// 如果没有找到价格，使用硬编码默认值（$0.134，来自 gemini-3-pro-image-preview）
	if basePrice <= 0 {
		basePrice = 0.134
	}

	// 4K 尺寸翻倍
	if imageSize == "4K" {
		return basePrice * 2
	}

	return basePrice
}

func getDefaultGrokImagineImagePrice(model string, imageSize string) (float64, bool) {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "grok-imagine-image-2.0":
		return getGrokImagineImageTierPrice(imageSize, defaultGrokImagineImage20Price1K, defaultGrokImagineImage20Price2K), true
	case "grok-imagine-image-quality":
		return getGrokImagineImageTierPrice(imageSize, defaultGrokImagineImageQualityPrice1K, defaultGrokImagineImageQualityPrice2K), true
	case "grok-imagine", "grok-imagine-image", "grok-imagine-edit":
		return getGrokImagineImageTierPrice(imageSize, defaultGrokImagineImagePrice1K, defaultGrokImagineImagePrice2K), true
	default:
		return 0, false
	}
}

func getGrokImagineImageTierPrice(imageSize string, price1K, price2K float64) float64 {
	if strings.EqualFold(strings.TrimSpace(imageSize), "1K") {
		return price1K
	}
	return price2K
}

// isOpenAIImageBillingModel checks if the model is an OpenAI image generation model.
func isOpenAIImageBillingModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "gpt-image-")
}

// getOpenAIImageDefaultPrice returns a per-image default price for OpenAI gpt-image models.
// These are estimates based on official image output token pricing at 1K resolution.
func getOpenAIImageDefaultPrice(model string) float64 {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case m == "gpt-image-2.5-sunburst" || m == "gpt-image-2.5-flare":
		return 0.060
	case strings.HasPrefix(m, "gpt-image-2"):
		return 0.060
	case strings.HasPrefix(m, "gpt-image-1.5"):
		return 0.050
	case strings.HasPrefix(m, "gpt-image-1-mini"):
		return 0.020
	case strings.HasPrefix(m, "gpt-image-1"):
		return 0.040
	default:
		return 0.060
	}
}
