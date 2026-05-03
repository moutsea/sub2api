package kiro

import "testing"

func TestGetModelIDMapsOpus47ToOpus47(t *testing.T) {
	got := GetModelID("claude-opus-4-7")
	if got != "claude-opus-4.7" {
		t.Fatalf("GetModelID(claude-opus-4-7) = %q, want %q", got, "claude-opus-4.7")
	}
}

func TestGetModelIDThinkingSuffix(t *testing.T) {
	tests := map[string]string{
		"claude-opus-4-6-thinking":            "claude-opus-4.6",
		"claude-sonnet-4-6-thinking":          "claude-sonnet-4.6",
		"claude-sonnet-4-5-20250929-thinking": "claude-sonnet-4.5",
		"claude-haiku-4-5-20251001-thinking":  "claude-haiku-4.5",
		"claude-opus-4-6-1m-thinking":         "claude-opus-4.6",
	}
	for model, want := range tests {
		if got := GetModelID(model); got != want {
			t.Fatalf("GetModelID(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestApplyThinkingDefaultsFromModelName(t *testing.T) {
	req := &ClaudeRequest{Model: "claude-opus-4-6-thinking"}
	if !ApplyThinkingDefaultsFromModelName(req) {
		t.Fatal("expected thinking defaults to be applied")
	}
	if req.Thinking["type"] != "adaptive" {
		t.Fatalf("thinking type = %v, want adaptive", req.Thinking["type"])
	}
	if req.OutputConfig["effort"] != "high" {
		t.Fatalf("output_config.effort = %v, want high", req.OutputConfig["effort"])
	}

	req = &ClaudeRequest{Model: "claude-sonnet-4-6-thinking"}
	if !ApplyThinkingDefaultsFromModelName(req) {
		t.Fatal("expected thinking defaults to be applied")
	}
	if req.Thinking["type"] != "enabled" {
		t.Fatalf("thinking type = %v, want enabled", req.Thinking["type"])
	}
}
