package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type kiroAPIKeyProtocolUpstream struct {
	requestURL     string
	requestHeader  http.Header
	requestBody    []byte
	responseBody   string
	responseReader io.ReadCloser
	responseHeader http.Header
}

type kiroAPIKeyScriptedResponse struct {
	statusCode int
	body       string
	header     http.Header
}

type kiroAPIKeyScriptedUpstream struct {
	responses      []kiroAPIKeyScriptedResponse
	requestBodies  [][]byte
	requestHeaders []http.Header
}

func (u *kiroAPIKeyScriptedUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	responseIndex := len(u.requestBodies)
	u.requestBodies = append(u.requestBodies, body)
	u.requestHeaders = append(u.requestHeaders, req.Header.Clone())
	if responseIndex >= len(u.responses) {
		return nil, io.ErrUnexpectedEOF
	}
	response := u.responses[responseIndex]
	return &http.Response{
		StatusCode: response.statusCode,
		Body:       io.NopCloser(strings.NewReader(response.body)),
		Header:     response.header.Clone(),
	}, nil
}

func (u *kiroAPIKeyProtocolUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	u.requestURL = req.URL.String()
	u.requestHeader = req.Header.Clone()
	u.requestBody = body
	responseBody := u.responseReader
	if responseBody == nil {
		responseBody = io.NopCloser(strings.NewReader(u.responseBody))
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       responseBody,
		Header:     u.responseHeader.Clone(),
	}, nil
}

func newKiroAPIKeyProtocolService(upstream HTTPUpstream) *KiroGatewayService {
	return &KiroGatewayService{
		httpUpstream:  upstream,
		tokenProvider: NewKiroTokenProvider(nil, upstream),
	}
}

func newKiroAPIKeyProtocolAccount() *Account {
	return &Account{
		ID:       91,
		Name:     "kiro-apikey",
		Platform: PlatformKiro,
		Credentials: map[string]any{
			"auth_type": KiroAuthMethodAPIKey,
			"api_key":   "kiro-secret",
			"base_url":  "https://kiro.example/custom/",
		},
	}
}

func newOpenAIKiroTestContext(body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	return c, recorder
}

func TestKiroAPIKeyGPTChatCompletionsUsesAnthropicMessagesProtocol(t *testing.T) {
	upstream := &kiroAPIKeyProtocolUpstream{
		responseBody:   `{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`,
		responseHeader: make(http.Header),
	}
	svc := newKiroAPIKeyProtocolService(upstream)
	account := newKiroAPIKeyProtocolAccount()
	body := []byte(`{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hi"}]}`)
	c, recorder := newOpenAIKiroTestContext(body)

	result, err := svc.ForwardChatCompletions(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "https://kiro.example/custom/v1/messages", upstream.requestURL)
	require.Equal(t, "Bearer kiro-secret", upstream.requestHeader.Get("Authorization"))
	require.Equal(t, "kiro-secret", upstream.requestHeader.Get("x-api-key"))
	require.Equal(t, "2023-06-01", upstream.requestHeader.Get("anthropic-version"))
	require.Empty(t, upstream.requestHeader.Get("X-Amz-Target"))

	var upstreamPayload map[string]any
	require.NoError(t, json.Unmarshal(upstream.requestBody, &upstreamPayload))
	require.Equal(t, "gpt-5.6-sol", upstreamPayload["model"])
	require.Equal(t, float64(defaultKiroAPIKeyOpenAIMaxTokens), upstreamPayload["max_tokens"])
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"object":"chat.completion"`)
	require.Contains(t, recorder.Body.String(), `"content":"ok"`)
}

func TestKiroAPIKeyOpenAIThinkingUsesLargerDefaultMaxTokens(t *testing.T) {
	upstream := &kiroAPIKeyProtocolUpstream{
		responseBody:   `{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`,
		responseHeader: make(http.Header),
	}
	svc := newKiroAPIKeyProtocolService(upstream)
	body := []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":20000}}`)
	c, _ := newOpenAIKiroTestContext(body)

	result, err := svc.ForwardChatCompletions(context.Background(), c, newKiroAPIKeyProtocolAccount(), body)
	require.NoError(t, err)
	require.NotNil(t, result)

	var upstreamPayload map[string]any
	require.NoError(t, json.Unmarshal(upstream.requestBody, &upstreamPayload))
	require.Equal(t, float64(kiro.KiroFixedMaxTokens), upstreamPayload["max_tokens"])
}

