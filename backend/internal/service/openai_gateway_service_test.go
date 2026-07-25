package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type stubOpenAIAccountRepo struct {
	AccountRepository
	accounts []Account
}

func (r stubOpenAIAccountRepo) ListByGroup(ctx context.Context, groupID int64) ([]Account, error) {
	return append([]Account(nil), r.accounts...), nil
}

func (r stubOpenAIAccountRepo) ListByPlatform(ctx context.Context, platform string) ([]Account, error) {
	out := make([]Account, 0, len(r.accounts))
	for _, account := range r.accounts {
		if account.Platform == platform {
			out = append(out, account)
		}
	}
	return out, nil
}

func (r stubOpenAIAccountRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]Account, error) {
	return append([]Account(nil), r.accounts...), nil
}

func (r stubOpenAIAccountRepo) ListSchedulableByPlatform(ctx context.Context, platform string) ([]Account, error) {
	return append([]Account(nil), r.accounts...), nil
}

type stubConcurrencyCache struct {
	ConcurrencyCache
}

type recordingOpenAIHTTPUpstream struct {
	calls int
}

func (s *recordingOpenAIHTTPUpstream) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	s.calls++
	return nil, errors.New("unexpected upstream call")
}

type openAICapacityAccountRepoStub struct {
	AccountRepository
	tempCalls  int
	lastUntil  time.Time
	lastReason string
}

func (r *openAICapacityAccountRepoStub) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	r.tempCalls++
	r.lastUntil = until
	r.lastReason = reason
	return nil
}

func (c stubConcurrencyCache) AcquireAccountSlot(ctx context.Context, accountID int64, maxConcurrency int, requestID string) (bool, error) {
	return true, nil
}

func (c stubConcurrencyCache) ReleaseAccountSlot(ctx context.Context, accountID int64, requestID string) error {
	return nil
}

func (c stubConcurrencyCache) GetAccountsLoadBatch(ctx context.Context, accounts []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error) {
	out := make(map[int64]*AccountLoadInfo, len(accounts))
	for _, acc := range accounts {
		out[acc.ID] = &AccountLoadInfo{AccountID: acc.ID, LoadRate: 0}
	}
	return out, nil
}

func TestOpenAIGatewayService_GenerateSessionHash_Priority(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)

	svc := &OpenAIGatewayService{}

	// 1) session_id header wins
	c.Request.Header.Set("session_id", "sess-123")
	c.Request.Header.Set("conversation_id", "conv-456")
	h1 := svc.GenerateSessionHash(c, map[string]any{"prompt_cache_key": "ses_aaa"})
	if h1 == "" {
		t.Fatalf("expected non-empty hash")
	}

	// 2) conversation_id used when session_id absent
	c.Request.Header.Del("session_id")
	h2 := svc.GenerateSessionHash(c, map[string]any{"prompt_cache_key": "ses_aaa"})
	if h2 == "" {
		t.Fatalf("expected non-empty hash")
	}
	if h1 == h2 {
		t.Fatalf("expected different hashes for different keys")
	}

	// 3) prompt_cache_key used when both headers absent
	c.Request.Header.Del("conversation_id")
	h3 := svc.GenerateSessionHash(c, map[string]any{"prompt_cache_key": "ses_aaa"})
	if h3 == "" {
		t.Fatalf("expected non-empty hash")
	}
	if h2 == h3 {
		t.Fatalf("expected different hashes for different keys")
	}

	// 4) empty when no signals
	h4 := svc.GenerateSessionHash(c, map[string]any{})
	if h4 != "" {
		t.Fatalf("expected empty hash when no signals")
	}
}

