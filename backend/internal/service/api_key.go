package service

import (
	"log"
	"time"
)

type APIKey struct {
	ID            int64
	UserID        int64
	Key           string
	Name          string
	GroupID       *int64
	Status        string
	IPWhitelist   []string
	IPBlacklist   []string
	QuotaLimitUSD *float64
	QuotaUsedUSD  float64
	CreatedAt     time.Time
	UpdatedAt     time.Time
	User          *User
	Group         *Group
}

func (k *APIKey) IsActive() bool {
	return k.Status == StatusActive
}

func (k *APIKey) IsQuotaExceeded() bool {
	if k.QuotaLimitUSD == nil || *k.QuotaLimitUSD <= 0 {
		return false
	}
	exceeded := k.QuotaUsedUSD >= *k.QuotaLimitUSD
	if exceeded {
		log.Printf("[QuotaExceeded] api_key_id=%d used=%.6f limit=%.6f", k.ID, k.QuotaUsedUSD, *k.QuotaLimitUSD)
	}
	return exceeded
}
