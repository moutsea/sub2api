package kiro

import (
	"fmt"
	"strings"
	"testing"
)

// buildBenchConvo 构造指定长度/单条大小的会话，用于量化前缀哈希的开销。
func buildBenchConvo(nMessages, contentBytes int) *ClaudeRequest {
	req := &ClaudeRequest{
		System:   strings.Repeat("system prompt ", 600),
		Messages: make([]ClaudeMessage, 0, nMessages),
	}
	body := strings.Repeat("x", contentBytes)
	for i := 0; i < nMessages; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		req.Messages = append(req.Messages, ClaudeMessage{Role: role, Content: body})
	}
	return req
}

// BenchmarkCachePrefixHashes 量化前缀哈希随会话长度的增长。
// 这条路径在**每个请求**上执行，长 agentic 会话下是实打实的热点。
func BenchmarkCachePrefixHashes(b *testing.B) {
	for _, n := range []int{10, 100, 400} {
		req := buildBenchConvo(n, 2000)
		b.Run(fmt.Sprintf("messages=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, _, _ = cachePrefixHashes(req)
			}
		})
	}
}

// BenchmarkBeginPrefixHit 量化完整命中路径（哈希 + 查表）的开销。
func BenchmarkBeginPrefixHit(b *testing.B) {
	tracker := NewCacheTracker(DefaultCacheTTL)
	defer tracker.Stop()
	scope := CacheScope{AccountID: 1, Model: "claude-opus-4-8", SessionID: "bench"}

	for _, n := range []int{10, 100, 400} {
		req := buildBenchConvo(n, 2000)
		estimation := EstimateCache(req)
		tracker.BeginPrefix(scope, req, estimation).Commit()
		b.Run(fmt.Sprintf("messages=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = tracker.BeginPrefix(scope, req, estimation)
			}
		})
	}
}