func TestOpenAIGatewayService_ForwardRejectsStatelessReasoningReferenceBeforeUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupCodexCache(t)

	reqBody := []byte(`{
		"model": "gpt-5.4",
		"stream": true,
		"tool_choice": "auto",
		"input": [
			{"type": "reasoning", "id": "rs_02b340e9f9ee714e016a04830017c08193a6ba36add422c726"},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]}
		]
	}`)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", bytes.NewReader(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &recordingOpenAIHTTPUpstream{}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{},
		httpUpstream: upstream,
	}

	account := &Account{
		ID:       1,
		Name:     "openai-oauth",
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
	}

	result, err := svc.Forward(context.Background(), c, account, reqBody)

	if err == nil {
		t.Fatalf("expected local validation error")
	}
	if result != nil {
		t.Fatalf("expected nil result")
	}
	if upstream.calls != 0 {
		t.Fatalf("upstream calls = %d, want 0", upstream.calls)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	var body map[string]map[string]any
	if unmarshalErr := json.Unmarshal(rec.Body.Bytes(), &body); unmarshalErr != nil {
		t.Fatalf("response json: %v", unmarshalErr)
	}
	errorBody := body["error"]
	if errorBody["type"] != "invalid_request_error" {
		t.Fatalf("error.type = %v, want invalid_request_error", errorBody["type"])
	}
	if errorBody["param"] != "input" {
		t.Fatalf("error.param = %v, want input", errorBody["param"])
	}
	message, _ := errorBody["message"].(string)
	if !strings.Contains(message, "rs_02b340e9f9ee714e016a04830017c08193a6ba36add422c726") {
		t.Fatalf("message = %q, want item id", message)
	}
}

func TestShouldGuardOpenAIStatelessReasoning(t *testing.T) {
	cases := []struct {
		name    string
		account *Account
		want    bool
	}{
		{
			name:    "oauth",
			account: &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth},
			want:    true,
		},
		{
			name:    "api_key_default_official",
			account: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
			want:    true,
		},
		{
			name: "api_key_official_base_url",
			account: &Account{
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Credentials: map[string]any{"base_url": "https://api.openai.com/v1"},
			},
			want: true,
		},
		{
			name: "api_key_custom_compatible_base_url",
			account: &Account{
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Credentials: map[string]any{"base_url": "https://oneapi.example.com/v1"},
			},
			want: false,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldGuardOpenAIStatelessReasoning(tt.account); got != tt.want {
				t.Fatalf("shouldGuardOpenAIStatelessReasoning() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOpenAISelectAccountWithLoadAwareness_FiltersUnschedulable(t *testing.T) {
	now := time.Now()
	resetAt := now.Add(10 * time.Minute)
	groupID := int64(1)

	rateLimited := Account{
		ID:               1,
		Platform:         PlatformOpenAI,
		Type:             AccountTypeAPIKey,
		Status:           StatusActive,
		Schedulable:      true,
		Concurrency:      1,
		Priority:         0,
		RateLimitResetAt: &resetAt,
	}
	available := Account{
		ID:          2,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Priority:    1,
	}

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: []Account{rateLimited, available}},
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
	}

	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "", "gpt-5.2", nil)
	if err != nil {
		t.Fatalf("SelectAccountWithLoadAwareness error: %v", err)
	}
	if selection == nil || selection.Account == nil {
		t.Fatalf("expected selection with account")
	}
	if selection.Account.ID != available.ID {
		t.Fatalf("expected account %d, got %d", available.ID, selection.Account.ID)
	}
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAISelectAccountWithLoadAwareness_FiltersUnschedulableWhenNoConcurrencyService(t *testing.T) {
	now := time.Now()
	resetAt := now.Add(10 * time.Minute)
	groupID := int64(1)

	rateLimited := Account{
		ID:               1,
		Platform:         PlatformOpenAI,
		Type:             AccountTypeAPIKey,
		Status:           StatusActive,
		Schedulable:      true,
		Concurrency:      1,
		Priority:         0,
		RateLimitResetAt: &resetAt,
	}
	available := Account{
		ID:          2,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Priority:    1,
	}

	svc := &OpenAIGatewayService{
		accountRepo: stubOpenAIAccountRepo{accounts: []Account{rateLimited, available}},
		// concurrencyService is nil, forcing the non-load-batch selection path.
	}

	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "", "gpt-5.2", nil)
	if err != nil {
		t.Fatalf("SelectAccountWithLoadAwareness error: %v", err)
	}
	if selection == nil || selection.Account == nil {
		t.Fatalf("expected selection with account")
	}
	if selection.Account.ID != available.ID {
		t.Fatalf("expected account %d, got %d", available.ID, selection.Account.ID)
	}
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAISelectAccountWithLoadAwareness_ModelUnsupportedReturnsSentinelWithoutConcurrencyService(t *testing.T) {
	groupID := int64(1)
	account := Account{
		ID:          1,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-4o": "gpt-4o"},
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo: stubOpenAIAccountRepo{accounts: []Account{account}},
	}

	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "", "gemini-2.5-flash", nil)
	if selection != nil {
		t.Fatalf("expected nil selection, got %#v", selection)
	}
	if !errors.Is(err, ErrModelNotSupported) {
		t.Fatalf("expected ErrModelNotSupported, got %v", err)
	}
}

