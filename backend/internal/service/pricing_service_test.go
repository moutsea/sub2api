package service

import (
	"math"
	"testing"
)

func TestPricingServiceGetModelPricing_UsesStaticGPT5xPricingOverrides(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.2": {
				InputCostPerToken: 0.52,
			},
			"gpt-5.1-codex": {
				InputCostPerToken: 0.51,
			},
		},
	}

	tests := []struct {
		model  string
		input  float64
		output float64
	}{
		{model: "gpt-6-astra", input: 10e-06, output: 50e-06},
		{model: "gpt-6-astra-2026-09-04", input: 10e-06, output: 50e-06},
		{model: "gpt-6-sol", input: 2e-06, output: 10e-06},
		{model: "gpt-6-sol-20260923", input: 2e-06, output: 10e-06},
		{model: "gpt-6-luna", input: 0.1e-06, output: 0.5e-06},
		{model: "gpt-6-luna-20260923", input: 0.1e-06, output: 0.5e-06},
		{model: "gpt-5.3", input: 1.75e-06, output: 14e-06},
		{model: "gpt-5.6", input: 5e-06, output: 30e-06},
		{model: "gpt-5.6-sol", input: 5e-06, output: 30e-06},
		{model: "gpt-5.6-terra", input: 2.5e-06, output: 15e-06},
		{model: "gpt-5.6-luna", input: 1e-06, output: 6e-06},
		{model: "gpt-5.4", input: 2.5e-06, output: 15e-06},
		{model: "gpt-5.5", input: 5e-06, output: 30e-06},
		{model: "gpt-5.6-sol-20260709", input: 5e-06, output: 30e-06},
		{model: "gpt-5.4-20260424", input: 2.5e-06, output: 15e-06},
		{model: "gpt-5.5-20260424", input: 5e-06, output: 30e-06},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			pricing := svc.GetModelPricing(tt.model)
			if pricing == nil {
				t.Fatalf("expected pricing for %q", tt.model)
			}
			if pricing.InputCostPerToken != tt.input {
				t.Fatalf("input pricing = %v, want %v", pricing.InputCostPerToken, tt.input)
			}
			if pricing.OutputCostPerToken != tt.output {
				t.Fatalf("output pricing = %v, want %v", pricing.OutputCostPerToken, tt.output)
			}
		})
	}

}

func TestPricingServiceGetModelPricing_GPT6AstraUsesGPT56Policy(t *testing.T) {
	svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{}}

	pricing := svc.GetModelPricing("gpt-6-astra")
	if pricing == nil {
		t.Fatal("expected gpt-6-astra pricing")
	}
	if pricing.CacheCreationInputTokenCost != 12.5e-06 {
		t.Fatalf("cache creation pricing = %v, want %v", pricing.CacheCreationInputTokenCost, 12.5e-06)
	}
	if pricing.CacheReadInputTokenCost != 1e-06 {
		t.Fatalf("cache read pricing = %v, want %v", pricing.CacheReadInputTokenCost, 1e-06)
	}
	if pricing.LongContextInputTokenThreshold != 272000 {
		t.Fatalf("long context threshold = %d, want %d", pricing.LongContextInputTokenThreshold, 272000)
	}
	if pricing.LongContextInputCostMultiplier != 2 || pricing.LongContextOutputCostMultiplier != 1.5 {
		t.Fatalf("long context multipliers = %v/%v, want 2/1.5", pricing.LongContextInputCostMultiplier, pricing.LongContextOutputCostMultiplier)
	}
	if !pricing.SupportsPromptCaching {
		t.Fatal("expected gpt-6-astra to support prompt caching")
	}
}

func TestPricingServiceGetModelPricing_GPT6SolAndLunaUseOfficialStandardRates(t *testing.T) {
	svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{}}

	tests := []struct {
		model          string
		input          float64
		cachedInput    float64
		cacheWrites    float64
		output         float64
		inputLongMult  float64
		outputLongMult float64
	}{
		{
			model:          "gpt-6-sol",
			input:          2e-06,
			cachedInput:    0.2e-06,
			cacheWrites:    2.5e-06,
			output:         10e-06,
			inputLongMult:  2,
			outputLongMult: 1.5,
		},
		{
			model:          "gpt-6-luna",
			input:          0.1e-06,
			cachedInput:    0.01e-06,
			cacheWrites:    0.125e-06,
			output:         0.5e-06,
			inputLongMult:  2,
			outputLongMult: 1.5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			pricing := svc.GetModelPricing(tt.model)
			if pricing == nil {
				t.Fatalf("expected pricing for %q", tt.model)
			}
			if pricing.InputCostPerToken != tt.input || pricing.CacheReadInputTokenCost != tt.cachedInput ||
				pricing.CacheCreationInputTokenCost != tt.cacheWrites || pricing.OutputCostPerToken != tt.output {
				t.Fatalf("pricing = %#v", pricing)
			}
			if pricing.LongContextInputTokenThreshold != 272000 ||
				pricing.LongContextInputCostMultiplier != tt.inputLongMult ||
				pricing.LongContextOutputCostMultiplier != tt.outputLongMult {
				t.Fatalf("long-context pricing = %#v", pricing)
			}
		})
	}
}

