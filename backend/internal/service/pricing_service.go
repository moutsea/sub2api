package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
)

var (
	openAIModelDatePattern = regexp.MustCompile(`-\d{8}$`)
	openAIModelBasePattern = regexp.MustCompile(`^(gpt-\d+(?:\.\d+)?)(?:-|$)`)
)

const (
	openAIPricingFallbackModel      = "gpt-5.5"
	openAICodexPricingFallbackModel = "gpt-5.1-codex"
)

var openAIStaticPricingOverrides = map[string]*LiteLLMModelPricing{
	"gpt-5.6": {
		InputCostPerToken:               5e-06,
		OutputCostPerToken:              30e-06,
		CacheCreationInputTokenCost:     6.25e-06,
		CacheReadInputTokenCost:         5e-07,
		LongContextInputTokenThreshold:  272000,
		LongContextInputCostMultiplier:  2.0,
		LongContextOutputCostMultiplier: 1.5,
		LiteLLMProvider:                 "openai",
		Mode:                            "chat",
		SupportsPromptCaching:           true,
	},
	"gpt-5.6-sol": {
		InputCostPerToken:               5e-06,
		OutputCostPerToken:              30e-06,
		CacheCreationInputTokenCost:     6.25e-06,
		CacheReadInputTokenCost:         5e-07,
		LongContextInputTokenThreshold:  272000,
		LongContextInputCostMultiplier:  2.0,
		LongContextOutputCostMultiplier: 1.5,
		LiteLLMProvider:                 "openai",
		Mode:                            "chat",
		SupportsPromptCaching:           true,
	},
	"gpt-5.6-terra": {
		InputCostPerToken:               2.5e-06,
		OutputCostPerToken:              15e-06,
		CacheCreationInputTokenCost:     3.125e-06,
		CacheReadInputTokenCost:         2.5e-07,
		LongContextInputTokenThreshold:  272000,
		LongContextInputCostMultiplier:  2.0,
		LongContextOutputCostMultiplier: 1.5,
		LiteLLMProvider:                 "openai",
		Mode:                            "chat",
		SupportsPromptCaching:           true,
	},
	"gpt-5.6-luna": {
		InputCostPerToken:               1e-06,
		OutputCostPerToken:              6e-06,
		CacheCreationInputTokenCost:     1.25e-06,
		CacheReadInputTokenCost:         1e-07,
		LongContextInputTokenThreshold:  272000,
		LongContextInputCostMultiplier:  2.0,
		LongContextOutputCostMultiplier: 1.5,
		LiteLLMProvider:                 "openai",
		Mode:                            "chat",
		SupportsPromptCaching:           true,
	},
	"gpt-5.3": {
		InputCostPerToken:               1.75e-06,
		InputCostPerTokenPriority:       3.5e-06,
		OutputCostPerToken:              14e-06,
		OutputCostPerTokenPriority:      28e-06,
		CacheReadInputTokenCost:         1.75e-07,
		CacheReadInputTokenCostPriority: 3.5e-07,
		SupportsServiceTier:             true,
		LiteLLMProvider:                 "openai",
		Mode:                            "chat",
		SupportsPromptCaching:           true,
	},
	"gpt-5.4": {
		InputCostPerToken:               2.5e-06,
		OutputCostPerToken:              15e-06,
		CacheReadInputTokenCost:         2.5e-07,
		LongContextInputTokenThreshold:  272000,
		LongContextInputCostMultiplier:  2.0,
		LongContextOutputCostMultiplier: 1.5,
		LiteLLMProvider:                 "openai",
		Mode:                            "chat",
		SupportsPromptCaching:           true,
	},
	"gpt-5.5": {
		InputCostPerToken:               5e-06,
		InputCostPerTokenPriority:       1e-05,
		OutputCostPerToken:              30e-06,
		OutputCostPerTokenPriority:      60e-06,
		CacheReadInputTokenCost:         5e-07,
		CacheReadInputTokenCostPriority: 1e-06,
		SupportsServiceTier:             true,
		LiteLLMProvider:                 "openai",
		Mode:                            "chat",
		SupportsPromptCaching:           true,
	},
}

