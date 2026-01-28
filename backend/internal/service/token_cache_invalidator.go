package service

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
)

type TokenCacheInvalidator interface {
	InvalidateToken(ctx context.Context, account *Account) error
}

type CompositeTokenCacheInvalidator struct {
	cache GeminiTokenCache // 统一使用一个缓存接口，通过缓存键前缀区分平台
}

func NewCompositeTokenCacheInvalidator(cache GeminiTokenCache) *CompositeTokenCacheInvalidator {
	return &CompositeTokenCacheInvalidator{
		cache: cache,
	}
}

func (c *CompositeTokenCacheInvalidator) InvalidateToken(ctx context.Context, account *Account) error {
	if c == nil || c.cache == nil || account == nil {
		return nil
	}
	if account.Type != AccountTypeOAuth {
		return nil
	}

	// 收集所有可能的缓存键
	var cacheKeys []string
	accountIDStr := strconv.FormatInt(account.ID, 10)

	switch account.Platform {
	case PlatformGemini:
		// Gemini 可能有 project_id 键和 account_id 键两种
		projectID := strings.TrimSpace(account.GetCredential("project_id"))
		if projectID != "" {
			cacheKeys = append(cacheKeys, "gemini:"+projectID)
		}
		cacheKeys = append(cacheKeys, "gemini:account:"+accountIDStr)
	case PlatformAntigravity:
		// Antigravity 可能有 project_id 键和 account_id 键两种
		projectID := strings.TrimSpace(account.GetCredential("project_id"))
		if projectID != "" {
			cacheKeys = append(cacheKeys, "ag:"+projectID)
		}
		cacheKeys = append(cacheKeys, "ag:account:"+accountIDStr)
	case PlatformOpenAI:
		cacheKeys = append(cacheKeys, OpenAITokenCacheKey(account))
	case PlatformAnthropic:
		cacheKeys = append(cacheKeys, ClaudeTokenCacheKey(account))
	default:
		return nil
	}

	// 删除所有可能的缓存键
	var lastErr error
	for _, key := range cacheKeys {
		if err := c.cache.DeleteAccessToken(ctx, key); err != nil {
			slog.Warn("token_cache_invalidate_failed", "account_id", account.ID, "cache_key", key, "error", err)
			lastErr = err
		} else {
			slog.Debug("token_cache_invalidated", "account_id", account.ID, "cache_key", key)
		}
	}
	return lastErr
}
