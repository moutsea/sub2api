package service

import (
	"testing"
)

// newKiroAccount creates a minimal Kiro account for endpoint routing tests.
func newKiroAccount(preferredEndpoint string) *Account {
	creds := map[string]any{}
	if preferredEndpoint != "" {
		creds["preferred_endpoint"] = preferredEndpoint
	}
	return &Account{
		Platform:    PlatformKiro,
		Credentials: creds,
	}
}

func TestGetKiroEndpoints_Default(t *testing.T) {
	account := newKiroAccount("")
	endpoints := getKiroEndpoints(account)

	if len(endpoints) != 2 {
		t.Fatalf("expected 2 endpoints, got %d", len(endpoints))
	}
	if endpoints[0].Name != "AWSQ" {
		t.Errorf("expected first endpoint AWSQ, got %s", endpoints[0].Name)
	}
	if endpoints[1].Name != "CodeWhisperer" {
		t.Errorf("expected second endpoint CodeWhisperer, got %s", endpoints[1].Name)
	}
}

func TestGetKiroEndpoints_PreferredCW(t *testing.T) {
	account := newKiroAccount("cw")

	endpoints := getKiroEndpoints(account)
	if len(endpoints) != 2 {
		t.Fatalf("expected 2 endpoints with preferred=cw, got %d", len(endpoints))
	}
	if endpoints[0].Name != "CodeWhisperer" {
		t.Errorf("expected CodeWhisperer first with preferred=cw, got %s", endpoints[0].Name)
	}
	if endpoints[1].Name != "AWSQ" {
		t.Errorf("expected AWSQ second with preferred=cw, got %s", endpoints[1].Name)
	}
}

func TestGetKiroEndpoints_PreferredKiro(t *testing.T) {
	account := newKiroAccount("kiro")

	endpoints := getKiroEndpoints(account)
	if len(endpoints) != 2 {
		t.Fatalf("expected 2 endpoints with preferred=kiro, got %d", len(endpoints))
	}
	if endpoints[0].Name != "CodeWhisperer" {
		t.Errorf("expected CodeWhisperer first with preferred=kiro, got %s", endpoints[0].Name)
	}
}

func TestGetKiroEndpoints_UnknownPreferred(t *testing.T) {
	// Unknown preferred_endpoint should fall back to default (AWSQ first)
	account := newKiroAccount("something_else")

	endpoints := getKiroEndpoints(account)
	if len(endpoints) != 2 {
		t.Fatalf("expected 2 endpoints for unknown preferred, got %d", len(endpoints))
	}
	if endpoints[0].Name != "AWSQ" {
		t.Errorf("expected AWSQ first for unknown preferred, got %s", endpoints[0].Name)
	}
}
