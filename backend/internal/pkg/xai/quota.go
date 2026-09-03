package xai

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// QuotaWindow is one xAI rate-limit dimension (requests or tokens).
type QuotaWindow struct {
	Limit     *int64 `json:"limit,omitempty"`
	Remaining *int64 `json:"remaining,omitempty"`
	ResetUnix *int64 `json:"reset_unix,omitempty"`
	ResetAt   string `json:"reset_at,omitempty"`
}

// QuotaSnapshot is a safe, serializable subset of xAI response headers.
// Headers is intentionally allowlisted so account metadata never stores
// unrelated or sensitive upstream response headers.
type QuotaSnapshot struct {
	Requests          *QuotaWindow      `json:"requests,omitempty"`
	Tokens            *QuotaWindow      `json:"tokens,omitempty"`
	RetryAfterSeconds *int              `json:"retry_after_seconds,omitempty"`
	SubscriptionTier  string            `json:"subscription_tier,omitempty"`
	EntitlementStatus string            `json:"entitlement_status,omitempty"`
	StatusCode        int               `json:"status_code,omitempty"`
	Headers           map[string]string `json:"headers,omitempty"`
	UpdatedAt         string            `json:"updated_at"`
}

var quotaHeaderAllowlist = []string{
	"x-ratelimit-limit-requests",
	"x-ratelimit-remaining-requests",
	"x-ratelimit-reset-requests",
	"x-ratelimit-limit-tokens",
	"x-ratelimit-remaining-tokens",
	"x-ratelimit-reset-tokens",
	"x-rate-limit-limit-requests",
	"x-rate-limit-remaining-requests",
	"x-rate-limit-reset-requests",
	"x-rate-limit-limit-tokens",
	"x-rate-limit-remaining-tokens",
	"x-rate-limit-reset-tokens",
	"retry-after",
	"x-subscription-tier",
	"xai-subscription-tier",
	"x-xai-subscription-tier",
	"x-xai-user-tier",
	"xai-user-tier",
	"xai-tier",
	"x-plan-tier",
	"x-entitlement-status",
	"xai-entitlement-status",
	"x-xai-entitlement-status",
	"x-xai-user-entitlement-status",
}

// ParseQuotaHeaders extracts xAI quota data from either successful or error
// responses. It returns nil when the response contains no useful quota signal.
func ParseQuotaHeaders(headers http.Header, statusCode int) *QuotaSnapshot {
	if headers == nil {
		return nil
	}

	snapshot := &QuotaSnapshot{
		Requests:   parseQuotaWindow(headers, "requests"),
		Tokens:     parseQuotaWindow(headers, "tokens"),
		StatusCode: statusCode,
		Headers:    make(map[string]string),
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	snapshot.RetryAfterSeconds = parseRetryAfter(getHeader(headers, "retry-after"))
	snapshot.SubscriptionTier = firstHeader(headers,
		"xai-subscription-tier", "x-subscription-tier", "x-xai-subscription-tier",
		"x-xai-user-tier", "xai-user-tier", "xai-tier", "x-plan-tier")
	snapshot.EntitlementStatus = firstHeader(headers,
		"xai-entitlement-status", "x-entitlement-status", "x-xai-entitlement-status",
		"x-xai-user-entitlement-status", "x-user-entitlement-status")

	for _, name := range quotaHeaderAllowlist {
		if value := strings.TrimSpace(getHeader(headers, name)); value != "" {
			snapshot.Headers[name] = value
		}
	}

	if snapshot.Requests == nil && snapshot.Tokens == nil &&
		snapshot.RetryAfterSeconds == nil && snapshot.SubscriptionTier == "" &&
		snapshot.EntitlementStatus == "" && len(snapshot.Headers) == 0 {
		return nil
	}
	return snapshot
}

func parseQuotaWindow(headers http.Header, dimension string) *QuotaWindow {
	window := &QuotaWindow{
		Limit:     parseInt64Ptr(firstHeader(headers, "x-ratelimit-limit-"+dimension, "x-rate-limit-limit-"+dimension)),
		Remaining: parseInt64Ptr(firstHeader(headers, "x-ratelimit-remaining-"+dimension, "x-rate-limit-remaining-"+dimension)),
	}
	if reset := parseResetHeader(firstHeader(headers, "x-ratelimit-reset-"+dimension, "x-rate-limit-reset-"+dimension)); reset != nil {
		window.ResetUnix = reset
		window.ResetAt = time.Unix(*reset, 0).UTC().Format(time.RFC3339)
	}
	if window.Limit == nil && window.Remaining == nil && window.ResetUnix == nil {
		return nil
	}
	return window
}

func parseResetHeader(raw string) *int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if value, err := strconv.ParseInt(raw, 10, 64); err == nil {
		// Accept Unix seconds, Unix milliseconds, and relative seconds.
		switch {
		case value >= 1_000_000_000_000:
			value /= 1000
		case value >= 1_000_000_000:
			// Already a plausible Unix-seconds timestamp.
		default:
			value = time.Now().Unix() + value
		}
		return &value
	}
	if duration, err := time.ParseDuration(raw); err == nil && duration > 0 {
		return ptrInt64(time.Now().Add(duration).Unix())
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		value := parsed.Unix()
		return &value
	}
	return nil
}

func ptrInt64(value int64) *int64 {
	return &value
}

func parseRetryAfter(raw string) *int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if value, err := strconv.Atoi(raw); err == nil {
		if value < 0 {
			value = 0
		}
		return &value
	}
	if parsed, err := http.ParseTime(raw); err == nil {
		value := int(time.Until(parsed).Seconds())
		if value < 0 {
			value = 0
		}
		return &value
	}
	return nil
}

func parseInt64Ptr(raw string) *int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil
	}
	return &value
}

func firstHeader(headers http.Header, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(getHeader(headers, name)); value != "" {
			return value
		}
	}
	return ""
}

func getHeader(headers http.Header, name string) string {
	if value := headers.Get(name); value != "" {
		return value
	}
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}
