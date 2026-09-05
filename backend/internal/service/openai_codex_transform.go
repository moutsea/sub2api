package service

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	opencodeCodexHeaderURL = "https://raw.githubusercontent.com/anomalyco/opencode/dev/packages/opencode/src/session/prompt/codex_header.txt"
	codexCacheTTL          = 15 * time.Minute

	codexImageGenerationBridgeMarker = "<sub2api-codex-image-generation>"
	codexImageGenerationBridgeText   = codexImageGenerationBridgeMarker + "\nWhen the user asks for raster image generation or editing, use the OpenAI Responses native `image_generation` tool attached to this request. The local Codex client may not expose an `image_gen` namespace, but that does not mean image generation is unavailable. Do not ask the user to switch to CLI fallback solely because `image_gen` is absent.\n</sub2api-codex-image-generation>"
)

//go:embed prompts/codex_cli_instructions.md
var codexCLIInstructions string

var codexModelMap = map[string]string{
	"codex-auto-review":          "codex-auto-review",
	"gpt-6-astra":                "gpt-6-astra",
	"gpt-6-astra-none":           "gpt-6-astra",
	"gpt-6-astra-low":            "gpt-6-astra",
	"gpt-6-astra-medium":         "gpt-6-astra",
	"gpt-6-astra-high":           "gpt-6-astra",
	"gpt-6-astra-xhigh":          "gpt-6-astra",
	"gpt-5.6":                    "gpt-5.6",
	"gpt-5.6-none":               "gpt-5.6",
	"gpt-5.6-low":                "gpt-5.6",
	"gpt-5.6-medium":             "gpt-5.6",
	"gpt-5.6-high":               "gpt-5.6",
	"gpt-5.6-xhigh":              "gpt-5.6",
	"gpt-5.6-sol":                "gpt-5.6-sol",
	"gpt-5.6-sol-none":           "gpt-5.6-sol",
	"gpt-5.6-sol-low":            "gpt-5.6-sol",
	"gpt-5.6-sol-medium":         "gpt-5.6-sol",
	"gpt-5.6-sol-high":           "gpt-5.6-sol",
	"gpt-5.6-sol-xhigh":          "gpt-5.6-sol",
	"gpt-5.6-terra":              "gpt-5.6-terra",
	"gpt-5.6-terra-none":         "gpt-5.6-terra",
	"gpt-5.6-terra-low":          "gpt-5.6-terra",
	"gpt-5.6-terra-medium":       "gpt-5.6-terra",
	"gpt-5.6-terra-high":         "gpt-5.6-terra",
	"gpt-5.6-terra-xhigh":        "gpt-5.6-terra",
	"gpt-5.6-luna":               "gpt-5.6-luna",
	"gpt-5.6-luna-none":          "gpt-5.6-luna",
	"gpt-5.6-luna-low":           "gpt-5.6-luna",
	"gpt-5.6-luna-medium":        "gpt-5.6-luna",
	"gpt-5.6-luna-high":          "gpt-5.6-luna",
	"gpt-5.6-luna-xhigh":         "gpt-5.6-luna",
	"gpt-5.5":                    "gpt-5.5",
	"gpt-5.5-none":               "gpt-5.5",
	"gpt-5.5-low":                "gpt-5.5",
	"gpt-5.5-medium":             "gpt-5.5",
	"gpt-5.5-high":               "gpt-5.5",
	"gpt-5.5-xhigh":              "gpt-5.5",
	"gpt-5.4-mini":               "gpt-5.4-mini",
	"gpt-5.4-mini-none":          "gpt-5.4-mini",
	"gpt-5.4-mini-low":           "gpt-5.4-mini",
	"gpt-5.4-mini-medium":        "gpt-5.4-mini",
	"gpt-5.4-mini-high":          "gpt-5.4-mini",
	"gpt-5.4-mini-xhigh":         "gpt-5.4-mini",
	"gpt-5.4":                    "gpt-5.4",
	"gpt-5.4-none":               "gpt-5.4",
	"gpt-5.4-low":                "gpt-5.4",
	"gpt-5.4-medium":             "gpt-5.4",
	"gpt-5.4-high":               "gpt-5.4",
	"gpt-5.4-xhigh":              "gpt-5.4",
	"gpt-5.4-codex":              "gpt-5.4",
	"gpt-5.4-codex-low":          "gpt-5.4",
	"gpt-5.4-codex-medium":       "gpt-5.4",
	"gpt-5.4-codex-high":         "gpt-5.4",
	"gpt-5.4-codex-xhigh":        "gpt-5.4",
	"gpt-5.3-codex-spark":        "gpt-5.3-codex-spark",
	"gpt-5.3-codex-spark-none":   "gpt-5.3-codex-spark",
	"gpt-5.3-codex-spark-low":    "gpt-5.3-codex-spark",
	"gpt-5.3-codex-spark-medium": "gpt-5.3-codex-spark",
	"gpt-5.3-codex-spark-high":   "gpt-5.3-codex-spark",
	"gpt-5.3-codex-spark-xhigh":  "gpt-5.3-codex-spark",

	// 废弃和旧 Codex/GPT-5 系列不再作为目标模型，统一迁移到当前推荐的轻量 Codex 模型。
	"gpt-5.3":                   "gpt-5.4-mini",
	"gpt-5.3-none":              "gpt-5.4-mini",
	"gpt-5.3-low":               "gpt-5.4-mini",
	"gpt-5.3-medium":            "gpt-5.4-mini",
	"gpt-5.3-high":              "gpt-5.4-mini",
	"gpt-5.3-xhigh":             "gpt-5.4-mini",
	"gpt-5.3-codex":             "gpt-5.4-mini",
	"gpt-5.3-codex-low":         "gpt-5.4-mini",
	"gpt-5.3-codex-medium":      "gpt-5.4-mini",
	"gpt-5.3-codex-high":        "gpt-5.4-mini",
	"gpt-5.3-codex-xhigh":       "gpt-5.4-mini",
	"gpt-5.2":                   "gpt-5.4-mini",
	"gpt-5.2-none":              "gpt-5.4-mini",
	"gpt-5.2-low":               "gpt-5.4-mini",
	"gpt-5.2-medium":            "gpt-5.4-mini",
	"gpt-5.2-high":              "gpt-5.4-mini",
	"gpt-5.2-xhigh":             "gpt-5.4-mini",
	"gpt-5.2-codex":             "gpt-5.4-mini",
	"gpt-5.2-codex-low":         "gpt-5.4-mini",
	"gpt-5.2-codex-medium":      "gpt-5.4-mini",
	"gpt-5.2-codex-high":        "gpt-5.4-mini",
	"gpt-5.2-codex-xhigh":       "gpt-5.4-mini",
	"gpt-5.1-codex":             "gpt-5.4-mini",
	"gpt-5.1-codex-low":         "gpt-5.4-mini",
	"gpt-5.1-codex-medium":      "gpt-5.4-mini",
	"gpt-5.1-codex-high":        "gpt-5.4-mini",
	"gpt-5.1-codex-max":         "gpt-5.4-mini",
	"gpt-5.1-codex-max-low":     "gpt-5.4-mini",
	"gpt-5.1-codex-max-medium":  "gpt-5.4-mini",
	"gpt-5.1-codex-max-high":    "gpt-5.4-mini",
	"gpt-5.1-codex-max-xhigh":   "gpt-5.4-mini",
	"gpt-5.1-codex-mini":        "gpt-5.4-mini",
	"gpt-5.1-codex-mini-medium": "gpt-5.4-mini",
	"gpt-5.1-codex-mini-high":   "gpt-5.4-mini",
	"gpt-5.1":                   "gpt-5.4-mini",
	"gpt-5.1-none":              "gpt-5.4-mini",
	"gpt-5.1-low":               "gpt-5.4-mini",
	"gpt-5.1-medium":            "gpt-5.4-mini",
	"gpt-5.1-high":              "gpt-5.4-mini",
	"gpt-5.1-chat-latest":       "gpt-5.4-mini",
	"gpt-5-codex":               "gpt-5.4-mini",
	"codex-mini-latest":         "gpt-5.4-mini",
	"gpt-5-codex-mini":          "gpt-5.4-mini",
	"gpt-5-codex-mini-medium":   "gpt-5.4-mini",
	"gpt-5-codex-mini-high":     "gpt-5.4-mini",
	"gpt-5":                     "gpt-5.4-mini",
	"gpt-5-mini":                "gpt-5.4-mini",
	"gpt-5-nano":                "gpt-5.4-mini",
}

