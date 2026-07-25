package service

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
)

const KiroModelOpus48 = "claude-opus-4-8"
const KiroModelOpus5 = "claude-opus-5"

var kiroAPIKeyOpus48Model = claude.Model{
	ID:          KiroModelOpus48,
	Type:        "model",
	DisplayName: "Claude Opus 4.8",
	CreatedAt:   "2026-05-29T00:00:00Z",
}

var kiroAPIKeyOpus5Model = claude.Model{
	ID:          KiroModelOpus5,
	Type:        "model",
	DisplayName: "Claude Opus 5",
	CreatedAt:   "2026-07-25T00:00:00Z",
}

var kiroAPIKeyDefaultModels = buildKiroAPIKeyDefaultModels()

// KiroAPIKeyDefaultModels returns suggested models for custom Anthropic
// Messages-compatible endpoints. The upstream remains authoritative and can
// accept model IDs beyond this list when no explicit mapping is configured.
func KiroAPIKeyDefaultModels() []claude.Model {
	return kiroAPIKeyDefaultModels
}

func buildKiroAPIKeyDefaultModels() []claude.Model {
	models := make([]claude.Model, 0, len(kiro.DefaultModels)+len(claude.DefaultModels)+2)
	seen := make(map[string]struct{}, cap(models))
	appendModel := func(model claude.Model) {
		if _, exists := seen[model.ID]; exists {
			return
		}
		seen[model.ID] = struct{}{}
		models = append(models, model)
	}

	appendModel(kiroAPIKeyOpus5Model)
	appendModel(kiroAPIKeyOpus48Model)
	for _, model := range kiro.DefaultModels {
		appendModel(claude.Model{
			ID:          model.ID,
			Type:        model.Type,
			DisplayName: model.DisplayName,
			CreatedAt:   model.CreatedAt,
		})
	}
	for _, model := range claude.DefaultModels {
		appendModel(model)
	}
	return models
}
