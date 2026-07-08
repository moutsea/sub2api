// Package gemini provides minimal fallback model metadata for Gemini native endpoints.
// It is used when upstream model listing is unavailable (e.g. OAuth token missing AI Studio scopes).
package gemini

import "strings"

type Model struct {
	Name                       string   `json:"name"`
	DisplayName                string   `json:"displayName,omitempty"`
	Description                string   `json:"description,omitempty"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods,omitempty"`
}

type ModelsListResponse struct {
	Models []Model `json:"models"`
}

func DefaultModels() []Model {
	methods := []string{"generateContent", "streamGenerateContent"}
	return []Model{
		{Name: "models/gemini-3.5-flash", DisplayName: "Gemini 3.5 Flash", SupportedGenerationMethods: methods},
		{Name: "models/gemini-3.1-pro-preview", DisplayName: "Gemini 3.1 Pro Preview", SupportedGenerationMethods: methods},
		{Name: "models/gemini-3.1-pro-preview-customtools", DisplayName: "Gemini 3.1 Pro Preview (Custom Tools)", SupportedGenerationMethods: methods},
		{Name: "models/gemini-3-flash-preview", DisplayName: "Gemini 3 Flash Preview", SupportedGenerationMethods: methods},
		{Name: "models/gemini-3.1-flash-lite", DisplayName: "Gemini 3.1 Flash-Lite", SupportedGenerationMethods: methods},
		{Name: "models/gemini-3.1-flash-image", DisplayName: "Nano Banana 2", SupportedGenerationMethods: methods},
		{Name: "models/gemini-3.1-flash-lite-image", DisplayName: "Nano Banana Lite", SupportedGenerationMethods: methods},
		{Name: "models/gemini-3-pro-image", DisplayName: "Nano Banana Pro", SupportedGenerationMethods: methods},
		{Name: "models/gemini-pro-latest", DisplayName: "Gemini Pro Latest", SupportedGenerationMethods: methods},
		{Name: "models/gemini-flash-latest", DisplayName: "Gemini Flash Latest", SupportedGenerationMethods: methods},
		{Name: "models/gemini-2.5-flash", SupportedGenerationMethods: methods},
		{Name: "models/gemini-2.5-flash-lite", SupportedGenerationMethods: methods},
		{Name: "models/gemini-2.5-flash-image", SupportedGenerationMethods: methods},
		{Name: "models/gemini-2.5-pro", SupportedGenerationMethods: methods},
		{Name: "models/gemini-3.1-flash-tts-preview", DisplayName: "Gemini 3.1 Flash TTS Preview", SupportedGenerationMethods: methods},
		{Name: "models/gemini-2.5-flash-preview-tts", DisplayName: "Gemini 2.5 Flash TTS Preview", SupportedGenerationMethods: methods},
		{Name: "models/gemini-2.5-pro-preview-tts", DisplayName: "Gemini 2.5 Pro TTS Preview", SupportedGenerationMethods: methods},
		{Name: "models/gemini-2.0-flash", SupportedGenerationMethods: methods},
		{Name: "models/gemini-3-pro-preview", SupportedGenerationMethods: methods},
	}
}

func HasFallbackModel(model string) bool {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return false
	}
	if !strings.HasPrefix(trimmed, "models/") {
		trimmed = "models/" + trimmed
	}
	for _, fallbackModel := range DefaultModels() {
		if fallbackModel.Name == trimmed {
			return true
		}
	}
	return false
}

func FallbackModelsList() ModelsListResponse {
	return ModelsListResponse{Models: DefaultModels()}
}

func FallbackModel(model string) Model {
	methods := []string{"generateContent", "streamGenerateContent"}
	if model == "" {
		return Model{Name: "models/unknown", SupportedGenerationMethods: methods}
	}
	if len(model) >= 7 && model[:7] == "models/" {
		return Model{Name: model, SupportedGenerationMethods: methods}
	}
	return Model{Name: "models/" + model, SupportedGenerationMethods: methods}
}
