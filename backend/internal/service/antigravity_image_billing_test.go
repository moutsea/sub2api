package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type imageBillingFailWriter struct {
	gin.ResponseWriter
	failOn string
}

func (w imageBillingFailWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), w.failOn) {
		return 0, io.ErrClosedPipe
	}
	return w.ResponseWriter.Write(p)
}

func TestAntigravityImageBillingPreservesDeliveredImagesOnFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	imageA := `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"YQ=="}}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":100}}`
	imageB := `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"Yg=="}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":200}}`
	for _, tc := range []struct {
		name      string
		chunks    []string
		ending    string
		failOn    string
		nonstream bool
		want      int
	}{
		{name: "read error after terminal image", chunks: []string{imageB}, want: 1},
		{name: "read error after multiple images", chunks: []string{imageA, imageB}, want: 2},
		{name: "idle timeout after image", chunks: []string{imageA}, ending: "timeout", want: 1},
		{name: "oversized trailing frame", chunks: []string{imageA}, ending: "oversized", want: 1},
		{name: "write failure after first image", chunks: []string{imageA, imageB}, failOn: "Yg==", want: 1},
		{name: "first image write fails", chunks: []string{imageA}, failOn: "YQ=="},
		{name: "no output images", chunks: []string{`{"candidates":[{"content":{"parts":[{"text":"Thinking"},{"thought":true,"inlineData":{"mimeType":"image/png","data":"YQ=="}}]}}]}`}},
		{name: "incomplete image frame", chunks: []string{`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"YQ`}},
		{name: "nonstream image not delivered", chunks: []string{imageB}, nonstream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			var payload strings.Builder
			for _, chunk := range tc.chunks {
				fmt.Fprintf(&payload, "data: {\"response\":%s}\n\n", chunk)
			}
			var reader io.ReadCloser = io.NopCloser(io.MultiReader(strings.NewReader(payload.String()), iotest.ErrReader(io.ErrUnexpectedEOF)))
			if tc.ending == "timeout" {
				pipe, writer := io.Pipe()
				t.Cleanup(func() { _ = pipe.Close(); _ = writer.Close() })
				go func() { _, _ = io.WriteString(writer, payload.String()) }()
				reader = pipe
				cfg.Gateway.StreamDataIntervalTimeout = 1
			} else if tc.ending == "oversized" {
				cfg.Gateway.MaxLineSize = 64 * 1024
				reader = io.NopCloser(strings.NewReader(payload.String() + "data: " + strings.Repeat("x", 128*1024)))
			}
			upstream := gemini4TestUpstream(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Request-Id": {"image-request"}}, Body: reader}, nil
			})
			svc := &AntigravityGatewayService{httpUpstream: upstream, tokenProvider: &AntigravityTokenProvider{}, settingService: &SettingService{cfg: cfg, settingRepo: continuationSettingRepo{}}}
			account := &Account{ID: 42, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "test", "project_id": "project"}}
			body := `{"contents":[{"parts":[{"text":"Draw"}]}],"generationConfig":{"imageConfig":{"imageSize":"4K"}}}`
			action := "streamGenerateContent"
			if tc.nonstream {
				action = "generateContent"
			}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-pro-image:"+action, strings.NewReader(body))
			if tc.failOn != "" {
				c.Writer = imageBillingFailWriter{ResponseWriter: c.Writer, failOn: tc.failOn}
			}
			result, err := svc.ForwardGemini(c.Request.Context(), c, account, "gemini-3-pro-image", action, !tc.nonstream, []byte(body))
			require.Error(t, err)
			if tc.want == 0 {
				require.Nil(t, result, "failed requests without delivered images must not be billed")
				return
			}
			require.NotNil(t, result)
			require.True(t, result.PartialUsage)
			require.True(t, result.ImageBilling)
			require.Equal(t, tc.want, result.ImageCount)
			require.Equal(t, "4K", result.ImageSize)
			require.Equal(t, "image-request", result.RequestID)
			require.Equal(t, "gemini-3-pro-image", result.Model)
			require.NotNil(t, result.FirstTokenMs)
			require.Equal(t, tc.want, strings.Count(rec.Body.String(), `"mimeType":"image/png"`))
		})
	}
}

