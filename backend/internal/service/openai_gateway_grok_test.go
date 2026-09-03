package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type grokTestUpstream struct {
	response *http.Response
	request  *http.Request
	body     []byte
	err      error
}

type grokSequenceUpstream struct {
	responses []*http.Response
}

func (u *grokTestUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.request = req
	if u.err != nil {
		return nil, u.err
	}
	u.body, _ = io.ReadAll(req.Body)
	return u.response, nil
}

func (u *grokSequenceUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	if len(u.responses) == 0 {
		return nil, errors.New("unexpected upstream call")
	}
	response := u.responses[0]
	u.responses = u.responses[1:]
	return response, nil
}

func newGrokTestContext(method, path string, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, path, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, recorder
}

func TestForwardGrokResponsesAPIKey(t *testing.T) {
	body := []byte(`{"model":"grok","input":"hi","stream":false}`)
	c, _ := newGrokTestContext(http.MethodPost, "/v1/responses", body)
	upstream := &grokTestUpstream{response: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"resp_grok","model":"grok-4.3","usage":{"input_tokens":3,"output_tokens":2}}`,
		)),
	}}
	account := &Account{
		ID:          42,
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "xai-test-key", "base_url": "https://api.x.ai/v1"},
	}
	svc := &OpenAIGatewayService{httpUpstream: upstream, cfg: &config.Config{}}

	result, err := svc.forwardGrokResponses(context.Background(), c, account, body, "grok", false, time.Now())
	if err != nil {
		t.Fatalf("forwardGrokResponses returned error: %v", err)
	}
	if upstream.request == nil {
		t.Fatal("expected an upstream request")
	}
	if got, want := upstream.request.URL.String(), "https://api.x.ai/v1/responses"; got != want {
		t.Fatalf("upstream URL = %q, want %q", got, want)
	}
	if got, want := upstream.request.Header.Get("Authorization"), "Bearer xai-test-key"; got != want {
		t.Fatalf("authorization = %q, want %q", got, want)
	}
	if got, want := gjson.GetBytes(upstream.body, "model").String(), grokDefaultModel; got != want {
		t.Fatalf("upstream model = %q, want %q", got, want)
	}
	if result.Usage.InputTokens != 3 || result.Usage.OutputTokens != 2 {
		t.Fatalf("usage = %#v, want input=3 output=2", result.Usage)
	}
	if result.Model != grokDefaultModel {
		t.Fatalf("billing model = %q, want %q", result.Model, grokDefaultModel)
	}
}

func TestApplyGrokPromptCacheKeyIsTenantAndModelScoped(t *testing.T) {
	body := []byte(`{"model":"grok-4.5","input":"hi","prompt_cache_key":"client-seed"}`)
	c, _ := newGrokTestContext(http.MethodPost, "/v1/responses", body)
	c.Set("api_key", &APIKey{ID: 12})
	first, err := applyGrokPromptCacheKey(body, c, body, "grok-4.5")
	if err != nil {
		t.Fatalf("applyGrokPromptCacheKey returned error: %v", err)
	}
	second, err := applyGrokPromptCacheKey(body, c, body, "grok-4.3")
	if err != nil {
		t.Fatalf("applyGrokPromptCacheKey returned error: %v", err)
	}
	firstKey := gjson.GetBytes(first, "prompt_cache_key").String()
	secondKey := gjson.GetBytes(second, "prompt_cache_key").String()
	if firstKey == "client-seed" || firstKey == "" || firstKey == secondKey {
		t.Fatalf("cache keys are not isolated: first=%q second=%q", firstKey, secondKey)
	}
	other, _ := newGrokTestContext(http.MethodPost, "/v1/responses", body)
	other.Set("api_key", &APIKey{ID: 13})
	third, err := applyGrokPromptCacheKey(body, other, body, "grok-4.5")
	if err != nil {
		t.Fatalf("applyGrokPromptCacheKey returned error: %v", err)
	}
	if got := gjson.GetBytes(third, "prompt_cache_key").String(); got == firstKey {
		t.Fatal("cache key must differ between API keys")
	}
}

func TestGrokUpstreamRequestDoesNotForwardSessionHeaders(t *testing.T) {
	c, _ := newGrokTestContext(http.MethodPost, "/v1/chat/completions", []byte(`{"model":"grok-4.6","messages":[]}`))
	c.Request.Header.Set("session_id", "client-session")
	c.Request.Header.Set("conversation_id", "client-conversation")
	c.Request.Header.Set("accept-language", "zh-CN")
	account := &Account{Platform: PlatformGrok, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_key": "test-key", "base_url": "https://api.x.ai/v1",
	}}
	svc := &OpenAIGatewayService{}
	req, err := svc.buildChatCompletionsRequest(c.Request.Context(), c, account, []byte(`{"model":"grok-4.6","messages":[]}`), "test-key")
	if err != nil {
		t.Fatalf("buildChatCompletionsRequest() error = %v", err)
	}
	if got := req.Header.Get("session_id"); got != "" {
		t.Fatalf("session_id = %q, want omitted", got)
	}
	if got := req.Header.Get("conversation_id"); got != "" {
		t.Fatalf("conversation_id = %q, want omitted", got)
	}
	if got := req.Header.Get("accept-language"); got != "zh-CN" {
		t.Fatalf("accept-language = %q, want forwarded", got)
	}
}

func TestGrokPromptCacheBodyKeyBeatsPerCallConversationHeader(t *testing.T) {
	body := []byte(`{"model":"grok","prompt_cache_key":"parent-session","input":"summary"}`)
	c, _ := newGrokTestContext(http.MethodPost, "/v1/responses", body)
	c.Set("api_key", &APIKey{ID: 77})
	c.Request.Header.Set("X-Grok-Conv-Id", "turn-summary-unique-label")

	got, err := applyGrokPromptCacheKey(body, c, body, "grok-4.5")
	if err != nil {
		t.Fatalf("applyGrokPromptCacheKey returned error: %v", err)
	}

	withoutCallHeader, _ := newGrokTestContext(http.MethodPost, "/v1/responses", body)
	withoutCallHeader.Set("api_key", &APIKey{ID: 77})
	want, err := applyGrokPromptCacheKey(body, withoutCallHeader, body, "grok-4.5")
	if err != nil {
		t.Fatalf("applyGrokPromptCacheKey without call header returned error: %v", err)
	}
	if gotKey, wantKey := gjson.GetBytes(got, "prompt_cache_key").String(), gjson.GetBytes(want, "prompt_cache_key").String(); gotKey != wantKey {
		t.Fatalf("body prompt_cache_key was shadowed by per-call header: got=%q want=%q", gotKey, wantKey)
	}
}

func TestGrokAPIKeyBaseURLIgnoresOAuthEnvironmentDefault(t *testing.T) {
	t.Setenv("XAI_BASE_URL", xai.DefaultCLIBaseURL)
	account := &Account{
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "xai-test-key"},
	}
	if got, want := account.GetGrokBaseURL(), xai.DefaultBaseURL; got != want {
		t.Fatalf("Grok API-key base URL = %q, want %q", got, want)
	}
}

func TestGrokCacheSeedRejectsMessageIDsAndReadsNestedMetadata(t *testing.T) {
	if got := grokCacheSeed(nil, []byte(`{"previous_response_id":"msg_123"}`)); got != "" {
		t.Fatalf("message ID cache seed = %q, want empty", got)
	}
	if got := grokCacheSeed(nil, []byte(`{"previous_response_id":"resp_123"}`)); got != "grok-prev-resp:resp_123" {
		t.Fatalf("response ID cache seed = %q", got)
	}
	if got := grokCacheSeed(nil, []byte(`{"metadata":{"user_id":"{\"session_id\":\"nested-session\"}"},"input":"hi"}`)); got != "nested-session" {
		t.Fatalf("nested metadata cache seed = %q, want nested-session", got)
	}
}

func TestResolveGrokRequestModel(t *testing.T) {
	tests := []struct {
		name        string
		account     *Account
		requested   string
		expectModel string
	}{
		{
			name:        "foreign model falls back",
			account:     &Account{Platform: PlatformGrok},
			requested:   "claude-sonnet-4-6",
			expectModel: grokDefaultModel,
		},
		{
			name:        "grok alias is canonicalized",
			account:     &Account{Platform: PlatformGrok},
			requested:   "grok-latest",
			expectModel: "grok-4.6",
		},
		{
			name:        "unknown grok model is preserved",
			account:     &Account{Platform: PlatformGrok},
			requested:   "grok-future-1",
			expectModel: "grok-future-1",
		},
		{
			name:        "xai provider prefix is stripped",
			account:     &Account{Platform: PlatformGrok},
			requested:   "xai/grok-4.3",
			expectModel: "grok-4.3",
		},
		{
			name:        "composer alias is canonicalized",
			account:     &Account{Platform: PlatformGrok},
			requested:   "composer-2.5",
			expectModel: "grok-composer-2.5-fast",
		},
		{
			name: "explicit custom mapping wins",
			account: &Account{Platform: PlatformGrok, Credentials: map[string]any{
				"model_mapping": map[string]any{"claude-sonnet-4-6": "xai-custom-model"},
			}},
			requested:   "claude-sonnet-4-6",
			expectModel: "xai-custom-model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveGrokRequestModel(tt.account, tt.requested); got != tt.expectModel {
				t.Fatalf("resolveGrokRequestModel() = %q, want %q", got, tt.expectModel)
			}
		})
	}
}

func TestResolveGrokRequestModelSupportsAliasAndWildcardMappings(t *testing.T) {
	account := &Account{
		Platform: PlatformGrok,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"grok-4.5": "grok-4.5",
				"grok-3-*": "grok-4.3",
			},
		},
	}
	if got := resolveGrokRequestModel(account, "grok-4.5-latest"); got != "grok-4.5" {
		t.Fatalf("alias mapping = %q, want grok-4.5", got)
	}
	if got := resolveGrokRequestModel(account, "grok-3-mini-fast"); got != "grok-4.3" {
		t.Fatalf("wildcard mapping = %q, want grok-4.3", got)
	}
	if !isGrokModelSupportedByAccount(account, "grok-4.5-latest") {
		t.Fatal("canonical alias should be considered supported")
	}
	if isGrokModelSupportedByAccount(account, "grok-4.6") {
		t.Fatal("unmapped Grok model should not be considered supported")
	}
}

func TestIsGrokModelSupportedByAccountWithoutMapping(t *testing.T) {
	cases := []struct {
		name        string
		credentials map[string]any
	}{
		{name: "nil credentials"},
		{name: "empty credentials", credentials: map[string]any{}},
		{name: "empty model mapping", credentials: map[string]any{
			"model_mapping": map[string]any{},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{
				Platform:    PlatformGrok,
				Type:        AccountTypeAPIKey,
				Credentials: tc.credentials,
			}
			if !isGrokModelSupportedByAccount(account, "grok-4.6") {
				t.Fatal("Grok API Key without an explicit mapping should support grok-4.6")
			}
			if !isGrokModelSupportedByAccount(account, "grok-future-1") {
				t.Fatal("Grok API Key without an explicit mapping should support future Grok models")
			}
		})
	}
}

func TestNormalizeGrokChatReasoningReportsOnlyActualChanges(t *testing.T) {
	request := map[string]any{"reasoning_effort": "high"}
	if normalizeGrokChatReasoning(request, "grok-4.5") {
		t.Fatal("already normalized reasoning should not report a change")
	}
	if got := request["reasoning_effort"]; got != "high" {
		t.Fatalf("reasoning_effort = %#v, want high", got)
	}

	request = map[string]any{"reasoning_effort": "minimal"}
	if !normalizeGrokChatReasoning(request, "grok-4.5") {
		t.Fatal("minimal reasoning should report normalization")
	}
	if got := request["reasoning_effort"]; got != "low" {
		t.Fatalf("reasoning_effort = %#v, want low", got)
	}
}

func TestExtractOpenAIUsageIncludesIndependentGrokReasoningTokens(t *testing.T) {
	usage, ok := extractOpenAIUsageFromJSONBytes([]byte(`{"usage":{"prompt_tokens":32,"completion_tokens":9,"total_tokens":135,"completion_tokens_details":{"reasoning_tokens":94}}}`), true)
	if !ok {
		t.Fatal("expected usage to parse")
	}
	if usage.OutputTokens != 103 {
		t.Fatalf("output tokens = %d, want 103", usage.OutputTokens)
	}
}

func TestForwardGrokResponsesTransportErrorWritesBadGateway(t *testing.T) {
	body := []byte(`{"model":"grok-4.3","input":"hi","stream":false}`)
	c, recorder := newGrokTestContext(http.MethodPost, "/v1/responses", body)
	upstream := &grokTestUpstream{err: errors.New("dial tcp 192.0.2.1:443: connection refused")}
	account := &Account{
		ID:          50,
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "xai-test-key", "base_url": "https://api.x.ai/v1"},
	}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	if _, err := svc.forwardGrokResponses(context.Background(), c, account, body, "grok-4.3", false, time.Now()); err == nil {
		t.Fatal("expected transport error")
	}
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}
	if !strings.Contains(recorder.Body.String(), `"type":"upstream_error"`) {
		t.Fatalf("response body = %s, want upstream_error", recorder.Body.String())
	}
	if captured, ok := c.Get(OpsUpstreamRequestBodyKey); !ok || !strings.Contains(captured.(string), `"model":"grok-4.3"`) {
		t.Fatalf("upstream request body was not captured: %#v", captured)
	}
}

func TestForwardGrokResponsesKeepsReplayErrorBodyWhenRetryIsNotApplicable(t *testing.T) {
	body := []byte(`{"model":"grok-4.3","input":"hi","stream":false}`)
	c, _ := newGrokTestContext(http.MethodPost, "/v1/responses", body)
	upstream := &grokSequenceUpstream{responses: []*http.Response{{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"Could not decrypt encrypted_content"}}`)),
	}}}
	account := &Account{
		ID:          52,
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "xai-test-key", "base_url": "https://api.x.ai/v1"},
	}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	_, err := svc.forwardGrokResponses(context.Background(), c, account, body, "grok-4.3", false, time.Now())
	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) {
		t.Fatalf("error = %v, want replay error failover", err)
	}
}

