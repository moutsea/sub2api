// Package kiro provides cache tracking utilities for predicting Anthropic prompt cache hits.
package kiro

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

const (
	// DefaultCacheTTL is Anthropic's prompt cache TTL (5 minutes)
	DefaultCacheTTL = 5 * time.Minute
	// CacheCleanupInterval is how often to clean up expired cache entries
	CacheCleanupInterval = 1 * time.Minute
)

// cacheScope 标识一条独立的缓存预测轨迹。
//
// 带上 accountID 很关键：failover 到另一个账号后，上游缓存是各自独立的，
// 共用一个键会让新账号的首次请求被误判为命中。
type cacheScope struct {
	accountID int64
	model     string
	sessionID string
	clientKey string
}

// prefixCacheEntry 记录某个 scope 上一次请求的可缓存前缀。
//
// 只存"历史消息条数 + 该条数处的滚动链哈希"，而不是每条消息一个哈希。
// chainDigests 是滚动哈希：位置 k 的摘要已经蕴含 m0..mk，所以
// (historyCount, chainHash) 足以判定前缀关系 —— 内存从 O(消息数) 降到 O(1)。
// 400 条消息的会话此前每个条目要占约 32KB，现在是固定 32 字节。
type prefixCacheEntry struct {
	lastSeen        time.Time
	stableHash      [sha256.Size]byte
	historyCount    int
	chainHash       [sha256.Size]byte
	cacheableTokens int
	sequence        uint64
}

// CacheResult holds the result of a cache check, providing the previous
// cacheable token count so callers can compute cache_read vs cache_creation.
type CacheResult struct {
	Hit        bool // Whether the cache key was seen within TTL
	PrevTokens int  // Previous cacheable token count (0 on miss)

	tracker *CacheTracker
	scope   cacheScope
	// valid 区分"未参与前缀缓存"（零值 CacheResult）和"参与了但未命中"。
	valid           bool
	stableHash      [sha256.Size]byte
	historyCount    int
	chainHash       [sha256.Size]byte
	cacheableTokens int
	sequence        uint64
}

// CacheTracker tracks cache keys with TTL to predict Anthropic prompt cache hits.
// It maintains a map of cache keys to their last seen time and cacheable token count,
// allowing prediction of whether a request will hit the upstream prompt cache
// and how tokens should be split between cache_read and cache_creation.
type CacheTracker struct {
	mu             sync.RWMutex
	prefixSeen     map[cacheScope]prefixCacheEntry
	prefixSequence uint64
	ttl            time.Duration
	cleanupTicker  *time.Ticker
	stopCh         chan struct{}
	stopOnce       sync.Once
}

// GlobalCacheTracker is the global instance used for cache hit prediction
var GlobalCacheTracker *CacheTracker

func init() {
	GlobalCacheTracker = NewCacheTracker(DefaultCacheTTL)
}

// NewCacheTracker creates a new CacheTracker with the specified TTL
func NewCacheTracker(ttl time.Duration) *CacheTracker {
	ct := &CacheTracker{
		prefixSeen: make(map[cacheScope]prefixCacheEntry),
		ttl:        ttl,
		stopCh:     make(chan struct{}),
	}
	ct.cleanupTicker = time.NewTicker(CacheCleanupInterval)
	go ct.cleanupLoop()
	return ct
}

type CacheScope struct {
	AccountID int64
	Model     string
	SessionID string
	ClientKey string
}

// BeginPrefix 判定本次请求的可缓存前缀是否已在上游缓存中，但**不**写入缓存状态。
// 调用方必须在请求成功后显式调用 Commit()，避免失败请求污染预测。
func (ct *CacheTracker) BeginPrefix(scope CacheScope, req *ClaudeRequest, estimation CacheEstimation) CacheResult {
	if req == nil || !estimation.MeetsCacheThreshold {
		return CacheResult{}
	}
	stableHash, chain, ok := cachePrefixHashes(req)
	if !ok {
		return CacheResult{}
	}
	clientKey := ""
	if scope.ClientKey != "" {
		clientDigest := sha256.Sum256([]byte(scope.ClientKey))
		clientKey = hex.EncodeToString(clientDigest[:])
	}
	internalScope := cacheScope{accountID: scope.AccountID, model: scope.Model, sessionID: scope.SessionID, clientKey: clientKey}

	historyCount := len(chain)
	result := CacheResult{
		tracker:         ct,
		scope:           internalScope,
		valid:           true,
		stableHash:      stableHash,
		historyCount:    historyCount,
		cacheableTokens: estimation.CacheableTokens,
	}
	if historyCount > 0 {
		result.chainHash = chain[historyCount-1]
	}

	ct.mu.Lock()
	defer ct.mu.Unlock()
	ct.prefixSequence++
	result.sequence = ct.prefixSequence

	if entry, found := ct.prefixSeen[internalScope]; found &&
		time.Since(entry.lastSeen) <= ct.ttl &&
		entry.stableHash == stableHash &&
		chainHasPrefix(chain, entry.historyCount, entry.chainHash) {
		result.Hit = true
		result.PrevTokens = entry.cacheableTokens
	}
	return result
}

