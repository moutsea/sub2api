package kiro

import "testing"

func TestGetModelIDMapsOpus47ToOpus47(t *testing.T) {
	got := GetModelID("claude-opus-4-7")
	if got != "claude-opus-4.7" {
		t.Fatalf("GetModelID(claude-opus-4-7) = %q, want %q", got, "claude-opus-4.7")
	}
}

func TestGetModelIDMapsKiroOAuthModels(t *testing.T) {
	tests := map[string]string{
		"auto":                     "auto",
		"deepseek-3.2":             "deepseek-3.2",
		"deepseek-v3.2":            "deepseek-3.2",
		"glm-5":                    "glm-5",
		"minimax-m2.5":             "minimax-m2.5",
		"minimax-m2.1":             "minimax-m2.1",
		"qwen3-coder-next":         "qwen3-coder-next",
		"qwen3-coder-480b-a35b":    "qwen3-coder-next",
		"gpt-5.6-sol":              "gpt-5.6-sol",
		"gpt-5.6-terra":            "gpt-5.6-terra",
		"gpt-5.6-luna":             "gpt-5.6-luna",
		"claude-sonnet-5":          "claude-sonnet-5",
		"claude-sonnet-5.0":        "claude-sonnet-5",
		"claude-sonnet-5-thinking": "claude-sonnet-5",
		"claude-opus-5":            "claude-opus-5",
		"claude-opus-5.0":          "claude-opus-5",
		"claude-opus-5-thinking":   "claude-opus-5",
		"claude-opus-5-5":          "claude-opus-5.5",
		"claude-opus-5.5":          "claude-opus-5.5",
		"claude-opus-5-5-thinking": "claude-opus-5.5",
		"claude-opus-5.5-thinking": "claude-opus-5.5",
		"claude-sonnet-4":          "claude-sonnet-4",
		"claude-sonnet-4.5":        "claude-sonnet-4.5",
		"claude-opus-4-8":          "claude-opus-4.8",
		"claude-opus-4.8":          "claude-opus-4.8",
		"claude-opus-4.7":          "claude-opus-4.7",
	}
	for model, want := range tests {
		if got := GetModelID(model); got != want {
			t.Fatalf("GetModelID(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestIsOAuthModelSupported(t *testing.T) {
	tests := map[string]bool{
		"auto":                     true,
		"deepseek-3.2":             true,
		"glm-5":                    true,
		"qwen3-coder-next":         true,
		"gpt-5.6":                  false,
		"gpt-5.6-sol":              true,
		"gpt-5.6-terra":            true,
		"gpt-5.6-luna":             true,
		"claude-sonnet-5":          true,
		"claude-sonnet-5-thinking": true,
		"claude-opus-5":            true,
		"claude-opus-5-thinking":   true,
		"claude-opus-5-5":          true,
		"claude-opus-5.5":          true,
		"claude-opus-5-5-thinking": true,
		"claude-opus-5.5-thinking": true,
		"claude-opus-4-8":          true,
		"claude-opus-4.8":          true,
		"claude-opus-4-8-thinking": true,
		"claude-opus-4-6-thinking": true,
		"deepseek-3.2-thinking":    false,
		"gpt-5.6-sol-thinking":     false,
		"unsupported-future-model": false,
	}
	for model, want := range tests {
		if got := IsOAuthModelSupported(model); got != want {
			t.Fatalf("IsOAuthModelSupported(%q) = %v, want %v", model, got, want)
		}
	}
}

func TestGetContextWindowLimitForKiroOAuthModels(t *testing.T) {
	tests := map[string]int{
		"deepseek-3.2":             KiroContextWindowLimit128K,
		"glm-5":                    KiroContextWindowLimit,
		"minimax-m2.5":             KiroContextWindowLimit,
		"qwen3-coder-next":         KiroContextWindowLimit256K,
		"gpt-5.6-sol":              KiroContextWindowLimit1M,
		" GPT-5.6-SOL ":            KiroContextWindowLimit1M,
		"gpt-5.6-terra":            KiroContextWindowLimit1M,
		"gpt-5.6-luna":             KiroContextWindowLimit1M,
		"claude-sonnet-5":          KiroContextWindowLimit1M,
		"claude-sonnet-5-thinking": KiroContextWindowLimit1M,
		"claude-opus-5":            KiroContextWindowLimit1M,
		"claude-opus-5-thinking":   KiroContextWindowLimit1M,
		"claude-opus-5-5":          KiroContextWindowLimit1M,
		"claude-opus-5.5":          KiroContextWindowLimit1M,
		"claude-opus-5-5-thinking": KiroContextWindowLimit1M,
		"claude-opus-5.5-thinking": KiroContextWindowLimit1M,
		"claude-opus-4-8":          KiroContextWindowLimit1M,
		"claude-opus-4.8":          KiroContextWindowLimit1M,
		"claude-opus-4.7":          KiroContextWindowLimit1M,
	}
	for model, want := range tests {
		if got := GetContextWindowLimit(model); got != want {
			t.Fatalf("GetContextWindowLimit(%q) = %d, want %d", model, got, want)
		}
	}
}

func TestDefaultModelsIncludeGPT56Series(t *testing.T) {
	found := make(map[string]bool, len(DefaultModels))
	for _, model := range DefaultModels {
		found[model.ID] = true
	}

	for _, modelID := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		if !found[modelID] {
			t.Fatalf("DefaultModels does not include %q", modelID)
		}
	}
	if found["gpt-5.6"] {
		t.Fatal("DefaultModels unexpectedly includes unsupported gpt-5.6 alias")
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

	req = &ClaudeRequest{Model: "claude-opus-4-8-thinking"}
	if !ApplyThinkingDefaultsFromModelName(req) {
		t.Fatal("expected thinking defaults to be applied")
	}
	if req.Thinking["type"] != "enabled" {
		t.Fatalf("thinking type = %v, want enabled", req.Thinking["type"])
	}
}

func TestApplyThinkingDefaultsFromModelNameSkipsSonnet5(t *testing.T) {
	req := &ClaudeRequest{Model: "claude-sonnet-5-thinking"}
	if ApplyThinkingDefaultsFromModelName(req) {
		t.Fatal("sonnet 5 adaptive thinking is upstream-default and should not inject manual thinking")
	}
	if req.Thinking != nil {
		t.Fatalf("thinking = %v, want nil", req.Thinking)
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