type codexTransformResult struct {
	Modified             bool
	NormalizedModel      string
	PromptCacheKey       string
	InjectedInstructions string
}

type opencodeCacheMetadata struct {
	ETag        string `json:"etag"`
	LastFetch   string `json:"lastFetch,omitempty"`
	LastChecked int64  `json:"lastChecked"`
}

func applyCodexOAuthTransform(reqBody map[string]any) codexTransformResult {
	result := codexTransformResult{}
	// 工具续链需求会影响存储策略与 input 过滤逻辑。
	needsToolContinuation := NeedsToolContinuation(reqBody)

	model := ""
	if v, ok := reqBody["model"].(string); ok {
		model = v
	}
	normalizedModel := normalizeCodexModel(model)
	if normalizedModel != "" {
		// 从模型名后缀提取 reasoning effort（如 -xhigh, -high, -medium, -low, -none）
		// 并设置到 reasoning.effort 参数中（仅在请求体未显式指定时）。
		if effort := extractCodexModelEffort(model, normalizedModel); effort != "" {
			if _, hasReasoning := reqBody["reasoning"]; !hasReasoning {
				reqBody["reasoning"] = map[string]any{"effort": effort}
				result.Modified = true
			}
		}

		if model != normalizedModel {
			reqBody["model"] = normalizedModel
			result.Modified = true
		}
		result.NormalizedModel = normalizedModel
	}

	// OAuth 走 ChatGPT internal API 时，store 必须为 false；显式 true 也会强制覆盖。
	// 避免上游返回 "Store must be set to false"。
	if v, ok := reqBody["store"].(bool); !ok || v {
		reqBody["store"] = false
		result.Modified = true
	}
	if v, ok := reqBody["stream"].(bool); !ok || !v {
		reqBody["stream"] = true
		result.Modified = true
	}
	if ensureReasoningEncryptedContentInclude(reqBody) {
		result.Modified = true
	}

	if _, ok := reqBody["max_output_tokens"]; ok {
		delete(reqBody, "max_output_tokens")
		result.Modified = true
	}
	if _, ok := reqBody["max_completion_tokens"]; ok {
		delete(reqBody, "max_completion_tokens")
		result.Modified = true
	}

	if normalizeCodexTools(reqBody) {
		result.Modified = true
	}

	if v, ok := reqBody["prompt_cache_key"].(string); ok {
		result.PromptCacheKey = strings.TrimSpace(v)
	}

	instructions := strings.TrimSpace(getOpenCodeCodexHeader())
	existingInstructions, _ := reqBody["instructions"].(string)
	existingInstructions = strings.TrimSpace(existingInstructions)

	if instructions != "" {
		if existingInstructions != instructions {
			result.InjectedInstructions = instructions
			reqBody["instructions"] = instructions
			result.Modified = true
		}
	} else if existingInstructions == "" {
		// 未获取到 opencode 指令时，回退使用 Codex CLI 指令。
		codexInstructions := strings.TrimSpace(getCodexCLIInstructions())
		if codexInstructions != "" {
			result.InjectedInstructions = codexInstructions
			reqBody["instructions"] = codexInstructions
			result.Modified = true
		}
	}

	// 续链场景保留 item_reference 与 id，避免 call_id 上下文丢失。
	if input, ok := reqBody["input"].([]any); ok {
		input = filterCodexInput(input, needsToolContinuation)
		reqBody["input"] = input
		result.Modified = true
	} else if inputStr, ok := reqBody["input"].(string); ok {
		// Codex 模型要求 input 必须是 list，将字符串包装为消息数组。
		reqBody["input"] = []any{
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "input_text",
						"text": inputStr,
					},
				},
			},
		}
		result.Modified = true
	}

	return result
}

