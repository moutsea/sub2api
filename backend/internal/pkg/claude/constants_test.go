package claude

import "testing"

func TestDefaultHeadersMatchCurrentClaudeCodeFingerprint(t *testing.T) {
	want := map[string]string{
		"User-Agent":                  "claude-cli/2.1.209 (external, cli)",
		"X-Stainless-Package-Version": "0.94.0",
		"X-Stainless-Runtime-Version": "v26.3.0",
	}

	for header, expected := range want {
		if got := DefaultHeaders[header]; got != expected {
			t.Fatalf("DefaultHeaders[%q] = %q, want %q", header, got, expected)
		}
	}

	headers := NewRequestHeaders()
	for header, expected := range want {
		if got := headers[header]; got != expected {
			t.Fatalf("NewRequestHeaders()[%q] = %q, want %q", header, got, expected)
		}
	}
	if headers["X-Stainless-OS"] == "" || headers["X-Stainless-Arch"] == "" {
		t.Fatal("NewRequestHeaders() did not include randomized platform headers")
	}
}