func TestAntigravityImageBillingUsesReturnedImages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name   string
		chunks []string
		want   int
	}{
		{
			name:   "blocked prompt",
			chunks: []string{`{"promptFeedback":{"blockReason":"SAFETY"},"usageMetadata":{"promptTokenCount":10}}`},
		},
		{
			name:   "text only",
			chunks: []string{`{"candidates":[{"content":{"parts":[{"text":"Please clarify"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}`},
		},
		{
			name:   "thought images are not output images",
			chunks: []string{`{"candidates":[{"content":{"parts":[{"thought":true,"inlineData":{"mimeType":"image/png","data":"YQ=="}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"thoughtsTokenCount":5}}`},
		},
		{
			name: "one image with other parts", want: 1,
			chunks: []string{`{"candidates":[{"content":{"parts":[{"text":"Result"},{"thought":true,"inlineData":{"mimeType":"image/png","data":"YQ=="}},{"inlineData":{"mimeType":"audio/wav","data":"YQ=="}},{"inlineData":{"mimeType":"image/png","data":""}},{"inlineData":{"mimeType":"image/png","data":"Yg=="}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":100}}`},
		},
		{
			name: "multiple images in one chunk", want: 2,
			chunks: []string{`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"YQ=="}},{"fileData":{"mimeType":"image/jpeg","fileUri":"https://example.com/generated.jpg"}},{"fileData":{"mimeType":"image/png","fileUri":""}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":200}}`},
		},
		{
			name: "separate images with identical bytes", want: 2,
			chunks: []string{
				`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"YQ=="}}]}}]}`,
				`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"YQ=="}}]},"finishReason":"STOP"}]}`,
				`{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":200}}`,
			},
		},
	} {
		for _, stream := range []bool{false, true} {
			for _, subscriptionBilling := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%t/subscription=%t", tc.name, stream, subscriptionBilling), func(t *testing.T) {
					cfg := &config.Config{}
					var payload strings.Builder
					for _, chunk := range tc.chunks {
						fmt.Fprintf(&payload, "data: {\"response\":%s}\n\n", chunk)
					}
					upstream := gemini4TestUpstream(func(req *http.Request) (*http.Response, error) {
						return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload.String()))}, nil
					})
					svc := &AntigravityGatewayService{httpUpstream: upstream, tokenProvider: &AntigravityTokenProvider{}, settingService: &SettingService{cfg: cfg, settingRepo: continuationSettingRepo{}}}
					account := &Account{ID: 42, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "test", "project_id": "project"}}
					body := `{"contents":[{"role":"user","parts":[{"text":"Draw"}]}],"generationConfig":{"imageConfig":{"imageSize":"4K"}}}`
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3-pro-image:generateContent", strings.NewReader(body))
					action := "generateContent"
					if stream {
						action = "streamGenerateContent"
					}
					result, err := svc.ForwardGemini(c.Request.Context(), c, account, "gemini-3-pro-image", action, stream, []byte(body))
					require.NoError(t, err)
					require.Equal(t, tc.want, result.ImageCount)
					require.Equal(t, "4K", result.ImageSize)

					logs, users, subs := &geminiTierUsageRepo{}, &geminiTierUserRepo{}, &geminiTierSubscriptionRepo{}
					gateway := &GatewayService{cfg: cfg, billingService: NewBillingService(nil, nil), usageLogRepo: logs, userRepo: users, userSubRepo: subs, billingCacheService: &BillingCacheService{}, deferredService: &DeferredService{}}
					price := 0.5
					groupID := int64(20)
					user := &User{ID: 9}
					key := &APIKey{ID: 7, UserID: user.ID, User: user, GroupID: &groupID, Group: &Group{ID: groupID, RateMultiplier: 1.5, ImagePrice4K: &price}}
					var subscription *UserSubscription
					if subscriptionBilling {
						key.Group.SubscriptionType = SubscriptionTypeSubscription
						subscription = &UserSubscription{ID: 30, UserID: user.ID, GroupID: groupID}
					}
					require.NoError(t, gateway.RecordUsage(context.Background(), &RecordUsageInput{Result: result, APIKey: key, User: user, Account: account, Subscription: subscription}))
					require.Len(t, logs.logs, 1)
					require.Equal(t, tc.want, logs.logs[0].ImageCount)
					wantCost := float64(tc.want) * price
					require.InDelta(t, wantCost, logs.logs[0].TotalCost, 1e-12)
					require.InDelta(t, wantCost*1.5, logs.logs[0].ActualCost, 1e-12)
					if tc.want == 0 {
						require.Empty(t, users.amounts)
						require.Empty(t, subs.amounts)
					} else if subscriptionBilling {
						require.Equal(t, []float64{wantCost}, subs.amounts)
						require.Empty(t, users.amounts)
					} else {
						require.Equal(t, []float64{wantCost * 1.5}, users.amounts)
						require.Empty(t, subs.amounts)
					}
				})
			}
		}
	}
}