func TestPricingServiceGetModelPricingExactSkipsProviderFallback(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.5": {
				InputCostPerToken:  5e-06,
				OutputCostPerToken: 30e-06,
			},
		},
	}

	if pricing := svc.GetModelPricingExact("gpt-unknown"); pricing != nil {
		t.Fatalf("expected unknown model to have no exact pricing, got %+v", pricing)
	}
	if pricing := svc.GetModelPricingExact("gpt-5.6-unknown"); pricing != nil {
		t.Fatalf("expected unknown model variant to have no exact pricing, got %+v", pricing)
	}
	pricing := svc.GetModelPricingExact("gpt-6-astra")
	if pricing == nil {
		t.Fatal("expected static pricing for gpt-6-astra")
	}
	if pricing.InputCostPerToken != 10e-06 || pricing.OutputCostPerToken != 50e-06 {
		t.Fatalf("pricing = %v/%v, want %v/%v", pricing.InputCostPerToken, pricing.OutputCostPerToken, 10e-06, 50e-06)
	}
	if pricing := svc.GetModelPricingExact("gpt-5.4-mini"); pricing == nil {
		t.Fatal("expected known gpt-5.4-mini variant to use gpt-5.4 pricing")
	}
}

func TestBillingServiceGetModelPricingForDisplaySkipsProviderFallback(t *testing.T) {
	svc := NewBillingService(nil, &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.5": {
				InputCostPerToken:  5e-06,
				OutputCostPerToken: 30e-06,
			},
		},
	})

	if _, err := svc.GetModelPricingForDisplay("gpt-unknown"); err == nil {
		t.Fatal("expected unknown model display pricing to fail")
	}
	pricing, err := svc.GetModelPricingForDisplay("gpt-6-astra")
	if err != nil {
		t.Fatalf("expected static gpt-6-astra display pricing, got %v", err)
	}
	if pricing.InputPricePerToken != 10e-06 || pricing.OutputPricePerToken != 50e-06 {
		t.Fatalf("pricing = %v/%v, want %v/%v", pricing.InputPricePerToken, pricing.OutputPricePerToken, 10e-06, 50e-06)
	}
}

func TestPricingServiceGetModelPricing_Gemini38StaticFallback(t *testing.T) {
	svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{}}

	for _, model := range []string{
		"gemini-3.8-flash",
		"gemini/gemini-3.8-flash",
		"vertex_ai/gemini-3.8-flash",
		"models/gemini-3.8-flash",
	} {
		t.Run(model, func(t *testing.T) {
			pricing := svc.GetModelPricing(model)
			if pricing == nil {
				t.Fatalf("expected pricing for %q", model)
			}
			if pricing.InputCostPerToken != 7.5e-07 || pricing.OutputCostPerToken != 3.75e-06 {
				t.Fatalf("pricing = %v/%v, want %v/%v", pricing.InputCostPerToken, pricing.OutputCostPerToken, 7.5e-07, 3.75e-06)
			}
			if pricing.CacheReadInputTokenCost != 7.5e-08 {
				t.Fatalf("cache read pricing = %v, want %v", pricing.CacheReadInputTokenCost, 7.5e-08)
			}
		})
	}
}

func TestBillingServiceGetModelPricing_Gemini38FallbackWhenDynamicMissing(t *testing.T) {
	svc := NewBillingService(nil, &PricingService{pricingData: map[string]*LiteLLMModelPricing{}})

	for _, model := range []string{
		"gemini-3.8-flash",
		"gemini/gemini-3.8-flash",
		"vertex_ai/gemini-3.8-flash",
		"models/gemini-3.8-flash",
	} {
		t.Run(model, func(t *testing.T) {
			pricing, err := svc.GetModelPricing(model)
			if err != nil {
				t.Fatalf("expected pricing for %q, got error %v", model, err)
			}
			if pricing.InputPricePerToken != 7.5e-07 || pricing.OutputPricePerToken != 3.75e-06 {
				t.Fatalf("pricing = %v/%v, want %v/%v", pricing.InputPricePerToken, pricing.OutputPricePerToken, 7.5e-07, 3.75e-06)
			}
		})
	}

	cost, err := svc.CalculateCost("gemini-3.8-flash", UsageTokens{
		InputTokens:     100,
		OutputTokens:    10,
		CacheReadTokens: 30,
	}, 1)
	if err != nil {
		t.Fatalf("expected Gemini 3.8 cost, got error %v", err)
	}
	wantTotal := float64(100)*7.5e-07 + float64(10)*3.75e-06 + float64(30)*7.5e-08
	if cost.TotalCost != wantTotal {
		t.Fatalf("total cost = %v, want %v", cost.TotalCost, wantTotal)
	}
}

