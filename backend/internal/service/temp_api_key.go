package service

import "time"

// TempAPIKey represents a temporary API key with expiration and rate limiting
type TempAPIKey struct {
	ID        int64
	Key       string
	Name      string
	GroupID   int64
	Group     *Group

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
	Status string // active, inactive, expired

	// 创建信息
	CreatedBy int64
	Creator   *User
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TempAPIKeyStatus constants
const (
	TempAPIKeyStatusActive   = "active"
	TempAPIKeyStatusInactive = "inactive"
	TempAPIKeyStatusExpired  = "expired"
)

// IsExpired checks if the temp API key is expired
func (k *TempAPIKey) IsExpired() bool {
	if k.ExpiresAt == nil {
		return false // Not activated yet
	}
	return time.Now().After(*k.ExpiresAt)
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

// RemainingRequests returns the number of remaining requests in current period
func (k *TempAPIKey) RemainingRequests() int {
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
