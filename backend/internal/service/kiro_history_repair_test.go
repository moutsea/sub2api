package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/stretchr/testify/require"
)

func TestKiroHistoryRepairDoesNotInventAssistantReplies(t *testing.T) {
	for _, apiKey := range []bool{false, true} {
		name := "oauth"
		if apiKey {
			name = "apikey"
		}
		t.Run(name, func(t *testing.T) {
			upstream := &kiroAPIKeyProtocolUpstream{
				responseBody:   string(mustContentPayload(t, "继续处理任务")),
				responseHeader: make(http.Header),
			}
			svc, account := newKiroResponsesTestService(upstream)
			if apiKey {
				upstream.responseBody = `{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"继续处理任务"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`
				svc, account = newKiroAPIKeyProtocolService(upstream), newKiroAPIKeyProtocolAccount()
			}
			body := []byte(`{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[
				{"role":"user","content":"修复问题"},
				{"role":"assistant","content":[{"type":"tool_use","id":"interrupted","name":"read_file","input":{}}]},
				{"role":"user","content":"继续"}
			]}`)
			c, _ := newOpenAIKiroTestContext(body)
			result, err := svc.Forward(context.Background(), c, account, body)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.NotContains(t, string(upstream.requestBody), `"text":"."`)
			require.NotContains(t, string(upstream.requestBody), `"content":"."`)
			if apiKey {
				var req kiro.ClaudeRequest
				require.NoError(t, json.Unmarshal(upstream.requestBody, &req))
				require.Len(t, req.Messages, 2)
				require.Equal(t, "修复问题", req.Messages[0].Content)
				require.Equal(t, "继续", req.Messages[1].Content)
				for _, msg := range req.Messages {
					require.Equal(t, "user", msg.Role)
				}
			} else {
				var req kiro.CodeWhispererRequest
				require.NoError(t, json.Unmarshal(upstream.requestBody, &req))
				require.Empty(t, req.ConversationState.History)
				require.Contains(t, req.ConversationState.CurrentMessage.UserInputMessage.Content, "修复问题\n继续")
			}
		})
	}
}

func TestKiroHistoryRepairRemovesRepeatedToolResults(t *testing.T) {
	upstream := &kiroAPIKeyProtocolUpstream{
		responseBody:   `{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`,
		responseHeader: make(http.Header),
	}
	svc, account := newKiroAPIKeyProtocolService(upstream), newKiroAPIKeyProtocolAccount()
	body := []byte(`{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[
		{"role":"user","content":"Read a.go"},
		{"role":"assistant","content":[{"type":"tool_use","id":"repeated","name":"read_file","input":{"path":"a.go"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"repeated","content":"A"}]},
		{"role":"assistant","content":"A is loaded"},
		{"role":"user","content":"Read b.go"},
		{"role":"assistant","content":[{"type":"tool_use","id":"repeated","name":"read_file","input":{"path":"b.go"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"repeated","content":"B"},{"type":"text","text":"Continue with the image"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}}]}
	]}`)
	c, _ := newOpenAIKiroTestContext(body)
	_, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	var req kiro.ClaudeRequest
	require.NoError(t, json.Unmarshal(upstream.requestBody, &req))
	encoded, err := json.Marshal(req.Messages)
	require.NoError(t, err)
	require.JSONEq(t, `[
		{"role":"user","content":"Read a.go"},
		{"role":"assistant","content":[{"type":"tool_use","id":"repeated","name":"read_file","input":{"path":"a.go"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"repeated","content":"A"}]},
		{"role":"assistant","content":"A is loaded"},
		{"role":"user","content":"Read b.go"},
		{"role":"user","content":[{"type":"text","text":"Continue with the image"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}}]}
	]`, string(encoded))
}

func TestKiroHistoryRepairPreservesSplitToolResultOrder(t *testing.T) {
	upstream := &kiroAPIKeyProtocolUpstream{
		responseBody:   `{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`,
		responseHeader: make(http.Header),
	}
	svc, account := newKiroAPIKeyProtocolService(upstream), newKiroAPIKeyProtocolAccount()
	body := []byte(`{"model":"claude-sonnet-4-6","max_tokens":1024,"tools":[{"name":"read_file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}],"messages":[
		{"role":"user","content":"Read two files"},
		{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"read_file","input":{"path":"a.go"}},{"type":"tool_use","id":"a","name":"read_file","input":{"path":"a.go"}},{"type":"tool_use","id":"b","name":"read_file","input":{"path":"b.go"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"A"}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"A"}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"b","content":"B"}]},
		{"role":"user","content":"Summarize the files"}
	]}`)
	c, _ := newOpenAIKiroTestContext(body)
	_, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	var req kiro.ClaudeRequest
	require.NoError(t, json.Unmarshal(upstream.requestBody, &req))
	encoded, err := json.Marshal(req.Messages)
	require.NoError(t, err)
	require.JSONEq(t, `[
		{"role":"user","content":"Read two files"},
		{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"read_file","input":{"path":"a.go"}},{"type":"tool_use","id":"b","name":"read_file","input":{"path":"b.go"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"A"}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"b","content":"B"}]},
		{"role":"user","content":"Summarize the files"}
	]`, string(encoded))
}