// Commit 把本次请求的前缀写入缓存状态，表示上游已经真的处理过它。
func (r CacheResult) Commit() {
	if r.tracker == nil || !r.valid {
		return
	}
	r.tracker.mu.Lock()
	defer r.tracker.mu.Unlock()
	// 并发下允许乱序完成：已有更新的条目时不要用旧请求覆盖。
	// prefixSequence 是全局单调计数器，同一 scope 内后发起的请求必然序号更大。
	if current, ok := r.tracker.prefixSeen[r.scope]; ok && current.sequence > r.sequence {
		return
	}
	r.tracker.prefixSeen[r.scope] = prefixCacheEntry{
		lastSeen:        time.Now(),
		stableHash:      r.stableHash,
		historyCount:    r.historyCount,
		chainHash:       r.chainHash,
		cacheableTokens: r.cacheableTokens,
		sequence:        r.sequence,
	}
}

// chainHasPrefix 判断已存的 (count, hash) 是否是当前 chain 的前缀。
//
// 因为 chain 是滚动哈希（位置 k 的摘要蕴含 m0..mk），只需比较当前 chain 在
// 同一位置的摘要即可断定前面所有消息都一致 —— 无需逐条比对。
// prevCount == 0 表示上一次请求没有历史消息（只有 system+tools），
// 空前缀对任意 chain 都成立，这与 CacheableTokens 的口径一致：
// 那次请求可缓存的部分本就只有 system+tools，而它确实已被上游缓存。
func chainHasPrefix(chain [][sha256.Size]byte, prevCount int, prevHash [sha256.Size]byte) bool {
	if prevCount == 0 {
		return true
	}
	if prevCount > len(chain) {
		return false
	}
	return chain[prevCount-1] == prevHash
}

// cachePrefixHashes 计算稳定前缀（system+tools）摘要，以及历史消息的滚动链摘要。
//
// 返回的 chain 长度为 len(Messages)-1：最后一条消息不可缓存，与 EstimateCache
// 的口径保持一致。chain 只在本次调用期间存活，条目里只保留最后一个摘要。
func cachePrefixHashes(req *ClaudeRequest) (stableHash [sha256.Size]byte, chain [][sha256.Size]byte, ok bool) {
	stable, err := json.Marshal(struct {
		System any          `json:"system"`
		Tools  []ClaudeTool `json:"tools"`
	}{System: req.System, Tools: req.Tools})
	if err != nil {
		return stableHash, nil, false
	}
	stableHash = sha256.Sum256(stable)

	historyLen := len(req.Messages) - 1
	if historyLen <= 0 {
		return stableHash, nil, true
	}

	chain = make([][sha256.Size]byte, 0, historyLen)
	hasher := sha256.New()
	var running [sha256.Size]byte
	for _, message := range req.Messages[:historyLen] {
		// json.Marshal 对 map 键已是确定性排序，且被哈希的类型里没有
		// json.RawMessage，所以单次 marshal 已经足够规范化 —— 之前的
		// marshal→decode→marshal round-trip 是纯开销（实测约 7.7 倍）。
		encoded, err := json.Marshal(message)
		if err != nil {
			return stableHash, nil, false
		}
		hasher.Reset()
		hasher.Write(running[:])
		hasher.Write(encoded)
		// 写入独立变量再赋值，避免 Sum 的目标切片与刚被 Write 读取的 running
		// 共享底层数组（虽然当前 sha256 实现下安全，但不值得依赖这个细节）。
		var next [sha256.Size]byte
		hasher.Sum(next[:0])
		running = next
		chain = append(chain, running)
	}
	return stableHash, chain, true
}

// cleanupLoop runs the background cleanup goroutine
func (ct *CacheTracker) cleanupLoop() {
	for {
		select {
		case <-ct.stopCh:
			return
		case <-ct.cleanupTicker.C:
			ct.cleanup()
		}
	}
}

// cleanup removes expired entries from the cache
func (ct *CacheTracker) cleanup() {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	now := time.Now()
	for key, entry := range ct.prefixSeen {
		if now.Sub(entry.lastSeen) >= ct.ttl {
			delete(ct.prefixSeen, key)
		}
	}
}

// Stop stops the background cleanup goroutine
func (ct *CacheTracker) Stop() {
	ct.stopOnce.Do(func() {
		close(ct.stopCh)
		ct.cleanupTicker.Stop()
	})
}

// Size returns the current number of tracked prefix scopes (for testing/debugging).
//
// 之前这里统计的是已废弃的 seen map。切到前缀缓存后 seen 不再被写入，
// Size() 会恒返回 0 —— 如果被接到监控上就是一条永远为零的假指标。
func (ct *CacheTracker) Size() int {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return len(ct.prefixSeen)
}