func TestForwardGrokResponsesOAuthUsesCLIProxyAndIdentityHeaders(t *testing.T) {
	t.Setenv("XAI_BASE_URL", "")
	body := []byte(`{"model":"claude-sonnet-4-6","input":"hi","stream":false}`)
	c, _ := newGrokTestContext(http.MethodPost, "/v1/responses", body)
	upstream := &grokTestUpstream{response: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"resp_grok","model":"grok-4.3","usage":{"input_tokens":3,"output_tokens":2}}`,
		)),
	}}
	account := &Account{
		ID:          46,
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "grok-access"},
	}
	svc := &OpenAIGatewayService{httpUpstream: upstream, cfg: &config.Config{}}

	result, err := svc.forwardGrokResponses(context.Background(), c, account, body, "claude-sonnet-4-6", false, time.Now())
	if err != nil {
		t.Fatalf("forwardGrokResponses returned error: %v", err)
	}
	if got, want := upstream.request.URL.String(), xai.DefaultCLIBaseURL+"/responses"; got != want {
		t.Fatalf("upstream URL = %q, want %q", got, want)
	}
	if got, want := upstream.request.Header.Get("X-XAI-Token-Auth"), xai.CLITokenAuth; got != want {
		t.Fatalf("token auth header = %q, want %q", got, want)
	}
	if got, want := upstream.request.Header.Get("x-grok-client-identifier"), xai.CLIClientIdentifier; got != want {
		t.Fatalf("client identifier = %q, want %q", got, want)
	}
	if got, want := gjson.GetBytes(upstream.body, "model").String(), grokDefaultModel; got != want {
		t.Fatalf("upstream model = %q, want %q", got, want)
	}
	if result.Model != grokDefaultModel {
		t.Fatalf("billing model = %q, want %q", result.Model, grokDefaultModel)
	}
}

func TestGrokTextEndpointsRejectImagineModels(t *testing.T) {
	account := &Account{
		ID:          53,
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "grok-test-key", "base_url": "https://api.x.ai/v1"},
	}
	upstream := &grokTestUpstream{response: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	oauthAccount := *account
	oauthAccount.Type = AccountTypeOAuth
	oauthAccount.Credentials = map[string]any{"access_token": "grok-oauth-token", "base_url": "https://api.x.ai/v1"}

	tests := []struct {
		name string
		call func(*gin.Context) error
	}{
		{
			name: "responses",
			call: func(c *gin.Context) error {
				_, err := svc.forwardGrokResponses(context.Background(), c, account, []byte(`{"model":"grok-imagine-image","input":"cat"}`), "grok-imagine-image", false, time.Now())
				return err
			},
		},
		{
			name: "chat completions",
			call: func(c *gin.Context) error {
				_, err := svc.ForwardChatCompletions(context.Background(), c, account, []byte(`{"model":"grok-imagine-image","messages":[]}`))
				return err
			},
		},
		{
			name: "chat through responses",
			call: func(c *gin.Context) error {
				_, err := svc.ForwardChatCompletionsViaResponses(context.Background(), c, account, []byte(`{"model":"grok-imagine-video","messages":[]}`), false)
				return err
			},
		},
		{
			name: "Claude API key compatibility",
			call: func(c *gin.Context) error {
				_, err := svc.ForwardAsClaudeMessages(context.Background(), c, account, []byte(`{"model":"grok-imagine-image","messages":[{"role":"user","content":"cat"}]}`))
				return err
			},
		},
		{
			name: "Claude OAuth compatibility",
			call: func(c *gin.Context) error {
				_, err := svc.ForwardAsClaudeMessages(context.Background(), c, &oauthAccount, []byte(`{"model":"grok-imagine-video","messages":[{"role":"user","content":"waves"}]}`))
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream.request = nil
			c, recorder := newGrokTestContext(http.MethodPost, "/v1/test", nil)
			if err := tt.call(c); err == nil {
				t.Fatal("expected Imagine model validation error")
			}
			if upstream.request != nil {
				t.Fatal("Imagine model request must not reach upstream")
			}
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
			}
			if !strings.Contains(recorder.Body.String(), "image/video model") {
				t.Fatalf("response body = %s, want validation message", recorder.Body.String())
			}
		})
	}
}

func TestForwardClaudeGrokAPIKeyUsesGrokModelForBilling(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	c, _ := newGrokTestContext(http.MethodPost, "/v1/messages", body)
	upstream := &grokTestUpstream{response: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"chatcmpl_grok","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
		)),
	}}
	account := &Account{
		ID:          47,
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "xai-test-key", "base_url": "https://api.x.ai/v1"},
	}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.ForwardAsClaudeMessages(context.Background(), c, account, body)
	if err != nil {
		t.Fatalf("ForwardAsClaudeMessages returned error: %v", err)
	}
	if result == nil || result.Model != grokDefaultModel {
		t.Fatalf("billing model = %#v, want %q", result, grokDefaultModel)
	}
	if got, want := gjson.GetBytes(upstream.body, "model").String(), grokDefaultModel; got != want {
		t.Fatalf("upstream model = %q, want %q", got, want)
	}
	if gjson.GetBytes(upstream.body, "reasoning").Exists() {
		t.Fatalf("Grok Chat request must not contain Responses reasoning object: %s", upstream.body)
	}
}

func TestForwardGrokChatCompletionsStreamingIncludesUsage(t *testing.T) {
	body := []byte(`{"model":"grok-4.3","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	c, _ := newGrokTestContext(http.MethodPost, "/v1/chat/completions", body)
	upstream := &grokTestUpstream{response: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(strings.Join([]string{
			`data: {"choices":[{"delta":{"content":"ok"}}]}`,
			`data: {"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
			`data: [DONE]`,
			"",
		}, "\n"))),
	}}
	account := &Account{
		ID:          51,
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "xai-test-key", "base_url": "https://api.x.ai/v1"},
	}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.ForwardChatCompletions(context.Background(), c, account, body)
	if err != nil {
		t.Fatalf("ForwardChatCompletions returned error: %v", err)
	}
	if result.Usage.InputTokens != 3 || result.Usage.OutputTokens != 2 {
		t.Fatalf("usage = %#v, want input=3 output=2", result.Usage)
	}
	if got := gjson.GetBytes(upstream.body, "stream_options.include_usage").Bool(); !got {
		t.Fatalf("stream_options.include_usage = false, body=%s", upstream.body)
	}
}

func TestForwardGrokChatCompletionsUsesIsolatedCacheHeader(t *testing.T) {
	body := []byte(`{"model":"grok-4.3","messages":[{"role":"user","content":"hi"}],"prompt_cache_key":"client-seed"}`)
	c, _ := newGrokTestContext(http.MethodPost, "/v1/chat/completions", body)
	c.Set("api_key", &APIKey{ID: 99})
	upstream := &grokTestUpstream{response: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"chatcmpl_grok","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)),
	}}
	account := &Account{
		ID:          99,
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "xai-test-key", "base_url": "https://api.x.ai/v1"},
	}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	if _, err := svc.ForwardChatCompletions(context.Background(), c, account, body); err != nil {
		t.Fatalf("ForwardChatCompletions returned error: %v", err)
	}
	if got := upstream.request.Header.Get("X-Grok-Conv-Id"); got == "" || got == "client-seed" {
		t.Fatalf("cache header = %q, want isolated key", got)
	}
	if gjson.GetBytes(upstream.body, "prompt_cache_key").Exists() {
		t.Fatalf("Chat Completions body must not contain Responses prompt_cache_key: %s", upstream.body)
	}
}

func TestResolveGrokStreamIdleTimeout(t *testing.T) {
	if got := resolveGrokStreamIdleTimeout(0, &Account{Platform: PlatformGrok}); got != defaultGrokStreamIdleTimeout {
		t.Fatalf("default Grok idle timeout = %s, want %s", got, defaultGrokStreamIdleTimeout)
	}
	if got := resolveGrokStreamIdleTimeout(7, &Account{Platform: PlatformGrok}); got != 7*time.Second {
		t.Fatalf("configured idle timeout = %s, want 7s", got)
	}
	if got := resolveGrokStreamIdleTimeout(0, &Account{Platform: PlatformOpenAI}); got != 0 {
		t.Fatalf("non-Grok idle timeout = %s, want disabled", got)
	}
	if err := grokStreamIdleFailoverError(&Account{Platform: PlatformGrok}, 7*time.Second); err.StatusCode != http.StatusBadGateway || err.Message == "" {
		t.Fatalf("idle failover error = %#v", err)
	}
}

func TestForwardClaudeGrokOAuthNonStreamingReturnsClaudeJSON(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	c, recorder := newGrokTestContext(http.MethodPost, "/v1/messages", body)
	upstream := &grokTestUpstream{response: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"resp_grok","model":"grok-4.3","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":3,"output_tokens":2}}`,
		)),
	}}
	account := &Account{
		ID:          48,
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "grok-access", "base_url": "https://api.x.ai/v1"},
	}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.ForwardAsClaudeMessages(context.Background(), c, account, body)
	if err != nil {
		t.Fatalf("ForwardAsClaudeMessages returned error: %v", err)
	}
	if result == nil || result.Stream {
		t.Fatalf("result = %#v, want non-streaming result", result)
	}
	if result.Usage.InputTokens != 3 || result.Usage.OutputTokens != 2 {
		t.Fatalf("usage = %#v, want input=3 output=2", result.Usage)
	}
	if got, want := recorder.Header().Get("Content-Type"), "application/json; charset=utf-8"; got != want {
		t.Fatalf("response content type = %q, want %q", got, want)
	}
	if got, want := gjson.GetBytes(upstream.body, "reasoning.effort").String(), "xhigh"; got != want {
		t.Fatalf("Grok Responses reasoning effort = %q, want %q; body=%s", got, want, upstream.body)
	}
	if !strings.Contains(recorder.Body.String(), `"type":"message"`) || !strings.Contains(recorder.Body.String(), `"text":"ok"`) {
		t.Fatalf("response body is not Claude JSON: %s", recorder.Body.String())
	}
}