func TestKiroAPIKeyOpenAISignatureErrorRetriesWithThinkingDisabled(t *testing.T) {
	upstream := &kiroAPIKeyScriptedUpstream{
		responses: []kiroAPIKeyScriptedResponse{
			{
				statusCode: http.StatusBadRequest,
				body:       `{"type":"error","error":{"type":"invalid_request_error","message":"Invalid signature in thinking block"}}`,
				header:     make(http.Header),
			},
			{
				statusCode: http.StatusOK,
				body:       `{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`,
				header:     make(http.Header),
			},
		},
	}
	svc := newKiroAPIKeyProtocolService(upstream)
	body := []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":20000}}`)
	c, recorder := newOpenAIKiroTestContext(body)

	result, err := svc.ForwardChatCompletions(context.Background(), c, newKiroAPIKeyProtocolAccount(), body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.requestBodies, 2)
	require.Contains(t, string(upstream.requestBodies[0]), `"thinking"`)
	require.NotContains(t, string(upstream.requestBodies[1]), `"thinking"`)
	require.Equal(t, "1", upstream.requestHeaders[1].Get("X-Stainless-Retry-Count"))
	require.Contains(t, recorder.Body.String(), `"content":"ok"`)
}

func TestKiroAPIKeyOpenAIToolSignatureErrorUsesSecondStageRetry(t *testing.T) {
	upstream := &kiroAPIKeyScriptedUpstream{
		responses: []kiroAPIKeyScriptedResponse{
			{
				statusCode: http.StatusBadRequest,
				body:       `{"type":"error","error":{"type":"invalid_request_error","message":"Invalid signature in thinking block"}}`,
				header:     make(http.Header),
			},
			{
				statusCode: http.StatusBadRequest,
				body:       `{"type":"error","error":{"type":"invalid_request_error","message":"Expected thinking block before tool_use because of invalid signature"}}`,
				header:     make(http.Header),
			},
			{
				statusCode: http.StatusOK,
				body:       `{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`,
				header:     make(http.Header),
			},
		},
	}
	svc := newKiroAPIKeyProtocolService(upstream)
	body := []byte(`{
		"model":"claude-opus-4-8",
		"messages":[
			{"role":"user","content":"run it"},
			{"role":"assistant","content":"","tool_calls":[{"id":"toolu_1","type":"function","function":{"name":"shell","arguments":"{\"command\":\"pwd\"}"}}]},
			{"role":"tool","tool_call_id":"toolu_1","content":"/tmp"}
		],
		"thinking":{"type":"enabled","budget_tokens":20000}
	}`)
	c, recorder := newOpenAIKiroTestContext(body)

	result, err := svc.ForwardChatCompletions(context.Background(), c, newKiroAPIKeyProtocolAccount(), body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.requestBodies, 3)
	require.Contains(t, string(upstream.requestBodies[1]), `"tool_use"`)
	require.NotContains(t, string(upstream.requestBodies[2]), `"tool_use"`)
	require.NotContains(t, string(upstream.requestBodies[2]), `"tool_result"`)
	require.Equal(t, "2", upstream.requestHeaders[2].Get("X-Stainless-Retry-Count"))
	require.Contains(t, recorder.Body.String(), `"content":"ok"`)
}

