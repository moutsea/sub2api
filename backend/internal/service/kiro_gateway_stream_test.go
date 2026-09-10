package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
)

type synchronizedResponseRecorder struct {
	*httptest.ResponseRecorder
	mu             sync.Mutex
	firstWriteOnce sync.Once
	firstWriteCh   chan struct{}
}

func newSynchronizedResponseRecorder() *synchronizedResponseRecorder {
	return &synchronizedResponseRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		firstWriteCh:     make(chan struct{}),
	}
}

func (r *synchronizedResponseRecorder) Write(data []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	n, err := r.ResponseRecorder.Write(data)
	if n > 0 {
		r.firstWriteOnce.Do(func() {
			close(r.firstWriteCh)
		})
	}
	return n, err
}

func (r *synchronizedResponseRecorder) WriteHeader(statusCode int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ResponseRecorder.WriteHeader(statusCode)
}

func (r *synchronizedResponseRecorder) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ResponseRecorder.Flush()
}

func (r *synchronizedResponseRecorder) WaitForWrite(timeout time.Duration) bool {
	select {
	case <-r.firstWriteCh:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (r *synchronizedResponseRecorder) BodyString() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Body.String()
}

func newKiroStreamTestContext() (*gin.Context, *synchronizedResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := newSynchronizedResponseRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	return c, rec
}

func newKiroStreamHTTPResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader([]byte(body))),
		Header:     http.Header{},
	}
}

type blockingReadCloser struct {
	once   sync.Once
	closed chan struct{}
}

func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{closed: make(chan struct{})}
}

func (r *blockingReadCloser) Read(_ []byte) (int, error) {
	<-r.closed
	return 0, io.EOF
}

func (r *blockingReadCloser) Close() error {
	r.once.Do(func() {
		close(r.closed)
	})
	return nil
}

func newBlockingKiroStreamHTTPResponse(body io.ReadCloser) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       body,
		Header:     http.Header{},
	}
}

type gatedReadCloser struct {
	once     sync.Once
	release  chan struct{}
	data     []byte
	readDone bool
}

func newGatedReadCloser(data []byte) *gatedReadCloser {
	return &gatedReadCloser{
		release: make(chan struct{}),
		data:    data,
	}
}

func (r *gatedReadCloser) Read(p []byte) (int, error) {
	<-r.release
	if r.readDone {
		return 0, io.EOF
	}
	r.readDone = true
	return copy(p, r.data), nil
}

func (r *gatedReadCloser) Close() error {
	r.once.Do(func() {
		close(r.release)
	})
	return nil
}

func (r *gatedReadCloser) Release() {
	r.once.Do(func() {
		close(r.release)
	})
}

func TestKiroOAuthInitialResponseTimeoutDisabled(t *testing.T) {
	oauthAccount := &Account{Platform: PlatformKiro}
	apiKeyAccount := &Account{
		Platform: PlatformKiro,
		Credentials: map[string]any{
			"auth_type": KiroAuthMethodAPIKey,
		},
	}

	defaultSvc := &KiroGatewayService{}
	if got := defaultSvc.kiroOAuthInitialResponseTimeout(oauthAccount); got != 0 {
		t.Fatalf("default timeout = %s, want disabled", got)
	}
	if got := defaultSvc.kiroOAuthInitialResponseTimeout(apiKeyAccount); got != 0 {
		t.Fatalf("apikey timeout = %s, want disabled", got)
	}

	cappedSvc := &KiroGatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{ResponseHeaderTimeout: 30},
		},
	}
	if got := cappedSvc.kiroOAuthInitialResponseTimeout(oauthAccount); got != 0 {
		t.Fatalf("configured timeout = %s, want disabled", got)
	}
}

func TestKiroInitialResponseFailoverMessageIncludesTimeout(t *testing.T) {
	failoverErr := kiroInitialResponseFailover(" first_renderable_event ", 60*time.Second)

	if failoverErr.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d", failoverErr.StatusCode, http.StatusGatewayTimeout)
	}
	if failoverErr.Message != "kiro_initial_response_timeout:first_renderable_event:timeout=1m0s" {
		t.Fatalf("message = %q", failoverErr.Message)
	}
}

