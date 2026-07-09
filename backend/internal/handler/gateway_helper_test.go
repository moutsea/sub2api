package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// TestWrapReleaseOnDone_NoGoroutineLeak 验证 wrapReleaseOnDone 修复后不会泄露 goroutine
func TestWrapReleaseOnDone_NoGoroutineLeak(t *testing.T) {
	// 记录测试开始时的 goroutine 数量
	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	initialGoroutines := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var releaseCount int32
	release := wrapReleaseOnDone(ctx, func() {
		atomic.AddInt32(&releaseCount, 1)
	})

	// 正常释放
	release()

	// 等待足够时间确保 goroutine 退出
	time.Sleep(200 * time.Millisecond)

	// 验证只释放一次
	if count := atomic.LoadInt32(&releaseCount); count != 1 {
		t.Errorf("expected release count to be 1, got %d", count)
	}

	// 强制 GC，清理已退出的 goroutine
	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	// 验证 goroutine 数量没有增加（允许±2的误差，考虑到测试框架本身可能创建的 goroutine）
	finalGoroutines := runtime.NumGoroutine()
	if finalGoroutines > initialGoroutines+2 {
		t.Errorf("goroutine leak detected: initial=%d, final=%d, leaked=%d",
			initialGoroutines, finalGoroutines, finalGoroutines-initialGoroutines)
	}
}

func TestApplyKiroOpus47GroupDowngrade_RewritesModelOnlyForEnabledKiroGroup(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-7","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	group := &service.Group{Platform: service.PlatformKiro, KiroOpus47Downgrade: true}

	model, rewrittenBody, downgraded, err := applyKiroOpus47GroupDowngrade(group, "claude-opus-4-7", body)
	if err != nil {
		t.Fatalf("applyKiroOpus47GroupDowngrade error: %v", err)
	}
	if !downgraded {
		t.Fatal("expected downgrade")
	}
	if model != "claude-opus-4-6" {
		t.Fatalf("model = %q, want claude-opus-4-6", model)
	}

	var payload map[string]any
	if err := json.Unmarshal(rewrittenBody, &payload); err != nil {
		t.Fatalf("rewritten body is invalid JSON: %v", err)
	}
	if payload["model"] != "claude-opus-4-6" {
		t.Fatalf("rewritten model = %v, want claude-opus-4-6", payload["model"])
	}
	if payload["stream"] != true {
		t.Fatalf("stream field was not preserved: %v", payload["stream"])
	}

	model, rewrittenBody, downgraded, err = applyKiroOpus47GroupDowngrade(
		&service.Group{Platform: service.PlatformAnthropic, KiroOpus47Downgrade: true},
		"claude-opus-4-7",
		body,
	)
	if err != nil {
		t.Fatalf("applyKiroOpus47GroupDowngrade error: %v", err)
	}
	if downgraded {
		t.Fatal("did not expect downgrade for non-Kiro group")
	}
	if model != "claude-opus-4-7" || string(rewrittenBody) != string(body) {
		t.Fatal("non-Kiro request should remain unchanged")
	}
}

func TestApplyKiroOpus47GroupDowngrade_ReturnsErrorForInvalidJSON(t *testing.T) {
	group := &service.Group{Platform: service.PlatformKiro, KiroOpus47Downgrade: true}

	_, _, downgraded, err := applyKiroOpus47GroupDowngrade(group, "claude-opus-4-7", []byte(`{"model":`))

	if err == nil {
		t.Fatal("expected rewrite error")
	}
	if downgraded {
		t.Fatal("invalid JSON must not be reported as downgraded")
	}
}

