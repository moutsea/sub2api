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

func TestCollectGeminiSSEPreservesTerminalMetadata(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		for _, done := range []string{"", "data: [DONE]\n\n"} {
			chunks := []string{
				`{"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"partial","thoughtSignature":"keep-signature"},{"functionCall":{"name":"tool","args":{}}}]}}]}`,
				`{"candidates":[{"index":0,"content":{"parts":[]},"finishReason":"CONTINUATION","continuationToken":"checkpoint"}],"modelVersion":"gemini-4-argon"}`,
				`{"usageMetadata":{"promptTokenCount":140,"cachedContentTokenCount":40,"candidatesTokenCount":10,"thoughtsTokenCount":20}}`,
			}
			var stream strings.Builder
			for _, chunk := range chunks {
				if wrapped {
					chunk = `{"response":` + chunk + `}`
				}
				fmt.Fprintf(&stream, "data: %s\n\n", chunk)
			}
			stream.WriteString(done)
			result, usage, err := collectGeminiSSE(strings.NewReader(stream.String()), wrapped, geminiTextDeltas)
			require.NoError(t, err)
			require.Equal(t, "CONTINUATION", extractGeminiFinishReason(result))
			candidate := result["candidates"].([]any)[0].(map[string]any)
			require.Equal(t, "checkpoint", candidate["continuationToken"])
			parts := extractGeminiParts(result)
			require.Equal(t, "partial", parts[0]["text"])
			require.Equal(t, "keep-signature", parts[0]["thoughtSignature"])
			require.NotNil(t, parts[1]["functionCall"])
			require.NotNil(t, result["usageMetadata"])
			require.Equal(t, 30, usage.OutputTokens)
		}
	}
}

func TestCollectGeminiSSEClearsObsoleteCheckpoint(t *testing.T) {
	for _, tokenField := range []string{"", `,"continuationToken":""`, `,"continuationToken":null`} {
		t.Run(tokenField, func(t *testing.T) {
			payload := "data: " + `{"candidates":[{"content":{"parts":[{"text":"answer"}]},"continuationToken":"checkpoint"}]}` + "\n\n" +
				fmt.Sprintf("data: {\"candidates\":[{\"finishReason\":\"STOP\"%s}]}\n\n", tokenField)
			response, _, err := collectGeminiSSE(strings.NewReader(payload), false, geminiTextDeltas)
			require.NoError(t, err)
			require.Equal(t, "STOP", extractGeminiFinishReason(response))
			candidate := response["candidates"].([]any)[0].(map[string]any)
			require.NotContains(t, candidate, "continuationToken")
		})
	}
}

func TestGemini4CompatContinuesWithinBudget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			calls := 0
			var firstPayload map[string]any
			upstream := gemini4TestUpstream(func(req *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, "/v1beta/models/gemini-4-argon:streamGenerateContent", req.URL.Path)
				require.Equal(t, "sse", req.URL.Query().Get("alt"))
				require.Equal(t, "test-key", req.Header.Get("x-goog-api-key"))
				var payload map[string]any
				require.NoError(t, json.NewDecoder(req.Body).Decode(&payload))
				require.Equal(t, float64(131072), payload["generationConfig"].(map[string]any)["maxOutputTokens"])
				var body string
				if calls == 1 {
					firstPayload = payload
					body = "data: " + `{"usageMetadata":{"promptTokenCount":140,"thoughtsTokenCount":10}}` + "\n\n" +
						"data: " + `{"candidates":[{"content":{"role":"model","parts":[{"text":"same"}]},"finishReason":"CONTINUATION","continuationToken":"checkpoint-1"}],"usageMetadata":{"promptTokenCount":140,"cachedContentTokenCount":40,"candidatesTokenCount":1,"thoughtsTokenCount":20}}` + "\n\n"
				} else {
					require.Equal(t, 2, calls)
					require.Equal(t, "checkpoint-1", payload["continuationToken"])
					delete(payload, "continuationToken")
					require.Equal(t, firstPayload, payload, "original contents/config must stay unchanged")
					body = "data: " + `{"candidates":[{"content":{"role":"model","parts":[{"text":"same"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":161,"cachedContentTokenCount":60,"candidatesTokenCount":1,"thoughtsTokenCount":30}}` + "\n\n"
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}}
			account := &Account{ID: 42, Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-key"}}
			body := fmt.Sprintf(`{"model":"gemini-4-argon","max_tokens":131072,"messages":[{"role":"user","content":"hello"}],"stream":%t}`, stream)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
			result, err := svc.Forward(c.Request.Context(), c, account, []byte(body))
			require.NoError(t, err)
			require.Equal(t, 2, calls)
			require.Equal(t, 52, result.Usage.OutputTokens)
			require.Equal(t, 201, result.Usage.InputTokens)
			require.Equal(t, 100, result.Usage.CacheReadInputTokens)
			require.Equal(t, 2, strings.Count(recorder.Body.String(), "same"))
			require.Equal(t, 1, strings.Count(recorder.Body.String(), `"stop_reason":"end_turn"`))
			require.NotContains(t, recorder.Body.String(), "checkpoint-1")
			if stream {
				require.Equal(t, 1, strings.Count(recorder.Body.String(), "event: message_start"))
				require.Equal(t, 1, strings.Count(recorder.Body.String(), "event: message_stop"))
			}
		})
	}
}

