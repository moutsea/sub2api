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

type geminiStreamReadError struct{}

func (geminiStreamReadError) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestGemini4NativeCheckpointUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, provider := range []string{"api_key", "oauth", "antigravity"} {
		for _, stream := range []bool{false, true} {
			if provider == "api_key" && !stream {
				continue // The API key non-streaming endpoint returns JSON, not SSE.
			}
			for _, ending := range []string{"STOP", "CONTINUATION", "MAX_TOKENS", "eof", "done_without_finish", "read_error", "before_checkpoint", "blocked"} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", provider, stream, ending), func(t *testing.T) {
					chunks := []string{
						`{"candidates":[{"content":{"parts":[{"text":"committed","thoughtSignature":"sig"}]},"continuationToken":"checkpoint"}],"usageMetadata":{"promptTokenCount":140,"cachedContentTokenCount":40,"candidatesTokenCount":100,"thoughtsTokenCount":32668}}`,
						`{"candidates":[{"content":{"parts":[{"text":"pending"}]}}],"usageMetadata":{"promptTokenCount":140,"cachedContentTokenCount":40,"candidatesTokenCount":200,"thoughtsTokenCount":39800}}`,
					}
					complete := ending == "STOP" || ending == "CONTINUATION" || ending == "MAX_TOKENS"
					if complete {
						chunks = append(chunks, fmt.Sprintf(`{"candidates":[{"finishReason":%q,"continuationToken":"terminal-token"}]}`, ending),
							`{"usageMetadata":{"promptTokenCount":150,"cachedContentTokenCount":40,"candidatesTokenCount":1000,"thoughtsTokenCount":40000}}`)
					}
					if ending == "before_checkpoint" {
						chunks = []string{`{"candidates":[{"content":{"parts":[{"text":"unconfirmed"}]},"finishReason":"FINISH_REASON_UNSPECIFIED"}],"usageMetadata":{"promptTokenCount":140,"candidatesTokenCount":1000}}`}
					}
					if ending == "blocked" {
						chunks = []string{`{"promptFeedback":{"blockReason":"SAFETY"},"usageMetadata":{"promptTokenCount":999}}`}
					}
					var payload strings.Builder
					for _, chunk := range chunks {
						if provider != "api_key" {
							chunk = `{"response":` + chunk + `}`
						}
						fmt.Fprintf(&payload, "data: %s\n\n", chunk)
					}
					if ending == "done_without_finish" || ending == "STOP" {
						payload.WriteString("data: [DONE]\n\n")
					}
					var reader io.Reader = strings.NewReader(payload.String())
					if ending == "read_error" {
						reader = io.MultiReader(reader, geminiStreamReadError{})
					}
					calls := 0
					upstream := gemini4TestUpstream(func(*http.Request) (*http.Response, error) {
						calls++
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "X-Gemini-Service-Tier": {"flex"}}, Body: io.NopCloser(reader)}, nil
					})
					account := &Account{ID: 42, Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test", "access_token": "test", "project_id": "project"}}
					if provider != "api_key" {
						account.Type = AccountTypeOAuth
					}
					recorder := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(recorder)
					body := `{"contents":[{"parts":[{"text":"write a long story"}]}],"generationConfig":{"maxOutputTokens":131072},"serviceTier":"flex"}`
					action := "generateContent"
					if stream {
						action = "streamGenerateContent"
					}
					c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-4-argon:"+action, strings.NewReader(body))
					var result *ForwardResult
					var err error
					if provider == "antigravity" {
						account.Platform = PlatformAntigravity
						svc := &AntigravityGatewayService{httpUpstream: upstream, tokenProvider: &AntigravityTokenProvider{}, settingService: &SettingService{cfg: &config.Config{}, settingRepo: continuationSettingRepo{}}}
						result, err = svc.ForwardGemini(c.Request.Context(), c, account, "gemini-4-argon", action, stream, []byte(body))
					} else {
						svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}, tokenProvider: &GeminiTokenProvider{}}
						result, err = svc.ForwardNative(c.Request.Context(), c, account, "gemini-4-argon", action, stream, []byte(body))
					}
					require.Equal(t, 1, calls, "native callers control continuation and retries")
					require.NotContains(t, recorder.Body.String(), geminiContinuationHopKey)
					if complete || ending == "blocked" {
						require.NoError(t, err)
						require.NotNil(t, result)
						require.False(t, result.PartialUsage)
						if complete {
							require.Equal(t, ClaudeUsage{InputTokens: 110, OutputTokens: 41000, CacheReadInputTokens: 40}, result.Usage)
							require.Contains(t, recorder.Body.String(), `"finishReason":"`+ending+`"`)
							require.Contains(t, recorder.Body.String(), "terminal-token")
							if stream && ending == "STOP" {
								require.Contains(t, recorder.Body.String(), "data: [DONE]\n\n")
							}
						} else {
							require.Equal(t, ClaudeUsage{}, result.Usage)
							require.Contains(t, recorder.Body.String(), `"blockReason":"SAFETY"`)
						}
					} else {
						require.Error(t, err)
						if ending == "before_checkpoint" {
							require.Nil(t, result, "unconfirmed generation is not billable")
						} else {
							require.NotNil(t, result)
							require.True(t, result.PartialUsage)
							require.Equal(t, ClaudeUsage{InputTokens: 100, OutputTokens: 32768, CacheReadInputTokens: 40}, result.Usage)
						}
						if stream {
							require.Equal(t, 1, strings.Count(recorder.Body.String(), "event: error\n"))
							var event map[string]any
							payload := strings.SplitAfter(recorder.Body.String(), "event: error\ndata: ")[1]
							require.NoError(t, json.Unmarshal([]byte(payload), &event))
							require.Equal(t, float64(http.StatusBadGateway), event["error"].(map[string]any)["code"])
						} else {
							require.Equal(t, http.StatusBadGateway, recorder.Code)
						}
					}
					if result != nil {
						require.Equal(t, "flex", result.ServiceTier)
					}
				})
			}
		}
	}
}