var claudeOpus48EquivalentStaticPricing = &LiteLLMModelPricing{
	InputCostPerToken:           5e-06,
	OutputCostPerToken:          25e-06,
	CacheCreationInputTokenCost: 6.25e-06,
	CacheReadInputTokenCost:     0.5e-06,
	LiteLLMProvider:             "anthropic",
	Mode:                        "chat",
	SupportsPromptCaching:       true,
}

// LiteLLMModelPricing LiteLLM价格数据结构
// 只保留我们需要的字段，使用指针来处理可能缺失的值
type LiteLLMModelPricing struct {
	InputCostPerToken               float64 `json:"input_cost_per_token"`
	InputCostPerTokenPriority       float64 `json:"input_cost_per_token_priority"`
	OutputCostPerToken              float64 `json:"output_cost_per_token"`
	OutputCostPerTokenPriority      float64 `json:"output_cost_per_token_priority"`
	CacheCreationInputTokenCost     float64 `json:"cache_creation_input_token_cost"`
	CacheReadInputTokenCost         float64 `json:"cache_read_input_token_cost"`
	CacheReadInputTokenCostPriority float64 `json:"cache_read_input_token_cost_priority"`
	LongContextInputTokenThreshold  int     `json:"long_context_input_token_threshold,omitempty"`
	LongContextInputCostMultiplier  float64 `json:"long_context_input_cost_multiplier,omitempty"`
	LongContextOutputCostMultiplier float64 `json:"long_context_output_cost_multiplier,omitempty"`
	SupportsServiceTier             bool    `json:"supports_service_tier"`
	LiteLLMProvider                 string  `json:"litellm_provider"`
	Mode                            string  `json:"mode"`
	SupportsPromptCaching           bool    `json:"supports_prompt_caching"`
	OutputCostPerImage              float64 `json:"output_cost_per_image"` // 图片生成模型每张图片价格
}

// PricingRemoteClient 远程价格数据获取接口
type PricingRemoteClient interface {
	FetchPricingJSON(ctx context.Context, url string) ([]byte, error)
	FetchHashText(ctx context.Context, url string) (string, error)
}

// LiteLLMRawEntry 用于解析原始JSON数据
type LiteLLMRawEntry struct {
	InputCostPerToken               *float64 `json:"input_cost_per_token"`
	InputCostPerTokenPriority       *float64 `json:"input_cost_per_token_priority"`
	OutputCostPerToken              *float64 `json:"output_cost_per_token"`
	OutputCostPerTokenPriority      *float64 `json:"output_cost_per_token_priority"`
	CacheCreationInputTokenCost     *float64 `json:"cache_creation_input_token_cost"`
	CacheReadInputTokenCost         *float64 `json:"cache_read_input_token_cost"`
	CacheReadInputTokenCostPriority *float64 `json:"cache_read_input_token_cost_priority"`
	LongContextInputTokenThreshold  *int     `json:"long_context_input_token_threshold"`
	LongContextInputCostMultiplier  *float64 `json:"long_context_input_cost_multiplier"`
	LongContextOutputCostMultiplier *float64 `json:"long_context_output_cost_multiplier"`
	SupportsServiceTier             bool     `json:"supports_service_tier"`
	LiteLLMProvider                 string   `json:"litellm_provider"`
	Mode                            string   `json:"mode"`
	SupportsPromptCaching           bool     `json:"supports_prompt_caching"`
	OutputCostPerImage              *float64 `json:"output_cost_per_image"`
}

