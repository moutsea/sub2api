package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/tempapikey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// TempAPIKeyRepo implements the temp API key repository
type TempAPIKeyRepo struct {
	client *dbent.Client
}

// NewTempAPIKeyRepo creates a new TempAPIKeyRepo
func NewTempAPIKeyRepo(client *dbent.Client) *TempAPIKeyRepo {
	return &TempAPIKeyRepo{client: client}
}

// activeQuery returns a query with soft delete filter
func (r *TempAPIKeyRepo) activeQuery() *dbent.TempAPIKeyQuery {
	return r.client.TempAPIKey.Query().Where(tempapikey.DeletedAtIsNil())
}

// Create creates a new temp API key
func (r *TempAPIKeyRepo) Create(ctx context.Context, key *service.TempAPIKey) error {
	creator := r.client.TempAPIKey.Create().
		SetKey(key.Key).
		SetName(key.Name).
		SetGroupID(key.GroupID).
		SetValidDays(key.ValidDays).
		SetDailyLimit(key.DailyLimit).
		SetStatus(key.Status).
		SetCreatedBy(key.CreatedBy)

	if key.KeyType != "" {
		creator.SetKeyType(key.KeyType)
	}
	if key.TotalQuotaUSD > 0 {
		creator.SetTotalQuotaUsd(key.TotalQuotaUSD)
	}

	created, err := creator.Save(ctx)
	if err != nil {
		return err
	}
	key.ID = created.ID
	key.CreatedAt = created.CreatedAt
	key.UpdatedAt = created.UpdatedAt
	return nil
}

// GetByID retrieves a temp API key by ID
func (r *TempAPIKeyRepo) GetByID(ctx context.Context, id int64) (*service.TempAPIKey, error) {
	row, err := r.activeQuery().
		Where(tempapikey.ID(id)).
		WithGroup().
		WithCreator().
		Only(ctx)
	if err != nil {
		return nil, err
	}
	return r.toServiceModel(row), nil
}

// GetByKey retrieves a temp API key by key string
func (r *TempAPIKeyRepo) GetByKey(ctx context.Context, key string) (*service.TempAPIKey, error) {
	row, err := r.activeQuery().
		Where(tempapikey.Key(key)).
		WithGroup().
		WithCreator().
		Only(ctx)
	if err != nil {
		return nil, err
	}
	return r.toServiceModel(row), nil
}

// GetByKeyForAuth retrieves a temp API key for authentication (minimal fields)
func (r *TempAPIKeyRepo) GetByKeyForAuth(ctx context.Context, key string) (*service.TempAPIKey, error) {
	row, err := r.activeQuery().
		Where(tempapikey.Key(key)).
		WithGroup().
		Only(ctx)
	if err != nil {
		return nil, err
	}
	return r.toServiceModel(row), nil
}

// Update updates a temp API key
func (r *TempAPIKeyRepo) Update(ctx context.Context, key *service.TempAPIKey) error {
	updater := r.client.TempAPIKey.UpdateOneID(key.ID).
		SetName(key.Name).
		SetStatus(key.Status).
		SetValidDays(key.ValidDays).
		SetDailyLimit(key.DailyLimit)

	if key.ActivatedAt != nil {
		updater.SetActivatedAt(*key.ActivatedAt)
	}
	if key.ExpiresAt != nil {
		updater.SetExpiresAt(*key.ExpiresAt)
	}
	if key.CurrentPeriodStart != nil {
		updater.SetCurrentPeriodStart(*key.CurrentPeriodStart)
	}
	updater.SetCurrentPeriodCount(key.CurrentPeriodCount)
	updater.SetTotalRequests(key.TotalRequests)

	_, err := updater.Save(ctx)
	return err
}

// Delete soft deletes a temp API key
func (r *TempAPIKeyRepo) Delete(ctx context.Context, id int64) error {
	now := time.Now()
	_, err := r.client.TempAPIKey.UpdateOneID(id).
		SetDeletedAt(now).
		Save(ctx)
	return err
}

