// Package claude provides constants and helpers for Claude API integration.
package claude

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"regexp"

	"github.com/google/uuid"
)

// Claude Code 客户端相关常量

// Beta header 常量
const (
	BetaOAuth                   = "oauth-2025-04-20"
	BetaClaudeCode              = "claude-code-20250219"
	BetaInterleavedThinking     = "interleaved-thinking-2025-05-14"
	BetaFineGrainedToolStreaming = "fine-grained-tool-streaming-2025-05-14"
	BetaContext1M               = "context-1m-2025-08-07"
	BetaPromptCachingScope      = "prompt-caching-scope-2026-01-05"
)

// CLI 版本号常量 — 跟随官方 Claude Code CLI 更新时只需改这里
const (
	CLIVersion            = "2.1.37"                          // claude-cli 版本
	StainlessSDKVersion   = "0.70.0"                          // @anthropic-ai/sdk 版本
	StainlessNodeVersion  = "v24.13.0"                        // Node.js runtime 版本
	StainlessTimeout      = "600"                             // 默认请求超时（秒）
)

// osPlatform 表示一个 OS + Arch 组合，用于 X-Stainless-OS / X-Stainless-Arch
type osPlatform struct {
	OS   string
	Arch string
}

// realisticPlatforms 是真实 Claude Code CLI 用户常见的 OS/Arch 组合
var realisticPlatforms = []osPlatform{
	{"Linux", "x64"},
	{"Linux", "arm64"},
	{"MacOS", "arm64"},
	{"MacOS", "x64"},
}

// RandomPlatform 返回一个随机的 OS/Arch 组合，模拟真实用户多样性
func RandomPlatform() (os, arch string) {
	p := realisticPlatforms[rand.IntN(len(realisticPlatforms))]
	return p.OS, p.Arch
}

// DefaultBetaHeader Claude Code 客户端默认的 anthropic-beta header
const DefaultBetaHeader = BetaClaudeCode + "," + BetaOAuth + "," + BetaInterleavedThinking + "," + BetaFineGrainedToolStreaming + "," + BetaPromptCachingScope

// HaikuBetaHeader Haiku 模型使用的 anthropic-beta header（不需要 claude-code beta）
const HaikuBetaHeader = BetaOAuth + "," + BetaInterleavedThinking + "," + BetaPromptCachingScope

// APIKeyBetaHeader API-key 账号建议使用的 anthropic-beta header（不包含 oauth）
const APIKeyBetaHeader = BetaClaudeCode + "," + BetaInterleavedThinking + "," + BetaFineGrainedToolStreaming + "," + BetaPromptCachingScope

// APIKeyHaikuBetaHeader Haiku 模型在 API-key 账号下使用的 anthropic-beta header（不包含 oauth / claude-code）
const APIKeyHaikuBetaHeader = BetaInterleavedThinking + "," + BetaPromptCachingScope

// DefaultHeaders 是 Claude Code 客户端默认请求头（固定部分）。
// 注意：X-Stainless-OS 和 X-Stainless-Arch 不在此处，由 NewRequestHeaders() 随机生成。
var DefaultHeaders = map[string]string{
	"User-Agent":                                fmt.Sprintf("claude-cli/%s (external, cli)", CLIVersion),
	"X-Stainless-Lang":                          "js",
	"X-Stainless-Package-Version":               StainlessSDKVersion,
	"X-Stainless-Runtime":                       "node",
	"X-Stainless-Runtime-Version":               StainlessNodeVersion,
	"X-Stainless-Retry-Count":                   "0",
	"X-Stainless-Timeout":                       StainlessTimeout,
	"X-App":                                     "cli",
	"Anthropic-Dangerous-Direct-Browser-Access": "true",
}

// NewRequestHeaders 返回一份完整的请求头（含随机 OS/Arch），每次调用生成新的 map。
// 用于需要多样化指纹的场景（如 apikey 渠道转发）。
func NewRequestHeaders() map[string]string {
	headers := make(map[string]string, len(DefaultHeaders)+2)
	for k, v := range DefaultHeaders {
		headers[k] = v
	}
	os, arch := RandomPlatform()
	headers["X-Stainless-OS"] = os
	headers["X-Stainless-Arch"] = arch
	return headers
}

