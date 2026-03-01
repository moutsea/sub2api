package service

import (
	"context"
	"errors"
	"time"
)

// TempAPIKeyService handles temp API key business logic
type TempAPIKeyService struct {
	repo TempAPIKeyRepository
}

// TempAPIKeyRepository defines the interface for temp API key data access
type TempAPIKeyRepository interface {
	GetByKey(ctx context.Context, key string) (*TempAPIKey, error)
	ActivateAndIncrement(ctx context.Context, id int64) (*TempAPIKey, bool, error)
	AddCostUSD(ctx context.Context, id int64, costUSD float64) (*TempAPIKey, bool, error)
	AddDailyCostUSD(ctx context.Context, id int64, costUSD float64) (*TempAPIKey, bool, error)
	IncrementUsageCounters(ctx context.Context, id int64) error
}

// NewTempAPIKeyService creates a new TempAPIKeyService
func NewTempAPIKeyService(repo TempAPIKeyRepository) *TempAPIKeyService {
	return &TempAPIKeyService{repo: repo}
}

// Temp API Key errors
var (
	ErrTempAPIKeyNotFound    = errors.New("temp API key not found")
	ErrTempAPIKeyExpired     = errors.New("temp API key has expired")
	ErrTempAPIKeyInactive    = errors.New("temp API key is inactive")
	ErrTempAPIKeyRateLimited = errors.New("temp API key rate limit exceeded")
	ErrTempAPIKeyExhausted   = errors.New("temp API key quota exhausted")
)

// GetByKey retrieves a temp API key by key string
func (s *TempAPIKeyService) GetByKey(ctx context.Context, key string) (*TempAPIKey, error) {
	return s.repo.GetByKey(ctx, key)
}

// ValidateAndIncrement validates a temp API key and increments its usage
// Returns the updated key if valid, or an error if validation fails
func (s *TempAPIKeyService) ValidateAndIncrement(ctx context.Context, key *TempAPIKey) (*TempAPIKey, error) {
	// Check status
	if key.Status == TempAPIKeyStatusInactive {
		return nil, ErrTempAPIKeyInactive
	}

	// Check exhausted status (for quota_only keys)
	if key.Status == TempAPIKeyStatusExhausted {
		return nil, ErrTempAPIKeyExhausted
	}

	// For quota_only type, check if quota is exhausted
	if key.KeyType == TempAPIKeyTypeQuotaOnly {
		if key.IsExhausted() {
			return nil, ErrTempAPIKeyExhausted
		}
	} else {
		// For time_limited / time_quota type, check if already expired (for activated keys)
		if key.ExpiresAt != nil && time.Now().After(*key.ExpiresAt) {
			return nil, ErrTempAPIKeyExpired
		}
	}

	// Activate (if needed) and increment usage
	updated, rateLimited, err := s.repo.ActivateAndIncrement(ctx, key.ID)
	if err != nil {
		return nil, err
	}

	if rateLimited {
		return updated, ErrTempAPIKeyRateLimited
	}

	return updated, nil
}

// IsTempAPIKey checks if a key string is a temp API key
// Supports both new prefix (sk-cc-) and legacy prefix (sk-temp-)
func IsTempAPIKey(key string) bool {
	return (len(key) > 6 && key[:6] == "sk-cc-") ||
		(len(key) > 8 && key[:8] == "sk-temp-")
}