func TestShouldStopKiroOAuthInitialFailoverBudget(t *testing.T) {
	account := &service.Account{Platform: service.PlatformKiro}
	failoverErr := &service.UpstreamFailoverError{
		StatusCode: http.StatusGatewayTimeout,
		Message:    "kiro_initial_response_timeout:first_renderable_event",
	}

	if shouldStopKiroOAuthInitialFailover(account, failoverErr, time.Now().Add(-50*time.Second)) {
		t.Fatal("should allow another retry when enough budget remains")
	}
	if !shouldStopKiroOAuthInitialFailover(account, failoverErr, time.Now().Add(-70*time.Second)) {
		t.Fatal("should stop when remaining budget is below next-attempt buffer")
	}
	if !shouldStopKiroOAuthInitialFailover(account, failoverErr, time.Now().Add(-110*time.Second)) {
		t.Fatal("should stop when total budget is exhausted")
	}
}

func TestShouldStopKiroClaudeOAuthInitialFailoverBudget(t *testing.T) {
	account := &service.Account{Platform: service.PlatformKiro}
	failoverErr := &service.UpstreamFailoverError{
		StatusCode: http.StatusGatewayTimeout,
		Message:    "kiro_initial_response_timeout:first_renderable_event",
	}

	if shouldStopKiroClaudeOAuthInitialFailover(account, failoverErr, time.Now().Add(-42*time.Second)) {
		t.Fatal("should allow a second Kiro OAuth attempt when enough 80s budget remains")
	}
	if !shouldStopKiroClaudeOAuthInitialFailover(account, failoverErr, time.Now().Add(-50*time.Second)) {
		t.Fatal("should stop when another Kiro OAuth attempt would exceed the 80s client retry deadline")
	}
	if !shouldStopKiroClaudeOAuthInitialFailover(account, failoverErr, time.Now().Add(-85*time.Second)) {
		t.Fatal("should stop when the 80s client retry deadline is exhausted")
	}
}

func TestShouldStopKiroOAuthInitialFailoverScope(t *testing.T) {
	failoverErr := &service.UpstreamFailoverError{
		StatusCode: http.StatusGatewayTimeout,
		Message:    "kiro_initial_response_timeout:first_renderable_event",
	}
	startedAt := time.Now().Add(-110 * time.Second)

	if shouldStopKiroOAuthInitialFailover(&service.Account{Platform: service.PlatformAnthropic}, failoverErr, startedAt) {
		t.Fatal("non-Kiro account should not use Kiro budget")
	}
	if shouldStopKiroOAuthInitialFailover(&service.Account{
		Platform: service.PlatformKiro,
		Credentials: map[string]any{
			"auth_type": service.KiroAuthMethodAPIKey,
		},
	}, failoverErr, startedAt) {
		t.Fatal("Kiro API key account should not use OAuth budget")
	}
	if shouldStopKiroOAuthInitialFailover(&service.Account{Platform: service.PlatformKiro}, &service.UpstreamFailoverError{
		StatusCode: http.StatusGatewayTimeout,
		Message:    "other_timeout",
	}, startedAt) {
		t.Fatal("non-initial-response timeout should not use Kiro budget")
	}
}

func TestShouldBypassKiroClaudeOAuthClientRetryForAck(t *testing.T) {
	kiroOAuth := &service.Account{Platform: service.PlatformKiro}
	timeoutErr := &service.UpstreamFailoverError{
		StatusCode: http.StatusGatewayTimeout,
		Message:    "kiro_initial_response_timeout:first_renderable_event",
	}

	if !shouldBypassKiroClaudeOAuthClientRetryForAck(kiroOAuth, timeoutErr, true) {
		t.Fatal("streaming Kiro OAuth initial timeout should bypass 80s client retry cutoff")
	}
	if shouldBypassKiroClaudeOAuthClientRetryForAck(kiroOAuth, timeoutErr, false) {
		t.Fatal("non-streaming Kiro OAuth initial timeout should keep client retry cutoff")
	}
	if shouldBypassKiroClaudeOAuthClientRetryForAck(&service.Account{Platform: service.PlatformAnthropic}, timeoutErr, true) {
		t.Fatal("non-Kiro accounts should not bypass client retry cutoff")
	}
	if shouldBypassKiroClaudeOAuthClientRetryForAck(kiroOAuth, &service.UpstreamFailoverError{
		StatusCode: http.StatusBadGateway,
		Message:    "kiro_empty_stream",
	}, true) {
		t.Fatal("non-initial-timeout failover should not bypass client retry cutoff")
	}
}