func ensureReasoningEncryptedContentInclude(reqBody map[string]any) bool {
	if reqBody == nil {
		return false
	}
	store, ok := reqBody["store"].(bool)
	if !ok || store {
		return false
	}

	const includeValue = "reasoning.encrypted_content"
	raw, exists := reqBody["include"]
	if !exists || raw == nil {
		reqBody["include"] = []any{includeValue}
		return true
	}

	switch include := raw.(type) {
	case []any:
		for _, item := range include {
			if value, ok := item.(string); ok && value == includeValue {
				return false
			}
		}
		reqBody["include"] = append(include, includeValue)
		return true
	case []string:
		for _, value := range include {
			if value == includeValue {
				return false
			}
		}
		next := make([]string, 0, len(include)+1)
		next = append(next, include...)
		next = append(next, includeValue)
		reqBody["include"] = next
		return true
	default:
		return false
	}
}

func normalizeCodexModel(model string) string {
	if model == "" {
		return "gpt-5.4"
	}

	modelID := model
	if strings.Contains(modelID, "/") {
		parts := strings.Split(modelID, "/")
		modelID = parts[len(parts)-1]
	}

	if mapped := getNormalizedCodexModel(modelID); mapped != "" {
		return mapped
	}

	normalized := canonicalizeCodexModelID(modelID)

	if hasOpenAIModelPrefix(normalized, "gpt-6-astra") {
		return "gpt-6-astra"
	}
	if hasOpenAIModelPrefix(normalized, "gpt-5.6-sol") {
		return "gpt-5.6-sol"
	}
	if hasOpenAIModelPrefix(normalized, "gpt-5.6-terra") {
		return "gpt-5.6-terra"
	}
	if hasOpenAIModelPrefix(normalized, "gpt-5.6-luna") {
		return "gpt-5.6-luna"
	}
	if hasOpenAIModelPrefix(normalized, "gpt-5.6") {
		return "gpt-5.6"
	}
	if hasOpenAIModelPrefix(normalized, "gpt-5.5") {
		return "gpt-5.5"
	}
	if hasOpenAIModelPrefix(normalized, "gpt-5.4-mini") {
		return "gpt-5.4-mini"
	}
	if hasOpenAIModelPrefix(normalized, "gpt-5.4") {
		return "gpt-5.4"
	}
	if hasOpenAIModelPrefix(normalized, "gpt-5.3-codex-spark") {
		return "gpt-5.3-codex-spark"
	}
	if hasOpenAIModelPrefix(normalized, "gpt-5.3-codex") {
		return "gpt-5.4-mini"
	}
	if hasOpenAIModelPrefix(normalized, "gpt-5.3") {
		return "gpt-5.4-mini"
	}
	if hasOpenAIModelPrefix(normalized, "gpt-5.2-codex") {
		return "gpt-5.4-mini"
	}
	if hasOpenAIModelPrefix(normalized, "gpt-5.2") {
		return "gpt-5.4-mini"
	}
	if hasOpenAIModelPrefix(normalized, "gpt-5.1-codex-max") {
		return "gpt-5.4-mini"
	}
	if hasOpenAIModelPrefix(normalized, "gpt-5.1-codex-mini") {
		return "gpt-5.4-mini"
	}
	if hasOpenAIModelPrefix(normalized, "codex-mini-latest") ||
		hasOpenAIModelPrefix(normalized, "gpt-5-codex-mini") {
		return "gpt-5.4-mini"
	}
	if hasOpenAIModelPrefix(normalized, "gpt-5.1-codex") {
		return "gpt-5.4-mini"
	}
	if hasOpenAIModelPrefix(normalized, "gpt-5.1") {
		return "gpt-5.4-mini"
	}
	if hasOpenAIModelPrefix(normalized, "gpt-5-codex") {
		return "gpt-5.4-mini"
	}
	if hasOpenAIModelPrefix(normalized, "codex") {
		return "gpt-5.4"
	}
	if hasOpenAIModelPrefix(normalized, "gpt-5") {
		return "gpt-5.4-mini"
	}
	if strings.HasPrefix(normalized, "gpt-5.") {
		return ""
	}

	return "gpt-5.4"
}

