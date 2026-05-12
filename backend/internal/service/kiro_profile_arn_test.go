package service

import (
	"context"
	"strings"
	"testing"
)

func TestResolveKiroProfileArn_IdCMissingProfileArn(t *testing.T) {
	account := &Account{
		ID:       123,
		Platform: PlatformKiro,
		Credentials: map[string]any{
			"auth_type":     KiroAuthMethodIdC,
			"client_id":     "client",
			"client_secret": "secret",
		},
	}

	profileArn, err := resolveKiroProfileArn(context.Background(), account, nil, nil, "")
	if err == nil {
		t.Fatalf("expected missing profile_arn error")
	}
	if profileArn != "" {
		t.Fatalf("expected empty profileArn, got %q", profileArn)
	}
	if !strings.Contains(err.Error(), "missing profile_arn") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveKiroProfileArn_AcceptsCamelCaseCredential(t *testing.T) {
	want := "arn:aws:codewhisperer:us-east-1:123456789012:profile/ABCDEF"
	account := &Account{
		ID:       123,
		Platform: PlatformKiro,
		Credentials: map[string]any{
			"auth_type":  KiroAuthMethodIdC,
			"profileArn": want,
		},
	}

	got, err := resolveKiroProfileArn(context.Background(), account, nil, nil, "")
	if err != nil {
		t.Fatalf("resolveKiroProfileArn returned error: %v", err)
	}
	if got != want {
		t.Fatalf("profileArn = %q, want %q", got, want)
	}
}