func TestGatewayFailoverExhaustedReturnsRetryableRateLimitForKiroInitialTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	handler := &GatewayHandler{}
	handler.handleFailoverExhausted(c, http.StatusGatewayTimeout, "kiro_initial_response_timeout:first_renderable_event", false)

	if got := recorder.Code; got != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", got, http.StatusTooManyRequests)
	}
	if got := recorder.Header().Get("Retry-After"); got != kiroClaudeOAuthClientRetryAfterHeader {
		t.Fatalf("Retry-After = %q, want %q", got, kiroClaudeOAuthClientRetryAfterHeader)
	}
}

func TestOpenAIGatewayFailoverExhaustedReturnsRetryableRateLimitForKiroInitialTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	handler := &OpenAIGatewayHandler{}
	handler.handleFailoverExhausted(c, http.StatusGatewayTimeout, "kiro_initial_response_timeout:first_renderable_event", false)

	if got := recorder.Code; got != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", got, http.StatusTooManyRequests)
	}
	if got := recorder.Header().Get("Retry-After"); got != kiroClaudeOAuthClientRetryAfterHeader {
		t.Fatalf("Retry-After = %q, want %q", got, kiroClaudeOAuthClientRetryAfterHeader)
	}
}

// TestWrapReleaseOnDone_ContextCancellation 验证 context 取消时也能正确释放
func TestWrapReleaseOnDone_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var releaseCount int32
	_ = wrapReleaseOnDone(ctx, func() {
		atomic.AddInt32(&releaseCount, 1)
	})

	// 取消 context，应该触发释放
	cancel()

	// 等待释放完成
	time.Sleep(100 * time.Millisecond)

	// 验证释放被调用
	if count := atomic.LoadInt32(&releaseCount); count != 1 {
		t.Errorf("expected release count to be 1, got %d", count)
	}
}

// TestWrapReleaseOnDone_MultipleCallsOnlyReleaseOnce 验证多次调用 release 只释放一次
func TestWrapReleaseOnDone_MultipleCallsOnlyReleaseOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var releaseCount int32
	release := wrapReleaseOnDone(ctx, func() {
		atomic.AddInt32(&releaseCount, 1)
	})

	// 调用多次
	release()
	release()
	release()

	// 等待执行完成
	time.Sleep(100 * time.Millisecond)

	// 验证只释放一次
	if count := atomic.LoadInt32(&releaseCount); count != 1 {
		t.Errorf("expected release count to be 1, got %d", count)
	}
}

// TestWrapReleaseOnDone_NilReleaseFunc 验证 nil releaseFunc 不会 panic
func TestWrapReleaseOnDone_NilReleaseFunc(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	release := wrapReleaseOnDone(ctx, nil)

	if release != nil {
		t.Error("expected nil release function when releaseFunc is nil")
	}
}

// TestWrapReleaseOnDone_ConcurrentCalls 验证并发调用的安全性
func TestWrapReleaseOnDone_ConcurrentCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var releaseCount int32
	release := wrapReleaseOnDone(ctx, func() {
		atomic.AddInt32(&releaseCount, 1)
	})

	// 并发调用 release
	const numGoroutines = 10
	for i := 0; i < numGoroutines; i++ {
		go release()
	}

	// 等待所有 goroutine 完成
	time.Sleep(200 * time.Millisecond)

	// 验证只释放一次
	if count := atomic.LoadInt32(&releaseCount); count != 1 {
		t.Errorf("expected release count to be 1, got %d", count)
	}
}

// BenchmarkWrapReleaseOnDone 性能基准测试
func BenchmarkWrapReleaseOnDone(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		release := wrapReleaseOnDone(ctx, func() {})
		release()
	}
}
