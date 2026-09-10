package repository

import (
	"crypto/tls"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	utls "github.com/refraction-networking/utls"
	"github.com/stretchr/testify/require"
)

// TestUpstreamTransportNegotiatesHTTP2 验证非 Anthropic 域名能协商到 HTTP/2。
//
// 回归目标：此前 wrapWithUTLSDialer 对所有域名都设置 DialTLSContext 并返回
// utls.UConn。Go 的 transport 用 `pconn.conn.(*tls.Conn)` 断言提取 TLS 状态，
// utls.UConn 断言失败 → tlsState 为 nil → 永远不启用 HTTP/2。
// 结果是所有上游请求都退化为 HTTP/1.1，叠加 Connection: close 后完全无法复用连接。
func TestUpstreamTransportNegotiatesHTTP2(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Proto)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	transport, err := buildUpstreamTransport(defaultPoolSettings(nil), nil)
	require.NoError(t, err)
	// 测试服务端使用自签证书，跳过校验；NextProtos 留空以验证 dialer 自己填充 h2
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	// TLSClientConfig 要在 wrap 之前生效，这里重新 wrap 一次
	transport = wrapWithUTLSDialer(transport)

	resp, err := (&http.Client{Transport: transport}).Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, "HTTP/2.0", string(body), "非 Anthropic 域名应协商到 HTTP/2")
}

// TestUTLSNegotiatesHTTP1Only 验证 uTLS 路径的 ALPN 被压到 http/1.1。
//
// 回归目标（一个隐蔽的既有故障）：
// utls 的 ALPNExtension.writeToUConn 会反向覆盖 uc.config.NextProtos，
// 所以对预置 ClientHelloID 设置 Config.NextProtos 是无效的，
// 预置 spec 里硬编码的 "h2,http/1.1" 永远生效 → 服务端协商出 h2。
// 但 Go 的 transport 无法从 *utls.UConn 提取 tlsState，不会切到 HTTP/2，
// 仍按 HTTP/1.1 在 h2 连接上说话 → 服务端收到 bogus greeting 后直接断连。
// 实测 api.anthropic.com 会协商到 h2，即这条路径本会失败。
func TestUTLSNegotiatesHTTP1Only(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Proto)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "https://")
	host, _, err := net.SplitHostPort(addr)
	require.NoError(t, err)

	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer conn.Close()

	uc := utls.UClient(conn, &utls.Config{
		ServerName:         host,
		InsecureSkipVerify: true,
	}, utls.HelloCustom)
	spec, err := firefoxHTTP1Spec()
	require.NoError(t, err)
	require.NoError(t, uc.ApplyPreset(spec))
	require.NoError(t, uc.Handshake())

	negotiated := uc.ConnectionState().NegotiatedProtocol
	require.NotEqual(t, "h2", negotiated,
		"uTLS 连接绝不能协商出 h2：Go 会在这条连接上按 HTTP/1.1 说话，服务端将断连")
	require.Contains(t, []string{"http/1.1", ""}, negotiated)
}

// TestFirefoxHTTP1SpecKeepsFingerprintOtherwiseIntact 验证除 ALPN 之外的
// Firefox 指纹特征未被改动（密码套件、扩展数量保持一致）。
func TestFirefoxHTTP1SpecKeepsFingerprintOtherwiseIntact(t *testing.T) {
	original, err := utls.UTLSIdToSpec(utls.HelloFirefox_Auto)
	require.NoError(t, err)
	patched, err := firefoxHTTP1Spec()
	require.NoError(t, err)

	require.Equal(t, original.CipherSuites, patched.CipherSuites, "密码套件不应改变")
	require.Equal(t, len(original.Extensions), len(patched.Extensions), "扩展数量不应改变")

	var found bool
	for _, ext := range patched.Extensions {
		if alpn, ok := ext.(*utls.ALPNExtension); ok {
			found = true
			require.Equal(t, []string{"http/1.1"}, alpn.AlpnProtocols)
		}
	}
	require.True(t, found, "Firefox spec 应含 ALPN 扩展；缺失说明 utls 版本行为变了")
}

// TestNeedsUTLSFingerprint 验证只有 Anthropic 系域名走 uTLS 指纹伪装。
// uTLS 与 h2 互斥，所以这个判定直接决定了哪些上游能用上 HTTP/2。
func TestNeedsUTLSFingerprint(t *testing.T) {
	utlsHosts := []string{
		"api.anthropic.com",
		"claude.ai",
		"API.ANTHROPIC.COM",
	}
	stdHosts := []string{
		"runtime.us-east-1.kiro.dev",
		"q.us-east-1.amazonaws.com",
		"codewhisperer.us-east-1.amazonaws.com",
		"prod.us-east-1.auth.desktop.kiro.dev",
	}

	for _, host := range utlsHosts {
		require.True(t, needsUTLSFingerprint(host), "expected uTLS for %s", host)
	}
	for _, host := range stdHosts {
		require.False(t, needsUTLSFingerprint(host), "expected standard TLS (h2-capable) for %s", host)
	}
}