// TestKiroStreamingEmptyBodyFailsOverBeforeCommit 验证上游 200 + 空 body
// 在信封提交前就失败转移。
//
// 行为变更：以前信封在响应头到达时就提交，空流只能以 200 + SSE error 收场
// —— 客户端拿到的是一个"成功但没内容"的响应，也没有换账号重试的机会。
// 现在空流（可能是软限流）会 failover，且状态码不再是 502。
func TestKiroStreamingEmptyBodyFailsOverBeforeCommit(t *testing.T) {
	c, rec := newKiroStreamTestContext()
	svc := &KiroGatewayService{}
	resp := newKiroStreamHTTPResponse("")
	resp.Header.Set("x-amzn-requestid", "req-empty-1")

	result, err := svc.handleStreamingResponse(
		c,
		resp,
		time.Now(),
		"claude-sonnet-4-6",
		10,
		nil,
		0,
		0,
		false,
		false,
	)

	if result != nil {
		t.Fatalf("result = %+v, want nil", result)
	}
	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) {
		t.Fatalf("err = %v, want UpstreamFailoverError", err)
	}
	if failoverErr.StatusCode == http.StatusBadGateway {
		t.Fatal("status = 502; upstream returned HTTP 200 so it would have no matching record")
	}
	info := DecodeKiroFailureMessage(failoverErr.Message)
	if info == nil || info.RequestID != "req-empty-1" {
		t.Fatalf("request id not preserved: %+v", info)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("response body = %q, want empty before commit", rec.Body.String())
	}
}

// TestKiroStreamingCommitsInitialEnvelopeBeforeFirstRenderableEvent 验证上游
// 首个 token 很慢时，客户端仍能提前收到 SSE 信封（不至于在收到任何字节前
// 被 Cloudflare/客户端超时断开）。
//
// 机制变更：信封提交时机从"上游响应头一到就提交"改成"有内容时提交，
// 否则由 streamCommitDeadline 到点兜底提交"。本测试要保住的性质没变，
// 只是改为验证兜底提交这条路径（用短 deadline 以免测试等 20s）。
func TestKiroStreamingCommitsInitialEnvelopeBeforeFirstRenderableEvent(t *testing.T) {
	c, rec := newKiroStreamTestContext()
	svc := &KiroGatewayService{streamCommitDeadline: 30 * time.Millisecond}
	body := newGatedReadCloser(mustContentPayload(t, "hello"))
	defer body.Close()

	type streamResult struct {
		result *kiroStreamResult
		err    error
	}
	done := make(chan streamResult, 1)
	go func() {
		result, err := svc.handleStreamingResponseWithOptions(
			c,
			newBlockingKiroStreamHTTPResponse(body),
			time.Now(),
			"claude-opus-4-7",
			10,
			nil,
			0,
			0,
			false,
			false,
			kiroStreamOptions{},
		)
		done <- streamResult{result: result, err: err}
	}()

	// 到达提交上限后，信封先于被门控的首个 token 发出
	if !rec.WaitForWrite(500 * time.Millisecond) {
		t.Fatal("expected initial SSE envelope before first renderable event")
	}
	initialBody := rec.BodyString()
	if !strings.Contains(initialBody, "message_start") {
		t.Fatalf("initial body = %q, want message_start", initialBody)
	}
	if strings.Contains(initialBody, "hello") {
		t.Fatalf("initial body = %q, should not contain delayed token", initialBody)
	}

	body.Release()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("err = %v, want nil", got.err)
		}
		if got.result == nil {
			t.Fatal("result = nil, want stream result")
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not finish after releasing delayed body")
	}
	if !strings.Contains(rec.BodyString(), "hello") {
		t.Fatalf("response body = %q, want delayed token after release", rec.BodyString())
	}
}

