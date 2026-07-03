package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type createAccountRepoStub struct {
	created      []*Account
	boundGroups  map[int64][]int64
	duplicate    *Account
	duplicateErr error
	createErr    error
	getByIDFunc  func(context.Context, int64) (*Account, error)
	updateFunc   func(context.Context, *Account) error
	bindErr      error
}

func (s *createAccountRepoStub) Create(_ context.Context, account *Account) error {
	if s.createErr != nil {
		return s.createErr
	}
	if account.ID == 0 {
		account.ID = int64(len(s.created) + 1)
	}
	s.created = append(s.created, account)
	return nil
}

func (s *createAccountRepoStub) GetByID(ctx context.Context, id int64) (*Account, error) {
	if s.getByIDFunc != nil {
		return s.getByIDFunc(ctx, id)
	}
	panic("unexpected GetByID call")
}

func (s *createAccountRepoStub) GetByIDs(context.Context, []int64) ([]*Account, error) {
	panic("unexpected GetByIDs call")
}

func (s *createAccountRepoStub) ExistsByID(context.Context, int64) (bool, error) {
	panic("unexpected ExistsByID call")
}

func (s *createAccountRepoStub) GetByCRSAccountID(context.Context, string) (*Account, error) {
	panic("unexpected GetByCRSAccountID call")
}

func (s *createAccountRepoStub) FindByKiroRefreshToken(context.Context, string) (*Account, error) {
	return s.duplicate, s.duplicateErr
}

func (s *createAccountRepoStub) Update(ctx context.Context, account *Account) error {
	if s.updateFunc != nil {
		return s.updateFunc(ctx, account)
	}
	panic("unexpected Update call")
}

func (s *createAccountRepoStub) Delete(context.Context, int64) error {
	panic("unexpected Delete call")
}

func (s *createAccountRepoStub) List(context.Context, pagination.PaginationParams) ([]Account, *pagination.PaginationResult, error) {
	panic("unexpected List call")
}

func (s *createAccountRepoStub) ListWithFilters(context.Context, pagination.PaginationParams, string, string, string, string) ([]Account, *pagination.PaginationResult, error) {
	panic("unexpected ListWithFilters call")
}

func (s *createAccountRepoStub) ListByGroup(context.Context, int64) ([]Account, error) {
	panic("unexpected ListByGroup call")
}

func (s *createAccountRepoStub) ListActive(context.Context) ([]Account, error) {
	panic("unexpected ListActive call")
}

func (s *createAccountRepoStub) ListByPlatform(context.Context, string) ([]Account, error) {
	panic("unexpected ListByPlatform call")
}

func (s *createAccountRepoStub) ListErrorByPlatform(context.Context, string) ([]Account, error) {
	panic("unexpected ListErrorByPlatform call")
}

func (s *createAccountRepoStub) ListDeletedByPlatform(context.Context, string) ([]Account, error) {
	panic("unexpected ListDeletedByPlatform call")
}

func (s *createAccountRepoStub) RestoreAccount(context.Context, int64) error {
	panic("unexpected RestoreAccount call")
}

func (s *createAccountRepoStub) UpdateLastUsed(context.Context, int64) error {
	panic("unexpected UpdateLastUsed call")
}

func (s *createAccountRepoStub) BatchUpdateLastUsed(context.Context, map[int64]time.Time) error {
	panic("unexpected BatchUpdateLastUsed call")
}

func (s *createAccountRepoStub) SetError(context.Context, int64, string) error {
	panic("unexpected SetError call")
}

func (s *createAccountRepoStub) SetSchedulable(context.Context, int64, bool) error {
	panic("unexpected SetSchedulable call")
}

func (s *createAccountRepoStub) AutoPauseExpiredAccounts(context.Context, time.Time) (int64, error) {
	panic("unexpected AutoPauseExpiredAccounts call")
}

func (s *createAccountRepoStub) BindGroups(_ context.Context, accountID int64, groupIDs []int64) error {
	if s.bindErr != nil {
		return s.bindErr
	}
	if s.boundGroups == nil {
		s.boundGroups = map[int64][]int64{}
	}
	s.boundGroups[accountID] = append([]int64(nil), groupIDs...)
	return nil
}

func (s *createAccountRepoStub) ListSchedulable(context.Context) ([]Account, error) {
	panic("unexpected ListSchedulable call")
}

func (s *createAccountRepoStub) ListSchedulableByGroupID(context.Context, int64) ([]Account, error) {
	panic("unexpected ListSchedulableByGroupID call")
}

func (s *createAccountRepoStub) ListSchedulableByPlatform(context.Context, string) ([]Account, error) {
	panic("unexpected ListSchedulableByPlatform call")
}

