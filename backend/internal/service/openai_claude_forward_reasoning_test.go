package service

import "testing"

func TestEnsureResponsesReasoning_AddsFallbackWhenMissing(t *testing.T) {
	reqBody := map[string]any{
		"model": "gpt-5.3-codex",
	}

	modified := ensureResponsesReasoning(reqBody)
	if !modified {
		t.Fatal("expected modified=true when reasoning is missing")
	}

	reasoning, ok := reqBody["reasoning"].(map[string]any)
	if !ok || reasoning == nil {
		t.Fatal("reasoning should be initialized")
	}
	if reasoning["effort"] != "xhigh" {
		t.Fatalf("effort = %v, want xhigh", reasoning["effort"])
	}
	if reasoning["summary"] != "auto" {
		t.Fatalf("summary = %v, want auto", reasoning["summary"])
	}
}

func TestEnsureResponsesReasoning_AddsEffortWhenEmpty(t *testing.T) {
	reqBody := map[string]any{
		"reasoning": map[string]any{},
	}

	modified := ensureResponsesReasoning(reqBody)
	if !modified {
		t.Fatal("expected modified=true when reasoning.effort is empty")
	}

	reasoning := reqBody["reasoning"].(map[string]any)
	if reasoning["effort"] != "xhigh" {
		t.Fatalf("effort = %v, want xhigh", reasoning["effort"])
	}
	if reasoning["summary"] != "auto" {
		t.Fatalf("summary = %v, want auto", reasoning["summary"])
	}
}

func TestEnsureResponsesReasoning_KeepExistingEffortAndAddSummary(t *testing.T) {
	reqBody := map[string]any{
		"reasoning": map[string]any{"effort": "high"},
	}

	modified := ensureResponsesReasoning(reqBody)
	if !modified {
		t.Fatal("expected modified=true when reasoning.summary is missing")
	}

	reasoning := reqBody["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" {
		t.Fatalf("effort = %v, want high", reasoning["effort"])
	}
	if reasoning["summary"] != "auto" {
		t.Fatalf("summary = %v, want auto", reasoning["summary"])
	}
}

func TestEnsureResponsesReasoning_KeepExistingReasoning(t *testing.T) {
	reqBody := map[string]any{
		"reasoning": map[string]any{
			"effort":  "high",
			"summary": "detailed",
		},
	}

	modified := ensureResponsesReasoning(reqBody)
	if modified {
		t.Fatal("expected modified=false when reasoning already has effort and summary")
	}
}
