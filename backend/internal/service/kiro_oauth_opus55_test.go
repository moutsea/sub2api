package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestKiroOAuthOpus55Forwarding(t *testing.T) {
	for _, model := range []string{
		"claude-opus-5-5", "claude-opus-5.5",
		"claude-opus-5-5-thinking", "claude-opus-5.5-thinking",
	} {
		for _, protocol := range []string{"messages", "chat_completions", "responses"} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%v", model, protocol, stream), func(t *testing.T) {
					upstream := &kiroAPIKeyProtocolUpstream{
						responseBody: string(mustContentPayload(t, "OPUS_55_OK")),
					}
					svc, account := newKiroResponsesTestService(upstream)
					body := []byte(fmt.Sprintf(`{"model":%q,"stream":%v,"max_tokens":256,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"},{"role":"user","content":"continue"}]}`, model, stream))
					if protocol == "responses" {
						body = []byte(fmt.Sprintf(`{"model":%q,"stream":%v,"input":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"},{"role":"user","content":"continue"}]}`, model, stream))
					}
					c, recorder := newOpenAIKiroTestContext(body)
					switch protocol {
					case "messages":
						result, err := svc.Forward(context.Background(), c, account, body)
						require.NoError(t, err)
						require.Equal(t, model, result.Model)
						require.Equal(t, stream, result.Stream)
					case "chat_completions":
						result, err := svc.ForwardChatCompletions(context.Background(), c, account, body)
						require.NoError(t, err)
						require.Equal(t, model, result.Model)
						require.Equal(t, stream, result.Stream)
					case "responses":
						result, err := svc.ForwardResponses(context.Background(), c, account, body)
						require.NoError(t, err)
						require.Equal(t, model, result.Model)
						require.Equal(t, stream, result.Stream)
					}
					require.Equal(t, http.StatusOK, recorder.Code)
					if stream {
						var text strings.Builder
						for _, line := range strings.Split(recorder.Body.String(), "\n") {
							if !strings.HasPrefix(line, "data: ") {
								continue
							}
							event := gjson.Parse(strings.TrimPrefix(line, "data: "))
							switch protocol {
							case "messages":
								if event.Get("delta.type").String() == "text_delta" {
									text.WriteString(event.Get("delta.text").String())
								}
							case "chat_completions":
								text.WriteString(event.Get("choices.0.delta.content").String())
							case "responses":
								if event.Get("type").String() == "response.output_text.delta" {
									text.WriteString(event.Get("delta").String())
								}
							}
						}
						require.Equal(t, "OPUS_55_OK", text.String())
					} else {
						require.Contains(t, recorder.Body.String(), "OPUS_55_OK")
					}
					require.Contains(t, upstream.requestURL, "/generateAssistantResponse")
					require.Equal(t, "Bearer oauth-test-token", upstream.requestHeader.Get("Authorization"))

					var payload kiro.CodeWhispererRequest
					require.NoError(t, json.Unmarshal(upstream.requestBody, &payload))
					require.Equal(t, "claude-opus-5.5", payload.ConversationState.CurrentMessage.UserInputMessage.ModelID)
					require.NotEmpty(t, payload.ConversationState.History)
					for _, entry := range payload.ConversationState.History {
						if entry.User != nil {
							require.Equal(t, "claude-opus-5.5", entry.User.ModelID)
						}
					}
				})
			}
		}
	}
}

func TestKiroOAuthOpus55Connection(t *testing.T) {
	for _, authType := range []string{KiroAuthMethodSocial, KiroAuthMethodIdC} {
		for _, model := range []string{"claude-opus-5-5", "claude-opus-5.5"} {
			t.Run(authType+"/"+model, func(t *testing.T) {
				upstream := &kiroAPIKeyProtocolUpstream{responseBody: string(mustContentPayload(t, "OPUS_55_OK"))}
				svc, account := newKiroResponsesTestService(upstream)
				account.Credentials["auth_type"] = authType
				result, err := svc.TestConnection(context.Background(), account, model)
				require.NoError(t, err)
				require.Equal(t, "OPUS_55_OK", result.Text)
				require.Equal(t, "claude-opus-5.5", result.MappedModel)
				var payload kiro.CodeWhispererRequest
				require.NoError(t, json.Unmarshal(upstream.requestBody, &payload))
				require.Equal(t, "claude-opus-5.5", payload.ConversationState.CurrentMessage.UserInputMessage.ModelID)
			})
		}
	}
}
