package repository

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyutil"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
	utls "github.com/refraction-networking/utls"
)

// 默认配置常量
// 这些值在配置文件未指定时作为回退默认值使用
const (
	// directProxyKey: 无代理时的缓存键标识
	directProxyKey = "direct"
	// defaultMaxIdleConns: 默认最大空闲连接总数
	// HTTP/2 场景下，单连接可多路复用，240 足以支撑高并发
	defaultMaxIdleConns = 240
	// defaultMaxIdleConnsPerHost: 默认每主机最大空闲连接数
	defaultMaxIdleConnsPerHost = 120
	// defaultMaxConnsPerHost: 默认每主机最大连接数（含活跃连接）
	// 达到上限后新请求会等待，而非无限创建连接
	defaultMaxConnsPerHost = 240
	// defaultIdleConnTimeout: 默认空闲连接超时时间（90秒）
	// 超时后连接会被关闭，释放系统资源（建议小于上游 LB 超时）
	defaultIdleConnTimeout = 90 * time.Second
	// defaultResponseHeaderTimeout: 默认等待响应头超时时间（5分钟）
	// LLM 请求可能排队较久，需要较长超时
	defaultResponseHeaderTimeout = 300 * time.Second
	// defaultMaxUpstreamClients: 默认最大客户端缓存数量
	// 超出后会淘汰最久未使用的客户端
	defaultMaxUpstreamClients = 5000
	// defaultClientIdleTTLSeconds: 默认客户端空闲回收阈值（15分钟）
	defaultClientIdleTTLSeconds = 900
)

var errUpstreamClientLimitReached = errors.New("upstream client cache limit reached")

// poolSettings 连接池配置参数
// 封装 Transport 所需的各项连接池参数
type poolSettings struct {
	maxIdleConns          int           // 最大空闲连接总数
	maxIdleConnsPerHost   int           // 每主机最大空闲连接数
	maxConnsPerHost       int           // 每主机最大连接数（含活跃）
	idleConnTimeout       time.Duration // 空闲连接超时时间
	responseHeaderTimeout time.Duration // 等待响应头超时时间
}

// upstreamClientEntry 上游客户端缓存条目
// 记录客户端实例及其元数据，用于连接池管理和淘汰策略
type upstreamClientEntry struct {
	client   *http.Client // HTTP 客户端实例
	proxyKey string       // 代理标识（用于检测代理变更）
	poolKey  string       // 连接池配置标识（用于检测配置变更）
	lastUsed int64        // 最后使用时间戳（纳秒），用于 LRU 淘汰
	inFlight int64        // 当前进行中的请求数，>0 时不可淘汰
}

// httpUpstreamService 通用 HTTP 上游服务
// 用于向任意 HTTP API（Claude、OpenAI 等）发送请求，支持可选代理
//
// 架构设计：
// - 根据隔离策略（proxy/account/account_proxy）缓存客户端实例
// - 每个客户端拥有独立的 Transport 连接池
// - 支持 LRU + 空闲时间双重淘汰策略
//
// 性能优化：
// 1. 根据隔离策略缓存客户端实例，避免频繁创建 http.Client
// 2. 复用 Transport 连接池，减少 TCP 握手和 TLS 协商开销
// 3. 支持账号级隔离与空闲回收，降低连接层关联风险
// 4. 达到最大连接数后等待可用连接，而非无限创建
// 5. 仅回收空闲客户端，避免中断活跃请求
// 6. HTTP/2 多路复用，连接上限不等于并发请求上限
// 7. 代理变更时清空旧连接池，避免复用错误代理
// 8. 账号并发数与连接池上限对应（账号隔离策略下）
type httpUpstreamService struct {
	cfg     *config.Config                  // 全局配置
	mu      sync.RWMutex                    // 保护 clients map 的读写锁
	clients map[string]*upstreamClientEntry // 客户端缓存池，key 由隔离策略决定
}

// NewHTTPUpstream 创建通用 HTTP 上游服务
// 使用配置中的连接池参数构建 Transport
//
// 参数:
//   - cfg: 全局配置，包含连接池参数和隔离策略
//
// 返回:
//   - service.HTTPUpstream 接口实现
func NewHTTPUpstream(cfg *config.Config) service.HTTPUpstream {
	return &httpUpstreamService{
		cfg:     cfg,
		clients: make(map[string]*upstreamClientEntry),
	}
}

