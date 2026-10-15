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
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func forwardGemini4TestRequest(t *testing.T, ag, stream bool, upstream gemini4TestUpstream) (*httptest.ResponseRecorder, *ForwardResult, error) {
	t.Helper()
	body := fmt.Sprintf(`{"model":"gemini-4-argon","max_tokens":131072,"stream":%t,"messages":[{"role":"user","content":"Write a detailed explanation"}]}`, stream)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	if ag {
		svc := &AntigravityGatewayService{httpUpstream: upstream, tokenProvider: &AntigravityTokenProvider{}, settingService: &SettingService{cfg: &config.Config{}, settingRepo: continuationSettingRepo{}}}
		account := &Account{ID: 42, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "test", "project_id": "project"}}
		result, err := svc.Forward(c.Request.Context(), c, account, []byte(body))
		return recorder, result, err
	}
	svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}}
	account := &Account{ID: 42, Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test"}}
	result, err := svc.Forward(c.Request.Context(), c, account, []byte(body))
	return recorder, result, err
}

func TestGemini4UnspecifiedFinishDoesNotConfirmUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, ag := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, checkpoint := range []bool{false, true} {
				for _, ending := range []string{"eof", "done", "read_error"} {
					t.Run(fmt.Sprintf("antigravity=%t/stream=%t/checkpoint=%t/%s", ag, stream, checkpoint, ending), func(t *testing.T) {
						var payload strings.Builder
						writeChunk := func(chunk string) {
							if ag {
								chunk = `{"response":` + chunk + `}`
							}
							fmt.Fprintf(&payload, "data: %s\n\n", chunk)
						}
						unconfirmedTokens := 1000
						if checkpoint {
							writeChunk(`{"candidates":[{"finishReason":"FINISH_REASON_UNSPECIFIED","continuationToken":"checkpoint"}],"usageMetadata":{"promptTokenCount":140,"cachedContentTokenCount":40,"thoughtsTokenCount":32768}}`)
							unconfirmedTokens = 40000
						}
						writeChunk(fmt.Sprintf(`{"candidates":[{"content":{"parts":[{"text":"incomplete"}]},"finishReason":"FINISH_REASON_UNSPECIFIED"}],"usageMetadata":{"promptTokenCount":140,"cachedContentTokenCount":40,"thoughtsTokenCount":%d}}`, unconfirmedTokens))
						if ending == "done" {
							payload.WriteString("data: [DONE]\n\n")
						}
						var reader io.Reader = strings.NewReader(payload.String())
						if ending == "read_error" {
							reader = io.MultiReader(reader, geminiStreamReadError{})
						}
						calls := 0
						upstream := gemini4TestUpstream(func(*http.Request) (*http.Response, error) {
							calls++
							return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(reader)}, nil
						})
						recorder, result, err := forwardGemini4TestRequest(t, ag, stream, upstream)
						require.Error(t, err)
						require.Equal(t, 1, calls)
						if checkpoint {
							require.NotNil(t, result)
							require.True(t, result.PartialUsage)
							require.Equal(t, ClaudeUsage{InputTokens: 100, CacheReadInputTokens: 40, OutputTokens: 32768}, result.Usage)
						} else {
							require.Nil(t, result, "unconfirmed generation must not be billed")
						}
						if stream {
							require.Equal(t, 1, strings.Count(recorder.Body.String(), "event: error\n"))
							require.NotContains(t, recorder.Body.String(), "event: message_stop")
						} else {
							require.Equal(t, http.StatusBadGateway, recorder.Code)
						}
					})
				}
			}
		}
	}
}