func TestOpenAISelectAccountWithLoadAwareness_ModelUnsupportedReturnsSentinelWithLoadBatch(t *testing.T) {
	groupID := int64(1)
	account := Account{
		ID:          1,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-4o": "gpt-4o"},
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: []Account{account}},
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
	}

	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "", "gemini-2.5-flash", nil)
	if selection != nil {
		t.Fatalf("expected nil selection, got %#v", selection)
	}
	if !errors.Is(err, ErrModelNotSupported) {
		t.Fatalf("expected ErrModelNotSupported, got %v", err)
	}
}

func TestOpenAISelectAccountWithLoadAwareness_SupportedButUnschedulableDoesNotReturnModelSentinel(t *testing.T) {
	groupID := int64(1)
	account := Account{
		ID:          1,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: false,
		Concurrency: 1,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-4o": "gpt-4o"},
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo: stubOpenAIAccountRepo{accounts: []Account{account}},
	}

	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "", "gpt-4o", nil)
	if selection != nil {
		t.Fatalf("expected nil selection, got %#v", selection)
	}
	if err == nil {
		t.Fatalf("expected generic no available error")
	}
	if errors.Is(err, ErrModelNotSupported) {
		t.Fatalf("did not expect ErrModelNotSupported for supported but unschedulable account: %v", err)
	}
}

func TestOpenAIStreamingTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			StreamDataIntervalTimeout: 1,
			StreamKeepaliveInterval:   0,
			MaxLineSize:               defaultMaxLineSize,
		},
	}
	svc := &OpenAIGatewayService{cfg: cfg}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       pr,
		Header:     http.Header{},
	}

	start := time.Now()
	_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 1}, start, "model", "model", "")
	_ = pw.Close()
	_ = pr.Close()

	if err == nil || !strings.Contains(err.Error(), "stream data interval timeout") {
		t.Fatalf("expected stream timeout error, got %v", err)
	}
	if !strings.Contains(rec.Body.String(), "stream_timeout") {
		t.Fatalf("expected stream_timeout SSE error, got %q", rec.Body.String())
	}
}

func TestOpenAIStreamingTooLong(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			StreamDataIntervalTimeout: 0,
			StreamKeepaliveInterval:   0,
			MaxLineSize:               64 * 1024,
		},
	}
	svc := &OpenAIGatewayService{cfg: cfg}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       pr,
		Header:     http.Header{},
	}

	go func() {
		defer func() { _ = pw.Close() }()
		// 写入超过 MaxLineSize 的单行数据，触发 ErrTooLong
		payload := "data: " + strings.Repeat("a", 128*1024) + "\n"
		_, _ = pw.Write([]byte(payload))
	}()

	_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 2}, time.Now(), "model", "model", "")
	_ = pr.Close()

	if !errors.Is(err, bufio.ErrTooLong) {
		t.Fatalf("expected ErrTooLong, got %v", err)
	}
	if !strings.Contains(rec.Body.String(), "response_too_large") {
		t.Fatalf("expected response_too_large SSE error, got %q", rec.Body.String())
	}
}

func TestOpenAINonStreamingContentTypePassThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		Security: config.SecurityConfig{
			ResponseHeaders: config.ResponseHeaderConfig{Enabled: false},
		},
	}
	svc := &OpenAIGatewayService{cfg: cfg}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	body := []byte(`{"usage":{"input_tokens":1,"output_tokens":2,"input_tokens_details":{"cached_tokens":0}}}`)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/vnd.test+json"}},
	}

	_, err := svc.handleNonStreamingResponse(c.Request.Context(), resp, c, &Account{}, "model", "model", "")
	if err != nil {
		t.Fatalf("handleNonStreamingResponse error: %v", err)
	}

	if !strings.Contains(rec.Header().Get("Content-Type"), "application/vnd.test+json") {
		t.Fatalf("expected Content-Type passthrough, got %q", rec.Header().Get("Content-Type"))
	}
}

func TestOpenAINonStreamingContentTypeDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		Security: config.SecurityConfig{
			ResponseHeaders: config.ResponseHeaderConfig{Enabled: false},
		},
	}
	svc := &OpenAIGatewayService{cfg: cfg}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	body := []byte(`{"usage":{"input_tokens":1,"output_tokens":2,"input_tokens_details":{"cached_tokens":0}}}`)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     http.Header{},
	}

	_, err := svc.handleNonStreamingResponse(c.Request.Context(), resp, c, &Account{}, "model", "model", "")
	if err != nil {
		t.Fatalf("handleNonStreamingResponse error: %v", err)
	}

	if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("expected default Content-Type, got %q", rec.Header().Get("Content-Type"))
	}
}

func TestOpenAINonStreamingResponse_StripsInjectedInstructionsAndNormalizesModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{cfg: &config.Config{}}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	injectedInstructions := GetCodexCLIInstructions()
	body := []byte(`{"id":"resp_1","model":"gpt-5.2-2025-12-11","instructions":` + strconv.Quote(injectedInstructions) + `,"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":11,"output_tokens":5,"input_tokens_details":{"cached_tokens":7}}}`)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}

	usage, err := svc.handleNonStreamingResponse(c.Request.Context(), resp, c, &Account{Type: AccountTypeOAuth}, "gpt-5.1", "gpt-5.2", injectedInstructions)
	if err != nil {
		t.Fatalf("handleNonStreamingResponse error: %v", err)
	}
	if usage == nil || usage.InputTokens != 11 || usage.OutputTokens != 5 || usage.CacheReadInputTokens != 7 {
		t.Fatalf("unexpected usage: %+v", usage)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if _, exists := got["instructions"]; exists {
		t.Fatalf("expected injected instructions to be stripped, got %v", got["instructions"])
	}
	if model, _ := got["model"].(string); model != "gpt-5.2" {
		t.Fatalf("expected normalized model gpt-5.2, got %q", model)
	}
}

func TestOpenAIHandleOAuthSSEToJSON_RebuildsMissingOutputAndStripsInjectedInstructions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{cfg: &config.Config{}}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	injectedInstructions := GetCodexCLIInstructions()
	instructionsJSON, err := json.Marshal(injectedInstructions)
	if err != nil {
		t.Fatalf("marshal instructions: %v", err)
	}

	body := []byte("event: response.output_item.done\n" +
		"data: " + `{"type":"response.output_item.done","item":{"id":"msg_1","type":"message","status":"completed","content":[{"type":"output_text","text":"OK"}],"role":"assistant"},"output_index":0}` + "\n\n" +
		"event: response.completed\n" +
		"data: " + `{"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.2-2025-12-11","instructions":` + string(instructionsJSON) + `,"output":[],"usage":{"input_tokens":11,"output_tokens":5,"input_tokens_details":{"cached_tokens":7}}}}` + "\n")

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	usage, err := svc.handleOAuthSSEToJSON(context.Background(), resp, c, &Account{Type: AccountTypeOAuth}, body, "gpt-5.1", "gpt-5.2", injectedInstructions)
	if err != nil {
		t.Fatalf("handleOAuthSSEToJSON error: %v", err)
	}
	if usage == nil || usage.InputTokens != 11 || usage.OutputTokens != 5 || usage.CacheReadInputTokens != 7 {
		t.Fatalf("unexpected usage: %+v", usage)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if _, exists := got["instructions"]; exists {
		t.Fatalf("expected injected instructions to be stripped, got %v", got["instructions"])
	}
	if model, _ := got["model"].(string); model != "gpt-5.2" {
		t.Fatalf("expected normalized model gpt-5.2, got %q", model)
	}
	output, _ := got["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("expected rebuilt output item, got %v", got["output"])
	}
	item, _ := output[0].(map[string]any)
	content, _ := item["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected one content part, got %v", item["content"])
	}
	part, _ := content[0].(map[string]any)
	if text, _ := part["text"].(string); text != "OK" {
		t.Fatalf("expected rebuilt text OK, got %q", text)
	}
}