// Do 执行 HTTP 请求
// 根据隔离策略获取或创建客户端，并跟踪请求生命周期
//
// 参数:
//   - req: HTTP 请求对象
//   - proxyURL: 代理地址，空字符串表示直连
//   - accountID: 账户 ID，用于账户级隔离
//   - accountConcurrency: 账户并发限制，用于动态调整连接池大小
//
// 返回:
//   - *http.Response: HTTP 响应（Body 已包装，关闭时自动更新计数）
//   - error: 请求错误
//
// 注意:
//   - 调用方必须关闭 resp.Body，否则会导致 inFlight 计数泄漏
//   - inFlight > 0 的客户端不会被淘汰，确保活跃请求不被中断
func (s *httpUpstreamService) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	if err := s.validateRequestHost(req); err != nil {
		return nil, err
	}

	// 获取或创建对应的客户端，并标记请求占用
	entry, err := s.acquireClient(proxyURL, accountID, accountConcurrency)
	if err != nil {
		return nil, err
	}

	// 执行请求
	resp, err := entry.client.Do(req)
	if err != nil {
		// 请求失败，立即减少计数
		atomic.AddInt64(&entry.inFlight, -1)
		atomic.StoreInt64(&entry.lastUsed, time.Now().UnixNano())
		return nil, err
	}

	// 包装响应体，在关闭时自动减少计数并更新时间戳
	// 这确保了流式响应（如 SSE）在完全读取前不会被淘汰
	resp.Body = wrapTrackedBody(resp.Body, func() {
		atomic.AddInt64(&entry.inFlight, -1)
		atomic.StoreInt64(&entry.lastUsed, time.Now().UnixNano())
	})

	return resp, nil
}

func (s *httpUpstreamService) shouldValidateResolvedIP() bool {
	if s.cfg == nil {
		return false
	}
	if !s.cfg.Security.URLAllowlist.Enabled {
		return false
	}
	return !s.cfg.Security.URLAllowlist.AllowPrivateHosts
}

func (s *httpUpstreamService) validateRequestHost(req *http.Request) error {
	if !s.shouldValidateResolvedIP() {
		return nil
	}
	if req == nil || req.URL == nil {
		return errors.New("request url is nil")
	}
	host := strings.TrimSpace(req.URL.Hostname())
	if host == "" {
		return errors.New("request host is empty")
	}
	if err := urlvalidator.ValidateResolvedIP(host); err != nil {
		return err
	}
	return nil
}

func (s *httpUpstreamService) redirectChecker(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return s.validateRequestHost(req)
}

// acquireClient 获取或创建客户端，并标记为进行中请求
// 用于请求路径，避免在获取后被淘汰
func (s *httpUpstreamService) acquireClient(proxyURL string, accountID int64, accountConcurrency int) (*upstreamClientEntry, error) {
	return s.getClientEntry(proxyURL, accountID, accountConcurrency, true, true)
}

// getOrCreateClient 获取或创建客户端
// 根据隔离策略和参数决定缓存键，处理代理变更和配置变更
//
// 参数:
//   - proxyURL: 代理地址
//   - accountID: 账户 ID
//   - accountConcurrency: 账户并发限制
//
// 返回:
//   - *upstreamClientEntry: 客户端缓存条目
//
// 隔离策略说明:
//   - proxy: 按代理地址隔离，同一代理共享客户端
//   - account: 按账户隔离，同一账户共享客户端（代理变更时重建）
//   - account_proxy: 按账户+代理组合隔离，最细粒度
func (s *httpUpstreamService) getOrCreateClient(proxyURL string, accountID int64, accountConcurrency int) *upstreamClientEntry {
	entry, _ := s.getClientEntry(proxyURL, accountID, accountConcurrency, false, false)
	return entry
}

