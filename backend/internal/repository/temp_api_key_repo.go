package repository

import (
	"context"
	"database/sql"
	"fmt"
	"log"
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
	sqlDB  *sql.DB
}

// NewTempAPIKeyRepo creates a new TempAPIKeyRepo
func NewTempAPIKeyRepo(client *dbent.Client, sqlDB *sql.DB) *TempAPIKeyRepo {
	return &TempAPIKeyRepo{client: client, sqlDB: sqlDB}
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
	if key.DailyQuotaUSD > 0 {
		creator.SetDailyQuotaUsd(key.DailyQuotaUSD)
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

	// quota_only 类型允许更新总额度
	if key.KeyType == service.TempAPIKeyTypeQuotaOnly {
		updater.SetTotalQuotaUsd(key.TotalQuotaUSD)
	}

	// time_quota 类型允许更新每日 USD 额度
	if key.KeyType == service.TempAPIKeyTypeTimeQuota {
		updater.SetDailyQuotaUsd(key.DailyQuotaUSD)
	}

	if key.ActivatedAt != nil {
		updater.SetActivatedAt(*key.ActivatedAt)
	}
	if key.ExpiresAt != nil {
		updater.SetExpiresAt(*key.ExpiresAt)
	}
	// 注意：不更新 CurrentPeriodStart、CurrentPeriodCount、TotalRequests
	// 这些运行时统计字段由 ActivateAndIncrement / IncrementUsageCounters 原子管理
	// 在此处覆盖会与并发请求产生竞态，导致周期重置被撤销

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

// TempAPIKeyListFilters holds optional filters for listing temp API keys
type TempAPIKeyListFilters struct {
	KeyType   string // "time_limited", "quota_only", or "time_quota"
	Status    string // "active", "inactive", "expired", "exhausted"
	GroupID   int64  // filter by group
	Search    string // fuzzy match on name or key
	Activated string // "yes" = activated, "no" = not activated
}

// List lists temp API keys with pagination and optional filters
func (r *TempAPIKeyRepo) List(ctx context.Context, params pagination.PaginationParams, filters TempAPIKeyListFilters) ([]*service.TempAPIKey, *pagination.PaginationResult, error) {
	query := r.activeQuery()

	if filters.KeyType != "" {
		query = query.Where(tempapikey.KeyType(filters.KeyType))
	}
	if filters.Status != "" {
		switch filters.Status {
		case "expired":
			// expired = activated + expires_at in the past
			query = query.Where(tempapikey.ActivatedAtNotNil(), tempapikey.ExpiresAtLT(time.Now()))
		case "exhausted":
			query = query.Where(tempapikey.StatusEQ("exhausted"))
		default:
			query = query.Where(tempapikey.StatusEQ(filters.Status))
		}
	}
	if filters.GroupID > 0 {
		query = query.Where(tempapikey.GroupID(filters.GroupID))
	}
	if filters.Search != "" {
		query = query.Where(
			tempapikey.Or(
				tempapikey.NameContains(filters.Search),
				tempapikey.KeyContains(filters.Search),
			),
		)
	}
	if filters.Activated == "yes" {
		query = query.Where(tempapikey.ActivatedAtNotNil())
	} else if filters.Activated == "no" {
		query = query.Where(tempapikey.ActivatedAtIsNil())
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

// ActivateAndIncrement activates (if needed) and validates rate limits.
// For time_limited: increments current_period_count for daily limit checking.
// For quota_only: checks quota exhaustion only.
// total_requests is incremented separately in RecordUsage after successful completion.
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

		// 首次使用时记录激活时间
		if row.ActivatedAt == nil {
			_, err := r.client.TempAPIKey.Update().
				Where(tempapikey.ID(id)).
				SetActivatedAt(now).
				Save(ctx)
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

		return r.toServiceModel(row), false, nil
	}

	// time_quota 类型：限时 + 每日 USD 额度
	if row.KeyType == service.TempAPIKeyTypeTimeQuota {
		updater := r.client.TempAPIKey.UpdateOneID(id)

		if row.ActivatedAt == nil {
			// 首次激活
			expiresAt := now.Add(time.Duration(row.ValidDays) * 24 * time.Hour)
			updater.SetActivatedAt(now)
			updater.SetExpiresAt(expiresAt)
			updater.SetCurrentPeriodStart(now)
			updater.SetCurrentPeriodCostUsd(0)
		} else if row.CurrentPeriodStart != nil && now.Sub(*row.CurrentPeriodStart) >= 24*time.Hour {
			// 周期已过期，重置（以 ActivatedAt 为锚点对齐周期）
			anchor := *row.ActivatedAt
			elapsed := now.Sub(anchor)
			periods := int(elapsed / (24 * time.Hour))
			newPeriodStart := anchor.Add(time.Duration(periods) * 24 * time.Hour)
			updater.SetCurrentPeriodStart(newPeriodStart)
			updater.SetCurrentPeriodCostUsd(0)
		} else if row.DailyQuotaUsd > 0 && row.CurrentPeriodCostUsd >= row.DailyQuotaUsd {
			// 当前周期内已达每日 USD 额度上限
			log.Printf("[TempAPIKey] Daily quota exceeded: id=%d cost=%.4f limit=%.4f periodStart=%v",
				id, row.CurrentPeriodCostUsd, row.DailyQuotaUsd, row.CurrentPeriodStart)
			return r.toServiceModel(row), true, nil
		}

		_, err = updater.Save(ctx)
		if err != nil {
			return nil, false, err
		}

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

	// time_limited 类型
	updater := r.client.TempAPIKey.UpdateOneID(id)

	if row.ActivatedAt == nil {
		// 首次激活（count 在 RecordUsage 成功后递增）
		expiresAt := now.Add(time.Duration(row.ValidDays) * 24 * time.Hour)
		updater.SetActivatedAt(now)
		updater.SetExpiresAt(expiresAt)
		updater.SetCurrentPeriodStart(now)
		updater.SetCurrentPeriodCount(0)
	} else if row.CurrentPeriodStart != nil && now.Sub(*row.CurrentPeriodStart) >= 24*time.Hour {
		// 周期已过期，重置（count 在 RecordUsage 成功后递增）
		// 以 ActivatedAt 为锚点对齐周期，防止漂移
		// 例：10:00 激活，每个周期都是 10:00-10:00，无论用户何时请求
		anchor := *row.ActivatedAt
		elapsed := now.Sub(anchor)
		periods := int(elapsed / (24 * time.Hour))
		newPeriodStart := anchor.Add(time.Duration(periods) * 24 * time.Hour)
		updater.SetCurrentPeriodStart(newPeriodStart)
		updater.SetCurrentPeriodCount(0)
	} else if row.CurrentPeriodCount >= row.DailyLimit {
		// 当前周期内已达上限
		elapsed := time.Duration(0)
		if row.CurrentPeriodStart != nil {
			elapsed = now.Sub(*row.CurrentPeriodStart)
		}
		log.Printf("[TempAPIKey] Rate limited: id=%d count=%d limit=%d periodStart=%v elapsed=%v",
			id, row.CurrentPeriodCount, row.DailyLimit, row.CurrentPeriodStart, elapsed)
		return r.toServiceModel(row), true, nil
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

// BatchUpdateDailyQuota updates daily USD quota for multiple time_quota keys
func (r *TempAPIKeyRepo) BatchUpdateDailyQuota(ctx context.Context, ids []int64, dailyQuota float64) (int, error) {
	return r.client.TempAPIKey.Update().
		Where(tempapikey.IDIn(ids...), tempapikey.DeletedAtIsNil(), tempapikey.KeyType(service.TempAPIKeyTypeTimeQuota)).
		SetDailyQuotaUsd(dailyQuota).
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

// DeleteExpiredBefore soft deletes all temp API keys that expired before the given time.
// Returns the number of keys deleted.
func (r *TempAPIKeyRepo) DeleteExpiredBefore(ctx context.Context, before time.Time) (int, error) {
	now := time.Now()
	return r.client.TempAPIKey.Update().
		Where(
			tempapikey.DeletedAtIsNil(),
			tempapikey.ExpiresAtNotNil(),
			tempapikey.ExpiresAtLT(before),
		).
		SetDeletedAt(now).
		Save(ctx)
}

// CountByGroupID counts keys by group ID
func (r *TempAPIKeyRepo) CountByGroupID(ctx context.Context, groupID int64) (int, error) {
	return r.activeQuery().Where(tempapikey.GroupID(groupID)).Count(ctx)
}

// IncrementUsageCounters atomically increments both current_period_count and total_requests.
// Called after a request completes successfully (in RecordUsage), so upstream failures don't consume daily quota.
func (r *TempAPIKeyRepo) IncrementUsageCounters(ctx context.Context, id int64) error {
	_, err := r.client.TempAPIKey.Update().
		Where(tempapikey.ID(id)).
		AddCurrentPeriodCount(1).
		AddTotalRequests(1).
		Save(ctx)
	return err
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

// AddDailyCostUSD adds cost to a time_quota temp API key's current period and checks if daily quota exceeded
// Returns the updated key and whether the daily quota is now exhausted
func (r *TempAPIKeyRepo) AddDailyCostUSD(ctx context.Context, id int64, costUSD float64) (*service.TempAPIKey, bool, error) {
	// 使用原子操作增加当前周期消费金额
	_, err := r.client.TempAPIKey.Update().
		Where(tempapikey.ID(id)).
		AddCurrentPeriodCostUsd(costUSD).
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

	// 检查是否已达每日额度上限
	exceeded := false
	if row.DailyQuotaUsd > 0 && row.CurrentPeriodCostUsd >= row.DailyQuotaUsd {
		exceeded = true
	}

	return key, exceeded, nil
}

// toServiceModel converts ent model to service model
func (r *TempAPIKeyRepo) toServiceModel(row *dbent.TempAPIKey) *service.TempAPIKey {
	if row == nil {
		return nil
	}

	key := &service.TempAPIKey{
		ID:                   row.ID,
		Key:                  row.Key,
		Name:                 row.Name,
		GroupID:              row.GroupID,
		KeyType:              row.KeyType,
		TotalQuotaUSD:        row.TotalQuotaUsd,
		TotalCostUSD:         row.TotalCostUsd,
		DailyQuotaUSD:        row.DailyQuotaUsd,
		CurrentPeriodCostUSD: row.CurrentPeriodCostUsd,
		ValidDays:            row.ValidDays,
		DailyLimit:           row.DailyLimit,
		CurrentPeriodCount:   row.CurrentPeriodCount,
		TotalRequests:        row.TotalRequests,
		Status:               row.Status,
		CreatedBy:            row.CreatedBy,
		CreatedAt:            row.CreatedAt,
		UpdatedAt:            row.UpdatedAt,
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

// RecalculatePeriodCounts recalculates current_period_count for all active time_limited keys
// by counting actual usage_logs within each key's current 24-hour period window.
// Returns the number of keys updated.
func (r *TempAPIKeyRepo) RecalculatePeriodCounts(ctx context.Context) (int, error) {
	if r.sqlDB == nil {
		return 0, fmt.Errorf("sql.DB not available")
	}

	// Get all active, activated time_limited keys
	keys, err := r.client.TempAPIKey.Query().
		Where(
			tempapikey.DeletedAtIsNil(),
			tempapikey.Or(
				tempapikey.KeyType(service.TempAPIKeyTypeLimited),
				tempapikey.KeyType(service.TempAPIKeyTypeTimeQuota),
			),
			tempapikey.Status(service.TempAPIKeyStatusActive),
			tempapikey.ActivatedAtNotNil(),
		).
		All(ctx)
	if err != nil {
		return 0, fmt.Errorf("query time_limited keys: %w", err)
	}

	now := time.Now()
	updated := 0

	for _, key := range keys {
		if key.ActivatedAt == nil {
			continue
		}

		// Calculate current period start (aligned to activated_at, 24h windows)
		anchor := *key.ActivatedAt
		elapsed := now.Sub(anchor)
		if elapsed < 0 {
			continue
		}
		periods := int(elapsed / (24 * time.Hour))
		periodStart := anchor.Add(time.Duration(periods) * 24 * time.Hour)
		periodEnd := periodStart.Add(24 * time.Hour)

		// Count actual requests in this period from usage_logs
		var count int
		err := r.sqlDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM usage_logs WHERE temp_api_key_id = $1 AND created_at >= $2 AND created_at < $3",
			key.ID, periodStart, periodEnd,
		).Scan(&count)
		if err != nil {
			log.Printf("[TempAPIKey] RecalculatePeriodCounts: count query failed for id=%d: %v", key.ID, err)
			continue
		}

		// Also count total requests across all time
		var totalCount int64
		err = r.sqlDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM usage_logs WHERE temp_api_key_id = $1",
			key.ID,
		).Scan(&totalCount)
		if err != nil {
			log.Printf("[TempAPIKey] RecalculatePeriodCounts: total count query failed for id=%d: %v", key.ID, err)
			continue
		}

		// Update the key
		updater := r.client.TempAPIKey.UpdateOneID(key.ID).
			SetCurrentPeriodStart(periodStart).
			SetCurrentPeriodCount(count).
			SetTotalRequests(totalCount)

		// time_quota 类型：重算当前周期的 USD 消费
		if key.KeyType == service.TempAPIKeyTypeTimeQuota {
			var periodCost float64
			err = r.sqlDB.QueryRowContext(ctx,
				"SELECT COALESCE(SUM(actual_cost), 0) FROM usage_logs WHERE temp_api_key_id = $1 AND created_at >= $2 AND created_at < $3",
				key.ID, periodStart, periodEnd,
			).Scan(&periodCost)
			if err != nil {
				log.Printf("[TempAPIKey] RecalculatePeriodCounts: cost query failed for id=%d: %v", key.ID, err)
				continue
			}
			updater.SetCurrentPeriodCostUsd(periodCost)
		}

		_, err = updater.Save(ctx)
		if err != nil {
			log.Printf("[TempAPIKey] RecalculatePeriodCounts: update failed for id=%d: %v", key.ID, err)
			continue
		}

		updated++
	}

	return updated, nil
}