func TestOpenAIHandleCCViaResponsesNonStreamingResponse_RebuildsMissingOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	body := []byte("event: response.output_item.done\n" +
		"data: " + `{"type":"response.output_item.done","item":{"id":"msg_1","type":"message","status":"completed","content":[{"type":"output_text","text":"OK"}],"role":"assistant"},"output_index":0}` + "\n\n" +
		"event: response.completed\n" +
		"data: " + `{"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","output":[],"usage":{"input_tokens":11,"output_tokens":5,"input_tokens_details":{"cached_tokens":7}}}}` + "\n")

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	usage, err := svc.handleCCViaResponsesNonStreamingResponse(context.Background(), resp, c, &Account{Type: AccountTypeOAuth}, "gpt-5.2", "")
	if err != nil {
		t.Fatalf("handleCCViaResponsesNonStreamingResponse error: %v", err)
	}
	if usage == nil || usage.InputTokens != 11 || usage.OutputTokens != 5 || usage.CacheReadInputTokens != 7 {
		t.Fatalf("unexpected usage: %+v", usage)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal CC response: %v", err)
	}

	choices, _ := got["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("expected one choice, got %v", got["choices"])
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if content, _ := message["content"].(string); content != "OK" {
		t.Fatalf("expected message content OK, got %q", content)
	}
	if model, _ := got["model"].(string); model != "gpt-5.2" {
		t.Fatalf("expected visible mapped model gpt-5.2, got %q", model)
	}
}

func TestOpenAIStreamingResponse_NormalizesSnapshotModelToStableAlias(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			StreamDataIntervalTimeout: 0,
			StreamKeepaliveInterval:   0,
			MaxLineSize:               defaultMaxLineSize,
		},
	}
	svc := &OpenAIGatewayService{cfg: cfg}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	payload := "event: response.created\n" +
		"data: " + `{"type":"response.created","response":{"id":"resp_1","model":"gpt-5.2-2025-12-11"}}` + "\n\n" +
		"event: response.completed\n" +
		"data: " + `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.2-2025-12-11","usage":{"input_tokens":11,"output_tokens":5}}}` + "\n\n"
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(payload)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{Type: AccountTypeOAuth}, time.Now(), "gpt-5.1", "gpt-5.2", "")
	if err != nil {
		t.Fatalf("handleStreamingResponse error: %v", err)
	}

	body := rec.Body.String()
	if strings.Contains(body, "gpt-5.2-2025-12-11") {
		t.Fatalf("expected snapshot model to be normalized, got %q", body)
	}
	if !strings.Contains(body, `"model":"gpt-5.2"`) {
		t.Fatalf("expected stable model alias in stream, got %q", body)
	}
}

func TestOpenAIStreamingResponse_StripsInjectedInstructionsAndNormalizesModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			StreamDataIntervalTimeout: 0,
			StreamKeepaliveInterval:   0,
			MaxLineSize:               defaultMaxLineSize,
		},
	}
	svc := &OpenAIGatewayService{cfg: cfg}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	injectedInstructions := GetCodexCLIInstructions()
	instructionsJSON, err := json.Marshal(injectedInstructions)
	if err != nil {
		t.Fatalf("marshal instructions: %v", err)
	}

	payload := "event: response.created\n" +
		"data: " + `{"type":"response.created","response":{"id":"resp_1","model":"gpt-5.2-2025-12-11","instructions":` + string(instructionsJSON) + `}}` + "\n\n" +
		"event: response.completed\n" +
		"data: " + `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.2-2025-12-11","instructions":` + string(instructionsJSON) + `,"usage":{"input_tokens":11,"output_tokens":5}}}` + "\n\n"
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(payload)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}

	_, err = svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{Type: AccountTypeOAuth}, time.Now(), "gpt-5.1", "gpt-5.2", injectedInstructions)
	if err != nil {
		t.Fatalf("handleStreamingResponse error: %v", err)
	}

	body := rec.Body.String()
	if strings.Contains(body, injectedInstructions) {
		t.Fatalf("expected injected instructions to be stripped, got %q", body)
	}
	if strings.Contains(body, `"instructions"`) {
		t.Fatalf("expected instructions field to be stripped, got %q", body)
	}
	if strings.Contains(body, "gpt-5.2-2025-12-11") {
		t.Fatalf("expected snapshot model to be normalized, got %q", body)
	}
	if !strings.Contains(body, `"model":"gpt-5.2"`) {
		t.Fatalf("expected stable model alias in stream, got %q", body)
	}
}

