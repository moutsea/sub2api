package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

// isGrokContentPolicyRejection identifies request-scoped safety refusals. A
// content refusal cannot be fixed by selecting another account, so failover
// would only waste quota and obscure the useful upstream error.
func isGrokContentPolicyRejection(statusCode int, body []byte) bool {
	if statusCode != http.StatusForbidden || len(body) == 0 {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(string(body)))
	var payload any
	if json.Unmarshal(body, &payload) == nil {
		if grokStructuredAccountAccessMarker(payload) {
			return false
		}
		if grokStructuredContentPolicyMarker(payload) {
			return true
		}
	}
	for _, phrase := range []string{
		"content_policy", "content policy", "content moderation", "content_filter",
		"content filter", "prohibited content", "forbidden content", "prompt violates",
		"input violates", "request violates", "violates usage guidelines", "text is sensitive",
		"image is sensitive", "blocked by policy", "rejected by policy",
	} {
		if strings.Contains(text, phrase) {
			return !grokAccountAccessMessage(text)
		}
	}
	return false
}

func grokAccountAccessMessage(text string) bool {
	for _, phrase := range []string{
		"account suspended", "account has been suspended", "account disabled", "account has been disabled",
		"user suspended", "user has been suspended", "user disabled", "user has been disabled",
		"subscription required", "entitlement required", "not entitled",
	} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

func grokStructuredAccountAccessMarker(value any) bool {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			normalizedKey := normalizeGrokErrorMarker(key)
			if normalizedKey == "code" || normalizedKey == "error_code" || normalizedKey == "type" || normalizedKey == "category" || normalizedKey == "reason" {
				if marker, ok := child.(string); ok && isGrokAccountAccessCode(marker) {
					return true
				}
			}
			if grokStructuredAccountAccessMarker(child) {
				return true
			}
		}
	case []any:
		for _, child := range node {
			if grokStructuredAccountAccessMarker(child) {
				return true
			}
		}
	}
	return false
}

func grokStructuredContentPolicyMarker(value any) bool {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			normalizedKey := normalizeGrokErrorMarker(key)
			if normalizedKey == "code" || normalizedKey == "error_code" || normalizedKey == "type" || normalizedKey == "category" || normalizedKey == "reason" {
				if marker, ok := child.(string); ok && isGrokContentPolicyCode(marker) {
					return true
				}
			}
			if grokStructuredContentPolicyMarker(child) {
				return true
			}
		}
	case []any:
		for _, child := range node {
			if grokStructuredContentPolicyMarker(child) {
				return true
			}
		}
	}
	return false
}

func normalizeGrokErrorMarker(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, " ", "_")
	return value
}

func isGrokContentPolicyCode(value string) bool {
	switch normalizeGrokErrorMarker(value) {
	case "content_filter", "content_policy", "content_policy_violation", "content_moderation", "cyber_policy", "new_sensitive":
		return true
	default:
		return false
	}
}

func isGrokAccountAccessCode(value string) bool {
	switch normalizeGrokErrorMarker(value) {
	case "account_suspended", "account_disabled", "user_suspended", "user_disabled", "subscription_required", "entitlement_required", "not_entitled", "plan_required":
		return true
	default:
		return false
	}
}

func grokErrorText(body []byte) string {
	message := strings.TrimSpace(extractUpstreamErrorMessage(body))
	if message != "" {
		return strings.ToLower(message)
	}
	return strings.ToLower(strings.TrimSpace(string(body)))
}

// shouldFailoverGrokUpstreamError is body-aware for Grok. It keeps content
// policy and ordinary client validation errors on the current account while
// allowing quota, authentication, capacity, and server failures to try a
// sibling account.
func (s *OpenAIGatewayService) shouldFailoverGrokUpstreamError(statusCode int, body []byte) bool {
	if isGrokContentPolicyRejection(statusCode, body) {
		return false
	}
	text := grokErrorText(body)
	if statusCode == http.StatusUnprocessableEntity && isGrokDecoderCompatibilityError(body) {
		return true
	}
	if statusCode == http.StatusBadRequest {
		return strings.Contains(text, "free usage") ||
			strings.Contains(text, "quota") ||
			strings.Contains(text, "spending limit") ||
			strings.Contains(text, "rate limit") ||
			strings.Contains(text, "decrypt") ||
			strings.Contains(text, "decode")
	}
	return statusCode == http.StatusUnauthorized ||
		statusCode == http.StatusPaymentRequired ||
		statusCode == http.StatusForbidden ||
		statusCode == http.StatusTooManyRequests ||
		statusCode == http.StatusGatewayTimeout ||
		statusCode >= 500
}

