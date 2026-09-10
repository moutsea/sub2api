package service

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// Kiro 上游故障分类。
//
// 排查 502 时发现的核心问题：所有失败都被压成一个 502，导致无法和上游对单，
// 也无法区分"上游收到了请求但处理失败"和"请求根本没发出去"。这里把故障分成三类：
//
//   - A 类 (kiroFailureInBand): 上游返回 HTTP 200，但在 AWS EventStream 里带
//     in-band exception（如 ThrottlingException），或返回了无法渲染的空内容。
//     上游侧记录的是 200，我们必须按 exception 类型映射成 429/529，而不是 502。
//   - B 类 (kiroFailureTransport): 连接建立/读写阶段就失败了（dial、TLS、EOF、
//     RST）。请求可能根本没到上游，上游侧无任何记录。应为 502/504。
//   - C 类：上游明确返回了 >=400 的 HTTP 状态码。这类由 writeMappedClaudeError
//     和既有的 UpstreamFailoverError{StatusCode: resp.StatusCode} 处理，
//     状态码本身就是上游给的，无需重新分类。
const (
	kiroFailureInBand    = "in_band_exception"
	kiroFailureTransport = "transport"
)

// kiroUpstreamFailure 描述一次 Kiro 上游失败，携带对单所需的全部信息。
type kiroUpstreamFailure struct {
	// Class 是上面三类之一，决定客户端看到的状态码语义。
	Class string
	// StatusCode 是映射后的对下游状态码。
	StatusCode int
	// ExceptionType 是 AWS 的 :exception-type / :error-code / __type，
	// 例如 ThrottlingException。A 类必填，是和上游对单的关键字段。
	ExceptionType string
	// RequestID 是上游 x-amzn-requestid。所有分支都要带上，否则无法对单。
	RequestID string
	// Reason 是内部原因标记（如 kiro_empty_stream），用于日志与 ops 记录。
	Reason string
	// Message 是已脱敏的上游错误文本。
	Message string
	// RetryAfterSeconds > 0 时会写 Retry-After 响应头。
	RetryAfterSeconds int
}

// isRetryableKiroException 判断 in-band exception 换一个账号重试是否有意义。
//
// 这个区分很关键，不能对所有 in-band exception 一律 failover：
//
//   - 可重试（限流、过载、容量不足）：换账号很可能成功，值得 failover，
//     且客户端应看到 429/529 而不是 502。
//   - 确定性（订阅不支持、参数非法、鉴权失败）：换账号只会撞同一面墙，
//     白白耗掉 failover 预算，还会把上游那句可操作的原因
//     （例如 "Your subscription does not support this application."）
//     替换成一句无信息量的 502 文案。这类应当直接把上游错误透传给客户端。
func isRetryableKiroException(exceptionType string) bool {
	if strings.TrimSpace(exceptionType) == "" {
		// 无 exception 类型（纯空流）：可能是软限流，换账号有意义
		return true
	}
	status, _ := kiroExceptionStatus(exceptionType)
	switch status {
	case http.StatusTooManyRequests, kiroStatusOverloaded, http.StatusBadGateway:
		return true
	default:
		// 401 / 400 等确定性错误
		return false
	}
}