func TestGemini4SignatureRetryPreservesOutputBudget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, limit string
		stream      bool
		want        int
	}{
		{"nonstream_default", "", false, 32768},
		{"stream_default", "", true, 65536},
		{"explicit", `,"max_tokens":131072`, false, 131072},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			upstream := gemini4TestUpstream(func(req *http.Request) (*http.Response, error) {
				calls++
				var payload map[string]any
				require.NoError(t, json.NewDecoder(req.Body).Decode(&payload))
				generation, _ := payload["generationConfig"].(map[string]any)
				require.Equal(t, float64(tc.want), generation["maxOutputTokens"], "attempt %d", calls)
				if calls <= 2 {
					return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"Invalid thought_signature"}}`))}, nil
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("data: " + `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1}}` + "\n\n"))}, nil
			})
			svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}}
			account := &Account{ID: 42, Type: AccountTypeAPIKey, Platform: PlatformGemini, Credentials: map[string]any{"api_key": "test"}}
			body := fmt.Sprintf(`{"model":"gemini-4-argon","stream":%t,"messages":[{"role":"user","content":"Hello"}]%s}`, tc.stream, tc.limit)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
			_, err := svc.Forward(c.Request.Context(), c, account, []byte(body))
			require.NoError(t, err)
			require.Equal(t, 3, calls, "exercise both signature recovery stages")
		})
	}
}

func TestGemini4NativeTimeoutPreservesCheckpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			pipe, writer := io.Pipe()
			defer pipe.Close()
			defer writer.Close()
			written := make(chan error, 1)
			go func() {
				_, err := io.WriteString(writer, "data: "+`{"response":{"candidates":[{"continuationToken":"checkpoint"}],"usageMetadata":{"promptTokenCount":140,"cachedContentTokenCount":40,"candidatesTokenCount":100,"thoughtsTokenCount":32668}}}`+"\n\n"+
					"data: "+`{"response":{"usageMetadata":{"promptTokenCount":140,"candidatesTokenCount":40000}}}`+"\n\n")
				written <- err
				// Leave the upstream open without a terminal frame to trigger the idle timeout.
			}()
			cfg := &config.Config{}
			cfg.Gateway.StreamDataIntervalTimeout = 1
			upstream := gemini4TestUpstream(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: pipe}, nil
			})
			svc := &AntigravityGatewayService{httpUpstream: upstream, tokenProvider: &AntigravityTokenProvider{}, settingService: &SettingService{cfg: cfg, settingRepo: continuationSettingRepo{}}}
			account := &Account{ID: 42, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "test", "project_id": "project"}}
			body := `{"contents":[{"parts":[{"text":"Hello"}]}]}`
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-4-argon:generateContent", strings.NewReader(body))
			action := "generateContent"
			if stream {
				action = "streamGenerateContent"
			}
			result, err := svc.ForwardGemini(c.Request.Context(), c, account, "gemini-4-argon", action, stream, []byte(body))
			require.ErrorContains(t, err, "stream data interval timeout")
			require.NoError(t, <-written)
			require.NotNil(t, result)
			require.True(t, result.PartialUsage)
			require.Equal(t, ClaudeUsage{InputTokens: 100, OutputTokens: 32768, CacheReadInputTokens: 40}, result.Usage)
			_, err = writer.Write([]byte("after timeout"))
			require.ErrorIs(t, err, io.ErrClosedPipe, "returning must release the upstream read")
		})
	}
}