func TestKiroAPIKeyOpenAIRequestAppliesJitter(t *testing.T) {
	upstream := &kiroAPIKeyProtocolUpstream{
		responseBody:   `{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`,
		responseHeader: make(http.Header),
	}
	svc := newKiroAPIKeyProtocolService(upstream)
	svc.cfg = &config.Config{
		Gateway: config.GatewayConfig{
			RequestJitterMinMs: 30,
			RequestJitterMaxMs: 30,
		},
	}
	body := []byte(`{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hi"}]}`)
	c, _ := newOpenAIKiroTestContext(body)

	start := time.Now()
	result, err := svc.ForwardChatCompletions(context.Background(), c, newKiroAPIKeyProtocolAccount(), body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.GreaterOrEqual(t, time.Since(start), 25*time.Millisecond)
}

func TestKiroAPIKeyUnauthorizedDoesNotInvalidateOAuthTokenState(t *testing.T) {
	provider := NewKiroTokenProvider(nil, nil)
	svc := &KiroGatewayService{tokenProvider: provider}
	account := newKiroAPIKeyProtocolAccount()

	svc.handleUpstreamError(
		context.Background(),
		"[kiro-apikey-test]",
		account,
		http.StatusUnauthorized,
		make(http.Header),
		[]byte(`{"type":"error","error":{"message":"invalid api key"}}`),
	)

	_, exists := provider.cache.Load(account.ID)
	require.False(t, exists)
}

func TestKiroAPIKeyClaudeSSEConvertsToOpenAISSE(t *testing.T) {
	upstream := &kiroAPIKeyProtocolUpstream{
		responseBody: strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"usage":{"input_tokens":4}}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
			``,
			`event: content_block_stop`,
			`data: {"type":"content_block_stop","index":0}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
			``,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n"),
		responseHeader: make(http.Header),
	}
	svc := newKiroAPIKeyProtocolService(upstream)
	body := []byte(`{"model":"gpt-5.6-terra","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	c, recorder := newOpenAIKiroTestContext(body)

	result, err := svc.ForwardChatCompletions(context.Background(), c, newKiroAPIKeyProtocolAccount(), body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "https://kiro.example/custom/v1/messages", upstream.requestURL)
	require.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
	require.Contains(t, recorder.Body.String(), `"object":"chat.completion.chunk"`)
	require.Contains(t, recorder.Body.String(), `"content":"hello"`)
	require.Contains(t, recorder.Body.String(), `"finish_reason":"stop"`)
	require.Contains(t, recorder.Body.String(), "data: [DONE]")
}

func TestKiroAPIKeyClaudeStreamCommitsKeepaliveBeforeUpstreamBody(t *testing.T) {
	streamBody := newGatedReadCloser([]byte(strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":4}}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")))
	defer streamBody.Close()
	upstream := &kiroAPIKeyProtocolUpstream{
		responseReader: streamBody,
		responseHeader: make(http.Header),
	}
	svc := newKiroAPIKeyProtocolService(upstream)
	body := []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	claudeReq, err := kiro.ParseClaudeRequestFromJSON(body)
	require.NoError(t, err)
	c, recorder := newKiroStreamTestContext()

	type forwardResult struct {
		result *ForwardResult
		err    error
	}
	done := make(chan forwardResult, 1)
	go func() {
		result, forwardErr := svc.forwardClaudeAPIRequest(
			context.Background(),
			c,
			newKiroAPIKeyProtocolAccount(),
			claudeReq,
			body,
			"kiro-secret",
			"",
			claudeReq.Model,
			time.Now(),
			kiro.CacheEstimation{},
			kiro.CacheResult{},
			false,
		)
		done <- forwardResult{result: result, err: forwardErr}
	}()

	require.True(t, recorder.WaitForWrite(200*time.Millisecond), "expected SSE keepalive before upstream body data")
	require.Contains(t, recorder.BodyString(), ":\n\n")
	streamBody.Release()

	select {
	case got := <-done:
		require.NoError(t, got.err)
		require.NotNil(t, got.result)
	case <-time.After(time.Second):
		t.Fatal("stream did not finish after releasing upstream body")
	}
}

func TestKiroAPIKeyOpenAIStreamSendsKeepaliveWhileUpstreamSilent(t *testing.T) {
	streamBody := newGatedReadCloser([]byte(strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":4}}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")))
	defer streamBody.Close()
	svc := &KiroGatewayService{
		settingService: NewSettingService(nil, &config.Config{
			Gateway: config.GatewayConfig{StreamKeepaliveInterval: 1},
		}),
	}
	c, recorder := newKiroStreamTestContext()

	type streamResult struct {
		usage *OpenAIUsage
		err   error
	}
	done := make(chan streamResult, 1)
	go func() {
		usage, _, streamErr := svc.handleClaudeAPIAsOpenAIStream(
			c,
			newBlockingKiroStreamHTTPResponse(streamBody),
			"gpt-5.6-sol",
			4,
			time.Now(),
		)
		done <- streamResult{usage: usage, err: streamErr}
	}()

	require.True(t, recorder.WaitForWrite(200*time.Millisecond), "expected initial OpenAI SSE event")
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(recorder.BodyString(), ":\n\n") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	require.Contains(t, recorder.BodyString(), ":\n\n", "expected keepalive while upstream body is silent")
	streamBody.Release()

	select {
	case got := <-done:
		require.NoError(t, got.err)
		require.NotNil(t, got.usage)
	case <-time.After(time.Second):
		t.Fatal("stream did not finish after releasing upstream body")
	}
}