// TestPoolKeyStableAcrossConcurrencyValues 验证连接池 key 不再因调用方传入
// 不同并发数而抖动。
//
// 回归目标：token 刷新路径传 accountConcurrency=1，主转发路径传
// account.Concurrency=N。两者 cacheKey 相同（account+proxy）但 poolKey 不同，
// 导致 shouldReuseEntry 判定不可复用 → 每次交替调用都销毁并重建整个 transport。
func TestPoolKeyStableAcrossConcurrencyValues(t *testing.T) {
	svc := &httpUpstreamService{
		cfg:     &config.Config{},
		clients: make(map[string]*upstreamClientEntry),
	}
	isolation := config.ConnectionPoolIsolationAccountProxy

	// 小并发值都会被 minAccountPoolConns 抬到同一档位，poolKey 必须一致
	refreshKey := svc.buildPoolKey(isolation, 1)
	for _, concurrency := range []int{2, 3, 4} {
		require.Equal(t, refreshKey, svc.buildPoolKey(isolation, concurrency),
			"concurrency=%d 应与 concurrency=1 共享同一 poolKey", concurrency)
	}

	// 同一并发值必须稳定（幂等）
	require.Equal(t, svc.buildPoolKey(isolation, 32), svc.buildPoolKey(isolation, 32))
}

// TestAccountPoolNotThrottledByConcurrency 验证连接池上限不再与账号并发数 1:1 绑定。
//
// 单个客户端请求可能需要多条上游连接（主转发 + websearch + usage_limits +
// token 刷新）。此前 maxConnsPerHost = accountConcurrency 会让这些辅助请求
// 与主转发互相抢槽位，HTTP/1.1 下尤其容易在 transport 层排队。
func TestAccountPoolNotThrottledByConcurrency(t *testing.T) {
	svc := &httpUpstreamService{
		cfg:     &config.Config{},
		clients: make(map[string]*upstreamClientEntry),
	}

	settings := svc.resolvePoolSettings(config.ConnectionPoolIsolationAccountProxy, 1)
	require.GreaterOrEqual(t, settings.maxConnsPerHost, minAccountPoolConns,
		"并发为 1 的账号也要有足够连接给辅助请求使用")

	settings = svc.resolvePoolSettings(config.ConnectionPoolIsolationAccountProxy, 10)
	require.Greater(t, settings.maxConnsPerHost, 10,
		"连接池上限应高于账号并发数，留出辅助请求余量")

	// 不应超过全局默认上限
	require.LessOrEqual(t, settings.maxConnsPerHost, defaultMaxConnsPerHost)
}

// TestAccountPoolLimitOverflowSafe 验证异常大的账号并发数不会让连接数翻负。
//
// 负数 MaxIdleConnsPerHost 会让 Go 的 tryPutIdleConn 永远返回 errTooManyIdleHost
// （判定是 len(idles) >= max，负数上限对任意 len 都成立），从而彻底禁用连接复用。
//
// 注意 accountPoolLimit 只负责放大 + 下限 + 防溢出，**不施加上限**；
// 上限属于配置语义，由 resolvePoolSettings 按字段用 capPositive 收口。
func TestAccountPoolLimitOverflowSafe(t *testing.T) {
	for _, concurrency := range []int{
		1, 4, 16, 1000,
		math.MaxInt32,
		math.MaxInt64 / 2,
		math.MaxInt64,
	} {
		limit := accountPoolLimit(concurrency)
		require.Greater(t, limit, 0, "concurrency=%d 推导出非正连接数", concurrency)
	}

	// 放大系数生效，且不被任何硬编码常量截断
	require.Equal(t, 1000*accountPoolHeadroom, accountPoolLimit(1000))
	// 低并发有下限
	require.Equal(t, minAccountPoolConns, accountPoolLimit(1))
}

