package kiro

import (
	"strings"
	"testing"
)

func TestCacheTrackerPrefixRequiresStableAndHistoryPrefix(t *testing.T) {
	tracker := NewCacheTracker(DefaultCacheTTL)
	defer tracker.Stop()
	scope := CacheScope{AccountID: 7, Model: "claude-opus-4-8", SessionID: "session"}
	base := &ClaudeRequest{
		System: "system prompt with enough content to be cacheable",
		Messages: []ClaudeMessage{
			{Role: "user", Content: "first user message"},
			{Role: "assistant", Content: "first response"},
			{Role: "user", Content: "second user message"},
		},
	}
	base.System = strings.Repeat("system prompt ", 600)
	estimation := EstimateCache(base)
	if !estimation.MeetsCacheThreshold {
		t.Fatal("test request should meet cache threshold")
	}
	first := tracker.BeginPrefix(scope, base, estimation)
	if first.Hit {
		t.Fatal("first request must miss")
	}
	first.Commit()

	appended := &ClaudeRequest{
		System:   base.System,
		Messages: append(append([]ClaudeMessage(nil), base.Messages...), ClaudeMessage{Role: "assistant", Content: "second response"}, ClaudeMessage{Role: "user", Content: "third user message"}),
	}
	appendedEstimation := EstimateCache(appended)
	hit := tracker.BeginPrefix(scope, appended, appendedEstimation)
	if !hit.Hit || hit.PrevTokens != estimation.CacheableTokens {
		t.Fatalf("appended request should hit previous prefix: %+v", hit)
	}
	hit.Commit()

	changed := &ClaudeRequest{System: base.System, Messages: append([]ClaudeMessage(nil), appended.Messages...)}
	changed.Messages[0].Content = "changed first user message"
	changedResult := tracker.BeginPrefix(scope, changed, EstimateCache(changed))
	if changedResult.Hit {
		t.Fatal("changed history must miss")
	}
	stableChanged := &ClaudeRequest{System: strings.Repeat("system prompt ", 600) + " changed", Messages: append([]ClaudeMessage(nil), appended.Messages...)}
	stableChangedResult := tracker.BeginPrefix(scope, stableChanged, EstimateCache(stableChanged))
	if stableChangedResult.Hit {
		t.Fatal("changed stable prefix must miss")
	}

	otherAccount := tracker.BeginPrefix(CacheScope{AccountID: 8, Model: scope.Model, SessionID: scope.SessionID}, appended, appendedEstimation)
	if otherAccount.Hit {
		t.Fatal("different accounts must not share cache")
	}
}

func TestCacheTrackerPrefixIsCommittedOnlyExplicitly(t *testing.T) {
	tracker := NewCacheTracker(DefaultCacheTTL)
	defer tracker.Stop()
	scope := CacheScope{AccountID: 1, Model: "model", SessionID: "session"}
	req := &ClaudeRequest{System: strings.Repeat("system prompt ", 600), Messages: []ClaudeMessage{{Role: "user", Content: "request"}}}
	estimation := EstimateCache(req)
	first := tracker.BeginPrefix(scope, req, estimation)
	if first.Hit {
		t.Fatal("first request must miss")
	}
	second := tracker.BeginPrefix(scope, req, estimation)
	if second.Hit {
		t.Fatal("uncommitted request must not warm cache")
	}
	first.Commit()
	third := tracker.BeginPrefix(scope, req, estimation)
	if !third.Hit {
		t.Fatal("committed request should warm cache")
	}
}