// getClientEntry 获取或创建客户端条目
// markInFlight=true 时会标记进行中请求，用于请求路径防止被淘汰
// enforceLimit=true 时会限制客户端数量，超限且无法淘汰时返回错误
func (s *httpUpstreamService) getClientEntry(proxyURL string, accountID int64, accountConcurrency int, markInFlight bool, enforceLimit bool) (*upstreamClientEntry, error) {
	// 获取隔离模式
	isolation := s.getIsolationMode()
	// 标准化代理 URL 并解析
	proxyKey, parsedProxy := normalizeProxyURL(proxyURL)
	// 构建缓存键（根据隔离策略不同）
	cacheKey := buildCacheKey(isolation, proxyKey, accountID)
	// 构建连接池配置键（用于检测配置变更）
	poolKey := s.buildPoolKey(isolation, accountConcurrency)

	now := time.Now()
	nowUnix := now.UnixNano()

	// 读锁快速路径：命中缓存直接返回，减少锁竞争
	s.mu.RLock()
	if entry, ok := s.clients[cacheKey]; ok && s.shouldReuseEntry(entry, isolation, proxyKey, poolKey) {
		atomic.StoreInt64(&entry.lastUsed, nowUnix)
		if markInFlight {
			atomic.AddInt64(&entry.inFlight, 1)
		}
		s.mu.RUnlock()
		return entry, nil
	}
	s.mu.RUnlock()

	// 写锁慢路径：创建或重建客户端
	s.mu.Lock()
	if entry, ok := s.clients[cacheKey]; ok {
		if s.shouldReuseEntry(entry, isolation, proxyKey, poolKey) {
			atomic.StoreInt64(&entry.lastUsed, nowUnix)
			if markInFlight {
				atomic.AddInt64(&entry.inFlight, 1)
			}
			s.mu.Unlock()
			return entry, nil
		}
		s.removeClientLocked(cacheKey, entry)
	}

	// 超出缓存上限时尝试淘汰，无法淘汰则拒绝新建
	if enforceLimit && s.maxUpstreamClients() > 0 {
		s.evictIdleLocked(now)
		if len(s.clients) >= s.maxUpstreamClients() {
			if !s.evictOldestIdleLocked() {
				s.mu.Unlock()
				return nil, errUpstreamClientLimitReached
			}
		}
	}

	// 缓存未命中或需要重建，创建新客户端
	settings := s.resolvePoolSettings(isolation, accountConcurrency)
	transport, err := buildUpstreamTransport(settings, parsedProxy)
	if err != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("build transport: %w", err)
	}
	client := &http.Client{Transport: transport}
	if s.shouldValidateResolvedIP() {
		client.CheckRedirect = s.redirectChecker
	}
	entry := &upstreamClientEntry{
		client:   client,
		proxyKey: proxyKey,
		poolKey:  poolKey,
	}
	atomic.StoreInt64(&entry.lastUsed, nowUnix)
	if markInFlight {
		atomic.StoreInt64(&entry.inFlight, 1)
	}
	s.clients[cacheKey] = entry

	// 执行淘汰策略：先淘汰空闲超时的，再淘汰超出数量限制的
	s.evictIdleLocked(now)
	s.evictOverLimitLocked()
	s.mu.Unlock()
	return entry, nil
}

// shouldReuseEntry 判断缓存条目是否可复用
// 若代理或连接池配置发生变化，则需要重建客户端
func (s *httpUpstreamService) shouldReuseEntry(entry *upstreamClientEntry, isolation, proxyKey, poolKey string) bool {
	if entry == nil {
		return false
	}
	if isolation == config.ConnectionPoolIsolationAccount && entry.proxyKey != proxyKey {
		return false
	}
	if entry.poolKey != poolKey {
		return false
	}
	return true
}

// removeClientLocked 移除客户端（需持有锁）
// 从缓存中删除并关闭空闲连接
//
// 参数:
//   - key: 缓存键
//   - entry: 客户端条目
func (s *httpUpstreamService) removeClientLocked(key string, entry *upstreamClientEntry) {
	delete(s.clients, key)
	if entry != nil && entry.client != nil {
		// 关闭空闲连接，释放系统资源
		// 注意：这不会中断活跃连接
		entry.client.CloseIdleConnections()
	}
}

// evictIdleLocked 淘汰空闲超时的客户端（需持有锁）
// 遍历所有客户端，移除超过 TTL 且无活跃请求的条目
//
// 参数:
//   - now: 当前时间
func (s *httpUpstreamService) evictIdleLocked(now time.Time) {
	ttl := s.clientIdleTTL()
	if ttl <= 0 {
		return
	}
	// 计算淘汰截止时间
	cutoff := now.Add(-ttl).UnixNano()
	for key, entry := range s.clients {
		// 跳过有活跃请求的客户端
		if atomic.LoadInt64(&entry.inFlight) != 0 {
			continue
		}
		// 淘汰超时的空闲客户端
		if atomic.LoadInt64(&entry.lastUsed) <= cutoff {
			s.removeClientLocked(key, entry)
		}
	}
}

