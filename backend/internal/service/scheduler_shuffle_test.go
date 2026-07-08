package service

import (
	"testing"
	"time"
)

func TestShuffleWithinSortGroups_SingleElement(t *testing.T) {
	accounts := []accountWithLoad{
		{account: &Account{ID: 1, Priority: 1}, loadInfo: &AccountLoadInfo{LoadRate: 50}},
	}
	shuffleWithinSortGroups(accounts)
	if accounts[0].account.ID != 1 {
		t.Errorf("single element should remain unchanged")
	}
}

func TestShuffleWithinSortGroups_DifferentPriority(t *testing.T) {
	accounts := []accountWithLoad{
		{account: &Account{ID: 1, Priority: 1}, loadInfo: &AccountLoadInfo{LoadRate: 50}},
		{account: &Account{ID: 2, Priority: 2}, loadInfo: &AccountLoadInfo{LoadRate: 50}},
	}
	// Different priority = different groups, order must be preserved
	for i := 0; i < 100; i++ {
		shuffleWithinSortGroups(accounts)
		if accounts[0].account.ID != 1 || accounts[1].account.ID != 2 {
			t.Fatalf("different priority accounts should not be shuffled")
		}
	}
}

func TestShuffleWithinSortGroups_DifferentLoadRate(t *testing.T) {
	accounts := []accountWithLoad{
		{account: &Account{ID: 1, Priority: 1}, loadInfo: &AccountLoadInfo{LoadRate: 30}},
		{account: &Account{ID: 2, Priority: 1}, loadInfo: &AccountLoadInfo{LoadRate: 60}},
	}
	for i := 0; i < 100; i++ {
		shuffleWithinSortGroups(accounts)
		if accounts[0].account.ID != 1 || accounts[1].account.ID != 2 {
			t.Fatalf("different load rate accounts should not be shuffled")
		}
	}
}

func TestShuffleWithinSortGroups_SameGroupShuffles(t *testing.T) {
	now := time.Now()
	sameTime := now.Truncate(time.Second)

	sawDifferentOrder := false
	for i := 0; i < 200; i++ {
		accounts := []accountWithLoad{
			{account: &Account{ID: 1, Priority: 1, LastUsedAt: &sameTime}, loadInfo: &AccountLoadInfo{LoadRate: 50}},
			{account: &Account{ID: 2, Priority: 1, LastUsedAt: &sameTime}, loadInfo: &AccountLoadInfo{LoadRate: 50}},
			{account: &Account{ID: 3, Priority: 1, LastUsedAt: &sameTime}, loadInfo: &AccountLoadInfo{LoadRate: 50}},
		}
		shuffleWithinSortGroups(accounts)
		if accounts[0].account.ID != 1 {
			sawDifferentOrder = true
			break
		}
	}
	if !sawDifferentOrder {
		t.Errorf("same group accounts should be shuffled (statistically impossible to always be in order after 200 runs)")
	}
}

func TestShuffleWithinSortGroups_MixedGroups(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	later := now.Add(10 * time.Second)

	// Group 1: priority=1, load=30, time=now (IDs 1,2)
	// Group 2: priority=1, load=60, time=later (IDs 3,4)
	// Group 3: priority=2, load=30, time=now (ID 5)
	accounts := []accountWithLoad{
		{account: &Account{ID: 1, Priority: 1, LastUsedAt: &now}, loadInfo: &AccountLoadInfo{LoadRate: 30}},
		{account: &Account{ID: 2, Priority: 1, LastUsedAt: &now}, loadInfo: &AccountLoadInfo{LoadRate: 30}},
		{account: &Account{ID: 3, Priority: 1, LastUsedAt: &later}, loadInfo: &AccountLoadInfo{LoadRate: 60}},
		{account: &Account{ID: 4, Priority: 1, LastUsedAt: &later}, loadInfo: &AccountLoadInfo{LoadRate: 60}},
		{account: &Account{ID: 5, Priority: 2, LastUsedAt: &now}, loadInfo: &AccountLoadInfo{LoadRate: 30}},
	}

	for i := 0; i < 100; i++ {
		shuffleWithinSortGroups(accounts)

		// Group boundaries must be preserved
		g1 := map[int64]bool{accounts[0].account.ID: true, accounts[1].account.ID: true}
		g2 := map[int64]bool{accounts[2].account.ID: true, accounts[3].account.ID: true}

		if !g1[1] || !g1[2] {
			t.Fatalf("group 1 (IDs 1,2) should stay in positions 0-1, got %d,%d", accounts[0].account.ID, accounts[1].account.ID)
		}
		if !g2[3] || !g2[4] {
			t.Fatalf("group 2 (IDs 3,4) should stay in positions 2-3, got %d,%d", accounts[2].account.ID, accounts[3].account.ID)
		}
		if accounts[4].account.ID != 5 {
			t.Fatalf("group 3 (ID 5) should stay in position 4, got %d", accounts[4].account.ID)
		}
	}
}

