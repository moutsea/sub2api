package geminicli

// Model represents a selectable Gemini model for UI/testing purposes.
// Keep JSON fields consistent with existing frontend expectations.
type Model struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
}

// DefaultModels is the curated Gemini model list used by the admin UI "test account" flow.
var DefaultModels = []Model{
	{ID: "gemini-3.5-flash", Type: "model", DisplayName: "Gemini 3.5 Flash", CreatedAt: ""},
	{ID: "gemini-3.1-pro-preview", Type: "model", DisplayName: "Gemini 3.1 Pro Preview", CreatedAt: ""},
	{ID: "gemini-3.1-pro-preview-customtools", Type: "model", DisplayName: "Gemini 3.1 Pro Preview (Custom Tools)", CreatedAt: ""},
	{ID: "gemini-3-flash-preview", Type: "model", DisplayName: "Gemini 3 Flash Preview", CreatedAt: ""},
	{ID: "gemini-3.1-flash-lite", Type: "model", DisplayName: "Gemini 3.1 Flash-Lite", CreatedAt: ""},
	{ID: "gemini-3.1-flash-image", Type: "model", DisplayName: "Nano Banana 2", CreatedAt: ""},
	{ID: "gemini-3.1-flash-lite-image", Type: "model", DisplayName: "Nano Banana Lite", CreatedAt: ""},
	{ID: "gemini-3-pro-image", Type: "model", DisplayName: "Nano Banana Pro", CreatedAt: ""},
	{ID: "gemini-pro-latest", Type: "model", DisplayName: "Gemini Pro Latest", CreatedAt: ""},
	{ID: "gemini-flash-latest", Type: "model", DisplayName: "Gemini Flash Latest", CreatedAt: ""},
	{ID: "gemini-2.5-flash", Type: "model", DisplayName: "Gemini 2.5 Flash", CreatedAt: ""},
	{ID: "gemini-2.5-flash-lite", Type: "model", DisplayName: "Gemini 2.5 Flash-Lite", CreatedAt: ""},
	{ID: "gemini-2.5-flash-image", Type: "model", DisplayName: "Gemini 2.5 Flash Image", CreatedAt: ""},
	{ID: "gemini-2.5-pro", Type: "model", DisplayName: "Gemini 2.5 Pro", CreatedAt: ""},
	{ID: "gemini-3.1-flash-tts-preview", Type: "model", DisplayName: "Gemini 3.1 Flash TTS Preview", CreatedAt: ""},
	{ID: "gemini-2.5-flash-preview-tts", Type: "model", DisplayName: "Gemini 2.5 Flash TTS Preview", CreatedAt: ""},
	{ID: "gemini-2.5-pro-preview-tts", Type: "model", DisplayName: "Gemini 2.5 Pro TTS Preview", CreatedAt: ""},
	{ID: "gemini-2.0-flash", Type: "model", DisplayName: "Gemini 2.0 Flash", CreatedAt: ""},
	{ID: "gemini-3-pro-preview", Type: "model", DisplayName: "Gemini 3 Pro Preview", CreatedAt: ""},
}

// DefaultTestModel is the default model to preselect in test flows.
const DefaultTestModel = "gemini-2.5-flash"
