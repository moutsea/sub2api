// Package kiro provides cache tracking utilities for predicting Anthropic prompt cache hits.
package kiro

import (
	"crypto/md5"
	"encoding/hex"
	"sync"
	"time"
)

const (
	// DefaultCacheTTL is Anthropic's prompt cache TTL (5 minutes)
	DefaultCacheTTL = 5 * time.Minute
	// CacheCleanupInterval is how often to clean up expired cache entries
	CacheCleanupInterval = 1 * time.Minute
)

// cacheEntry stores the last seen time and the cacheable token count for a cache key.
type cacheEntry struct {
	lastSeen       time.Time
	cacheableTokens int
}

// CacheResult holds the result of a cache check, providing the previous
// cacheable token count so callers can compute cache_read vs cache_creation.
type CacheResult struct {
	Hit            bool // Whether the cache key was seen within TTL
	PrevTokens     int  // Previous cacheable token count (0 on miss)
}

// CacheTracker tracks cache keys with TTL to predict Anthropic prompt cache hits.
// It maintains a map of cache keys to their last seen time and cacheable token count,
// allowing prediction of whether a request will hit the upstream prompt cache
// and how tokens should be split between cache_read and cache_creation.
type CacheTracker struct {
	mu            sync.RWMutex
	seen          map[string]cacheEntry
	ttl           time.Duration
	cleanupTicker *time.Ticker
	stopCh        chan struct{}
	stopOnce      sync.Once
}

// GlobalCacheTracker is the global instance used for cache hit prediction
var GlobalCacheTracker *CacheTracker

func init() {
	GlobalCacheTracker = NewCacheTracker(DefaultCacheTTL)
}

// NewCacheTracker creates a new CacheTracker with the specified TTL
func NewCacheTracker(ttl time.Duration) *CacheTracker {
	ct := &CacheTracker{
		seen:   make(map[string]cacheEntry),
		ttl:    ttl,
		stopCh: make(chan struct{}),
	}
	ct.cleanupTicker = time.NewTicker(CacheCleanupInterval)
	go ct.cleanupLoop()
	return ct
}

// GenerateCacheKey creates an MD5 hash from system prompt + tools JSON.
// This key uniquely identifies the cacheable portion of a request.
func GenerateCacheKey(systemPrompt string, toolsJSON string) string {
	h := md5.New()
	h.Write([]byte(systemPrompt))
	h.Write([]byte("|"))
	h.Write([]byte(toolsJSON))
	return hex.EncodeToString(h.Sum(nil))
}

// CheckAndMark checks if a cache key was seen within TTL, returns the previous
// cacheable token count, and updates the entry with the current token count.
//
// On cache hit: returns CacheResult{Hit: true, PrevTokens: <previous count>}
// On cache miss: returns CacheResult{Hit: false, PrevTokens: 0}
//
// The TTL is refreshed on each call, matching Anthropic's cache behavior.
func (ct *CacheTracker) CheckAndMark(key string, cacheableTokens int) CacheResult {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	now := time.Now()
	if entry, exists := ct.seen[key]; exists {
		if now.Sub(entry.lastSeen) <= ct.ttl {
			prevTokens := entry.cacheableTokens
			ct.seen[key] = cacheEntry{lastSeen: now, cacheableTokens: cacheableTokens}
			return CacheResult{Hit: true, PrevTokens: prevTokens}
		}
	}
	ct.seen[key] = cacheEntry{lastSeen: now, cacheableTokens: cacheableTokens}
	return CacheResult{Hit: false, PrevTokens: 0}
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
	for key, entry := range ct.seen {
		if now.Sub(entry.lastSeen) >= ct.ttl {
			delete(ct.seen, key)
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

// Size returns the current number of tracked cache keys (for testing/debugging)
func (ct *CacheTracker) Size() int {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return len(ct.seen)
}
