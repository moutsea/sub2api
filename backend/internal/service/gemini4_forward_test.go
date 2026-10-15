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

type gemini4TestUpstream func(*http.Request) (*http.Response, error)

func (f gemini4TestUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return f(req)
}

func TestGemini4ArgonNativeForward(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, action := range []string{"generateContent", "streamGenerateContent"} {
		t.Run(action, func(t *testing.T) {
			stream := action == "streamGenerateContent"
			requestBody := `{"contents":[{"role":"user","parts":[{"text":"Hello"}]}],"generationConfig":{"maxOutputTokens":32768,"thinkingConfig":{"thinkingLevel":"low"}}}`
			responseBody := `{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello!","thoughtSignature":"test-signature"}]},"finishReason":"STOP"}],"modelVersion":"gemini-4-argon","usageMetadata":{"promptTokenCount":140,"cachedContentTokenCount":40,"candidatesTokenCount":10,"thoughtsTokenCount":20}}`
			called := false
			upstream := gemini4TestUpstream(func(req *http.Request) (*http.Response, error) {
				called = true
				require.Equal(t, http.MethodPost, req.Method)
				require.Equal(t, "/v1beta/models/gemini-4-argon:"+action, req.URL.Path)
				require.Equal(t, "test-gemini-key", req.Header.Get("x-goog-api-key"))
				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				require.JSONEq(t, requestBody, string(body))
				contentType, bodyText := "application/json", responseBody
				if stream {
					require.Equal(t, "sse", req.URL.Query().Get("alt"))
					contentType, bodyText = "text/event-stream", "data: "+responseBody+"\n\n"
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(bodyText))}, nil
			})
			svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}}
			account := &Account{ID: 42, Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-gemini-key"}}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-4-argon:"+action, strings.NewReader(requestBody))

			result, err := svc.ForwardNative(c.Request.Context(), c, account, "gemini-4-argon", action, stream, []byte(requestBody))
			require.NoError(t, err)
			require.True(t, called)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Contains(t, recorder.Body.String(), "Hello!")
			require.Contains(t, recorder.Body.String(), "test-signature")
			require.Equal(t, "gemini-4-argon", result.Model)
			require.Equal(t, 100, result.Usage.InputTokens)
			require.Equal(t, 30, result.Usage.OutputTokens)
			require.Equal(t, 40, result.Usage.CacheReadInputTokens)
		})
	}
}

func TestAccountTestServiceGemini4Argon(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := gemini4TestUpstream(func(req *http.Request) (*http.Response, error) {
		require.Equal(t, "/v1beta/models/gemini-4-argon:streamGenerateContent", req.URL.Path)
		require.Equal(t, "sse", req.URL.Query().Get("alt"))
		require.Equal(t, "test-gemini-key", req.Header.Get("x-goog-api-key"))
		var payload map[string]any
		require.NoError(t, json.NewDecoder(req.Body).Decode(&payload))
		require.NotEmpty(t, payload["contents"])
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hello!\"}]},\"finishReason\":\"STOP\"}]}\n\n")),
		}, nil
	})
	svc := &AccountTestService{httpUpstream: upstream, cfg: &config.Config{}}
	account := &Account{ID: 42, Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-gemini-key"}}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/42/test", nil)

	require.NoError(t, svc.testGeminiAccountConnection(c, account, "gemini-4-argon"))
	require.Contains(t, recorder.Body.String(), `"model":"gemini-4-argon"`)
	require.Contains(t, recorder.Body.String(), "Hello!")
	require.Contains(t, recorder.Body.String(), `"type":"test_complete","success":true`)
}