func TestForwardGrokResponsesStreamingPreservesEventsAndParsesUsage(t *testing.T) {
	body := []byte(`{"model":"grok-4.3","input":"hi","stream":true}`)
	c, recorder := newGrokTestContext(http.MethodPost, "/v1/responses", body)
	stream := strings.Join([]string{
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":8,"output_tokens":2}}}`,
		``,
	}, "\n")
	upstream := &grokTestUpstream{response: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(stream)),
	}}
	account := &Account{
		ID:          49,
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "xai-test-key", "base_url": "https://api.x.ai/v1"},
	}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.Forward(context.Background(), c, account, body)
	if err != nil {
		t.Fatalf("Forward returned error: %v", err)
	}
	if result == nil || !result.Stream {
		t.Fatalf("result = %#v, want streaming result", result)
	}
	if result.Usage.InputTokens != 8 || result.Usage.OutputTokens != 2 {
		t.Fatalf("usage = %#v, want input=8 output=2", result.Usage)
	}
	responseBody := recorder.Body.String()
	if strings.Contains(responseBody, "event: response.output_text.delta") || !strings.Contains(responseBody, `"type":"response.output_text.delta"`) || !strings.Contains(responseBody, `"delta":"hello"`) {
		t.Fatalf("response event type was not carried in payload: %s", responseBody)
	}
}

