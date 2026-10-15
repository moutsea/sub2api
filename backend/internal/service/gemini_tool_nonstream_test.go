package service

import (
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

func TestGeminiToolNonStreamingForwardInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, provider := range []string{"gemini-api-key", "gemini-oauth", "antigravity"} {
		for _, model := range []string{"gemini-4-argon", "gemini-2.5-pro"} {
			for _, tc := range []struct {
				name      string
				argsField string
				wantInput string
			}{
				{name: "omitted", wantInput: `{}`},
				{name: "null", argsField: `,"args":null`, wantInput: `{}`},
				{name: "empty object", argsField: `,"args":{}`, wantInput: `{}`},
				{name: "populated object", argsField: `,"args":{"timezone":"UTC","options":{"seconds":true}}`, wantInput: `{"timezone":"UTC","options":{"seconds":true}}`},
			} {
				t.Run(provider+"/"+model+"/"+tc.name, func(t *testing.T) {
					calls := 0
					upstream := gemini4TestUpstream(func(req *http.Request) (*http.Response, error) {
						calls++
						payload := `{"candidates":[{"content":{"parts":[{"text":"Checking the time."},{"functionCall":{"name":"get_clock"` + tc.argsField + `}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}`
						if provider != "gemini-api-key" {
							payload = `{"response":` + payload + `}`
						}
						contentType := "application/json"
						if provider != "gemini-api-key" || model == "gemini-4-argon" {
							require.Contains(t, req.URL.Path, "streamGenerateContent")
							contentType = "text/event-stream"
							payload = "data: " + payload + "\n\n"
						} else {
							require.Contains(t, req.URL.Path, ":generateContent")
						}
						return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(payload))}, nil
					})
					account := &Account{ID: 42, Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test"}}
					if provider != "gemini-api-key" {
						account.Type = AccountTypeOAuth
						account.Credentials = map[string]any{"access_token": "test", "project_id": "project"}
					}
					body := fmt.Sprintf(`{"model":%q,"stream":false,"max_tokens":1024,"messages":[{"role":"user","content":"Get the time"}],"tools":[{"name":"get_clock","input_schema":{"type":"object","properties":{"timezone":{"type":"string"},"options":{"type":"object","properties":{"seconds":{"type":"boolean"}}}}}}]}`, model)
					recorder := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(recorder)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
					var result *ForwardResult
					var err error
					if provider == "antigravity" {
						account.Platform = PlatformAntigravity
						svc := &AntigravityGatewayService{httpUpstream: upstream, tokenProvider: &AntigravityTokenProvider{}, settingService: &SettingService{cfg: &config.Config{}, settingRepo: continuationSettingRepo{}}}
						result, err = svc.Forward(c.Request.Context(), c, account, []byte(body))
					} else {
						svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}, tokenProvider: &GeminiTokenProvider{}}
						result, err = svc.Forward(c.Request.Context(), c, account, []byte(body))
					}
					require.NoError(t, err)
					require.Equal(t, 1, calls)
					require.Equal(t, http.StatusOK, recorder.Code)
					require.NotNil(t, result)
					require.Equal(t, ClaudeUsage{InputTokens: 10, OutputTokens: 5}, result.Usage)

					// Check the serialized response so an omitted input field also fails.
					var response struct {
						Content []struct {
							Type  string          `json:"type"`
							ID    string          `json:"id"`
							Name  string          `json:"name"`
							Text  string          `json:"text"`
							Input json.RawMessage `json:"input"`
						} `json:"content"`
						StopReason string `json:"stop_reason"`
					}
					require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
					require.Equal(t, "tool_use", response.StopReason)
					require.Len(t, response.Content, 2)
					require.Equal(t, "text", response.Content[0].Type)
					require.Equal(t, "Checking the time.", response.Content[0].Text)
					require.Empty(t, response.Content[0].Input)
					tool := response.Content[1]
					require.Equal(t, "tool_use", tool.Type)
					require.NotEmpty(t, tool.ID)
					require.Equal(t, "get_clock", tool.Name)
					require.JSONEq(t, tc.wantInput, string(tool.Input))
				})
			}
		}
	}
}
