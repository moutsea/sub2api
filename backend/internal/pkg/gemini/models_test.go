package gemini

import "testing"

func TestDefaultModelsContainsFallbackCatalogModels(t *testing.T) {
	t.Parallel()

	models := DefaultModels()
	byName := make(map[string]Model, len(models))
	for _, model := range models {
		if _, exists := byName[model.Name]; exists {
			t.Fatalf("duplicate fallback Gemini model %q", model.Name)
		}
		byName[model.Name] = model
	}

	required := []string{
		"models/gemini-3.5-flash",
		"models/gemini-3.1-pro-preview",
		"models/gemini-3.1-flash-lite",
		"models/gemini-3.1-flash-lite-image",
		"models/gemini-3-pro-image",
		"models/gemini-pro-latest",
		"models/gemini-flash-latest",
		"models/gemini-2.5-flash-lite",
		"models/gemini-2.5-flash-image",
		"models/gemini-3.1-pro-preview-customtools",
		"models/gemini-3.1-flash-image",
	}

	for _, name := range required {
		model, ok := byName[name]
		if !ok {
			t.Fatalf("expected fallback model %q to exist", name)
		}
		if len(model.SupportedGenerationMethods) == 0 {
			t.Fatalf("expected fallback model %q to advertise generation methods", name)
		}
	}
}

func TestHasFallbackModelRecognizesCustomtoolsModel(t *testing.T) {
	t.Parallel()

	if !HasFallbackModel("gemini-3.1-pro-preview-customtools") {
		t.Fatalf("expected customtools model to exist in fallback catalog")
	}
	if !HasFallbackModel("models/gemini-3.1-pro-preview-customtools") {
		t.Fatalf("expected prefixed customtools model to exist in fallback catalog")
	}
	if HasFallbackModel("gemini-unknown") {
		t.Fatalf("did not expect unknown model to exist in fallback catalog")
	}
}