func TestForwardGrokResponsesStreamingAddsMissingPayloadEventType(t *testing.T) {
	body := []byte(`{"model":"grok-4.3","input":"hi","stream":true}`)
	c, recorder := newGrokTestContext(http.MethodPost, "/v1/responses", body)
	stream := strings.Join([]string{
		`event: response.output_text.delta`,
		`data: {"delta":"hello"}`,
		``,
		`event: response.completed`,
		`data: {}`,
		``,
	}, "\n")
	upstream := &grokTestUpstream{response: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(stream)),
	}}
	account := &Account{ID: 54, Platform: PlatformGrok, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "xai-test-key", "base_url": "https://api.x.ai/v1"}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	if _, err := svc.Forward(context.Background(), c, account, body); err != nil {
		t.Fatalf("Forward returned error: %v", err)
	}
	if !strings.Contains(recorder.Body.String(), `"type":"response.output_text.delta"`) {
		t.Fatalf("missing injected event type: %s", recorder.Body.String())
	}
}

func TestGrokResponsesPingFilterRewritesStrictPingFrames(t *testing.T) {
	source := io.NopCloser(strings.NewReader(strings.Join([]string{
		"event: ping",
		`data: {"type":"ping"}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","delta":"ok"}`,
		"",
	}, "\n")))
	filtered := newGrokResponsesPingFilterBody(source, &Account{Platform: PlatformGrok}, defaultMaxLineSize)
	defer filtered.Close()
	data, err := io.ReadAll(filtered)
	if err != nil {
		t.Fatalf("read filtered stream: %v", err)
	}
	if strings.Contains(string(data), "event: ping") || !strings.Contains(string(data), ": ping\n\n") {
		t.Fatalf("ping frame was not rewritten: %s", data)
	}
	if !strings.Contains(string(data), "response.output_text.delta") {
		t.Fatalf("non-ping frame was not preserved: %s", data)
	}
}

