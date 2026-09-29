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

type kiroThinkingCacheStub struct{ values map[string][]byte }

type kiroOpenAIUsageResponse struct {
	PromptTokens        int `json:"prompt_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"prompt_tokens_details"`
}

func (c *kiroThinkingCacheStub) Put(_ context.Context, key string, content []byte, _ time.Duration) error {
	if c.values == nil {
		c.values = make(map[string][]byte)
	}
	c.values[key] = append([]byte(nil), content...)
	return nil
}

func (c *kiroThinkingCacheStub) Get(_ context.Context, key string) ([]byte, error) {
	return append([]byte(nil), c.values[key]...), nil
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

func TestKiroAPIKeyOpus55PassesModelToClaudeAPI(t *testing.T) {
	upstream := &kiroAPIKeyProtocolUpstream{
		responseBody:   `{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1,"cache_creation_input_tokens":30,"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":20}}}`,
		responseHeader: make(http.Header),
	}
	svc := newKiroAPIKeyProtocolService(upstream)
	account := newKiroAPIKeyProtocolAccount()
	body := []byte(`{"model":"claude-opus-5-5","max_tokens":256,"messages":[{"role":"user","content":"hi"}]}`)
	c, _ := newOpenAIKiroTestContext(body)

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 30, result.Usage.CacheCreationInputTokens)
	require.Equal(t, 10, result.Usage.CacheCreation5mTokens)
	require.Equal(t, 20, result.Usage.CacheCreation1hTokens)
	require.Equal(t, "https://kiro.example/custom/v1/messages", upstream.requestURL)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(upstream.requestBody, &payload))
	require.Equal(t, KiroModelOpus55, payload["model"])
}

func TestKiroAPIKeyOpus55OpenAICompatibilityPassesModel(t *testing.T) {
	upstream := &kiroAPIKeyProtocolUpstream{
		responseBody:   `{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1,"cache_creation_input_tokens":30,"cache_read_input_tokens":20,"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":20}}}`,
		responseHeader: make(http.Header),
	}
	svc := newKiroAPIKeyProtocolService(upstream)
	body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`)
	c, recorder := newOpenAIKiroTestContext(body)

	result, err := svc.ForwardChatCompletions(context.Background(), c, newKiroAPIKeyProtocolAccount(), body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 30, result.Usage.CacheCreationInputTokens)
	require.Equal(t, 20, result.Usage.CacheReadInputTokens)
	require.Equal(t, 10, result.Usage.CacheCreation5mTokens)
	require.Equal(t, 20, result.Usage.CacheCreation1hTokens)
	require.Equal(t, 3, result.Usage.InputTokens)
	var response struct {
		Usage kiroOpenAIUsageResponse `json:"usage"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, 53, response.Usage.PromptTokens)
	require.Equal(t, 54, response.Usage.TotalTokens)
	require.Equal(t, 20, response.Usage.PromptTokensDetails.CachedTokens)
	require.Equal(t, 30, response.Usage.PromptTokensDetails.CacheWriteTokens)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(upstream.requestBody, &payload))
	require.Equal(t, KiroModelOpus55, payload["model"])
}

func TestKiroAPIKeyOpus55OpenAIStreamReportsTotalPromptTokens(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{"usage":{"input_tokens":3,"cache_creation_input_tokens":30,"cache_read_input_tokens":20}}}`, ``,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`, ``,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`, ``,
		`data: {"type":"content_block_stop","index":0}`, ``,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`, ``,
		`data: {"type":"message_stop"}`, ``,
	}, "\n")
	for _, tc := range []struct {
		name          string
		streamOptions string
		includeUsage  bool
	}{
		{name: "requested", streamOptions: `,"stream_options":{"include_usage":true}`, includeUsage: true},
		{name: "not requested"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &kiroAPIKeyProtocolUpstream{responseBody: stream, responseHeader: make(http.Header)}
			svc := newKiroAPIKeyProtocolService(upstream)
			body := []byte(`{"model":"claude-opus-5-5","stream":true,"messages":[{"role":"user","content":"hi"}]` + tc.streamOptions + `}`)
			c, recorder := newOpenAIKiroTestContext(body)

			result, err := svc.ForwardChatCompletions(context.Background(), c, newKiroAPIKeyProtocolAccount(), body)
			require.NoError(t, err)
			require.Equal(t, 3, result.Usage.InputTokens)
			require.Equal(t, 30, result.Usage.CacheCreationInputTokens)
			require.Equal(t, 20, result.Usage.CacheReadInputTokens)
			var responseUsage *kiroOpenAIUsageResponse
			for _, line := range strings.Split(recorder.Body.String(), "\n") {
				if !strings.HasPrefix(line, "data: {") {
					continue
				}
				var chunk struct {
					Usage *kiroOpenAIUsageResponse `json:"usage"`
				}
				require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk))
				if chunk.Usage != nil {
					responseUsage = chunk.Usage
				}
			}
			if !tc.includeUsage {
				require.Nil(t, responseUsage)
				return
			}
			require.NotNil(t, responseUsage)
			require.Equal(t, 53, responseUsage.PromptTokens)
			require.Equal(t, 54, responseUsage.TotalTokens)
			require.Equal(t, 20, responseUsage.PromptTokensDetails.CachedTokens)
			require.Equal(t, 30, responseUsage.PromptTokensDetails.CacheWriteTokens)
		})
	}
}

