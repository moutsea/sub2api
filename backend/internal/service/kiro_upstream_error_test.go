package service

import (
	"net/http"
	"strings"
	"testing"
)

// TestKiroExceptionStatusMapping 验证 AWS in-band exception 到状态码的映射。
//
// 核心诉求：上游返回 HTTP 200 + in-band exception 时，客户端不能收到 502。
// 上游侧记录的是 200，报 502 会让上游查无此单（"我们没收到 502 的请求"），
// 同时也误导客户端以为是网关故障而不是限流。
func TestKiroExceptionStatusMapping(t *testing.T) {
	cases := []struct {
		exceptionType string
		wantStatus    int
		wantRetry     bool
	}{
		{"ThrottlingException", http.StatusTooManyRequests, true},
		{"TooManyRequestsException", http.StatusTooManyRequests, true},
		{"ServiceQuotaExceededException", http.StatusTooManyRequests, true},
		{"ServiceUnavailableException", kiroStatusOverloaded, true},
		{"ModelNotReadyException", kiroStatusOverloaded, true},
		{"AccessDeniedException", http.StatusUnauthorized, false},
		{"ExpiredTokenException", http.StatusUnauthorized, false},
		{"ValidationException", http.StatusBadRequest, false},
		{"InternalServerException", http.StatusBadGateway, false},
	}

	for _, tc := range cases {
		t.Run(tc.exceptionType, func(t *testing.T) {
			status, retryAfter := kiroExceptionStatus(tc.exceptionType)
			if status != tc.wantStatus {
				t.Fatalf("status = %d, want %d", status, tc.wantStatus)
			}
			if tc.wantRetry && retryAfter <= 0 {
				t.Fatalf("retryAfter = %d, want > 0 for a retryable exception", retryAfter)
			}
			// 限流/过载类绝不能映射成 502
			if tc.wantRetry && status == http.StatusBadGateway {
				t.Fatal("retryable exception must not map to 502")
			}
		})
	}
}

// TestKiroExceptionStatusUnknownIsNotBadGateway 验证未识别的 exception 也不会
// 被压成 502 —— 否则新出现的 AWS exception 类型又会回到无法对单的状态。
func TestKiroExceptionStatusUnknownIsNotBadGateway(t *testing.T) {
	status, retryAfter := kiroExceptionStatus("SomeBrandNewAWSException")
	if status == http.StatusBadGateway {
		t.Fatal("unknown in-band exception must not map to 502")
	}
	if retryAfter <= 0 {
		t.Fatal("unknown in-band exception should advise a retry delay")
	}
}

// TestKiroFailureMessageRoundTrip 验证故障分类能通过 UpstreamFailoverError.Message
// 无损传递到 handler 层。
//
// UpstreamFailoverError 只有 StatusCode + Message 两个字段，而 handler 在
// failover 链耗尽后只拿得到 Message，所以分类信息必须编码进去再解出来。
func TestKiroFailureMessageRoundTrip(t *testing.T) {
	original := newKiroInBandFailure("ThrottlingException", "rate exceeded", "req-abc-123")
	failover := original.failoverError()

	if failover.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", failover.StatusCode)
	}

	decoded := DecodeKiroFailureMessage(failover.Message)
	if decoded == nil {
		t.Fatal("DecodeKiroFailureMessage returned nil")
	}
	if decoded.ExceptionType != "ThrottlingException" {
		t.Fatalf("exception = %q, want ThrottlingException", decoded.ExceptionType)
	}
	if decoded.RequestID != "req-abc-123" {
		t.Fatalf("request_id = %q, want req-abc-123", decoded.RequestID)
	}
	if decoded.Class != kiroFailureInBand {
		t.Fatalf("class = %q, want %q", decoded.Class, kiroFailureInBand)
	}
	if decoded.RetryAfterSeconds <= 0 {
		t.Fatalf("retryAfter = %d, want > 0", decoded.RetryAfterSeconds)
	}
	if decoded.IsTransport() {
		t.Fatal("in-band failure must not be classified as transport")
	}

	// 客户端文案里必须带 request_id，用户报障时可直接用于和上游对单
	msg := decoded.ClientMessage(failover.StatusCode)
	if !strings.Contains(msg, "req-abc-123") {
		t.Fatalf("client message %q should contain the request id", msg)
	}
	if !strings.Contains(msg, "ThrottlingException") {
		t.Fatalf("client message %q should contain the exception type", msg)
	}
}

// TestKiroTransportFailureRoundTrip 验证 B 类（请求可能未到上游）的编解码，
// 且明确区别于上游返回的错误。
func TestKiroTransportFailureRoundTrip(t *testing.T) {
	failure := newKiroTransportFailure("kiro_transport_error", http.StatusBadGateway, "dial tcp: i/o timeout")
	failover := failure.failoverError()

	decoded := DecodeKiroFailureMessage(failover.Message)
	if decoded == nil {
		t.Fatal("DecodeKiroFailureMessage returned nil")
	}
	if !decoded.IsTransport() {
		t.Fatalf("class = %q, want transport", decoded.Class)
	}
	if failover.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 for a transport failure", failover.StatusCode)
	}
	// 传输失败没有 requestID（请求没走到上游），文案不应硬塞一个空 id
	if strings.Contains(decoded.ClientMessage(failover.StatusCode), "request_id=") {
		t.Fatal("transport failure has no upstream request id; message should omit it")
	}
}