func TestForwardGrokResponsesRejectsDisallowedBaseURL(t *testing.T) {
	body := []byte(`{"model":"grok-4.3","input":"hi","stream":false}`)
	c, _ := newGrokTestContext(http.MethodPost, "/v1/responses", body)
	upstream := &grokTestUpstream{response: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"usage":{"input_tokens":1,"output_tokens":1}}`)),
	}}
	account := &Account{
		ID:          44,
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "xai-test-key", "base_url": "https://evil.example/v1"},
	}
	svc := &OpenAIGatewayService{
		httpUpstream: upstream,
		cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
			Enabled:       true,
			UpstreamHosts: []string{"api.x.ai"},
		}}},
	}

	if _, err := svc.forwardGrokResponses(context.Background(), c, account, body, "grok-4.3", false, time.Now()); err == nil || !strings.Contains(err.Error(), "invalid base_url") {
		t.Fatalf("forwardGrokResponses error = %v, want disallowed base_url error", err)
	}
	if upstream.request != nil {
		t.Fatal("disallowed Grok base URL should not reach upstream")
	}
}

func TestGrokErrorClassificationKeepsContentPolicyOnCurrentAccount(t *testing.T) {
	svc := &OpenAIGatewayService{}
	policy := []byte(`{"error":{"code":"content_policy_violation","message":"prompt violates content policy"}}`)
	if svc.shouldFailoverGrokUpstreamError(http.StatusForbidden, policy) {
		t.Fatal("content-policy rejection must not fail over")
	}
	quota := []byte(`{"error":{"message":"free usage exhausted"}}`)
	if !svc.shouldFailoverGrokUpstreamError(http.StatusTooManyRequests, quota) {
		t.Fatal("quota rejection should fail over")
	}
}

func TestGrokErrorClassificationHandlesStructuredAndDecoderErrors(t *testing.T) {
	svc := &OpenAIGatewayService{}
	if svc.shouldFailoverGrokUpstreamError(http.StatusForbidden, []byte(`{"error":{"code":"content_filter"}}`)) {
		t.Fatal("structured content filter should remain request-scoped")
	}
	if !svc.shouldFailoverGrokUpstreamError(http.StatusForbidden, []byte(`{"error":{"code":"account_suspended"}}`)) {
		t.Fatal("account suspension should fail over")
	}
	decoderError := []byte(`{"error":{"message":"input[0] failed to deserialize ModelInput"}}`)
	if !svc.shouldFailoverGrokUpstreamError(http.StatusUnprocessableEntity, decoderError) {
		t.Fatal("decoder compatibility error should fail over")
	}
}

func TestPatchGrokResponsesBodySanitizesUnsupportedFieldsAndTools(t *testing.T) {
	body := []byte(`{"model":"grok-4.5","external_web_access":true,"presence_penalty":0.2,"input":[{"type":"additional_tools","tools":[{"type":"function","name":"lookup"}]},{"role":"user","content":null}],"tools":[{"type":"function","name":"lookup"},{"type":"unsupported","name":"drop"}],"tool_choice":{"type":"function","name":"missing"}}`)
	patched, err := patchGrokResponsesBody(body, "grok-4.5")
	if err != nil {
		t.Fatalf("patchGrokResponsesBody returned error: %v", err)
	}
	if gjson.GetBytes(patched, "external_web_access").Exists() || gjson.GetBytes(patched, "presence_penalty").Exists() {
		t.Fatalf("unsupported fields were not removed: %s", patched)
	}
	if len(gjson.GetBytes(patched, "tools").Array()) != 1 {
		t.Fatalf("unexpected sanitized tools: %s", patched)
	}
	if gjson.GetBytes(patched, "tool_choice").Exists() {
		t.Fatalf("invalid tool_choice was not removed: %s", patched)
	}
	if gjson.GetBytes(patched, "input.1.content").Exists() {
		t.Fatalf("null input content was not removed: %s", patched)
	}
}

func TestGrokReplayRetryPreservesSummaryAndDropsOpaqueState(t *testing.T) {
	body := []byte(`{"previous_response_id":"resp_stale","input":[{"type":"compaction","encrypted_content":"opaque","summary":[{"type":"summary_text","text":"visible history"}]},{"type":"reasoning","id":"rs_1","content":null,"encrypted_content":"opaque-2"},{"type":"message","role":"user","content":"continue"}]}`)
	patched, changed, err := sanitizeGrokCompactionReplayBody(body)
	if err != nil {
		t.Fatalf("sanitizeGrokCompactionReplayBody returned error: %v", err)
	}
	if !changed {
		t.Fatal("expected replay body to change")
	}
	if gjson.GetBytes(patched, "previous_response_id").Exists() {
		t.Fatalf("stale previous_response_id was not removed: %s", patched)
	}
	if gjson.GetBytes(patched, `input.#(type=="compaction")`).Exists() || gjson.GetBytes(patched, `input.#(type=="reasoning")`).Exists() {
		t.Fatalf("opaque replay items remain: %s", patched)
	}
	if !strings.Contains(gjson.GetBytes(patched, "input.0.content.0.text").String(), "visible history") {
		t.Fatalf("visible compaction summary was lost: %s", patched)
	}
}