func TestKiroAPIKeyOpus55OpenAICompatibilityOmitsTemperature(t *testing.T) {
	for _, tc := range []struct {
		name            string
		model           string
		wantTemperature bool
	}{
		{name: "Opus 5.5", model: KiroModelOpus55},
		{name: "other model", model: "claude-sonnet-4-20250514", wantTemperature: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &kiroAPIKeyProtocolUpstream{
				responseBody:   `{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":3,"output_tokens":1}}`,
				responseHeader: make(http.Header),
			}
			svc := newKiroAPIKeyProtocolService(upstream)
			body := []byte(`{"model":"` + tc.model + `","temperature":0,"messages":[{"role":"user","content":"hi"}]}`)
			c, _ := newOpenAIKiroTestContext(body)

			_, err := svc.ForwardChatCompletions(context.Background(), c, newKiroAPIKeyProtocolAccount(), body)
			require.NoError(t, err)
			var payload map[string]any
			require.NoError(t, json.Unmarshal(upstream.requestBody, &payload))
			if tc.wantTemperature {
				require.Equal(t, float64(0), payload["temperature"])
			} else {
				require.NotContains(t, payload, "temperature")
			}
		})
	}
}

func TestKiroAPIKeyOpus55RejectsForcedToolChoiceBeforeUpstream(t *testing.T) {
	for _, choice := range []string{`"required"`, `{"type":"function","function":{"name":"search"}}`} {
		t.Run(choice, func(t *testing.T) {
			upstream := &kiroAPIKeyProtocolUpstream{}
			svc := newKiroAPIKeyProtocolService(upstream)
			body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"search","parameters":{"type":"object"}}}],"tool_choice":` + choice + `}`)
			c, _ := newOpenAIKiroTestContext(body)

			_, err := svc.ForwardChatCompletions(context.Background(), c, newKiroAPIKeyProtocolAccount(), body)
			require.ErrorContains(t, err, "not supported")
			require.Empty(t, upstream.requestBody)
		})
	}
}

func TestKiroAPIKeyOpus55ConvertsLegacyThinkingToAdaptive(t *testing.T) {
	for _, tc := range []struct {
		name         string
		thinking     string
		wantThinking bool
		wantMax      float64
		wantEffort   string
		outputConfig string
	}{
		{name: "enabled", thinking: `{"type":"enabled","budget_tokens":20000}`, wantThinking: true, wantMax: float64(kiro.KiroFixedMaxTokens), wantEffort: "high"},
		{name: "disabled", thinking: `{"type":"disabled"}`, wantThinking: false, wantMax: float64(defaultKiroAPIKeyOpenAIMaxTokens), wantEffort: "low"},
		{name: "explicit effort", thinking: `{"type":"enabled","budget_tokens":20000}`, wantThinking: true, wantMax: float64(kiro.KiroFixedMaxTokens), wantEffort: "low", outputConfig: `,"output_config":{"effort":"low"}`},
		{name: "reasoning effort", thinking: `{"type":"enabled","budget_tokens":20000}`, wantThinking: true, wantMax: float64(kiro.KiroFixedMaxTokens), wantEffort: "low", outputConfig: `,"reasoning_effort":"low"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &kiroAPIKeyProtocolUpstream{responseBody: `{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":3,"output_tokens":1}}`, responseHeader: make(http.Header)}
			svc := newKiroAPIKeyProtocolService(upstream)
			body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}],"thinking":` + tc.thinking + tc.outputConfig + `}`)
			c, _ := newOpenAIKiroTestContext(body)

			_, err := svc.ForwardChatCompletions(context.Background(), c, newKiroAPIKeyProtocolAccount(), body)
			require.NoError(t, err)
			var payload map[string]any
			require.NoError(t, json.Unmarshal(upstream.requestBody, &payload))
			require.Equal(t, tc.wantMax, payload["max_tokens"])
			require.Equal(t, tc.wantEffort, payload["output_config"].(map[string]any)["effort"])
			if tc.wantThinking {
				require.Equal(t, map[string]any{"type": "adaptive"}, payload["thinking"])
			} else {
				require.NotContains(t, payload, "thinking")
			}
		})
	}
}

func TestKiroAPIKeyOpus55HighEffortGetsLargerDefaultMaxTokens(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options string
		wantMax float64
	}{
		{name: "output config xhigh", options: `,"output_config":{"effort":"xhigh"}`, wantMax: float64(kiro.KiroFixedMaxTokens)},
		{name: "reasoning effort max", options: `,"reasoning_effort":"max"`, wantMax: float64(kiro.KiroFixedMaxTokens)},
		{name: "medium effort", options: `,"output_config":{"effort":"medium"}`, wantMax: float64(defaultKiroAPIKeyOpenAIMaxTokens)},
		{name: "explicit max tokens", options: `,"output_config":{"effort":"max"},"max_tokens":2048`, wantMax: 2048},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &kiroAPIKeyProtocolUpstream{
				responseBody:   `{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":3,"output_tokens":1}}`,
				responseHeader: make(http.Header),
			}
			svc := newKiroAPIKeyProtocolService(upstream)
			body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]` + tc.options + `}`)
			c, _ := newOpenAIKiroTestContext(body)

			_, err := svc.ForwardChatCompletions(context.Background(), c, newKiroAPIKeyProtocolAccount(), body)
			require.NoError(t, err)
			var payload map[string]any
			require.NoError(t, json.Unmarshal(upstream.requestBody, &payload))
			require.Equal(t, tc.wantMax, payload["max_tokens"])
		})
	}
}

func TestKiroAPIKeyOpenAIStreamKeepsCacheCreationBreakdown(t *testing.T) {
	usage := &OpenAIUsage{}
	_, _, _, err := parseClaudeSSEData([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":1,"cache_creation_input_tokens":30,"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":20}}}}`), usage)
	require.NoError(t, err)
	require.Equal(t, 30, usage.CacheCreationInputTokens)
	require.Equal(t, 10, usage.CacheCreation5mTokens)
	require.Equal(t, 20, usage.CacheCreation1hTokens)
}

