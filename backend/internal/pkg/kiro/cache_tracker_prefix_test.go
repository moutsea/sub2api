package kiro

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// newCacheableRequest 构造一个满足缓存阈值的请求。
// system 部分足够大，保证 MeetsCacheThreshold 为真。
func newCacheableRequest(messages ...ClaudeMessage) *ClaudeRequest {
	return &ClaudeRequest{
		System:   strings.Repeat("system prompt ", 600),
		Messages: messages,
	}
}

func userMsg(content string) ClaudeMessage {
	return ClaudeMessage{Role: "user", Content: content}
}

func assistantMsg(content string) ClaudeMessage {
	return ClaudeMessage{Role: "assistant", Content: content}
}

// TestCacheTrackerPrefixExpiresAfterTTL 验证超过 TTL 后不再命中。
//
// 上游 prompt cache 的 TTL 是 5 分钟，过期后前缀已不在上游缓存里；
// 若我们仍报命中，就会把本该计入 cache_creation 的 token 记成 cache_read，
// 少收用户的钱，并让 usage 与上游实际不一致。
func TestCacheTrackerPrefixExpiresAfterTTL(t *testing.T) {
	tracker := NewCacheTracker(40 * time.Millisecond)
	defer tracker.Stop()

	scope := CacheScope{AccountID: 1, Model: "claude-opus-4-8", SessionID: "s"}
	req := newCacheableRequest(userMsg("one"), assistantMsg("two"), userMsg("three"))
	estimation := EstimateCache(req)
	if !estimation.MeetsCacheThreshold {
		t.Fatal("fixture must meet cache threshold")
	}

	tracker.BeginPrefix(scope, req, estimation).Commit()

	// TTL 内：命中
	if got := tracker.BeginPrefix(scope, req, estimation); !got.Hit {
		t.Fatal("expected hit within TTL")
	}

	time.Sleep(80 * time.Millisecond)

	if got := tracker.BeginPrefix(scope, req, estimation); got.Hit {
		t.Fatal("expected miss after TTL expiry")
	}
}

// TestCacheTrackerCleanupRemovesExpiredPrefixEntries 验证后台清理会回收过期条目。
// 每个条目常驻内存，不回收会随会话数无界增长。
func TestCacheTrackerCleanupRemovesExpiredPrefixEntries(t *testing.T) {
	tracker := NewCacheTracker(20 * time.Millisecond)
	defer tracker.Stop()

	req := newCacheableRequest(userMsg("a"), assistantMsg("b"), userMsg("c"))
	estimation := EstimateCache(req)
	for i := int64(0); i < 5; i++ {
		scope := CacheScope{AccountID: i, Model: "m", SessionID: "s"}
		tracker.BeginPrefix(scope, req, estimation).Commit()
	}
	if got := tracker.Size(); got != 5 {
		t.Fatalf("Size() = %d, want 5 tracked scopes", got)
	}

	time.Sleep(40 * time.Millisecond)
	tracker.cleanup()

	if got := tracker.Size(); got != 0 {
		t.Fatalf("Size() = %d after cleanup, want 0", got)
	}
}

// TestCacheTrackerSizeReflectsPrefixEntries 验证 Size() 统计的是前缀缓存条目。
//
// 回归目标：Size() 曾统计已废弃的 seen map。切到前缀缓存后 seen 不再被写入，
// Size() 恒为 0 —— 接到监控上就是一条永远为零的假指标。
func TestCacheTrackerSizeReflectsPrefixEntries(t *testing.T) {
	tracker := NewCacheTracker(DefaultCacheTTL)
	defer tracker.Stop()

	if got := tracker.Size(); got != 0 {
		t.Fatalf("fresh tracker Size() = %d, want 0", got)
	}

	req := newCacheableRequest(userMsg("a"), assistantMsg("b"), userMsg("c"))
	estimation := EstimateCache(req)
	tracker.BeginPrefix(CacheScope{AccountID: 1, Model: "m", SessionID: "s"}, req, estimation).Commit()

	if got := tracker.Size(); got != 1 {
		t.Fatalf("Size() = %d after one commit, want 1", got)
	}
}