func TestForwardGrokResponsesRetriesCompactionDecode422(t *testing.T) {
	body := []byte(`{"model":"grok-4.5","input":[{"type":"reasoning","encrypted_content":"opaque"}],"stream":false}`)
	c, _ := newGrokTestContext(http.MethodPost, "/v1/responses", body)
	upstream := &grokSequenceUpstream{responses: []*http.Response{
		{
			StatusCode: http.StatusUnprocessableEntity,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"failed to decode compaction blob"}}`)),
		},
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"resp_retry","usage":{"input_tokens":1,"output_tokens":1}}`)),
		},
	}}
	account := &Account{ID: 53, Platform: PlatformGrok, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "xai-test-key", "base_url": "https://api.x.ai/v1"}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	if _, err := svc.forwardGrokResponses(context.Background(), c, account, body, "grok-4.5", false, time.Now()); err != nil {
		t.Fatalf("forwardGrokResponses returned error: %v", err)
	}
	if len(upstream.responses) != 0 {
		t.Fatalf("upstream calls remaining = %d, want both attempts consumed", len(upstream.responses))
	}
}

func TestSanitizeGrokResponsesInputLiftsToolOutputImages(t *testing.T) {
	body := []byte(`{"input":[{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"ok","images":[{"url":"data:image/png;base64,QQ=="}]}]}`)
	patched, err := patchGrokResponsesBody(body, "grok-4.5")
	if err != nil {
		t.Fatalf("patchGrokResponsesBody returned error: %v", err)
	}
	if got := gjson.GetBytes(patched, "input.1.output").String(); got != "ok" {
		t.Fatalf("tool output = %q, want ok", got)
	}
	if got := gjson.GetBytes(patched, "input.#(type==\"message\").content.1.type").String(); got != "input_image" {
		t.Fatalf("lifted image type = %q, want input_image: %s", got, patched)
	}
}

func TestGrokResponsesInputPairsToolHistoryAndNormalizesAliases(t *testing.T) {
	body := []byte(`{"input":[{"type":"function_call_output","output":{"ok":true}},{"type":"custom_tool_call","id":"custom_1","name":"lookup","input":{"q":"x"}}]}`)
	patched, err := patchGrokResponsesBody(body, "grok-4.5")
	if err != nil {
		t.Fatalf("patchGrokResponsesBody returned error: %v", err)
	}
	callID := gjson.GetBytes(patched, "input.1.call_id").String()
	if callID == "" {
		t.Fatalf("normalized function call has no call_id: %s", patched)
	}
	if got := gjson.GetBytes(patched, "input.0.call_id").String(); got != callID {
		t.Fatalf("orphan output call_id = %q, want %q: %s", got, callID, patched)
	}
	if got := gjson.GetBytes(patched, "input.1.type").String(); got != "function_call" {
		t.Fatalf("normalized call type = %q, want function_call", got)
	}
	if got := gjson.GetBytes(patched, "input.1.arguments").String(); got != `{"input":{"q":"x"}}` {
		t.Fatalf("normalized arguments = %q, want wrapped JSON object", got)
	}
}

func TestLowerGrokClientToolsAndRestoresResponse(t *testing.T) {
	body := []byte(`{
		"model":"grok-4.6",
		"tools":[
			{"type":"custom","name":"apply_patch"},
			{"type":"tool_search"},
			{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"send_message","parameters":{"type":"object"}}]}
		],
		"input":[{"type":"custom_tool_call","id":"ctc_1","name":"apply_patch","input":"*** Begin Patch"}]
	}`)
	patched, mapping, err := lowerGrokClientToolsForTest(body)
	if err != nil {
		t.Fatalf("lowerGrokClientTools returned error: %v", err)
	}
	if !mapping.CustomTools["apply_patch"] || !mapping.ToolSearch {
		t.Fatalf("unexpected mapping: %#v", mapping)
	}
	if got := mapping.NamespaceTools["collaboration__send_message"]; got.Namespace != "collaboration" || got.Name != "send_message" {
		t.Fatalf("unexpected namespace mapping: %#v", mapping.NamespaceTools)
	}
	if got := gjson.GetBytes(patched, "tools.#(name==\"apply_patch\").type").String(); got != "function" {
		t.Fatalf("custom tool type = %q, want function: %s", got, patched)
	}
	if got := gjson.GetBytes(patched, "tools.#(name==\"collaboration__send_message\").type").String(); got != "function" {
		t.Fatalf("namespace tool type = %q, want function: %s", got, patched)
	}
	if got := gjson.GetBytes(patched, "input.0.type").String(); got != "function_call" {
		t.Fatalf("history type = %q, want function_call: %s", got, patched)
	}

	c, _ := newGrokTestContext(http.MethodPost, "/v1/responses", nil)
	setGrokResponsesClientToolMapping(c, mapping)
	restored, err := restoreGrokResponsesClientToolPayload(c, []byte(`{"output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"apply_patch","arguments":"{\"input\":\"*** Begin Patch\"}"}]}`))
	if err != nil {
		t.Fatalf("restoreGrokResponsesClientToolPayload returned error: %v", err)
	}
	if got := gjson.GetBytes(restored, "output.0.type").String(); got != "custom_tool_call" {
		t.Fatalf("restored type = %q, want custom_tool_call: %s", got, restored)
	}
	if got := gjson.GetBytes(restored, "output.0.input").String(); got != "*** Begin Patch" {
		t.Fatalf("restored custom input = %q", got)
	}
}

