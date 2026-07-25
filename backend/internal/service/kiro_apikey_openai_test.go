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

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type kiroAPIKeyProtocolUpstream struct {
	requestURL     string
	requestHeader  http.Header
	requestBody    []byte
	responseBody   string
	responseHeader http.Header
}

func (u *kiroAPIKeyProtocolUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	u.requestURL = req.URL.String()
	u.requestHeader = req.Header.Clone()
	u.requestBody = body
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(u.responseBody)),
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
