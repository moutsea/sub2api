package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/gin-gonic/gin"
)

func (s *KiroGatewayService) beginKiroOAuthCache(c *gin.Context, account *Account, model string, req *kiro.ClaudeRequest, estimation kiro.CacheEstimation) kiro.CacheResult {
	if account == nil || account.IsKiroApiKey() || c == nil {
		return kiro.CacheResult{}
	}
	return kiro.GlobalCacheTracker.BeginPrefix(kiro.CacheScope{
		AccountID: account.ID,
		Model:     model,
		SessionID: kiro.GenerateStableConversationID(c),
		ClientKey: kiro.ExtractAPIKey(c),
	}, req, estimation)
}

func (s *KiroGatewayService) applyKiroSimulatedCacheReadRatio(cacheRead, cacheCreation int) (int, int) {
	ratio := 1.0
	if s.settingService != nil {
		ratio = s.settingService.GetKiroSimulatedCacheReadRatio(context.Background())
	} else if s.cfg != nil {
		ratio = s.cfg.Kiro.KiroSimulatedCacheReadRatioOrDefault()
	}
	if cacheRead <= 0 || ratio >= 1 {
		return cacheRead, cacheCreation
	}
	return int(float64(cacheRead) * ratio), cacheCreation
}
