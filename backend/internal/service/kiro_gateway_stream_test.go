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