// TestKiroEmptyFailureCarriesRequestID 验证"上游 200 但空响应"分支会带上 requestID。
// 这是修复前完全丢失的字段，也是和上游对单的唯一凭据。
func TestKiroEmptyFailureCarriesRequestID(t *testing.T) {
	failure := newKiroEmptyFailure("kiro_empty_stream", "req-xyz-789")
	failover := failure.failoverError()

	decoded := DecodeKiroFailureMessage(failover.Message)
	if decoded == nil {
		t.Fatal("DecodeKiroFailureMessage returned nil")
	}
	if decoded.RequestID != "req-xyz-789" {
		t.Fatalf("request_id = %q, want req-xyz-789", decoded.RequestID)
	}
	if decoded.Reason != "kiro_empty_stream" {
		t.Fatalf("reason = %q, want kiro_empty_stream", decoded.Reason)
	}
	if failover.StatusCode == http.StatusBadGateway {
		t.Fatal("upstream-200 empty stream must not surface as 502")
	}
}

// TestDecodeNonKiroMessageReturnsNil 验证非 Kiro 消息不会被误解析，
// 让 handler 正常回退到按状态码映射的旧逻辑。
func TestDecodeNonKiroMessageReturnsNil(t *testing.T) {
	for _, msg := range []string{
		"",
		"upstream error: 500",
		"kiro_initial_response_timeout:response_headers:timeout=40s",
		"kiro_failure|truncated",
	} {
		if got := DecodeKiroFailureMessage(msg); got != nil {
			t.Fatalf("DecodeKiroFailureMessage(%q) = %+v, want nil", msg, got)
		}
	}
}

// TestInBandFailureNeverClassifiesAs502 是本次修复的核心断言。
//
// 上游 HTTP 200 + in-band exception 时，上游侧记录的是 200。
// 如果我们对外报 502，上游就会说"没收到这些 502 的请求"——正是排查的起点。
// 所有 A 类故障的分类状态码都不能是 502。
func TestInBandFailureNeverClassifiesAs502(t *testing.T) {
	exceptions := []string{
		"ThrottlingException",
		"ServiceUnavailableException",
		"ModelNotReadyException",
		"ServiceQuotaExceededException",
		"SomeUnknownFutureException",
		"", // 空 exception（纯空流）
	}
	for _, ex := range exceptions {
		name := ex
		if name == "" {
			name = "(empty)"
		}
		t.Run(name, func(t *testing.T) {
			var failover *UpstreamFailoverError
			if ex == "" {
				failover = newKiroEmptyFailure("kiro_empty_stream", "req-1").failoverError()
			} else {
				failover = newKiroInBandFailure(ex, "", "req-1").failoverError()
			}
			if failover.StatusCode == http.StatusBadGateway {
				t.Fatalf("in-band exception %q classified as 502", ex)
			}
			// requestID 必须保留，否则无法和上游对单
			info := DecodeKiroFailureMessage(failover.Message)
			if info == nil || info.RequestID != "req-1" {
				t.Fatalf("request id lost for exception %q", ex)
			}
		})
	}
}

// TestTransportFailureClassifiesAs5xxGateway 验证 B 类（可能未到上游）
// 落在 502/504，与 A 类明确区分。
func TestTransportFailureClassifiesAs5xxGateway(t *testing.T) {
	cases := map[string]int{
		"kiro_transport_error":                http.StatusBadGateway,
		"kiro_stream_timeout_before_first_ev": http.StatusGatewayTimeout,
	}
	for reason, status := range cases {
		failover := newKiroTransportFailure(reason, status, "boom").failoverError()
		if failover.StatusCode != status {
			t.Fatalf("reason %q: status = %d, want %d", reason, failover.StatusCode, status)
		}
		info := DecodeKiroFailureMessage(failover.Message)
		if info == nil || !info.IsTransport() {
			t.Fatalf("reason %q: not classified as transport", reason)
		}
	}
}

// TestKiroFailureDetailIncludesCorrelationFields 验证 ops 记录里的 detail
// 带齐对单所需字段。
func TestKiroFailureDetailIncludesCorrelationFields(t *testing.T) {
	failure := newKiroInBandFailure("ThrottlingException", "slow down", "req-1")
	detail := failure.detail()
	for _, want := range []string{"class=", "exception=ThrottlingException", "request_id=req-1"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail = %q, want it to contain %q", detail, want)
		}
	}
	if failure.opsKind() != "in_band_exception" {
		t.Fatalf("opsKind = %q, want in_band_exception", failure.opsKind())
	}
}
