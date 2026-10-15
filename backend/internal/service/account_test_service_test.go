package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type accountTestHTTPUpstreamFunc func(*http.Request) (*http.Response, error)

func (f accountTestHTTPUpstreamFunc) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return f(req)
}

func TestAccountTestServiceOpenAIImageAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name    string
		baseURL string
		wantURL string
	}{
		{"default", "", "https://api.openai.com/v1/images/generations"},
		{"root", "https://images.example.com", "https://images.example.com/v1/images/generations"},
		{"root_trailing_slash", "https://images.example.com/", "https://images.example.com/v1/images/generations"},
		{"base_with_v1", "https://images.example.com/v1", "https://images.example.com/v1/images/generations"},
		{"v1_trailing_slash", "https://images.example.com/v1/", "https://images.example.com/v1/images/generations"},
		{"path_prefix", "https://images.example.com/openai/v1", "https://images.example.com/openai/v1/images/generations"},
		{"full_endpoint", "https://images.example.com/v1/images/generations", "https://images.example.com/v1/images/generations"},
		{"full_endpoint_trailing_slash", "https://images.example.com/v1/images/generations/", "https://images.example.com/v1/images/generations"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			upstream := accountTestHTTPUpstreamFunc(func(req *http.Request) (*http.Response, error) {
				called = true
				require.Equal(t, http.MethodPost, req.Method)
				require.Equal(t, tc.wantURL, req.URL.String())
				require.Equal(t, "Bearer test-image-key", req.Header.Get("Authorization"))
				require.Equal(t, "application/json", req.Header.Get("Content-Type"))

				var payload map[string]any
				require.NoError(t, json.NewDecoder(req.Body).Decode(&payload))
				require.Equal(t, "gpt-image-2.5-flare", payload["model"])
				require.NotEmpty(t, payload["prompt"])
				require.Equal(t, float64(1), payload["n"])
				// GPT Image returns base64 by default and does not support this legacy parameter.
				require.NotContains(t, payload, "response_format")

				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body: io.NopCloser(strings.NewReader(
						`{"data":[{"b64_json":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO5qS9sAAAAASUVORK5CYII="}]}`,
					)),
				}, nil
			})
			account := &Account{
				ID:          42,
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Concurrency: 1,
				Credentials: map[string]any{"api_key": "test-image-key", "base_url": tc.baseURL},
			}
			svc := &AccountTestService{httpUpstream: upstream, cfg: &config.Config{}}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/42/test", nil)

			require.NoError(t, svc.testOpenAIAccountConnection(c, account, "gpt-image-2.5-flare"))
			require.True(t, called)
			require.Contains(t, recorder.Body.String(), `"type":"image"`)
			require.Contains(t, recorder.Body.String(), `"type":"test_complete","success":true`)
		})
	}
}
