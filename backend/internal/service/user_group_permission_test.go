//go:build unit

package service

import "testing"

func TestUserCanBindGroup_PublicGroupsRemainAvailableWithExplicitGroups(t *testing.T) {
	user := &User{AllowedGroups: []int64{42}}

	if !user.CanBindGroup(7, false) {
		t.Fatal("expected public group to remain available")
	}
	if !user.CanBindGroup(42, true) {
		t.Fatal("expected explicitly allowed exclusive group to be available")
	}
	if user.CanBindGroup(99, true) {
		t.Fatal("expected unlisted exclusive group to remain unavailable")
	}
}

func TestUserCanBindGroup_NoExplicitGroupsOnlyAllowsPublicGroups(t *testing.T) {
	user := &User{}

	if !user.CanBindGroup(7, false) {
		t.Fatal("expected public group to be available")
	}
	if user.CanBindGroup(42, true) {
		t.Fatal("expected exclusive group to require explicit access")
	}
}
