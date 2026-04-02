package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbuser "github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/ent/userallowedgroup"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type userRepository struct {
	client *dbent.Client
	sql    sqlExecutor
}

func NewUserRepository(client *dbent.Client, sqlDB *sql.DB) service.UserRepository {
	return newUserRepositoryWithSQL(client, sqlDB)
}

func newUserRepositoryWithSQL(client *dbent.Client, sqlq sqlExecutor) *userRepository {
	return &userRepository{client: client, sql: sqlq}
}

func (r *userRepository) Create(ctx context.Context, userIn *service.User) error {
	if userIn == nil {
		return nil
	}

	// 统一使用 ent 的事务：保证用户与允许分组的更新原子化，
	// 并避免基于 *sql.Tx 手动构造 ent client 导致的 ExecQuerier 断言错误。
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return err
	}

	var txClient *dbent.Client
	if err == nil {
		defer func() { _ = tx.Rollback() }()
		txClient = tx.Client()
	} else {
		// 已处于外部事务中（ErrTxStarted），复用当前 client 并由调用方负责提交/回滚。
		txClient = r.client
	}

	created, err := txClient.User.Create().
		SetEmail(userIn.Email).
		SetUsername(userIn.Username).
		SetNotes(userIn.Notes).
		SetPasswordHash(userIn.PasswordHash).
		SetRole(userIn.Role).
		SetBalance(userIn.Balance).
		SetConcurrency(userIn.Concurrency).
		SetStatus(userIn.Status).
		Save(ctx)
	if err != nil {
		return translatePersistenceError(err, nil, service.ErrEmailExists)
	}

	if err := r.syncUserAllowedGroupsWithClient(ctx, txClient, created.ID, userIn.AllowedGroups); err != nil {
		return err
	}

	if tx != nil {
		if err := tx.Commit(); err != nil {
			return err
		}
	}

	applyUserEntityToService(userIn, created)
	return nil
}

func (r *userRepository) GetByID(ctx context.Context, id int64) (*service.User, error) {
	m, err := r.client.User.Query().Where(dbuser.IDEQ(id)).Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrUserNotFound, nil)
	}

	out := userEntityToService(m)
	groups, rates, err := r.loadAllowedGroups(ctx, []int64{id})
	if err != nil {
		return nil, err
	}
	if v, ok := groups[id]; ok {
		out.AllowedGroups = v
	}
	if v, ok := rates[id]; ok {
		out.AllowedGroupRates = v
	}
	return out, nil
}

func (r *userRepository) GetByEmail(ctx context.Context, email string) (*service.User, error) {
	m, err := r.client.User.Query().Where(dbuser.EmailEQ(email)).Only(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrUserNotFound, nil)
	}

	out := userEntityToService(m)
	groups, rates, err := r.loadAllowedGroups(ctx, []int64{m.ID})
	if err != nil {
		return nil, err
	}
	if v, ok := groups[m.ID]; ok {
		out.AllowedGroups = v
	}
	if v, ok := rates[m.ID]; ok {
		out.AllowedGroupRates = v
	}
	return out, nil
}

func (r *userRepository) Update(ctx context.Context, userIn *service.User) error {
	if userIn == nil {
		return nil
	}

	// 使用 ent 事务包裹用户更新与 allowed_groups 同步，避免跨层事务不一致。
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return err
	}

	var txClient *dbent.Client
	if err == nil {
		defer func() { _ = tx.Rollback() }()
		txClient = tx.Client()
	} else {
		// 已处于外部事务中（ErrTxStarted），复用当前 client 并由调用方负责提交/回滚。
		txClient = r.client
	}

	updated, err := txClient.User.UpdateOneID(userIn.ID).
		SetEmail(userIn.Email).
		SetUsername(userIn.Username).
		SetNotes(userIn.Notes).
		SetPasswordHash(userIn.PasswordHash).
		SetRole(userIn.Role).
		SetBalance(userIn.Balance).
		SetConcurrency(userIn.Concurrency).
		SetStatus(userIn.Status).
		Save(ctx)
	if err != nil {
		return translatePersistenceError(err, service.ErrUserNotFound, service.ErrEmailExists)
	}

	if err := r.syncUserAllowedGroupsWithClient(ctx, txClient, updated.ID, userIn.AllowedGroups); err != nil {
		return err
	}

	if tx != nil {
		if err := tx.Commit(); err != nil {
			return err
		}
	}

	userIn.UpdatedAt = updated.UpdatedAt
	return nil
}

