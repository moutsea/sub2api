//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestTempAPIKeyRepo_Update_QuotaOnlyPersistsTotalQuotaUSD(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewTempAPIKeyRepo(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("temp-key-update-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "test-password-hash",
		Role:         service.RoleAdmin,
		Status:       service.StatusActive,
		Concurrency:  3,
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:   fmt.Sprintf("temp-key-update-group-%d", time.Now().UnixNano()),
		Status: service.StatusActive,
	})

	created := &service.TempAPIKey{
		Key:           fmt.Sprintf("sk-temp-it-quota-update-%d", time.Now().UnixNano()),
		Name:          "quota-update-test",
		GroupID:       group.ID,
		KeyType:       service.TempAPIKeyTypeQuotaOnly,
		TotalQuotaUSD: 10,
		Status:        service.TempAPIKeyStatusActive,
		CreatedBy:     user.ID,
		ValidDays:     7,
		DailyLimit:    100,
	}
	require.NoError(t, repo.Create(ctx, created))

	created.TotalQuotaUSD = 25
	require.NoError(t, repo.Update(ctx, created))

	got, err := repo.GetByID(ctx, created.ID)
	require.NoError(t, err)
	require.InEpsilon(t, 25.0, got.TotalQuotaUSD, 0.000001)
}

func TestTempAPIKeyRepo_Update_PersistsGroupID(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewTempAPIKeyRepo(client, integrationDB)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("temp-key-update-group-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "test-password-hash",
		Role:         service.RoleAdmin,
		Status:       service.StatusActive,
		Concurrency:  3,
	})
	groupA := mustCreateGroup(t, client, &service.Group{
		Name:   fmt.Sprintf("temp-key-group-a-%d", time.Now().UnixNano()),
		Status: service.StatusActive,
	})
	groupB := mustCreateGroup(t, client, &service.Group{
		Name:   fmt.Sprintf("temp-key-group-b-%d", time.Now().UnixNano()),
		Status: service.StatusActive,
	})

	created := &service.TempAPIKey{
		Key:        fmt.Sprintf("sk-temp-it-group-update-%d", time.Now().UnixNano()),
		Name:       "group-update-test",
		GroupID:    groupA.ID,
		KeyType:    service.TempAPIKeyTypeLimited,
		Status:     service.TempAPIKeyStatusActive,
		CreatedBy:  user.ID,
		ValidDays:  7,
		DailyLimit: 100,
	}
	require.NoError(t, repo.Create(ctx, created))

	created.GroupID = groupB.ID
	require.NoError(t, repo.Update(ctx, created))

	got, err := repo.GetByID(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, groupB.ID, got.GroupID)
}