func TestGemini4PreservesTextDeltas(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, ag := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, chunks := range [][]string{{"ha", "ha"}, {"a", "ab"}, {"a", "\n", "\n", "a"}} {
				t.Run(fmt.Sprintf("antigravity=%t/stream=%t/chunks=%q", ag, stream, chunks), func(t *testing.T) {
					upstream := gemini4TestUpstream(func(*http.Request) (*http.Response, error) {
						var payload strings.Builder
						for i, chunk := range chunks {
							candidate := map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": chunk}}}}
							if i == len(chunks)-1 {
								candidate["finishReason"] = "STOP"
							}
							response := map[string]any{"candidates": []any{candidate}, "usageMetadata": map[string]any{"promptTokenCount": 10, "candidatesTokenCount": i + 1}}
							if ag {
								response = map[string]any{"response": response}
							}
							encoded, err := json.Marshal(response)
							require.NoError(t, err)
							fmt.Fprintf(&payload, "data: %s\n\n", encoded)
						}
						return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload.String()))}, nil
					})
					recorder, result, err := forwardGemini4TestRequest(t, ag, stream, upstream)
					require.NoError(t, err)
					require.Equal(t, len(chunks), result.Usage.OutputTokens)
					var text strings.Builder
					if stream {
						for _, line := range strings.Split(recorder.Body.String(), "\n") {
							if !strings.HasPrefix(line, "data: ") {
								continue
							}
							var event map[string]any
							require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event))
							if delta, ok := event["delta"].(map[string]any); ok && delta["type"] == "text_delta" {
								text.WriteString(delta["text"].(string))
							}
						}
					} else {
						var response map[string]any
						require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
						for _, raw := range response["content"].([]any) {
							part := raw.(map[string]any)
							if part["type"] == "text" {
								text.WriteString(part["text"].(string))
							}
						}
					}
					require.Equal(t, strings.Join(chunks, ""), text.String())
				})
			}
		}
	}
}

func TestGeminiNativeOAuthPreservesTextDeltas(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, model := range []string{"gemini-4-argon", "gemini-2.5-pro"} {
		for _, finishReason := range []string{"STOP", "CONTINUATION"} {
			for _, chunks := range [][]string{{"ha", "ha"}, {"a", "ab"}, {"a", "\n", "\n", "a"}} {
				t.Run(fmt.Sprintf("%s/%s/chunks=%q", model, finishReason, chunks), func(t *testing.T) {
					calls := 0
					upstream := gemini4TestUpstream(func(req *http.Request) (*http.Response, error) {
						calls++
						require.Equal(t, "/v1internal:streamGenerateContent", req.URL.Path)
						var payload strings.Builder
						for _, chunk := range chunks {
							encoded, err := json.Marshal(chunk)
							require.NoError(t, err)
							fmt.Fprintf(&payload, "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":%s}]}}]}}\n\n", encoded)
						}
						terminal := map[string]any{"finishReason": finishReason}
						if finishReason == "CONTINUATION" {
							terminal["continuationToken"] = "checkpoint"
						}
						encoded, err := json.Marshal(map[string]any{"response": map[string]any{
							"candidates":    []any{terminal},
							"usageMetadata": map[string]any{"promptTokenCount": 10, "candidatesTokenCount": len(chunks)},
						}})
						require.NoError(t, err)
						fmt.Fprintf(&payload, "data: %s\n\n", encoded)
						return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload.String()))}, nil
					})
					svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}, tokenProvider: &GeminiTokenProvider{}}
					account := &Account{ID: 42, Platform: PlatformGemini, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "test", "project_id": "project"}}
					body := `{"contents":[{"parts":[{"text":"Repeat ha twice"}]}]}`
					recorder := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(recorder)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/"+model+":generateContent", strings.NewReader(body))
					result, err := svc.ForwardNative(c.Request.Context(), c, account, model, "generateContent", false, []byte(body))
					require.NoError(t, err)
					require.Equal(t, 1, calls, "native callers retain control of continuation")
					require.Equal(t, ClaudeUsage{InputTokens: 10, OutputTokens: len(chunks)}, result.Usage)
					var response map[string]any
					require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
					var text strings.Builder
					for _, part := range extractGeminiParts(response) {
						if value, ok := part["text"].(string); ok {
							text.WriteString(value)
						}
					}
					require.Equal(t, strings.Join(chunks, ""), text.String())
					require.Equal(t, finishReason, extractGeminiFinishReason(response))
					if finishReason == "CONTINUATION" {
						candidate := response["candidates"].([]any)[0].(map[string]any)
						require.Equal(t, "checkpoint", candidate["continuationToken"])
					}
				})
			}
		}
	}
}