// kiroExceptionStatus 把 AWS in-band exception 类型映射成对下游的状态码。
//
// 之所以不能一律 502：ThrottlingException 意味着上游限流，客户端应当退避重试；
// 报 502 会让客户端以为是网关坏了，也让上游查不到对应的 502 记录。
func kiroExceptionStatus(exceptionType string) (int, int) {
	normalized := strings.ToLower(strings.TrimSpace(exceptionType))
	normalized = strings.TrimSuffix(normalized, "exception")

	switch {
	case strings.Contains(normalized, "throttl"),
		strings.Contains(normalized, "toomanyrequests"),
		strings.Contains(normalized, "limitexceeded"),
		strings.Contains(normalized, "quotaexceeded"):
		// 上游限流：透传 429 并给出退避提示
		return http.StatusTooManyRequests, 3
	case strings.Contains(normalized, "serviceunavailable"),
		strings.Contains(normalized, "overloaded"),
		strings.Contains(normalized, "modelnotready"),
		strings.Contains(normalized, "capacity"):
		// 上游过载：529（Anthropic overloaded_error 语义）
		return kiroStatusOverloaded, 5
	case strings.Contains(normalized, "accessdenied"),
		strings.Contains(normalized, "unauthorized"),
		strings.Contains(normalized, "expiredtoken"),
		strings.Contains(normalized, "invalidtoken"):
		return http.StatusUnauthorized, 0
	case strings.Contains(normalized, "validation"),
		strings.Contains(normalized, "malformed"),
		strings.Contains(normalized, "invalidrequest"):
		return http.StatusBadRequest, 0
	case strings.Contains(normalized, "internalserver"),
		strings.Contains(normalized, "internalfailure"):
		return http.StatusBadGateway, 0
	default:
		// 未识别的 in-band exception：按上游过载处理并保留 exception 类型，
		// 便于后续根据真实数据补充映射规则。
		return kiroStatusOverloaded, 3
	}
}

// kiroStatusOverloaded 是 Anthropic 的 overloaded_error 状态码。
const kiroStatusOverloaded = 529

// newKiroInBandFailure 构造 A 类失败：上游 HTTP 200 + in-band exception。
func newKiroInBandFailure(exceptionType, message, requestID string) *kiroUpstreamFailure {
	status, retryAfter := kiroExceptionStatus(exceptionType)
	return &kiroUpstreamFailure{
		Class:             kiroFailureInBand,
		StatusCode:        status,
		ExceptionType:     strings.TrimSpace(exceptionType),
		RequestID:         strings.TrimSpace(requestID),
		Reason:            "kiro_in_band_exception",
		Message:           sanitizeKiroClientErrorMessage(message),
		RetryAfterSeconds: retryAfter,
	}
}

// newKiroEmptyFailure 构造 A 类失败中的"上游 200 但内容不可渲染"子类。
//
// 这类失败上游返回的是 200，requestID 是唯一能和上游对单的凭据，
// 必须带上——之前这条分支完全丢弃了 requestID。
func newKiroEmptyFailure(reason, requestID string) *kiroUpstreamFailure {
	return &kiroUpstreamFailure{
		Class:      kiroFailureInBand,
		StatusCode: kiroStatusOverloaded,
		RequestID:  strings.TrimSpace(requestID),
		Reason:     reason,
		// 上游 200 空响应通常是软限流，给出退避提示
		RetryAfterSeconds: 3,
	}
}

// newKiroTransportFailure 构造 B 类失败：请求可能未到达上游。
func newKiroTransportFailure(reason string, statusCode int, message string) *kiroUpstreamFailure {
	if statusCode == 0 {
		statusCode = http.StatusBadGateway
	}
	return &kiroUpstreamFailure{
		Class:      kiroFailureTransport,
		StatusCode: statusCode,
		Reason:     reason,
		Message:    sanitizeKiroClientErrorMessage(message),
	}
}