// evictOldestIdleLocked 淘汰最久未使用且无活跃请求的客户端（需持有锁）
func (s *httpUpstreamService) evictOldestIdleLocked() bool {
	var (
		oldestKey   string
		oldestEntry *upstreamClientEntry
		oldestTime  int64
	)
	// 查找最久未使用且无活跃请求的客户端
	for key, entry := range s.clients {
		// 跳过有活跃请求的客户端
		if atomic.LoadInt64(&entry.inFlight) != 0 {
			continue
		}
		lastUsed := atomic.LoadInt64(&entry.lastUsed)
		if oldestEntry == nil || lastUsed < oldestTime {
			oldestKey = key
			oldestEntry = entry
			oldestTime = lastUsed
		}
	}
	// 所有客户端都有活跃请求，无法淘汰
	if oldestEntry == nil {
		return false
	}
	s.removeClientLocked(oldestKey, oldestEntry)
	return true
}

// evictOverLimitLocked 淘汰超出数量限制的客户端（需持有锁）
// 使用 LRU 策略，优先淘汰最久未使用且无活跃请求的客户端
func (s *httpUpstreamService) evictOverLimitLocked() bool {
	maxClients := s.maxUpstreamClients()
	if maxClients <= 0 {
		return false
	}
	evicted := false
	// 循环淘汰直到满足数量限制
	for len(s.clients) > maxClients {
		if !s.evictOldestIdleLocked() {
			return evicted
		}
		evicted = true
	}
	return evicted
}

// getIsolationMode 获取连接池隔离模式
// 从配置中读取，无效值回退到 account_proxy 模式
//
// 返回:
//   - string: 隔离模式（proxy/account/account_proxy）
func (s *httpUpstreamService) getIsolationMode() string {
	if s.cfg == nil {
		return config.ConnectionPoolIsolationAccountProxy
	}
	mode := strings.ToLower(strings.TrimSpace(s.cfg.Gateway.ConnectionPoolIsolation))
	if mode == "" {
		return config.ConnectionPoolIsolationAccountProxy
	}
	switch mode {
	case config.ConnectionPoolIsolationProxy, config.ConnectionPoolIsolationAccount, config.ConnectionPoolIsolationAccountProxy:
		return mode
	default:
		return config.ConnectionPoolIsolationAccountProxy
	}
}

// maxUpstreamClients 获取最大客户端缓存数量
// 从配置中读取，无效值使用默认值
func (s *httpUpstreamService) maxUpstreamClients() int {
	if s.cfg == nil {
		return defaultMaxUpstreamClients
	}
	if s.cfg.Gateway.MaxUpstreamClients > 0 {
		return s.cfg.Gateway.MaxUpstreamClients
	}
	return defaultMaxUpstreamClients
}

// clientIdleTTL 获取客户端空闲回收阈值
// 从配置中读取，无效值使用默认值
func (s *httpUpstreamService) clientIdleTTL() time.Duration {
	if s.cfg == nil {
		return time.Duration(defaultClientIdleTTLSeconds) * time.Second
	}
	if s.cfg.Gateway.ClientIdleTTLSeconds > 0 {
		return time.Duration(s.cfg.Gateway.ClientIdleTTLSeconds) * time.Second
	}
	return time.Duration(defaultClientIdleTTLSeconds) * time.Second
}

// minAccountPoolConns 是账号级连接池的下限。
//
// 单个"客户端请求"往往需要多条上游连接：主转发 + websearch agentic 循环 +
// usage_limits 查询 + token 刷新。把上限直接压成 account.Concurrency 会让这些
// 辅助请求和主请求互相抢槽位，并发一高就在 transport 层排队甚至超时。
const minAccountPoolConns = 16

// accountPoolHeadroom 是账号并发数到连接数的放大系数，理由同上。
const accountPoolHeadroom = 4

