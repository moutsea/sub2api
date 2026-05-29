package service

import "github.com/Wei-Shaw/sub2api/internal/pkg/claude"

const KiroModelOpus48 = "claude-opus-4-8"

var kiroAPIKeyOpus48Model = claude.Model{
	ID:          KiroModelOpus48,
	Type:        "model",
	DisplayName: "Claude Opus 4.8",
	CreatedAt:   "2026-05-29T00:00:00Z",
}

var kiroAPIKeyDefaultModels = buildKiroAPIKeyDefaultModels()

// KiroAPIKeyDefaultModels returns the Claude-compatible model list for Kiro
// apikey passthrough accounts. This list is independent from the Kiro OAuth
// runtime allowlist because apikey accounts are Claude-compatible passthrough.
func KiroAPIKeyDefaultModels() []claude.Model {
	return kiroAPIKeyDefaultModels
}

func buildKiroAPIKeyDefaultModels() []claude.Model {
	models := make([]claude.Model, 0, len(claude.DefaultModels)+1)
	models = append(models, kiroAPIKeyOpus48Model)
	for _, model := range claude.DefaultModels {
		if model.ID == KiroModelOpus48 {
			continue
		}
		models = append(models, model)
	}
	return models
}