func TestPricingServiceMatchOpenAIModel_FallsBackToNearestPricedModel(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.2": {
				InputCostPerToken: 0.52,
			},
			"gpt-5.1-codex": {
				InputCostPerToken: 0.51,
			},
		},
	}

	tests := []struct {
		model string
		want  float64
	}{
		{model: "gpt-5.6", want: 5e-06},
		{model: "gpt-5.6-20260424", want: 5e-06},
		{model: "gpt-5.5-codex", want: 0.51},
		{model: "gpt-5.6-codex", want: 0.51},
		{model: "gpt-5.4-codex-high", want: 0.51},
		{model: "codex-auto-review", want: 0.51},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			pricing := svc.GetModelPricing(tt.model)
			if pricing == nil {
				t.Fatalf("expected pricing for %q", tt.model)
			}
			if pricing.InputCostPerToken != tt.want {
				t.Fatalf("pricing = %v, want %v", pricing.InputCostPerToken, tt.want)
			}
		})
	}

}

func TestBillingServiceGetModelPricing_UsesGPTFallbackInsteadOfClaudeForUnknownOpenAI(t *testing.T) {
	svc := NewBillingService(nil, &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{},
	})

	pricing, err := svc.GetModelPricing("gpt-unknown")
	if err != nil {
		t.Fatalf("expected GPT fallback pricing, got error %v", err)
	}
	if pricing.InputPricePerToken != 5e-06 {
		t.Fatalf("input pricing = %v, want %v", pricing.InputPricePerToken, 5e-06)
	}
	if pricing.OutputPricePerToken != 30e-06 {
		t.Fatalf("output pricing = %v, want %v", pricing.OutputPricePerToken, 30e-06)
	}
}

func TestBillingServiceGetModelPricing_GrokFallbacks(t *testing.T) {
	svc := NewBillingService(nil, &PricingService{pricingData: map[string]*LiteLLMModelPricing{}})

	tests := []struct {
		model  string
		input  float64
		output float64
	}{
		{model: "grok", input: 2e-6, output: 6e-6},
		{model: "grok-latest", input: 2e-6, output: 6e-6},
		{model: "grok-4.20-0309-reasoning", input: 1.25e-6, output: 2.5e-6},
		{model: "grok-future-1", input: 2e-6, output: 6e-6},
		{model: "grok-4.5", input: 2e-6, output: 6e-6},
		{model: "grok-4.6-20260901", input: 2e-6, output: 6e-6},
		{model: "grok-build", input: 1e-6, output: 2e-6},
		{model: "grok-composer", input: 1e-6, output: 2e-6},
		{model: "grok-composer-2.5-fast", input: 1e-6, output: 2e-6},
		{model: "composer-2.5", input: 1e-6, output: 2e-6},
		{model: "xai/grok-composer-2.5-fast", input: 1e-6, output: 2e-6},
		{model: "x-ai/composer-2.5", input: 1e-6, output: 2e-6},
		{model: "grok/grok-build", input: 1e-6, output: 2e-6},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			pricing, err := svc.GetModelPricing(tt.model)
			if err != nil {
				t.Fatalf("GetModelPricing(%q): %v", tt.model, err)
			}
			if pricing.InputPricePerToken != tt.input || pricing.OutputPricePerToken != tt.output {
				t.Fatalf("pricing = %#v, want input=%v output=%v", pricing, tt.input, tt.output)
			}
		})
	}

	longContext, err := svc.CalculateCost("grok-4.5", UsageTokens{InputTokens: 200000, OutputTokens: 1}, 1)
	if err != nil {
		t.Fatalf("CalculateCost(grok-4.5): %v", err)
	}
	if longContext.InputCost < 0.799 || longContext.InputCost > 0.801 {
		t.Fatalf("long-context input cost = %v, want 0.8", longContext.InputCost)
	}
}

func TestBillingServiceIsModelSupported_GrokProviderPrefixes(t *testing.T) {
	svc := NewBillingService(nil, &PricingService{pricingData: map[string]*LiteLLMModelPricing{}})
	for _, model := range []string{"grok-4.6", "xai/grok-4.6", "x-ai/grok-4.6", "grok/grok-4.6"} {
		if !svc.IsModelSupported(model) {
			t.Fatalf("IsModelSupported(%q) = false, want true", model)
		}
	}
	if svc.IsModelSupported("xai/llama-4") {
		t.Fatal("unknown provider model should remain unsupported")
	}
}

