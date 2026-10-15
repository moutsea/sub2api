package service

import (
	"context"
	"log"
	"strings"
)

const openAIImageGroupName = "gpt-image"

// ResolveOpenAIRequestGroupID routes OpenAI image requests to the dedicated
// image group while preserving the API key group as the fallback.
func (s *OpenAIGatewayService) ResolveOpenAIRequestGroupID(ctx context.Context, apiKey *APIKey, endpoint, model string, body []byte) *int64 {
	if apiKey == nil {
		return nil
	}
	current := apiKey.Group
	if current == nil || current.Platform != PlatformOpenAI || !IsImageGenerationIntent(endpoint, model, body) || strings.EqualFold(strings.TrimSpace(current.Name), openAIImageGroupName) {
		return apiKey.GroupID
	}
	if s == nil || s.groupRepo == nil {
		return apiKey.GroupID
	}

	groups, err := s.groupRepo.ListActiveByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		log.Printf("[OpenAI Image Routing] failed to resolve %q group: %v", openAIImageGroupName, err)
		return apiKey.GroupID
	}
	for index := range groups {
		group := &groups[index]
		if group.ID > 0 && group.IsActive() && group.Platform == PlatformOpenAI && strings.EqualFold(strings.TrimSpace(group.Name), openAIImageGroupName) {
			log.Printf("[OpenAI Image Routing] image request routed from group_id=%d to group_id=%d", current.ID, group.ID)
			return &group.ID
		}
	}

	log.Printf("[OpenAI Image Routing] active %q group not found; using group_id=%d", openAIImageGroupName, current.ID)
	return apiKey.GroupID
}