func TestKiroNonStreamingInitialResponseTimeoutBeforeCommit(t *testing.T) {
	c, rec := newKiroStreamTestContext()
	svc := &KiroGatewayService{}
	body := newBlockingReadCloser()
	defer body.Close()

	result, err := svc.handleNonStreamingResponseWithOptions(
		c,
		newBlockingKiroStreamHTTPResponse(body),
		time.Now(),
		"claude-opus-4-7",
		10,
		nil,
		0,
		0,
		false,
		false,
		kiroStreamOptions{initialResponseTimeout: 10 * time.Millisecond},
	)
	_ = body.Close()

	if result != nil {
		t.Fatalf("result = %+v, want nil", result)
	}
	var timeoutErr *kiroInitialResponseTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("err = %v, want kiroInitialResponseTimeoutError", err)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("response body = %q, want empty before timeout", rec.Body.String())
	}
}

// TestKiroStreamingNoRenderableContentFailsOverBeforeCommit 验证上游 HTTP 200
// 但没有任何可渲染内容时，在信封提交前就失败转移。
//
// 覆盖两种真实形态：
//   - 只有 usage 元数据帧（上游计了费但没产出内容）
//   - 帧本身损坏（缺字段导致解析失败）
//
// 行为变更：以前信封在响应头到达时就提交，这两种都只能以 200 + SSE error
// 收场 —— 客户端把它当成功、用户还被计费，且没有换账号重试的机会。
func TestKiroStreamingNoRenderableContentFailsOverBeforeCommit(t *testing.T) {
	cases := []struct {
		name  string
		frame []byte
	}{
		{
			name: "usage_metadata_only",
			frame: mustKiroEventFrame(t, "messageMetadataEvent", map[string]any{
				"tokenUsage": map[string]any{"inputTokens": 100, "outputTokens": 0},
			}),
		},
		{
			name:  "malformed_tool_frame",
			frame: mustKiroEventFrame(t, "toolUseEvent", map[string]any{"stop": true}),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := newKiroStreamTestContext()
			svc := &KiroGatewayService{}
			resp := newKiroStreamHTTPResponse(string(tc.frame))
			resp.Header.Set("x-amzn-requestid", "req-norender-1")

			result, err := svc.handleStreamingResponse(
				c, resp, time.Now(), "claude-opus-4-7", 10, nil, 0, 0, false, false,
			)

			if result != nil {
				t.Fatalf("result = %+v, want nil so the caller can fail over", result)
			}
			var failoverErr *UpstreamFailoverError
			if !errors.As(err, &failoverErr) {
				t.Fatalf("err = %v, want UpstreamFailoverError", err)
			}
			// 上游返回的是 HTTP 200，报 502 会让上游查无此单
			if failoverErr.StatusCode == http.StatusBadGateway {
				t.Fatal("status = 502, want non-502 for an upstream-200 response")
			}
			info := DecodeKiroFailureMessage(failoverErr.Message)
			if info == nil || info.RequestID != "req-norender-1" {
				t.Fatalf("request id not preserved for reconciliation: %+v", info)
			}
			if rec.Body.Len() != 0 {
				t.Fatalf("response body = %q, want empty before commit", rec.Body.String())
			}
		})
	}
}

