package service

import "testing"

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
		{model: "gpt-5.3", input: 1.75e-06, output: 14e-06},
		{model: "gpt-5.4", input: 2.5e-06, output: 15e-06},
		{model: "gpt-5.5", input: 5e-06, output: 30e-06},
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
