// Package kiro — service endpoint family routing.
//
// Background:
//   - Legacy family routes to `q.<region>.amazonaws.com` (chat/MCP) and
//     `q.<region>.amazonaws.com/getUsageLimits` (usage).
//   - Kiro family routes to `runtime.<region>.kiro.dev` (chat/MCP) and
//     `management.<region>.kiro.dev` (usage). This is the AWS-announced
//     migration target; the legacy `q.*.amazonaws.com` domains are marked
//     for deprecation in official firewall docs (kiro.dev, updated 2026-05-06).
//   - Protocol (Bearer auth, headers, body schema, AWS Event Stream response)
//     is identical between families. Only the host changes.
package kiro

import (
	"fmt"
	"sync/atomic"
)

// ServiceEndpointFamily selects which upstream domain family to use.
type ServiceEndpointFamily string

const (
	// ServiceEndpointFamilyKiro uses runtime.<region>.kiro.dev / management.<region>.kiro.dev.
	ServiceEndpointFamilyKiro ServiceEndpointFamily = "kiro"
	// ServiceEndpointFamilyLegacy uses q.<region>.amazonaws.com (deprecated).
	ServiceEndpointFamilyLegacy ServiceEndpointFamily = "legacy"

	// DefaultServiceEndpointFamily is the family used when configuration is empty or invalid.
	DefaultServiceEndpointFamily = ServiceEndpointFamilyKiro

	// DefaultRegion is used when callers do not specify a region.
	DefaultRegion = "us-east-1"

	// DefaultProfileArn is the shared Kiro social/free-tier profile ARN returned
	// by social auth refresh. Do not use it as an IdC fallback: IdC tokens require
	// their matching profile_arn, otherwise management/runtime returns 403.
	DefaultProfileArn = "arn:aws:codewhisperer:us-east-1:699475941385:profile/EHGA3GRVQMUK"
)

// defaultFamily holds the process-wide default family, settable once at startup
// via SetDefaultServiceEndpointFamily(). Falls back to DefaultServiceEndpointFamily.
var defaultFamily atomic.Value // stores ServiceEndpointFamily

// SetDefaultServiceEndpointFamily installs the process-wide default family.
// Intended to be called once at startup from the application entrypoint,
// wired from the top-level config.
func SetDefaultServiceEndpointFamily(family ServiceEndpointFamily) {
	defaultFamily.Store(NormalizeServiceEndpointFamily(string(family)))
}

// GetDefaultServiceEndpointFamily returns the process-wide default family.
func GetDefaultServiceEndpointFamily() ServiceEndpointFamily {
	if v := defaultFamily.Load(); v != nil {
		if f, ok := v.(ServiceEndpointFamily); ok {
			return f
		}
	}
	return DefaultServiceEndpointFamily
}

// NormalizeServiceEndpointFamily returns a valid family, falling back to the default
// when input is empty or not a known value.
func NormalizeServiceEndpointFamily(s string) ServiceEndpointFamily {
	switch ServiceEndpointFamily(s) {
	case ServiceEndpointFamilyKiro:
		return ServiceEndpointFamilyKiro
	case ServiceEndpointFamilyLegacy:
		return ServiceEndpointFamilyLegacy
	default:
		return DefaultServiceEndpointFamily
	}
}

// resolveFamily applies the fallback chain: explicit arg -> process default -> hardcoded default.
func resolveFamily(family ServiceEndpointFamily) ServiceEndpointFamily {
	if family == ServiceEndpointFamilyKiro || family == ServiceEndpointFamilyLegacy {
		return family
	}
	return GetDefaultServiceEndpointFamily()
}

// RuntimeHost returns the host used for chat and MCP requests.
//
// Family == kiro   -> runtime.<region>.kiro.dev
// Family == legacy -> q.<region>.amazonaws.com
func RuntimeHost(region string, family ServiceEndpointFamily) string {
	if region == "" {
		region = DefaultRegion
	}
	if family == ServiceEndpointFamilyLegacy {
		return fmt.Sprintf("q.%s.amazonaws.com", region)
	}
	return fmt.Sprintf("runtime.%s.kiro.dev", region)
}

// ManagementHost returns the host used for usage/preference queries.
//
// Family == kiro   -> management.<region>.kiro.dev
// Family == legacy -> q.<region>.amazonaws.com
func ManagementHost(region string, family ServiceEndpointFamily) string {
	if region == "" {
		region = DefaultRegion
	}
	if family == ServiceEndpointFamilyLegacy {
		return fmt.Sprintf("q.%s.amazonaws.com", region)
	}
	return fmt.Sprintf("management.%s.kiro.dev", region)
}

// GenerateAssistantResponseURL returns the full URL for the chat completion endpoint.
func GenerateAssistantResponseURL(region string, family ServiceEndpointFamily) string {
	return fmt.Sprintf("https://%s/generateAssistantResponse", RuntimeHost(region, family))
}

// MCPURL returns the full URL for the MCP endpoint.
func MCPURL(region string, family ServiceEndpointFamily) string {
	return fmt.Sprintf("https://%s/mcp", RuntimeHost(region, family))
}

// GetUsageLimitsURL returns the full URL (without query string) for the usage-limits endpoint.
func GetUsageLimitsURL(region string, family ServiceEndpointFamily) string {
	return fmt.Sprintf("https://%s/getUsageLimits", ManagementHost(region, family))
}

// SetUserPreferenceURL returns the root URL used by the SetUserPreference call (POST to root).
func SetUserPreferenceURL(region string, family ServiceEndpointFamily) string {
	return fmt.Sprintf("https://%s/", ManagementHost(region, family))
}