func TestPricingServiceGetModelPricing_Sonnet5DoesNotFallBackToSonnet4(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"claude-sonnet-4": {
				InputCostPerToken: 4,
			},
		},
	}

	if pricing := svc.GetModelPricing("claude-sonnet-5"); pricing != nil {
		t.Fatalf("pricing = %v, want nil when sonnet 5 dynamic pricing is absent", pricing)
	}
}

func TestPricingServiceGetModelPricing_Opus5And48UseExplicitStaticPricing(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"claude-opus-4": {
				InputCostPerToken:  15e-6,
				OutputCostPerToken: 75e-6,
			},
			"claude-opus-4.6": {
				InputCostPerToken:  99e-6,
				OutputCostPerToken: 199e-6,
			},
		},
	}

	for _, model := range []string{
		"claude-opus-5",
		"claude-opus-5.0",
		"claude-opus-5-0-thinking",
		"claude-opus-5-20260701",
		"claude-opus-4-8",
		"claude-opus-4.8-thinking",
	} {
		t.Run(model, func(t *testing.T) {
			pricing := svc.GetModelPricing(model)
			if pricing == nil {
				t.Fatalf("expected pricing for %q", model)
			}
			if pricing.InputCostPerToken != 5e-6 {
				t.Fatalf("input pricing = %v, want %v", pricing.InputCostPerToken, 5e-6)
			}
			if pricing.OutputCostPerToken != 25e-6 {
				t.Fatalf("output pricing = %v, want %v", pricing.OutputCostPerToken, 25e-6)
			}
			if pricing.CacheCreationInputTokenCost != 6.25e-6 {
				t.Fatalf("cache creation pricing = %v, want %v", pricing.CacheCreationInputTokenCost, 6.25e-6)
			}
			if pricing.CacheReadInputTokenCost != 0.5e-6 {
				t.Fatalf("cache read pricing = %v, want %v", pricing.CacheReadInputTokenCost, 0.5e-6)
			}
		})
	}
}

func TestPricingServiceGetModelPricing_Opus55HasSeparatePrice(t *testing.T) {
	svc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"claude-opus-5": {
			InputCostPerToken:  5e-6,
			OutputCostPerToken: 25e-6,
		},
	}}

	pricing := svc.GetModelPricing(KiroModelOpus55)
	if pricing == nil || pricing.InputCostPerToken != 4e-6 || pricing.OutputCostPerToken != 20e-6 ||
		pricing.CacheCreationInputTokenCost != 5e-6 || pricing.CacheReadInputTokenCost != 0.2e-6 {
		t.Fatalf("Opus 5.5 fallback pricing = %+v", pricing)
	}

	svc.pricingData[KiroModelOpus55] = &LiteLLMModelPricing{InputCostPerToken: 3e-6, OutputCostPerToken: 15e-6}
	pricing = svc.GetModelPricing("claude-opus-5-5-thinking")
	if pricing == nil || pricing.InputCostPerToken != 3e-6 || pricing.OutputCostPerToken != 15e-6 {
		t.Fatalf("Opus 5.5 dynamic pricing = %+v", pricing)
	}

	delete(svc.pricingData, "claude-opus-5")
	pricing = svc.GetModelPricing("claude-opus-5-thinking")
	if pricing == nil || pricing.InputCostPerToken != 5e-6 || pricing.OutputCostPerToken != 25e-6 {
		t.Fatalf("Opus 5 must not use Opus 5.5 pricing: %+v", pricing)
	}
}

func TestBillingServiceCalculateCost_Opus55CacheDuration(t *testing.T) {
	svc := NewBillingService(nil, &PricingService{pricingData: map[string]*LiteLLMModelPricing{}})
	pricing, err := svc.GetModelPricing(KiroModelOpus55)
	if err != nil {
		t.Fatal(err)
	}
	if !pricing.SupportsCacheBreakdown || pricing.CacheCreation5mPrice != 5 || pricing.CacheCreation1hPrice != 8 {
		t.Fatalf("Opus 5.5 cache pricing = %+v", pricing)
	}

	for _, tc := range []struct {
		name   string
		tokens UsageTokens
		want   float64
	}{
		{name: "split", tokens: UsageTokens{CacheCreationTokens: 300000, CacheCreation5mTokens: 100000, CacheCreation1hTokens: 200000}, want: 2.1},
		{name: "aggregate fallback", tokens: UsageTokens{CacheCreationTokens: 300000}, want: 1.5},
		{name: "unclassified remainder", tokens: UsageTokens{CacheCreationTokens: 300000, CacheCreation5mTokens: 100000, CacheCreation1hTokens: 100000}, want: 1.8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cost, err := svc.CalculateCost(KiroModelOpus55, tc.tokens, 1)
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(cost.CacheCreationCost-tc.want) > 1e-12 {
				t.Fatalf("cache creation cost = %v, want %v", cost.CacheCreationCost, tc.want)
			}
		})
	}

	svc.pricingService.pricingData[KiroModelOpus55] = &LiteLLMModelPricing{
		InputCostPerToken:           4e-6,
		OutputCostPerToken:          20e-6,
		CacheCreationInputTokenCost: 5e-6,
	}
	pricing, err = svc.GetModelPricing(KiroModelOpus55)
	if err != nil || !pricing.SupportsCacheBreakdown || pricing.CacheCreation1hPrice != 8 {
		t.Fatalf("dynamic Opus 5.5 cache pricing = %+v, err = %v", pricing, err)
	}
}

