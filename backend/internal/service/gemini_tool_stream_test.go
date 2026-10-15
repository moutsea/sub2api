package service

import (
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

type geminiToolStreamBlock struct {
	kind  string
	name  string
	value string
}

// Reassemble the wire events as a Claude client would, checking block lifecycle
// and JSON validity rather than just counting tool names in the response.
func readGeminiToolStreamBlocks(t *testing.T, body string) []geminiToolStreamBlock {
	t.Helper()
	var blocks []geminiToolStreamBlock
	openIndex := -1
	ids := make(map[string]bool)
	stopped := false
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
		}
		require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event))
		require.False(t, stopped, "event after message_stop")
		switch event.Type {
		case "content_block_start":
			require.Equal(t, -1, openIndex, "previous block must stop before starting another")
			require.Equal(t, len(blocks), event.Index)
			openIndex = event.Index
			blocks = append(blocks, geminiToolStreamBlock{kind: event.ContentBlock.Type, name: event.ContentBlock.Name})
			if event.ContentBlock.Type == "tool_use" {
				require.NotEmpty(t, event.ContentBlock.ID)
				require.False(t, ids[event.ContentBlock.ID], "independent calls need distinct IDs")
				ids[event.ContentBlock.ID] = true
			}
		case "content_block_delta":
			require.NotEqual(t, -1, openIndex)
			require.Equal(t, openIndex, event.Index)
			switch event.Delta.Type {
			case "text_delta":
				require.Equal(t, "text", blocks[openIndex].kind)
				blocks[openIndex].value += event.Delta.Text
			case "input_json_delta":
				require.Equal(t, "tool_use", blocks[openIndex].kind)
				blocks[openIndex].value += event.Delta.PartialJSON
			default:
				t.Fatalf("unexpected delta type: %s", event.Delta.Type)
			}
		case "content_block_stop":
			require.NotEqual(t, -1, openIndex)
			require.Equal(t, openIndex, event.Index)
			if blocks[openIndex].kind == "tool_use" {
				var args map[string]any
				require.NoError(t, json.Unmarshal([]byte(blocks[openIndex].value), &args), "invalid tool input: %s", blocks[openIndex].value)
				require.NotNil(t, args)
			}
			openIndex = -1
		case "message_delta":
			require.Equal(t, -1, openIndex)
			require.Equal(t, "tool_use", event.Delta.StopReason)
		case "message_stop":
			require.Equal(t, -1, openIndex)
			stopped = true
		case "message_start":
		default:
			t.Fatalf("unexpected event: %s", line)
		}
	}
	require.True(t, stopped)
	return blocks
}

func TestGeminiToolStreamIndependentCalls(t *testing.T) {
	gin.SetMode(gin.TestMode)
	call := func(args any) map[string]any {
		return map[string]any{"functionCall": map[string]any{"name": "read_file", "args": args}}
	}
	text := map[string]any{"text": "Reading files."}
	a := map[string]any{"path": "a.go"}
	b := map[string]any{"path": "b.go"}
	toolA := geminiToolStreamBlock{"tool_use", "read_file", `{"path":"a.go"}`}
	toolB := geminiToolStreamBlock{"tool_use", "read_file", `{"path":"b.go"}`}
	textBlock := geminiToolStreamBlock{"text", "", "Reading files."}
	for _, tc := range []struct {
		name   string
		chunks [][]map[string]any
		want   []geminiToolStreamBlock
	}{
		{"same chunk", [][]map[string]any{{call(a), call(b)}}, []geminiToolStreamBlock{toolA, toolB}},
		{"identical calls", [][]map[string]any{{call(a), call(a)}}, []geminiToolStreamBlock{toolA, toolA}},
		{"separate chunks", [][]map[string]any{{call(a)}, {call(b)}}, []geminiToolStreamBlock{toolA, toolB}},
		{"empty arguments", [][]map[string]any{{call(nil), call(map[string]any{})}}, []geminiToolStreamBlock{{"tool_use", "read_file", "{}"}, {"tool_use", "read_file", "{}"}}},
		{"complete string arguments", [][]map[string]any{{call(`{"path":"a.go"}`), call(`{"path":"b.go"}`)}}, []geminiToolStreamBlock{toolA, toolB}},
		{"string argument deltas", [][]map[string]any{{call(`{"path":`)}, {call(`"a.go"}`)}, {call(b)}}, []geminiToolStreamBlock{toolA, toolB}},
		{"cumulative string arguments", [][]map[string]any{{call(`{"path":"`)}, {call(`{"path":"a`)}, {call(`{"path":"a.go"}`)}, {call(b)}}, []geminiToolStreamBlock{toolA, toolB}},
		{"text between calls", [][]map[string]any{{text, call(a), text, call(b), text}}, []geminiToolStreamBlock{textBlock, toolA, textBlock, toolB, textBlock}},
	} {
		for _, mode := range []geminiTextStreamMode{geminiTextDeltas, geminiTextLegacyAuto} {
			t.Run(fmt.Sprintf("%s/mode=%d", tc.name, mode), func(t *testing.T) {
				var payload strings.Builder
				for i, parts := range tc.chunks {
					candidate := map[string]any{"content": map[string]any{"parts": parts}}
					if i == len(tc.chunks)-1 {
						candidate["finishReason"] = "STOP"
					}
					encoded, err := json.Marshal(map[string]any{"candidates": []any{candidate}})
					require.NoError(t, err)
					fmt.Fprintf(&payload, "data: %s\n\n", encoded)
				}
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				resp := &http.Response{Body: io.NopCloser(strings.NewReader(payload.String()))}
				_, err := (&GeminiMessagesCompatService{}).handleStreamingResponse(c, resp, time.Now(), "gemini", mode)
				require.NoError(t, err)
				require.Equal(t, tc.want, readGeminiToolStreamBlocks(t, recorder.Body.String()))
			})
		}
	}
}

