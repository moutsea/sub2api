package geminicli

import "testing"

func TestDefaultModelsContainsImageModels(t *testing.T) {
	t.Parallel()

	byID := make(map[string]Model, len(DefaultModels))
	for _, model := range DefaultModels {
		if _, exists := byID[model.ID]; exists {
			t.Fatalf("duplicate curated Gemini model %q", model.ID)
		}
		byID[model.ID] = model
	}

	required := []string{
		"gemini-3.5-flash",
		"gemini-3.1-pro-preview",
		"gemini-3.1-flash-lite",
		"gemini-3.1-flash-lite-image",
		"gemini-3-pro-image",
		"gemini-pro-latest",
		"gemini-flash-latest",
		"gemini-2.5-flash-lite",
		"gemini-2.5-flash-image",
		"gemini-3.1-pro-preview-customtools",
		"gemini-3.1-flash-image",
	}

	for _, id := range required {
		if _, ok := byID[id]; !ok {
			t.Fatalf("expected curated Gemini model %q to exist", id)
		}
	}
}

func TestDefaultTestModelUsesActiveStableModel(t *testing.T) {
	t.Parallel()

	if DefaultTestModel != "gemini-2.5-flash" {
		t.Fatalf("expected default test model to use active stable Gemini model, got %q", DefaultTestModel)
	}
}