// PricingService 动态价格服务
type PricingService struct {
	cfg          *config.Config
	remoteClient PricingRemoteClient
	mu           sync.RWMutex
	pricingData  map[string]*LiteLLMModelPricing
	lastUpdated  time.Time
	localHash    string

	// 停止信号
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewPricingService 创建价格服务
func NewPricingService(cfg *config.Config, remoteClient PricingRemoteClient) *PricingService {
	s := &PricingService{
		cfg:          cfg,
		remoteClient: remoteClient,
		pricingData:  make(map[string]*LiteLLMModelPricing),
		stopCh:       make(chan struct{}),
	}
	return s
}

// Initialize 初始化价格服务
func (s *PricingService) Initialize() error {
	// 确保数据目录存在
	if err := os.MkdirAll(s.cfg.Pricing.DataDir, 0755); err != nil {
		log.Printf("[Pricing] Failed to create data directory: %v", err)
	}

	// 首次加载价格数据
	if err := s.checkAndUpdatePricing(); err != nil {
		log.Printf("[Pricing] Initial load failed, using fallback: %v", err)
		if err := s.useFallbackPricing(); err != nil {
			return fmt.Errorf("failed to load pricing data: %w", err)
		}
	}

	// 启动定时更新
	s.startUpdateScheduler()

	log.Printf("[Pricing] Service initialized with %d models", len(s.pricingData))
	return nil
}

// Stop 停止价格服务
func (s *PricingService) Stop() {
	close(s.stopCh)
	s.wg.Wait()
	log.Println("[Pricing] Service stopped")
}

// startUpdateScheduler 启动定时更新调度器
func (s *PricingService) startUpdateScheduler() {
	// 定期检查哈希更新
	hashInterval := time.Duration(s.cfg.Pricing.HashCheckIntervalMinutes) * time.Minute
	if hashInterval < time.Minute {
		hashInterval = 10 * time.Minute
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(hashInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := s.syncWithRemote(); err != nil {
					log.Printf("[Pricing] Sync failed: %v", err)
				}
			case <-s.stopCh:
				return
			}
		}
	}()

	log.Printf("[Pricing] Update scheduler started (check every %v)", hashInterval)
}

// checkAndUpdatePricing 检查并更新价格数据
func (s *PricingService) checkAndUpdatePricing() error {
	pricingFile := s.getPricingFilePath()

	// 检查本地文件是否存在
	if _, err := os.Stat(pricingFile); os.IsNotExist(err) {
		log.Println("[Pricing] Local pricing file not found, downloading...")
		return s.downloadPricingData()
	}

	// 检查文件是否过期
	info, err := os.Stat(pricingFile)
	if err != nil {
		return s.downloadPricingData()
	}

	fileAge := time.Since(info.ModTime())
	maxAge := time.Duration(s.cfg.Pricing.UpdateIntervalHours) * time.Hour

	if fileAge > maxAge {
		log.Printf("[Pricing] Local file is %v old, updating...", fileAge.Round(time.Hour))
		if err := s.downloadPricingData(); err != nil {
			log.Printf("[Pricing] Download failed, using existing file: %v", err)
		}
	}

	// 加载本地文件
	return s.loadPricingData(pricingFile)
}

// syncWithRemote 与远程同步（基于哈希校验）
func (s *PricingService) syncWithRemote() error {
	pricingFile := s.getPricingFilePath()

	// 计算本地文件哈希
	localHash, err := s.computeFileHash(pricingFile)
	if err != nil {
		log.Printf("[Pricing] Failed to compute local hash: %v", err)
		return s.downloadPricingData()
	}

	// 如果配置了哈希URL，从远程获取哈希进行比对
	if s.cfg.Pricing.HashURL != "" {
		remoteHash, err := s.fetchRemoteHash()
		if err != nil {
			log.Printf("[Pricing] Failed to fetch remote hash: %v", err)
			return nil // 哈希获取失败不影响正常使用
		}

		if remoteHash != localHash {
			log.Println("[Pricing] Remote hash differs, downloading new version...")
			return s.downloadPricingData()
		}
		log.Println("[Pricing] Hash check passed, no update needed")
		return nil
	}

	// 没有哈希URL时，基于时间检查
	info, err := os.Stat(pricingFile)
	if err != nil {
		return s.downloadPricingData()
	}

	fileAge := time.Since(info.ModTime())
	maxAge := time.Duration(s.cfg.Pricing.UpdateIntervalHours) * time.Hour

	if fileAge > maxAge {
		log.Printf("[Pricing] File is %v old, downloading...", fileAge.Round(time.Hour))
		return s.downloadPricingData()
	}

	return nil
}