// TestCacheTrackerStaleCommitDoesNotOverwriteNewer 验证乱序完成时，
// 旧请求不会覆盖新请求已写入的前缀。
//
// 同一会话的并发请求可能乱序返回。若旧请求的 Commit 覆盖了新请求的条目，
// 缓存状态会回退到更短的前缀，导致后续请求少报 cache_read。
func TestCacheTrackerStaleCommitDoesNotOverwriteNewer(t *testing.T) {
	tracker := NewCacheTracker(DefaultCacheTTL)
	defer tracker.Stop()

	scope := CacheScope{AccountID: 1, Model: "m", SessionID: "s"}
	shortReq := newCacheableRequest(userMsg("a"), assistantMsg("b"), userMsg("c"))
	longReq := newCacheableRequest(
		userMsg("a"), assistantMsg("b"), userMsg("c"), assistantMsg("d"), userMsg("e"),
	)

	shortEstimation := EstimateCache(shortReq)
	longEstimation := EstimateCache(longReq)
	if longEstimation.CacheableTokens <= shortEstimation.CacheableTokens {
		t.Fatal("fixture: long request must have more cacheable tokens")
	}

	// 先后发起，后者序号更大
	older := tracker.BeginPrefix(scope, shortReq, shortEstimation)
	newer := tracker.BeginPrefix(scope, longReq, longEstimation)

	// 乱序完成：新的先落，旧的后落
	newer.Commit()
	older.Commit()

	// 从公开行为验证：再发一次长前缀请求，PrevTokens 应反映**较新**那次
	// （较长）的可缓存量。若旧 Commit 覆盖成功，这里会拿到较短那次的值。
	probe := tracker.BeginPrefix(scope, longReq, longEstimation)
	if !probe.Hit {
		t.Fatal("expected hit after commits")
	}
	if probe.PrevTokens != longEstimation.CacheableTokens {
		t.Fatalf("PrevTokens = %d, want %d (stale commit must not roll the entry back)",
			probe.PrevTokens, longEstimation.CacheableTokens)
	}
}

// TestCacheTrackerConcurrentBeginCommitIsRaceFree 在 -race 下压并发路径。
// 同时验证并发结束后条目仍然自洽（能被后续同前缀请求命中）。
func TestCacheTrackerConcurrentBeginCommitIsRaceFree(t *testing.T) {
	tracker := NewCacheTracker(DefaultCacheTTL)
	defer tracker.Stop()

	req := newCacheableRequest(userMsg("a"), assistantMsg("b"), userMsg("c"))
	estimation := EstimateCache(req)

	const goroutines = 32
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// 混用多个 scope 与同一 scope，覆盖 map 写入竞争
			scope := CacheScope{AccountID: int64(i % 4), Model: "m", SessionID: "s"}
			tracker.BeginPrefix(scope, req, estimation).Commit()
		}(i)
	}
	wg.Wait()

	if got := tracker.Size(); got != 4 {
		t.Fatalf("Size() = %d, want 4 distinct scopes", got)
	}
	got := tracker.BeginPrefix(CacheScope{AccountID: 0, Model: "m", SessionID: "s"}, req, estimation)
	if !got.Hit {
		t.Fatal("expected hit after concurrent commits")
	}
}

// TestCacheTrackerTruncatedHistoryMisses 验证客户端截断会话后不再命中。
// 上一次记录的前缀比当前更长，说明当前请求不是它的延续，必须保守地判为未命中。
func TestCacheTrackerTruncatedHistoryMisses(t *testing.T) {
	tracker := NewCacheTracker(DefaultCacheTTL)
	defer tracker.Stop()

	scope := CacheScope{AccountID: 1, Model: "m", SessionID: "s"}
	long := newCacheableRequest(
		userMsg("a"), assistantMsg("b"), userMsg("c"), assistantMsg("d"), userMsg("e"),
	)
	tracker.BeginPrefix(scope, long, EstimateCache(long)).Commit()

	truncated := newCacheableRequest(userMsg("a"), assistantMsg("b"))
	if got := tracker.BeginPrefix(scope, truncated, EstimateCache(truncated)); got.Hit {
		t.Fatal("truncated conversation must not hit a longer stored prefix")
	}
}