func TestGeminiContinuationBoundaries(t *testing.T) {
	checkpoint := `{"candidates":[{"finishReason":"CONTINUATION","continuationToken":"token"}],"usageMetadata":{"thoughtsTokenCount":30}}`
	for _, tc := range []struct {
		name, body            string
		budget, status, calls int
		wantErr               bool
	}{
		{"budget", checkpoint, 30, 200, 0, false},
		{"missing_token", strings.ReplaceAll(checkpoint, `,"continuationToken":"token"`, ""), 100, 200, 0, true},
		{"repeated_token", checkpoint, 100, 200, 1, true},
		{"upstream_error", checkpoint, 100, 503, 1, true},
		{"missing_finish", `{"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}`, 100, 200, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			reader := newGeminiContinuationBody(context.Background(), io.NopCloser(strings.NewReader("data: "+tc.body+"\n\n")), tc.budget, func(context.Context, string) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader("data: " + tc.body + "\n\n"))}, nil
			})
			defer reader.Close()
			body, err := io.ReadAll(reader)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Contains(t, string(body), "MAX_TOKENS")
			}
			require.Equal(t, tc.calls, calls)
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	reader := newGeminiContinuationBody(ctx, io.NopCloser(strings.NewReader("data: "+checkpoint+"\n\n")), 100, nil)
	defer reader.Close()
	cancel()
	_, err := io.ReadAll(reader)
	require.ErrorIs(t, err, context.Canceled)
}

func TestGeminiContinuationTrailingUsageAtBudget(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		t.Run(fmt.Sprint(wrapped), func(t *testing.T) {
			var stream strings.Builder
			for _, chunk := range []string{
				`{"candidates":[{"finishReason":"CONTINUATION","continuationToken":"checkpoint"}]}`,
				`{"usageMetadata":{"candidatesTokenCount":10,"thoughtsTokenCount":20}}`,
			} {
				if wrapped {
					chunk = `{"response":` + chunk + `}`
				}
				fmt.Fprintf(&stream, "data: %s\n\n", chunk)
			}
			reader := newGeminiContinuationBody(context.Background(), io.NopCloser(strings.NewReader(stream.String())), 30, func(context.Context, string) (*http.Response, error) {
				t.Error("continuation must stop at the requested token budget")
				return nil, fmt.Errorf("unexpected continuation")
			})
			defer reader.Close()
			result, usage, err := collectGeminiSSE(reader, wrapped, geminiTextDeltas)
			require.NoError(t, err)
			require.Equal(t, "MAX_TOKENS", extractGeminiFinishReason(result))
			require.Equal(t, 30, usage.OutputTokens)
		})
	}
}

func TestGeminiContinuationUnspecifiedPreservesTerminalReason(t *testing.T) {
	for _, reason := range []string{"STOP", "MAX_TOKENS", "CONTINUATION"} {
		t.Run(reason, func(t *testing.T) {
			chunks := []string{
				`{"candidates":[{"finishReason":"FINISH_REASON_UNSPECIFIED"}],"usageMetadata":{"thoughtsTokenCount":10}}`,
				fmt.Sprintf(`{"candidates":[{"finishReason":%q,"continuationToken":"checkpoint"}],"usageMetadata":{"thoughtsTokenCount":20}}`, reason),
				`{"candidates":[{"finishReason":"FINISH_REASON_UNSPECIFIED"}],"usageMetadata":{"thoughtsTokenCount":25}}`,
			}
			calls := 0
			reader := newGeminiContinuationBody(context.Background(), io.NopCloser(strings.NewReader("data: "+strings.Join(chunks, "\n\ndata: ")+"\n\n")), 100,
				func(_ context.Context, token string) (*http.Response, error) {
					calls++
					require.Equal(t, "checkpoint", token)
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("data: " + `{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"thoughtsTokenCount":30}}` + "\n\n"))}, nil
				})
			defer reader.Close()
			response, usage, err := collectGeminiSSE(reader, false, geminiTextDeltas)
			require.NoError(t, err)
			if reason == "CONTINUATION" {
				require.Equal(t, 1, calls)
				require.Equal(t, "STOP", extractGeminiFinishReason(response))
				require.Equal(t, 55, usage.OutputTokens)
			} else {
				require.Zero(t, calls)
				require.Equal(t, reason, extractGeminiFinishReason(response))
				require.Equal(t, 25, usage.OutputTokens)
			}
		})
	}
}