func canonicalizeCodexModelID(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return ""
	}

	var builder strings.Builder
	builder.Grow(len(model))
	lastDash := false
	for _, r := range model {
		switch r {
		case ' ', '\t', '\n', '\r', '_':
			if builder.Len() == 0 || lastDash {
				continue
			}
			builder.WriteByte('-')
			lastDash = true
		default:
			builder.WriteRune(r)
			lastDash = r == '-'
		}
	}

	return strings.Trim(builder.String(), "-")
}

// extractCodexModelEffort extracts the reasoning effort suffix from the original model name
// by comparing it with the normalized model name.
// e.g. ("gpt-5.3-codex-spark-xhigh", "gpt-5.3-codex-spark") → "xhigh"
//
//	("gpt-5.4-mini-high", "gpt-5.4-mini") → "high"
//	("gpt-5.5", "gpt-5.5") → "" (no effort suffix)
//
// Also handles cross-version normalization (e.g. 5.3→5.4-mini) by matching the effort
// suffix directly from the original model name when the prefix check fails.
func extractCodexModelEffort(originalModel, normalizedModel string) string {
	if originalModel == "" || normalizedModel == "" || originalModel == normalizedModel {
		return ""
	}
	original := strings.ToLower(originalModel)
	normalized := strings.ToLower(normalizedModel)

	// Try stripping path prefix (e.g. "provider/gpt-5.3-codex-xhigh")
	if idx := strings.LastIndex(original, "/"); idx >= 0 {
		original = original[idx+1:]
	}

	validEfforts := map[string]bool{
		"none": true, "low": true, "medium": true, "high": true, "xhigh": true,
	}

	// Primary: the effort suffix is the part after the normalized model name + "-"
	if strings.HasPrefix(original, normalized+"-") {
		suffix := original[len(normalized)+1:]
		if validEfforts[suffix] {
			return suffix
		}
	}

	// gpt-5.1-codex-max 无显式 effort 后缀时，贴近旧 max 档位设置 xhigh。
	if strings.Contains(original, "gpt-5.1-codex-max") &&
		!strings.HasSuffix(original, "-low") &&
		!strings.HasSuffix(original, "-medium") &&
		!strings.HasSuffix(original, "-high") &&
		!strings.HasSuffix(original, "-xhigh") {
		return "xhigh"
	}

	// Fallback: when normalization changes the version (e.g. 5.3→5.4-mini),
	// the prefix won't match. Extract effort from the trailing segment directly.
	if lastDash := strings.LastIndex(original, "-"); lastDash >= 0 {
		suffix := original[lastDash+1:]
		if validEfforts[suffix] {
			return suffix
		}
	}

	return ""
}