// downloadPricingData 从远程下载价格数据
func (s *PricingService) downloadPricingData() error {
	remoteURL, err := s.validatePricingURL(s.cfg.Pricing.RemoteURL)
	if err != nil {
		return err
	}
	log.Printf("[Pricing] Downloading from %s", remoteURL)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var expectedHash string
	if strings.TrimSpace(s.cfg.Pricing.HashURL) != "" {
		expectedHash, err = s.fetchRemoteHash()
		if err != nil {
			return fmt.Errorf("fetch remote hash: %w", err)
		}
	}

	body, err := s.remoteClient.FetchPricingJSON(ctx, remoteURL)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}

	if expectedHash != "" {
		actualHash := sha256.Sum256(body)
		if !strings.EqualFold(expectedHash, hex.EncodeToString(actualHash[:])) {
			return fmt.Errorf("pricing hash mismatch")
		}
	}

	// 解析JSON数据（使用灵活的解析方式）
	data, err := s.parsePricingData(body)
	if err != nil {
		return fmt.Errorf("parse pricing data: %w", err)
	}

	// 保存到本地文件
	pricingFile := s.getPricingFilePath()
	if err := os.WriteFile(pricingFile, body, 0644); err != nil {
		log.Printf("[Pricing] Failed to save file: %v", err)
	}

	// 保存哈希
	hash := sha256.Sum256(body)
	hashStr := hex.EncodeToString(hash[:])
	hashFile := s.getHashFilePath()
	if err := os.WriteFile(hashFile, []byte(hashStr+"\n"), 0644); err != nil {
		log.Printf("[Pricing] Failed to save hash: %v", err)
	}

	// 更新内存数据
	s.mu.Lock()
	s.pricingData = data
	s.lastUpdated = time.Now()
	s.localHash = hashStr
	s.mu.Unlock()

	log.Printf("[Pricing] Downloaded %d models successfully", len(data))
	return nil
}

// parsePricingData 解析价格数据（处理各种格式）
func (s *PricingService) parsePricingData(body []byte) (map[string]*LiteLLMModelPricing, error) {
	// 首先解析为 map[string]json.RawMessage
	var rawData map[string]json.RawMessage
	if err := json.Unmarshal(body, &rawData); err != nil {
		return nil, fmt.Errorf("parse raw JSON: %w", err)
	}

	result := make(map[string]*LiteLLMModelPricing)
	skipped := 0

	for modelName, rawEntry := range rawData {
		// 跳过 sample_spec 等文档条目
		if modelName == "sample_spec" {
			continue
		}

		// 尝试解析每个条目
		var entry LiteLLMRawEntry
		if err := json.Unmarshal(rawEntry, &entry); err != nil {
			skipped++
			continue
		}

		// 只保留有有效价格的条目
		if entry.InputCostPerToken == nil && entry.OutputCostPerToken == nil {
			continue
		}

		pricing := &LiteLLMModelPricing{
			SupportsServiceTier:   entry.SupportsServiceTier,
			LiteLLMProvider:       entry.LiteLLMProvider,
			Mode:                  entry.Mode,
			SupportsPromptCaching: entry.SupportsPromptCaching,
		}

		if entry.InputCostPerToken != nil {
			pricing.InputCostPerToken = *entry.InputCostPerToken
		}
		if entry.InputCostPerTokenPriority != nil {
			pricing.InputCostPerTokenPriority = *entry.InputCostPerTokenPriority
		}
		if entry.OutputCostPerToken != nil {
			pricing.OutputCostPerToken = *entry.OutputCostPerToken
		}
		if entry.OutputCostPerTokenPriority != nil {
			pricing.OutputCostPerTokenPriority = *entry.OutputCostPerTokenPriority
		}
		if entry.CacheCreationInputTokenCost != nil {
			pricing.CacheCreationInputTokenCost = *entry.CacheCreationInputTokenCost
		}
		if entry.CacheReadInputTokenCost != nil {
			pricing.CacheReadInputTokenCost = *entry.CacheReadInputTokenCost
		}
		if entry.CacheReadInputTokenCostPriority != nil {
			pricing.CacheReadInputTokenCostPriority = *entry.CacheReadInputTokenCostPriority
		}
		if entry.LongContextInputTokenThreshold != nil {
			pricing.LongContextInputTokenThreshold = *entry.LongContextInputTokenThreshold
		}
		if entry.LongContextInputCostMultiplier != nil {
			pricing.LongContextInputCostMultiplier = *entry.LongContextInputCostMultiplier
		}
		if entry.LongContextOutputCostMultiplier != nil {
			pricing.LongContextOutputCostMultiplier = *entry.LongContextOutputCostMultiplier
		}
		if entry.OutputCostPerImage != nil {
			pricing.OutputCostPerImage = *entry.OutputCostPerImage
		}

		result[modelName] = pricing
	}

	if skipped > 0 {
		log.Printf("[Pricing] Skipped %d invalid entries", skipped)
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("no valid pricing entries found")
	}

	return result, nil
}

