package xai

import "strings"

// Model describes an xAI model in OpenAI-compatible /models shape.
type Model struct {
	ID          string `json:"id"`
	Object      string `json:"object"`
	Type        string `json:"type,omitempty"`
	Created     int64  `json:"created,omitempty"`
	OwnedBy     string `json:"owned_by"`
	DisplayName string `json:"display_name,omitempty"`
}

// DefaultTextModel is the fallback model for Grok text requests and aliases.
const DefaultTextModel = "grok-4.6"

var defaultModels = []Model{
	{ID: DefaultTextModel, Object: "model", Type: "model", OwnedBy: "xai", DisplayName: "Grok 4.6"},
	{ID: "grok-4.5", Object: "model", Type: "model", OwnedBy: "xai", DisplayName: "Grok 4.5"},
	{ID: "grok-4.3", Object: "model", Type: "model", OwnedBy: "xai", DisplayName: "Grok 4.3"},
	{ID: "grok-3-mini", Object: "model", Type: "model", OwnedBy: "xai", DisplayName: "Grok 3 Mini"},
	{ID: "grok-3-mini-fast", Object: "model", Type: "model", OwnedBy: "xai", DisplayName: "Grok 3 Mini Fast"},
	{ID: "grok-build-0.1", Object: "model", Type: "model", OwnedBy: "xai", DisplayName: "Grok Build 0.1"},
	{ID: "grok-composer-2.5-fast", Object: "model", Type: "model", OwnedBy: "xai", DisplayName: "Grok Composer 2.5 Fast"},
	{ID: "grok-4.20-0309-reasoning", Object: "model", Type: "model", OwnedBy: "xai", DisplayName: "Grok 4.20 Reasoning"},
	{ID: "grok-4.20-0309-non-reasoning", Object: "model", Type: "model", OwnedBy: "xai", DisplayName: "Grok 4.20 Non Reasoning"},
	{ID: "grok-4.20-multi-agent-0309", Object: "model", Type: "model", OwnedBy: "xai", DisplayName: "Grok 4.20 Multi Agent"},
	{ID: "grok-imagine-image-quality", Object: "model", Type: "model", OwnedBy: "xai", DisplayName: "Grok Imagine Image Quality"},
	{ID: "grok-imagine-image", Object: "model", Type: "model", OwnedBy: "xai", DisplayName: "Grok Imagine Image"},
	{ID: "grok-imagine-image-2.0", Object: "model", Type: "model", OwnedBy: "xai", DisplayName: "Grok Imagine Image 2.0"},
	{ID: "grok-imagine-video", Object: "model", Type: "model", OwnedBy: "xai", DisplayName: "Grok Imagine Video"},
	{ID: "grok-imagine-video-1.5", Object: "model", Type: "model", OwnedBy: "xai", DisplayName: "Grok Imagine Video 1.5"},
}

func DefaultModels() []Model {
	out := make([]Model, len(defaultModels))
	copy(out, defaultModels)
	return out
}

func DefaultModelIDs() []string {
	models := DefaultModels()
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}

func DefaultModelMapping() map[string]string {
	mapping := make(map[string]string, len(defaultModels)+20)
	for _, model := range defaultModels {
		mapping[model.ID] = model.ID
	}
	mapping["grok"] = DefaultTextModel
	mapping["grok-latest"] = DefaultTextModel
	mapping["grok-4.6-latest"] = "grok-4.6"
	mapping["grok-4.5-latest"] = "grok-4.5"
	mapping["grok-4.3-latest"] = "grok-4.3"
	mapping["grok-3-mini-fast"] = "grok-3-mini-fast"
	mapping["grok-3-mini-latest"] = "grok-3-mini"
	mapping["grok-build"] = "grok-build-0.1"
	mapping["grok-build-latest"] = "grok-build-0.1"
	mapping["grok-composer"] = "grok-composer-2.5-fast"
	mapping["composer-2.5"] = "grok-composer-2.5-fast"
	mapping["grok-4.20-reasoning"] = "grok-4.20-0309-reasoning"
	mapping["grok-4.20-non-reasoning"] = "grok-4.20-0309-non-reasoning"
	mapping["grok-4.20-multi-agent"] = "grok-4.20-multi-agent-0309"
	mapping["grok-4.20-multi-agent-latest"] = "grok-4.20-multi-agent-0309"
	mapping["grok-imagine"] = "grok-imagine-image-quality"
	mapping["grok-imagine-1"] = "grok-imagine-image-quality"
	mapping["grok-imagine-edit"] = "grok-imagine-image-quality"
	mapping["grok-imagine-image"] = "grok-imagine-image"
	mapping["grok-imagine-image-quality"] = "grok-imagine-image-quality"
	mapping["grok-imagine-video"] = "grok-imagine-video"
	mapping["grok-imagine-video-1.5-preview"] = "grok-imagine-video-1.5"
	mapping["grok-video-1.5"] = "grok-imagine-video-1.5"
	return mapping
}

// StripProviderPrefix removes the provider namespace accepted by OpenAI
// clients (for example xai/grok-4.5, x-ai/grok-4.5, or grok/grok-4.5).
// It intentionally leaves unknown namespaces untouched.
func StripProviderPrefix(model string) string {
	trimmed := strings.TrimSpace(model)
	lower := strings.ToLower(trimmed)
	for _, prefix := range []string{"xai/", "x-ai/", "grok/"} {
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(trimmed[len(prefix):])
		}
	}
	return trimmed
}

// ResolveModelID canonicalizes the built-in Grok aliases while preserving
// provider model IDs that are not part of the curated catalog. This lets an
// account use newly released xAI models without requiring a mapping update.
func ResolveModelID(model string) string {
	model = StripProviderPrefix(model)
	if mapped, ok := DefaultModelMapping()[strings.ToLower(model)]; ok {
		return mapped
	}
	return model
}

// IsGrokModelID reports whether a model name belongs to the xAI namespace.
// Unknown grok-* IDs are retained so newly released models do not require a
// code update, while foreign client model names can safely fall back to the
// configured default model.
func IsGrokModelID(model string) bool {
	model = strings.ToLower(StripProviderPrefix(model))
	return model == "grok" || strings.HasPrefix(model, "grok-")
}

// IsGrokImagineModel reports whether a model is an xAI image/video model.
// Imagine models use the media endpoints and must not be sent to /responses.
func IsGrokImagineModel(model string) bool {
	model = strings.ToLower(StripProviderPrefix(model))
	return strings.HasPrefix(model, "grok-imagine") || model == "grok-video-1.5"
}
