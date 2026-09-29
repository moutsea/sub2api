package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIEnvironmentContextSingaporeDate(t *testing.T) {
	for _, testCase := range []struct {
		name string
		now  time.Time
		date string
	}{
		{"before midnight", time.Date(2026, 9, 29, 15, 59, 59, 0, time.UTC), "2026-09-29"},
		{"at midnight", time.Date(2026, 9, 29, 16, 0, 0, 0, time.UTC), "2026-09-30"},
		{"new year", time.Date(2026, 12, 31, 16, 0, 0, 0, time.UTC), "2027-01-01"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			body, err := applyOpenAIEnvironmentContext([]byte(`{"instructions":"Be concise","input":"hello"}`), "input", testCase.now)
			require.NoError(t, err)
			instructions := gjson.GetBytes(body, "instructions").String()
			require.Equal(t, "Be concise\n\n<environment_context>\n<timezone>Asia/Singapore</timezone>\n<current_date>"+testCase.date+"</current_date>\n</environment_context>", instructions)
		})
	}
}

func TestOpenAIEnvironmentContextPreservesRequest(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		messageField string
		body         string
		contextPath  string
	}{
		{
			name: "instructions retain environment fields", messageField: "input",
			body:        `{"instructions":"Before <environment_context>\n<cwd>/workspace</cwd>\n<timezone>UTC</timezone>\n<current_date>2000-01-01</current_date>\n</environment_context> After","input":"hello"}`,
			contextPath: "instructions",
		},
		{
			name: "developer message text", messageField: "input",
			body:        `{"input":[{"role":"developer","content":"<environment_context><cwd>/workspace</cwd></environment_context>"}]}`,
			contextPath: "input.0.content",
		},
		{
			name: "system input text array", messageField: "input",
			body:        `{"input":[{"role":"system","content":[{"type":"input_text","text":"<environment_context><timezone>UTC</timezone></environment_context>"},{"type":"input_image","image_url":"https://example.com/image.png"}]}]}`,
			contextPath: "input.0.content.0.text",
		},
		{
			name: "chat system text array", messageField: "messages",
			body:        `{"messages":[{"role":"system","content":[{"type":"text","text":"<environment_context><current_date>2000-01-01</current_date></environment_context>"}]}]}`,
			contextPath: "messages.0.content.0.text",
		},
		{
			name: "chat missing environment", messageField: "messages",
			body:        `{"messages":[{"role":"system","content":"Be concise"},{"role":"user","content":"hello"}]}`,
			contextPath: "messages.0.content",
		},
		{
			name: "responses string input", messageField: "input",
			body: `{"input":"hello"}`, contextPath: "instructions",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			now := time.Date(2026, 9, 29, 16, 0, 0, 0, time.UTC)
			body, err := applyOpenAIEnvironmentContext([]byte(testCase.body), testCase.messageField, now)
			require.NoError(t, err)
			text := gjson.GetBytes(body, testCase.contextPath).String()
			require.Contains(t, text, "<timezone>Asia/Singapore</timezone>")
			require.Contains(t, text, "<current_date>2026-09-30</current_date>")
			require.Equal(t, 1, strings.Count(text, "<environment_context>"))
			if strings.Contains(testCase.body, "<cwd>") {
				require.Contains(t, text, "<cwd>/workspace</cwd>")
			}
			if testCase.name == "instructions retain environment fields" {
				require.True(t, strings.HasPrefix(text, "Before "))
				require.True(t, strings.HasSuffix(text, " After"))
			}
			if testCase.name == "chat missing environment" {
				require.Equal(t, "Be concise", gjson.GetBytes(body, "messages.1.content").String())
				require.Equal(t, "hello", gjson.GetBytes(body, "messages.2.content").String())
			}
			if testCase.name == "system input text array" {
				require.Equal(t, gjson.Get(testCase.body, "input.0.content.1").Raw, gjson.GetBytes(body, "input.0.content.1").Raw)
			}
			again, err := applyOpenAIEnvironmentContext(body, testCase.messageField, now)
			require.NoError(t, err)
			require.Equal(t, string(body), string(again))
		})
	}
}

