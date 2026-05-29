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

	svc.setKiroModelCapability(account.ID, kiroDynamicProbeModelOpus48, kiroModelCapabilityUnsupported)
	got = svc.resolveKiroUpstreamModel(account, kiroDynamicProbeModelOpus48)
	require.Equal(t, kiroDynamicFallbackModelOpus46, got)
}

func TestDowngradeKiroOpus47ModelRequiresKiroGroupSwitch(t *testing.T) {
	group := &Group{
		Platform:            PlatformKiro,
		KiroOpus47Downgrade: true,
	}

	got, ok := DowngradeKiroOpus47Model(group, kiroDynamicProbeModelOpus47)
	require.True(t, ok)
	require.Equal(t, kiroDynamicFallbackModelOpus46, got)

	got, ok = DowngradeKiroOpus47Model(group, "claude-sonnet-4-6")
	require.False(t, ok)
	require.Equal(t, "claude-sonnet-4-6", got)

	group.KiroOpus47Downgrade = false
	got, ok = DowngradeKiroOpus47Model(group, kiroDynamicProbeModelOpus47)
	require.False(t, ok)
	require.Equal(t, kiroDynamicProbeModelOpus47, got)

	group.Platform = PlatformAnthropic
	group.KiroOpus47Downgrade = true
	got, ok = DowngradeKiroOpus47Model(group, kiroDynamicProbeModelOpus47)
	require.False(t, ok)
	require.Equal(t, kiroDynamicProbeModelOpus47, got)
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

func TestKiroOpusInitialResponseTimeoutRequiresOAuthSlowFallbackModel(t *testing.T) {
	oauthAccount := &Account{
		ID:          8,
		Platform:    PlatformKiro,
		Credentials: map[string]any{"auth_type": KiroAuthMethodSocial},
	}
	apiKeyAccount := &Account{
		ID:          9,
		Platform:    PlatformKiro,
		Credentials: map[string]any{"auth_type": KiroAuthMethodAPIKey},
	}

	require.Equal(t, 40*time.Second, kiroOpusSlowFallbackInitialResponseTimeout(oauthAccount, "claude-opus-4.8", "claude-opus-4.8"))
	require.Equal(t, 40*time.Second, kiroOpusSlowFallbackInitialResponseTimeout(oauthAccount, kiroDynamicProbeModelOpus48, kiroDynamicProbeModelOpus48))
	require.Equal(t, 40*time.Second, kiroOpusSlowFallbackInitialResponseTimeout(oauthAccount, "claude-opus-4.7", "claude-opus-4.7"))
	require.Equal(t, 40*time.Second, kiroOpusSlowFallbackInitialResponseTimeout(oauthAccount, kiroDynamicProbeModelOpus47, kiroDynamicProbeModelOpus47))
	require.Zero(t, kiroOpusSlowFallbackInitialResponseTimeout(apiKeyAccount, kiroDynamicProbeModelOpus48, kiroDynamicProbeModelOpus48))
	require.Zero(t, kiroOpusSlowFallbackInitialResponseTimeout(apiKeyAccount, kiroDynamicProbeModelOpus47, kiroDynamicProbeModelOpus47))
	require.Zero(t, kiroOpusSlowFallbackInitialResponseTimeout(oauthAccount, kiroDynamicFallbackModelOpus46, kiroDynamicFallbackModelOpus46))

	startedAt := time.Now().Add(-35 * time.Second)
	remaining := kiroOpusSlowFallbackRemainingInitialResponseTimeout(oauthAccount, kiroDynamicProbeModelOpus47, kiroDynamicProbeModelOpus47, startedAt)
	require.True(t, remaining > 0 && remaining <= 5*time.Second)

	expired := kiroOpusSlowFallbackRemainingInitialResponseTimeout(oauthAccount, kiroDynamicProbeModelOpus47, kiroDynamicProbeModelOpus47, time.Now().Add(-45*time.Second))
	require.Equal(t, time.Nanosecond, expired)
}

func TestKiroOAuthModelsOnlyApplyToNonApiKeyAccounts(t *testing.T) {
	oauthAccount := &Account{
		ID:          8,
		Platform:    PlatformKiro,
		Credentials: map[string]any{"auth_type": KiroAuthMethodSocial},
	}
	apiKeyAccount := &Account{
		ID:          9,
		Platform:    PlatformKiro,
		Credentials: map[string]any{"auth_type": KiroAuthMethodAPIKey},
	}

	require.True(t, IsKiroModelSupportedByAccount(oauthAccount, "deepseek-3.2"))
	require.True(t, IsKiroModelSupportedByAccount(oauthAccount, "glm-5"))
	require.True(t, IsKiroModelSupportedByAccount(oauthAccount, KiroModelOpus48))
	require.False(t, IsKiroModelSupportedByAccount(apiKeyAccount, "deepseek-3.2"))
	require.True(t, IsKiroModelSupportedByAccount(apiKeyAccount, "claude-sonnet-4-5"))
	require.True(t, IsKiroModelSupportedByAccount(apiKeyAccount, KiroModelOpus48))
	require.True(t, isKiroOAuthOnlyModel("qwen3-coder-next"))
	require.False(t, isKiroOAuthOnlyModel("claude-sonnet-4-5"))
	require.False(t, isKiroOAuthOnlyModel(KiroModelOpus48))

	oauthAccount.Credentials["model_mapping"] = map[string]any{"glm-5": "glm-5"}
	require.True(t, IsKiroModelSupportedByAccount(oauthAccount, "glm-5"))
	require.False(t, IsKiroModelSupportedByAccount(oauthAccount, "deepseek-3.2"))

	apiKeyAccount.Credentials["model_mapping"] = map[string]any{"claude-opus-4-8": "claude-opus-4-8"}
	require.True(t, IsKiroModelSupportedByAccount(apiKeyAccount, KiroModelOpus48))
	require.False(t, IsKiroModelSupportedByAccount(apiKeyAccount, "claude-sonnet-4-5"))

	apiKeyAccount.Credentials["model_mapping"] = map[string]any{"deepseek-3.2": "claude-sonnet-4-5"}
	require.True(t, IsKiroModelSupportedByAccount(apiKeyAccount, "deepseek-3.2"))
	require.Equal(t, "claude-sonnet-4-5", apiKeyAccount.GetMappedModel("deepseek-3.2"))
}

func TestKiroAPIKeyDefaultModelsIncludesOpus48(t *testing.T) {
	models := KiroAPIKeyDefaultModels()
	require.NotEmpty(t, models)
	require.Equal(t, KiroModelOpus48, models[0].ID)

	var count int
	for _, model := range models {
		if model.ID == KiroModelOpus48 {
			count++
		}
	}
	require.Equal(t, 1, count)
	require.True(t, IsKiroModelSupported(KiroModelOpus48))
}

func TestRemapModelForFreeTierAllowsKiroFreeModels(t *testing.T) {
	svc := &KiroGatewayService{}
	account := &Account{
		ID:          10,
		Platform:    PlatformKiro,
		Credentials: map[string]any{"auth_type": KiroAuthMethodSocial},
	}

	got, remapped := svc.remapModelForFreeTier(account, "deepseek-3.2", true)
	require.False(t, remapped)
	require.Equal(t, "deepseek-3.2", got)

	got, remapped = svc.remapModelForFreeTier(account, "claude-opus-4-6", true)
	require.True(t, remapped)
	require.Equal(t, "claude-sonnet-4-5", got)
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

	fallbackModel, ok = svc.maybeFallbackUnsupportedKiroModel(
		account,
		kiroDynamicProbeModelOpus48,
		kiroDynamicProbeModelOpus48,
		"Model claude-opus-4.8 is not supported for this account",
	)
	require.True(t, ok)
	require.Equal(t, kiroDynamicFallbackModelOpus46, fallbackModel)
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