func TestReplayGeminiContinuationPreservesWrappedRequest(t *testing.T) {
	original := `{"model":"gemini-4-argon","project":"project-1","request":{"contents":[{"parts":[{"text":"hello"}]}],"generationConfig":{"maxOutputTokens":131072}}}`
	req, err := http.NewRequest(http.MethodPost, "https://example.com/v1internal:streamGenerateContent?alt=sse", strings.NewReader(original))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer test-key")
	replayed, err := replayGeminiContinuationRequest(context.Background(), req, "checkpoint")
	require.NoError(t, err)
	require.Equal(t, req.URL.String(), replayed.URL.String())
	require.Equal(t, req.Header, replayed.Header)
	var payload map[string]any
	require.NoError(t, json.NewDecoder(replayed.Body).Decode(&payload))
	inner := payload["request"].(map[string]any)
	require.Equal(t, "checkpoint", inner["continuationToken"])
	delete(inner, "continuationToken")
	got, err := json.Marshal(payload)
	require.NoError(t, err)
	require.JSONEq(t, original, string(got))
}

type continuationSettingRepo struct{ SettingRepository }

func (continuationSettingRepo) GetValue(context.Context, string) (string, error) {
	return "", ErrSettingNotFound
}

func TestGemini4AntigravityContinuation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			calls := 0
			upstream := gemini4TestUpstream(func(req *http.Request) (*http.Response, error) {
				calls++
				var payload map[string]any
				require.NoError(t, json.NewDecoder(req.Body).Decode(&payload))
				require.Equal(t, "gemini-4-argon", payload["model"])
				inner := payload["request"].(map[string]any)
				require.Equal(t, float64(131072), inner["generationConfig"].(map[string]any)["maxOutputTokens"])
				require.Equal(t, "Bearer test-token", req.Header.Get("Authorization"))
				finish, token := "CONTINUATION", `,"continuationToken":"checkpoint"`
				if calls == 2 {
					require.Equal(t, "checkpoint", inner["continuationToken"])
					finish, token = "STOP", ""
				}
				require.LessOrEqual(t, calls, 2)
				chunk := fmt.Sprintf(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"answer"}]},"finishReason":"%s"%s}]}}`, finish, token)
				usage := `{"response":{"usageMetadata":{"promptTokenCount":140,"cachedContentTokenCount":40,"candidatesTokenCount":10,"thoughtsTokenCount":20}}}`
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("data: " + chunk + "\n\ndata: " + usage + "\n\ndata: [DONE]\n\n"))}, nil
			})
			svc := &AntigravityGatewayService{
				httpUpstream: upstream, tokenProvider: &AntigravityTokenProvider{},
				settingService: &SettingService{cfg: &config.Config{}, settingRepo: continuationSettingRepo{}},
			}
			account := &Account{ID: 42, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "test-token", "project_id": "project"}}
			body := fmt.Sprintf(`{"model":"gemini-4-argon","max_tokens":131072,"messages":[{"role":"user","content":"Explain merge sort in detail"}],"stream":%t}`, stream)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
			result, err := svc.Forward(c.Request.Context(), c, account, []byte(body))
			require.NoError(t, err)
			require.Equal(t, 2, calls)
			require.Equal(t, 60, result.Usage.OutputTokens)
			require.Equal(t, 200, result.Usage.InputTokens)
			require.Equal(t, 80, result.Usage.CacheReadInputTokens)
			require.Equal(t, 1, strings.Count(recorder.Body.String(), `"stop_reason":"end_turn"`))
			require.Contains(t, recorder.Body.String(), `"output_tokens":60`)
			if stream {
				require.Equal(t, 1, strings.Count(recorder.Body.String(), "event: message_stop"))
			}
		})
	}
}

func TestGeminiContinuationCloseUnblocksRead(t *testing.T) {
	pipe, writer := io.Pipe()
	defer writer.Close()
	reader := newGeminiContinuationBody(context.Background(), pipe, 100, nil)
	finished := make(chan error, 1)
	go func() { _, err := io.ReadAll(reader); finished <- err }()
	require.NoError(t, reader.Close())
	select {
	case err := <-finished:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("Close did not unblock continuation reader")
	}
}

func TestGemini4NativePreservesContinuationForCaller(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		calls := 0
		responseBody := `{"candidates":[{"finishReason":"CONTINUATION","continuationToken":"next-token"}],"usageMetadata":{"thoughtsTokenCount":30}}`
		requestBody := `{"contents":[{"parts":[{"text":"hello"}]}],"continuationToken":"previous-token","generationConfig":{"maxOutputTokens":131072}}`
		upstream := gemini4TestUpstream(func(req *http.Request) (*http.Response, error) {
			calls++
			payload, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.JSONEq(t, requestBody, string(payload))
			body := responseBody
			if stream {
				body = "data: " + body + "\n\n"
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}}
		account := &Account{ID: 42, Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-key"}}
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-4-argon:generateContent", nil)
		action := "generateContent"
		if stream {
			action = "streamGenerateContent"
		}
		_, err := svc.ForwardNative(c.Request.Context(), c, account, "gemini-4-argon", action, stream, []byte(requestBody))
		require.NoError(t, err)
		require.Equal(t, 1, calls)
		require.Contains(t, recorder.Body.String(), `"continuationToken":"next-token"`)
		require.Contains(t, recorder.Body.String(), `"finishReason":"CONTINUATION"`)
	}
}

func TestGemini4NonstreamKeepsAllToolCalls(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := gemini4TestUpstream(func(req *http.Request) (*http.Response, error) {
		chunks := []string{
			`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"first_tool","args":{"a":1}},"thoughtSignature":"sig-a"}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"second_tool","args":{"b":2}},"thoughtSignature":"sig-b"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20}}`,
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("data: " + strings.Join(chunks, "\n\ndata: ") + "\n\n"))}, nil
	})
	svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}}
	account := &Account{ID: 42, Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test"}}
	body := `{"model":"gemini-4-argon","max_tokens":131072,"stream":false,"messages":[{"role":"user","content":"Call both tools"}],"tools":[{"name":"first_tool","input_schema":{"type":"object"}},{"name":"second_tool","input_schema":{"type":"object"}}]}`
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	_, err := svc.Forward(c.Request.Context(), c, account, []byte(body))
	require.NoError(t, err)
	require.Contains(t, recorder.Body.String(), `"name":"first_tool"`)
	require.Contains(t, recorder.Body.String(), `"name":"second_tool"`)
}

func TestGemini4FailedContinuation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, ag := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("antigravity=%t/stream=%t", ag, stream), func(t *testing.T) {
				calls := 0
				upstream := gemini4TestUpstream(func(req *http.Request) (*http.Response, error) {
					calls++
					status := 200
					chunk := `{"candidates":[{"content":{"role":"model","parts":[{"text":"partial answer"}]},"finishReason":"CONTINUATION","continuationToken":"checkpoint"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":1,"thoughtsTokenCount":65535}}`
					if ag {
						chunk = `{"response":` + chunk + `}`
					}
					payload := "data: " + chunk + "\n\n"
					if calls == 2 {
						status = 503
						payload = `{"error":{"message":"temporarily unavailable"}}`
					}
					return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload))}, nil
				})
				body := fmt.Sprintf(`{"model":"gemini-4-argon","max_tokens":131072,"stream":%t,"messages":[{"role":"user","content":"Write a long detailed explanation"}]}`, stream)
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
				var result *ForwardResult
				var err error
				if ag {
					svc := &AntigravityGatewayService{httpUpstream: upstream, tokenProvider: &AntigravityTokenProvider{}, settingService: &SettingService{cfg: &config.Config{}, settingRepo: continuationSettingRepo{}}}
					account := &Account{ID: 42, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "test", "project_id": "project"}}
					result, err = svc.Forward(c.Request.Context(), c, account, []byte(body))
				} else {
					svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}}
					account := &Account{ID: 42, Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test"}}
					result, err = svc.Forward(c.Request.Context(), c, account, []byte(body))
				}
				require.Error(t, err)
				require.Equal(t, 2, calls)
				require.NotNil(t, result)
				require.True(t, result.PartialUsage)
				require.Equal(t, 65536, result.Usage.OutputTokens)
				require.Equal(t, 100, result.Usage.InputTokens)
				if !stream {
					require.Equal(t, http.StatusBadGateway, recorder.Code)
					var payload map[string]any
					require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
					require.Equal(t, "error", payload["type"])
					require.NotEmpty(t, payload["error"].(map[string]any)["message"])
				}
				if stream {
					require.Equal(t, 1, strings.Count(recorder.Body.String(), "event: error"))
					require.NotContains(t, recorder.Body.String(), "event: message_stop")
					payload := strings.SplitAfter(recorder.Body.String(), "event: error\ndata: ")[1]
					var errorEvent map[string]any
					require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(payload)), &errorEvent))
					require.Equal(t, "error", errorEvent["type"])
					require.Equal(t, "api_error", errorEvent["error"].(map[string]any)["type"])
				}
			})
		}
	}
}

