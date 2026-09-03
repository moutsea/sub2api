package xai

import "testing"

func TestDefaultTextModelAndAliasesStayAligned(t *testing.T) {
	mapping := DefaultModelMapping()
	if got := mapping["grok"]; got != DefaultTextModel {
		t.Fatalf("grok alias = %q, want %q", got, DefaultTextModel)
	}
	if got := mapping["grok-latest"]; got != DefaultTextModel {
		t.Fatalf("grok-latest alias = %q, want %q", got, DefaultTextModel)
	}
	models := DefaultModels()
	if len(models) == 0 || models[0].ID != DefaultTextModel {
		t.Fatalf("first default model = %#v, want %q", models, DefaultTextModel)
	}
}
