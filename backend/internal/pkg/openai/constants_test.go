package openai

import "testing"

func TestDefaultModelsForAccount_APIKeyExcludesOAuthOnlyModels(t *testing.T) {
	models := DefaultModelsForAccount(false)
	for _, model := range models {
		if model.ID == "gpt-5.5" || model.ID == "codex-auto-review" || model.ID == "gpt-5.3-codex-spark" {
			t.Fatalf("did not expect API key model list to include %q", model.ID)
		}
	}

	foundMini := false
	for _, model := range models {
		if model.ID == "gpt-5.4-mini" {
			foundMini = true
			break
		}
	}
	if !foundMini {
		t.Fatalf("expected API key model list to include gpt-5.4-mini")
	}
	foundModels := map[string]bool{}
	for _, model := range models {
		foundModels[model.ID] = true
	}
	for _, modelID := range []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6"} {
		if !foundModels[modelID] {
			t.Fatalf("expected API key model list to include %s", modelID)
		}
	}
}

func TestDefaultModels_ExcludesDeprecatedCodexModels(t *testing.T) {
	deprecated := map[string]bool{
		"gpt-5.2":              true,
		"gpt-5.2-codex":        true,
		"gpt-5.2-codex-xhigh":  true,
		"gpt-5.3-codex":        true,
		"gpt-5.3-codex-low":    true,
		"gpt-5.3-codex-medium": true,
		"gpt-5.3-codex-high":   true,
		"gpt-5.3-codex-xhigh":  true,
	}
	for _, model := range DefaultModels {
		if deprecated[model.ID] {
			t.Fatalf("did not expect default model list to include deprecated model %q", model.ID)
		}
	}
}

func TestDefaultModelsForAccount_OAuthIncludesNewestGPTModels(t *testing.T) {
	models := DefaultModelsForAccount(true)
	if len(models) == 0 || models[0].ID != "gpt-6-astra" {
		t.Fatalf("expected OAuth model list to start with gpt-6-astra, got %+v", models)
	}
	found := map[string]bool{}
	for _, model := range models {
		found[model.ID] = true
	}
	for _, modelID := range []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6", "gpt-5.4-mini", "gpt-5.3-codex-spark", "codex-auto-review"} {
		if !found[modelID] {
			t.Fatalf("expected OAuth model list to include %s", modelID)
		}
	}
}

func TestDefaultModelsIncludeGPTImage25Models(t *testing.T) {
	found := map[string]bool{}
	for _, model := range DefaultModels {
		found[model.ID] = true
	}
	for _, modelID := range []string{"gpt-image-2.5-sunburst", "gpt-image-2.5-flare"} {
		if !found[modelID] {
			t.Fatalf("expected default model list to include %s", modelID)
		}
	}
}

func TestDefaultTestModelForAccount(t *testing.T) {
	if got := DefaultTestModelForAccount(true); got != DefaultOAuthTestModel {
		t.Fatalf("oauth test model = %q, want %q", got, DefaultOAuthTestModel)
	}
	if got := DefaultTestModelForAccount(false); got != DefaultAPIKeyTestModel {
		t.Fatalf("api key test model = %q, want %q", got, DefaultAPIKeyTestModel)
	}
}