func TestOpenAIEnvironmentContextDoesNotRewriteUserOrToolContent(t *testing.T) {
	body := []byte(`{"input":[{"role":"user","content":"<environment_context><timezone>UTC</timezone><current_date>2000-01-01</current_date></environment_context>"},{"type":"function_call_output","call_id":"call_1","output":"<timezone>UTC</timezone>"}],"tools":[{"type":"function","name":"clock","description":"<current_date>2000-01-01</current_date>"}],"metadata":{"number":9007199254740993}}`)
	updated, err := applyOpenAIEnvironmentContext(body, "input", time.Now())
	require.NoError(t, err)
	for _, path := range []string{"input", "tools", "metadata"} {
		require.Equal(t, gjson.GetBytes(body, path).Raw, gjson.GetBytes(updated, path).Raw)
	}
	require.Contains(t, gjson.GetBytes(updated, "instructions").String(), "<timezone>Asia/Singapore</timezone>")
}

func TestOpenAIPrepareRequestsApplyEnvironmentContext(t *testing.T) {
	service := &OpenAIGatewayService{}
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeAPIKey} {
		for _, platform := range []string{PlatformOpenAI, PlatformGrok} {
			for _, messageField := range []string{"input", "messages"} {
				t.Run(accountType+"/"+platform+"/"+messageField, func(t *testing.T) {
					account := &Account{Platform: platform, Type: accountType}
					body := []byte(`{"model":"test","` + messageField + `":[{"role":"user","content":"hello"}]}`)
					before := time.Now().In(openAIEnvironmentLocation).Format("2006-01-02")
					preparedBody, err := prepareOpenAIEnvironmentContext(account, body, messageField)
					require.NoError(t, err)
					var request *http.Request
					if messageField == "input" {
						request, err = service.buildUpstreamRequest(context.Background(), nil, account, preparedBody, "token", false, "", true)
					} else {
						request, err = service.buildChatCompletionsRequest(context.Background(), nil, account, preparedBody, "token")
					}
					require.NoError(t, err)
					defer request.Body.Close()
					upstreamBody, err := io.ReadAll(request.Body)
					require.NoError(t, err)
					if platform != PlatformOpenAI {
						require.Equal(t, string(body), string(upstreamBody))
						return
					}
					path := "instructions"
					if messageField == "messages" {
						path = "messages.0.content"
					}
					text := gjson.GetBytes(upstreamBody, path).String()
					require.Contains(t, text, "<timezone>Asia/Singapore</timezone>")
					after := time.Now().In(openAIEnvironmentLocation).Format("2006-01-02")
					require.True(t, strings.Contains(text, "<current_date>"+before+"</current_date>") || strings.Contains(text, "<current_date>"+after+"</current_date>"))
				})
			}
		}
	}
}

func TestOpenAIEnvironmentContextKeepsInjectedInstructionsHidden(t *testing.T) {
	response := map[string]any{"instructions": normalizeOpenAIEnvironmentText("Built-in instructions", "2026-09-29")}
	require.True(t, stripInjectedCodexInstructionsIfInjected(response, "Built-in instructions"))
	require.NotContains(t, response, "instructions")
	response["instructions"] = normalizeOpenAIEnvironmentText("Client instructions", "2026-09-29")
	require.False(t, stripInjectedCodexInstructionsIfInjected(response, "Built-in instructions"))
	require.Contains(t, response, "instructions")
}

func TestOpenAIEnvironmentContextInvalidInstructionsReturnsBadRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		for _, instructions := range []string{`123`, `true`, `[]`, `{}`} {
			for _, input := range []string{`"hello"`, `[{"role":"developer","content":"<environment_context><timezone>UTC</timezone></environment_context>"}]`} {
				t.Run(accountType+"/"+instructions+"/"+input, func(t *testing.T) {
					body := []byte(`{"model":"gpt-5.5","instructions":` + instructions + `,"input":` + input + `,"stream":true}`)
					recorder := httptest.NewRecorder()
					ginContext, _ := gin.CreateTestContext(recorder)
					ginContext.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
					ginContext.Request.Header.Set("User-Agent", "codex_cli_rs/0.98.0")
					upstream := &recordingOpenAIHTTPUpstream{}
					service := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
					account := &Account{Platform: PlatformOpenAI, Type: accountType}

					result, err := service.Forward(context.Background(), ginContext, account, body)

					require.EqualError(t, err, "OpenAI instructions must be a string")
					require.Nil(t, result)
					require.Zero(t, upstream.calls)
					require.True(t, ginContext.Writer.Written())
					require.Equal(t, http.StatusBadRequest, recorder.Code)
					require.True(t, json.Valid(recorder.Body.Bytes()))
					require.Equal(t, "invalid_request_error", gjson.GetBytes(recorder.Body.Bytes(), "error.type").String())
					require.Equal(t, "instructions", gjson.GetBytes(recorder.Body.Bytes(), "error.param").String())
				})
			}
		}
	}
}

