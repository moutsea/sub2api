package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResolveKiroUpstreamModelUsesCachedUnsupportedFallback(t *testing.T) {
	svc := &KiroGatewayService{}
	account := &Account{
		ID:          42,
		Platform:    PlatformKiro,
		Credentials: map[string]any{"auth_type": KiroAuthMethodSocial},
	}

	svc.setKiroModelCapability(account.ID, kiroDynamicProbeModelOpus47, kiroModelCapabilityUnsupported)

	got := svc.resolveKiroUpstreamModel(account, kiroDynamicProbeModelOpus47)
	require.Equal(t, kiroDynamicFallbackModelOpus46, got)
}

func TestResolveKiroUpstreamModelIgnoresApiKeyAccounts(t *testing.T) {
	svc := &KiroGatewayService{}
	account := &Account{
		ID:          7,
		Platform:    PlatformKiro,
		Credentials: map[string]any{"auth_type": KiroAuthMethodAPIKey},
	}

	svc.setKiroModelCapability(account.ID, kiroDynamicProbeModelOpus47, kiroModelCapabilityUnsupported)

	got := svc.resolveKiroUpstreamModel(account, kiroDynamicProbeModelOpus47)
	require.Equal(t, kiroDynamicProbeModelOpus47, got)
}

func TestGetKiroModelCapabilityExpires(t *testing.T) {
	svc := &KiroGatewayService{}
	key := kiroModelCapabilityKey{AccountID: 1, RequestedModel: kiroDynamicProbeModelOpus47}
	svc.modelCapabilityCache.Store(key, kiroModelCapabilityState{
		Status:    kiroModelCapabilityUnsupported,
		CheckedAt: time.Now().Add(-kiroModelCapabilityCacheTTL - time.Minute),
	})

	_, ok := svc.getKiroModelCapability(1, kiroDynamicProbeModelOpus47)
	require.False(t, ok)
}

func TestMaybeFallbackUnsupportedKiroModel(t *testing.T) {
	svc := &KiroGatewayService{}
	account := &Account{
		ID:          11,
		Platform:    PlatformKiro,
		Credentials: map[string]any{"auth_type": KiroAuthMethodSocial},
	}

	fallbackModel, ok := svc.maybeFallbackUnsupportedKiroModel(
		account,
		kiroDynamicProbeModelOpus47,
		kiroDynamicProbeModelOpus47,
		"Model claude-opus-4.7 is not supported for this account",
	)
	require.True(t, ok)
	require.Equal(t, kiroDynamicFallbackModelOpus46, fallbackModel)

	status, cached := svc.getKiroModelCapability(account.ID, kiroDynamicProbeModelOpus47)
	require.True(t, cached)
	require.Equal(t, kiroModelCapabilityUnsupported, status)
}

func TestIsKiroUnsupportedModelErrorRequiresModelMatch(t *testing.T) {
	require.True(t, isKiroUnsupportedModelError(
		"Unsupported model claude-opus-4.7",
		kiroDynamicProbeModelOpus47,
		"claude-opus-4.7",
	))
	require.False(t, isKiroUnsupportedModelError(
		"Model is not supported for this account",
		kiroDynamicProbeModelOpus47,
		"claude-opus-4.7",
	))
}

func TestIsKiroUnsupportedModelError_AllowsGenericInvalidModelMessage(t *testing.T) {
	require.True(t, isKiroUnsupportedModelError(
		"Invalid request: Invalid model. Please select a different model to continue.",
		kiroDynamicProbeModelOpus47,
		"claude-opus-4.7",
	))
}