func TestGemini4NativeTrailingUsagePreservesFinishReason(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, ag := range []bool{false, true} {
		for _, reason := range []string{"STOP", "MAX_TOKENS", "CONTINUATION"} {
			for _, placeholder := range []string{`""`, `"FINISH_REASON_UNSPECIFIED"`, `null`} {
				t.Run(fmt.Sprintf("antigravity=%t/%s/%s", ag, reason, placeholder), func(t *testing.T) {
					chunks := []string{
						fmt.Sprintf(`{"candidates":[{"index":0,"content":{"parts":[{"text":"answer"}]},"finishReason":%q,"continuationToken":"terminal-token"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20}}`, reason),
						fmt.Sprintf(`{"candidates":[{"index":0,"finishReason":%s,"continuationToken":""}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":25}}`, placeholder),
					}
					var payload strings.Builder
					for _, chunk := range chunks {
						fmt.Fprintf(&payload, "data: {\"response\":%s}\n\n", chunk)
					}
					calls := 0
					upstream := gemini4TestUpstream(func(*http.Request) (*http.Response, error) {
						calls++
						return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload.String()))}, nil
					})
					account := &Account{ID: 42, Platform: PlatformGemini, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "test", "project_id": "project"}}
					body := []byte(`{"contents":[{"parts":[{"text":"Hello"}]}]}`)
					recorder := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(recorder)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-4-argon:generateContent", nil)
					var result *ForwardResult
					var err error
					if ag {
						account.Platform = PlatformAntigravity
						svc := &AntigravityGatewayService{httpUpstream: upstream, tokenProvider: &AntigravityTokenProvider{}, settingService: &SettingService{cfg: &config.Config{}, settingRepo: continuationSettingRepo{}}}
						result, err = svc.ForwardGemini(c.Request.Context(), c, account, "gemini-4-argon", "generateContent", false, body)
					} else {
						svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}, tokenProvider: &GeminiTokenProvider{}}
						result, err = svc.ForwardNative(c.Request.Context(), c, account, "gemini-4-argon", "generateContent", false, body)
					}
					require.NoError(t, err)
					require.Equal(t, 1, calls, "native callers control continuation")
					require.Equal(t, ClaudeUsage{InputTokens: 100, OutputTokens: 25}, result.Usage)
					var response map[string]any
					require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
					require.Equal(t, reason, extractGeminiFinishReason(response))
					candidate := response["candidates"].([]any)[0].(map[string]any)
					require.Equal(t, "terminal-token", candidate["continuationToken"])
					require.Equal(t, "answer", extractGeminiParts(response)[0]["text"])
				})
			}
		}
	}
}