func (s *createAccountRepoStub) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]Account, error) {
	panic("unexpected ListSchedulableByGroupIDAndPlatform call")
}

func (s *createAccountRepoStub) ListSchedulableByPlatforms(context.Context, []string) ([]Account, error) {
	panic("unexpected ListSchedulableByPlatforms call")
}

func (s *createAccountRepoStub) ListSchedulableByGroupIDAndPlatforms(context.Context, int64, []string) ([]Account, error) {
	panic("unexpected ListSchedulableByGroupIDAndPlatforms call")
}

func (s *createAccountRepoStub) SetRateLimited(context.Context, int64, time.Time) error {
	panic("unexpected SetRateLimited call")
}

func (s *createAccountRepoStub) SetAntigravityQuotaScopeLimit(context.Context, int64, AntigravityQuotaScope, time.Time) error {
	panic("unexpected SetAntigravityQuotaScopeLimit call")
}

func (s *createAccountRepoStub) SetModelRateLimit(context.Context, int64, string, time.Time) error {
	panic("unexpected SetModelRateLimit call")
}

func (s *createAccountRepoStub) SetOverloaded(context.Context, int64, time.Time) error {
	panic("unexpected SetOverloaded call")
}

func (s *createAccountRepoStub) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	panic("unexpected SetTempUnschedulable call")
}

func (s *createAccountRepoStub) ClearTempUnschedulable(context.Context, int64) error {
	panic("unexpected ClearTempUnschedulable call")
}

func (s *createAccountRepoStub) ClearRateLimit(context.Context, int64) error {
	panic("unexpected ClearRateLimit call")
}

func (s *createAccountRepoStub) ClearAntigravityQuotaScopes(context.Context, int64) error {
	panic("unexpected ClearAntigravityQuotaScopes call")
}

func (s *createAccountRepoStub) ClearModelRateLimits(context.Context, int64) error {
	panic("unexpected ClearModelRateLimits call")
}

func (s *createAccountRepoStub) UpdateSessionWindow(context.Context, int64, *time.Time, *time.Time, string) error {
	panic("unexpected UpdateSessionWindow call")
}

func (s *createAccountRepoStub) UpdateExtra(context.Context, int64, map[string]any) error {
	panic("unexpected UpdateExtra call")
}

func (s *createAccountRepoStub) BulkUpdate(context.Context, []int64, AccountBulkUpdate) (int64, error) {
	panic("unexpected BulkUpdate call")
}

func TestCreateAccountKiroIdCProxyIDWithNilProxyRepoDoesNotPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	proxyID := int64(42)
	repo := &createAccountRepoStub{}
	svc := &adminServiceImpl{accountRepo: repo}

	account, err := svc.CreateAccount(ctx, &CreateAccountInput{
		Name:                  "kiro-idc",
		Platform:              PlatformKiro,
		Type:                  "oauth",
		ProxyID:               &proxyID,
		GroupIDs:              []int64{100},
		SkipMixedChannelCheck: true,
		Credentials: map[string]any{
			"auth_type":     KiroAuthMethodIdC,
			"client_id":     "client-id",
			"client_secret": "client-secret",
			"refresh_token": "refresh-token",
		},
	})

	require.NoError(t, err)
	require.Equal(t, kiro.BuilderIdProfileArn, account.Credentials["profile_arn"])
	require.Len(t, repo.created, 1)
	require.Equal(t, []int64{100}, repo.boundGroups[account.ID])
}

func TestUpdateAccountClearsExtraWhenExplicitlyProvided(t *testing.T) {
	existing := &Account{
		ID:          1,
		Name:        "openai",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{},
		Extra:       map[string]any{"codex_image_generation_bridge": true},
	}
	var updated *Account
	repo := &createAccountRepoStub{
		getByIDFunc: func(context.Context, int64) (*Account, error) {
			return existing, nil
		},
		updateFunc: func(_ context.Context, account *Account) error {
			updated = account
			return nil
		},
	}
	svc := &adminServiceImpl{accountRepo: repo}

	account, err := svc.UpdateAccount(context.Background(), existing.ID, &UpdateAccountInput{
		Extra:         map[string]any{},
		ExtraProvided: true,
	})

	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Empty(t, updated.Extra)
	require.Empty(t, account.Extra)
}

func TestUpdateAccountPreservesExtraWhenNotProvided(t *testing.T) {
	existing := &Account{
		ID:          1,
		Name:        "openai",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{},
		Extra:       map[string]any{"codex_image_generation_bridge": true},
	}
	var updated *Account
	repo := &createAccountRepoStub{
		getByIDFunc: func(context.Context, int64) (*Account, error) {
			return existing, nil
		},
		updateFunc: func(_ context.Context, account *Account) error {
			updated = account
			return nil
		},
	}
	svc := &adminServiceImpl{accountRepo: repo}

	account, err := svc.UpdateAccount(context.Background(), existing.ID, &UpdateAccountInput{})

	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, true, updated.Extra["codex_image_generation_bridge"])
	require.Equal(t, true, account.Extra["codex_image_generation_bridge"])
}