// loadPricingData 从本地文件加载价格数据
func (s *PricingService) loadPricingData(filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read file failed: %w", err)
	}

	// 使用灵活的解析方式
	pricingData, err := s.parsePricingData(data)
	if err != nil {
		return fmt.Errorf("parse pricing data: %w", err)
	}

	// 计算哈希
	hash := sha256.Sum256(data)
	hashStr := hex.EncodeToString(hash[:])

	s.mu.Lock()
	s.pricingData = pricingData
	s.localHash = hashStr

	info, _ := os.Stat(filePath)
	if info != nil {
		s.lastUpdated = info.ModTime()
	} else {
		s.lastUpdated = time.Now()
	}
	s.mu.Unlock()

	log.Printf("[Pricing] Loaded %d models from %s", len(pricingData), filePath)
	return nil
}

// useFallbackPricing 使用回退价格文件
func (s *PricingService) useFallbackPricing() error {
	fallbackFile := s.cfg.Pricing.FallbackFile

	if _, err := os.Stat(fallbackFile); os.IsNotExist(err) {
		return fmt.Errorf("fallback file not found: %s", fallbackFile)
	}

	log.Printf("[Pricing] Using fallback file: %s", fallbackFile)

	// 复制到数据目录
	data, err := os.ReadFile(fallbackFile)
	if err != nil {
		return fmt.Errorf("read fallback failed: %w", err)
	}

	pricingFile := s.getPricingFilePath()
	if err := os.WriteFile(pricingFile, data, 0644); err != nil {
		log.Printf("[Pricing] Failed to copy fallback: %v", err)
	}

	return s.loadPricingData(fallbackFile)
}

// fetchRemoteHash 从远程获取哈希值
func (s *PricingService) fetchRemoteHash() (string, error) {
	hashURL, err := s.validatePricingURL(s.cfg.Pricing.HashURL)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	hash, err := s.remoteClient.FetchHashText(ctx, hashURL)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(hash), nil
}

func (s *PricingService) validatePricingURL(raw string) (string, error) {
	if s.cfg != nil && !s.cfg.Security.URLAllowlist.Enabled {
		normalized, err := urlvalidator.ValidateURLFormat(raw, s.cfg.Security.URLAllowlist.AllowInsecureHTTP)
		if err != nil {
			return "", fmt.Errorf("invalid pricing url: %w", err)
		}
		return normalized, nil
	}
	normalized, err := urlvalidator.ValidateHTTPSURL(raw, urlvalidator.ValidationOptions{
		AllowedHosts:     s.cfg.Security.URLAllowlist.PricingHosts,
		RequireAllowlist: true,
		AllowPrivate:     s.cfg.Security.URLAllowlist.AllowPrivateHosts,
	})
	if err != nil {
		return "", fmt.Errorf("invalid pricing url: %w", err)
	}
	return normalized, nil
}

