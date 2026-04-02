package kiro

import (
	"runtime"
	"strings"
	"testing"
)

func TestUsageLimitsOSName_ReturnsNonEmpty(t *testing.T) {
	t.Parallel()
	result := usageLimitsOSName()
	if result == "" {
		t.Fatal("usageLimitsOSName() returned empty string")
	}
}

func TestUsageLimitsOSName_ContainsHashSeparator(t *testing.T) {
	t.Parallel()
	result := usageLimitsOSName()
	if !strings.Contains(result, "#") {
		t.Fatalf("usageLimitsOSName() = %q, want format 'os#version' with '#' separator", result)
	}
}

func TestUsageLimitsOSName_MatchesExpectedValues(t *testing.T) {
	t.Parallel()
	result := usageLimitsOSName()

	validValues := map[string]bool{
		"darwin#24.6.0":  true,
		"windows#10.0":   true,
		"linux#6.1.0":    true,
	}
	if !validValues[result] {
		t.Fatalf("usageLimitsOSName() = %q, want one of darwin#24.6.0, windows#10.0, linux#6.1.0", result)
	}
}

func TestUsageLimitsOSName_MatchesCurrentPlatform(t *testing.T) {
	t.Parallel()
	result := usageLimitsOSName()

	var expected string
	switch runtime.GOOS {
	case "darwin":
		expected = "darwin#24.6.0"
	case "windows":
		expected = "windows#10.0"
	default:
		expected = "linux#6.1.0"
	}

	if result != expected {
		t.Fatalf("usageLimitsOSName() = %q on GOOS=%q, want %q", result, runtime.GOOS, expected)
	}
}

func TestUsageLimitsOSName_FormatOSHashVersion(t *testing.T) {
	t.Parallel()
	result := usageLimitsOSName()

	parts := strings.SplitN(result, "#", 2)
	if len(parts) != 2 {
		t.Fatalf("usageLimitsOSName() = %q, expected exactly one '#' separator", result)
	}
	osName := parts[0]
	version := parts[1]

	if osName == "" {
		t.Fatal("OS name part is empty")
	}
	if version == "" {
		t.Fatal("version part is empty")
	}
	// Version should look like a semver-ish string (digits and dots)
	for _, ch := range version {
		if ch != '.' && (ch < '0' || ch > '9') {
			t.Fatalf("version part %q contains unexpected character %q", version, string(ch))
		}
	}
}