// BatchDelete soft deletes multiple temp API keys
func (r *TempAPIKeyRepo) BatchDelete(ctx context.Context, ids []int64) (int, error) {
	now := time.Now()
	return r.client.TempAPIKey.Update().
		Where(tempapikey.IDIn(ids...), tempapikey.DeletedAtIsNil()).
		SetDeletedAt(now).
		Save(ctx)
}

// List lists temp API keys with pagination and optional key_type filter
func (r *TempAPIKeyRepo) List(ctx context.Context, params pagination.PaginationParams, keyType string) ([]*service.TempAPIKey, *pagination.PaginationResult, error) {
	query := r.activeQuery()

	if keyType != "" {
		query = query.Where(tempapikey.KeyType(keyType))
	}

	// Count total
	total, err := query.Clone().Count(ctx)
	if err != nil {
		return nil, nil, err
	}

	// Apply pagination
	page := params.Page
	pageSize := params.PageSize
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	rows, err := query.
		WithGroup().
		WithCreator().
		Order(dbent.Desc(tempapikey.FieldCreatedAt)).
		Offset(offset).
		Limit(pageSize).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}

	keys := make([]*service.TempAPIKey, len(rows))
	for i, row := range rows {
		keys[i] = r.toServiceModel(row)
	}

	paginationResult := &pagination.PaginationResult{
		Total:    int64(total),
		Page:     page,
		PageSize: pageSize,
	}

	return keys, paginationResult, nil
}

// ListByGroupID lists temp API keys by group ID
func (r *TempAPIKeyRepo) ListByGroupID(ctx context.Context, groupID int64, params pagination.PaginationParams) ([]*service.TempAPIKey, *pagination.PaginationResult, error) {
	query := r.activeQuery().Where(tempapikey.GroupID(groupID))

	total, err := query.Clone().Count(ctx)
	if err != nil {
		return nil, nil, err
	}

	page := params.Page
	pageSize := params.PageSize
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	rows, err := query.
		WithGroup().
		WithCreator().
		Order(dbent.Desc(tempapikey.FieldCreatedAt)).
		Offset(offset).
		Limit(pageSize).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}

	keys := make([]*service.TempAPIKey, len(rows))
	for i, row := range rows {
		keys[i] = r.toServiceModel(row)
	}

	return keys, &pagination.PaginationResult{
		Total:    int64(total),
		Page:     page,
		PageSize: pageSize,
	}, nil
}

