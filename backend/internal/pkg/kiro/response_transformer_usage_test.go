package kiro

import "testing"

func TestExtractTokenUsage_NormalizesInputWithCache(t *testing.T) {
	input, output, cacheCreation, cacheRead, has := extractTokenUsage(map[string]any{
		"inputTokens":              float64(120),
		"outputTokens":             float64(30),
		"cacheCreationInputTokens": float64(10),
		"cacheReadInputTokens":     float64(20),
	})
	if !has {
		t.Fatal("expected has=true")
	}
	if input != 90 {
		t.Fatalf("input=%d, want 90", input)
	}
	if output != 30 {
		t.Fatalf("output=%d, want 30", output)
	}
	if cacheCreation != 10 {
		t.Fatalf("cacheCreation=%d, want 10", cacheCreation)
	}
	if cacheRead != 20 {
		t.Fatalf("cacheRead=%d, want 20", cacheRead)
	}
}

func TestAwsEventStreamParser_EmitsTokenUsageEvent(t *testing.T) {
	parser := NewAwsEventStreamParser("msg_test", "claude-sonnet-4-20250514")
	events := parser.Process([]byte(`{"tokenUsage":{"inputTokens":120,"outputTokens":30,"cacheCreationInputTokens":10,"cacheReadInputTokens":20,"contextUsagePercentage":55.5}}`))

	var usageEvent *StreamEvent
	for i := range events {
		if events[i].Type == EventBackendUsage {
			usageEvent = &events[i]
			break
		}
	}
	if usageEvent == nil {
		t.Fatalf("expected EventBackendUsage, got events=%v", events)
	}
	if !usageEvent.HasTokenUsage {
		t.Fatal("expected HasTokenUsage=true")
	}
	if usageEvent.InputTokens != 90 {
		t.Fatalf("input_tokens=%d, want 90", usageEvent.InputTokens)
	}
	if usageEvent.OutputTokens != 30 {
		t.Fatalf("output_tokens=%d, want 30", usageEvent.OutputTokens)
	}
	if usageEvent.CacheCreationInputTokens != 10 {
		t.Fatalf("cache_creation_input_tokens=%d, want 10", usageEvent.CacheCreationInputTokens)
	}
	if usageEvent.CacheReadInputTokens != 20 {
		t.Fatalf("cache_read_input_tokens=%d, want 20", usageEvent.CacheReadInputTokens)
	}
	if usageEvent.ContextPercentage != 55.5 {
		t.Fatalf("context_usage_percentage=%f, want 55.5", usageEvent.ContextPercentage)
	}
}

func TestParseCompleteResponseWithNameRestore_UsesUpstreamTokenUsage(t *testing.T) {
	// Simulate non-stream payload composed of multiple JSON event objects.
	payload := []byte(`{"content":"hello"}{"tokenUsage":{"inputTokens":42,"outputTokens":7,"cacheReadInputTokens":5}}{"stop":true}`)
	resp := ParseCompleteResponseWithNameRestore(payload, nil)

	if resp == nil {
		t.Fatal("resp should not be nil")
	}
	if !resp.HasTokenUsage {
		t.Fatal("expected HasTokenUsage=true")
	}
	if resp.InputTokens != 37 {
		t.Fatalf("input_tokens=%d, want 37", resp.InputTokens)
	}
	if resp.OutputTokens != 7 {
		t.Fatalf("output_tokens=%d, want 7", resp.OutputTokens)
	}
	if resp.CacheReadTokens != 5 {
		t.Fatalf("cache_read_input_tokens=%d, want 5", resp.CacheReadTokens)
	}
}
