package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type imageRoutingGroupRepo struct {
	service.GroupRepository
	groups []service.Group
}

func (repo imageRoutingGroupRepo) ListActiveByPlatform(context.Context, string) ([]service.Group, error) {
	return repo.groups, nil
}

type imageRoutingAccountRepo struct {
	service.AccountRepository
	groupIDs []int64
}

func (repo *imageRoutingAccountRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, _ string) ([]service.Account, error) {
	repo.groupIDs = append(repo.groupIDs, groupID)
	return nil, errors.New("stop after account lookup")
}

type imageRoutingGatewayCache struct {
	service.GatewayCache
	groupIDs []int64
}

func (cache *imageRoutingGatewayCache) GetSessionAccountID(_ context.Context, groupID int64, _ string) (int64, error) {
	cache.groupIDs = append(cache.groupIDs, groupID)
	return 0, nil
}

type imageRoutingBillingCache struct {
	service.BillingCache
}

func (imageRoutingBillingCache) GetUserBalance(context.Context, int64) (float64, error) {
	return 10, nil
}

func TestOpenAIImageGroupRouting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, testCase := range []struct {
		name        string
		path        string
		body        string
		platform    string
		wantGroupID int64
	}{
		{"image generation", "/v1/images/generations", `{"prompt":"draw a cat"}`, service.PlatformOpenAI, 2},
		{"image alias", "/images/generations", `{"prompt":"draw a cat"}`, service.PlatformOpenAI, 2},
		{"responses image tool", "/v1/responses", `{"model":"gpt-5.5","input":"draw a cat","tools":[{"type":"image_generation"}]}`, service.PlatformOpenAI, 2},
		{"responses image tool disabled", "/v1/responses", `{"model":"gpt-5.5","input":"hello","tools":[{"type":"image_generation"}],"tool_choice":"none"}`, service.PlatformOpenAI, 1},
		{"responses other tool selected", "/v1/responses", `{"model":"gpt-5.5","input":"hello","tools":[{"type":"image_generation"},{"type":"function","name":"search"}],"tool_choice":{"type":"function","name":"search"}}`, service.PlatformOpenAI, 1},
		{"text response", "/v1/responses", `{"model":"gpt-5.5","input":"hello"}`, service.PlatformOpenAI, 1},
		{"text chat", "/v1/chat/completions", `{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}]}`, service.PlatformOpenAI, 1},
		{"grok image", "/v1/images/generations", `{"prompt":"draw a cat"}`, service.PlatformGrok, 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := &config.Config{}
			billing := service.NewBillingCacheService(imageRoutingBillingCache{}, nil, nil, cfg)
			t.Cleanup(billing.Stop)
			accounts := &imageRoutingAccountRepo{}
			cache := &imageRoutingGatewayCache{}
			groups := imageRoutingGroupRepo{groups: []service.Group{{ID: 2, Name: "gpt-image", Platform: service.PlatformOpenAI, Status: service.StatusActive}}}
			gateway := service.NewOpenAIGatewayService(accounts, groups, nil, nil, nil, cache, cfg, nil, nil, nil, nil, billing, nil, nil, nil, nil, nil, nil, nil, nil)
			handler := NewOpenAIGatewayHandler(gateway, nil, service.NewConcurrencyService(nil), billing, cfg)
			group := &service.Group{ID: 1, Name: "default", Platform: testCase.platform}
			key := &service.APIKey{GroupID: &group.ID, Group: group, User: &service.User{ID: 1}}
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, testCase.path, strings.NewReader(testCase.body))
			ctx.Request.Header.Set("Content-Type", "application/json")
			ctx.Request.Header.Set("User-Agent", "codex_cli_rs/0.98.0")
			ctx.Request.Header.Set("session_id", "shared-text-and-image-session")
			ctx.Set(string(middleware2.ContextKeyAPIKey), key)
			ctx.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 1})

			switch testCase.path {
			case "/v1/responses":
				handler.Responses(ctx)
			case "/v1/chat/completions":
				handler.ChatCompletions(ctx)
			default:
				handler.Images(ctx)
			}

			require.Equal(t, http.StatusServiceUnavailable, recorder.Code, recorder.Body.String())
			require.Equal(t, []int64{testCase.wantGroupID}, accounts.groupIDs)
			require.NotEmpty(t, cache.groupIDs)
			for _, groupID := range cache.groupIDs {
				require.Equal(t, testCase.wantGroupID, groupID)
			}
			require.Same(t, group, key.Group)
			require.Equal(t, int64(1), *key.GroupID)
		})
	}
}
