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

func TestIsThinkingModelNameRequiresSuffix(t *testing.T) {
	tests := map[string]bool{
		"claude-opus-4-6-thinking":   true,
		" CLAUDE-OPUS-4-6-THINKING ": true,
		"claude-overthinking-test":   false,
		"thinking-claude-opus-4-6":   false,
		"claude-opus-thinking-test":  false,
		"claude-opus-4-6":            false,
	}
	for model, want := range tests {
		if got := IsThinkingModelName(model); got != want {
			t.Fatalf("IsThinkingModelName(%q) = %v, want %v", model, got, want)
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

func TestApplyThinkingDefaultsFromModelNameDoesNotOverrideExplicitThinking(t *testing.T) {
	req := &ClaudeRequest{
		Model:    "claude-opus-4-6-thinking",
		Thinking: map[string]any{"type": "enabled", "budget_tokens": float64(50000)},
		OutputConfig: map[string]any{
			"effort": "medium",
		},
	}

	if ApplyThinkingDefaultsFromModelName(req) {
		t.Fatal("explicit thinking should not be overwritten by model alias defaults")
	}
	if req.Thinking["type"] != "enabled" {
		t.Fatalf("thinking type = %v, want enabled", req.Thinking["type"])
	}
	if req.Thinking["budget_tokens"] != float64(50000) {
		t.Fatalf("budget_tokens = %v, want 50000", req.Thinking["budget_tokens"])
	}
	if req.OutputConfig["effort"] != "medium" {
		t.Fatalf("output_config.effort = %v, want medium", req.OutputConfig["effort"])
	}
}

func TestApplyThinkingDefaultsFromModelNamePreservesExplicitEffort(t *testing.T) {
	req := &ClaudeRequest{
		Model:        "claude-opus-4-6-thinking",
		OutputConfig: map[string]any{"effort": "medium"},
	}

	if !ApplyThinkingDefaultsFromModelName(req) {
		t.Fatal("expected thinking defaults to be applied")
	}
	if req.Thinking["type"] != "adaptive" {
		t.Fatalf("thinking type = %v, want adaptive", req.Thinking["type"])
	}
	if req.OutputConfig["effort"] != "medium" {
		t.Fatalf("output_config.effort = %v, want medium", req.OutputConfig["effort"])
	}
}

func TestIsThinkingConfigEnabled(t *testing.T) {
	tests := []struct {
		name string
		req  *ClaudeRequest
		want bool
	}{
		{name: "nil request", req: nil, want: false},
		{name: "nil thinking", req: &ClaudeRequest{}, want: false},
		{name: "enabled", req: &ClaudeRequest{Thinking: map[string]any{"type": "enabled"}}, want: true},
		{name: "adaptive", req: &ClaudeRequest{Thinking: map[string]any{"type": "adaptive"}}, want: true},
		{name: "disabled", req: &ClaudeRequest{Thinking: map[string]any{"type": "disabled"}}, want: false},
		{name: "empty", req: &ClaudeRequest{Thinking: map[string]any{"type": ""}}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsThinkingConfigEnabled(tt.req); got != tt.want {
				t.Fatalf("IsThinkingConfigEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}