// ActivateAndIncrement activates (if needed) and increments usage count
// Returns updated key and whether rate limit is exceeded
// 注意：此方法使用乐观锁策略，在高并发下可能有轻微的计数误差，但不会影响限流的有效性
// 对于 quota_only 类型，此方法只检查是否已耗尽额度，实际消费金额在 RecordUsage 中更新
func (r *TempAPIKeyRepo) ActivateAndIncrement(ctx context.Context, id int64) (*service.TempAPIKey, bool, error) {
	// Get current state with edges
	row, err := r.client.TempAPIKey.Query().
		Where(tempapikey.ID(id)).
		WithGroup().
		WithCreator().
		Only(ctx)
	if err != nil {
		return nil, false, err
	}

	now := time.Now()

	// quota_only 类型：基于美元消费金额检查
	if row.KeyType == service.TempAPIKeyTypeQuotaOnly {
		// 检查是否已用完额度（基于美元消费金额）
		if row.TotalQuotaUsd > 0 && row.TotalCostUsd >= row.TotalQuotaUsd {
			return r.toServiceModel(row), true, nil // Quota exhausted
		}

		// 更新激活时间和请求次数（消费金额在 RecordUsage 中更新）
		updateBuilder := r.client.TempAPIKey.Update().
			Where(tempapikey.ID(id))

		// 首次使用时记录激活时间
		if row.ActivatedAt == nil {
			updateBuilder.SetActivatedAt(now)
		}

		// 递增总请求数（用于统计，不用于限额检查）
		updateBuilder.AddTotalRequests(1)

		_, err := updateBuilder.Save(ctx)
		if err != nil {
			return nil, false, err
		}

		// 查询更新后的记录
		updated, err := r.client.TempAPIKey.Query().
			Where(tempapikey.ID(id)).
			WithGroup().
			WithCreator().
			Only(ctx)
		if err != nil {
			return nil, false, err
		}
		return r.toServiceModel(updated), false, nil
	}

	// time_limited 类型：原有逻辑
	updater := r.client.TempAPIKey.UpdateOneID(id)
	{
		// time_limited 类型：原有逻辑
		// Check rate limit first (before any update)
		// 如果已激活且在当前周期内，检查是否超限
		if row.ActivatedAt != nil && row.CurrentPeriodStart != nil {
			periodExpired := now.Sub(*row.CurrentPeriodStart) >= 24*time.Hour
			if !periodExpired && row.CurrentPeriodCount >= row.DailyLimit {
				return r.toServiceModel(row), true, nil // Rate limited
			}
		}

		// Activate if not yet activated
		if row.ActivatedAt == nil {
			expiresAt := now.Add(time.Duration(row.ValidDays) * 24 * time.Hour)
			updater.SetActivatedAt(now)
			updater.SetExpiresAt(expiresAt)
			updater.SetCurrentPeriodStart(now)
			updater.SetCurrentPeriodCount(1)
		} else {
			// Check if current period has expired (24 hours)
			if row.CurrentPeriodStart != nil && now.Sub(*row.CurrentPeriodStart) >= 24*time.Hour {
				// Reset period
				updater.SetCurrentPeriodStart(now)
				updater.SetCurrentPeriodCount(1)
			} else {
				// Increment count - 使用 AddCurrentPeriodCount 进行原子增加
				updater.AddCurrentPeriodCount(1)
			}
		}

		// Increment total requests - 使用 AddTotalRequests 进行原子增加
		updater.AddTotalRequests(1)
	}

	_, err = updater.Save(ctx)
	if err != nil {
		return nil, false, err
	}

	// Re-query with edges to get the updated record with Group and Creator
	updated, err := r.client.TempAPIKey.Query().
		Where(tempapikey.ID(id)).
		WithGroup().
		WithCreator().
		Only(ctx)
	if err != nil {
		return nil, false, err
	}

	return r.toServiceModel(updated), false, nil
}

// BatchUpdateStatus updates status for multiple keys
func (r *TempAPIKeyRepo) BatchUpdateStatus(ctx context.Context, ids []int64, status string) (int, error) {
	return r.client.TempAPIKey.Update().
		Where(tempapikey.IDIn(ids...), tempapikey.DeletedAtIsNil()).
		SetStatus(status).
		Save(ctx)
}

// BatchUpdateValidDays updates valid days for multiple keys
func (r *TempAPIKeyRepo) BatchUpdateValidDays(ctx context.Context, ids []int64, validDays int) (int, error) {
	return r.client.TempAPIKey.Update().
		Where(tempapikey.IDIn(ids...), tempapikey.DeletedAtIsNil()).
		SetValidDays(validDays).
		Save(ctx)
}

// BatchUpdateDailyLimit updates daily limit for multiple keys
func (r *TempAPIKeyRepo) BatchUpdateDailyLimit(ctx context.Context, ids []int64, dailyLimit int) (int, error) {
	return r.client.TempAPIKey.Update().
		Where(tempapikey.IDIn(ids...), tempapikey.DeletedAtIsNil()).
		SetDailyLimit(dailyLimit).
		Save(ctx)
}

