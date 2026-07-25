package service

import (
	"strings"
	"testing"
)

func TestSanitizeKiroClientErrorMessage(t *testing.T) {
	message := `request failed for profileArn=arn:aws:codewhisperer:us-east-1:123456789012:profile/ABC profile_id=internal-profile Authorization: Bearer secret-token access_token=plain-secret?key=query-secret&refresh_token=query-refresh`
	got := sanitizeKiroClientErrorMessage(message)

	for _, forbidden := range []string{
		"arn:aws:",
		"123456789012",
		"internal-profile",
		"secret-token",
		"plain-secret",
		"query-secret",
		"query-refresh",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("sanitized message leaked %q: %s", forbidden, got)
		}
	}
	if !strings.Contains(got, "request failed") {
		t.Fatalf("sanitized message lost useful context: %s", got)
	}
}

func TestSanitizeKiroClientErrorMessageLimitsLength(t *testing.T) {
	got := sanitizeKiroClientErrorMessage(strings.Repeat("x", maxKiroClientErrorMessageLength+100))
	if len(got) != maxKiroClientErrorMessageLength {
		t.Fatalf("length = %d, want %d", len(got), maxKiroClientErrorMessageLength)
	}
}