func TestClaudeOpusModelFamilyMatchingRequiresTokenBoundary(t *testing.T) {
	for _, model := range []string{
		"claude-opus-5",
		"claude-opus-5.0",
		"claude-opus-5-0-thinking",
		"claude-opus-5-20260701",
		"anthropic/claude-opus-5-thinking",
	} {
		if !isClaudeOpus5Model(model) {
			t.Fatalf("expected %q to match Opus 5", model)
		}
	}
	for _, model := range []string{"claude-opus-50", "claude-opus-51", "claude-opus-5beta"} {
		if isClaudeOpus5Model(model) {
			t.Fatalf("expected %q not to match Opus 5", model)
		}
	}

	for _, model := range []string{"claude-opus-4-80", "claude-opus-4.80", "claude-opus-4.8beta"} {
		if isClaudeOpus48Model(model) {
			t.Fatalf("expected %q not to match Opus 4.8", model)
		}
	}
}

func TestPricingServiceGetModelPricing_OpusFamilyDoesNotMatchFutureMajorVersion(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"claude-opus-50": {
				InputCostPerToken:  50e-6,
				OutputCostPerToken: 100e-6,
			},
		},
	}

	pricing := svc.GetModelPricing("claude-opus-5-thinking")
	if pricing == nil {
		t.Fatal("expected static Opus 5 pricing")
	}
	if pricing.InputCostPerToken != 5e-6 || pricing.OutputCostPerToken != 25e-6 {
		t.Fatalf("pricing = %v/%v, want %v/%v", pricing.InputCostPerToken, pricing.OutputCostPerToken, 5e-6, 25e-6)
	}
	if pricing := svc.GetModelPricing("claude-opus-50-thinking"); pricing != nil {
		t.Fatalf("pricing = %+v, want nil for unknown future major version", pricing)
	}
}

func TestPricingServiceGetModelPricing_Opus5PrefersDynamicPricing(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"claude-opus-5": {
				InputCostPerToken:           1e-6,
				OutputCostPerToken:          2e-6,
				CacheCreationInputTokenCost: 3e-6,
				CacheReadInputTokenCost:     4e-6,
			},
		},
	}

	pricing := svc.GetModelPricing("claude-opus-5-thinking")
	if pricing == nil {
		t.Fatal("expected dynamic Opus 5 pricing")
	}
	if pricing.InputCostPerToken != 1e-6 || pricing.OutputCostPerToken != 2e-6 {
		t.Fatalf("pricing = %v/%v, want %v/%v", pricing.InputCostPerToken, pricing.OutputCostPerToken, 1e-6, 2e-6)
	}
}

func TestBillingServiceGetModelPricing_Opus5DoesNotUseClaude3OpusFallback(t *testing.T) {
	svc := NewBillingService(nil, &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"claude-3-opus": {
				InputCostPerToken:  15e-6,
				OutputCostPerToken: 75e-6,
			},
		},
	})

	pricing, err := svc.GetModelPricing("claude-opus-5-thinking")
	if err != nil {
		t.Fatalf("expected Opus 5 pricing, got error %v", err)
	}
	if pricing.InputPricePerToken != 5e-6 || pricing.OutputPricePerToken != 25e-6 {
		t.Fatalf("pricing = %v/%v, want %v/%v", pricing.InputPricePerToken, pricing.OutputPricePerToken, 5e-6, 25e-6)
	}
}