// computeFileHash 计算文件哈希
func (s *PricingService) computeFileHash(filePath string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

// GetModelPricing 获取模型价格（带模糊匹配）
func (s *PricingService) GetModelPricing(modelName string) *LiteLLMModelPricing {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if modelName == "" {
		return nil
	}

	// 标准化模型名称（同时兼容 "models/xxx"、VertexAI 资源名等前缀）
	modelLower := strings.ToLower(strings.TrimSpace(modelName))
	lookupCandidates := s.buildModelLookupCandidates(modelLower)

	// 1. 精确匹配
	for _, candidate := range lookupCandidates {
		if candidate == "" {
			continue
		}
		if pricing, ok := s.pricingData[candidate]; ok {
			return pricing
		}
	}

	// 2. 处理常见的模型名称变体
	// claude-opus-4-5-20251101 -> claude-opus-4.5-20251101
	for _, candidate := range lookupCandidates {
		normalized := strings.ReplaceAll(candidate, "-4-5-", "-4.5-")
		if pricing, ok := s.pricingData[normalized]; ok {
			return pricing
		}
	}

	// 3. 尝试模糊匹配（去掉版本号后缀）
	// claude-opus-4-5-20251101 -> claude-opus-4.5
	baseName := s.extractBaseName(lookupCandidates[0])
	for key, pricing := range s.pricingData {
		keyBase := s.extractBaseName(strings.ToLower(key))
		if keyBase == baseName {
			return pricing
		}
	}

	// 4. 基于模型系列匹配（Claude）
	if pricing := s.matchByModelFamily(lookupCandidates[0]); pricing != nil {
		return pricing
	}
	if isClaudeOpus5Model(lookupCandidates[0]) || isClaudeOpus48Model(lookupCandidates[0]) {
		return cloneLiteLLMModelPricing(claudeOpus48EquivalentStaticPricing)
	}

	// 5. OpenAI 模型回退策略
	if strings.HasPrefix(lookupCandidates[0], "gpt-") || strings.HasPrefix(lookupCandidates[0], "codex-") {
		return s.matchOpenAIModel(lookupCandidates[0])
	}

	return nil
}

func (s *PricingService) buildModelLookupCandidates(modelLower string) []string {
	// Prefer canonical model name first (this also improves billing compatibility with "models/xxx").
	candidates := []string{
		normalizeModelNameForPricing(modelLower),
		modelLower,
	}
	candidates = append(candidates,
		strings.TrimPrefix(modelLower, "models/"),
		lastSegment(modelLower),
		lastSegment(strings.TrimPrefix(modelLower, "models/")),
	)

	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	if len(out) == 0 {
		return []string{modelLower}
	}
	return out
}

func normalizeModelNameForPricing(model string) string {
	// Common Gemini/VertexAI forms:
	// - models/gemini-2.0-flash-exp
	// - publishers/google/models/gemini-2.5-pro
	// - projects/.../locations/.../publishers/google/models/gemini-2.5-pro
	model = strings.TrimSpace(model)
	model = strings.TrimLeft(model, "/")
	model = strings.TrimPrefix(model, "models/")
	model = strings.TrimPrefix(model, "publishers/google/models/")

	if idx := strings.LastIndex(model, "/publishers/google/models/"); idx != -1 {
		model = model[idx+len("/publishers/google/models/"):]
	}
	if idx := strings.LastIndex(model, "/models/"); idx != -1 {
		model = model[idx+len("/models/"):]
	}

	model = strings.TrimLeft(model, "/")
	return model
}

func lastSegment(model string) string {
	if idx := strings.LastIndex(model, "/"); idx != -1 {
		return model[idx+1:]
	}
	return model
}

// extractBaseName 提取基础模型名称（去掉日期版本号）
func (s *PricingService) extractBaseName(model string) string {
	// 移除日期后缀 (如 -20251101, -20241022)
	parts := strings.Split(model, "-")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		// 跳过看起来像日期的部分（8位数字）
		if len(part) == 8 && isNumeric(part) {
			continue
		}
		// 跳过版本号（如 v1:0）
		if strings.Contains(part, ":") {
			continue
		}
		result = append(result, part)
	}
	return strings.Join(result, "-")
}