func lowerGrokClientToolsForTest(body []byte) ([]byte, grokResponsesClientToolMapping, error) {
	return lowerGrokResponsesClientTools(body)
}

func TestLowerGrokClientToolsPromotesDiscoveredNamespace(t *testing.T) {
	body := []byte(`{
		"model":"grok-4.6",
		"tools":[{"type":"tool_search"}],
		"input":[{"type":"tool_search_output","status":"completed","call_id":"search_1","tools":[{"type":"namespace","name":"multi_agent","tools":[{"type":"function","name":"spawn_agent","parameters":{"type":"object"}}]}]}]
	}`)
	patched, mapping, err := lowerGrokResponsesClientTools(body)
	if err != nil {
		t.Fatalf("lowerGrokResponsesClientTools returned error: %v", err)
	}
	if got := mapping.NamespaceTools["multi_agent__spawn_agent"]; got.Namespace != "multi_agent" || got.Name != "spawn_agent" {
		t.Fatalf("discovered namespace mapping = %#v", mapping.NamespaceTools)
	}
	if got := gjson.GetBytes(patched, "tools.#(name==\"multi_agent__spawn_agent\").type").String(); got != "function" {
		t.Fatalf("promoted tool type = %q, want function: %s", got, patched)
	}
	if got := gjson.GetBytes(patched, "input.0.type").String(); got != "function_call_output" {
		t.Fatalf("search output type = %q, want function_call_output: %s", got, patched)
	}
	if got := gjson.GetBytes(patched, "input.0.output").String(); !strings.Contains(got, "multi_agent") {
		t.Fatalf("search output lost discovered tools: %q", got)
	}
}

func TestRestoreGrokClientToolStreamCustomCall(t *testing.T) {
	c, _ := newGrokTestContext(http.MethodPost, "/v1/responses", nil)
	setGrokResponsesClientToolMapping(c, grokResponsesClientToolMapping{CustomTools: map[string]bool{"apply_patch": true}})
	events := [][]byte{
		[]byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"apply_patch","arguments":""}}`),
		[]byte(`{"type":"response.function_call_arguments.delta","sequence_number":1,"output_index":0,"item_id":"fc_1","delta":"{\"input\":\"patch\"}"}`),
		[]byte(`{"type":"response.function_call_arguments.done","sequence_number":2,"output_index":0,"item_id":"fc_1","arguments":"{\"input\":\"patch\"}"}`),
		[]byte(`{"type":"response.output_item.done","sequence_number":3,"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"apply_patch","arguments":"{\"input\":\"patch\"}"}}`),
	}
	var output [][]byte
	for _, event := range events {
		converted, err := restoreGrokResponsesClientToolStreamPayload(c, event)
		if err != nil {
			t.Fatalf("restore stream event failed: %v", err)
		}
		output = append(output, converted...)
	}
	if len(output) != 4 {
		t.Fatalf("restored event count = %d, want 4", len(output))
	}
	if got := gjson.GetBytes(output[0], "item.type").String(); got != "custom_tool_call" {
		t.Fatalf("added item type = %q", got)
	}
	if got := gjson.GetBytes(output[1], "type").String(); got != "response.custom_tool_call_input.delta" {
		t.Fatalf("synthetic delta type = %q", got)
	}
	if got := gjson.GetBytes(output[1], "delta").String(); got != "patch" {
		t.Fatalf("synthetic delta = %q", got)
	}
	if got := gjson.GetBytes(output[2], "type").String(); got != "response.custom_tool_call_input.done" {
		t.Fatalf("synthetic done type = %q", got)
	}
	if got := gjson.GetBytes(output[3], "item.input").String(); got != "patch" {
		t.Fatalf("done item input = %q", got)
	}
}

func TestRestoreGrokClientToolStreamNamespaceAndToolSearch(t *testing.T) {
	c, _ := newGrokTestContext(http.MethodPost, "/v1/responses", nil)
	setGrokResponsesClientToolMapping(c, grokResponsesClientToolMapping{
		ToolSearch: true,
		NamespaceTools: map[string]grokNamespaceTool{
			"collaboration__send_message": {Namespace: "collaboration", Name: "send_message"},
		},
	})
	events := [][]byte{
		[]byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"type":"function_call","id":"fc_ns","call_id":"call_ns","name":"collaboration__send_message","arguments":""}}`),
		[]byte(`{"type":"response.function_call_arguments.delta","sequence_number":1,"output_index":0,"item_id":"fc_ns","name":"collaboration__send_message","delta":"{}"}`),
		[]byte(`{"type":"response.output_item.added","sequence_number":2,"output_index":1,"item":{"type":"function_call","id":"fc_search","call_id":"call_search","name":"tool_search","arguments":""}}`),
		[]byte(`{"type":"response.output_item.done","sequence_number":3,"output_index":1,"item":{"type":"function_call","id":"fc_search","call_id":"call_search","name":"tool_search","arguments":"{\"query\":\"github\"}"}}`),
	}
	var output [][]byte
	for _, event := range events {
		converted, err := restoreGrokResponsesClientToolStreamPayload(c, event)
		if err != nil {
			t.Fatalf("restore stream event failed: %v", err)
		}
		output = append(output, converted...)
	}
	if len(output) != len(events) {
		t.Fatalf("restored event count = %d, want %d", len(output), len(events))
	}
	if got := gjson.GetBytes(output[0], "item.name").String(); got != "send_message" {
		t.Fatalf("namespace item name = %q", got)
	}
	if got := gjson.GetBytes(output[0], "item.namespace").String(); got != "collaboration" {
		t.Fatalf("namespace item namespace = %q", got)
	}
	if got := gjson.GetBytes(output[1], "name").String(); got != "send_message" {
		t.Fatalf("namespace argument event name = %q", got)
	}
	if got := gjson.GetBytes(output[2], "item.type").String(); got != "tool_search_call" {
		t.Fatalf("tool search item type = %q", got)
	}
	if got := gjson.GetBytes(output[3], "item.arguments.query").String(); got != "github" {
		t.Fatalf("tool search arguments = %q", got)
	}
	if got := gjson.GetBytes(output[3], "item.execution").String(); got != "client" {
		t.Fatalf("tool search execution = %q", got)
	}
}

