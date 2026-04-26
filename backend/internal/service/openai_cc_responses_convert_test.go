package service

import "testing"

func TestConvertCCRequestToResponses_PreservesServiceTier(t *testing.T) {
	body := map[string]any{
		"model":        "gpt-5.5",
		"service_tier": "priority",
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": "hello",
			},
		},
	}

	converted := convertCCRequestToResponses(body)
	serviceTier, _ := converted["service_tier"].(string)
	if serviceTier != "priority" {
		t.Fatalf("service_tier = %q, want %q", serviceTier, "priority")
	}
}