// Model 表示一个 Claude 模型
type Model struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
}

// DefaultModels Claude Code 客户端支持的默认模型列表
var DefaultModels = []Model{
	{
		ID:          "claude-opus-4-6",
		Type:        "model",
		DisplayName: "Claude Opus 4.6",
		CreatedAt:   "2026-02-06T00:00:00Z",
	},
	{
		ID:          "claude-opus-4-6-1m",
		Type:        "model",
		DisplayName: "Claude Opus 4.6 (1M Context)",
		CreatedAt:   "2026-02-06T00:00:00Z",
	},
	{
		ID:          "claude-sonnet-4-6",
		Type:        "model",
		DisplayName: "Claude Sonnet 4.6",
		CreatedAt:   "2026-02-17T00:00:00Z",
	},
	{
		ID:          "claude-sonnet-4-6-1m",
		Type:        "model",
		DisplayName: "Claude Sonnet 4.6 (1M Context)",
		CreatedAt:   "2026-02-17T00:00:00Z",
	},
	{
		ID:          "claude-opus-4-5-20251101",
		Type:        "model",
		DisplayName: "Claude Opus 4.5",
		CreatedAt:   "2025-11-01T00:00:00Z",
	},
	{
		ID:          "claude-sonnet-4-5-20250929",
		Type:        "model",
		DisplayName: "Claude Sonnet 4.5",
		CreatedAt:   "2025-09-29T00:00:00Z",
	},
	{
		ID:          "claude-haiku-4-5-20251001",
		Type:        "model",
		DisplayName: "Claude Haiku 4.5",
		CreatedAt:   "2025-10-01T00:00:00Z",
	},
}

// DefaultModelIDs 返回默认模型的 ID 列表
func DefaultModelIDs() []string {
	ids := make([]string, len(DefaultModels))
	for i, m := range DefaultModels {
		ids[i] = m.ID
	}
	return ids
}

// DefaultTestModel 测试时使用的默认模型
const DefaultTestModel = "claude-sonnet-4-5-20250929"

// userIDPattern 匹配 Claude Code 客户端的 metadata.user_id 格式
var userIDPattern = regexp.MustCompile(`^user_[a-fA-F0-9]{64}_account__session_[\w-]+$`)

// EnsureMetadataUserID 确保请求体中 metadata.user_id 存在且格式合规。
// 如果缺失或格式不对，基于 seed（如 apiKey）生成确定性的合规值。
// 返回处理后的 body（可能未修改）。
func EnsureMetadataUserID(body []byte, seed string) []byte {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}

	var metadata map[string]any
	if raw, ok := req["metadata"]; ok {
		if err := json.Unmarshal(raw, &metadata); err != nil {
			metadata = make(map[string]any)
		}
	} else {
		metadata = make(map[string]any)
	}

	// Check if existing user_id is compliant
	if uid, ok := metadata["user_id"].(string); ok && userIDPattern.MatchString(uid) {
		return body // already compliant, no change
	}

	// Generate deterministic user_id from seed
	metadata["user_id"] = generateUserID(seed)

	metadataBytes, err := json.Marshal(metadata)
	if err != nil {
		return body
	}
	req["metadata"] = metadataBytes

	newBody, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return newBody
}

// generateUserID 生成格式为 user_{64hex}_account__session_{uuid} 的确定性 user_id。
// clientID 部分基于 seed 的 SHA256，sessionUUID 基于 seed 的 UUID v5。
func generateUserID(seed string) string {
	h := sha256.Sum256([]byte(seed))
	clientID := hex.EncodeToString(h[:])                                     // 64 hex chars
	sessionUUID := uuid.NewSHA1(uuid.NameSpaceDNS, []byte("cc:"+seed)).String() // deterministic UUID
	return fmt.Sprintf("user_%s_account__session_%s", clientID, sessionUUID)
}
