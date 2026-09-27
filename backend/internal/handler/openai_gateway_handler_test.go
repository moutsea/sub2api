package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type responsesPreflightConcurrencyCache struct {
	service.ConcurrencyCache
	waitCountCalls int
}

func (cache *responsesPreflightConcurrencyCache) IncrementWaitCount(context.Context, int64, int) (bool, error) {
	cache.waitCountCalls++
	return false, nil
}

func TestResponsesToolContinuationPreflight(t *testing.T) {
	for _, platform := range []string{service.PlatformKiro, service.PlatformOpenAI, service.PlatformGrok} {
		t.Run(platform, func(t *testing.T) {
			for _, testCase := range []struct {
				name    string
				item    map[string]any
				allowed bool
			}{
				{"function", map[string]any{"type": "function_call", "call_id": "call_1", "name": "read", "arguments": `{}`}, true},
				{"legacy_tool", map[string]any{"type": "tool_call", "call_id": "call_1"}, true},
				{"local_shell", map[string]any{"type": "local_shell_call", "call_id": "call_1", "action": map[string]any{"type": "exec", "command": []string{"pwd"}}}, true},
				{"shell", map[string]any{"type": "shell_call", "call_id": "call_1", "action": map[string]any{"commands": []string{"pwd"}}}, true},
				{"custom", map[string]any{"type": "custom_tool_call", "call_id": "call_1", "name": "apply_patch", "input": "*** Begin Patch\n*** End Patch"}, true},
				{"apply_patch", map[string]any{"type": "apply_patch_call", "call_id": "call_1", "operation": map[string]any{"type": "delete_file", "path": "old.txt"}}, true},
				{"missing_call_id", map[string]any{"type": "apply_patch_call"}, false},
				{"blank_call_id", map[string]any{"type": "local_shell_call", "call_id": "  "}, false},
				{"function_output_only", map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "done"}, false},
				{"shell_output_only", map[string]any{"type": "shell_call_output", "call_id": "call_1", "output": "done"}, false},
				{"custom_output_only", map[string]any{"type": "custom_tool_call_output", "call_id": "call_1", "output": "done"}, false},
				{"patch_output_only", map[string]any{"type": "apply_patch_call_output", "call_id": "call_1", "output": "done"}, false},
			} {
				t.Run(testCase.name, func(t *testing.T) {
					cache := &responsesPreflightConcurrencyCache{}
					handler := NewOpenAIGatewayHandler(nil, nil, service.NewConcurrencyService(cache), nil, nil)
					router := gin.New()
					router.POST("/v1/responses", func(ctx *gin.Context) {
						ctx.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{Group: &service.Group{Platform: platform}})
						ctx.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 1, Concurrency: 1})
						handler.Responses(ctx)
					})
					body, err := json.Marshal(map[string]any{
						"model": "gpt-5.6-sol", "store": false,
						"input": []any{testCase.item, map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "done"}},
					})
					require.NoError(t, err)
					request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
					request.Header.Set("Content-Type", "application/json")
					request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")
					recorder := httptest.NewRecorder()
					router.ServeHTTP(recorder, request)
					if testCase.allowed {
						require.Equal(t, 1, cache.waitCountCalls)
						require.Equal(t, http.StatusTooManyRequests, recorder.Code, recorder.Body.String())
						require.Contains(t, recorder.Body.String(), "Too many pending requests")
					} else {
						require.Zero(t, cache.waitCountCalls)
						require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
						require.Contains(t, recorder.Body.String(), "function_call_output requires")
					}
				})
			}
		})
	}
}
