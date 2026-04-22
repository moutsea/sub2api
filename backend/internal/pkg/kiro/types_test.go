package kiro

import "testing"

func TestGetModelIDMapsOpus47ToOpus47(t *testing.T) {
	got := GetModelID("claude-opus-4-7")
	if got != "claude-opus-4.7" {
		t.Fatalf("GetModelID(claude-opus-4-7) = %q, want %q", got, "claude-opus-4.7")
	}
}
