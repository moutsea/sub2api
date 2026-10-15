package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type continuationAccounts struct {
	service.AccountRepository
	account service.Account
}

func (r continuationAccounts) ListSchedulableByPlatform(context.Context, string) ([]service.Account, error) {
	return []service.Account{r.account}, nil
}

type continuationUsageLogs struct {
	service.UsageLogRepository
	logs chan service.UsageLog
}

func (r continuationUsageLogs) Create(ctx context.Context, usage *service.UsageLog) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	r.logs <- *usage
	return true, nil
}

type continuationSettings struct{ service.SettingRepository }

func (continuationSettings) GetValue(context.Context, string) (string, error) {
	return "", service.ErrSettingNotFound
}

type continuationUpstream func(*http.Request) (*http.Response, error)

func (f continuationUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return f(req)
}

func TestMessagesRecordsGeminiContinuationUsageOnFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, platform := range []string{service.PlatformGemini, service.PlatformAntigravity} {
		for _, stream := range []bool{false, true} {
			for _, blocked := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%t/blocked=%t", platform, stream, blocked), func(t *testing.T) {
					cfg := &config.Config{RunMode: config.RunModeSimple}
					cfg.Default.RateMultiplier = 1
					account := service.Account{ID: 42, Platform: platform, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
						Credentials: map[string]any{"api_key": "test-key", "access_token": "test-token", "project_id": "project"}}
					if platform == service.PlatformAntigravity {
						account.Type = service.AccountTypeOAuth
					}
					accounts := continuationAccounts{account: account}
					logs := continuationUsageLogs{logs: make(chan service.UsageLog, 4)}
					calls := 0
					upstream := continuationUpstream(func(req *http.Request) (*http.Response, error) {
						calls++
						status := http.StatusOK
						chunk := `{"candidates":[{"content":{"parts":[{"text":"partial"}]},"finishReason":"CONTINUATION","continuationToken":"checkpoint"}],"usageMetadata":{"promptTokenCount":140,"cachedContentTokenCount":40,"candidatesTokenCount":1,"thoughtsTokenCount":65535}}`
						if platform == service.PlatformAntigravity {
							chunk = `{"response":` + chunk + `}`
						}
						payload := "data: " + chunk + "\n\n"
						if calls > 1 {
							status, payload = http.StatusServiceUnavailable, `{"error":"unavailable"}`
							if blocked {
								status = http.StatusOK
								payload = `{"promptFeedback":{"blockReason":"SAFETY"},"usageMetadata":{"promptTokenCount":999}}`
								if platform == service.PlatformAntigravity {
									payload = `{"response":` + payload + `}`
								}
								payload = "data: " + payload + "\n\n"
							}
						}
						return &http.Response{StatusCode: status, Header: http.Header{"X-Request-Id": {"request-1"}}, Body: io.NopCloser(strings.NewReader(payload))}, nil
					})
					billingCache := service.NewBillingCacheService(nil, nil, nil, cfg)
					t.Cleanup(billingCache.Stop)
					gateway := service.NewGatewayService(accounts, nil, logs, nil, nil, nil, cfg, nil, nil,
						service.NewBillingService(nil, nil), nil, billingCache, nil, upstream, &service.DeferredService{},
						nil, nil, nil, nil, nil, nil, nil, nil)
					antigravity := service.NewAntigravityGatewayService(accounts, nil, &service.AntigravityTokenProvider{}, nil, upstream,
						service.NewSettingService(continuationSettings{}, cfg), nil, nil)
					gemini := service.NewGeminiMessagesCompatService(accounts, nil, nil, nil, nil, nil, upstream, antigravity, cfg)
					h := NewGatewayHandler(gateway, gemini, antigravity, nil, nil, nil, service.NewConcurrencyService(nil), billingCache, cfg)
					router := gin.New()
					router.POST("/v1/messages", middleware2.ForcePlatform(platform), func(c *gin.Context) {
						c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 7, UserID: 9, User: &service.User{ID: 9}})
						c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 9})
						c.Set(string(middleware2.ContextKeyTempAPIKey), &service.TempAPIKey{ID: 88})
						h.Messages(c)
					})
					body := fmt.Sprintf(`{"model":"gemini-4-argon","max_tokens":131072,"stream":%t,"messages":[{"role":"user","content":"Write a detailed explanation"}]}`, stream)
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)).WithContext(ctx)
					request.Header.Set("User-Agent", "continuation-test")
					recorder := httptest.NewRecorder()
					router.ServeHTTP(recorder, request)
					cancel() // Accounting must survive the caller's request lifetime.
					require.Equal(t, 2, calls, "a failed continuation must not replay the whole generation")
					if stream {
						require.Contains(t, recorder.Body.String(), "event: error")
					} else {
						wantStatus := http.StatusBadGateway
						if blocked {
							wantStatus = http.StatusBadRequest
						}
						require.Equal(t, wantStatus, recorder.Code)
					}
					if blocked {
						require.Contains(t, recorder.Body.String(), "invalid_request_error")
						require.Contains(t, recorder.Body.String(), "SAFETY")
					}
					select {
					case usage := <-logs.logs:
						require.Equal(t, 65536, usage.OutputTokens)
						require.Equal(t, 100, usage.InputTokens)
						require.Equal(t, 40, usage.CacheReadTokens)
						require.Greater(t, usage.TotalCost, float64(0))
						require.Equal(t, account.ID, usage.AccountID)
						require.Equal(t, int64(88), *usage.TempAPIKeyID)
						require.Equal(t, "continuation-test", *usage.UserAgent)
					case <-time.After(2 * time.Second):
						t.Fatal("failed continuation did not record its completed hop")
					}
					select {
					case <-logs.logs:
						t.Fatal("completed hop was recorded twice")
					case <-time.After(50 * time.Millisecond):
					}
				})
			}
		}
	}
}

