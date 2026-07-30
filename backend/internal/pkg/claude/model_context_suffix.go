// Package claude provides constants and helpers for Claude API integration.
package claude

import "strings"

// Claude Code CLI 用 "claude-sonnet-5[1m]" 这种带方括号的写法表示「基础模型 +
// 1M 上下文」。方括号部分不是 Anthropic Messages API 认识的 model ID 的一部分：
// 1M 上下文在 API 上由 anthropic-beta: context-1m-2025-08-07 表达。
//
// 客户端会把这个标签带到派生请求上（例如 auto 权限模式下的 Bash 安全分类器会
// 另起一次 claude-sonnet-5[1m] 请求），而这些派生请求的模型名用户在 UI 上无法
// 干预。因此网关必须自己剥掉后缀，否则模型范围校验会判定模型不可用并返回 400。
const contextSuffix1M = "[1m]"

// SplitModelContextSuffix 把带上下文标记的模型名拆成基础模型名与是否请求 1M 上下文。
//
// 只识别已知的 "[1m]" 后缀，其余方括号内容原样保留：未知后缀交给上游报错，
// 比网关猜测语义更安全。
func SplitModelContextSuffix(model string) (baseModel string, wants1M bool) {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return model, false
	}
	// 后缀由客户端生成，大小写按 "[1m]" 处理即可，但对 "[1M]" 保持宽容。
	if len(trimmed) <= len(contextSuffix1M) {
		return model, false
	}
	tail := trimmed[len(trimmed)-len(contextSuffix1M):]
	if !strings.EqualFold(tail, contextSuffix1M) {
		return model, false
	}
	base := strings.TrimSpace(trimmed[:len(trimmed)-len(contextSuffix1M)])
	if base == "" {
		return model, false
	}
	return base, true
}

// EnsureContext1MBeta 在 beta header 中补齐 context-1m flag，已存在时原样返回。
func EnsureContext1MBeta(clientBetaHeader string) string {
	if strings.Contains(clientBetaHeader, BetaContext1M) {
		return clientBetaHeader
	}
	if strings.TrimSpace(clientBetaHeader) == "" {
		return BetaContext1M
	}
	return clientBetaHeader + "," + BetaContext1M
}