// stripCodexModelSuffix removes the "-codex" infix for models where the ChatGPT
// OAuth endpoint requires the base name (e.g. gpt-5.4-codex → gpt-5.4).
// gpt-5.3-codex-spark 保留 -codex 后缀；废弃的 5.3-codex 会先归一化到 gpt-5.4-mini。
// Non-GPT models are returned unchanged.
func stripCodexModelSuffix(model string) string {
	if !strings.HasPrefix(model, "gpt-") {
		return model
	}
	// 当前保留 gpt-5.3-codex-spark 的 -codex 后缀。
	lower := strings.ToLower(model)
	if strings.Contains(lower, "gpt-5.3") {
		return model
	}
	return strings.Replace(model, "-codex", "", 1)
}

// oauthModelFallbackVersion is the safe model version for non-Plus OAuth accounts.
const oauthModelFallbackVersion = "gpt-5.4-mini"

// getOAuthModelFallback returns a downgraded model for OAuth accounts when the
// upstream rejects a model (e.g. Free accounts cannot use gpt-5.4).
// Returns "" if no fallback is available (model is already the fallback or lower).
func getOAuthModelFallback(currentModel string) string {
	lower := strings.ToLower(currentModel)

	// 已经是轻量推荐模型时不继续自动降级。
	if strings.Contains(lower, "gpt-5.4-mini") {
		return ""
	}

	// gpt-5.5 → gpt-5.4
	if strings.Contains(lower, "gpt-5.5") {
		return "gpt-5.4"
	}

	// gpt-5.3-codex-spark 是 Pro research preview，不可用时回退到当前推荐的轻量模型。
	if strings.Contains(lower, "gpt-5.3-codex-spark") {
		return oauthModelFallbackVersion
	}

	// 废弃的 5.3 系列不再作为自动 fallback 目标。
	if strings.Contains(lower, "gpt-5.3") {
		return oauthModelFallbackVersion
	}

	// gpt-5.4 → gpt-5.4-mini
	if strings.Contains(lower, "gpt-5.4") {
		return oauthModelFallbackVersion
	}

	return oauthModelFallbackVersion
}