type nativeCheckpointReadError struct{}

func (nativeCheckpointReadError) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestGeminiNativeRecordsCheckpointUsageOnFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, provider := range []string{"api_key", "oauth", "antigravity"} {
		for _, stream := range []bool{false, true} {
			if provider == "api_key" && !stream {
				continue
			}
			for _, readError := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%t/read_error=%t", provider, stream, readError), func(t *testing.T) {
					cfg := &config.Config{RunMode: config.RunModeSimple}
					cfg.Default.RateMultiplier = 1
					account := service.Account{ID: 42, Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
						Credentials: map[string]any{"api_key": "test", "access_token": "test", "project_id": "project"}}
					if provider != "api_key" {
						account.Type = service.AccountTypeOAuth
					}
					if provider == "antigravity" {
						account.Platform = service.PlatformAntigravity
					}
					accounts := continuationAccounts{account: account}
					logs := continuationUsageLogs{logs: make(chan service.UsageLog, 4)}
					calls := 0
					upstream := continuationUpstream(func(*http.Request) (*http.Response, error) {
						calls++
						chunks := []string{
							`{"candidates":[{"content":{"parts":[{"text":"committed"}]},"continuationToken":"checkpoint"}],"usageMetadata":{"promptTokenCount":140,"cachedContentTokenCount":40,"candidatesTokenCount":100,"thoughtsTokenCount":32668}}`,
							`{"usageMetadata":{"promptTokenCount":140,"cachedContentTokenCount":40,"candidatesTokenCount":1000,"thoughtsTokenCount":39000}}`,
						}
						var payload strings.Builder
						for _, chunk := range chunks {
							if provider != "api_key" {
								chunk = `{"response":` + chunk + `}`
							}
							fmt.Fprintf(&payload, "data: %s\n\n", chunk)
						}
						var reader io.Reader = strings.NewReader(payload.String())
						if readError {
							reader = io.MultiReader(reader, nativeCheckpointReadError{})
						}
						return &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": {"native-request"}, "X-Gemini-Service-Tier": {"flex"}}, Body: io.NopCloser(reader)}, nil
					})
					billingCache := service.NewBillingCacheService(nil, nil, nil, cfg)
					t.Cleanup(billingCache.Stop)
					billing := service.NewBillingService(nil, nil)
					gateway := service.NewGatewayService(accounts, nil, logs, nil, nil, nil, cfg, nil, nil, billing, nil, billingCache, nil, upstream, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil)
					ag := service.NewAntigravityGatewayService(accounts, nil, &service.AntigravityTokenProvider{}, nil, upstream, service.NewSettingService(continuationSettings{}, cfg), nil, nil)
					gemini := service.NewGeminiMessagesCompatService(accounts, nil, nil, nil, &service.GeminiTokenProvider{}, nil, upstream, ag, cfg)
					h := NewGatewayHandler(gateway, gemini, ag, nil, nil, nil, service.NewConcurrencyService(nil), billingCache, cfg)
					router := gin.New()
					router.POST("/v1beta/models/*modelAction", middleware2.ForcePlatform(account.Platform), func(c *gin.Context) {
						c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 7, UserID: 9, User: &service.User{ID: 9}})
						c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 9})
						c.Set(string(middleware2.ContextKeyTempAPIKey), &service.TempAPIKey{ID: 88})
						h.GeminiV1BetaModels(c)
					})
					action := "generateContent"
					if stream {
						action = "streamGenerateContent"
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					request := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-4-argon:"+action, strings.NewReader(`{"contents":[{"parts":[{"text":"Hello"}]}],"serviceTier":"flex"}`)).WithContext(ctx)
					request.Header.Set("User-Agent", "native-checkpoint-test")
					recorder := httptest.NewRecorder()
					router.ServeHTTP(recorder, request)
					cancel()
					require.Equal(t, 1, calls, "partial failures must not replay generation")
					if stream {
						require.Equal(t, 1, strings.Count(recorder.Body.String(), "event: error"))
					} else {
						require.Equal(t, http.StatusBadGateway, recorder.Code)
					}
					select {
					case usage := <-logs.logs:
						require.Equal(t, 32768, usage.OutputTokens)
						require.Equal(t, 100, usage.InputTokens)
						require.Equal(t, 40, usage.CacheReadTokens)
						want, err := billing.CalculateCostWithServiceTier("gemini-4-argon", service.UsageTokens{InputTokens: 100, OutputTokens: 32768, CacheReadTokens: 40}, 1, "flex")
						require.NoError(t, err)
						require.InDelta(t, want.TotalCost, usage.TotalCost, 1e-12)
						require.Equal(t, "native-request", usage.RequestID)
						require.Equal(t, int64(88), *usage.TempAPIKeyID)
						require.Equal(t, "native-checkpoint-test", *usage.UserAgent)
					case <-time.After(2 * time.Second):
						t.Fatal("native checkpoint usage was not recorded")
					}
					select {
					case <-logs.logs:
						t.Fatal("native checkpoint was billed twice")
					case <-time.After(50 * time.Millisecond):
					}
				})
			}
		}
	}
}