func TestGeminiContinuationOnlyBillsConfirmedUsage(t *testing.T) {
	for _, checkpoint := range []bool{false, true} {
		t.Run(fmt.Sprint(checkpoint), func(t *testing.T) {
			first := `{"candidates":[{"finishReason":"CONTINUATION","continuationToken":"first"}],"usageMetadata":{"promptTokenCount":140,"cachedContentTokenCount":40,"candidatesTokenCount":1,"thoughtsTokenCount":30}}`
			second := `{"usageMetadata":{"promptTokenCount":999,"cachedContentTokenCount":99,"candidatesTokenCount":10,"thoughtsTokenCount":40}}`
			if checkpoint {
				second = `{"candidates":[{"continuationToken":"second"}],"usageMetadata":{"promptTokenCount":999,"cachedContentTokenCount":99,"candidatesTokenCount":10,"thoughtsTokenCount":40}}`
			}
			reader := newGeminiContinuationBody(context.Background(), io.NopCloser(strings.NewReader("data: "+first+"\n\n")), 1000,
				func(context.Context, string) (*http.Response, error) {
					// EOF without a terminal reason is a failed, incomplete hop.
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("data: " + second + "\n\n"))}, nil
				})
			defer reader.Close()
			_, err := io.ReadAll(reader)
			require.Error(t, err)
			result := geminiPartialForwardResult(reader, "request", "gemini-4-argon", true, time.Now())
			require.NotNil(t, result)
			require.True(t, result.PartialUsage)
			if checkpoint {
				require.Equal(t, ClaudeUsage{InputTokens: 1000, OutputTokens: 81, CacheReadInputTokens: 139}, result.Usage)
			} else {
				require.Equal(t, ClaudeUsage{InputTokens: 100, OutputTokens: 31, CacheReadInputTokens: 40}, result.Usage)
			}
		})
	}
	for _, chunk := range []string{`null`, `{"usageMetadata":{"promptTokenCount":100,"thoughtsTokenCount":20}}`} {
		reader := newGeminiContinuationBody(context.Background(), io.NopCloser(strings.NewReader("data: "+chunk+"\n\n")), 100, nil)
		_, err := io.ReadAll(reader)
		require.Error(t, err)
		require.Nil(t, geminiPartialForwardResult(reader, "request", "gemini-4-argon", false, time.Now()))
		require.NoError(t, reader.Close())
	}
}