func TestKiroStreamingErrorBeforeContentReturnsSSEErrorAfterCommit(t *testing.T) {
	c, rec := newKiroStreamTestContext()
	svc := &KiroGatewayService{}

	result, err := svc.handleStreamingResponse(
		c,
		newKiroStreamHTTPResponse(string(mustKiroExceptionFrame(t,
			"com.amazon.aws.codewhisperer#AccessDeniedException",
			map[string]any{"message": "Your subscription does not support this application."},
		))),
		time.Now(),
		"claude-sonnet-4-6",
		10,
		nil,
		0,
		0,
		false,
		false,
	)

	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if result == nil || result.usage == nil {
		t.Fatalf("result/usage should not be nil: %+v", result)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"event: message_start",
		"event: error",
		"Your subscription does not support this application.",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("response body missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "kiro_empty_stream") {
		t.Fatalf("response body should preserve the upstream exception instead of reporting an empty stream: %s", body)
	}
}

func TestKiroStreamingContextCancelledBeforeCommitDoesNotFailover(t *testing.T) {
	c, rec := newKiroStreamTestContext()
	ctx, cancel := context.WithCancel(c.Request.Context())
	cancel()
	c.Request = c.Request.WithContext(ctx)
	svc := &KiroGatewayService{}

	result, err := svc.handleStreamingResponse(
		c,
		newKiroStreamHTTPResponse(""),
		time.Now(),
		"claude-sonnet-4-6",
		10,
		nil,
		0,
		0,
		false,
		false,
	)

	if result != nil {
		t.Fatalf("result = %+v, want nil", result)
	}
	var failoverErr *UpstreamFailoverError
	if errors.As(err, &failoverErr) {
		t.Fatalf("err = %v, want context cancellation, not UpstreamFailoverError", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("response body = %q, want empty before failover", rec.Body.String())
	}
}

func TestKiroStreamingContentCommitsClaudeSSE(t *testing.T) {
	c, rec := newKiroStreamTestContext()
	svc := &KiroGatewayService{}

	result, err := svc.handleStreamingResponse(
		c,
		newKiroStreamHTTPResponse(string(mustContentPayload(t, "hello"))),
		time.Now(),
		"claude-sonnet-4-6",
		10,
		nil,
		0,
		0,
		false,
		false,
	)

	if err != nil {
		t.Fatalf("handleStreamingResponse error: %v", err)
	}
	if result == nil || result.usage == nil {
		t.Fatalf("result/usage should not be nil: %+v", result)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"event: message_start",
		"event: content_block_delta",
		"hello",
		"event: message_stop",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("response body missing %q: %s", want, body)
		}
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", rec.Header().Get("Content-Type"))
	}
}

func TestKiroStreamingBadCRCDoesNotSendNormalCompletion(t *testing.T) {
	c, rec := newKiroStreamTestContext()
	svc := &KiroGatewayService{}
	validFrame := mustContentPayload(t, "partial")
	invalidFrame := mustContentPayload(t, "must not leak")
	invalidFrame[len(invalidFrame)-1] ^= 0xff

	result, err := svc.handleStreamingResponse(
		c,
		newKiroStreamHTTPResponse(string(append(validFrame, invalidFrame...))),
		time.Now(),
		"claude-sonnet-4-6",
		10,
		nil,
		0,
		0,
		false,
		false,
	)

	if err != nil {
		t.Fatalf("handleStreamingResponse error: %v", err)
	}
	if result == nil || result.usage == nil {
		t.Fatalf("result/usage should not be nil: %+v", result)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: error") || !strings.Contains(body, "CRC mismatch") {
		t.Fatalf("response body missing CRC error: %s", body)
	}
	if strings.Contains(body, "event: message_delta") || strings.Contains(body, "event: message_stop") {
		t.Fatalf("response body contains normal completion after CRC error: %s", body)
	}
}

func TestKiroNonStreamingBadCRCReturnsFailoverBeforeCommit(t *testing.T) {
	c, rec := newKiroStreamTestContext()
	svc := &KiroGatewayService{}
	validFrame := mustContentPayload(t, "partial")
	invalidFrame := mustContentPayload(t, "must not leak")
	invalidFrame[len(invalidFrame)-1] ^= 0xff

	result, err := svc.handleNonStreamingResponse(
		c,
		newKiroStreamHTTPResponse(string(append(validFrame, invalidFrame...))),
		time.Now(),
		"claude-sonnet-4-6",
		10,
		nil,
		0,
		0,
		false,
		false,
	)

	if result != nil {
		t.Fatalf("result = %+v, want nil", result)
	}
	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) {
		t.Fatalf("err = %v, want UpstreamFailoverError", err)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("response body = %q, want empty before failover", rec.Body.String())
	}
}

