package kiro

import (
	"encoding/json"
	"testing"
)

func TestDetectToolInputTruncation_ValidJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "complete object",
			input: `{"file_path":"/foo/bar.go","content":"hello world"}`,
			want:  false,
		},
		{
			name:  "complete array",
			input: `[1,2,3]`,
			want:  false,
		},
		{
			name:  "boolean true",
			input: `{"enabled":true}`,
			want:  false,
		},
		{
			name:  "boolean false",
			input: `{"enabled":false}`,
			want:  false,
		},
		{
			name:  "null value",
			input: `{"value":null}`,
			want:  false,
		},
		{
			name:  "number value",
			input: `{"count":42}`,
			want:  false,
		},
		{
			name:  "nested object",
			input: `{"a":{"b":{"c":"d"}}}`,
			want:  false,
		},
		{
			name:  "string with escaped quotes",
			input: `{"msg":"he said \"hello\""}`,
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectToolInputTruncation("test_tool", tt.input)
			if got != tt.want {
				t.Errorf("DetectToolInputTruncation(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestDetectToolInputTruncation_Truncated(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "empty string",
			input: "",
			want:  true,
		},
		{
			name:  "unclosed brace",
			input: `{"file_path": "/foo", "content": "hello`,
			want:  true,
		},
		{
			name:  "unclosed bracket",
			input: `[1, 2, 3`,
			want:  true,
		},
		{
			name:  "trailing colon",
			input: `{"file_path":`,
			want:  true,
		},
		{
			name:  "trailing comma",
			input: `{"file_path": "/foo",`,
			want:  true,
		},
		{
			name:  "unclosed string in value",
			input: `{"content": "this is a long string that got cut`,
			want:  true,
		},
		{
			name:  "deeply nested truncation",
			input: `{"a":{"b":{"c":"d`,
			want:  true,
		},
		{
			name:  "truncated mid-key",
			input: `{"file_pa`,
			want:  true,
		},
		{
			name:  "truncated after opening brace",
			input: `{`,
			want:  true,
		},
		{
			name:  "truncated array mid-string",
			input: `["hello", "wor`,
			want:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectToolInputTruncation("test_tool", tt.input)
			if got != tt.want {
				t.Errorf("DetectToolInputTruncation(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestBuildSoftLimitInput(t *testing.T) {
	result := BuildSoftLimitInput()

	status, ok := result["_status"]
	if !ok || status != "SOFT_LIMIT_REACHED" {
		t.Errorf("expected _status=SOFT_LIMIT_REACHED, got %v", status)
	}

	msg, ok := result["_message"]
	if !ok {
		t.Error("expected _message field")
	}
	msgStr, ok := msg.(string)
	if !ok || msgStr == "" {
		t.Error("expected non-empty _message string")
	}

	// Verify it marshals to valid JSON
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("BuildSoftLimitInput() result not JSON-serializable: %v", err)
	}
	if len(data) == 0 {
		t.Error("expected non-empty JSON output")
	}
}
