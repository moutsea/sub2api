package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImageGenerationIntentToolChoice(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		choice string
		want   bool
	}{
		{name: "omitted", want: true},
		{name: "null", choice: `null`, want: true},
		{name: "auto", choice: `"auto"`, want: true},
		{name: "required", choice: `"required"`, want: true},
		{name: "none", choice: `"none"`},
		{name: "explicit image", choice: `{"type":"image_generation"}`, want: true},
		{name: "image string", choice: `"image_generation"`, want: true},
		{name: "nested image", choice: `{"tool":{"type":"image_generation"}}`, want: true},
		{name: "other builtin", choice: `{"type":"web_search"}`},
		{name: "other function", choice: `{"type":"function","name":"search"}`},
		{name: "function named image generation", choice: `{"type":"function","name":"image_generation"}`},
		{name: "nested function named image generation", choice: `{"type":"function","function":{"name":"image_generation"}}`, want: true},
		{name: "allowed image", choice: `{"type":"allowed_tools","mode":"auto","tools":[{"type":"image_generation"}]}`, want: true},
		{name: "required allowed image", choice: `{"type":"allowed_tools","mode":"required","tools":[{"type":"image_generation"}]}`, want: true},
		{name: "allowed other tool", choice: `{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"search"}]}`},
		{name: "allowed image named function", choice: `{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"image_generation"}]}`},
		{name: "empty allowed tools", choice: `{"type":"allowed_tools","mode":"auto","tools":[]}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			body := `{"model":"gpt-5.5","tools":[{"type":"image_generation"},{"type":"function","name":"search"},{"type":"function","name":"image_generation"}]`
			if testCase.choice != "" {
				body += `,"tool_choice":` + testCase.choice
			}
			body += `}`
			var request map[string]any
			require.NoError(t, json.Unmarshal([]byte(body), &request))
			require.Equal(t, testCase.want, IsImageGenerationIntent("/v1/responses", "gpt-5.5", []byte(body)))
			require.Equal(t, testCase.want, IsImageGenerationIntentMap("/v1/responses", "gpt-5.5", request))
		})
	}
}