func TestGeminiNativeRecordsDeliveredImagesOnFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	account := service.Account{ID: 42, Platform: service.PlatformAntigravity, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true,
		Credentials: map[string]any{"access_token": "test", "project_id": "project"}}
	accounts := continuationAccounts{account: account}
	logs := continuationUsageLogs{logs: make(chan service.UsageLog, 4)}
	calls := 0
	upstream := continuationUpstream(func(*http.Request) (*http.Response, error) {
		calls++
		payload := "data: " + `{"response":{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"YQ=="}},{"inlineData":{"mimeType":"image/png","data":"Yg=="}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":200}}}` + "\n\n"
		return &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": {"image-request"}}, Body: io.NopCloser(io.MultiReader(strings.NewReader(payload), nativeCheckpointReadError{}))}, nil
	})
	billingCache := service.NewBillingCacheService(nil, nil, nil, cfg)
	t.Cleanup(billingCache.Stop)
	billing := service.NewBillingService(nil, nil)
	gateway := service.NewGatewayService(accounts, nil, logs, nil, nil, nil, cfg, nil, nil, billing, nil, billingCache, nil, upstream, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil)
	ag := service.NewAntigravityGatewayService(accounts, nil, &service.AntigravityTokenProvider{}, nil, upstream, service.NewSettingService(continuationSettings{}, cfg), nil, nil)
	gemini := service.NewGeminiMessagesCompatService(accounts, nil, nil, nil, &service.GeminiTokenProvider{}, nil, upstream, ag, cfg)
	h := NewGatewayHandler(gateway, gemini, ag, nil, nil, nil, service.NewConcurrencyService(nil), billingCache, cfg)
	router := gin.New()
	router.POST("/v1beta/models/*modelAction", middleware2.ForcePlatform(account.Platform), func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 7, UserID: 9, User: &service.User{ID: 9}})
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 9})
		h.GeminiV1BetaModels(c)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-pro-image:streamGenerateContent", strings.NewReader(`{"contents":[{"parts":[{"text":"Draw"}]}],"generationConfig":{"imageConfig":{"imageSize":"4K"}}}`)).WithContext(ctx)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	cancel()
	require.Equal(t, 1, calls, "delivered images must not be regenerated after a trailing read error")
	require.Contains(t, recorder.Body.String(), `"data":"YQ=="`)
	require.Contains(t, recorder.Body.String(), `"data":"Yg=="`)
	require.Equal(t, 1, strings.Count(recorder.Body.String(), "event: error"))
	select {
	case usage := <-logs.logs:
		require.Equal(t, 2, usage.ImageCount)
		require.NotNil(t, usage.ImageSize)
		require.Equal(t, "4K", *usage.ImageSize)
		require.Equal(t, "image-request", usage.RequestID)
		want := billing.CalculateImageCost("gemini-3-pro-image", "4K", 2, nil, 1)
		require.Greater(t, want.TotalCost, float64(0))
		require.InDelta(t, want.TotalCost, usage.TotalCost, 1e-12)
		require.InDelta(t, want.ActualCost, usage.ActualCost, 1e-12)
	case <-time.After(2 * time.Second):
		t.Fatal("delivered images were not recorded after the read error")
	}
	select {
	case <-logs.logs:
		t.Fatal("delivered images were billed twice")
	case <-time.After(50 * time.Millisecond):
	}
}
