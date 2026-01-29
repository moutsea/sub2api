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

// CacheTracker tracks cache keys with TTL to predict Anthropic prompt cache hits.
// It maintains a map of cache keys to their last seen time, allowing prediction
// of whether a request will hit the upstream prompt cache.
type CacheTracker struct {
	mu            sync.RWMutex
	seen          map[string]time.Time
	ttl           time.Duration
	cleanupTicker *time.Ticker
	stopCh        chan struct{}
}

// GlobalCacheTracker is the global instance used for cache hit prediction
var GlobalCacheTracker *CacheTracker

func init() {
	GlobalCacheTracker = NewCacheTracker(DefaultCacheTTL)
}

// NewCacheTracker creates a new CacheTracker with the specified TTL
func NewCacheTracker(ttl time.Duration) *CacheTracker {
	ct := &CacheTracker{
		seen:   make(map[string]time.Time),
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

// CheckAndMark checks if a cache key was seen within TTL and marks it as seen.
// Returns true if the key was seen within TTL (cache hit prediction),
// false otherwise (cache miss prediction).
// The TTL is refreshed on each call, matching Anthropic's cache behavior.
func (ct *CacheTracker) CheckAndMark(key string) bool {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	now := time.Now()
	if lastSeen, exists := ct.seen[key]; exists {
		if now.Sub(lastSeen) <= ct.ttl {
			ct.seen[key] = now // Refresh TTL
			return true        // Cache hit
		}
	}
	ct.seen[key] = now
	return false // Cache miss
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
	for key, lastSeen := range ct.seen {
		if now.Sub(lastSeen) >= ct.ttl {
			delete(ct.seen, key)
		}
	}
}

// Stop stops the background cleanup goroutine
func (ct *CacheTracker) Stop() {
	close(ct.stopCh)
	ct.cleanupTicker.Stop()
}

// Size returns the current number of tracked cache keys (for testing/debugging)
func (ct *CacheTracker) Size() int {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return len(ct.seen)
}
