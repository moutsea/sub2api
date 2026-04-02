package service

import (
	"runtime"
	"testing"
)

func TestKiroOIDCOSName_ReturnsNonEmpty(t *testing.T) {
	t.Parallel()
	result := kiroOIDCOSName()
	if result == "" {
		t.Fatal("kiroOIDCOSName() returned empty string")
	}
}

func TestKiroOIDCOSName_MatchesExpectedValues(t *testing.T) {
	t.Parallel()
	result := kiroOIDCOSName()

	validValues := map[string]bool{
		"macOS":   true,
		"windows": true,
		"linux":   true,
	}
	if !validValues[result] {
		t.Fatalf("kiroOIDCOSName() = %q, want one of macOS, windows, linux", result)
	}
}

func TestKiroOIDCOSName_MatchesCurrentPlatform(t *testing.T) {
	t.Parallel()
	result := kiroOIDCOSName()

	var expected string
	switch runtime.GOOS {
	case "darwin":
		expected = "macOS"
	case "windows":
		expected = "windows"
	default:
		expected = "linux"
	}

	if result != expected {
		t.Fatalf("kiroOIDCOSName() = %q on GOOS=%q, want %q", result, runtime.GOOS, expected)
	}
}

func TestKiroOIDCOSName_NoHashSeparator(t *testing.T) {
	t.Parallel()
	result := kiroOIDCOSName()
	// OIDC OS name should NOT contain '#' (unlike usage limits OS name)
	for _, ch := range result {
		if ch == '#' {
			t.Fatalf("kiroOIDCOSName() = %q, should not contain '#'", result)
		}
	}
}