// isModelNotSupportedError checks if an upstream error message indicates
// a model is not supported/available for the account.
func isModelNotSupportedError(errorMessage string) bool {
	if errorMessage == "" {
		return false
	}
	lower := strings.ToLower(errorMessage)
	return (strings.Contains(lower, "model") || strings.Contains(lower, "gpt-")) &&
		(strings.Contains(lower, "not supported") ||
			strings.Contains(lower, "not available") ||
			strings.Contains(lower, "not found") ||
			strings.Contains(lower, "does not exist") ||
			strings.Contains(lower, "is not supported"))
}

func getNormalizedCodexModel(modelID string) string {
	if modelID == "" {
		return ""
	}
	if mapped, ok := codexModelMap[modelID]; ok {
		return mapped
	}
	lower := strings.ToLower(modelID)
	for key, value := range codexModelMap {
		if strings.ToLower(key) == lower {
			return value
		}
	}
	return ""
}

func getOpenCodeCachedPrompt(url, cacheFileName, metaFileName string) string {
	cacheDir := codexCachePath("")
	if cacheDir == "" {
		return ""
	}
	cacheFile := filepath.Join(cacheDir, cacheFileName)
	metaFile := filepath.Join(cacheDir, metaFileName)

	var cachedContent string
	if content, ok := readFile(cacheFile); ok {
		cachedContent = content
	}

	var meta opencodeCacheMetadata
	if loadJSON(metaFile, &meta) && meta.LastChecked > 0 && cachedContent != "" {
		if time.Since(time.UnixMilli(meta.LastChecked)) < codexCacheTTL {
			return cachedContent
		}
	}

	content, etag, status, err := fetchWithETag(url, meta.ETag)
	if err == nil && status == http.StatusNotModified && cachedContent != "" {
		return cachedContent
	}
	if err == nil && status >= 200 && status < 300 && content != "" {
		_ = writeFile(cacheFile, content)
		meta = opencodeCacheMetadata{
			ETag:        etag,
			LastFetch:   time.Now().UTC().Format(time.RFC3339),
			LastChecked: time.Now().UnixMilli(),
		}
		_ = writeJSON(metaFile, meta)
		return content
	}

	return cachedContent
}

func getOpenCodeCodexHeader() string {
	// 优先从 opencode 仓库缓存获取指令。
	opencodeInstructions := getOpenCodeCachedPrompt(opencodeCodexHeaderURL, "opencode-codex-header.txt", "opencode-codex-header-meta.json")

	// 若 opencode 指令可用，直接返回。
	if opencodeInstructions != "" {
		return opencodeInstructions
	}

	// 否则回退使用本地 Codex CLI 指令。
	return getCodexCLIInstructions()
}

func getCodexCLIInstructions() string {
	return codexCLIInstructions
}

func GetOpenCodeInstructions() string {
	return getOpenCodeCodexHeader()
}

// GetCodexCLIInstructions 返回内置的 Codex CLI 指令内容。
func GetCodexCLIInstructions() string {
	return getCodexCLIInstructions()
}

// ReplaceWithCodexInstructions 将请求 instructions 替换为内置 Codex 指令（必要时）。
func ReplaceWithCodexInstructions(reqBody map[string]any) bool {
	codexInstructions := strings.TrimSpace(getCodexCLIInstructions())
	if codexInstructions == "" {
		return false
	}

	existingInstructions, _ := reqBody["instructions"].(string)
	if strings.TrimSpace(existingInstructions) != codexInstructions {
		reqBody["instructions"] = codexInstructions
		return true
	}

	return false
}

// IsInstructionError 判断错误信息是否与指令格式/系统提示相关。
func IsInstructionError(errorMessage string) bool {
	if errorMessage == "" {
		return false
	}

	lowerMsg := strings.ToLower(errorMessage)
	instructionKeywords := []string{
		"instruction",
		"instructions",
		"system prompt",
		"system message",
		"invalid prompt",
		"prompt format",
	}

	for _, keyword := range instructionKeywords {
		if strings.Contains(lowerMsg, keyword) {
			return true
		}
	}

	return false
}

