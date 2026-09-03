package service

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

func TestGrokQuotaFetcherBuildUsageInfo(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	account := &Account{
		Platform: PlatformGrok,
		Extra: map[string]any{
			grokQuotaSnapshotExtraKey: map[string]any{
				"updated_at":         now.Format(time.RFC3339),
				"status_code":        200,
				"subscription_tier":  "supergrok",
				"entitlement_status": "active",
				"requests": map[string]any{
					"limit":     float64(100),
					"remaining": float64(80),
				},
			},
		},
	}

	usage := NewGrokQuotaFetcher().BuildUsageInfo(account)
	if usage.ErrorCode != "" || usage.GrokQuotaSnapshotState != "" {
		t.Fatalf("fetcher should only build the usage payload: %#v", usage)
	}
	if usage.SubscriptionTier != "supergrok" || usage.GrokEntitlementStatus != "active" {
		t.Fatalf("unexpected subscription metadata: %#v", usage)
	}
	if usage.GrokRequestQuota == nil || usage.GrokRequestQuota.Limit == nil || *usage.GrokRequestQuota.Limit != 100 {
		t.Fatalf("unexpected request quota: %#v", usage.GrokRequestQuota)
	}
}

func TestGrokQuotaFetcherUnknownSnapshot(t *testing.T) {
	usage := NewGrokQuotaFetcher().BuildUsageInfo(&Account{Platform: PlatformGrok})
	if usage.ErrorCode != "quota_unknown" || usage.Error == "" {
		t.Fatalf("expected explicit unknown quota state: %#v", usage)
	}
}

func TestGrokQuotaSnapshotFromExtraTypedValue(t *testing.T) {
	snapshot := &xai.QuotaSnapshot{StatusCode: 429, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	got, err := grokQuotaSnapshotFromExtra(map[string]any{grokQuotaSnapshotExtraKey: snapshot})
	if err != nil || got != snapshot {
		t.Fatalf("typed snapshot should be returned directly: got=%#v err=%v", got, err)
	}
}
