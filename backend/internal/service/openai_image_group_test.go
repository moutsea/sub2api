package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type openAIImageGroupRepoStub struct {
	GroupRepository
	groups []Group
	err    error
	calls  int
}

func (repo *openAIImageGroupRepoStub) ListActiveByPlatform(_ context.Context, platform string) ([]Group, error) {
	if platform != PlatformOpenAI {
		return nil, errors.New("unexpected platform")
	}
	repo.calls++
	return repo.groups, repo.err
}

func TestResolveOpenAIRequestGroupID(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		platform  string
		groupName string
		endpoint  string
		model     string
		body      string
		wantImage bool
	}{
		{name: "image endpoint", platform: PlatformOpenAI, endpoint: "/v1/images/generations", wantImage: true},
		{name: "image endpoint alias", platform: PlatformOpenAI, endpoint: "/images/generations", wantImage: true},
		{name: "multipart edit", platform: PlatformOpenAI, endpoint: "/v1/images/edits", body: "multipart body", wantImage: true},
		{name: "image model", platform: PlatformOpenAI, endpoint: "/v1/responses", model: "gpt-image-2", wantImage: true},
		{name: "responses image tool", platform: PlatformOpenAI, endpoint: "/v1/responses", model: "gpt-5.5", body: `{"tools":[{"type":"image_generation"}]}`, wantImage: true},
		{name: "responses image tool disabled", platform: PlatformOpenAI, endpoint: "/v1/responses", model: "gpt-5.5", body: `{"tools":[{"type":"image_generation"}],"tool_choice":"none"}`},
		{name: "responses other tool selected", platform: PlatformOpenAI, endpoint: "/v1/responses", model: "gpt-5.5", body: `{"tools":[{"type":"image_generation"},{"type":"function","name":"search"}],"tool_choice":{"type":"function","name":"search"}}`},
		{name: "image tool choice", platform: PlatformOpenAI, endpoint: "/responses", model: "gpt-5.5", body: `{"tool_choice":{"type":"image_generation"}}`, wantImage: true},
		{name: "ordinary response", platform: PlatformOpenAI, endpoint: "/v1/responses", model: "gpt-5.5", body: `{"input":"hello"}`},
		{name: "image input only", platform: PlatformOpenAI, endpoint: "/v1/responses", model: "gpt-5.5", body: `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.com/cat.png"}]}]}`},
		{name: "passive codex image namespace", platform: PlatformOpenAI, endpoint: "/v1/responses", model: "gpt-5.5", body: `{"tools":[{"type":"namespace","name":"image_gen"}]}`},
		{name: "already image group", platform: PlatformOpenAI, groupName: "gpt-image", endpoint: "/v1/images/generations"},
		{name: "grok images", platform: PlatformGrok, endpoint: "/v1/images/generations"},
		{name: "kiro", platform: PlatformKiro, model: "gpt-image-2"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			group := &Group{ID: 1, Name: testCase.groupName, Platform: testCase.platform}
			key := &APIKey{GroupID: &group.ID, Group: group}
			repo := &openAIImageGroupRepoStub{groups: []Group{
				{ID: 2, Name: "text", Platform: PlatformOpenAI, Status: StatusActive},
				{ID: 3, Name: "gpt-image", Platform: PlatformOpenAI, Status: StatusActive},
			}}
			svc := &OpenAIGatewayService{groupRepo: repo}

			groupID := svc.ResolveOpenAIRequestGroupID(context.Background(), key, testCase.endpoint, testCase.model, []byte(testCase.body))

			if testCase.wantImage {
				require.Equal(t, int64(3), *groupID)
				require.Equal(t, 1, repo.calls)
			} else {
				require.Same(t, key.GroupID, groupID)
				require.Zero(t, repo.calls)
			}
			require.Same(t, group, key.Group)
			require.Equal(t, int64(1), *key.GroupID)
		})
	}
}

func TestResolveOpenAIRequestGroupIDFallback(t *testing.T) {
	group := &Group{ID: 1, Platform: PlatformOpenAI}
	key := &APIKey{GroupID: &group.ID, Group: group}
	for _, testCase := range []struct {
		name   string
		groups []Group
		err    error
	}{
		{name: "missing group"},
		{name: "lookup failure", err: errors.New("database unavailable")},
		{name: "inactive group", groups: []Group{{ID: 2, Name: "gpt-image", Platform: PlatformOpenAI, Status: "inactive"}}},
		{name: "different platform", groups: []Group{{ID: 2, Name: "gpt-image", Platform: PlatformGrok, Status: StatusActive}}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			svc := &OpenAIGatewayService{groupRepo: &openAIImageGroupRepoStub{groups: testCase.groups, err: testCase.err}}
			require.Same(t, key.GroupID, svc.ResolveOpenAIRequestGroupID(context.Background(), key, "/v1/images/generations", "", nil))
		})
	}
	svc := &OpenAIGatewayService{}
	require.Same(t, key.GroupID, svc.ResolveOpenAIRequestGroupID(context.Background(), key, "/v1/images/generations", "", nil))
	require.Nil(t, svc.ResolveOpenAIRequestGroupID(context.Background(), nil, "/v1/images/generations", "", nil))
	require.Nil(t, svc.ResolveOpenAIRequestGroupID(context.Background(), &APIKey{}, "/v1/images/generations", "", nil))
}

func TestResolveOpenAIRequestGroupIDSharedPool(t *testing.T) {
	for _, subscriptionType := range []string{SubscriptionTypeStandard, SubscriptionTypeSubscription} {
		t.Run(subscriptionType, func(t *testing.T) {
			original := &Group{ID: 1, Platform: PlatformOpenAI}
			key := &APIKey{GroupID: &original.ID, Group: original, User: &User{ID: 1}}
			repo := &openAIImageGroupRepoStub{groups: []Group{{
				ID:               2,
				Name:             "gpt-image",
				Platform:         PlatformOpenAI,
				Status:           StatusActive,
				IsExclusive:      true,
				SubscriptionType: subscriptionType,
			}}}
			svc := &OpenAIGatewayService{groupRepo: repo}

			groupID := svc.ResolveOpenAIRequestGroupID(context.Background(), key, "/v1/images/generations", "gpt-image-2", nil)

			require.Equal(t, int64(2), *groupID)
			require.Same(t, original, key.Group)
			require.Equal(t, int64(1), *key.GroupID)
		})
	}
}