func TestGemini4PromptBlockedResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, ag := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, continued := range []bool{false, true} {
				for _, reason := range []string{"SAFETY", "BLOCKLIST", "PROHIBITED_CONTENT"} {
					t.Run(fmt.Sprintf("antigravity=%t/stream=%t/continued=%t/%s", ag, stream, continued, reason), func(t *testing.T) {
						calls := 0
						upstream := gemini4TestUpstream(func(*http.Request) (*http.Response, error) {
							calls++
							payload := fmt.Sprintf(`{"promptFeedback":{"blockReason":%q},"usageMetadata":{"promptTokenCount":999,"totalTokenCount":999}}`, reason)
							if continued && calls == 1 {
								payload = `{"candidates":[{"finishReason":"CONTINUATION","continuationToken":"checkpoint"}],"usageMetadata":{"promptTokenCount":140,"cachedContentTokenCount":40,"thoughtsTokenCount":65536}}`
							}
							if ag {
								payload = `{"response":` + payload + `}`
							}
							return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("data: " + payload + "\n\n"))}, nil
						})
						recorder, result, err := forwardGemini4TestRequest(t, ag, stream, upstream)
						var blocked *geminiPromptBlockedError
						require.ErrorAs(t, err, &blocked)
						require.Equal(t, reason, blocked.reason)
						if continued {
							require.Equal(t, 2, calls)
							require.NotNil(t, result)
							require.True(t, result.PartialUsage)
							require.Equal(t, ClaudeUsage{InputTokens: 100, CacheReadInputTokens: 40, OutputTokens: 65536}, result.Usage)
						} else {
							require.Equal(t, 1, calls)
							require.Nil(t, result, "rejected hop must not be billed")
						}
						payload := recorder.Body.String()
						if stream {
							require.Equal(t, 1, strings.Count(payload, "event: error\n"))
							require.NotContains(t, payload, "event: message_stop")
							payload = strings.SplitAfter(payload, "event: error\ndata: ")[1]
						} else {
							require.Equal(t, http.StatusBadRequest, recorder.Code)
						}
						var response map[string]any
						require.NoError(t, json.Unmarshal([]byte(payload), &response))
						require.Equal(t, "error", response["type"])
						detail := response["error"].(map[string]any)
						require.Equal(t, "invalid_request_error", detail["type"])
						require.Contains(t, detail["message"], reason)
					})
				}
			}
		}
	}
}

func TestGeminiContinuationPassesKeepalives(t *testing.T) {
	for _, heartbeat := range []string{": keepalive\n\n", "\n", "data:\n\n"} {
		t.Run(fmt.Sprintf("%q", heartbeat), func(t *testing.T) {
			reader := newGeminiContinuationBody(context.Background(), io.NopCloser(strings.NewReader(heartbeat+"data: "+`{"candidates":[{"finishReason":"STOP"}]}`+"\n\n")), 100, nil)
			defer reader.Close()
			got := make([]byte, len(heartbeat))
			_, err := io.ReadFull(reader, got)
			require.NoError(t, err)
			require.Equal(t, heartbeat, string(got))
			_, err = io.ReadAll(reader)
			require.NoError(t, err)
		})
	}
}

func TestGemini4AntigravityHeartbeatsPreventTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			pipe, writer := io.Pipe()
			done := make(chan struct{})
			defer func() { _ = pipe.Close(); <-done }()
			go func() {
				defer close(done)
				defer writer.Close()
				ticker := time.NewTicker(50 * time.Millisecond)
				defer ticker.Stop()
				for i := 0; i < 25; i++ {
					<-ticker.C
					if _, err := io.WriteString(writer, ": keepalive\n\n"); err != nil {
						return
					}
				}
				_, _ = io.WriteString(writer, "data: "+`{"response":{"candidates":[{"content":{"parts":[{"text":"done"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1}}}`+"\n\n")
			}()
			reader := newGeminiContinuationBody(context.Background(), pipe, 32768, nil)
			defer reader.Close()
			cfg := &config.Config{}
			cfg.Gateway.StreamDataIntervalTimeout = 1 // Scale down the production timeout for this regression.
			svc := &AntigravityGatewayService{settingService: &SettingService{cfg: cfg}}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			response := &http.Response{StatusCode: 200, Header: http.Header{}, Body: reader}
			var result *antigravityStreamResult
			var err error
			if stream {
				result, err = svc.handleClaudeStreamingResponse(c, response, time.Now(), "gemini-4-argon", 42)
			} else {
				result, err = svc.handleClaudeStreamToNonStreaming(c, response, time.Now(), "gemini-4-argon", 42)
			}
			require.NoError(t, err)
			require.Contains(t, recorder.Body.String(), "done")
			require.Equal(t, 1, result.usage.OutputTokens)
		})
	}
}