// BatchUpdateNamePrefix updates name prefix for multiple keys.
// Preserves the suffix after the last "-" in each key's name (e.g. "old-prefix-3" → "new-prefix-3").
// If a name has no "-", it is replaced entirely with "newPrefix-index".
func (r *TempAPIKeyRepo) BatchUpdateNamePrefix(ctx context.Context, ids []int64, newPrefix string) (int, error) {
	rows, err := r.client.TempAPIKey.Query().
		Where(tempapikey.IDIn(ids...), tempapikey.DeletedAtIsNil()).
		Select(tempapikey.FieldID, tempapikey.FieldName).
		All(ctx)
	if err != nil {
		return 0, err
	}

	updated := 0
	for i, row := range rows {
		suffix := fmt.Sprintf("%d", i+1)
		if idx := strings.LastIndex(row.Name, "-"); idx >= 0 {
			suffix = row.Name[idx+1:]
		}
		newName := newPrefix + "-" + suffix
		_, err := r.client.TempAPIKey.UpdateOneID(row.ID).SetName(newName).Save(ctx)
		if err != nil {
			return updated, err
		}
		updated++
	}
	return updated, nil
}

// ExistsByKey checks if a key exists
func (r *TempAPIKeyRepo) ExistsByKey(ctx context.Context, key string) (bool, error) {
	return r.activeQuery().Where(tempapikey.Key(key)).Exist(ctx)
}

// CountByGroupID counts keys by group ID
func (r *TempAPIKeyRepo) CountByGroupID(ctx context.Context, groupID int64) (int, error) {
	return r.activeQuery().Where(tempapikey.GroupID(groupID)).Count(ctx)
}

// AddCostUSD adds cost to a quota_only temp API key and checks if exhausted
// Returns the updated key and whether the quota is now exhausted
func (r *TempAPIKeyRepo) AddCostUSD(ctx context.Context, id int64, costUSD float64) (*service.TempAPIKey, bool, error) {
	// 使用原子操作增加消费金额
	_, err := r.client.TempAPIKey.Update().
		Where(tempapikey.ID(id)).
		AddTotalCostUsd(costUSD).
		Save(ctx)
	if err != nil {
		return nil, false, err
	}

	// 查询更新后的记录
	row, err := r.client.TempAPIKey.Query().
		Where(tempapikey.ID(id)).
		WithGroup().
		WithCreator().
		Only(ctx)
	if err != nil {
		return nil, false, err
	}

	key := r.toServiceModel(row)

	// 检查是否已耗尽额度，如果是则更新状态
	exhausted := false
	if row.TotalQuotaUsd > 0 && row.TotalCostUsd >= row.TotalQuotaUsd {
		exhausted = true
		_, err = r.client.TempAPIKey.UpdateOneID(id).
			SetStatus(service.TempAPIKeyStatusExhausted).
			Save(ctx)
		if err != nil {
			return key, exhausted, err
		}
		key.Status = service.TempAPIKeyStatusExhausted
	}

	return key, exhausted, nil
}

// toServiceModel converts ent model to service model
func (r *TempAPIKeyRepo) toServiceModel(row *dbent.TempAPIKey) *service.TempAPIKey {
	if row == nil {
		return nil
	}

	key := &service.TempAPIKey{
		ID:                 row.ID,
		Key:                row.Key,
		Name:               row.Name,
		GroupID:            row.GroupID,
		KeyType:            row.KeyType,
		TotalQuotaUSD:      row.TotalQuotaUsd,
		TotalCostUSD:       row.TotalCostUsd,
		ValidDays:          row.ValidDays,
		DailyLimit:         row.DailyLimit,
		CurrentPeriodCount: row.CurrentPeriodCount,
		TotalRequests:      row.TotalRequests,
		Status:             row.Status,
		CreatedBy:          row.CreatedBy,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}

	// Handle nullable time fields
	if row.ActivatedAt != nil {
		key.ActivatedAt = row.ActivatedAt
	}
	if row.ExpiresAt != nil {
		key.ExpiresAt = row.ExpiresAt
	}
	if row.CurrentPeriodStart != nil {
		key.CurrentPeriodStart = row.CurrentPeriodStart
	}

	// Load edges
	if row.Edges.Group != nil {
		key.Group = groupEntityToService(row.Edges.Group)
	}
	if row.Edges.Creator != nil {
		key.Creator = userEntityToService(row.Edges.Creator)
	}

	return key
}
