//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type availableModelsAccountRepo struct {
	AccountRepository
	accounts []Account
}

func (r *availableModelsAccountRepo) ListSchedulableByGroupID(ctx context.Context, groupID int64) ([]Account, error) {
	return r.accounts, nil
}

func TestGatewayServiceGetAvailableModelsOpenAIDefaultsWithoutMapping(t *testing.T) {
	groupID := int64(10)
	repo := &availableModelsAccountRepo{
		accounts: []Account{{Platform: PlatformOpenAI, Type: AccountTypeOAuth}},
	}
	svc := &GatewayService{accountRepo: repo}

	models := svc.GetAvailableModels(context.Background(), &groupID, PlatformOpenAI)

	require.Contains(t, models, "gpt-6-astra")
	require.Contains(t, models, "gpt-5.6")
}

func TestGatewayServiceGetAvailableModelsOpenAIAPIKeyExcludesOAuthOnlyModels(t *testing.T) {
	groupID := int64(10)
	repo := &availableModelsAccountRepo{
		accounts: []Account{{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}},
	}
	svc := &GatewayService{accountRepo: repo}

	models := svc.GetAvailableModels(context.Background(), &groupID, PlatformOpenAI)

	require.Contains(t, models, "gpt-6-astra")
	require.NotContains(t, models, "gpt-5.5")
}

func TestGatewayServiceGetAvailableModelsOpenAIDefaultsWithoutPlatformFilter(t *testing.T) {
	groupID := int64(10)
	repo := &availableModelsAccountRepo{
		accounts: []Account{{Platform: PlatformOpenAI, Type: AccountTypeOAuth}},
	}
	svc := &GatewayService{accountRepo: repo}

	models := svc.GetAvailableModels(context.Background(), &groupID, "")

	require.Contains(t, models, "gpt-6-astra")
	require.Contains(t, models, "gpt-5.6")
}
