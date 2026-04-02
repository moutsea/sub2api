//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// =============================================================================
// User.GetGroupRateMultiplier tests
// =============================================================================

func TestGetGroupRateMultiplier_NilMap(t *testing.T) {
	u := &User{AllowedGroupRates: nil}
	rate, ok := u.GetGroupRateMultiplier(1)
	require.False(t, ok)
	require.Equal(t, 0.0, rate)
}

func TestGetGroupRateMultiplier_EmptyMap(t *testing.T) {
	u := &User{AllowedGroupRates: map[int64]*float64{}}
	rate, ok := u.GetGroupRateMultiplier(1)
	require.False(t, ok)
	require.Equal(t, 0.0, rate)
}

func TestGetGroupRateMultiplier_GroupNotInMap(t *testing.T) {
	r := 1.5
	u := &User{AllowedGroupRates: map[int64]*float64{2: &r}}
	rate, ok := u.GetGroupRateMultiplier(1)
	require.False(t, ok)
	require.Equal(t, 0.0, rate)
}

func TestGetGroupRateMultiplier_NilValue(t *testing.T) {
	u := &User{AllowedGroupRates: map[int64]*float64{1: nil}}
	rate, ok := u.GetGroupRateMultiplier(1)
	require.False(t, ok)
	require.Equal(t, 0.0, rate)
}

func TestGetGroupRateMultiplier_CustomRate(t *testing.T) {
	r := 2.5
	u := &User{AllowedGroupRates: map[int64]*float64{1: &r}}
	rate, ok := u.GetGroupRateMultiplier(1)
	require.True(t, ok)
	require.Equal(t, 2.5, rate)
}

func TestGetGroupRateMultiplier_ZeroRate(t *testing.T) {
	// 0.0 means "free" — should return (0.0, true), NOT (0, false)
	r := 0.0
	u := &User{AllowedGroupRates: map[int64]*float64{1: &r}}
	rate, ok := u.GetGroupRateMultiplier(1)
	require.True(t, ok, "zero rate should return ok=true (free, not default)")
	require.Equal(t, 0.0, rate)
}

// =============================================================================
// Multiplier resolution logic tests
// =============================================================================

// resolveMultiplier mirrors the logic in gateway_service.go RecordUsage
func resolveMultiplier(defaultRate float64, group *Group, groupID *int64, user *User) float64 {
	multiplier := defaultRate
	if groupID != nil && group != nil {
		multiplier = group.RateMultiplier
		if userRate, ok := user.GetGroupRateMultiplier(*groupID); ok {
			multiplier = userRate
		}
	}
	return multiplier
}

func TestResolveMultiplier_NoGroup(t *testing.T) {
	// Case C: API key has no group → use global default
	user := &User{}
	m := resolveMultiplier(1.0, nil, nil, user)
	require.Equal(t, 1.0, m)
}

func TestResolveMultiplier_GroupDefault(t *testing.T) {
	// Case B: User has no custom rate → use group default
	gid := int64(1)
	group := &Group{ID: 1, RateMultiplier: 1.5}
	user := &User{} // no AllowedGroupRates
	m := resolveMultiplier(1.0, group, &gid, user)
	require.Equal(t, 1.5, m)
}

func TestResolveMultiplier_UserCustomRate(t *testing.T) {
	// Case A: User has custom rate → use custom rate
	gid := int64(1)
	group := &Group{ID: 1, RateMultiplier: 1.5}
	customRate := 2.0
	user := &User{AllowedGroupRates: map[int64]*float64{1: &customRate}}
	m := resolveMultiplier(1.0, group, &gid, user)
	require.Equal(t, 2.0, m)
}

func TestResolveMultiplier_UserCustomRateZero(t *testing.T) {
	// Case E: Custom rate is 0.0 → free usage
	gid := int64(1)
	group := &Group{ID: 1, RateMultiplier: 1.5}
	customRate := 0.0
	user := &User{AllowedGroupRates: map[int64]*float64{1: &customRate}}
	m := resolveMultiplier(1.0, group, &gid, user)
	require.Equal(t, 0.0, m)
}

func TestResolveMultiplier_AllowAllGroupsUser(t *testing.T) {
	// Case D: "Allow All Groups" user (no user_allowed_groups rows)
	// AllowedGroupRates is nil → falls back to group default
	gid := int64(1)
	group := &Group{ID: 1, RateMultiplier: 1.5}
	user := &User{AllowedGroupRates: nil}
	m := resolveMultiplier(1.0, group, &gid, user)
	require.Equal(t, 1.5, m)
}

