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

func newKiroStreamTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
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

func newGatedReadCloser(data string) *gatedReadCloser {
	return &gatedReadCloser{
		release: make(chan struct{}),
		data:    []byte(data),
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

func TestKiroStreamingEmptyBodyTriggersFailoverBeforeCommit(t *testing.T) {
	c, rec := newKiroStreamTestContext()
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
	if !errors.As(err, &failoverErr) {
		t.Fatalf("err = %v, want UpstreamFailoverError", err)
	}
	if failoverErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", failoverErr.StatusCode, http.StatusBadGateway)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("response body = %q, want empty before failover", rec.Body.String())
	}
}

func TestKiroStreamingInitialResponseTimeoutBeforeCommit(t *testing.T) {
	c, rec := newKiroStreamTestContext()
	svc := &KiroGatewayService{}
	body := newBlockingReadCloser()
	defer body.Close()

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

func TestKiroStreamingInitialAckBeforeFirstRenderableEvent(t *testing.T) {
	c, rec := newKiroStreamTestContext()
	svc := &KiroGatewayService{}
	body := newGatedReadCloser(`{"content":"hello"}{"stop":true}`)
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
			kiroStreamOptions{
				initialResponseTimeout: 500 * time.Millisecond,
				initialAckTimeout:      10 * time.Millisecond,
			},
		)
		done <- streamResult{result: result, err: err}
	}()

	deadline := time.After(200 * time.Millisecond)
	for rec.Body.Len() == 0 {
		select {
		case <-deadline:
			t.Fatal("expected initial ACK to be written before first renderable event")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	ackBody := rec.Body.String()
	if !strings.Contains(ackBody, "message_start") {
		t.Fatalf("ACK body = %q, want message_start", ackBody)
	}
	if strings.Contains(ackBody, "hello") {
		t.Fatalf("ACK body = %q, should not contain delayed token", ackBody)
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
	if !strings.Contains(rec.Body.String(), "hello") {
		t.Fatalf("response body = %q, want delayed token", rec.Body.String())
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

func TestKiroStreamingUsageOnlyTriggersFailoverBeforeCommit(t *testing.T) {
	c, rec := newKiroStreamTestContext()
	svc := &KiroGatewayService{}

	result, err := svc.handleStreamingResponse(
		c,
		newKiroStreamHTTPResponse(`{"tokenUsage":{"inputTokens":100,"outputTokens":0}}`),
		time.Now(),
		"claude-opus-4-7",
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
	if failoverErr.Message != "kiro_empty_stream" {
		t.Fatalf("message = %q, want kiro_empty_stream", failoverErr.Message)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("response body = %q, want empty before failover", rec.Body.String())
	}
}

func TestKiroStreamingStopOnlyTriggersFailoverBeforeCommit(t *testing.T) {
	c, rec := newKiroStreamTestContext()
	svc := &KiroGatewayService{}

	result, err := svc.handleStreamingResponse(
		c,
		newKiroStreamHTTPResponse(`{"stop":true}`),
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
	if failoverErr.Message != "kiro_empty_stream" {
		t.Fatalf("message = %q, want kiro_empty_stream", failoverErr.Message)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("response body = %q, want empty before failover", rec.Body.String())
	}
}

func TestKiroStreamingErrorBeforeContentTriggersFailoverBeforeCommit(t *testing.T) {
	c, rec := newKiroStreamTestContext()
	svc := &KiroGatewayService{}

	result, err := svc.handleStreamingResponse(
		c,
		newKiroStreamHTTPResponse(`{"__type":"com.amazon.aws.codewhisperer#AccessDeniedException","message":"Your subscription does not support this application."}`),
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
	if failoverErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", failoverErr.StatusCode, http.StatusBadGateway)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("response body = %q, want empty before failover", rec.Body.String())
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
		newKiroStreamHTTPResponse(`{"content":"hello"}{"stop":true}`),
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
	if failoverErr.Message != "kiro_empty_response" {
		t.Fatalf("message = %q, want kiro_empty_response", failoverErr.Message)
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
		newKiroStreamHTTPResponse(`{"content":"hello"}{"stop":true}`),
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
