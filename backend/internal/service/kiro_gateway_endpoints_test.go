package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
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

func legacyCfg() *config.Config {
	return &config.Config{Kiro: config.KiroConfig{ServiceEndpointFamily: "legacy"}}
}

func kiroCfg() *config.Config {
	return &config.Config{Kiro: config.KiroConfig{ServiceEndpointFamily: "kiro"}}
}

func TestGetKiroEndpoints_Default(t *testing.T) {
	account := newKiroAccount("")
	endpoints := getKiroEndpoints(account, legacyCfg())

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
	endpoints := getKiroEndpoints(account, legacyCfg())

	if len(endpoints) != 2 {
		t.Fatalf("expected 2 endpoints with preferred=awsq, got %d", len(endpoints))
	}
	if endpoints[0].Name != "AWSQ" {
		t.Errorf("expected AWSQ first with preferred=awsq, got %s", endpoints[0].Name)
	}
}

func TestGetKiroEndpoints_PreferredCW(t *testing.T) {
	account := newKiroAccount("cw")
	endpoints := getKiroEndpoints(account, legacyCfg())

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
	cfg := legacyCfg()
	// Test all AWSQ aliases
	for _, alias := range []string{"awsq", "q", "cli"} {
		account := newKiroAccount(alias)
		endpoints := getKiroEndpoints(account, cfg)
		if endpoints[0].Name != "AWSQ" {
			t.Errorf("alias %q: expected AWSQ first, got %s", alias, endpoints[0].Name)
		}
	}
	// Test all CW aliases
	for _, alias := range []string{"cw", "codewhisperer", "kiro"} {
		account := newKiroAccount(alias)
		endpoints := getKiroEndpoints(account, cfg)
		if endpoints[0].Name != "CodeWhisperer" {
			t.Errorf("alias %q: expected CodeWhisperer first, got %s", alias, endpoints[0].Name)
		}
	}
}

func TestGetKiroEndpoints_KiroFamily(t *testing.T) {
	account := newKiroAccount("")
	endpoints := getKiroEndpoints(account, kiroCfg())

	if len(endpoints) != 1 {
		t.Fatalf("kiro family should return single endpoint, got %d", len(endpoints))
	}
	if endpoints[0].Host != "runtime.us-east-1.kiro.dev" {
		t.Errorf("expected runtime.us-east-1.kiro.dev host, got %s", endpoints[0].Host)
	}
	if endpoints[0].URL != "https://runtime.us-east-1.kiro.dev/generateAssistantResponse" {
		t.Errorf("unexpected URL: %s", endpoints[0].URL)
	}
	if endpoints[0].AmzTarget != "" {
		t.Errorf("kiro family should not set X-Amz-Target, got %q", endpoints[0].AmzTarget)
	}
}

func TestGetKiroEndpoints_KiroFamilyIgnoresAccountPreference(t *testing.T) {
	// Per-account preferred_endpoint only applies under legacy family.
	account := newKiroAccount("cw")
	endpoints := getKiroEndpoints(account, kiroCfg())

	if len(endpoints) != 1 {
		t.Fatalf("kiro family should return single endpoint regardless of preference, got %d", len(endpoints))
	}
	if endpoints[0].Host != "runtime.us-east-1.kiro.dev" {
		t.Errorf("expected runtime host, got %s", endpoints[0].Host)
	}
}

func TestGetKiroEndpoints_NilConfigFallsBackToKiro(t *testing.T) {
	account := newKiroAccount("")
	endpoints := getKiroEndpoints(account, nil)

	if len(endpoints) != 1 {
		t.Fatalf("nil cfg should fall back to kiro family (single endpoint), got %d", len(endpoints))
	}
	if endpoints[0].Host != "runtime.us-east-1.kiro.dev" {
		t.Errorf("expected runtime host on nil cfg, got %s", endpoints[0].Host)
	}
}
