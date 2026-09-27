package service

import "testing"

func TestUpdateClaudeUsageFromSSEData_MessageStartAndDelta(t *testing.T) {
	usage := &ClaudeUsage{}

	startUpdated := updateClaudeUsageFromSSEData(`{"type":"message_start","message":{"usage":{"input_tokens":123,"cache_creation_input_tokens":11}}}`, usage)
	if !startUpdated {
		t.Fatal("expected message_start usage to be parsed")
	}
	if usage.InputTokens != 123 {
		t.Fatalf("input_tokens=%d, want 123", usage.InputTokens)
	}
	if usage.CacheCreationInputTokens != 11 {
		t.Fatalf("cache_creation_input_tokens=%d, want 11", usage.CacheCreationInputTokens)
	}

	deltaUpdated := updateClaudeUsageFromSSEData(`{"type":"message_delta","usage":{"output_tokens":45,"cache_read_input_tokens":6}}`, usage)
	if !deltaUpdated {
		t.Fatal("expected message_delta usage to be parsed")
	}
	if usage.OutputTokens != 45 {
		t.Fatalf("output_tokens=%d, want 45", usage.OutputTokens)
	}
	if usage.InputTokens != 123 {
		t.Fatalf("input_tokens=%d after delta, want preserved 123", usage.InputTokens)
	}
	if usage.CacheCreationInputTokens != 11 {
		t.Fatalf("cache_creation_input_tokens=%d after delta, want preserved 11", usage.CacheCreationInputTokens)
	}
	if usage.CacheReadInputTokens != 6 {
		t.Fatalf("cache_read_input_tokens=%d, want 6", usage.CacheReadInputTokens)
	}
}

func TestUpdateClaudeUsageFromSSEData_CacheCreationBreakdown(t *testing.T) {
	usage := &ClaudeUsage{}
	updated := updateClaudeUsageFromSSEData(`{"type":"message_start","message":{"usage":{"input_tokens":1,"cache_creation_input_tokens":30,"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":20}}}}`, usage)
	if !updated || usage.CacheCreation5mTokens != 10 || usage.CacheCreation1hTokens != 20 {
		t.Fatalf("stream cache creation usage = %+v, updated = %v", usage, updated)
	}
}

func TestApplyClaudeUsageMap_ExplicitZeroOverwritesExistingValue(t *testing.T) {
	usage := &ClaudeUsage{InputTokens: 123, CacheReadInputTokens: 456}

	updated := applyClaudeUsageMap(map[string]any{
		"input_tokens":            0,
		"cache_read_input_tokens": 0,
	}, usage, true)

	if updated {
		t.Fatal("zero-only usage update should not count as positive upstream usage")
	}
	if usage.InputTokens != 0 {
		t.Fatalf("input_tokens=%d, want explicit zero", usage.InputTokens)
	}
	if usage.CacheReadInputTokens != 0 {
		t.Fatalf("cache_read_input_tokens=%d, want explicit zero", usage.CacheReadInputTokens)
	}
}

func TestExtractClaudeUsageFromJSON(t *testing.T) {
	body := []byte(`{"type":"message","usage":{"input_tokens":77,"output_tokens":13,"cache_creation_input_tokens":8,"cache_creation":{"ephemeral_5m_input_tokens":3,"ephemeral_1h_input_tokens":5},"cache_read_input_tokens":9}}`)

	usage, ok := extractClaudeUsageFromJSON(body)
	if !ok {
		t.Fatal("expected usage to be extracted")
	}
	if usage.InputTokens != 77 {
		t.Fatalf("input_tokens=%d, want 77", usage.InputTokens)
	}
	if usage.OutputTokens != 13 {
		t.Fatalf("output_tokens=%d, want 13", usage.OutputTokens)
	}
	if usage.CacheCreationInputTokens != 8 {
		t.Fatalf("cache_creation_input_tokens=%d, want 8", usage.CacheCreationInputTokens)
	}
	if usage.CacheCreation5mTokens != 3 || usage.CacheCreation1hTokens != 5 {
		t.Fatalf("cache creation breakdown=%d/%d, want 3/5", usage.CacheCreation5mTokens, usage.CacheCreation1hTokens)
	}
	if usage.CacheReadInputTokens != 9 {
		t.Fatalf("cache_read_input_tokens=%d, want 9", usage.CacheReadInputTokens)
	}
}