func TestKiroAPIKeyIncompleteClaudeSSEDoesNotBecomeNormalCompletion(t *testing.T) {
	upstream := &kiroAPIKeyProtocolUpstream{
		responseBody: strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"usage":{"input_tokens":4}}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`,
			``,
		}, "\n"),
		responseHeader: make(http.Header),
	}
	svc := newKiroAPIKeyProtocolService(upstream)
	body := []byte(`{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	c, recorder := newOpenAIKiroTestContext(body)

	result, err := svc.ForwardChatCompletions(context.Background(), c, newKiroAPIKeyProtocolAccount(), body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, recorder.Body.String(), `"type":"upstream_incomplete_stream"`)
	require.NotContains(t, recorder.Body.String(), `"finish_reason":"stop"`)
	require.Equal(t, 1, strings.Count(recorder.Body.String(), "data: [DONE]"))
}

func TestKiroAPIKeyClaudeSSESanitizesUpstreamError(t *testing.T) {
	upstream := &kiroAPIKeyProtocolUpstream{
		responseBody: strings.Join([]string{
			`event: error`,
			`data: {"type":"error","error":{"type":"api_error","message":"profileArn=arn:aws:codewhisperer:us-east-1:123456789012:profile/SECRET Authorization: Bearer secret-token"}}`,
			``,
		}, "\n"),
		responseHeader: make(http.Header),
	}
	svc := newKiroAPIKeyProtocolService(upstream)
	body := []byte(`{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	c, recorder := newOpenAIKiroTestContext(body)

	result, err := svc.ForwardChatCompletions(context.Background(), c, newKiroAPIKeyProtocolAccount(), body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, recorder.Body.String(), `"type":"api_error"`)
	require.NotContains(t, recorder.Body.String(), "arn:aws:")
	require.NotContains(t, recorder.Body.String(), "123456789012")
	require.NotContains(t, recorder.Body.String(), "secret-token")
}

func TestKiroAPIKeyBuiltinWebSearchStillUsesAnthropicMessagesProtocol(t *testing.T) {
	upstream := &kiroAPIKeyProtocolUpstream{
		responseBody:   `{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`,
		responseHeader: make(http.Header),
	}
	svc := newKiroAPIKeyProtocolService(upstream)
	body := []byte(`{"model":"deepseek-v3","max_tokens":1024,"messages":[{"role":"user","content":"search"}],"tools":[{"type":"web_search_20250305","name":"web_search","input_schema":{"type":"object"}}]}`)
	claudeReq, err := kiro.ParseClaudeRequestFromJSON(body)
	require.NoError(t, err)
	c, recorder := newOpenAIKiroTestContext(body)

	result, err := svc.ForwardWithWebSearch(context.Background(), c, newKiroAPIKeyProtocolAccount(), body, claudeReq)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "https://kiro.example/custom/v1/messages", upstream.requestURL)
	require.Equal(t, "2023-06-01", upstream.requestHeader.Get("anthropic-version"))
	require.Empty(t, upstream.requestHeader.Get("X-Amz-Target"))
	require.Equal(t, http.StatusOK, recorder.Code)
}
