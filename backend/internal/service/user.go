package service

import (
	"time"

	"golang.org/x/crypto/bcrypt"
)

type User struct {
	ID                int64
	Email             string
	Username          string
	Notes             string
	PasswordHash      string
	Role              string
	Balance           float64
	Concurrency       int
	Status            string
	AllowedGroups     []int64
	AllowedGroupRates map[int64]*float64 // Per-group custom rate_multiplier overrides (groupID -> rate, nil = use group default)
	TokenVersion      int64              // Incremented on password change to invalidate existing tokens
	CreatedAt         time.Time
	UpdatedAt         time.Time

	APIKeys       []APIKey
	Subscriptions []UserSubscription
}

func (u *User) IsAdmin() bool {
	return u.Role == RoleAdmin
}

func (u *User) IsActive() bool {
	return u.Status == StatusActive
}

// CanBindGroup checks whether a user can bind to a given group.
// For standard groups:
// - Non-exclusive groups are always available to every user.
// - AllowedGroups grants access to matching exclusive groups.
func (u *User) CanBindGroup(groupID int64, isExclusive bool) bool {
	if !isExclusive {
		return true
	}

	for _, id := range u.AllowedGroups {
		if id == groupID {
			return true
		}
	}
	return false
}

func (u *User) SetPassword(password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u.PasswordHash = string(hash)
	return nil
}

func (u *User) CheckPassword(password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) == nil
}

// GetGroupRateMultiplier returns the user's custom rate multiplier for a group.
// Returns (customRate, true) if a per-user override exists, or (0, false) if not.
func (u *User) GetGroupRateMultiplier(groupID int64) (float64, bool) {
	if u.AllowedGroupRates == nil {
		return 0, false
	}
	rate, ok := u.AllowedGroupRates[groupID]
	if !ok || rate == nil {
		return 0, false
	}
	return *rate, true
}
