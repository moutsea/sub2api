package service

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Gemini may downgrade a Priority request to Standard. Prefer the actual tier
// reported by the upstream and fall back to the request only if it is absent.
// https://ai.google.dev/gemini-api/docs/generate-content/priority-inference
func resolveGeminiServiceTier(headers http.Header, requestBody []byte) string {
	if tiers := headers.Values("X-Gemini-Service-Tier"); len(tiers) > 0 {
		return normalizeGeminiServiceTier(tiers[0])
	}
	var request struct {
		ServiceTier      string `json:"serviceTier"`
		ServiceTierSnake string `json:"service_tier"`
	}
	if err := json.Unmarshal(requestBody, &request); err != nil {
		return "standard"
	}
	if request.ServiceTier != "" {
		return normalizeGeminiServiceTier(request.ServiceTier)
	}
	return normalizeGeminiServiceTier(request.ServiceTierSnake)
}

func normalizeGeminiServiceTier(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "priority":
		return "priority"
	case "flex":
		return "flex"
	default:
		return "standard"
	}
}
