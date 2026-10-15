package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestResolveGeminiServiceTier(t *testing.T) {
	for _, tc := range []struct {
		name, body, header, want string
		present                  bool
	}{
		{name: "default", body: `{}`, want: "standard"},
		{name: "snake_fallback", body: `{"service_tier":"priority"}`, want: "priority"},
		{name: "camel_fallback", body: `{"serviceTier":"flex"}`, want: "flex"},
		{name: "downgrade", body: `{"service_tier":"priority"}`, present: true, header: "standard", want: "standard"},
		{name: "confirmed_priority", body: `{}`, present: true, header: " Priority ", want: "priority"},
		{name: "empty_response_tier", body: `{"serviceTier":"priority"}`, present: true, want: "standard"},
		{name: "unknown_response_tier", body: `{"serviceTier":"priority"}`, present: true, header: "unknown", want: "standard"},
		{name: "unknown_request_tier", body: `{"serviceTier":"unknown"}`, want: "standard"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := http.Header{}
			if tc.present {
				headers.Set("X-Gemini-Service-Tier", tc.header)
			}
			require.Equal(t, tc.want, resolveGeminiServiceTier(headers, []byte(tc.body)))
		})
	}
}

type geminiTierUsageRepo struct {
	UsageLogRepository
	logs []UsageLog
}

func (r *geminiTierUsageRepo) Create(_ context.Context, usage *UsageLog) (bool, error) {
	r.logs = append(r.logs, *usage)
	return true, nil
}

type geminiTierUserRepo struct {
	UserRepository
	amounts []float64
}

func (r *geminiTierUserRepo) DeductBalance(_ context.Context, _ int64, amount float64) error {
	r.amounts = append(r.amounts, amount)
	return nil
}

type geminiTierSubscriptionRepo struct {
	UserSubscriptionRepository
	amounts []float64
}

func (r *geminiTierSubscriptionRepo) IncrementUsage(_ context.Context, _ int64, amount float64) error {
	r.amounts = append(r.amounts, amount)
	return nil
}

func TestGemini4NativeServiceTierBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, provider := range []string{"api_key", "oauth", "antigravity"} {
		for _, stream := range []bool{false, true} {
			for _, subscriptionBilling := range []bool{false, true} {
				for _, tc := range []struct{ field, requested, actual string }{
					{"service_tier", "priority", "priority"},
					{"serviceTier", "flex", "flex"},
					{"serviceTier", "priority", "standard"},
				} {
					t.Run(fmt.Sprintf("%s/stream=%t/subscription=%t/%s-to-%s", provider, stream, subscriptionBilling, tc.requested, tc.actual), func(t *testing.T) {
						cfg := &config.Config{}
						cfg.Default.RateMultiplier = 1.5
						wrapped := provider != "api_key"
						upstream := gemini4TestUpstream(func(req *http.Request) (*http.Response, error) {
							var payload map[string]any
							require.NoError(t, json.NewDecoder(req.Body).Decode(&payload))
							if wrapped {
								payload = payload["request"].(map[string]any)
							}
							require.Equal(t, tc.requested, payload[tc.field])
							response := `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1400,"cachedContentTokenCount":400,"candidatesTokenCount":100,"thoughtsTokenCount":200}}`
							if wrapped {
								response = `{"response":` + response + `}`
							}
							contentType := "application/json"
							if wrapped || stream {
								response, contentType = "data: "+response+"\n\n", "text/event-stream"
							}
							return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}, "X-Gemini-Service-Tier": {tc.actual}}, Body: io.NopCloser(strings.NewReader(response))}, nil
						})
						account := &Account{ID: 42, Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test", "access_token": "test", "project_id": "project"}}
						if wrapped {
							account.Type = AccountTypeOAuth
						}
						body := fmt.Sprintf(`{"contents":[{"parts":[{"text":"Hello"}]}],%q:%q}`, tc.field, tc.requested)
						action := "generateContent"
						if stream {
							action = "streamGenerateContent"
						}
						recorder := httptest.NewRecorder()
						c, _ := gin.CreateTestContext(recorder)
						c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-4-argon:"+action, strings.NewReader(body))
						var result *ForwardResult
						var err error
						if provider == "antigravity" {
							account.Platform = PlatformAntigravity
							svc := &AntigravityGatewayService{httpUpstream: upstream, tokenProvider: &AntigravityTokenProvider{}, settingService: &SettingService{cfg: cfg, settingRepo: continuationSettingRepo{}}}
							result, err = svc.ForwardGemini(c.Request.Context(), c, account, "gemini-4-argon", action, stream, []byte(body))
						} else {
							svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: cfg, tokenProvider: &GeminiTokenProvider{}}
							result, err = svc.ForwardNative(c.Request.Context(), c, account, "gemini-4-argon", action, stream, []byte(body))
						}
						require.NoError(t, err)
						require.Equal(t, tc.actual, result.ServiceTier)
						require.Equal(t, ClaudeUsage{InputTokens: 1000, OutputTokens: 300, CacheReadInputTokens: 400}, result.Usage)
						billing := NewBillingService(nil, nil)
						logs, users, subs := &geminiTierUsageRepo{}, &geminiTierUserRepo{}, &geminiTierSubscriptionRepo{}
						gateway := &GatewayService{cfg: cfg, billingService: billing, usageLogRepo: logs, userRepo: users, userSubRepo: subs, billingCacheService: &BillingCacheService{}, deferredService: &DeferredService{}}
						user := &User{ID: 9}
						apiKey := &APIKey{ID: 7, UserID: user.ID, User: user}
						var subscription *UserSubscription
						if subscriptionBilling {
							groupID := int64(20)
							apiKey.GroupID = &groupID
							apiKey.Group = &Group{ID: groupID, SubscriptionType: SubscriptionTypeSubscription, RateMultiplier: 1.5}
							subscription = &UserSubscription{ID: 30, UserID: user.ID, GroupID: groupID}
						}
						require.NoError(t, gateway.RecordUsage(context.Background(), &RecordUsageInput{Result: result, APIKey: apiKey, User: user, Account: account, Subscription: subscription}))
						want, err := billing.CalculateCostWithServiceTier("gemini-4-argon", UsageTokens{InputTokens: 1000, OutputTokens: 300, CacheReadTokens: 400}, 1.5, tc.actual)
						require.NoError(t, err)
						require.Len(t, logs.logs, 1)
						require.InDelta(t, want.TotalCost, logs.logs[0].TotalCost, 1e-12)
						require.InDelta(t, want.ActualCost, logs.logs[0].ActualCost, 1e-12)
						if subscriptionBilling {
							require.Empty(t, users.amounts)
							require.Len(t, subs.amounts, 1)
							require.InDelta(t, want.TotalCost, subs.amounts[0], 1e-12)
						} else {
							require.Empty(t, subs.amounts)
							require.Len(t, users.amounts, 1)
							require.InDelta(t, want.ActualCost, users.amounts[0], 1e-12)
						}
					})
				}
			}
		}
	}
}