// filterCodexInput 按需过滤 item_reference 与 id。
// preserveReferences 为 true 时保持引用与 id，以满足续链请求对上下文的依赖。
func filterCodexInput(input []any, preserveReferences bool) []any {
	filtered := make([]any, 0, len(input))
	for _, item := range input {
		m, ok := item.(map[string]any)
		if !ok {
			filtered = append(filtered, item)
			continue
		}
		typ, _ := m["type"].(string)
		if typ == "item_reference" {
			if !preserveReferences {
				continue
			}
			newItem := make(map[string]any, len(m))
			for key, value := range m {
				newItem[key] = value
			}
			delete(newItem, "call_id")
			filtered = append(filtered, newItem)
			continue
		}

		newItem := m
		copied := false
		// 仅在需要修改字段时创建副本，避免直接改写原始输入。
		ensureCopy := func() {
			if copied {
				return
			}
			newItem = make(map[string]any, len(m))
			for key, value := range m {
				newItem[key] = value
			}
			copied = true
		}

		if !isCodexToolCallItemType(typ) {
			if _, hasCallID := m["call_id"]; hasCallID {
				ensureCopy()
				delete(newItem, "call_id")
			}
		}

		if !preserveReferences {
			ensureCopy()
			delete(newItem, "id")
		}

		filtered = append(filtered, newItem)
	}
	return filtered
}

func isCodexToolCallItemType(typ string) bool {
	switch typ {
	case "function_call", "function_call_output",
		"computer_call", "computer_call_output",
		"local_shell_call",
		"shell_call", "shell_call_output",
		"apply_patch_call", "apply_patch_call_output",
		"custom_tool_call", "custom_tool_call_output",
		"tool_search_call", "tool_search_output",
		"program", "program_output":
		return true
	default:
		return false
	}
}

func normalizeCodexTools(reqBody map[string]any) bool {
	rawTools, ok := reqBody["tools"]
	if !ok || rawTools == nil {
		return false
	}
	tools, ok := rawTools.([]any)
	if !ok {
		return false
	}

	modified := false
	validTools := make([]any, 0, len(tools))

	for _, tool := range tools {
		toolMap, ok := tool.(map[string]any)
		if !ok {
			// Keep unknown structure as-is to avoid breaking upstream behavior.
			validTools = append(validTools, tool)
			continue
		}

		toolType, _ := toolMap["type"].(string)
		toolType = strings.TrimSpace(toolType)
		if toolType != "function" {
			validTools = append(validTools, toolMap)
			continue
		}

		// OpenAI Responses-style tools use top-level name/parameters.
		if name, ok := toolMap["name"].(string); ok && strings.TrimSpace(name) != "" {
			validTools = append(validTools, toolMap)
			continue
		}

		// ChatCompletions-style tools use {type:"function", function:{...}}.
		functionValue, hasFunction := toolMap["function"]
		function, ok := functionValue.(map[string]any)
		if !hasFunction || functionValue == nil || !ok || function == nil {
			// Drop invalid function tools.
			modified = true
			continue
		}

		if _, ok := toolMap["name"]; !ok {
			if name, ok := function["name"].(string); ok && strings.TrimSpace(name) != "" {
				toolMap["name"] = name
				modified = true
			}
		}
		if _, ok := toolMap["description"]; !ok {
			if desc, ok := function["description"].(string); ok && strings.TrimSpace(desc) != "" {
				toolMap["description"] = desc
				modified = true
			}
		}
		if _, ok := toolMap["parameters"]; !ok {
			if params, ok := function["parameters"]; ok {
				toolMap["parameters"] = params
				modified = true
			}
		}
		if _, ok := toolMap["strict"]; !ok {
			if strict, ok := function["strict"]; ok {
				toolMap["strict"] = strict
				modified = true
			}
		}

		validTools = append(validTools, toolMap)
	}

	if modified {
		reqBody["tools"] = validTools
	}

	return modified
}