// resolvePoolSettings 解析连接池配置
// 根据隔离策略和账户并发数动态调整连接池参数
//
// 参数:
//   - isolation: 隔离模式
//   - accountConcurrency: 账户并发限制
//
// 返回:
//   - poolSettings: 连接池配置
func (s *httpUpstreamService) resolvePoolSettings(isolation string, accountConcurrency int) poolSettings {
	settings := defaultPoolSettings(s.cfg)
	if !isAccountScopedIsolation(isolation) || accountConcurrency <= 0 {
		return settings
	}

	// 账户隔离模式下，根据账户并发数调整连接池大小。
	//
	// 注意这里不再把上限设成 accountConcurrency 本身：并发控制由上层的
	// 并发槽位（concurrency slot）负责，连接池只需保证不成为额外瓶颈。
	// 之前 1:1 绑定会导致辅助请求（websearch/usage_limits）和主转发抢连接，
	// 在 HTTP/1.1 下尤其明显。
	limit := accountPoolLimit(accountConcurrency)

	// 每个字段各自不超过**其自身配置**的上限：
	//   - 不能因为放大系数就顶穿运维显式配置的 max_idle_conns_per_host
	//     （那是用来约束内存/fd 的）
	//   - 也不能反过来把配置得比默认值更大的 max_conns_per_host 压回默认值
	//     （capPositive 只在 configured > 0 且确实更小时才收口）
	settings.maxConnsPerHost = capPositive(limit, settings.maxConnsPerHost)
	settings.maxIdleConns = capPositive(limit, settings.maxIdleConns)
	settings.maxIdleConnsPerHost = capPositive(limit, settings.maxIdleConnsPerHost)
	return settings
}

// isAccountScopedIsolation 判断隔离策略是否按账号维度切分连接池。
func isAccountScopedIsolation(isolation string) bool {
	return isolation == config.ConnectionPoolIsolationAccount ||
		isolation == config.ConnectionPoolIsolationAccountProxy
}

// accountPoolLimit 由账号并发数推导期望连接数，只负责放大 + 下限 + 防溢出。
//
// 这里刻意**不**施加任何上限：上限属于配置语义，由调用方按字段用
// capPositive 收口。之前在这里硬编码 defaultMaxConnsPerHost 收口，
// 会让显式配置的 gateway.max_conns_per_host（>240 时）失效 ——
// 高并发账号被静默压回 240，配置形同虚设。
//
// 防溢出不是洁癖：accountConcurrency 来自 DB，异常大的值乘以放大系数会翻成
// 负数。Go 的 tryPutIdleConn 判定是 `len(idles) >= t.maxIdleConnsPerHost()`，
// 负数上限会让任意 len（>=0）都命中 errTooManyIdleHost → **从不缓存空闲连接**
// → 彻底关掉连接复用，正是本次要修的问题被反向引入。
// 溢出时饱和到 math.MaxInt，交由 capPositive 按配置收口。
func accountPoolLimit(accountConcurrency int) int {
	if accountConcurrency <= 0 {
		return minAccountPoolConns
	}
	if accountConcurrency > math.MaxInt/accountPoolHeadroom {
		return math.MaxInt
	}
	limit := accountConcurrency * accountPoolHeadroom
	if limit < minAccountPoolConns {
		return minAccountPoolConns
	}
	return limit
}

// capPositive 在 configured > 0 时把 value 限制在 configured 以内。
// configured <= 0 表示"无限制"，此时不做限制。
func capPositive(value, configured int) int {
	if configured > 0 && value > configured {
		return configured
	}
	return value
}

// buildPoolKey 构建连接池配置键
// 用于检测配置变更，配置变更时需要重建客户端
//
// 参数:
//   - isolation: 隔离模式
//   - accountConcurrency: 账户并发限制
//
// 返回:
//   - string: 配置键
//
// 关键点：key 必须由**解析后**的连接池参数派生，而不是原始的 accountConcurrency。
// 否则同一账号的不同调用方传入不同并发数（例如 token 刷新传 1、主转发传 N）
// 会算出不同的 poolKey，而两者的 cacheKey 相同 → shouldReuseEntry 判定不可复用
// → 每次交替调用都销毁并重建整个 transport（连带 CloseIdleConnections），
// 高并发下会持续抖动，连接完全无法复用。
func (s *httpUpstreamService) buildPoolKey(isolation string, accountConcurrency int) string {
	if isolation == config.ConnectionPoolIsolationAccount || isolation == config.ConnectionPoolIsolationAccountProxy {
		if accountConcurrency > 0 {
			settings := s.resolvePoolSettings(isolation, accountConcurrency)
			return fmt.Sprintf("account:%d", settings.maxConnsPerHost)
		}
	}
	return "default"
}

