package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newKiroResponsesTestService(upstream HTTPUpstream) (*KiroGatewayService, *Account) {
	provider := NewKiroTokenProvider(nil, upstream)
	state := provider.getOrCreateState(42)
	state.AccessToken = "oauth-test-token"
	state.ExpiresAt = time.Now().Add(time.Hour)
	state.Status = KiroTokenStatusActive
	account := &Account{ID: 42, Name: "Kiro OAuth", Platform: PlatformKiro, Type: AccountTypeOAuth, Concurrency: 2,
		Credentials: map[string]any{"auth_type": "social", "refresh_token": "test-refresh", "profile_arn": "arn:test:profile"},
		Extra:       map[string]any{"kiro_subscription_type": "PRO"}}
	return &KiroGatewayService{tokenProvider: provider, httpUpstream: upstream}, account
}

func TestKiroOAuthForwardResponses(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			wire := mustContentPayload(t, "hello")
			wire = append(wire, mustKiroEventFrame(t, "toolUseEvent", map[string]any{"toolUseId": "call_1", "name": "read", "input": `{"path":"a.go"}`, "stop": true})...)
			upstream := &kiroAPIKeyProtocolUpstream{responseBody: string(wire), responseHeader: http.Header{"X-Amzn-Requestid": []string{"aws-request-1"}}}
			svc, account := newKiroResponsesTestService(upstream)
			body := []byte(fmt.Sprintf(`{"model":"claude-sonnet-4-6","stream":%v,"store":false,"instructions":"Be brief","input":"Read a.go","tools":[{"type":"function","name":"read","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}]}`, stream))
			c, rec := newOpenAIKiroTestContext(body)
			result, err := svc.ForwardResponses(context.Background(), c, account, body)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, stream, result.Stream)
			require.Equal(t, "Bearer oauth-test-token", upstream.requestHeader.Get("Authorization"))
			require.Contains(t, upstream.requestURL, "generateAssistantResponse")
			require.NotContains(t, upstream.requestURL, "/v1/messages")
			require.Contains(t, string(upstream.requestBody), "conversationState")
			require.Contains(t, string(upstream.requestBody), "Be brief")
			require.Equal(t, "aws-request-1", result.RequestID)
			require.Equal(t, "aws-request-1", rec.Header().Get("x-request-id"))
			require.Equal(t, http.StatusOK, rec.Code)
			var response map[string]any
			if stream {
				require.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
				sequence := 0
				for _, line := range strings.Split(rec.Body.String(), "\n") {
					if !strings.HasPrefix(line, "data: ") {
						continue
					}
					var event map[string]any
					require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event))
					require.Equal(t, float64(sequence), event["sequence_number"])
					sequence++
					if event["type"] == "response.completed" {
						response = event["response"].(map[string]any)
					}
				}
				require.NotNil(t, response)
				require.NotContains(t, rec.Body.String(), "chat.completion")
				require.NotContains(t, rec.Body.String(), "[DONE]")
			} else {
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
			}
			require.Equal(t, "response", response["object"])
			require.Equal(t, "completed", response["status"])
			output := response["output"].([]any)
			require.Len(t, output, 2)
			require.Equal(t, "hello", output[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"])
			call := output[1].(map[string]any)
			require.Equal(t, "function_call", call["type"])
			require.Equal(t, "call_1", call["call_id"])
			require.Equal(t, `{"path":"a.go"}`, call["arguments"])
			require.Equal(t, float64(result.Usage.OutputTokens), response["usage"].(map[string]any)["output_tokens"])
		})
	}
}

func TestKiroOAuthResponsesRejectsStatefulRequestsBeforeUpstream(t *testing.T) {
	upstream := &kiroAPIKeyProtocolUpstream{}
	svc, account := newKiroResponsesTestService(upstream)
	body := []byte(`{"model":"auto","input":"hello","previous_response_id":"resp_old"}`)
	c, rec := newOpenAIKiroTestContext(body)
	_, err := svc.ForwardResponses(context.Background(), c, account, body)
	require.Error(t, err)
	require.Equal(t, 400, rec.Code)
	require.Contains(t, rec.Body.String(), "previous_response_id")
	require.Empty(t, upstream.requestURL)
}
func TestKiroOAuthResponsesFailures(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			for _, body := range []string{"", string(mustKiroExceptionFrame(t, "throttlingException", map[string]any{"message": "retry"}))} {
				svc, account := newKiroResponsesTestService(&kiroAPIKeyProtocolUpstream{responseBody: body})
				request := []byte(fmt.Sprintf(`{"model":"auto","input":"hello","stream":%v}`, stream))
				c, rec := newOpenAIKiroTestContext(request)
				_, err := svc.ForwardResponses(context.Background(), c, account, request)
				var failover *UpstreamFailoverError
				require.ErrorAs(t, err, &failover)
				require.Empty(t, rec.Body.String())
				require.False(t, c.Writer.Written())
			}
		})
	}
}
func TestKiroOAuthResponsesPostCommitFailure(t *testing.T) {
	wire := mustContentPayload(t, "partial answer")
	wire = append(wire, mustKiroExceptionFrame(t, "internalServerException", map[string]any{"message": "failed"})...)
	svc, account := newKiroResponsesTestService(&kiroAPIKeyProtocolUpstream{responseBody: string(wire)})
	body := []byte(`{"model":"auto","input":"hello","stream":true}`)
	c, rec := newOpenAIKiroTestContext(body)
	result, err := svc.ForwardResponses(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), "response.output_text.delta")
	require.Contains(t, rec.Body.String(), "response.failed")
	require.NotContains(t, rec.Body.String(), "response.completed")
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event map[string]any
		require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event))
		if event["type"] == "response.failed" {
			output := event["response"].(map[string]any)["output"].([]any)
			require.Equal(t, "partial answer", output[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"])
		}
	}
}
func TestKiroOAuthResponsesRejectsAPIKey(t *testing.T) {
	svc := &KiroGatewayService{}
	body := []byte(`{"model":"auto","input":"hello"}`)
	c, rec := newOpenAIKiroTestContext(body)
	_, err := svc.ForwardResponses(context.Background(), c, newKiroAPIKeyProtocolAccount(), body)
	require.Error(t, err)
	require.Equal(t, 400, rec.Code)
}

func TestKiroOAuthResponsesCommitDeadlineFailure(t *testing.T) {
	body := newBlockingReadCloser()
	defer body.Close()
	svc, account := newKiroResponsesTestService(&kiroAPIKeyProtocolUpstream{responseReader: body})
	svc.openAICommitDeadline = 20 * time.Millisecond
	request := []byte(`{"model":"auto","input":"hello","stream":true}`)
	c, rec := newKiroStreamTestContext()
	done := make(chan error, 1)
	go func() { _, err := svc.ForwardResponses(context.Background(), c, account, request); done <- err }()
	require.True(t, rec.WaitForWrite(time.Second))
	require.Contains(t, rec.BodyString(), "response.created")
	require.NotContains(t, rec.BodyString(), "response.completed")
	require.NoError(t, body.Close())
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("forward did not finish after EOF")
	}
	require.Contains(t, rec.BodyString(), "response.failed")
	require.NotContains(t, rec.BodyString(), "response.completed")
}