// matchByModelFamily 基于模型系列匹配
func (s *PricingService) matchByModelFamily(model string) *LiteLLMModelPricing {
	// Claude模型系列匹配规则
	type familyPattern struct {
		name     string
		patterns []string
	}
	familyPatterns := []familyPattern{
		{name: "opus-5", patterns: []string{"claude-opus-5"}},
		{name: "opus-4.8", patterns: []string{"claude-opus-4.8", "claude-opus-4-8"}},
		{name: "opus-4.6", patterns: []string{"claude-opus-4.6", "claude-opus-4-6"}},
		{name: "opus-4.5", patterns: []string{"claude-opus-4.5", "claude-opus-4-5"}},
		{name: "opus-4", patterns: []string{"claude-opus-4", "claude-3-opus"}},
		{name: "sonnet-5", patterns: []string{"claude-sonnet-5"}},
		{name: "sonnet-4.6", patterns: []string{"claude-sonnet-4.6", "claude-sonnet-4-6"}},
		{name: "sonnet-4.5", patterns: []string{"claude-sonnet-4.5", "claude-sonnet-4-5"}},
		{name: "sonnet-4", patterns: []string{"claude-sonnet-4", "claude-3-5-sonnet"}},
		{name: "sonnet-3.5", patterns: []string{"claude-3-5-sonnet", "claude-3.5-sonnet"}},
		{name: "sonnet-3", patterns: []string{"claude-3-sonnet"}},
		{name: "haiku-3.5", patterns: []string{"claude-3-5-haiku", "claude-3.5-haiku"}},
		{name: "haiku-3", patterns: []string{"claude-3-haiku"}},
	}

	// 确定模型属于哪个系列
	var matchedFamily string
	var matchedPatterns []string
	for _, family := range familyPatterns {
		for _, pattern := range family.patterns {
			if containsModelFamilyToken(model, pattern) || containsModelFamilyToken(model, strings.ReplaceAll(pattern, "-", "")) {
				matchedFamily = family.name
				matchedPatterns = family.patterns
				break
			}
		}
		if matchedFamily != "" {
			break
		}
	}

	if matchedFamily == "" {
		// 简单的系列匹配
		if strings.Contains(model, "opus") {
			if isClaudeOpus5Model(model) {
				matchedFamily = "opus-5"
			} else if isClaudeOpus48Model(model) {
				matchedFamily = "opus-4.8"
			} else if strings.Contains(model, "4.5") || strings.Contains(model, "4-5") {
				matchedFamily = "opus-4.5"
			} else {
				matchedFamily = "opus-4"
			}
		} else if strings.Contains(model, "sonnet") {
			if strings.Contains(model, "4.5") || strings.Contains(model, "4-5") {
				matchedFamily = "sonnet-4.5"
			} else if strings.Contains(model, "3-5") || strings.Contains(model, "3.5") {
				matchedFamily = "sonnet-3.5"
			} else if strings.Contains(model, "5") {
				matchedFamily = "sonnet-5"
			} else {
				matchedFamily = "sonnet-4"
			}
		} else if strings.Contains(model, "haiku") {
			if strings.Contains(model, "3-5") || strings.Contains(model, "3.5") {
				matchedFamily = "haiku-3.5"
			} else {
				matchedFamily = "haiku-3"
			}
		}
	}

	if matchedFamily == "" {
		return nil
	}

	// 在价格数据中查找该系列的模型
	if len(matchedPatterns) == 0 {
		for _, family := range familyPatterns {
			if family.name == matchedFamily {
				matchedPatterns = family.patterns
				break
			}
		}
	}
	for _, pattern := range matchedPatterns {
		for key, pricing := range s.pricingData {
			keyLower := strings.ToLower(key)
			if containsModelFamilyToken(keyLower, pattern) {
				log.Printf("[Pricing] Fuzzy matched %s -> %s", model, key)
				return pricing
			}
		}
	}

	return nil
}

