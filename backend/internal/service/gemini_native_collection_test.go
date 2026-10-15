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

func TestGeminiNativeCollectionPreservesParts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name   string
		model  string
		chunks []string
		want   string
	}{
		{
			name: "parallel calls in separate chunks", model: "gemini-4-argon",
			chunks: []string{
				`[{"functionCall":{"name":"read_file","id":"call-a","args":{"path":"a.go"}},"thoughtSignature":"sig-a"}]`,
				`[{"functionCall":{"name":"read_file","id":"call-b","args":{"path":"b.go"}},"thoughtSignature":"sig-b"}]`,
			},
			want: `[{"functionCall":{"name":"read_file","id":"call-a","args":{"path":"a.go"}},"thoughtSignature":"sig-a"},{"functionCall":{"name":"read_file","id":"call-b","args":{"path":"b.go"}},"thoughtSignature":"sig-b"}]`,
		},
		{
			name: "identical calls remain distinct", model: "gemini-2.5-pro",
			chunks: []string{
				`[{"functionCall":{"name":"read_file","args":{"path":"a.go"}}}]`,
				`[{"functionCall":{"name":"read_file","args":{"path":"a.go"}}}]`,
			},
			want: `[{"functionCall":{"name":"read_file","args":{"path":"a.go"}}},{"functionCall":{"name":"read_file","args":{"path":"a.go"}}}]`,
		},
		{
			name: "text thoughts tools and signatures retain order", model: "gemini-4-argon",
			chunks: []string{
				`[{"text":"Plan","thought":true,"thoughtSignature":"thought-sig"},{"text":"before","thoughtSignature":"text-sig"}]`,
				`[{"text":"ha"},{"text":"ha"},{"functionCall":{"name":"read_file","args":{"path":"a.go"}},"thoughtSignature":"tool-sig-a"}]`,
				`[{"text":"between"},{"functionCall":{"name":"read_file","args":{"path":"b.go"}},"thoughtSignature":"tool-sig-b"}]`,
				`[{"text":"after"},{"text":"","thoughtSignature":"final-sig"}]`,
			},
			want: `[{"text":"Plan","thought":true,"thoughtSignature":"thought-sig"},{"text":"before","thoughtSignature":"text-sig"},{"text":"haha"},{"functionCall":{"name":"read_file","args":{"path":"a.go"}},"thoughtSignature":"tool-sig-a"},{"text":"between"},{"functionCall":{"name":"read_file","args":{"path":"b.go"}},"thoughtSignature":"tool-sig-b"},{"text":"after"},{"text":"","thoughtSignature":"final-sig"}]`,
		},
		{
			name: "images in separate chunks retain order", model: "gemini-3-pro-image",
			chunks: []string{
				`[{"text":"first"},{"inlineData":{"mimeType":"image/png","data":"YQ=="},"thoughtSignature":"image-sig-a"}]`,
				`[{"text":"second"},{"inlineData":{"mimeType":"image/png","data":"Yg=="},"thoughtSignature":"image-sig-b"}]`,
			},
			want: `[{"text":"first"},{"inlineData":{"mimeType":"image/png","data":"YQ=="},"thoughtSignature":"image-sig-a"},{"text":"second"},{"inlineData":{"mimeType":"image/png","data":"Yg=="},"thoughtSignature":"image-sig-b"}]`,
		},
	} {
		for _, ag := range []bool{false, true} {
			for _, reason := range []string{"STOP", "CONTINUATION"} {
				t.Run(fmt.Sprintf("%s/antigravity=%t/%s", tc.name, ag, reason), func(t *testing.T) {
					var payload strings.Builder
					for _, parts := range tc.chunks {
						fmt.Fprintf(&payload, "data: {\"response\":{\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":%s}}]}}\n\n", parts)
					}
					tokenField := ""
					if reason == "CONTINUATION" {
						tokenField = `,"continuationToken":"next-checkpoint"`
					}
					fmt.Fprintf(&payload, "data: {\"response\":{\"candidates\":[{\"index\":0,\"finishReason\":%q%s}],\"modelVersion\":%q}}\n\n", reason, tokenField, tc.model)
					payload.WriteString("data: " + `{"response":{"candidates":[{"index":0,"finishReason":"FINISH_REASON_UNSPECIFIED","continuationToken":""}],"usageMetadata":{"promptTokenCount":140,"cachedContentTokenCount":40,"candidatesTokenCount":20,"thoughtsTokenCount":10}}}` + "\n\n")
					calls := 0
					upstream := gemini4TestUpstream(func(req *http.Request) (*http.Response, error) {
						calls++
						require.Contains(t, req.URL.Path, "streamGenerateContent")
						return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload.String()))}, nil
					})
					body := []byte(`{"contents":[{"role":"user","parts":[{"text":"Process this request"}]}],"tools":[{"functionDeclarations":[{"name":"read_file","parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}}}}]}]}`)
					recorder := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(recorder)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/"+tc.model+":generateContent", strings.NewReader(string(body)))
					account := &Account{ID: 42, Platform: PlatformGemini, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "test", "project_id": "project"}}
					var result *ForwardResult
					var err error
					if ag {
						account.Platform = PlatformAntigravity
						svc := &AntigravityGatewayService{httpUpstream: upstream, tokenProvider: &AntigravityTokenProvider{}, settingService: &SettingService{cfg: &config.Config{}, settingRepo: continuationSettingRepo{}}}
						result, err = svc.ForwardGemini(c.Request.Context(), c, account, tc.model, "generateContent", false, body)
					} else {
						svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}, tokenProvider: &GeminiTokenProvider{}}
						result, err = svc.ForwardNative(c.Request.Context(), c, account, tc.model, "generateContent", false, body)
					}
					require.NoError(t, err)
					require.Equal(t, 1, calls, "native callers retain control of continuation")
					require.Equal(t, http.StatusOK, recorder.Code)
					require.Equal(t, ClaudeUsage{InputTokens: 100, CacheReadInputTokens: 40, OutputTokens: 30}, result.Usage)
					var response map[string]any
					require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
					parts, err := json.Marshal(extractGeminiParts(response))
					require.NoError(t, err)
					require.JSONEq(t, tc.want, string(parts))
					require.Equal(t, reason, extractGeminiFinishReason(response))
					require.Equal(t, tc.model, response["modelVersion"])
					require.Equal(t, float64(10), response["usageMetadata"].(map[string]any)["thoughtsTokenCount"])
					if reason == "CONTINUATION" {
						require.Equal(t, "next-checkpoint", response["candidates"].([]any)[0].(map[string]any)["continuationToken"])
					}
				})
			}
		}
	}
}
