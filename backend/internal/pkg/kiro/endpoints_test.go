package kiro

import "testing"

func TestRuntimeHost(t *testing.T) {
	tests := []struct {
		region string
		family ServiceEndpointFamily
		want   string
	}{
		{"us-east-1", ServiceEndpointFamilyKiro, "runtime.us-east-1.kiro.dev"},
		{"eu-central-1", ServiceEndpointFamilyKiro, "runtime.eu-central-1.kiro.dev"},
		{"us-east-1", ServiceEndpointFamilyLegacy, "q.us-east-1.amazonaws.com"},
		{"eu-central-1", ServiceEndpointFamilyLegacy, "q.eu-central-1.amazonaws.com"},
		{"", ServiceEndpointFamilyKiro, "runtime.us-east-1.kiro.dev"},
		{"", ServiceEndpointFamilyLegacy, "q.us-east-1.amazonaws.com"},
	}
	for _, tt := range tests {
		got := RuntimeHost(tt.region, tt.family)
		if got != tt.want {
			t.Errorf("RuntimeHost(%q, %q) = %q, want %q", tt.region, tt.family, got, tt.want)
		}
	}
}

func TestManagementHost(t *testing.T) {
	tests := []struct {
		region string
		family ServiceEndpointFamily
		want   string
	}{
		{"us-east-1", ServiceEndpointFamilyKiro, "management.us-east-1.kiro.dev"},
		{"eu-central-1", ServiceEndpointFamilyKiro, "management.eu-central-1.kiro.dev"},
		{"us-east-1", ServiceEndpointFamilyLegacy, "q.us-east-1.amazonaws.com"},
		{"eu-central-1", ServiceEndpointFamilyLegacy, "q.eu-central-1.amazonaws.com"},
	}
	for _, tt := range tests {
		got := ManagementHost(tt.region, tt.family)
		if got != tt.want {
			t.Errorf("ManagementHost(%q, %q) = %q, want %q", tt.region, tt.family, got, tt.want)
		}
	}
}

func TestGenerateAssistantResponseURL(t *testing.T) {
	tests := []struct {
		region string
		family ServiceEndpointFamily
		want   string
	}{
		{"us-east-1", ServiceEndpointFamilyKiro, "https://runtime.us-east-1.kiro.dev/generateAssistantResponse"},
		{"us-east-1", ServiceEndpointFamilyLegacy, "https://q.us-east-1.amazonaws.com/generateAssistantResponse"},
	}
	for _, tt := range tests {
		got := GenerateAssistantResponseURL(tt.region, tt.family)
		if got != tt.want {
			t.Errorf("GenerateAssistantResponseURL(%q, %q) = %q, want %q", tt.region, tt.family, got, tt.want)
		}
	}
}

func TestGetUsageLimitsURL(t *testing.T) {
	tests := []struct {
		region string
		family ServiceEndpointFamily
		want   string
	}{
		{"us-east-1", ServiceEndpointFamilyKiro, "https://management.us-east-1.kiro.dev/getUsageLimits"},
		{"eu-central-1", ServiceEndpointFamilyLegacy, "https://q.eu-central-1.amazonaws.com/getUsageLimits"},
	}
	for _, tt := range tests {
		got := GetUsageLimitsURL(tt.region, tt.family)
		if got != tt.want {
			t.Errorf("GetUsageLimitsURL(%q, %q) = %q, want %q", tt.region, tt.family, got, tt.want)
		}
	}
}

func TestSetUserPreferenceURL(t *testing.T) {
	got := SetUserPreferenceURL("us-east-1", ServiceEndpointFamilyKiro)
	want := "https://management.us-east-1.kiro.dev/"
	if got != want {
		t.Errorf("SetUserPreferenceURL = %q, want %q", got, want)
	}
}

func TestNormalizeServiceEndpointFamily(t *testing.T) {
	tests := []struct {
		input string
		want  ServiceEndpointFamily
	}{
		{"kiro", ServiceEndpointFamilyKiro},
		{"legacy", ServiceEndpointFamilyLegacy},
		{"", ServiceEndpointFamilyKiro},
		{"invalid", ServiceEndpointFamilyKiro},
		{"KIRO", ServiceEndpointFamilyKiro}, // case-sensitive, falls to default
	}
	for _, tt := range tests {
		got := NormalizeServiceEndpointFamily(tt.input)
		if got != tt.want {
			t.Errorf("NormalizeServiceEndpointFamily(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