func TestOpenAIEnvironmentContextInvalidInstructionsAfterHeartbeat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		t.Run(accountType, func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.5","instructions":123,"input":"hello","stream":true}`)
			recorder := httptest.NewRecorder()
			ginContext, _ := gin.CreateTestContext(recorder)
			ginContext.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
			ginContext.Request.Header.Set("User-Agent", "codex_cli_rs/0.98.0")
			ginContext.Header("Content-Type", "text/event-stream")
			_, err := ginContext.Writer.WriteString(":\n\n")
			require.NoError(t, err)
			ginContext.Writer.Flush()
			upstream := &recordingOpenAIHTTPUpstream{}
			service := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{Platform: PlatformOpenAI, Type: accountType}

			result, err := service.Forward(context.Background(), ginContext, account, body)

			require.EqualError(t, err, "OpenAI instructions must be a string")
			require.Nil(t, result)
			require.Zero(t, upstream.calls)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, "text/event-stream", recorder.Result().Header.Get("Content-Type"))
			frames := strings.Split(recorder.Body.String(), "\n\n")
			require.Len(t, frames, 3)
			require.Equal(t, ":", frames[0])
			require.Empty(t, frames[2])
			lines := strings.Split(frames[1], "\n")
			require.Len(t, lines, 2)
			require.Equal(t, "event:error", lines[0])
			require.True(t, strings.HasPrefix(lines[1], "data:"))
			require.JSONEq(t, `{"error":{"code":null,"type":"invalid_request_error","message":"OpenAI instructions must be a string","param":"instructions"}}`, strings.TrimPrefix(lines[1], "data:"))
		})
	}
}

func TestWriteOpenAIInvalidRequestEscapesSSEMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Header("Content-Type", "text/event-stream")
	ginContext.Writer.Flush()
	message := "Invalid \"input\"\nwith a newline and \\path"

	writeOpenAIInvalidRequest(ginContext, message, "input")

	lines := strings.Split(recorder.Body.String(), "\n")
	require.Len(t, lines, 4)
	require.Equal(t, "event:error", lines[0])
	payload := strings.TrimPrefix(lines[1], "data:")
	require.True(t, json.Valid([]byte(payload)))
	require.Equal(t, message, gjson.Get(payload, "error.message").String())
	require.Equal(t, "input", gjson.Get(payload, "error.param").String())
	require.True(t, strings.HasSuffix(recorder.Body.String(), "\n\n"))
}

type openAIEnvironmentUpstreamFunc func(*http.Request) (*http.Response, error)

func (upstream openAIEnvironmentUpstreamFunc) Do(request *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return upstream(request)
}

