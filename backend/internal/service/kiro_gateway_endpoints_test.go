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

func TestGetKiroEndpoints_DefaultSmallContext(t *testing.T) {
	account := newKiroAccount("")
	endpoints := getKiroEndpoints(account, 100000)

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

func TestGetKiroEndpoints_LargeContextSwitchesToCW(t *testing.T) {
	account := newKiroAccount("")
	endpoints := getKiroEndpoints(account, 160001)

	if len(endpoints) != 1 {
		t.Fatalf("expected 1 endpoint for large context, got %d", len(endpoints))
	}
	if endpoints[0].Name != "CodeWhisperer" {
		t.Errorf("expected CodeWhisperer only, got %s", endpoints[0].Name)
	}
}

func TestGetKiroEndpoints_BoundaryExact(t *testing.T) {
	account := newKiroAccount("")

	// Exactly at limit: should NOT trigger dynamic switch
	endpoints := getKiroEndpoints(account, 160000)
	if len(endpoints) != 2 {
		t.Fatalf("expected 2 endpoints at boundary, got %d", len(endpoints))
	}
	if endpoints[0].Name != "AWSQ" {
		t.Errorf("expected AWSQ first at boundary, got %s", endpoints[0].Name)
	}
}

func TestGetKiroEndpoints_PreferredAWSQ(t *testing.T) {
	account := newKiroAccount("awsq")

	// Even with large context, preferred_endpoint overrides dynamic logic
	endpoints := getKiroEndpoints(account, 200000)
	if len(endpoints) != 2 {
		t.Fatalf("expected 2 endpoints with preferred=awsq, got %d", len(endpoints))
	}
	if endpoints[0].Name != "AWSQ" {
		t.Errorf("expected AWSQ first with preferred=awsq, got %s", endpoints[0].Name)
	}
}

func TestGetKiroEndpoints_PreferredCW(t *testing.T) {
	account := newKiroAccount("cw")

	endpoints := getKiroEndpoints(account, 0)
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

func TestGetKiroEndpoints_PreferredOverridesDynamic(t *testing.T) {
	// preferred_endpoint = "awsq" should override even when context > limit
	account := newKiroAccount("awsq")
	endpoints := getKiroEndpoints(account, 180000)

	if len(endpoints) != 2 {
		t.Fatalf("expected 2 endpoints (preferred overrides dynamic), got %d", len(endpoints))
	}
	if endpoints[0].Name != "AWSQ" {
		t.Errorf("expected AWSQ first (preferred overrides dynamic), got %s", endpoints[0].Name)
	}
}

func TestGetKiroEndpoints_ZeroTokens(t *testing.T) {
	// Test connection scenario: estimatedTokens = 0
	account := newKiroAccount("")
	endpoints := getKiroEndpoints(account, 0)

	if len(endpoints) != 2 {
		t.Fatalf("expected 2 endpoints for zero tokens, got %d", len(endpoints))
	}
	if endpoints[0].Name != "AWSQ" {
		t.Errorf("expected AWSQ first for zero tokens, got %s", endpoints[0].Name)
	}
}
