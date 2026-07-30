package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type recordingKiroAPIKeyUpstream struct {
	requestBody []byte
	requestURL  string
}

func (u *recordingKiroAPIKeyUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	u.requestBody = body
	u.requestURL = req.URL.String()
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"content":[{"type":"text","text":"ok"}]}`)),
		Header:     make(http.Header),
	}, nil
}

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

	svc.setKiroModelCapability(account.ID, "claude-opus-5.0-thinking", kiroModelCapabilityUnsupported)
	got = svc.resolveKiroUpstreamModel(account, kiroDynamicProbeModelOpus5)
	require.Equal(t, kiroDynamicFallbackModelOpus46, got)

	svc.setKiroModelCapability(account.ID, kiroDynamicProbeModelOpus48, kiroModelCapabilityUnsupported)
	got = svc.resolveKiroUpstreamModel(account, kiroDynamicProbeModelOpus48)
	require.Equal(t, kiroDynamicFallbackModelOpus46, got)
}

func TestIsKiroDynamicProbeModelSupportsOpusAliases(t *testing.T) {
	for _, model := range []string{
		"claude-opus-5",
		"claude-opus-5.0",
		"claude-opus-5-0",
		"claude-opus-5-thinking",
		"claude-opus-5.0-thinking",
		"claude-opus-4-8-thinking",
		"claude-opus-4.7-thinking",
	} {
		require.Truef(t, isKiroDynamicProbeModel(model), "expected %s to use capability probing", model)
	}
	require.False(t, isKiroDynamicProbeModel("claude-opus-4-6"))
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

func TestKiroAPIKeyModelsAreDeterminedByCustomUpstream(t *testing.T) {
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
	require.False(t, IsKiroModelSupportedByAccount(oauthAccount, "gpt-5.6"))
	require.True(t, IsKiroModelSupportedByAccount(oauthAccount, "gpt-5.6-sol"))
	require.True(t, IsKiroModelSupportedByAccount(oauthAccount, "gpt-5.6-terra"))
	require.True(t, IsKiroModelSupportedByAccount(oauthAccount, "gpt-5.6-luna"))
	require.True(t, IsKiroModelSupportedByAccount(oauthAccount, "claude-sonnet-5"))
	require.True(t, IsKiroModelSupportedByAccount(oauthAccount, KiroModelOpus48))
	require.True(t, IsKiroModelSupportedByAccount(apiKeyAccount, "deepseek-3.2"))
	require.True(t, IsKiroModelSupportedByAccount(apiKeyAccount, "gpt-5.6"))
	require.True(t, IsKiroModelSupportedByAccount(apiKeyAccount, "gpt-5.6-sol"))
	require.True(t, IsKiroModelSupportedByAccount(apiKeyAccount, "claude-sonnet-4-5"))
	require.True(t, IsKiroModelSupportedByAccount(apiKeyAccount, KiroModelOpus48))
	require.True(t, IsKiroModelSupportedByAccount(apiKeyAccount, "custom-upstream-model"))

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

func TestKiroAPIKeyDefaultModelsIncludesNewestOpusModels(t *testing.T) {
	models := KiroAPIKeyDefaultModels()
	require.NotEmpty(t, models)
	// Newest Opus first, then the previous generation.
	require.Equal(t, KiroModelOpus5, models[0].ID)
	require.Equal(t, KiroModelOpus48, models[1].ID)

	counts := make(map[string]int)
	found := make(map[string]bool)
	for _, model := range models {
		found[model.ID] = true
		counts[model.ID]++
	}
	require.Equal(t, 1, counts[KiroModelOpus48])
	require.Equal(t, 1, counts[KiroModelOpus5])
	require.True(t, found["gpt-5.6-sol"])
	require.True(t, found["gpt-5.6-terra"])
	require.True(t, found["gpt-5.6-luna"])
	require.True(t, IsKiroModelSupported(KiroModelOpus48))
	require.True(t, IsKiroModelSupported(KiroModelOpus5))
}

func TestKiroAPIKeyConnectionPassesGPTModelToCustomUpstream(t *testing.T) {
	upstream := &recordingKiroAPIKeyUpstream{}
	svc := &KiroGatewayService{httpUpstream: upstream}
	account := &Account{
		ID:       12,
		Platform: PlatformKiro,
		Credentials: map[string]any{
			"auth_type": KiroAuthMethodAPIKey,
			"base_url":  "https://custom.example",
		},
	}

	result, err := svc.testClaudeAPIConnection(context.Background(), account, "secret", "gpt-5.6-sol")
	require.NoError(t, err)
	require.Equal(t, "gpt-5.6-sol", result.MappedModel)
	require.Equal(t, "https://custom.example/v1/messages", upstream.requestURL)

	var requestBody map[string]any
	require.NoError(t, json.Unmarshal(upstream.requestBody, &requestBody))
	require.Equal(t, "gpt-5.6-sol", requestBody["model"])
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
		"claude-opus-5.0-thinking",
		kiroDynamicProbeModelOpus5,
		"Model claude-opus-5 is not supported for this account",
	)
	require.True(t, ok)
	require.Equal(t, kiroDynamicFallbackModelOpus46, fallbackModel)

	status, cached := svc.getKiroModelCapability(account.ID, kiroDynamicProbeModelOpus5)
	require.True(t, cached)
	require.Equal(t, kiroModelCapabilityUnsupported, status)

	fallbackModel, ok = svc.maybeFallbackUnsupportedKiroModel(
		account,
		kiroDynamicProbeModelOpus47,
		kiroDynamicProbeModelOpus47,
		"Model claude-opus-4.7 is not supported for this account",
	)
	require.True(t, ok)
	require.Equal(t, kiroDynamicFallbackModelOpus46, fallbackModel)

	status, cached = svc.getKiroModelCapability(account.ID, kiroDynamicProbeModelOpus47)
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
