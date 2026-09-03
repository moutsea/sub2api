package xai

import (
	"net/http"
	"testing"
	"time"
)

func TestParseQuotaHeaders(t *testing.T) {
	headers := http.Header{
		"X-Ratelimit-Limit-Requests":     []string{"100"},
		"X-Ratelimit-Remaining-Requests": []string{"42"},
		"X-Ratelimit-Reset-Requests":     []string{"1735689600000"},
		"X-Ratelimit-Limit-Tokens":       []string{"1000000"},
		"Retry-After":                    []string{"7"},
		"XAI-Subscription-Tier":          []string{"supergrok"},
		"Authorization":                  []string{"should-not-be-stored"},
	}

	snapshot := ParseQuotaHeaders(headers, http.StatusTooManyRequests)
	if snapshot == nil || snapshot.Requests == nil || snapshot.Tokens == nil {
		t.Fatalf("expected quota snapshot with request and token windows: %#v", snapshot)
	}
	if *snapshot.Requests.Limit != 100 || *snapshot.Requests.Remaining != 42 {
		t.Fatalf("unexpected request window: %#v", snapshot.Requests)
	}
	if *snapshot.Requests.ResetUnix != 1735689600 {
		t.Fatalf("expected milliseconds reset to normalize to seconds, got %d", *snapshot.Requests.ResetUnix)
	}
	if *snapshot.RetryAfterSeconds != 7 || snapshot.SubscriptionTier != "supergrok" {
		t.Fatalf("unexpected metadata: %#v", snapshot)
	}
	if _, ok := snapshot.Headers["authorization"]; ok {
		t.Fatal("non-allowlisted header was persisted")
	}
}

func TestParseQuotaHeadersReturnsNilWithoutSignal(t *testing.T) {
	if snapshot := ParseQuotaHeaders(http.Header{"X-Request-ID": []string{"req"}}, 200); snapshot != nil {
		t.Fatalf("expected nil snapshot, got %#v", snapshot)
	}
}

func TestParseQuotaHeadersHTTPDateRetryAfter(t *testing.T) {
	date := time.Now().Add(20 * time.Second).UTC().Format(http.TimeFormat)
	snapshot := ParseQuotaHeaders(http.Header{"Retry-After": []string{date}}, 429)
	if snapshot == nil || snapshot.RetryAfterSeconds == nil || *snapshot.RetryAfterSeconds < 18 {
		t.Fatalf("expected HTTP-date retry-after, got %#v", snapshot)
	}
}

func TestParseQuotaHeadersRelativeReset(t *testing.T) {
	snapshot := ParseQuotaHeaders(http.Header{
		"X-Rate-Limit-Remaining-Requests": []string{"0"},
		"X-Rate-Limit-Reset-Requests":     []string{"60"},
	}, http.StatusTooManyRequests)
	if snapshot == nil || snapshot.Requests == nil || snapshot.Requests.ResetUnix == nil {
		t.Fatalf("expected relative reset to be parsed: %#v", snapshot)
	}
	reset := time.Unix(*snapshot.Requests.ResetUnix, 0)
	if reset.Before(time.Now().Add(50*time.Second)) || reset.After(time.Now().Add(70*time.Second)) {
		t.Fatalf("relative reset = %v, want about one minute from now", reset)
	}
}