func (r *userRepository) Delete(ctx context.Context, id int64) error {
	affected, err := r.client.User.Delete().Where(dbuser.IDEQ(id)).Exec(ctx)
	if err != nil {
		return translatePersistenceError(err, service.ErrUserNotFound, nil)
	}
	if affected == 0 {
		return service.ErrUserNotFound
	}
	return nil
}

func (r *userRepository) List(ctx context.Context, params pagination.PaginationParams) ([]service.User, *pagination.PaginationResult, error) {
	return r.ListWithFilters(ctx, params, service.UserListFilters{})
}

func (r *userRepository) ListWithFilters(ctx context.Context, params pagination.PaginationParams, filters service.UserListFilters) ([]service.User, *pagination.PaginationResult, error) {
	q := r.client.User.Query()

	if filters.Status != "" {
		q = q.Where(dbuser.StatusEQ(filters.Status))
	}
	if filters.Role != "" {
		q = q.Where(dbuser.RoleEQ(filters.Role))
	}
	if filters.Search != "" {
		q = q.Where(
			dbuser.Or(
				dbuser.EmailContainsFold(filters.Search),
				dbuser.UsernameContainsFold(filters.Search),
			),
		)
	}

	// If attribute filters are specified, we need to filter by user IDs first
	var allowedUserIDs []int64
	if len(filters.Attributes) > 0 {
		var attrErr error
		allowedUserIDs, attrErr = r.filterUsersByAttributes(ctx, filters.Attributes)
		if attrErr != nil {
			return nil, nil, attrErr
		}
		if len(allowedUserIDs) == 0 {
			// No users match the attribute filters
			return []service.User{}, paginationResultFromTotal(0, params), nil
		}
		q = q.Where(dbuser.IDIn(allowedUserIDs...))
	}

	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, nil, err
	}

	users, err := q.
		Offset(params.Offset()).
		Limit(params.Limit()).
		Order(dbent.Desc(dbuser.FieldID)).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}

	outUsers := make([]service.User, 0, len(users))
	if len(users) == 0 {
		return outUsers, paginationResultFromTotal(int64(total), params), nil
	}

	userIDs := make([]int64, 0, len(users))
	userMap := make(map[int64]*service.User, len(users))
	for i := range users {
		userIDs = append(userIDs, users[i].ID)
		u := userEntityToService(users[i])
		outUsers = append(outUsers, *u)
		userMap[u.ID] = &outUsers[len(outUsers)-1]
	}

	// Batch load active subscriptions with groups to avoid N+1.
	subs, err := r.client.UserSubscription.Query().
		Where(
			usersubscription.UserIDIn(userIDs...),
			usersubscription.StatusEQ(service.SubscriptionStatusActive),
		).
		WithGroup().
		All(ctx)
	if err != nil {
		return nil, nil, err
	}

	for i := range subs {
		if u, ok := userMap[subs[i].UserID]; ok {
			u.Subscriptions = append(u.Subscriptions, *userSubscriptionEntityToService(subs[i]))
		}
	}

	allowedGroupsByUser, ratesByUser, err := r.loadAllowedGroups(ctx, userIDs)
	if err != nil {
		return nil, nil, err
	}
	for id, u := range userMap {
		if groups, ok := allowedGroupsByUser[id]; ok {
			u.AllowedGroups = groups
		}
		if rates, ok := ratesByUser[id]; ok {
			u.AllowedGroupRates = rates
		}
	}

	return outUsers, paginationResultFromTotal(int64(total), params), nil
}