// recordKiroFailureOps 把 Kiro 故障写入 ops 记录。
//
// 为什么需要这个：流式/非流式处理函数内部没有 account，只能打 log；
// 但排查 502 时真正需要的是**可查询**的 ops 记录（带 requestID 和 exception
// 类型），否则和上游对单只能靠翻日志。这里在调用方（有 account 的地方）
// 从 failover error 里解出分类再落 ops。
func recordKiroFailureOps(c *gin.Context, account *Account, err error) {
	if c == nil || account == nil || err == nil {
		return
	}
	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) {
		return
	}
	info := DecodeKiroFailureMessage(failoverErr.Message)
	if info == nil {
		return
	}

	failure := &kiroUpstreamFailure{
		Class:         info.Class,
		StatusCode:    failoverErr.StatusCode,
		ExceptionType: info.ExceptionType,
		RequestID:     info.RequestID,
		Reason:        info.Reason,
		Message:       info.Message,
	}

	// 传输类失败没有上游状态码（请求可能没发出去），用 0 表示"上游无记录"
	upstreamStatus := failoverErr.StatusCode
	if info.IsTransport() {
		upstreamStatus = 0
	}

	setOpsUpstreamError(c, upstreamStatus, failure.Message, failure.detail())
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: upstreamStatus,
		UpstreamRequestID:  info.RequestID,
		Kind:               failure.opsKind(),
		Message:            failure.Message,
		Detail:             failure.detail(),
	})
}

// recordKiroPostCommitFailureOps 记录"流已提交后才失败"的情况。
//
// 这类失败无法再改 HTTP 状态码，函数会返回 (result, nil) 当作部分成功，
// 所以不会走 recordKiroFailureOps。但上游限流（ThrottlingException）绝大多数
// 落在这里：上游侧记录的是 200，如果我们也不落 ops，这批故障就彻底不可查。
func recordKiroPostCommitFailureOps(c *gin.Context, account *Account, exceptionType, requestID string) {
	if c == nil || account == nil {
		return
	}

	failure := &kiroUpstreamFailure{
		Class:         kiroFailureInBand,
		ExceptionType: strings.TrimSpace(exceptionType),
		RequestID:     strings.TrimSpace(requestID),
		Reason:        "kiro_failed_after_stream_commit",
	}
	if failure.ExceptionType != "" {
		failure.StatusCode, _ = kiroExceptionStatus(failure.ExceptionType)
	}

	// 上游 HTTP 状态是 200：故障在 body 里，不在状态码上。
	// 这里如实记 200，避免运维侧误以为上游返回了 5xx。
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: http.StatusOK,
		UpstreamRequestID:  failure.RequestID,
		Kind:               failure.opsKind(),
		Message:            failure.Message,
		Detail:             failure.detail() + " note=stream_already_committed",
	})
}

// opsKind 返回写入 ops 记录的 kind 字段，保留故障分类以便运维侧筛选。
func (f *kiroUpstreamFailure) opsKind() string {
	if f == nil {
		return ""
	}
	switch f.Class {
	case kiroFailureInBand:
		return "in_band_exception"
	case kiroFailureTransport:
		return "transport_error"
	default:
		return "http_error"
	}
}

// detail 返回人类可读的诊断串，进 ops 的 Detail 字段和日志。
// 格式固定为 class/exception/request_id/reason，便于 grep 与和上游对单。
func (f *kiroUpstreamFailure) detail() string {
	if f == nil {
		return ""
	}
	parts := make([]string, 0, 4)
	parts = append(parts, "class="+f.Class)
	if f.ExceptionType != "" {
		parts = append(parts, "exception="+f.ExceptionType)
	}
	if f.RequestID != "" {
		parts = append(parts, "request_id="+f.RequestID)
	}
	if f.Reason != "" {
		parts = append(parts, "reason="+f.Reason)
	}
	return strings.Join(parts, " ")
}

// clientMessage 返回对客户端展示的错误文本。
// 带上 request_id，这样用户报障时能直接提供可对单的 ID。
func (f *kiroUpstreamFailure) clientMessage() string {
	if f == nil {
		return ""
	}

	var base string
	switch f.Class {
	case kiroFailureInBand:
		switch f.StatusCode {
		case http.StatusTooManyRequests:
			base = "Upstream rate limit exceeded, please retry later"
		case kiroStatusOverloaded:
			base = "Upstream service overloaded, please retry later"
		case http.StatusUnauthorized:
			base = "Upstream authentication failed, please contact administrator"
		case http.StatusBadRequest:
			base = "Upstream rejected the request"
		default:
			base = "Upstream service temporarily unavailable"
		}
	case kiroFailureTransport:
		if f.StatusCode == http.StatusGatewayTimeout {
			base = "Upstream connection timed out before the request completed"
		} else {
			base = "Failed to reach upstream service"
		}
	default:
		base = "Upstream request failed"
	}

	if f.ExceptionType != "" {
		base += " (" + f.ExceptionType + ")"
	}
	if f.RequestID != "" {
		base += " [request_id=" + f.RequestID + "]"
	}
	return base
}