func TestOpenAIEnvironmentContextForwardSnapshots(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupCodexCache(t)
	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		for _, endpoint := range []string{"responses", "chat", "claude", "images"} {
			if endpoint == "images" && accountType != AccountTypeOAuth {
				continue
			}
			t.Run(accountType+"/"+endpoint, func(t *testing.T) {
				environment := "<environment_context><cwd>/workspace</cwd><timezone>UTC</timezone><current_date>2000-01-01</current_date></environment_context>"
				payload := map[string]any{"model": "gpt-5.5"}
				switch endpoint {
				case "responses":
					payload["instructions"] = environment
					payload["input"] = "hello"
				case "chat":
					payload["messages"] = []any{map[string]any{"role": "system", "content": environment}, map[string]any{"role": "user", "content": "hello"}}
				case "claude":
					payload["system"] = environment
					payload["messages"] = []any{map[string]any{"role": "user", "content": "hello"}}
					payload["max_tokens"] = 64
				}
				body, err := json.Marshal(payload)
				require.NoError(t, err)
				recorder := httptest.NewRecorder()
				ginContext, _ := gin.CreateTestContext(recorder)
				ginContext.Request = httptest.NewRequest(http.MethodPost, "/v1/"+endpoint, strings.NewReader(string(body)))
				ginContext.Request.Header.Set("User-Agent", "codex_cli_rs/0.98.0")
				ginContext.Set(OpenAIImagesDisableJSONHeartbeatContextKey, true)
				var upstreamBody []byte
				upstream := openAIEnvironmentUpstreamFunc(func(request *http.Request) (*http.Response, error) {
					var readErr error
					upstreamBody, readErr = io.ReadAll(request.Body)
					require.NoError(t, readErr)
					if endpoint != "claude" {
						require.Equal(t, string(upstreamBody), ginContext.GetString(OpsUpstreamRequestBodyKey))
					}
					return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"unavailable"}}`))}, nil
				})
				service := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
				account := &Account{ID: 1, Platform: PlatformOpenAI, Type: accountType, Credentials: map[string]any{"api_key": "test-key", "access_token": "test-token"}}
				before := time.Now().In(openAIEnvironmentLocation).Format("2006-01-02")
				switch endpoint {
				case "responses":
					_, err = service.Forward(context.Background(), ginContext, account, body)
				case "chat":
					if accountType == AccountTypeOAuth {
						_, err = service.ForwardChatCompletionsViaResponses(context.Background(), ginContext, account, body, false)
					} else {
						_, err = service.ForwardChatCompletions(context.Background(), ginContext, account, body)
					}
				case "claude":
					_, err = service.ForwardAsClaudeMessages(context.Background(), ginContext, account, body)
				case "images":
					_, err = service.forwardOpenAIImagesOAuthResponses(context.Background(), ginContext, account, &OpenAIImagesRequest{Model: "gpt-image-2", Prompt: "draw a cat"}, time.Now())
				}
				require.Error(t, err)
				require.NotEmpty(t, upstreamBody)
				path := "instructions"
				if accountType == AccountTypeAPIKey && (endpoint == "chat" || endpoint == "claude") {
					path = "messages.0.content"
				}
				text := gjson.GetBytes(upstreamBody, path).String()
				require.Contains(t, text, "<timezone>Asia/Singapore</timezone>")
				after := time.Now().In(openAIEnvironmentLocation).Format("2006-01-02")
				require.True(t, strings.Contains(text, "<current_date>"+before+"</current_date>") || strings.Contains(text, "<current_date>"+after+"</current_date>"))
				if endpoint != "images" {
					require.Contains(t, text, "<cwd>/workspace</cwd>")
				}
				if endpoint != "claude" {
					require.Equal(t, string(upstreamBody), ginContext.GetString(OpsUpstreamRequestBodyKey))
					events, exists := ginContext.Get(OpsUpstreamErrorsKey)
					require.True(t, exists)
					require.NotEmpty(t, events.([]*OpsUpstreamErrorEvent))
					for _, event := range events.([]*OpsUpstreamErrorEvent) {
						require.Equal(t, string(upstreamBody), event.UpstreamRequestBody)
					}
				}
			})
		}
	}
}

func TestOpenAIEnvironmentContextFallbackSnapshots(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"responses", "chat"} {
		t.Run(endpoint, func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.5","input":"hello","messages":[{"role":"user","content":"hello"}]}`)
			ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
			ginContext.Request = httptest.NewRequest(http.MethodPost, "/v1/"+endpoint, strings.NewReader(string(body)))
			ginContext.Request.Header.Set("User-Agent", "codex_cli_rs/0.98.0")
			var models []string
			upstream := openAIEnvironmentUpstreamFunc(func(request *http.Request) (*http.Response, error) {
				upstreamBody, err := io.ReadAll(request.Body)
				require.NoError(t, err)
				require.Equal(t, string(upstreamBody), ginContext.GetString(OpsUpstreamRequestBodyKey))
				require.Contains(t, gjson.GetBytes(upstreamBody, "instructions").String(), "<timezone>Asia/Singapore</timezone>")
				models = append(models, gjson.GetBytes(upstreamBody, "model").String())
				status, responseBody := http.StatusServiceUnavailable, `{"error":{"message":"unavailable"}}`
				if len(models) == 1 {
					status, responseBody = http.StatusBadRequest, `{"error":{"message":"model not supported"}}`
				}
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(responseBody))}, nil
			})
			service := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "test-token"}}
			var err error
			if endpoint == "responses" {
				_, err = service.Forward(context.Background(), ginContext, account, body)
			} else {
				_, err = service.ForwardChatCompletionsViaResponses(context.Background(), ginContext, account, body, false)
			}
			require.Error(t, err)
			require.Equal(t, []string{"gpt-5.5", "gpt-5.4"}, models)
		})
	}
}