// filterUsersByAttributes returns user IDs that match ALL the given attribute filters
func (r *userRepository) filterUsersByAttributes(ctx context.Context, attrs map[int64]string) ([]int64, error) {
	if len(attrs) == 0 {
		return nil, nil
	}

	if r.sql == nil {
		return nil, fmt.Errorf("sql executor is not configured")
	}

	clauses := make([]string, 0, len(attrs))
	args := make([]any, 0, len(attrs)*2+1)
	argIndex := 1
	for attrID, value := range attrs {
		clauses = append(clauses, fmt.Sprintf("(attribute_id = $%d AND value ILIKE $%d)", argIndex, argIndex+1))
		args = append(args, attrID, "%"+value+"%")
		argIndex += 2
	}

	query := fmt.Sprintf(
		`SELECT user_id
		 FROM user_attribute_values
		 WHERE %s
		 GROUP BY user_id
		 HAVING COUNT(DISTINCT attribute_id) = $%d`,
		strings.Join(clauses, " OR "),
		argIndex,
	)
	args = append(args, len(attrs))

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	result := make([]int64, 0)
	for rows.Next() {
		var userID int64
		if scanErr := rows.Scan(&userID); scanErr != nil {
			return nil, scanErr
		}
		result = append(result, userID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *userRepository) UpdateBalance(ctx context.Context, id int64, amount float64) error {
	client := clientFromContext(ctx, r.client)
	n, err := client.User.Update().Where(dbuser.IDEQ(id)).AddBalance(amount).Save(ctx)
	if err != nil {
		return translatePersistenceError(err, service.ErrUserNotFound, nil)
	}
	if n == 0 {
		return service.ErrUserNotFound
	}
	return nil
}

// DeductBalance 扣除用户余额
// 透支策略：允许余额变为负数，确保当前请求能够完成
// 中间件会阻止余额 <= 0 的用户发起后续请求
func (r *userRepository) DeductBalance(ctx context.Context, id int64, amount float64) error {
	client := clientFromContext(ctx, r.client)
	n, err := client.User.Update().
		Where(dbuser.IDEQ(id)).
		AddBalance(-amount).
		Save(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return service.ErrUserNotFound
	}
	return nil
}

func (r *userRepository) UpdateConcurrency(ctx context.Context, id int64, amount int) error {
	client := clientFromContext(ctx, r.client)
	n, err := client.User.Update().Where(dbuser.IDEQ(id)).AddConcurrency(amount).Save(ctx)
	if err != nil {
		return translatePersistenceError(err, service.ErrUserNotFound, nil)
	}
	if n == 0 {
		return service.ErrUserNotFound
	}
	return nil
}

func (r *userRepository) ExistsByEmail(ctx context.Context, email string) (bool, error) {
	return r.client.User.Query().Where(dbuser.EmailEQ(email)).Exist(ctx)
}

func (r *userRepository) RemoveGroupFromAllowedGroups(ctx context.Context, groupID int64) (int64, error) {
	// 仅操作 user_allowed_groups 联接表，legacy users.allowed_groups 列已弃用。
	affected, err := r.client.UserAllowedGroup.Delete().
		Where(userallowedgroup.GroupIDEQ(groupID)).
		Exec(ctx)
	if err != nil {
		return 0, err
	}
	return int64(affected), nil
}

func (r *userRepository) GetFirstAdmin(ctx context.Context) (*service.User, error) {
	m, err := r.client.User.Query().
		Where(
			dbuser.RoleEQ(service.RoleAdmin),
			dbuser.StatusEQ(service.StatusActive),
		).
		Order(dbent.Asc(dbuser.FieldID)).
		First(ctx)
	if err != nil {
		return nil, translatePersistenceError(err, service.ErrUserNotFound, nil)
	}

	out := userEntityToService(m)
	groups, rates, err := r.loadAllowedGroups(ctx, []int64{m.ID})
	if err != nil {
		return nil, err
	}
	if v, ok := groups[m.ID]; ok {
		out.AllowedGroups = v
	}
	if v, ok := rates[m.ID]; ok {
		out.AllowedGroupRates = v
	}
	return out, nil
}

func (r *userRepository) loadAllowedGroups(ctx context.Context, userIDs []int64) (map[int64][]int64, map[int64]map[int64]*float64, error) {
	groups := make(map[int64][]int64, len(userIDs))
	rates := make(map[int64]map[int64]*float64, len(userIDs))
	if len(userIDs) == 0 {
		return groups, rates, nil
	}

	rows, err := r.client.UserAllowedGroup.Query().
		Where(userallowedgroup.UserIDIn(userIDs...)).
		All(ctx)
	if err != nil {
		return nil, nil, err
	}

	for i := range rows {
		uid := rows[i].UserID
		gid := rows[i].GroupID
		groups[uid] = append(groups[uid], gid)
		if rows[i].RateMultiplier != nil {
			if rates[uid] == nil {
				rates[uid] = make(map[int64]*float64)
			}
			rates[uid][gid] = rows[i].RateMultiplier
		}
	}

	for userID := range groups {
		sort.Slice(groups[userID], func(i, j int) bool { return groups[userID][i] < groups[userID][j] })
	}

	return groups, rates, nil
}

// syncUserAllowedGroupsWithClient 在 ent client/事务内同步用户允许分组：
// 仅操作 user_allowed_groups 联接表，legacy users.allowed_groups 列已弃用。
// 保留已有记录的 rate_multiplier 自定义倍率。
func (r *userRepository) syncUserAllowedGroupsWithClient(ctx context.Context, client *dbent.Client, userID int64, groupIDs []int64) error {
	if client == nil {
		return nil
	}

	// 1. 读取已有记录的 rate_multiplier，以便重建时恢复
	existing, err := client.UserAllowedGroup.Query().
		Where(userallowedgroup.UserIDEQ(userID)).
		All(ctx)
	if err != nil {
		return err
	}
	existingRates := make(map[int64]*float64, len(existing))
	for _, row := range existing {
		if row.RateMultiplier != nil {
			existingRates[row.GroupID] = row.RateMultiplier
		}
	}

	// 2. 删除所有旧记录
	if _, err := client.UserAllowedGroup.Delete().Where(userallowedgroup.UserIDEQ(userID)).Exec(ctx); err != nil {
		return err
	}

	unique := make(map[int64]struct{}, len(groupIDs))
	for _, id := range groupIDs {
		if id <= 0 {
			continue
		}
		unique[id] = struct{}{}
	}

	// 3. 重建，恢复已有的 rate_multiplier
	if len(unique) > 0 {
		creates := make([]*dbent.UserAllowedGroupCreate, 0, len(unique))
		for groupID := range unique {
			c := client.UserAllowedGroup.Create().SetUserID(userID).SetGroupID(groupID)
			if rate, ok := existingRates[groupID]; ok {
				c = c.SetNillableRateMultiplier(rate)
			}
			creates = append(creates, c)
		}
		if err := client.UserAllowedGroup.
			CreateBulk(creates...).
			OnConflictColumns(userallowedgroup.FieldUserID, userallowedgroup.FieldGroupID).
			DoNothing().
			Exec(ctx); err != nil {
			return err
		}
	}

	return nil
}

func applyUserEntityToService(dst *service.User, src *dbent.User) {
	if dst == nil || src == nil {
		return
	}
	dst.ID = src.ID
	dst.CreatedAt = src.CreatedAt
	dst.UpdatedAt = src.UpdatedAt
}

// GetUserGroupRates returns all per-user rate multiplier overrides for a user.
// Key: groupID, Value: custom rate (nil entries are excluded).
func (r *userRepository) GetUserGroupRates(ctx context.Context, userID int64) (map[int64]*float64, error) {
	rows, err := r.client.UserAllowedGroup.Query().
		Where(
			userallowedgroup.UserIDEQ(userID),
			userallowedgroup.RateMultiplierNotNil(),
		).
		All(ctx)
	if err != nil {
		return nil, err
	}

	out := make(map[int64]*float64, len(rows))
	for _, row := range rows {
		out[row.GroupID] = row.RateMultiplier
	}
	return out, nil
}

// SetUserGroupRates batch-updates per-user rate multiplier overrides.
// rates map: groupID -> rate (*float64, nil means clear the override).
// Only updates rows that already exist in user_allowed_groups.
// Wrapped in a transaction to ensure atomicity.
func (r *userRepository) SetUserGroupRates(ctx context.Context, userID int64, rates map[int64]*float64) error {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	txClient := tx.Client()
	for groupID, rate := range rates {
		update := txClient.UserAllowedGroup.Update().
			Where(
				userallowedgroup.UserIDEQ(userID),
				userallowedgroup.GroupIDEQ(groupID),
			)
		if rate != nil {
			update = update.SetRateMultiplier(*rate)
		} else {
			update = update.ClearRateMultiplier()
		}
		if _, err := update.Save(ctx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetUserGroupRate returns the per-user rate multiplier for a specific group.
// Returns nil if no override is set.
func (r *userRepository) GetUserGroupRate(ctx context.Context, userID int64, groupID int64) (*float64, error) {
	row, err := r.client.UserAllowedGroup.Query().
		Where(
			userallowedgroup.UserIDEQ(userID),
			userallowedgroup.GroupIDEQ(groupID),
		).
		Only(ctx)
	if err != nil {
		// If no row found, return nil (no override)
		if dbent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return row.RateMultiplier, nil
}