// buildCacheKey 构建客户端缓存键
// 根据隔离策略决定缓存键的组成
//
// 参数:
//   - isolation: 隔离模式
//   - proxyKey: 代理标识
//   - accountID: 账户 ID
//
// 返回:
//   - string: 缓存键
//
// 缓存键格式:
//   - proxy 模式: "proxy:{proxyKey}"
//   - account 模式: "account:{accountID}"
//   - account_proxy 模式: "account:{accountID}|proxy:{proxyKey}"
func buildCacheKey(isolation, proxyKey string, accountID int64) string {
	switch isolation {
	case config.ConnectionPoolIsolationAccount:
		return fmt.Sprintf("account:%d", accountID)
	case config.ConnectionPoolIsolationAccountProxy:
		return fmt.Sprintf("account:%d|proxy:%s", accountID, proxyKey)
	default:
		return fmt.Sprintf("proxy:%s", proxyKey)
	}
}

// normalizeProxyURL 标准化代理 URL
// 处理空值和解析错误，返回标准化的键和解析后的 URL
//
// 参数:
//   - raw: 原始代理 URL 字符串
//
// 返回:
//   - string: 标准化的代理键（空或解析失败返回 "direct"）
//   - *url.URL: 解析后的 URL（空或解析失败返回 nil）
func normalizeProxyURL(raw string) (string, *url.URL) {
	proxyURL := strings.TrimSpace(raw)
	if proxyURL == "" {
		return directProxyKey, nil
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return directProxyKey, nil
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.ForceQuery = false
	if hostname := parsed.Hostname(); hostname != "" {
		port := parsed.Port()
		if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
			port = ""
		}
		hostname = strings.ToLower(hostname)
		if port != "" {
			parsed.Host = net.JoinHostPort(hostname, port)
		} else {
			parsed.Host = hostname
		}
	}
	return parsed.String(), parsed
}

// defaultPoolSettings 获取默认连接池配置
// 从全局配置中读取，无效值使用常量默认值
//
// 参数:
//   - cfg: 全局配置
//
// 返回:
//   - poolSettings: 连接池配置
func defaultPoolSettings(cfg *config.Config) poolSettings {
	maxIdleConns := defaultMaxIdleConns
	maxIdleConnsPerHost := defaultMaxIdleConnsPerHost
	maxConnsPerHost := defaultMaxConnsPerHost
	idleConnTimeout := defaultIdleConnTimeout
	responseHeaderTimeout := defaultResponseHeaderTimeout

	if cfg != nil {
		if cfg.Gateway.MaxIdleConns > 0 {
			maxIdleConns = cfg.Gateway.MaxIdleConns
		}
		if cfg.Gateway.MaxIdleConnsPerHost > 0 {
			maxIdleConnsPerHost = cfg.Gateway.MaxIdleConnsPerHost
		}
		if cfg.Gateway.MaxConnsPerHost >= 0 {
			maxConnsPerHost = cfg.Gateway.MaxConnsPerHost
		}
		if cfg.Gateway.IdleConnTimeoutSeconds > 0 {
			idleConnTimeout = time.Duration(cfg.Gateway.IdleConnTimeoutSeconds) * time.Second
		}
		if cfg.Gateway.ResponseHeaderTimeout > 0 {
			responseHeaderTimeout = time.Duration(cfg.Gateway.ResponseHeaderTimeout) * time.Second
		}
	}

	return poolSettings{
		maxIdleConns:          maxIdleConns,
		maxIdleConnsPerHost:   maxIdleConnsPerHost,
		maxConnsPerHost:       maxConnsPerHost,
		idleConnTimeout:       idleConnTimeout,
		responseHeaderTimeout: responseHeaderTimeout,
	}
}