func TestOpenAIResponseModelMatches_DoesNotConfuseFutureVersionWithGpt51(t *testing.T) {
	if openAIResponseModelMatches("gpt-5.10-2026-01-01", "gpt-5.2") {
		t.Fatalf("did not expect gpt-5.10 snapshot to match gpt-5.2")
	}
	if normalized := normalizeOpenAIResponseModel("gpt-5.10-2026-01-01"); normalized != "" {
		t.Fatalf("expected no normalization for unknown future version, got %q", normalized)
	}
}

func TestOpenAIResponseModelMatches_NormalizesGPT55Snapshot(t *testing.T) {
	if !openAIResponseModelMatches("gpt-5.5-2026-04-24", "gpt-5.5") {
		t.Fatalf("expected gpt-5.5 snapshot to match stable alias")
	}
	if normalized := normalizeOpenAIResponseModel("gpt-5.5-2026-04-24"); normalized != "gpt-5.5" {
		t.Fatalf("expected gpt-5.5 snapshot to normalize, got %q", normalized)
	}
}

func TestOpenAIResponseModelMatches_NormalizesGPT56Models(t *testing.T) {
	tests := []struct {
		actual string
		want   string
	}{
		{actual: "gpt-5.6", want: "gpt-5.6"},
		{actual: "gpt-5.6-sol", want: "gpt-5.6"},
		{actual: "gpt-5.6", want: "gpt-5.6-sol"},
		{actual: "gpt-5.6-sol-2026-07-09", want: "gpt-5.6-sol"},
		{actual: "gpt-5.6-terra-2026-07-09", want: "gpt-5.6-terra"},
		{actual: "gpt-5.6-luna-2026-07-09", want: "gpt-5.6-luna"},
	}

	for _, tt := range tests {
		t.Run(tt.actual, func(t *testing.T) {
			if !openAIResponseModelMatches(tt.actual, tt.want) {
				t.Fatalf("expected %q to match %q", tt.actual, tt.want)
			}
			if tt.actual == "gpt-5.6-sol" && tt.want == "gpt-5.6" {
				return
			}
			if tt.actual == "gpt-5.6" && tt.want == "gpt-5.6-sol" {
				return
			}
			if normalized := normalizeOpenAIResponseModel(tt.actual); normalized != tt.want {
				t.Fatalf("normalized model = %q, want %q", normalized, tt.want)
			}
		})
	}
}

func TestOpenAIResponseModelMatches_NormalizesCurrentOfficialCodexModels(t *testing.T) {
	tests := []struct {
		actual string
		want   string
	}{
		{actual: "gpt-5.4-mini-2026-04-24", want: "gpt-5.4-mini"},
		{actual: "gpt-5.3-codex-spark-2026-04-24", want: "gpt-5.3-codex-spark"},
	}

	for _, tt := range tests {
		t.Run(tt.actual, func(t *testing.T) {
			if !openAIResponseModelMatches(tt.actual, tt.want) {
				t.Fatalf("expected %q to match %q", tt.actual, tt.want)
			}
			if normalized := normalizeOpenAIResponseModel(tt.actual); normalized != tt.want {
				t.Fatalf("normalized model = %q, want %q", normalized, tt.want)
			}
		})
	}
}

func TestClientVisibleOpenAIModel_ExposesGPT54MiniRemap(t *testing.T) {
	if got := clientVisibleOpenAIModel("gpt-5.2", "gpt-5.4-mini"); got != "gpt-5.4-mini" {
		t.Fatalf("visible model = %q, want gpt-5.4-mini", got)
	}
	if got := clientVisibleOpenAIModel("gpt-5.3-codex", "gpt-5.4-mini"); got != "gpt-5.4-mini" {
		t.Fatalf("visible model = %q, want gpt-5.4-mini", got)
	}
}

func TestOpenAIStreamingHeadersOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		Security: config.SecurityConfig{
			ResponseHeaders: config.ResponseHeaderConfig{Enabled: false},
		},
		Gateway: config.GatewayConfig{
			StreamDataIntervalTimeout: 0,
			StreamKeepaliveInterval:   0,
			MaxLineSize:               defaultMaxLineSize,
		},
	}
	svc := &OpenAIGatewayService{cfg: cfg}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       pr,
		Header: http.Header{
			"Cache-Control": []string{"upstream"},
			"X-Request-Id":  []string{"req-123"},
			"Content-Type":  []string{"application/custom"},
		},
	}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("data: {}\n\n"))
	}()

	_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, &Account{ID: 1}, time.Now(), "model", "model", "")
	_ = pr.Close()
	if err != nil {
		t.Fatalf("handleStreamingResponse error: %v", err)
	}

	if rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("expected Cache-Control override, got %q", rec.Header().Get("Cache-Control"))
	}
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("expected Content-Type override, got %q", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("X-Request-Id") != "req-123" {
		t.Fatalf("expected X-Request-Id passthrough, got %q", rec.Header().Get("X-Request-Id"))
	}
}

func TestRateLimitService_HandleOpenAICapacityError_SetsTempUnschedulable(t *testing.T) {
	repo := &openAICapacityAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       301,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
	}

	start := time.Now()
	ok := service.HandleOpenAICapacityError(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		[]byte(`{"response":{"error":{"message":"Selected model is at capacity. Please try a different model."}}}`),
	)

	if !ok {
		t.Fatalf("expected capacity error to be handled")
	}
	if repo.tempCalls != 1 {
		t.Fatalf("expected temp unschedulable to be set once, got %d", repo.tempCalls)
	}
	minUntil := start.Add(openAICapacityCooldown - 2*time.Second)
	maxUntil := start.Add(openAICapacityCooldown + 2*time.Second)
	if repo.lastUntil.Before(minUntil) || repo.lastUntil.After(maxUntil) {
		t.Fatalf("unexpected cooldown window: got %v, want around %v", repo.lastUntil, start.Add(openAICapacityCooldown))
	}
	if !strings.Contains(repo.lastReason, "openai_model_capacity") {
		t.Fatalf("expected structured temp unsched reason, got %q", repo.lastReason)
	}
}

func TestOpenAIStreamingResponse_PreReadsCapacityAndFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			StreamDataIntervalTimeout: 1,
			StreamKeepaliveInterval:   0,
			MaxLineSize:               defaultMaxLineSize,
		},
	}
	repo := &openAICapacityAccountRepoStub{}
	rateLimitService := NewRateLimitService(repo, nil, cfg, nil, nil)
	svc := &OpenAIGatewayService{
		cfg:              cfg,
		rateLimitService: rateLimitService,
		toolCorrector:    NewCodexToolCorrector(),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	pr, pw := io.Pipe()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       pr,
		Header:     http.Header{"X-Request-Id": []string{"req-capacity"}},
	}

	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write([]byte("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"Selected model is at capacity. Please try a different model.\"}}}\n\n"))
	}()

	account := &Account{ID: 302, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	_, err := svc.handleStreamingResponse(c.Request.Context(), resp, c, account, time.Now(), "gpt-5.4", "gpt-5.4", "")
	_ = pr.Close()

	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) {
		t.Fatalf("expected UpstreamFailoverError, got %v", err)
	}
	if failoverErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected failover status 429, got %d", failoverErr.StatusCode)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected no client-visible body before failover, got %q", rec.Body.String())
	}
	if repo.tempCalls != 1 {
		t.Fatalf("expected capacity cooldown to be recorded once, got %d", repo.tempCalls)
	}
}