func TestPricingServiceGetModelPricing_Sonnet5FamilyMatchesDynamicPricing(t *testing.T) {
	svc := &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"claude-sonnet-5": {
				InputCostPerToken:  2e-6,
				OutputCostPerToken: 10e-6,
			},
			"claude-sonnet-4": {
				InputCostPerToken: 4,
			},
		},
	}

	pricing := svc.GetModelPricing("claude-sonnet-5-thinking")
	if pricing == nil {
		t.Fatal("expected sonnet 5 pricing")
	}
	if pricing.InputCostPerToken != 2e-6 {
		t.Fatalf("input pricing = %v, want %v", pricing.InputCostPerToken, 2e-6)
	}
	if pricing.OutputCostPerToken != 10e-6 {
		t.Fatalf("output pricing = %v, want %v", pricing.OutputCostPerToken, 10e-6)
	}
}

func TestBillingServiceGetModelPricing_Sonnet5UsesOwnPricingEntryMatchingSonnet46(t *testing.T) {
	svc := NewBillingService(nil, &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"claude-sonnet-5": {
				InputCostPerToken:           2e-6,
				OutputCostPerToken:          10e-6,
				CacheCreationInputTokenCost: 2.5e-6,
				CacheReadInputTokenCost:     0.2e-6,
			},
		},
	})

	sonnet5Fallback := svc.fallbackPrices["claude-sonnet-5"]
	if sonnet5Fallback == nil {
		t.Fatal("expected explicit sonnet 5 fallback pricing entry")
	}
	if sonnet5Fallback == svc.fallbackPrices["claude-sonnet-4-6"] {
		t.Fatal("sonnet 5 pricing should be its own entry, not the sonnet 4.6 pointer")
	}

	pricing, err := svc.GetModelPricing("claude-sonnet-5")
	if err != nil {
		t.Fatalf("expected sonnet 5 pricing, got error %v", err)
	}
	if pricing.InputPricePerToken != 3e-6 {
		t.Fatalf("input pricing = %v, want %v", pricing.InputPricePerToken, 3e-6)
	}
	if pricing.OutputPricePerToken != 15e-6 {
		t.Fatalf("output pricing = %v, want %v", pricing.OutputPricePerToken, 15e-6)
	}
	if pricing.CacheCreationPricePerToken != 3.75e-6 {
		t.Fatalf("cache creation pricing = %v, want %v", pricing.CacheCreationPricePerToken, 3.75e-6)
	}
	if pricing.CacheReadPricePerToken != 0.3e-6 {
		t.Fatalf("cache read pricing = %v, want %v", pricing.CacheReadPricePerToken, 0.3e-6)
	}
}

func TestBillingServiceGetModelPricing_OpenAIGPTDoesNotUseSeparateCacheCreationPrice(t *testing.T) {
	svc := NewBillingService(nil, &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.5": {
				InputCostPerToken:           5e-06,
				OutputCostPerToken:          30e-06,
				CacheCreationInputTokenCost: 99e-06,
				CacheReadInputTokenCost:     5e-07,
			},
		},
	})

	pricing, err := svc.GetModelPricing("gpt-5.5")
	if err != nil {
		t.Fatalf("expected GPT pricing, got error %v", err)
	}
	if pricing.CacheCreationPricePerToken != 0 {
		t.Fatalf("cache creation pricing = %v, want 0", pricing.CacheCreationPricePerToken)
	}
	if pricing.CacheReadPricePerToken != 5e-07 {
		t.Fatalf("cache read pricing = %v, want %v", pricing.CacheReadPricePerToken, 5e-07)
	}
}

func TestBillingServiceGetModelPricing_GPT6AstraDoesNotUseSeparateCacheCreationPrice(t *testing.T) {
	svc := NewBillingService(nil, &PricingService{pricingData: map[string]*LiteLLMModelPricing{}})

	pricing, err := svc.GetModelPricing("gpt-6-astra")
	if err != nil {
		t.Fatalf("expected GPT-6 Astra pricing, got error %v", err)
	}
	if pricing.CacheCreationPricePerToken != 0 {
		t.Fatalf("cache creation pricing = %v, want 0", pricing.CacheCreationPricePerToken)
	}
	if pricing.CacheReadPricePerToken != 1e-06 {
		t.Fatalf("cache read pricing = %v, want %v", pricing.CacheReadPricePerToken, 1e-06)
	}
}

func TestBillingServiceCalculateCost_OpenAIGPTIgnoresCacheCreationCharge(t *testing.T) {
	svc := NewBillingService(nil, &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.5": {
				InputCostPerToken:           5e-06,
				OutputCostPerToken:          30e-06,
				CacheCreationInputTokenCost: 99e-06,
				CacheReadInputTokenCost:     5e-07,
			},
		},
	})

	cost, err := svc.CalculateCost("gpt-5.5", UsageTokens{
		InputTokens:         100,
		OutputTokens:        10,
		CacheCreationTokens: 20,
		CacheReadTokens:     30,
	}, 1.0)
	if err != nil {
		t.Fatalf("expected GPT pricing, got error %v", err)
	}

	if cost.CacheCreationCost != 0 {
		t.Fatalf("cache creation cost = %v, want 0", cost.CacheCreationCost)
	}

	wantTotal := float64(100)*5e-06 + float64(10)*30e-06 + float64(30)*5e-07
	if cost.TotalCost != wantTotal {
		t.Fatalf("total cost = %v, want %v", cost.TotalCost, wantTotal)
	}
}