func isGrokDecoderCompatibilityError(body []byte) bool {
	text := strings.ToLower(string(body))
	decoderSignal := strings.Contains(text, "untagged enum") ||
		strings.Contains(text, "decode") ||
		strings.Contains(text, "deserialize") ||
		strings.Contains(text, "decoder")
	inputSignal := strings.Contains(text, "modelinput") ||
		strings.Contains(text, "model input") ||
		strings.Contains(text, "input[") ||
		strings.Contains(text, "input.")
	return decoderSignal && inputSignal
}

// handleGrokUpstreamError applies only temporary scheduling state. Grok OAuth
// and API-key credentials may point at shared pools, so generic error handling
// must never permanently disable them for a transient upstream response.
func (s *RateLimitService) handleGrokUpstreamError(ctx context.Context, account *Account, statusCode int, headers http.Header, body []byte) bool {
	if s == nil || account == nil || s.accountRepo == nil {
		return false
	}
	if isGrokContentPolicyRejection(statusCode, body) {
		return false
	}
	text := grokErrorText(body)
	now := time.Now()
	cooldown := time.Duration(0)
	rateLimited := false
	switch {
	case strings.Contains(text, "free usage"), strings.Contains(text, "free-usage"), strings.Contains(text, "spending limit"):
		cooldown = 30 * time.Minute
	case statusCode == http.StatusPaymentRequired:
		cooldown = 30 * time.Minute
	case statusCode == http.StatusTooManyRequests || strings.Contains(text, "rate limit") || strings.Contains(text, "quota"):
		cooldown = grokResetCooldown(headers, now)
		rateLimited = true
	case statusCode == http.StatusUnauthorized:
		cooldown = 10 * time.Minute
	case statusCode == http.StatusForbidden:
		cooldown = 10 * time.Minute
	case statusCode >= 500:
		cooldown = 2 * time.Minute
	}
	if cooldown <= 0 {
		return false
	}
	until := now.Add(cooldown)
	if rateLimited {
		if err := s.accountRepo.SetRateLimited(ctx, account.ID, until); err != nil {
			return false
		}
		account.RateLimitedAt = &now
		account.RateLimitResetAt = &until
	} else {
		if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, until, "grok upstream temporary error"); err != nil {
			return false
		}
		account.TempUnschedulableUntil = &until
		account.TempUnschedulableReason = "grok upstream temporary error"
	}
	return false
}

func grokResetCooldown(headers http.Header, now time.Time) time.Duration {
	const maxCooldown = 24 * time.Hour
	snapshot := xai.ParseQuotaHeaders(headers, http.StatusTooManyRequests)
	if snapshot != nil {
		if snapshot.RetryAfterSeconds != nil && *snapshot.RetryAfterSeconds > 0 {
			cooldown := time.Duration(*snapshot.RetryAfterSeconds) * time.Second
			if cooldown > maxCooldown {
				return maxCooldown
			}
			return cooldown
		}
		for _, window := range []*xai.QuotaWindow{snapshot.Requests, snapshot.Tokens} {
			if window == nil || window.Remaining == nil || *window.Remaining > 0 || window.ResetUnix == nil {
				continue
			}
			if reset := time.Unix(*window.ResetUnix, 0).Sub(now); reset > 0 {
				if reset > maxCooldown {
					return maxCooldown
				}
				return reset
			}
		}
	}
	return 2 * time.Minute
}

// Keep JSON error payloads useful when the upstream nests the message under a
// non-standard field. This helper is intentionally small and defensive.
func grokErrorCode(body []byte) string {
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	if code, ok := payload["code"].(string); ok {
		return strings.ToLower(strings.TrimSpace(code))
	}
	if nested, ok := payload["error"].(map[string]any); ok {
		if code, ok := nested["code"].(string); ok {
			return strings.ToLower(strings.TrimSpace(code))
		}
	}
	return ""
}