func TestOpenAIHandleOAuthSSEToJSON_CapacityTriggersFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	repo := &openAICapacityAccountRepoStub{}
	rateLimitService := NewRateLimitService(repo, nil, cfg, nil, nil)
	svc := &OpenAIGatewayService{
		cfg:              cfg,
		rateLimitService: rateLimitService,
		toolCorrector:    NewCodexToolCorrector(),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"X-Request-Id": []string{"req-capacity-json"}},
	}
	account := &Account{ID: 303, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	body := []byte("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"Selected model is at capacity. Please try a different model.\"}}}\n\n")

	_, err := svc.handleOAuthSSEToJSON(context.Background(), resp, c, account, body, "gpt-5.4", "gpt-5.4", "")

	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) {
		t.Fatalf("expected UpstreamFailoverError, got %v", err)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected no response body before failover, got %q", rec.Body.String())
	}
	if repo.tempCalls != 1 {
		t.Fatalf("expected capacity cooldown to be recorded once, got %d", repo.tempCalls)
	}
}

func TestOpenAIInvalidBaseURLWhenAllowlistDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{Enabled: false},
		},
	}
	svc := &OpenAIGatewayService{cfg: cfg}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	account := &Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "://invalid-url"},
	}

	_, err := svc.buildUpstreamRequest(c.Request.Context(), c, account, []byte("{}"), "token", false, "", false)
	if err == nil {
		t.Fatalf("expected error for invalid base_url when allowlist disabled")
	}
}

func TestOpenAIBuildUpstreamRequestAppliesAccountHeaderOverrides(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	c.Request.Header.Set("User-Agent", "client-agent")

	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":                       "test-key",
			credentialHeaderOverrideEnabled: true,
			credentialHeaderOverrides: map[string]any{
				"user-agent":    "account-agent",
				"openai-beta":   "responses=experimental",
				"authorization": "Bearer attacker",
			},
		},
	}

	svc := &OpenAIGatewayService{cfg: &config.Config{
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{Enabled: false},
		},
	}}
	req, err := svc.buildUpstreamRequest(
		c.Request.Context(), c, account, []byte("{}"), "real-token", false, "", false,
	)
	require.NoError(t, err)
	require.Equal(t, "Bearer real-token", req.Header.Get("Authorization"))
	require.Equal(t, "account-agent", req.Header.Get("User-Agent"))
	require.Equal(t, "responses=experimental", req.Header.Get("OpenAI-Beta"))
}

func TestOpenAIValidateUpstreamBaseURLDisabledRequiresHTTPS(t *testing.T) {
	cfg := &config.Config{
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{Enabled: false},
		},
	}
	svc := &OpenAIGatewayService{cfg: cfg}

	if _, err := svc.validateUpstreamBaseURL("http://not-https.example.com"); err == nil {
		t.Fatalf("expected http to be rejected when allow_insecure_http is false")
	}
	normalized, err := svc.validateUpstreamBaseURL("https://example.com")
	if err != nil {
		t.Fatalf("expected https to be allowed when allowlist disabled, got %v", err)
	}
	if normalized != "https://example.com" {
		t.Fatalf("expected raw url passthrough, got %q", normalized)
	}
}

func TestOpenAIValidateUpstreamBaseURLDisabledAllowsHTTP(t *testing.T) {
	cfg := &config.Config{
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{
				Enabled:           false,
				AllowInsecureHTTP: true,
			},
		},
	}
	svc := &OpenAIGatewayService{cfg: cfg}

	normalized, err := svc.validateUpstreamBaseURL("http://not-https.example.com")
	if err != nil {
		t.Fatalf("expected http allowed when allow_insecure_http is true, got %v", err)
	}
	if normalized != "http://not-https.example.com" {
		t.Fatalf("expected raw url passthrough, got %q", normalized)
	}
}

func TestOpenAIValidateUpstreamBaseURLEnabledEnforcesAllowlist(t *testing.T) {
	cfg := &config.Config{
		Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{
				Enabled:       true,
				UpstreamHosts: []string{"example.com"},
			},
		},
	}
	svc := &OpenAIGatewayService{cfg: cfg}

	if _, err := svc.validateUpstreamBaseURL("https://example.com"); err != nil {
		t.Fatalf("expected allowlisted host to pass, got %v", err)
	}
	if _, err := svc.validateUpstreamBaseURL("https://evil.com"); err == nil {
		t.Fatalf("expected non-allowlisted host to fail")
	}
}