func TestBillingServiceCalculateCost_FallsBackCacheReadToInputPriceWhenMissing(t *testing.T) {
	svc := NewBillingService(nil, &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gemini-unknown": {
				InputCostPerToken:  1e-06,
				OutputCostPerToken: 2e-06,
			},
		},
	})

	cost, err := svc.CalculateCost("gemini-unknown", UsageTokens{
		InputTokens:     100,
		OutputTokens:    10,
		CacheReadTokens: 30,
	}, 1.0)
	if err != nil {
		t.Fatalf("expected pricing, got error %v", err)
	}

	wantCacheRead := float64(30) * 1e-06
	if cost.CacheReadCost != wantCacheRead {
		t.Fatalf("cache read cost = %v, want %v", cost.CacheReadCost, wantCacheRead)
	}
}

func TestParsePricingData_PreservesServiceTierPriorityFields(t *testing.T) {
	svc := &PricingService{}
	pricingData, err := svc.parsePricingData([]byte(`{
		"gpt-5.4": {
			"input_cost_per_token": 0.0000025,
			"input_cost_per_token_priority": 0.000005,
			"output_cost_per_token": 0.000015,
			"output_cost_per_token_priority": 0.0000225,
			"cache_read_input_token_cost": 0.00000025,
			"cache_read_input_token_cost_priority": 0.0000005,
			"supports_service_tier": true,
			"litellm_provider": "openai",
			"mode": "chat"
		}
	}`))
	if err != nil {
		t.Fatalf("parsePricingData error = %v", err)
	}

	pricing := pricingData["gpt-5.4"]
	if pricing == nil {
		t.Fatalf("expected gpt-5.4 pricing")
	}
	if pricing.InputCostPerTokenPriority != 0.000005 {
		t.Fatalf("input priority pricing = %v, want %v", pricing.InputCostPerTokenPriority, 0.000005)
	}
	if pricing.OutputCostPerTokenPriority != 0.0000225 {
		t.Fatalf("output priority pricing = %v, want %v", pricing.OutputCostPerTokenPriority, 0.0000225)
	}
	if pricing.CacheReadInputTokenCostPriority != 0.0000005 {
		t.Fatalf("cache read priority pricing = %v, want %v", pricing.CacheReadInputTokenCostPriority, 0.0000005)
	}
	if !pricing.SupportsServiceTier {
		t.Fatalf("supports_service_tier = false, want true")
	}
}

func TestBillingServiceCalculateCostWithServiceTier_UsesPriorityPricing(t *testing.T) {
	svc := NewBillingService(nil, &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.5": {
				InputCostPerToken:               5e-06,
				InputCostPerTokenPriority:       1e-05,
				OutputCostPerToken:              30e-06,
				OutputCostPerTokenPriority:      60e-06,
				CacheReadInputTokenCost:         5e-07,
				CacheReadInputTokenCostPriority: 1e-06,
				SupportsServiceTier:             true,
			},
		},
	})

	cost, err := svc.CalculateCostWithServiceTier("gpt-5.5", UsageTokens{
		InputTokens:     100,
		OutputTokens:    10,
		CacheReadTokens: 30,
	}, 1.0, "priority")
	if err != nil {
		t.Fatalf("CalculateCostWithServiceTier error = %v", err)
	}

	wantTotal := float64(100)*1e-05 + float64(10)*60e-06 + float64(30)*1e-06
	if cost.TotalCost != wantTotal {
		t.Fatalf("total cost = %v, want %v", cost.TotalCost, wantTotal)
	}
}

func TestBillingServiceCalculateCostWithServiceTier_UsesGPT54LongContextPricing(t *testing.T) {
	svc := NewBillingService(nil, &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.4": {
				InputCostPerToken:       2.5e-06,
				OutputCostPerToken:      15e-06,
				CacheReadInputTokenCost: 2.5e-07,
			},
		},
	})

	cost, err := svc.CalculateCostWithServiceTier("gpt-5.4", UsageTokens{
		InputTokens:     300000,
		OutputTokens:    10,
		CacheReadTokens: 20,
	}, 1.0, "")
	if err != nil {
		t.Fatalf("CalculateCostWithServiceTier error = %v", err)
	}

	wantInput := float64(300000) * 5e-06
	wantOutput := float64(10) * 22.5e-06
	wantCacheRead := float64(20) * 5e-07
	wantTotal := wantInput + wantOutput + wantCacheRead

	if cost.InputCost != wantInput {
		t.Fatalf("input cost = %v, want %v", cost.InputCost, wantInput)
	}
	if cost.OutputCost != wantOutput {
		t.Fatalf("output cost = %v, want %v", cost.OutputCost, wantOutput)
	}
	if cost.CacheReadCost != wantCacheRead {
		t.Fatalf("cache read cost = %v, want %v", cost.CacheReadCost, wantCacheRead)
	}
	if cost.TotalCost != wantTotal {
		t.Fatalf("total cost = %v, want %v", cost.TotalCost, wantTotal)
	}
}