func TestLowerGrokClientToolsRejectsIncompleteToolSearchOutput(t *testing.T) {
	body := []byte(`{"model":"grok","tools":[{"type":"tool_search"}],"input":[{"type":"tool_search_output","call_id":"search_1"}]}`)
	if _, _, err := lowerGrokResponsesClientTools(body); err == nil || !strings.Contains(err.Error(), "output or tools") {
		t.Fatalf("error = %v, want missing output/tools error", err)
	}
}

func TestAccountTestServiceGrokAPIKey(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"ok"}`,
		"",
		`data: {"type":"response.completed"}`,
		"",
	}, "\n")
	upstream := &grokTestUpstream{response: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}}
	account := &Account{
		ID:          43,
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "xai-test-key", "base_url": "https://api.x.ai/v1"},
	}
	svc := &AccountTestService{httpUpstream: upstream}
	c, recorder := newGrokTestContext(http.MethodPost, "/api/v1/admin/accounts/43/test", nil)

	if err := svc.testGrokAccountConnection(c, account, "grok"); err != nil {
		t.Fatalf("testGrokAccountConnection returned error: %v", err)
	}
	if got, want := upstream.request.URL.String(), "https://api.x.ai/v1/responses"; got != want {
		t.Fatalf("upstream URL = %q, want %q", got, want)
	}
	if got := recorder.Body.String(); !strings.Contains(got, `"type":"test_complete"`) {
		t.Fatalf("test response did not contain completion event: %s", got)
	}
}

func TestProcessOpenAIStreamAcceptsPayloadEventTypes(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "payload type only",
			body: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
				"data: {\"type\":\"response.done\"}\n\n",
		},
		{
			name: "event type only",
			body: "event: response.output_text.delta\n" +
				"data: {\"delta\":\"hello\"}\n\n" +
				"event: response.completed\n" +
				"data: {}\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, recorder := newGrokTestContext(http.MethodPost, "/test", nil)
			svc := &AccountTestService{}
			if err := svc.processOpenAIStream(c, strings.NewReader(tt.body)); err != nil {
				t.Fatalf("processOpenAIStream returned error: %v", err)
			}
			responseBody := recorder.Body.String()
			if !strings.Contains(responseBody, `"type":"content"`) || !strings.Contains(responseBody, `"text":"hello"`) {
				t.Fatalf("content event missing: %s", responseBody)
			}
			if !strings.Contains(responseBody, `"type":"test_complete"`) {
				t.Fatalf("completion event missing: %s", responseBody)
			}
		})
	}
}

func TestProcessOpenAIStreamRejectsIncompleteEOF(t *testing.T) {
	c, recorder := newGrokTestContext(http.MethodPost, "/test", nil)
	svc := &AccountTestService{}
	err := svc.processOpenAIStream(c, strings.NewReader(`data: {"type":"response.output_text.delta","delta":"hello"}`))
	if err == nil || !strings.Contains(err.Error(), "ended before completion") {
		t.Fatalf("processOpenAIStream error = %v, want incomplete stream error", err)
	}
	if !strings.Contains(recorder.Body.String(), `"type":"error"`) {
		t.Fatalf("incomplete stream should emit error event: %s", recorder.Body.String())
	}
}

func TestHandleClaudeCompatResponsesStreamAcceptsPayloadEventTypes(t *testing.T) {
	stream := strings.NewReader(
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
			"data: {\"type\":\"response.done\",\"response\":{\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n",
	)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(stream),
	}
	c, recorder := newGrokTestContext(http.MethodPost, "/v1/messages", nil)
	svc := &OpenAIGatewayService{}
	result, err := svc.handleClaudeCompatResponsesStream(resp, c, time.Now(), "grok-4.3", 0)
	if err != nil {
		t.Fatalf("handleClaudeCompatResponsesStream returned error: %v", err)
	}
	if result.inputTokens != 3 || result.outputTokens != 2 {
		t.Fatalf("unexpected result: %#v", result)
	}
	responseBody := recorder.Body.String()
	if !strings.Contains(responseBody, `"text_delta"`) || !strings.Contains(responseBody, `"hello"`) {
		t.Fatalf("Claude text delta missing: %s", responseBody)
	}
}

func TestForwardGrokChatCompletionsNonStreamingUsesJSONResponses(t *testing.T) {
	body := []byte(`{"model":"grok","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	c, _ := newGrokTestContext(http.MethodPost, "/v1/chat/completions", body)
	upstream := &grokTestUpstream{response: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"resp_grok","model":"grok-4.3","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":3,"output_tokens":2}}`,
		)),
	}}
	account := &Account{
		ID:          45,
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{"access_token": "grok-access", "base_url": "https://api.x.ai/v1"},
	}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	result, err := svc.ForwardChatCompletionsViaResponses(context.Background(), c, account, body, false)
	if err != nil {
		t.Fatalf("ForwardChatCompletionsViaResponses returned error: %v", err)
	}
	if result == nil || result.Usage.InputTokens != 3 || result.Usage.OutputTokens != 2 {
		t.Fatalf("unexpected result usage: %#v", result)
	}
	if got, want := upstream.request.Header.Get("Accept"), "application/json"; got != want {
		t.Fatalf("upstream Accept = %q, want %q", got, want)
	}
	if got, want := gjson.GetBytes(upstream.body, "stream").Bool(), false; got != want {
		t.Fatalf("upstream stream = %v, want %v", got, want)
	}
	if got, want := gjson.GetBytes(upstream.body, "model").String(), grokDefaultModel; got != want {
		t.Fatalf("upstream model = %q, want %q", got, want)
	}
}

func TestForwardGrokResponsesShapedChatRequestPreservesInput(t *testing.T) {
	body := []byte(`{"model":"grok","input":"hi","stream":false}`)
	c, _ := newGrokTestContext(http.MethodPost, "/v1/chat/completions", body)
	upstream := &grokTestUpstream{response: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"resp_grok","model":"grok-4.3","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":3,"output_tokens":2}}`,
		)),
	}}
	account := &Account{
		ID:          46,
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "grok-key", "base_url": "https://api.x.ai/v1"},
	}
	svc := &OpenAIGatewayService{httpUpstream: upstream}

	if _, err := svc.ForwardChatCompletionsViaResponses(context.Background(), c, account, body, false); err != nil {
		t.Fatalf("ForwardChatCompletionsViaResponses returned error: %v", err)
	}
	if got := gjson.GetBytes(upstream.body, "input").String(); got != "hi" {
		t.Fatalf("upstream input = %q, want hi", got)
	}
}
