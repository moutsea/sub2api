package service

import "time"

// TempAPIKey represents a temporary API key with expiration and rate limiting
type TempAPIKey struct {
	ID        int64
	Key       string
	Name      string
	GroupID   int64
	Group     *Group

	// Key 类型
	KeyType       string  // time_limited（限时限额）、quota_only（仅限额不限时）
	TotalQuotaUSD float64 // 总额度限制（美元），仅 quota_only 类型使用
	TotalCostUSD  float64 // 已消费金额（美元），仅 quota_only 类型使用

	// 有效期设置
	ValidDays   int        // 有效天数
	ActivatedAt *time.Time // 首次激活时间
	ExpiresAt   *time.Time // 过期时间

	// 请求限制
	DailyLimit int // 每日请求限制

	// 当前周期使用统计
	CurrentPeriodStart *time.Time // 当前 24 小时周期开始时间
	CurrentPeriodCount int        // 当前周期请求次数

	// 总计统计
	TotalRequests int64 // 总请求次数

	// 状态
	Status string // active, inactive, expired, exhausted

	// 创建信息
	CreatedBy int64
	Creator   *User
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TempAPIKeyStatus constants
const (
	TempAPIKeyStatusActive    = "active"
	TempAPIKeyStatusInactive  = "inactive"
	TempAPIKeyStatusExpired   = "expired"
	TempAPIKeyStatusExhausted = "exhausted" // 额度用尽
)

// TempAPIKeyType constants
const (
	TempAPIKeyTypeLimited   = "time_limited" // 限时限额（默认）
	TempAPIKeyTypeQuotaOnly = "quota_only"   // 仅限额不限时
)

// IsExpired checks if the temp API key is expired
// quota_only 类型永不过期
func (k *TempAPIKey) IsExpired() bool {
	if k.KeyType == TempAPIKeyTypeQuotaOnly {
		return false // quota_only 类型不限时间
	}
	if k.ExpiresAt == nil {
		return false // Not activated yet
	}
	return time.Now().After(*k.ExpiresAt)
}

// IsExhausted checks if the quota_only key has exhausted its total quota (based on USD cost)
func (k *TempAPIKey) IsExhausted() bool {
	if k.KeyType != TempAPIKeyTypeQuotaOnly {
		return false
	}
	if k.TotalQuotaUSD <= 0 {
		return false // 0 表示不限制
	}
	return k.TotalCostUSD >= k.TotalQuotaUSD
}

// IsActivated checks if the temp API key has been activated
func (k *TempAPIKey) IsActivated() bool {
	return k.ActivatedAt != nil
}

// IsRateLimited checks if the temp API key has exceeded its daily limit
func (k *TempAPIKey) IsRateLimited() bool {
	if k.CurrentPeriodStart == nil {
		return false
	}
	// Check if current period has expired (24 hours)
	if time.Since(*k.CurrentPeriodStart) >= 24*time.Hour {
		return false // Period expired, will be reset on next use
	}
	return k.CurrentPeriodCount >= k.DailyLimit
}

// CurrentPeriodUsed returns the number of requests used in the current period.
// Returns 0 if the period has expired (not yet reset in DB).
func (k *TempAPIKey) CurrentPeriodUsed() int {
	if k.CurrentPeriodStart == nil {
		return 0
	}
	if time.Since(*k.CurrentPeriodStart) >= 24*time.Hour {
		return 0
	}
	return k.CurrentPeriodCount
}

// RemainingRequests returns the number of remaining requests in current period
// 对于 time_limited 类型，返回当前周期剩余请求次数
// 对于 quota_only 类型，此方法不适用，请使用 RemainingQuotaUSD
func (k *TempAPIKey) RemainingRequests() int {
	if k.KeyType == TempAPIKeyTypeQuotaOnly {
		return -1 // quota_only 类型不按请求次数限制
	}
	if k.CurrentPeriodStart == nil {
		return k.DailyLimit
	}
	// Check if current period has expired
	if time.Since(*k.CurrentPeriodStart) >= 24*time.Hour {
		return k.DailyLimit
	}
	remaining := k.DailyLimit - k.CurrentPeriodCount
	if remaining < 0 {
		return 0
	}
	return remaining
}

// RemainingQuotaUSD returns the remaining quota in USD for quota_only type
// 返回 -1 表示不限制
func (k *TempAPIKey) RemainingQuotaUSD() float64 {
	if k.KeyType != TempAPIKeyTypeQuotaOnly {
		return -1 // 非 quota_only 类型不适用
	}
	if k.TotalQuotaUSD <= 0 {
		return -1 // 不限制，返回 -1 表示无限
	}
	remaining := k.TotalQuotaUSD - k.TotalCostUSD
	if remaining < 0 {
		return 0
	}
	return remaining
}

// RemainingTime returns the remaining time until expiration
func (k *TempAPIKey) RemainingTime() *time.Duration {
	if k.ExpiresAt == nil {
		return nil
	}
	remaining := time.Until(*k.ExpiresAt)
	if remaining < 0 {
		zero := time.Duration(0)
		return &zero
	}
	return &remaining
}