// buildUpstreamTransport 构建上游请求的 Transport
// 使用配置文件中的连接池参数，支持生产环境调优
//
// 参数:
//   - settings: 连接池配置
//   - proxyURL: 代理 URL（nil 表示直连）
//
// 返回:
//   - *http.Transport: 配置好的 Transport 实例
//   - error: 代理配置错误
//
// Transport 参数说明:
//   - MaxIdleConns: 所有主机的最大空闲连接总数
//   - MaxIdleConnsPerHost: 每主机最大空闲连接数（影响连接复用率）
//   - MaxConnsPerHost: 每主机最大连接数（达到后新请求等待）
//   - IdleConnTimeout: 空闲连接超时（超时后关闭）
//   - ResponseHeaderTimeout: 等待响应头超时（不影响流式传输）
func buildUpstreamTransport(settings poolSettings, proxyURL *url.URL) (*http.Transport, error) {
	transport := &http.Transport{
		MaxIdleConns:          settings.maxIdleConns,
		MaxIdleConnsPerHost:   settings.maxIdleConnsPerHost,
		MaxConnsPerHost:       settings.maxConnsPerHost,
		IdleConnTimeout:       settings.idleConnTimeout,
		ResponseHeaderTimeout: settings.responseHeaderTimeout,
		// ForceAttemptHTTP2: 即使自定义了 DialContext/DialTLSContext 也尝试协商 HTTP/2。
		// 之前没设这个值，加上 DialTLSContext 非 nil，Go 会完全跳过 HTTP/2 自动升级，
		// 导致所有上游请求都退化成 HTTP/1.1 —— 注释里写的"HTTP/2 多路复用"从未生效。
		ForceAttemptHTTP2: true,
	}
	if err := proxyutil.ConfigureTransportProxy(transport, proxyURL); err != nil {
		return nil, err
	}

	// 🔥 Enable uTLS for Anthropic domains to bypass TLS fingerprinting
	transport = wrapWithUTLSDialer(transport)

	return transport, nil
}

// trackedBody 带跟踪功能的响应体包装器
// 在 Close 时执行回调，用于更新请求计数
type trackedBody struct {
	io.ReadCloser // 原始响应体
	once          sync.Once
	onClose       func() // 关闭时的回调函数
}

// Close 关闭响应体并执行回调
// 使用 sync.Once 确保回调只执行一次
func (b *trackedBody) Close() error {
	err := b.ReadCloser.Close()
	if b.onClose != nil {
		b.once.Do(b.onClose)
	}
	return err
}

// wrapTrackedBody 包装响应体以跟踪关闭事件
// 用于在响应体关闭时更新 inFlight 计数
//
// 参数:
//   - body: 原始响应体
//   - onClose: 关闭时的回调函数
//
// 返回:
//   - io.ReadCloser: 包装后的响应体
func wrapTrackedBody(body io.ReadCloser, onClose func()) io.ReadCloser {
	if body == nil {
		return body
	}
	return &trackedBody{ReadCloser: body, onClose: onClose}
}

// needsUTLSFingerprint 判断目标主机是否需要 uTLS 指纹伪装。
// 仅 Anthropic 系域名需要绕过 Cloudflare 的 TLS 指纹检测。
func needsUTLSFingerprint(host string) bool {
	host = strings.ToLower(host)
	return strings.Contains(host, "anthropic.com") || strings.Contains(host, "claude.ai")
}