// failoverError 把故障包装成 UpstreamFailoverError 以触发账号切换。
// 分类信息编码进 Message，在 failover 链耗尽后仍能还原出正确的对下游状态码。
func (f *kiroUpstreamFailure) failoverError() *UpstreamFailoverError {
	if f == nil {
		return &UpstreamFailoverError{StatusCode: http.StatusBadGateway}
	}
	return &UpstreamFailoverError{
		StatusCode: f.StatusCode,
		Message:    f.encodeMessage(),
	}
}

// kiroFailureMessagePrefix 标记一条 failover Message 携带了 Kiro 故障分类。
const kiroFailureMessagePrefix = "kiro_failure|"

// encodeMessage 把分类信息序列化进 failover Message。
//
// UpstreamFailoverError 只有 StatusCode 和 Message 两个字段，而 handler 在
// failover 耗尽后只拿得到 Message，所以把 class/exception/request_id 编码进去，
// 由 handler 侧解出来生成最终响应。
func (f *kiroUpstreamFailure) encodeMessage() string {
	if f == nil {
		return ""
	}
	// 用 \x1f (unit separator) 分隔，避免和错误文本内容冲突
	return fmt.Sprintf("%s%s\x1f%s\x1f%s\x1f%s\x1f%d\x1f%s",
		kiroFailureMessagePrefix,
		f.Class,
		f.ExceptionType,
		f.RequestID,
		f.Reason,
		f.RetryAfterSeconds,
		f.Message,
	)
}

// DecodeKiroFailureMessage 从 failover Message 还原故障分类。
// 非 Kiro 分类消息返回 nil，调用方回退到原有的按状态码映射逻辑。
func DecodeKiroFailureMessage(message string) *KiroFailureInfo {
	if !strings.HasPrefix(message, kiroFailureMessagePrefix) {
		return nil
	}
	parts := strings.SplitN(strings.TrimPrefix(message, kiroFailureMessagePrefix), "\x1f", 6)
	if len(parts) < 6 {
		return nil
	}
	retryAfter := 0
	if _, err := fmt.Sscanf(parts[4], "%d", &retryAfter); err != nil {
		retryAfter = 0
	}
	return &KiroFailureInfo{
		Class:             parts[0],
		ExceptionType:     parts[1],
		RequestID:         parts[2],
		Reason:            parts[3],
		RetryAfterSeconds: retryAfter,
		Message:           parts[5],
	}
}

// KiroFailureInfo 是 handler 侧可见的故障分类信息。
type KiroFailureInfo struct {
	Class             string
	ExceptionType     string
	RequestID         string
	Reason            string
	RetryAfterSeconds int
	Message           string
}

// IsTransport 表示请求可能从未到达上游，上游侧不会有记录。
func (i *KiroFailureInfo) IsTransport() bool {
	return i != nil && i.Class == kiroFailureTransport
}

// ClientMessage 复用服务层的文案生成逻辑，保证两侧口径一致。
// status 由 UpstreamFailoverError.StatusCode 传入（未参与 Message 编码）。
func (i *KiroFailureInfo) ClientMessage(status int) string {
	if i == nil {
		return ""
	}
	f := &kiroUpstreamFailure{
		Class:         i.Class,
		StatusCode:    status,
		ExceptionType: i.ExceptionType,
		RequestID:     i.RequestID,
		Reason:        i.Reason,
		Message:       i.Message,
	}
	return f.clientMessage()
}
