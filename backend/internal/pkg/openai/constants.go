// Package openai provides helpers and types for OpenAI API integration.
package openai

import _ "embed"

// Model represents an OpenAI model
type Model struct {
	ID          string `json:"id"`
	Object      string `json:"object"`
	Created     int64  `json:"created"`
	OwnedBy     string `json:"owned_by"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
}

// DefaultModels OpenAI models list
var DefaultModels = []Model{
	{ID: "gpt-5.6-sol", Object: "model", Created: 1783641600, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.6 Sol"},
	{ID: "gpt-5.6-terra", Object: "model", Created: 1783641600, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.6 Terra"},
	{ID: "gpt-5.6", Object: "model", Created: 1783641600, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.6"},
	{ID: "gpt-5.5", Object: "model", Created: 1776988800, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.5"},
	{ID: "gpt-5.4-mini", Object: "model", Created: 0, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.4 Mini"},
	{ID: "gpt-5.4", Object: "model", Created: 1772755200, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.4"},
	{ID: "codex-auto-review", Object: "model", Created: 0, OwnedBy: "openai", Type: "model", DisplayName: "Codex Auto Review"},
	{ID: "gpt-5.3-codex-spark", Object: "model", Created: 0, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.3 Codex Spark"},
	{ID: "gpt-5.1-codex-max", Object: "model", Created: 1730419200, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.1 Codex Max"},
	{ID: "gpt-5.1-codex", Object: "model", Created: 1730419200, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.1 Codex"},
	{ID: "gpt-5.1", Object: "model", Created: 1731456000, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.1"},
	{ID: "gpt-5.1-codex-mini", Object: "model", Created: 1730419200, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5.1 Codex Mini"},
	{ID: "gpt-5", Object: "model", Created: 1722988800, OwnedBy: "openai", Type: "model", DisplayName: "GPT-5"},
	{ID: "gpt-image-1", Object: "model", Created: 1733875200, OwnedBy: "openai", Type: "model", DisplayName: "GPT Image 1"},
	{ID: "gpt-image-1.5", Object: "model", Created: 1735689600, OwnedBy: "openai", Type: "model", DisplayName: "GPT Image 1.5"},
	{ID: "gpt-image-2", Object: "model", Created: 1738368000, OwnedBy: "openai", Type: "model", DisplayName: "GPT Image 2"},
}

// DefaultModelIDs returns the default model ID list
func DefaultModelIDs() []string {
	ids := make([]string, len(DefaultModels))
	for i, m := range DefaultModels {
		ids[i] = m.ID
	}
	return ids
}

func cloneModels(models []Model) []Model {
	cloned := make([]Model, len(models))
	copy(cloned, models)
	return cloned
}

func isOAuthOnlyModel(modelID string) bool {
	switch modelID {
	case "gpt-5.5", "codex-auto-review", "gpt-5.3-codex-spark":
		return true
	default:
		return false
	}
}

// DefaultModelsForAccount returns the curated OpenAI model list for the given account type.
func DefaultModelsForAccount(isOAuth bool) []Model {
	if isOAuth {
		return cloneModels(DefaultModels)
	}

	models := make([]Model, 0, len(DefaultModels))
	for _, model := range DefaultModels {
		if isOAuthOnlyModel(model.ID) {
			continue
		}
		models = append(models, model)
	}
	return models
}

const (
	DefaultOAuthTestModel  = "gpt-5.5"
	DefaultAPIKeyTestModel = "gpt-5.4"
)

// DefaultTestModelForAccount returns the default test model for the given account type.
func DefaultTestModelForAccount(isOAuth bool) string {
	if isOAuth {
		return DefaultOAuthTestModel
	}
	return DefaultAPIKeyTestModel
}

// DefaultTestModel default model for testing OpenAI accounts
const DefaultTestModel = DefaultAPIKeyTestModel

// DefaultInstructions default instructions for non-Codex CLI requests
// Content loaded from instructions.txt at compile time
//
//go:embed instructions.txt
var DefaultInstructions string