// wrapWithUTLSDialer 为需要指纹伪装的域名启用 uTLS，其余域名走标准 crypto/tls。
//
// 为什么要按域名区分：Go 的 transport 在 dialConn 里用
// `pconn.conn.(*tls.Conn)` 断言来提取 TLS 状态并决定是否切到 HTTP/2
// (net/http/transport.go)。utls.UConn 不是 *tls.Conn，断言失败 → tlsState 为 nil
// → 永远不会启用 HTTP/2。也就是说 uTLS 与 h2 互斥。
//
// 之前的实现对所有域名都套 uTLS，导致全部上游请求被锁死在 HTTP/1.1。
// 对 Kiro 这类不需要指纹伪装、但并发很高的上游，HTTP/1.1 + 无连接复用
// 会造成握手风暴和连接槽位排队，是 502 的主要成因之一。
//
// 现在的分工：
//   - Anthropic 域名：uTLS + Firefox 指纹，ALPN 强制只宣告 http/1.1
//     （详见 firefoxHTTP1Spec 的注释：之前会协商出 h2 但按 h1 说话，服务端直接断连）
//   - 其他域名（含 Kiro）：标准 crypto/tls，ALPN 宣告 h2 + http/1.1，
//     返回真正的 *tls.Conn 让 Go 能正常升级到 HTTP/2
//
// 注意：本函数只在 Go 走"自定义 TLS dialer"分支时生效，即目标为 https 且
// 未经 HTTP 代理。经 http:// 代理时 cm.scheme() 是 "http"，Go 会自己 CONNECT
// 再用标准 crypto/tls 握手，uTLS 不参与（这是既有行为，非本次改动引入）。
func wrapWithUTLSDialer(transport *http.Transport) *http.Transport {
	// Store original dialer
	originalDialContext := transport.DialContext
	if originalDialContext == nil {
		dialer := &net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}
		originalDialContext = dialer.DialContext
	}

	baseTLSConfig := transport.TLSClientConfig

	transport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		// Extract hostname for SNI
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			host = addr
		}

		// Dial TCP connection
		conn, err := originalDialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}

		if !needsUTLSFingerprint(host) {
			// 标准 TLS 路径：返回 *tls.Conn，Go 才能读到 ALPN 结果并升级 h2
			stdConfig := &tls.Config{
				MinVersion: tls.VersionTLS12,
			}
			if baseTLSConfig != nil {
				stdConfig = baseTLSConfig.Clone()
			}
			stdConfig.ServerName = host
			if len(stdConfig.NextProtos) == 0 {
				stdConfig.NextProtos = []string{"h2", "http/1.1"}
			}
			tlsConn := tls.Client(conn, stdConfig)
			if hsErr := tlsConn.HandshakeContext(ctx); hsErr != nil {
				conn.Close()
				return nil, fmt.Errorf("tls handshake failed for %s: %w", host, hsErr)
			}
			return tlsConn, nil
		}

		// uTLS 路径：Firefox 指纹，但 ALPN 必须只宣告 http/1.1（原因见下）
		tlsConfig := &utls.Config{
			ServerName:         host,
			InsecureSkipVerify: false,
		}
		if baseTLSConfig != nil {
			tlsConfig.InsecureSkipVerify = baseTLSConfig.InsecureSkipVerify
			tlsConfig.RootCAs = baseTLSConfig.RootCAs
		}
		tlsConn := utls.UClient(conn, tlsConfig, utls.HelloCustom)
		spec, err := firefoxHTTP1Spec()
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("build utls spec for %s: %w", host, err)
		}
		if err := tlsConn.ApplyPreset(spec); err != nil {
			conn.Close()
			return nil, fmt.Errorf("apply utls spec for %s: %w", host, err)
		}
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, fmt.Errorf("tls handshake failed for %s: %w", host, err)
		}

		return tlsConn, nil
	}

	return transport
}

// firefoxHTTP1Spec 返回 Firefox 指纹的 ClientHelloSpec，但把 ALPN 改成只宣告
// http/1.1。
//
// 为什么必须改 spec 而不是设 utls.Config.NextProtos：
// utls 的 ALPNExtension.writeToUConn 会**反向覆盖** uc.config.NextProtos
// （见 u_tls_extensions.go），所以对预置 ClientHelloID 来说 Config.NextProtos
// 是被忽略的，预置 spec 里硬编码的 "h2,http/1.1" 永远生效。
//
// 这会造成一个隐蔽故障：ALPN 协商出 h2，但 Go 的 transport 拿到的是
// *utls.UConn，`pconn.conn.(*tls.Conn)` 断言失败 → tlsState 为 nil →
// 不会切到 HTTP/2，于是仍按 HTTP/1.1 在一条 h2 连接上说话。
// 服务端（实测 api.anthropic.com 会协商到 h2）收到的是 "GET / HTTP/1.1"
// 而非 h2 preface，直接判为 bogus greeting 并断开。
//
// 因为 uTLS 与 h2 在当前实现下互斥（无法从 UConn 提取 tlsState），
// 唯一自洽的选择是让 ALPN 与实际使用的协议一致 —— 只宣告 http/1.1。
// 除 ALPN 之外的指纹特征（密码套件、扩展顺序、曲线等）仍保持 Firefox 原样。
func firefoxHTTP1Spec() (*utls.ClientHelloSpec, error) {
	spec, err := utls.UTLSIdToSpec(utls.HelloFirefox_Auto)
	if err != nil {
		return nil, err
	}
	for _, ext := range spec.Extensions {
		if alpn, ok := ext.(*utls.ALPNExtension); ok {
			alpn.AlpnProtocols = []string{"http/1.1"}
		}
	}
	return &spec, nil
}