func hasOpenAIImageGenerationTool(reqBody map[string]any) bool {
	rawTools, ok := reqBody["tools"]
	if !ok || rawTools == nil {
		return false
	}
	tools, ok := rawTools.([]any)
	if !ok {
		return false
	}
	for _, rawTool := range tools {
		toolMap, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(firstNonEmptyString(toolMap["type"])) == "image_generation" {
			return true
		}
	}
	return false
}

func normalizeOpenAIResponsesImageGenerationTools(reqBody map[string]any) bool {
	rawTools, ok := reqBody["tools"]
	if !ok || rawTools == nil {
		return false
	}
	tools, ok := rawTools.([]any)
	if !ok {
		return false
	}

	modified := false
	for _, rawTool := range tools {
		toolMap, ok := rawTool.(map[string]any)
		if !ok || strings.TrimSpace(firstNonEmptyString(toolMap["type"])) != "image_generation" {
			continue
		}
		if _, ok := toolMap["output_format"]; !ok {
			if value := strings.TrimSpace(firstNonEmptyString(toolMap["format"])); value != "" {
				toolMap["output_format"] = value
				modified = true
			}
		}
		if _, ok := toolMap["output_compression"]; !ok {
			if value, exists := toolMap["compression"]; exists && value != nil {
				toolMap["output_compression"] = value
				modified = true
			}
		}
		if _, ok := toolMap["format"]; ok {
			delete(toolMap, "format")
			modified = true
		}
		if _, ok := toolMap["compression"]; ok {
			delete(toolMap, "compression")
			modified = true
		}
	}
	return modified
}

func ensureOpenAIResponsesImageGenerationTool(reqBody map[string]any) bool {
	if len(reqBody) == 0 {
		return false
	}

	tool := map[string]any{
		"type":          "image_generation",
		"output_format": "png",
	}

	rawTools, ok := reqBody["tools"]
	if !ok || rawTools == nil {
		reqBody["tools"] = []any{tool}
		return true
	}

	tools, ok := rawTools.([]any)
	if !ok {
		reqBody["tools"] = []any{tool}
		return true
	}
	for _, rawTool := range tools {
		toolMap, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(firstNonEmptyString(toolMap["type"])) == "image_generation" {
			return false
		}
	}

	reqBody["tools"] = append(tools, tool)
	return true
}

func applyCodexImageGenerationBridgeInstructions(reqBody map[string]any) bool {
	if len(reqBody) == 0 || !hasOpenAIImageGenerationTool(reqBody) {
		return false
	}

	existing, _ := reqBody["instructions"].(string)
	if strings.Contains(existing, codexImageGenerationBridgeMarker) {
		return false
	}

	existing = strings.TrimRight(existing, " \t\r\n")
	if strings.TrimSpace(existing) == "" {
		reqBody["instructions"] = codexImageGenerationBridgeText
		return true
	}

	reqBody["instructions"] = existing + "\n\n" + codexImageGenerationBridgeText
	return true
}

func validateOpenAIResponsesImageModel(reqBody map[string]any, model string) error {
	if !hasOpenAIImageGenerationTool(reqBody) {
		return nil
	}
	model = strings.TrimSpace(model)
	if !isOpenAIImageGenerationModel(model) {
		return nil
	}
	return fmt.Errorf("/v1/responses image_generation requests require a Responses-capable text model; image-only model %q is not allowed", model)
}

func codexCachePath(filename string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	cacheDir := filepath.Join(home, ".opencode", "cache")
	if filename == "" {
		return cacheDir
	}
	return filepath.Join(cacheDir, filename)
}

func readFile(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
}

func writeFile(path, content string) error {
	if path == "" {
		return fmt.Errorf("empty cache path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func loadJSON(path string, target any) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	if err := json.Unmarshal(data, target); err != nil {
		return false
	}
	return true
}

func writeJSON(path string, value any) error {
	if path == "" {
		return fmt.Errorf("empty json path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func fetchWithETag(url, etag string) (string, string, int, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", "", 0, err
	}
	req.Header.Set("User-Agent", "sub2api-codex")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", 0, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", resp.StatusCode, err
	}
	return string(body), resp.Header.Get("etag"), resp.StatusCode, nil
}