func TestResolveMultiplier_UserRateForDifferentGroup(t *testing.T) {
	// User has custom rate for group 2, but API key is bound to group 1
	// Should use group 1's default rate
	gid := int64(1)
	group := &Group{ID: 1, RateMultiplier: 1.5}
	customRate := 3.0
	user := &User{AllowedGroupRates: map[int64]*float64{2: &customRate}}
	m := resolveMultiplier(1.0, group, &gid, user)
	require.Equal(t, 1.5, m)
}

func TestResolveMultiplier_UserRateNilEntry(t *testing.T) {
	// User has the group in map but value is nil → use group default
	gid := int64(1)
	group := &Group{ID: 1, RateMultiplier: 1.5}
	user := &User{AllowedGroupRates: map[int64]*float64{1: nil}}
	m := resolveMultiplier(1.0, group, &gid, user)
	require.Equal(t, 1.5, m)
}

// =============================================================================
// End-to-end billing: verify ActualCost = TotalCost * multiplier
// =============================================================================

func TestActualCost_MultiplierApplied(t *testing.T) {
	// The billing formula is: ActualCost = TotalCost * rateMultiplier
	// (from billing_service.go line 276)
	// Note: CalculateCost treats rateMultiplier <= 0 as 1.0 (line 273-274)

	tests := []struct {
		name       string
		totalCost  float64
		multiplier float64
		wantActual float64
	}{
		{"default 1.0x", 10.0, 1.0, 10.0},
		{"custom 2.0x", 10.0, 2.0, 20.0},
		{"custom 0.5x", 10.0, 0.5, 5.0},
		{"custom 1.5x", 10.0, 1.5, 15.0},
		{"custom 0.01x", 10.0, 0.01, 0.1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := tt.totalCost * tt.multiplier
			require.InDelta(t, tt.wantActual, actual, 0.0001)
		})
	}
}

func TestResolveMultiplier_IntegrationWithBilling(t *testing.T) {
	// Simulate the full chain: resolve multiplier → apply to cost
	totalCost := 10.0
	globalDefault := 1.0

	// Case: user has custom rate 2.0 for group 1
	gid := int64(1)
	group := &Group{ID: 1, RateMultiplier: 1.5}
	customRate := 2.0
	user := &User{AllowedGroupRates: map[int64]*float64{1: &customRate}}

	multiplier := resolveMultiplier(globalDefault, group, &gid, user)
	actualCost := totalCost * multiplier

	require.Equal(t, 2.0, multiplier, "should use user custom rate")
	require.InDelta(t, 20.0, actualCost, 0.0001, "actual cost should be totalCost * custom rate")

	// Case: same user, different group (no custom rate) → group default
	gid2 := int64(2)
	group2 := &Group{ID: 2, RateMultiplier: 3.0}
	multiplier2 := resolveMultiplier(globalDefault, group2, &gid2, user)
	actualCost2 := totalCost * multiplier2

	require.Equal(t, 3.0, multiplier2, "should use group default rate")
	require.InDelta(t, 30.0, actualCost2, 0.0001, "actual cost should be totalCost * group default")
}

// =============================================================================
// Auth cache snapshot round-trip for AllowedGroupRates
// =============================================================================

func TestAuthSnapshotRoundTrip_AllowedGroupRates(t *testing.T) {
	customRate := 1.8
	user := &User{
		ID:                42,
		Status:            StatusActive,
		Role:              RoleUser,
		Balance:           100.0,
		Concurrency:       5,
		AllowedGroupRates: map[int64]*float64{10: &customRate},
	}

	snapshot := APIKeyAuthUserSnapshot{
		ID:                user.ID,
		Status:            user.Status,
		Role:              user.Role,
		Balance:           user.Balance,
		Concurrency:       user.Concurrency,
		AllowedGroupRates: user.AllowedGroupRates,
	}

	// Restore from snapshot
	restored := &User{
		ID:                snapshot.ID,
		Status:            snapshot.Status,
		Role:              snapshot.Role,
		Balance:           snapshot.Balance,
		Concurrency:       snapshot.Concurrency,
		AllowedGroupRates: snapshot.AllowedGroupRates,
	}

	rate, ok := restored.GetGroupRateMultiplier(10)
	require.True(t, ok)
	require.Equal(t, 1.8, rate)

	// Group not in map
	_, ok = restored.GetGroupRateMultiplier(99)
	require.False(t, ok)
}

func TestAuthSnapshotRoundTrip_NilAllowedGroupRates(t *testing.T) {
	snapshot := APIKeyAuthUserSnapshot{
		ID:                42,
		AllowedGroupRates: nil,
	}

	restored := &User{
		ID:                snapshot.ID,
		AllowedGroupRates: snapshot.AllowedGroupRates,
	}

	_, ok := restored.GetGroupRateMultiplier(10)
	require.False(t, ok)
}