func TestCollectGeminiSSEPreservesPartOrderAndSignatures(t *testing.T) {
	chunks := []string{
		`{"candidates":[{"content":{"parts":[{"text":"before","thoughtSignature":"text-signature"},{"functionCall":{"name":"lookup","args":{"id":1}},"thoughtSignature":"tool-signature-1"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"between"},{"functionCall":{"name":"lookup","args":{"id":2}},"thoughtSignature":"tool-signature-2"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"after"}]},"finishReason":"STOP"}]}`,
	}
	result, _, err := collectGeminiSSE(strings.NewReader("data: "+strings.Join(chunks, "\n\ndata: ")+"\n\n"), false, geminiTextDeltas)
	require.NoError(t, err)
	parts := extractGeminiParts(result)
	require.Len(t, parts, 5)
	require.Equal(t, "before", parts[0]["text"])
	require.Equal(t, "text-signature", parts[0]["thoughtSignature"])
	require.Equal(t, "tool-signature-1", parts[1]["thoughtSignature"])
	require.Equal(t, "between", parts[2]["text"])
	require.Equal(t, "tool-signature-2", parts[3]["thoughtSignature"])
	require.Equal(t, float64(2), parts[3]["functionCall"].(map[string]any)["args"].(map[string]any)["id"])
	require.Equal(t, "after", parts[4]["text"])
	require.Equal(t, "STOP", extractGeminiFinishReason(result))
}
