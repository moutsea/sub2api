package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGemini4ArgonPricingSchedule(t *testing.T) {
	cutoff := time.Date(2026, time.December, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		at     time.Time
		input  float64
		output float64
		cached float64
	}{
		{"introductory", cutoff.Add(-time.Nanosecond), 2e-6, 10e-6, 0.1e-6},
		{"standard", cutoff, 4e-6, 20e-6, 0.2e-6},
		{"after_cutoff", cutoff.Add(24 * time.Hour), 4e-6, 20e-6, 0.2e-6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pricing := geminiStaticPricingAt("gemini-4-argon", tc.at)
			require.NotNil(t, pricing)
			require.Equal(t, tc.input, pricing.InputCostPerToken)
			require.Equal(t, tc.output, pricing.OutputCostPerToken)
			require.Equal(t, tc.cached, pricing.CacheReadInputTokenCost)
			require.InDelta(t, tc.input*1.8, pricing.InputCostPerTokenPriority, 1e-15)
			require.InDelta(t, tc.output*1.8, pricing.OutputCostPerTokenPriority, 1e-15)
			require.InDelta(t, tc.cached*1.8, pricing.CacheReadInputTokenCostPriority, 1e-15)
			require.True(t, pricing.SupportsPromptCaching)
			// Lookup results must not mutate the shared standard price card.
			pricing.InputCostPerToken = 0
			require.Equal(t, tc.input, geminiStaticPricingAt("gemini-4-argon", tc.at).InputCostPerToken)
		})
	}
	require.Nil(t, geminiStaticPricingAt("gemini-4-unknown", cutoff))
}

func TestGemini4ArgonBillingFallback(t *testing.T) {
	want := modelPricingFromLiteLLM(geminiStaticPricingAt("gemini-4-argon", time.Now()))
	for _, pricingService := range []*PricingService{nil, {pricingData: map[string]*LiteLLMModelPricing{}}} {
		billing := NewBillingService(nil, pricingService)
		require.Contains(t, billing.ListSupportedModels(), "gemini-4-argon")
		for _, model := range []string{
			"gemini-4-argon",
			"models/gemini-4-argon",
			"gemini/gemini-4-argon",
			"vertex_ai/gemini-4-argon",
			"projects/example/locations/global/publishers/google/models/gemini-4-argon",
		} {
			pricing, err := billing.GetModelPricing(model)
			require.NoError(t, err, model)
			require.Equal(t, want, pricing, model)
			display, err := billing.GetModelPricingForDisplay(model)
			require.NoError(t, err, model)
			require.Equal(t, pricing, display, model)
			require.True(t, billing.IsModelSupported(model), model)
		}
		cost, err := billing.CalculateCost("gemini-4-argon", UsageTokens{
			InputTokens: 100, OutputTokens: 30, CacheReadTokens: 40,
		}, 1)
		require.NoError(t, err)
		require.InDelta(t, 100*want.InputPricePerToken+30*want.OutputPricePerToken+40*want.CacheReadPricePerToken, cost.TotalCost, 1e-12)
		_, err = billing.GetModelPricingForDisplay("gemini-4-unknown")
		require.Error(t, err)
		priority, err := billing.CalculateCostWithServiceTier("gemini-4-argon", UsageTokens{
			InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000,
		}, 1, "priority")
		require.NoError(t, err)
		require.InDelta(t, 1_000_000*want.InputPricePerTokenPriority, priority.InputCost, 1e-10)
		require.InDelta(t, 1_000_000*want.OutputPricePerTokenPriority, priority.OutputCost, 1e-10)
		require.InDelta(t, 1_000_000*want.CacheReadPricePerTokenPriority, priority.CacheReadCost, 1e-10)
	}
}

func TestGemini4ArgonDynamicPricingTakesPrecedence(t *testing.T) {
	for _, model := range []string{"gemini-4-argon", "models/gemini-4-argon", "vertex_ai/gemini-4-argon"} {
		t.Run(model, func(t *testing.T) {
			remote := &LiteLLMModelPricing{InputCostPerToken: 3e-6, OutputCostPerToken: 15e-6, CacheReadInputTokenCost: 0.15e-6}
			pricingService := &PricingService{pricingData: map[string]*LiteLLMModelPricing{model: remote}}
			require.Equal(t, remote, pricingService.GetModelPricing(model))
			require.Equal(t, remote, pricingService.GetModelPricingExact(model))
			billing := NewBillingService(nil, pricingService)
			pricing, err := billing.GetModelPricing(model)
			require.NoError(t, err)
			require.Equal(t, modelPricingFromLiteLLM(remote), pricing)
			display, err := billing.GetModelPricingForDisplay(model)
			require.NoError(t, err)
			require.Equal(t, pricing, display)
		})
	}
}