func TestGeminiToolStreamContinuationPreservesCalls(t *testing.T) {
	gin.SetMode(gin.TestMode)
	chunks := []string{
		`{"_sub2api_continuation_hop":0,"candidates":[{"content":{"parts":[{"functionCall":{"name":"read_file","args":{"path":"a.go"}}}]}}]}`,
		`{"_sub2api_continuation_hop":1,"candidates":[{"content":{"parts":[{"functionCall":{"name":"read_file","args":"{\"path\":"}}]}}]}`,
		`{"_sub2api_continuation_hop":2,"candidates":[{"content":{"parts":[{"functionCall":{"name":"read_file","args":"\"b.go\"}"}}]},"finishReason":"STOP"}]}`,
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	resp := &http.Response{Body: io.NopCloser(strings.NewReader("data: " + strings.Join(chunks, "\n\ndata: ") + "\n\n"))}
	_, err := (&GeminiMessagesCompatService{}).handleStreamingResponse(c, resp, time.Now(), "gemini-4-argon", geminiTextDeltas)
	require.NoError(t, err)
	require.Equal(t, []geminiToolStreamBlock{
		{"tool_use", "read_file", `{"path":"a.go"}`},
		{"tool_use", "read_file", `{"path":"b.go"}`},
	}, readGeminiToolStreamBlocks(t, recorder.Body.String()))
}

func TestGeminiToolStreamForwardSameNameCalls(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, model := range []string{"gemini-4-argon", "gemini-2.5-pro"} {
		for _, oauth := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/oauth=%t", model, oauth), func(t *testing.T) {
				upstream := gemini4TestUpstream(func(req *http.Request) (*http.Response, error) {
					require.Contains(t, req.URL.Path, "streamGenerateContent")
					payload := `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"read_file","args":{"path":"a.go"}}},{"functionCall":{"name":"read_file","args":{"path":"b.go"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20}}`
					if oauth {
						payload = `{"response":` + payload + `}`
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("data: " + payload + "\n\n"))}, nil
				})
				svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}, tokenProvider: &GeminiTokenProvider{}}
				account := &Account{ID: 42, Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test"}}
				if oauth {
					account.Type = AccountTypeOAuth
					account.Credentials = map[string]any{"access_token": "test", "project_id": "project"}
				}
				body := fmt.Sprintf(`{"model":%q,"stream":true,"max_tokens":8192,"messages":[{"role":"user","content":"Read a.go and b.go"}],"tools":[{"name":"read_file","input_schema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}]}`, model)
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
				result, err := svc.Forward(c.Request.Context(), c, account, []byte(body))
				require.NoError(t, err)
				require.Equal(t, ClaudeUsage{InputTokens: 100, OutputTokens: 20}, result.Usage)
				require.Equal(t, []geminiToolStreamBlock{{"tool_use", "read_file", `{"path":"a.go"}`}, {"tool_use", "read_file", `{"path":"b.go"}`}}, readGeminiToolStreamBlocks(t, recorder.Body.String()))
			})
		}
	}
}