func TestKiroAPIKeyOpus55OpenAIToolContinuationRestoresThinking(t *testing.T) {
	upstream := &kiroAPIKeyScriptedUpstream{responses: []kiroAPIKeyScriptedResponse{
		{statusCode: http.StatusOK, header: make(http.Header), body: `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"thinking","thinking":"","signature":"sig-1"},{"type":"tool_use","id":"toolu_1","name":"search","input":{"q":"hello"}},{"type":"tool_use","id":"toolu_2","name":"search","input":{"q":"world"}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}}`},
		{statusCode: http.StatusOK, header: make(http.Header), body: `{"id":"msg_2","type":"message","role":"assistant","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":2}}`},
	}}
	svc := newKiroAPIKeyProtocolService(upstream)
	svc.thinkingCache = &kiroThinkingCacheStub{}
	account := newKiroAPIKeyProtocolAccount()
	first := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"search"}],"tools":[{"type":"function","function":{"name":"search","parameters":{"type":"object"}}}]}`)
	c, _ := newOpenAIKiroTestContext(first)
	_, err := svc.ForwardChatCompletions(context.Background(), c, account, first)
	require.NoError(t, err)

	second := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"search"},{"role":"assistant","tool_calls":[{"id":"toolu_1","type":"function","function":{"name":"search","arguments":"{\"q\":\"hello\"}"}},{"id":"toolu_2","type":"function","function":{"name":"search","arguments":"{\"q\":\"world\"}"}}]},{"role":"tool","tool_call_id":"toolu_1","content":"first result"},{"role":"tool","tool_call_id":"toolu_2","content":"second result"}],"tools":[{"type":"function","function":{"name":"search","parameters":{"type":"object"}}}]}`)
	c, _ = newOpenAIKiroTestContext(second)
	_, err = svc.ForwardChatCompletions(context.Background(), c, account, second)
	require.NoError(t, err)
	require.Len(t, upstream.requestBodies, 2)
	var payload struct {
		Messages []map[string]any `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(upstream.requestBodies[1], &payload))
	require.Len(t, payload.Messages, 3)
	assistantBlocks := payload.Messages[1]["content"].([]any)
	require.Equal(t, "thinking", assistantBlocks[0].(map[string]any)["type"])
	require.Equal(t, "sig-1", assistantBlocks[0].(map[string]any)["signature"])
	require.Equal(t, "tool_use", assistantBlocks[1].(map[string]any)["type"])
	require.Equal(t, "toolu_2", assistantBlocks[2].(map[string]any)["id"])
	resultBlocks := payload.Messages[2]["content"].([]any)
	require.Len(t, resultBlocks, 2)
	require.Equal(t, "toolu_1", resultBlocks[0].(map[string]any)["tool_use_id"])
	require.Equal(t, "toolu_2", resultBlocks[1].(map[string]any)["tool_use_id"])
}

func TestKiroAPIKeyOpus55OpenAIStreamToolContinuationRestoresThinking(t *testing.T) {
	stream := strings.Join([]string{
		`event: message_start`, `data: {"type":"message_start","message":{"usage":{"input_tokens":10}}}`, ``,
		`event: content_block_start`, `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`, ``,
		`event: content_block_delta`, `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"plan"}}`, ``,
		`event: content_block_delta`, `data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-stream"}}`, ``,
		`event: content_block_stop`, `data: {"type":"content_block_stop","index":0}`, ``,
		`event: content_block_start`, `data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_stream","name":"search","input":{}}}`, ``,
		`event: content_block_delta`, `data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":\"hello\"}"}}`, ``,
		`event: content_block_stop`, `data: {"type":"content_block_stop","index":1}`, ``,
		`event: message_delta`, `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`, ``,
		`event: message_stop`, `data: {"type":"message_stop"}`, ``,
	}, "\n")
	upstream := &kiroAPIKeyScriptedUpstream{responses: []kiroAPIKeyScriptedResponse{
		{statusCode: http.StatusOK, header: make(http.Header), body: stream},
		{statusCode: http.StatusOK, header: make(http.Header), body: `{"id":"msg_2","type":"message","role":"assistant","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":2}}`},
	}}
	svc := newKiroAPIKeyProtocolService(upstream)
	svc.thinkingCache = &kiroThinkingCacheStub{}
	account := newKiroAPIKeyProtocolAccount()
	first := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"search"}],"tools":[{"type":"function","function":{"name":"search","parameters":{"type":"object"}}}],"stream":true}`)
	c, _ := newOpenAIKiroTestContext(first)
	_, err := svc.ForwardChatCompletions(context.Background(), c, account, first)
	require.NoError(t, err)

	second := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"search"},{"role":"assistant","tool_calls":[{"id":"toolu_stream","type":"function","function":{"name":"search","arguments":"{\"q\":\"hello\"}"}}]},{"role":"tool","tool_call_id":"toolu_stream","content":"result"}]}`)
	c, _ = newOpenAIKiroTestContext(second)
	_, err = svc.ForwardChatCompletions(context.Background(), c, account, second)
	require.NoError(t, err)
	var payload struct {
		Messages []map[string]any `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(upstream.requestBodies[1], &payload))
	assistantBlocks := payload.Messages[1]["content"].([]any)
	require.Equal(t, "plan", assistantBlocks[0].(map[string]any)["thinking"])
	require.Equal(t, "sig-stream", assistantBlocks[0].(map[string]any)["signature"])
	require.Equal(t, map[string]any{"q": "hello"}, assistantBlocks[1].(map[string]any)["input"])
}

func TestKiroAPIKeyOpus55ToolContinuationWithoutCachedTurnFailsBeforeUpstream(t *testing.T) {
	upstream := &kiroAPIKeyProtocolUpstream{}
	svc := newKiroAPIKeyProtocolService(upstream)
	svc.thinkingCache = &kiroThinkingCacheStub{}
	body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"search"},{"role":"assistant","tool_calls":[{"id":"toolu_missing","type":"function","function":{"name":"search","arguments":"{}"}}]},{"role":"tool","tool_call_id":"toolu_missing","content":"result"}]}`)
	c, _ := newOpenAIKiroTestContext(body)
	_, err := svc.ForwardChatCompletions(context.Background(), c, newKiroAPIKeyProtocolAccount(), body)
	require.ErrorContains(t, err, "tool continuation expired")
	require.Empty(t, upstream.requestBody)
}

func TestKiroAPIKeyClaudeDoesNotUseLocalSimulatedCache(t *testing.T) {
	upstream := &kiroAPIKeyProtocolUpstream{
		responseBody:   `{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}]}`,
		responseHeader: make(http.Header),
	}
	svc := newKiroAPIKeyProtocolService(upstream)
	payload, err := json.Marshal(map[string]any{
		"model":  "claude-opus-4-8",
		"system": strings.Repeat("system prompt ", 600),
		"messages": []map[string]string{
			{"role": "user", "content": "request"},
		},
	})
	require.NoError(t, err)
	c, _ := newOpenAIKiroTestContext(payload)

	result, err := svc.Forward(context.Background(), c, newKiroAPIKeyProtocolAccount(), payload)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Zero(t, result.Usage.CacheReadInputTokens)
	require.Zero(t, result.Usage.CacheCreationInputTokens)
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
			"gpt-5.6-sol",
			false,
			4,
			time.Now(),
			false,
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
