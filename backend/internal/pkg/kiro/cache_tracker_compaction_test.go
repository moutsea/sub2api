package kiro

import (
	"strings"
	"testing"
)

// TestCompactionScenarioKeepsStableCredit 模拟真实的上下文压缩场景，
// 量化"历史分叉时保留稳定前缀额度"带来的计费差异。
//
// 背景：客户端（如 Claude Code）在会话变长后会压缩上下文 —— 保留 system+tools，
// 但把中间历史换成摘要。此时上游 prompt cache 里 system+tools 这段前缀仍然是热的，
// 只有分叉点之后失效。若整体判未命中，用户会为同一份 system prompt 反复付全价。
func TestCompactionScenarioKeepsStableCredit(t *testing.T) {
	tracker := NewCacheTracker(DefaultCacheTTL)
	defer tracker.Stop()
	scope := CacheScope{AccountID: 1, Model: "claude-opus-4-8", SessionID: "s", ClientKey: "k"}

	system := strings.Repeat("you are a helpful coding assistant with detailed instructions ", 200)
	tools := []ClaudeTool{{
		Name:        "read_file",
		Description: strings.Repeat("reads a file from disk ", 50),
		InputSchema: map[string]any{"type": "object"},
	}}

	// 一轮长会话
	long := &ClaudeRequest{System: system, Tools: tools}
	for i := 0; i < 12; i++ {
		long.Messages = append(long.Messages, ClaudeMessage{
			Role:    map[bool]string{true: "user", false: "assistant"}[i%2 == 0],
			Content: strings.Repeat("conversation turn content ", 40),
		})
	}
	longEst := EstimateCache(long)
	if !longEst.MeetsCacheThreshold {
		t.Fatal("fixture must meet cache threshold")
	}
	tracker.BeginPrefix(scope, long, longEst).Commit()

	// 客户端压缩：system+tools 不变，历史被摘要替换
	compacted := &ClaudeRequest{
		System: system,
		Tools:  tools,
		Messages: []ClaudeMessage{
			{Role: "user", Content: strings.Repeat("summary of earlier conversation ", 30)},
			{Role: "assistant", Content: "understood"},
			{Role: "user", Content: "next question"},
		},
	}
	compactedEst := EstimateCache(compacted)
	got := tracker.BeginPrefix(scope, compacted, compactedEst)

	if !got.Hit || got.HitKind != CacheHitStable {
		t.Fatalf("compaction should keep a stable-level hit, got Hit=%v kind=%q", got.Hit, got.HitKind)
	}

	// 对比两种计费口径下的 cache_creation（越低越省钱）
	withCredit, creationWithCredit := compactedEst.SplitCacheTokens(got, 0)
	fullMiss := CacheResult{} // 旧行为：整体未命中
	_, creationFullMiss := compactedEst.SplitCacheTokens(fullMiss, 0)

	if withCredit <= 0 {
		t.Fatalf("expected non-zero cache_read credit, got %d", withCredit)
	}
	if creationWithCredit >= creationFullMiss {
		t.Fatalf("cache_creation should drop: with_credit=%d full_miss=%d", creationWithCredit, creationFullMiss)
	}

	saved := creationFullMiss - creationWithCredit
	pct := float64(saved) / float64(creationFullMiss) * 100
	t.Logf("compaction billing: cache_creation %d -> %d (cache_read=%d, %.0f%% of cacheable moved to the cheaper tier)",
		creationFullMiss, creationWithCredit, withCredit, pct)

	// 稳定前缀应当占可缓存量的可观比例，否则这个修复没有实际意义
	if pct < 10 {
		t.Fatalf("stable credit only moved %.1f%% of tokens; fixture may not represent a real system prompt", pct)
	}
}

// TestStableCreditNeverExceedsPreviousCacheable 验证 credit 不会超过上一次真正
// 缓存过的量 —— 否则会声称上游缓存了它其实没缓存的 token（少收用户的钱）。
func TestStableCreditNeverExceedsPreviousCacheable(t *testing.T) {
	tracker := NewCacheTracker(DefaultCacheTTL)
	defer tracker.Stop()
	scope := CacheScope{AccountID: 1, Model: "m", SessionID: "s"}

	system := strings.Repeat("system prompt ", 600)

	// 上一次：只有 system + 一条消息（可缓存量几乎只有 system）
	first := &ClaudeRequest{System: system, Messages: []ClaudeMessage{userMsg("q1")}}
	firstEst := EstimateCache(first)
	tracker.BeginPrefix(scope, first, firstEst).Commit()

	// 本次：历史分叉，且本次 StableTokens 与上次相同
	forked := &ClaudeRequest{
		System:   system,
		Messages: []ClaudeMessage{userMsg("totally different"), assistantMsg("x"), userMsg("q2")},
	}
	forkedEst := EstimateCache(forked)
	got := tracker.BeginPrefix(scope, forked, forkedEst)

	if got.PrevTokens > firstEst.CacheableTokens {
		t.Fatalf("credit %d exceeds what was previously cacheable %d", got.PrevTokens, firstEst.CacheableTokens)
	}
	if got.PrevTokens > forkedEst.CacheableTokens {
		t.Fatalf("credit %d exceeds current cacheable %d", got.PrevTokens, forkedEst.CacheableTokens)
	}
}

// TestStableCreditRequiresSameAccount 验证换账号仍然是完全未命中。
// 上游 prompt cache 是按账号隔离的，新账号上什么都没缓存，
// 给额度就是少收用户的钱且与上游 usage 不一致。
func TestStableCreditRequiresSameAccount(t *testing.T) {
	tracker := NewCacheTracker(DefaultCacheTTL)
	defer tracker.Stop()

	req := newCacheableRequest(userMsg("a"), assistantMsg("b"), userMsg("c"))
	est := EstimateCache(req)
	tracker.BeginPrefix(CacheScope{AccountID: 1, Model: "m", SessionID: "s"}, req, est).Commit()

	// 同会话、同前缀，但换了账号：必须完全未命中
	forked := newCacheableRequest(userMsg("a"), assistantMsg("CHANGED"), userMsg("c"))
	got := tracker.BeginPrefix(CacheScope{AccountID: 2, Model: "m", SessionID: "s"}, forked, EstimateCache(forked))
	if got.Hit {
		t.Fatalf("different account must be a full miss, got kind=%q credit=%d", got.HitKind, got.PrevTokens)
	}
}