func TestSortAccountWithLoadCandidates_KiroCreditsWeightedRandom(t *testing.T) {
	usageCache := NewUsageCache()
	usageCache.StoreKiroCredits(1, &KiroCreditsInfo{AvailableCredits: 100})
	usageCache.StoreKiroCredits(2, &KiroCreditsInfo{AvailableCredits: 1})
	svc := &GatewayService{usageCache: usageCache}

	highCreditFirst := 0
	lowCreditFirst := 0
	for i := 0; i < 2000; i++ {
		accounts := []accountWithLoad{
			{account: &Account{ID: 1, Platform: PlatformKiro, Priority: 1}, loadInfo: &AccountLoadInfo{LoadRate: 50}},
			{account: &Account{ID: 2, Platform: PlatformKiro, Priority: 1}, loadInfo: &AccountLoadInfo{LoadRate: 50}},
		}
		svc.sortAccountWithLoadCandidates(accounts, false)
		switch accounts[0].account.ID {
		case 1:
			highCreditFirst++
		case 2:
			lowCreditFirst++
		}
	}
	if highCreditFirst <= lowCreditFirst {
		t.Fatalf("high-credit account should be selected more often, high=%d low=%d", highCreditFirst, lowCreditFirst)
	}
	if lowCreditFirst == 0 {
		t.Fatalf("low-credit account should still have a small chance to be first, high=%d low=%d", highCreditFirst, lowCreditFirst)
	}
}

func TestSortAccountWithLoadCandidates_PriorityAndLoadRemainHardBoundaries(t *testing.T) {
	usageCache := NewUsageCache()
	usageCache.StoreKiroCredits(1, &KiroCreditsInfo{AvailableCredits: 1})
	usageCache.StoreKiroCredits(2, &KiroCreditsInfo{AvailableCredits: 1000})
	usageCache.StoreKiroCredits(3, &KiroCreditsInfo{AvailableCredits: 1000})
	svc := &GatewayService{usageCache: usageCache}

	for i := 0; i < 200; i++ {
		accounts := []accountWithLoad{
			{account: &Account{ID: 1, Platform: PlatformKiro, Priority: 1}, loadInfo: &AccountLoadInfo{LoadRate: 10}},
			{account: &Account{ID: 2, Platform: PlatformKiro, Priority: 2}, loadInfo: &AccountLoadInfo{LoadRate: 10}},
			{account: &Account{ID: 3, Platform: PlatformKiro, Priority: 1}, loadInfo: &AccountLoadInfo{LoadRate: 20}},
		}
		svc.sortAccountWithLoadCandidates(accounts, false)
		if accounts[0].account.ID != 1 {
			t.Fatalf("priority/load should outrank credits, got first account %d", accounts[0].account.ID)
		}
	}
}

func TestOpenAISortAccountWithLoadCandidates_KiroCreditsWeightedRandom(t *testing.T) {
	usageCache := NewUsageCache()
	usageCache.StoreKiroCredits(1, &KiroCreditsInfo{AvailableCredits: 100})
	usageCache.StoreKiroCredits(2, &KiroCreditsInfo{AvailableCredits: 1})
	svc := &OpenAIGatewayService{usageCache: usageCache}

	highCreditFirst := 0
	lowCreditFirst := 0
	for i := 0; i < 2000; i++ {
		accounts := []accountWithLoad{
			{account: &Account{ID: 1, Platform: PlatformKiro, Priority: 1}, loadInfo: &AccountLoadInfo{LoadRate: 50}},
			{account: &Account{ID: 2, Platform: PlatformKiro, Priority: 1}, loadInfo: &AccountLoadInfo{LoadRate: 50}},
		}
		svc.sortOpenAIAccountWithLoadCandidates(accounts)
		switch accounts[0].account.ID {
		case 1:
			highCreditFirst++
		case 2:
			lowCreditFirst++
		}
	}
	if highCreditFirst <= lowCreditFirst {
		t.Fatalf("high-credit account should be selected more often, high=%d low=%d", highCreditFirst, lowCreditFirst)
	}
	if lowCreditFirst == 0 {
		t.Fatalf("low-credit account should still have a small chance to be first, high=%d low=%d", highCreditFirst, lowCreditFirst)
	}
}

func TestShuffleWithinPriorityAndLastUsed_SameGroupShuffles(t *testing.T) {
	sawDifferentOrder := false
	for i := 0; i < 200; i++ {
		accounts := []*Account{
			{ID: 1, Priority: 1, LastUsedAt: nil},
			{ID: 2, Priority: 1, LastUsedAt: nil},
			{ID: 3, Priority: 1, LastUsedAt: nil},
		}
		shuffleWithinPriorityAndLastUsed(accounts)
		if accounts[0].ID != 1 {
			sawDifferentOrder = true
			break
		}
	}
	if !sawDifferentOrder {
		t.Errorf("same group accounts should be shuffled")
	}
}

func TestShuffleWithinPriorityAndLastUsed_DifferentPriorityPreserved(t *testing.T) {
	accounts := []*Account{
		{ID: 1, Priority: 1, LastUsedAt: nil},
		{ID: 2, Priority: 2, LastUsedAt: nil},
	}
	for i := 0; i < 100; i++ {
		shuffleWithinPriorityAndLastUsed(accounts)
		if accounts[0].ID != 1 || accounts[1].ID != 2 {
			t.Fatalf("different priority accounts should not be shuffled")
		}
	}
}

func TestSameLastUsedAt(t *testing.T) {
	now := time.Now()
	sameSecond := now.Truncate(time.Second)
	sameSecondPlus := sameSecond.Add(500 * time.Millisecond)
	differentSecond := sameSecond.Add(2 * time.Second)

	tests := []struct {
		name string
		a, b *time.Time
		want bool
	}{
		{"both nil", nil, nil, true},
		{"a nil", nil, &sameSecond, false},
		{"b nil", &sameSecond, nil, false},
		{"same second", &sameSecond, &sameSecondPlus, true},
		{"different second", &sameSecond, &differentSecond, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameLastUsedAt(tt.a, tt.b); got != tt.want {
				t.Errorf("sameLastUsedAt() = %v, want %v", got, tt.want)
			}
		})
	}
}
