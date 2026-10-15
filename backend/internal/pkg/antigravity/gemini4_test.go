package antigravity

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGemini4ThoughtUsageAndContinuation(t *testing.T) {
	for _, finish := range []string{"STOP", "CONTINUATION"} {
		body := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]},"finishReason":"` + finish + `"}],"usageMetadata":{"promptTokenCount":140,"cachedContentTokenCount":40,"candidatesTokenCount":10,"thoughtsTokenCount":20}}}`
		stop := "end_turn"
		if finish == "CONTINUATION" {
			stop = "max_tokens"
		}
		converted, usage, err := TransformGeminiToClaude([]byte(body), "gemini-4-argon", 42)
		require.NoError(t, err)
		require.Equal(t, 100, usage.InputTokens)
		require.Equal(t, 30, usage.OutputTokens)
		require.Equal(t, 40, usage.CacheReadInputTokens)
		require.Contains(t, string(converted), `"stop_reason":"`+stop+`"`)
		processor := NewStreamingProcessor("gemini-4-argon", 42)
		events := processor.ProcessLine("data: " + body)
		_, usage = processor.Finish()
		require.Equal(t, 30, usage.OutputTokens)
		require.Contains(t, string(events), `"stop_reason":"`+stop+`"`)
	}
}

func TestGemini4PreservesRequestedOutputLimit(t *testing.T) {
	request := &ClaudeRequest{
		Model: "gemini-4-argon", MaxTokens: 131072,
		Messages: []ClaudeMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}},
		Thinking: &ThinkingConfig{Type: "enabled", BudgetTokens: 200000},
	}
	body, err := TransformClaudeToGemini(request, "project", request.Model)
	require.NoError(t, err)
	var upstream V1InternalRequest
	require.NoError(t, json.Unmarshal(body, &upstream))
	require.Equal(t, request.MaxTokens, upstream.Request.GenerationConfig.MaxOutputTokens)
}