// TestCacheTrackerSessionAndClientKeyIsolation 验证 sessionID / clientKey 参与隔离。
// 不同客户端或会话各自独立，避免一个客户端的前缀被另一个误判为已缓存。
func TestCacheTrackerSessionAndClientKeyIsolation(t *testing.T) {
	tracker := NewCacheTracker(DefaultCacheTTL)
	defer tracker.Stop()

	req := newCacheableRequest(userMsg("a"), assistantMsg("b"), userMsg("c"))
	estimation := EstimateCache(req)
	base := CacheScope{AccountID: 1, Model: "m", SessionID: "s1", ClientKey: "k1"}
	tracker.BeginPrefix(base, req, estimation).Commit()

	otherSession := base
	otherSession.SessionID = "s2"
	if got := tracker.BeginPrefix(otherSession, req, estimation); got.Hit {
		t.Fatal("different session must not share cache state")
	}

	otherClient := base
	otherClient.ClientKey = "k2"
	if got := tracker.BeginPrefix(otherClient, req, estimation); got.Hit {
		t.Fatal("different client key must not share cache state")
	}

	otherModel := base
	otherModel.Model = "other-model"
	if got := tracker.BeginPrefix(otherModel, req, estimation); got.Hit {
		t.Fatal("different model must not share cache state")
	}
}

// TestCacheTrackerBelowThresholdIsInert 验证不满足缓存阈值的请求完全不参与，
// 且对其调用 Commit() 是安全的空操作（不会写入脏条目）。
func TestCacheTrackerBelowThresholdIsInert(t *testing.T) {
	tracker := NewCacheTracker(DefaultCacheTTL)
	defer tracker.Stop()

	small := &ClaudeRequest{System: "tiny", Messages: []ClaudeMessage{userMsg("hi")}}
	estimation := EstimateCache(small)
	if estimation.MeetsCacheThreshold {
		t.Skip("fixture unexpectedly meets threshold")
	}

	result := tracker.BeginPrefix(CacheScope{AccountID: 1, Model: "m", SessionID: "s"}, small, estimation)
	if result.Hit {
		t.Fatal("below-threshold request must not report a hit")
	}
	result.Commit() // 必须是安全的空操作

	if got := tracker.Size(); got != 0 {
		t.Fatalf("Size() = %d, want 0 (below-threshold requests must not be tracked)", got)
	}
}

// TestCachePrefixHashesChainIsPrefixStable 验证滚动链的核心性质：
// 追加消息不会改变已有位置的摘要。这是"只存最后一个摘要"能替代整条历史的前提。
func TestCachePrefixHashesChainIsPrefixStable(t *testing.T) {
	base := newCacheableRequest(userMsg("a"), assistantMsg("b"), userMsg("c"))
	extended := newCacheableRequest(
		userMsg("a"), assistantMsg("b"), userMsg("c"), assistantMsg("d"), userMsg("e"),
	)

	baseStable, baseChain, ok := cachePrefixHashes(base)
	if !ok {
		t.Fatal("hashing base failed")
	}
	extStable, extChain, ok := cachePrefixHashes(extended)
	if !ok {
		t.Fatal("hashing extended failed")
	}

	if baseStable != extStable {
		t.Fatal("stable hash must not change when only messages are appended")
	}
	if len(extChain) <= len(baseChain) {
		t.Fatalf("extended chain len = %d, want > %d", len(extChain), len(baseChain))
	}
	for i := range baseChain {
		if baseChain[i] != extChain[i] {
			t.Fatalf("chain digest at %d changed after append; rolling-hash prefix property broken", i)
		}
	}

	// 改动历史中的任意一条，其后所有摘要都必须变化
	mutated := newCacheableRequest(
		userMsg("a"), assistantMsg("CHANGED"), userMsg("c"), assistantMsg("d"), userMsg("e"),
	)
	_, mutChain, ok := cachePrefixHashes(mutated)
	if !ok {
		t.Fatal("hashing mutated failed")
	}
	if mutChain[0] != extChain[0] {
		t.Fatal("digest before the mutation point should be unchanged")
	}
	if mutChain[1] == extChain[1] {
		t.Fatal("digest at the mutation point must change")
	}
}