// matchOpenAIModel OpenAI 模型回退匹配策略
// 回退顺序：
// 1. gpt-5.2-codex -> gpt-5.2（去掉后缀如 -codex, -mini, -max 等）
// 2. gpt-5.2-20251222 -> gpt-5.2（去掉日期版本号）
// 3. 未公开定价的新版本回退到最近的公开 GPT-5 价格
func (s *PricingService) matchOpenAIModel(model string) *LiteLLMModelPricing {
	// 尝试的回退变体
	variants := s.generateOpenAIModelVariants(model, openAIModelDatePattern)
	allowStaticFallback := !strings.Contains(model, "codex")

	// 1. 优先查 JSON 动态数据源（pricingData），保证远程更新的价格能即时生效
	if pricing, ok := s.pricingData[model]; ok {
		log.Printf("[Pricing] OpenAI dynamic pricing matched %s", model)
		return pricing
	}
	for _, variant := range variants {
		if pricing, ok := s.pricingData[variant]; ok {
			log.Printf("[Pricing] OpenAI dynamic pricing fallback matched %s -> %s", model, variant)
			return pricing
		}
	}

	// 2. 动态源未命中时，回退到静态硬编码（仅非 codex 模型）
	if allowStaticFallback {
		if pricing, ok := openAIStaticPricingOverrides[model]; ok {
			log.Printf("[Pricing] OpenAI static pricing matched %s", model)
			return cloneLiteLLMModelPricing(pricing)
		}
		for _, variant := range variants {
			if pricing, ok := openAIStaticPricingOverrides[variant]; ok {
				log.Printf("[Pricing] OpenAI static pricing fallback matched %s -> %s", model, variant)
				return cloneLiteLLMModelPricing(pricing)
			}
		}
	}

	// 3. 最终兜底：回退到最近的公开 GPT-5 价格
	fallbackModel := openAIPricingFallbackModel
	if strings.Contains(model, "codex") {
		fallbackModel = openAICodexPricingFallbackModel
	}
	if pricing, ok := s.pricingData[fallbackModel]; ok {
		log.Printf("[Pricing] OpenAI fallback to priced model %s -> %s", model, fallbackModel)
		return pricing
	}
	if allowStaticFallback {
		if pricing, ok := openAIStaticPricingOverrides[fallbackModel]; ok {
			log.Printf("[Pricing] OpenAI static pricing fallback %s -> %s", model, fallbackModel)
			return cloneLiteLLMModelPricing(pricing)
		}
	}

	return nil
}

func cloneLiteLLMModelPricing(pricing *LiteLLMModelPricing) *LiteLLMModelPricing {
	if pricing == nil {
		return nil
	}
	cloned := *pricing
	return &cloned
}

// generateOpenAIModelVariants 生成 OpenAI 模型的回退变体列表
func (s *PricingService) generateOpenAIModelVariants(model string, datePattern *regexp.Regexp) []string {
	seen := make(map[string]bool)
	var variants []string

	addVariant := func(v string) {
		if v != model && !seen[v] {
			seen[v] = true
			variants = append(variants, v)
		}
	}

	// 1. 去掉日期版本号: gpt-5.2-20251222 -> gpt-5.2
	withoutDate := datePattern.ReplaceAllString(model, "")
	if withoutDate != model {
		addVariant(withoutDate)
	}

	// 2. 提取基础版本号: gpt-5.2-codex -> gpt-5.2
	// 只匹配纯数字版本号格式 gpt-X 或 gpt-X.Y，不匹配 gpt-4o 这种带字母后缀的
	if matches := openAIModelBasePattern.FindStringSubmatch(model); len(matches) > 1 {
		addVariant(matches[1])
	}

	// 3. 同时去掉日期后再提取基础版本号
	if withoutDate != model {
		if matches := openAIModelBasePattern.FindStringSubmatch(withoutDate); len(matches) > 1 {
			addVariant(matches[1])
		}
	}

	return variants
}

// GetStatus 获取服务状态
func (s *PricingService) GetStatus() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return map[string]any{
		"model_count":  len(s.pricingData),
		"last_updated": s.lastUpdated,
		"local_hash":   s.localHash[:min(8, len(s.localHash))],
	}
}

// ForceUpdate 强制更新
func (s *PricingService) ForceUpdate() error {
	return s.downloadPricingData()
}

// getPricingFilePath 获取价格文件路径
func (s *PricingService) getPricingFilePath() string {
	return filepath.Join(s.cfg.Pricing.DataDir, "model_pricing.json")
}

// getHashFilePath 获取哈希文件路径
func (s *PricingService) getHashFilePath() string {
	return filepath.Join(s.cfg.Pricing.DataDir, "model_pricing.sha256")
}

// isNumeric 检查字符串是否为纯数字
func isNumeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