// TestConfiguredMaxConnsPerHostAboveDefaultIsHonored 验证显式配置的
// max_conns_per_host 大于默认值(240)时不会被压回默认值。
//
// 回归目标：accountPoolLimit 曾用硬编码的 defaultMaxConnsPerHost 收口，
// 于是 `max_conns_per_host: 500` + 高并发账号仍只拿到 240，配置形同虚设。
// 不会造成 502，但会限制高并发账号的吞吐。
func TestConfiguredMaxConnsPerHostAboveDefaultIsHonored(t *testing.T) {
	const configured = 500
	svc := &httpUpstreamService{
		cfg: &config.Config{Gateway: config.GatewayConfig{
			ConnectionPoolIsolation: config.ConnectionPoolIsolationAccount,
			MaxConnsPerHost:         configured,
			MaxIdleConns:            configured,
			MaxIdleConnsPerHost:     configured,
		}},
		clients: make(map[string]*upstreamClientEntry),
	}

	// concurrency 200 → 期望 800，被配置的 500 收口（而不是被默认值 240 截断）
	settings := svc.resolvePoolSettings(config.ConnectionPoolIsolationAccount, 200)
	require.Equal(t, configured, settings.maxConnsPerHost,
		"应收口到配置值 %d，而不是默认值 %d", configured, defaultMaxConnsPerHost)
	require.Greater(t, settings.maxConnsPerHost, defaultMaxConnsPerHost,
		"配置大于默认值时必须生效")

	// concurrency 80 → 320，低于配置上限，应原样保留
	settings = svc.resolvePoolSettings(config.ConnectionPoolIsolationAccount, 80)
	require.Equal(t, 80*accountPoolHeadroom, settings.maxConnsPerHost)
	require.Greater(t, settings.maxConnsPerHost, defaultMaxConnsPerHost)
}

// TestUnlimitedMaxConnsPerHostNotClampedByDefault 验证显式配置为 0（无限制）时
// 账号连接池不被默认值收口。
//
// defaultPoolSettings 对 MaxConnsPerHost 用的是 `>= 0` 判定，
// 即显式 0 会被当作"无限制"保留，这里确认该语义不被账号缩放破坏。
func TestUnlimitedMaxConnsPerHostNotClampedByDefault(t *testing.T) {
	svc := &httpUpstreamService{
		cfg: &config.Config{Gateway: config.GatewayConfig{
			ConnectionPoolIsolation: config.ConnectionPoolIsolationAccount,
			MaxConnsPerHost:         0, // 无限制
		}},
		clients: make(map[string]*upstreamClientEntry),
	}

	settings := svc.resolvePoolSettings(config.ConnectionPoolIsolationAccount, 200)
	require.Equal(t, 200*accountPoolHeadroom, settings.maxConnsPerHost,
		"无限制配置下应保留放大后的值，不被默认值 240 截断")
}

// TestResolvePoolSettingsRespectsPerFieldConfig 验证放大系数不会顶穿运维为
// 各字段单独配置的上限（这些配置是用来约束内存/fd 的）。
func TestResolvePoolSettingsRespectsPerFieldConfig(t *testing.T) {
	svc := &httpUpstreamService{
		cfg: &config.Config{Gateway: config.GatewayConfig{
			ConnectionPoolIsolation: config.ConnectionPoolIsolationAccount,
			MaxIdleConns:            100,
			MaxIdleConnsPerHost:     20,
			MaxConnsPerHost:         200,
		}},
		clients: make(map[string]*upstreamClientEntry),
	}

	// concurrency 40 → 期望放大到 160，但 maxIdleConnsPerHost 配置只有 20
	settings := svc.resolvePoolSettings(config.ConnectionPoolIsolationAccount, 40)
	require.LessOrEqual(t, settings.maxIdleConnsPerHost, 20, "不应顶穿 max_idle_conns_per_host")
	require.LessOrEqual(t, settings.maxIdleConns, 100, "不应顶穿 max_idle_conns")
	require.LessOrEqual(t, settings.maxConnsPerHost, 200, "不应顶穿 max_conns_per_host")
	require.Greater(t, settings.maxConnsPerHost, 0)
}

// TestUpstreamRejectsNothing 兜底验证：显式 Host / Connection 头在 h2 下不会被拒绝。
// 移除 Connection: close 后仍可能有历史配置开启它，确认两种协议都能正常工作。
func TestExplicitHostAndConnectionHeadersTolerated(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Proto)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	transport, err := buildUpstreamTransport(defaultPoolSettings(nil), nil)
	require.NoError(t, err)
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	transport = wrapWithUTLSDialer(transport)

	req, err := http.NewRequest("POST", srv.URL, strings.NewReader("{}"))
	require.NoError(t, err)
	req.Header.Set("Host", strings.TrimPrefix(srv.URL, "https://"))
	req.Header.Set("Connection", "close")

	resp, err := (&http.Client{Transport: transport}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}