func TestCreateAccountKiroIdCPreservesValidProfileArnWhenFetchUnavailable(t *testing.T) {
	withKiroProfileFetchStubs(t, func(context.Context, string, string, string, string, string) (*kiro.IdCAccessTokenInfo, error) {
		return nil, errors.New("network unavailable")
	}, nil)

	repo := &createAccountRepoStub{}
	svc := &adminServiceImpl{accountRepo: repo}
	wantProfileArn := "arn:aws:codewhisperer:us-east-1:123456789012:profile/USERPROFILE"

	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:                  "kiro-idc",
		Platform:              PlatformKiro,
		Type:                  "oauth",
		GroupIDs:              []int64{100},
		SkipMixedChannelCheck: true,
		Credentials: map[string]any{
			"auth_type":  KiroAuthMethodIdC,
			"profileArn": wantProfileArn,
		},
	})

	require.NoError(t, err)
	require.Equal(t, wantProfileArn, account.GetKiroProfileArn())
	require.NotContains(t, account.Credentials, "profile_arn")
	require.Len(t, repo.created, 1)
}

func TestCreateAccountKiroIdCSavesRotatedRefreshTokenFromProfileFetch(t *testing.T) {
	wantProfileArn := "arn:aws:codewhisperer:us-east-1:123456789012:profile/FETCHED"
	withKiroProfileFetchStubs(t,
		func(_ context.Context, clientID, clientSecret, refreshToken, region, proxyURL string) (*kiro.IdCAccessTokenInfo, error) {
			require.Equal(t, "client-id", clientID)
			require.Equal(t, "client-secret", clientSecret)
			require.Equal(t, "old-refresh-token", refreshToken)
			require.Equal(t, kiro.DefaultRegion, region)
			require.Empty(t, proxyURL)
			return &kiro.IdCAccessTokenInfo{
				AccessToken:  "access-token",
				RefreshToken: "new-refresh-token",
			}, nil
		},
		func(_ context.Context, accessToken, region, proxyURL string, family kiro.ServiceEndpointFamily) (string, error) {
			require.Equal(t, "access-token", accessToken)
			require.Equal(t, kiro.DefaultRegion, region)
			require.Empty(t, proxyURL)
			require.Empty(t, family)
			return wantProfileArn, nil
		},
	)

	repo := &createAccountRepoStub{}
	svc := &adminServiceImpl{accountRepo: repo}

	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:                  "kiro-idc",
		Platform:              PlatformKiro,
		Type:                  "oauth",
		GroupIDs:              []int64{100},
		SkipMixedChannelCheck: true,
		Credentials: map[string]any{
			"auth_type":     KiroAuthMethodIdC,
			"client_id":     "client-id",
			"client_secret": "client-secret",
			"refresh_token": "old-refresh-token",
		},
	})

	require.NoError(t, err)
	require.Equal(t, wantProfileArn, account.GetKiroProfileArn())
	require.Equal(t, "new-refresh-token", account.Credentials["refresh_token"])
	require.Len(t, repo.created, 1)
}

func TestCreateAccountKiroIdCFillsBuilderProfileArnWhenFetchUnavailable(t *testing.T) {
	repo := &createAccountRepoStub{}
	svc := &adminServiceImpl{accountRepo: repo}

	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:                  "kiro-idc",
		Platform:              PlatformKiro,
		Type:                  "oauth",
		GroupIDs:              []int64{100},
		SkipMixedChannelCheck: true,
		Credentials: map[string]any{
			"auth_type": KiroAuthMethodIdC,
		},
	})

	require.NoError(t, err)
	require.Equal(t, kiro.BuilderIdProfileArn, account.GetKiroProfileArn())
	require.Len(t, repo.created, 1)
}

func withKiroProfileFetchStubs(
	t *testing.T,
	refresh func(context.Context, string, string, string, string, string) (*kiro.IdCAccessTokenInfo, error),
	fetch func(context.Context, string, string, string, kiro.ServiceEndpointFamily) (string, error),
) {
	t.Helper()
	originalRefresh := refreshKiroIdCAccessToken
	originalFetch := fetchKiroAvailableProfileArn
	if refresh != nil {
		refreshKiroIdCAccessToken = refresh
	}
	if fetch != nil {
		fetchKiroAvailableProfileArn = fetch
	}
	t.Cleanup(func() {
		refreshKiroIdCAccessToken = originalRefresh
		fetchKiroAvailableProfileArn = originalFetch
	})
}