func TestKiroOpenAIStreamingBadCRCDoesNotSendNormalCompletion(t *testing.T) {
	c, rec := newKiroStreamTestContext()
	svc := &KiroGatewayService{}
	validFrame := mustContentPayload(t, "partial")
	invalidFrame := mustContentPayload(t, "must not leak")
	invalidFrame[len(invalidFrame)-1] ^= 0xff

	result, err := svc.handleOpenAIStreamingResponse(
		c,
		newKiroStreamHTTPResponse(string(append(validFrame, invalidFrame...))),
		time.Now(),
		"gpt-5.6-sol",
		10,
		nil,
		0,
		0,
		false,
	)

	if err != nil {
		t.Fatalf("handleOpenAIStreamingResponse error: %v", err)
	}
	if result == nil || result.usage == nil {
		t.Fatalf("result/usage should not be nil: %+v", result)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"event_stream_decode_error"`) || !strings.Contains(body, "CRC mismatch") {
		t.Fatalf("response body missing CRC error: %s", body)
	}
	if strings.Contains(body, `"finish_reason":"stop"`) || strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Fatalf("response body contains normal completion after CRC error: %s", body)
	}
	if strings.Count(body, "data: [DONE]") != 1 {
		t.Fatalf("response body should contain exactly one [DONE]: %s", body)
	}
}

// TestKiroOpenAIStreamingEmptyBodyTriggersFailoverBeforeCommit 验证空流在
// SSE 信封提交**之前**就失败转移，而不是以 200 + SSE error 事件收场。
//
// 行为变更说明：这条路径以前在函数入口就写 c.Status(200) + initial chunk，
// 于是任何失败都只能以 SSE error 事件呈现，HTTP 状态仍是 200，
// 既无法换账号重试，也让"上游 200 空流"这类故障对客户端表现为成功。
// 现在信封延迟到确有内容才提交，提交前的失败走正常分类 + failover。
func TestKiroOpenAIStreamingEmptyBodyTriggersFailoverBeforeCommit(t *testing.T) {
	c, rec := newKiroStreamTestContext()
	svc := &KiroGatewayService{}

	result, err := svc.handleOpenAIStreamingResponse(
		c,
		newKiroStreamHTTPResponse(""),
		time.Now(),
		"gpt-5.6-sol",
		10,
		nil,
		0,
		0,
		false,
	)

	if result != nil {
		t.Fatalf("result = %+v, want nil", result)
	}
	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) {
		t.Fatalf("err = %v, want UpstreamFailoverError", err)
	}
	info := DecodeKiroFailureMessage(failoverErr.Message)
	if info == nil {
		t.Fatalf("message = %q, want a decodable kiro failure envelope", failoverErr.Message)
	}
	if info.Class != kiroFailureInBand {
		t.Fatalf("class = %q, want %q", info.Class, kiroFailureInBand)
	}
	// 上游返回的是 HTTP 200，报 502 会让上游查无此单
	if failoverErr.StatusCode == http.StatusBadGateway {
		t.Fatal("status = 502, want non-502 for an upstream-200 empty stream")
	}
	// 未提交信封 —— 不能有任何响应体漏给客户端
	if rec.Body.Len() != 0 {
		t.Fatalf("response body = %q, want empty before failover", rec.Body.String())
	}
}

// TestKiroStreamingThrottlingBeforeCommitMapsTo429 是整条修复链路最关键的
// 端到端断言：上游 HTTP 200 + ThrottlingException 必须映射成 429 并 failover，
// 而不是 502。
//
// 上游侧记录的是 200，如果我们对外报 502，上游就会说"没收到这些 502 请求"。
func TestKiroStreamingThrottlingBeforeCommitMapsTo429(t *testing.T) {
	frame := mustKiroExceptionFrame(t, "ThrottlingException",
		map[string]string{"message": "Too many requests"})

	t.Run("claude", func(t *testing.T) {
		c, rec := newKiroStreamTestContext()
		svc := &KiroGatewayService{}
		resp := newKiroStreamHTTPResponse(string(frame))
		resp.Header.Set("x-amzn-requestid", "req-throttle-1")

		result, err := svc.handleStreamingResponse(
			c, resp, time.Now(), "claude-sonnet-4-6", 10, nil, 0, 0, false, false,
		)
		assertThrottlingFailover(t, result == nil, err, rec.Body.Len())
	})

	t.Run("openai", func(t *testing.T) {
		c, rec := newKiroStreamTestContext()
		svc := &KiroGatewayService{}
		resp := newKiroStreamHTTPResponse(string(frame))
		resp.Header.Set("x-amzn-requestid", "req-throttle-1")

		result, err := svc.handleOpenAIStreamingResponse(
			c, resp, time.Now(), "gpt-5.6-sol", 10, nil, 0, 0, false,
		)
		assertThrottlingFailover(t, result == nil, err, rec.Body.Len())
	})
}

func assertThrottlingFailover(t *testing.T, resultIsNil bool, err error, bodyLen int) {
	t.Helper()
	if !resultIsNil {
		t.Fatal("result should be nil so the caller can fail over")
	}
	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) {
		t.Fatalf("err = %v, want UpstreamFailoverError", err)
	}
	if failoverErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (upstream returned HTTP 200 + ThrottlingException)",
			failoverErr.StatusCode)
	}
	info := DecodeKiroFailureMessage(failoverErr.Message)
	if info == nil {
		t.Fatalf("message = %q, want decodable envelope", failoverErr.Message)
	}
	if info.ExceptionType != "ThrottlingException" {
		t.Fatalf("exception = %q, want ThrottlingException", info.ExceptionType)
	}
	if info.RequestID != "req-throttle-1" {
		t.Fatalf("request_id = %q, want req-throttle-1 (needed to reconcile with upstream)", info.RequestID)
	}
	if info.RetryAfterSeconds <= 0 {
		t.Fatalf("retryAfter = %d, want > 0 for throttling", info.RetryAfterSeconds)
	}
	if bodyLen != 0 {
		t.Fatalf("body len = %d, want 0 before commit", bodyLen)
	}
}

// TestKiroOpenAIStreamingCommitDeadlineCommitsWithoutData 验证上游迟迟不发
// 第一帧时，到达提交上限后仍会提交 SSE 信封。
//
// 这是延迟提交的安全阀：不提交虽然保住了 failover 机会，但客户端和中间层
// （Cloudflare 约 120s）在收到任何字节前会超时断开。到点必须先保住连接。
func TestKiroOpenAIStreamingCommitDeadlineCommitsWithoutData(t *testing.T) {
	c, rec := newKiroStreamTestContext()
	svc := &KiroGatewayService{openAICommitDeadline: 30 * time.Millisecond}

	// 上游连接挂住不发数据，直到 body 被关闭
	blocking := newBlockingReadCloser()
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = blocking.Close()
	}()

	result, err := svc.handleOpenAIStreamingResponse(
		c,
		newBlockingKiroStreamHTTPResponse(blocking),
		time.Now(),
		"gpt-5.6-sol",
		10,
		nil,
		0,
		0,
		false,
	)
	if err != nil {
		t.Fatalf("handleOpenAIStreamingResponse error: %v", err)
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}
	// 到点提交后，即使上游最终什么都没发，客户端也已收到信封
	body := rec.Body.String()
	if !strings.Contains(body, "data: ") {
		t.Fatalf("expected SSE envelope to be committed at deadline: %q", body)
	}
	// 提交后才发现空流 → 以 SSE error 收场，并标记供调用方落 ops
	if !result.failedAfterCommit {
		t.Fatal("failedAfterCommit = false, want true")
	}
	if !strings.Contains(body, "upstream_empty_stream") {
		t.Fatalf("expected empty-stream SSE error after commit: %q", body)
	}
}

// TestKiroOpenAIStreamingEmptyAfterCommitSendsSSEError 验证信封已提交后才发现
// 空流时，仍以 SSE error 事件收场（此时状态码已无法更改）。
//
// 构造方式：先发一个只含 usage 的非可渲染帧让信封提交，再结束流。
func TestKiroOpenAIStreamingEmptyAfterCommitSendsSSEError(t *testing.T) {
	c, rec := newKiroStreamTestContext()
	svc := &KiroGatewayService{}

	// 一个可渲染帧先提交信封，随后 CRC 损坏帧在提交后触发错误
	validFrame := mustContentPayload(t, "hi")
	broken := mustContentPayload(t, "x")
	broken[len(broken)-1] ^= 0xff

	result, err := svc.handleOpenAIStreamingResponse(
		c,
		newKiroStreamHTTPResponse(string(append(validFrame, broken...))),
		time.Now(),
		"gpt-5.6-sol",
		10,
		nil,
		0,
		0,
		false,
	)
	if err != nil {
		t.Fatalf("handleOpenAIStreamingResponse error: %v", err)
	}
	if result == nil {
		t.Fatal("result should not be nil after commit")
	}
	// 提交后失败要把信息带回调用方落 ops，否则这类故障不可查
	if !result.failedAfterCommit {
		t.Fatal("failedAfterCommit = false, want true so the caller records ops")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "data: ") {
		t.Fatalf("expected SSE payload after commit: %s", body)
	}
	if strings.Contains(body, `"finish_reason":"stop"`) {
		t.Fatalf("must not send a normal completion after failure: %s", body)
	}
}

func TestKiroOpenAINonStreamingBadCRCReturnsFailoverBeforeCommit(t *testing.T) {
	c, rec := newKiroStreamTestContext()
	svc := &KiroGatewayService{}
	validFrame := mustContentPayload(t, "partial")
	invalidFrame := mustContentPayload(t, "must not leak")
	invalidFrame[len(invalidFrame)-1] ^= 0xff

	result, err := svc.handleOpenAINonStreamingResponse(
		c,
		newKiroStreamHTTPResponse(string(append(validFrame, invalidFrame...))),
		"gpt-5.6-sol",
		10,
		nil,
		0,
		0,
		false,
		0,
	)

	if result != nil {
		t.Fatalf("result = %+v, want nil", result)
	}
	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) {
		t.Fatalf("err = %v, want UpstreamFailoverError", err)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("response body = %q, want empty before failover", rec.Body.String())
	}
}

func TestKiroNonStreamingEmptyBodyTriggersFailoverBeforeCommit(t *testing.T) {
	c, rec := newKiroStreamTestContext()
	svc := &KiroGatewayService{}

	result, err := svc.handleNonStreamingResponse(
		c,
		newKiroStreamHTTPResponse(""),
		time.Now(),
		"claude-sonnet-4-6",
		10,
		nil,
		0,
		0,
		false,
		false,
	)

	if result != nil {
		t.Fatalf("result = %+v, want nil", result)
	}
	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) {
		t.Fatalf("err = %v, want UpstreamFailoverError", err)
	}
	// Message 现在携带故障分类（class/exception/request_id/reason），需要解码后断言。
	info := DecodeKiroFailureMessage(failoverErr.Message)
	if info == nil {
		t.Fatalf("message = %q, want a decodable kiro failure envelope", failoverErr.Message)
	}
	if info.Reason != "kiro_empty_response" {
		t.Fatalf("reason = %q, want kiro_empty_response", info.Reason)
	}
	// 上游返回 HTTP 200 + 空 body 属于 A 类：不能报 502，否则上游查无此单
	if info.Class != kiroFailureInBand {
		t.Fatalf("class = %q, want %q", info.Class, kiroFailureInBand)
	}
	if failoverErr.StatusCode == http.StatusBadGateway {
		t.Fatalf("status = 502, want a non-502 status for an upstream-200 empty response")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("response body = %q, want empty before failover", rec.Body.String())
	}
}

func TestKiroNonStreamingContentWritesClaudeJSON(t *testing.T) {
	c, rec := newKiroStreamTestContext()
	svc := &KiroGatewayService{}

	result, err := svc.handleNonStreamingResponse(
		c,
		newKiroStreamHTTPResponse(string(mustContentPayload(t, "hello"))),
		time.Now(),
		"claude-sonnet-4-6",
		10,
		nil,
		0,
		0,
		false,
		false,
	)

	if err != nil {
		t.Fatalf("handleNonStreamingResponse error: %v", err)
	}
	if result == nil || result.usage == nil {
		t.Fatalf("result/usage should not be nil: %+v", result)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`"type":"message"`,
		`"text":"hello"`,
		`"stop_reason":"end_turn"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("response body missing %q: %s", want, body)
		}
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("content-type = %q, want application/json", rec.Header().Get("Content-Type"))
	}
}