func TestBillingServiceLongContextCacheCreationOnlyAffectsGrok(t *testing.T) {
	svc := NewBillingService(nil, &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"gpt-5.4": {
			InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6, CacheCreationInputTokenCost: 3e-6,
			LongContextInputTokenThreshold: 200000, LongContextInputCostMultiplier: 2, LongContextOutputCostMultiplier: 2,
		},
	}})
	tokens := UsageTokens{InputTokens: 100000, CacheCreationTokens: 120000, OutputTokens: 1}
	gptCost, err := svc.CalculateCost("gpt-5.4", tokens, 1)
	if err != nil {
		t.Fatalf("GPT cost error = %v", err)
	}
	if math.Abs(gptCost.InputCost-100000e-6) > 1e-12 || math.Abs(gptCost.OutputCost-2e-6) > 1e-12 || gptCost.CacheCreationCost != 0 {
		t.Fatalf("GPT cost = %#v, cache creation should not trigger long-context pricing", gptCost)
	}

	grokCost, err := svc.CalculateCost("grok-4.6", UsageTokens{InputTokens: 100000, CacheCreationTokens: 100001, OutputTokens: 1}, 1)
	if err != nil {
		t.Fatalf("Grok cost error = %v", err)
	}
	if grokCost.InputCost <= 100000*2e-6 || grokCost.OutputCost <= 2e-6 {
		t.Fatalf("Grok cost = %#v, cache creation should trigger long-context pricing", grokCost)
	}
}

func TestBillingServiceCalculateCostWithServiceTier_GPT54PriorityFallsBackToGenericMultiplier(t *testing.T) {
	svc := NewBillingService(nil, &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.4": {
				InputCostPerToken:               2.5e-06,
				InputCostPerTokenPriority:       5e-06,
				OutputCostPerToken:              15e-06,
				OutputCostPerTokenPriority:      22.5e-06,
				CacheReadInputTokenCost:         2.5e-07,
				CacheReadInputTokenCostPriority: 5e-07,
			},
		},
	})

	cost, err := svc.CalculateCostWithServiceTier("gpt-5.4", UsageTokens{
		InputTokens:     100,
		OutputTokens:    10,
		CacheReadTokens: 20,
	}, 1.0, "priority")
	if err != nil {
		t.Fatalf("CalculateCostWithServiceTier error = %v", err)
	}

	wantTotal := float64(100)*5e-06 + float64(10)*30e-06 + float64(20)*5e-07
	if cost.TotalCost != wantTotal {
		t.Fatalf("total cost = %v, want %v", cost.TotalCost, wantTotal)
	}
}

func TestBillingServiceCalculateCostWithServiceTier_GPT54LongContextPriorityDoesNotStack(t *testing.T) {
	svc := NewBillingService(nil, &PricingService{
		pricingData: map[string]*LiteLLMModelPricing{
			"gpt-5.4": {
				InputCostPerToken:               2.5e-06,
				InputCostPerTokenPriority:       5e-06,
				OutputCostPerToken:              15e-06,
				OutputCostPerTokenPriority:      30e-06,
				CacheReadInputTokenCost:         2.5e-07,
				CacheReadInputTokenCostPriority: 5e-07,
				LongContextInputTokenThreshold:  272000,
				LongContextInputCostMultiplier:  2.0,
				LongContextOutputCostMultiplier: 1.5,
			},
		},
	})

	cost, err := svc.CalculateCostWithServiceTier("gpt-5.4", UsageTokens{
		InputTokens:     300000,
		OutputTokens:    10,
		CacheReadTokens: 20,
	}, 1.0, "priority")
	if err != nil {
		t.Fatalf("CalculateCostWithServiceTier error = %v", err)
	}

	wantInput := float64(300000) * 5e-06
	wantOutput := float64(10) * 22.5e-06
	wantCacheRead := float64(20) * 5e-07
	wantTotal := wantInput + wantOutput + wantCacheRead
	if cost.TotalCost != wantTotal {
		t.Fatalf("total cost = %v, want %v", cost.TotalCost, wantTotal)
	}
}
