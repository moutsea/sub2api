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

func TestGetKiroEndpoints_PreferredAWSQ(t *testing.T) {
	account := newKiroAccount("awsq")
	endpoints := getKiroEndpoints(account)

	if len(endpoints) != 2 {
		t.Fatalf("expected 2 endpoints with preferred=awsq, got %d", len(endpoints))
	}
	if endpoints[0].Name != "AWSQ" {
		t.Errorf("expected AWSQ first with preferred=awsq, got %s", endpoints[0].Name)
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

func TestGetKiroEndpoints_PreferredAliases(t *testing.T) {
	// Test all AWSQ aliases
	for _, alias := range []string{"awsq", "q", "cli"} {
		account := newKiroAccount(alias)
		endpoints := getKiroEndpoints(account)
		if endpoints[0].Name != "AWSQ" {
			t.Errorf("alias %q: expected AWSQ first, got %s", alias, endpoints[0].Name)
		}
	}
	// Test all CW aliases
	for _, alias := range []string{"cw", "codewhisperer", "kiro"} {
		account := newKiroAccount(alias)
		endpoints := getKiroEndpoints(account)
		if endpoints[0].Name != "CodeWhisperer" {
			t.Errorf("alias %q: expected CodeWhisperer first, got %s", alias, endpoints[0].Name)
		}
	}
}
