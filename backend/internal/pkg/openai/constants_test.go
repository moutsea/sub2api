package openai

import "testing"

func TestDefaultModelsForAccount_APIKeyExcludesOAuthOnlyModels(t *testing.T) {
	models := DefaultModelsForAccount(false)
	for _, model := range models {
		if model.ID == "gpt-5.5" {
			t.Fatalf("did not expect API key model list to include %q", model.ID)
		}
	}
}

func TestDefaultModelsForAccount_OAuthIncludesGPT55(t *testing.T) {
	models := DefaultModelsForAccount(true)
	if len(models) == 0 || models[0].ID != "gpt-5.5" {
		t.Fatalf("expected OAuth model list to start with gpt-5.5, got %+v", models)
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
