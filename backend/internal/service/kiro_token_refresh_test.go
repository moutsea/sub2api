//go:build unit

package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// kiroRefreshRepo 包装现有的账号仓储 mock，额外记录 Update 调用并可注入延迟。
type kiroRefreshRepo struct {
	*mockAccountRepoForPlatform
	updateCalls  atomic.Int32
	getByIDDelay time.Duration
	mu           sync.Mutex
	updated      []*Account
}

func (r *kiroRefreshRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	if r.getByIDDelay > 0 {
		time.Sleep(r.getByIDDelay)
	}
	return r.mockAccountRepoForPlatform.GetByID(ctx, id)
}

func (r *kiroRefreshRepo) Update(ctx context.Context, account *Account) error {
	r.updateCalls.Add(1)
	r.mu.Lock()
	r.updated = append(r.updated, account)
	r.mu.Unlock()
	return nil
}

func newKiroRefreshRepo(account *Account) *kiroRefreshRepo {
	return &kiroRefreshRepo{
		mockAccountRepoForPlatform: &mockAccountRepoForPlatform{
			accountsByID: map[int64]*Account{account.ID: account},
		},
	}
}

func newKiroTestAccount(id int64) *Account {
	return &Account{
		ID:          id,
		Platform:    PlatformKiro,
		Concurrency: 8,
		Credentials: map[string]any{
			"auth_type":     "social",
			"refresh_token": "refresh-v1",
			"access_token":  "access-v1",
		},
	}
}

// TestCachedAccessTokenReturnsValidToken 验证未过期的 token 直接命中内存态，
// 不触发任何 DB / 网络调用。
func TestCachedAccessTokenReturnsValidToken(t *testing.T) {
	account := newKiroTestAccount(1)
	repo := newKiroRefreshRepo(account)
	p := NewKiroTokenProvider(repo, nil)

	state := p.getOrCreateState(account.ID)
	state.AccessToken = "access-valid"
	state.ExpiresAt = time.Now().Add(1 * time.Hour)
	state.Status = KiroTokenStatusActive

	token, err := p.cachedAccessToken(state, account)
	if err != nil {
		t.Fatalf("cachedAccessToken returned error: %v", err)
	}
	if token != "access-valid" {
		t.Fatalf("token = %q, want access-valid", token)
	}
	if repo.updateCalls.Load() != 0 {
		t.Fatalf("expected no DB writes for a cache hit, got %d", repo.updateCalls.Load())
	}
}

// TestCachedAccessTokenRejectsUnavailableStates 验证 cooldown/banned/exhausted
// 三种状态都会返回错误，而不是放行一个空 token。
func TestCachedAccessTokenRejectsUnavailableStates(t *testing.T) {
	cases := []struct {
		name  string
		setup func(s *KiroTokenState)
	}{
		{"cooldown", func(s *KiroTokenState) {
			s.Status = KiroTokenStatusCooldown
			s.CooldownUntil = time.Now().Add(time.Minute)
		}},
		{"banned", func(s *KiroTokenState) { s.Status = KiroTokenStatusBanned }},
		{"exhausted", func(s *KiroTokenState) { s.Status = KiroTokenStatusExhausted }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := newKiroTestAccount(2)
			p := NewKiroTokenProvider(newKiroRefreshRepo(account), nil)
			state := p.getOrCreateState(account.ID)
			// 预置一个非零 ExpiresAt，避免走 initializeStateFromAccount
			state.ExpiresAt = time.Now().Add(time.Hour)
			state.AccessToken = "x"
			tc.setup(state)

			if _, err := p.cachedAccessToken(state, account); err == nil {
				t.Fatalf("expected error for %s state", tc.name)
			}
		})
	}
}

// TestCachedAccessTokenDoesNotBlockOnConcurrentReads 验证 state.mu 只保护内存态。
//
// 回归目标：刷新以前是在 state.mu 内做的（含 DB 读 + HTTP + DB 写），
// 该账号所有并发请求都会阻塞在锁上，一次慢刷新能把整批请求拖到超时。
// 现在锁内只有内存读写，大量并发命中缓存时应当迅速返回。
func TestCachedAccessTokenDoesNotBlockOnConcurrentReads(t *testing.T) {
	account := newKiroTestAccount(3)
	p := NewKiroTokenProvider(newKiroRefreshRepo(account), nil)

	state := p.getOrCreateState(account.ID)
	state.AccessToken = "access-valid"
	state.ExpiresAt = time.Now().Add(time.Hour)
	state.Status = KiroTokenStatusActive

	const goroutines = 64
	var wg sync.WaitGroup
	start := time.Now()
	errCh := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.cachedAccessToken(state, account); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("concurrent cachedAccessToken failed: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("64 concurrent cache hits took %v, expected to be fast", elapsed)
	}
}

// TestPersistAccountCredentialsWritesBeforeSwap 验证凭据是同步落库的。
//
// 回归目标：之前是 `go p.updateAccountCredentials(...)`，内存态先换、DB 异步写。
// refresh_token 每次刷新都会轮换，异步写未落地时若发生下一次刷新，
// 会从 DB 读到旧 refresh_token 去刷新，直接失败。
func TestPersistAccountCredentialsWritesBeforeSwap(t *testing.T) {
	account := newKiroTestAccount(4)
	repo := newKiroRefreshRepo(account)
	p := NewKiroTokenProvider(repo, nil)

	tokenInfo := &KiroTokenInfo{
		AccessToken:  "access-v2",
		RefreshToken: "refresh-v2",
		ExpiresAt:    time.Now().Add(time.Hour),
	}

	if err := p.persistAccountCredentials(context.Background(), account.ID, tokenInfo); err != nil {
		t.Fatalf("persistAccountCredentials returned error: %v", err)
	}

	// 同步返回后 DB 写必须已经完成（无需 sleep 等待 goroutine）
	if got := repo.updateCalls.Load(); got != 1 {
		t.Fatalf("updateCalls = %d, want 1 (synchronous write)", got)
	}

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.updated) != 1 {
		t.Fatalf("expected 1 updated account, got %d", len(repo.updated))
	}
	if got := repo.updated[0].Credentials["refresh_token"]; got != "refresh-v2" {
		t.Fatalf("persisted refresh_token = %v, want refresh-v2", got)
	}
	if got := repo.updated[0].Credentials["access_token"]; got != "access-v2" {
		t.Fatalf("persisted access_token = %v, want access-v2", got)
	}
	// token 版本号必须写入，防止缓存竞态
	if _, ok := repo.updated[0].Credentials[TokenVersionKey]; !ok {
		t.Fatal("expected token version to be set")
	}
}

// TestPersistAccountCredentialsSurvivesCallerCancel 验证凭据落库不受发起方
// context 取消影响。
//
// singleflight 会让多个请求共享同一次刷新。如果落库沿用第一个调用者的 ctx，
// 那个客户端一断开就会留下"内存态已换、DB 未落库"的不一致，
// 下一次刷新会从 DB 读到旧 refresh_token 而失败。
// GetAccessToken 用 context.WithoutCancel 隔断了这条传播路径。
func TestPersistAccountCredentialsSurvivesCallerCancel(t *testing.T) {
	account := newKiroTestAccount(6)
	repo := newKiroRefreshRepo(account)
	p := NewKiroTokenProvider(repo, nil)

	// 模拟发起方已断开
	callerCtx, cancel := context.WithCancel(context.Background())
	cancel()

	// 传入与 GetAccessToken 相同的隔断方式
	refreshCtx, cancelRefresh := context.WithTimeout(context.WithoutCancel(callerCtx), kiroRefreshTimeout)
	defer cancelRefresh()

	err := p.persistAccountCredentials(refreshCtx, account.ID, &KiroTokenInfo{
		AccessToken:  "access-v2",
		RefreshToken: "refresh-v2",
		ExpiresAt:    time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("persist failed after caller cancel: %v", err)
	}
	if got := repo.updateCalls.Load(); got != 1 {
		t.Fatalf("updateCalls = %d, want 1 despite caller cancellation", got)
	}
}

// TestApplyCredentialUpdatePreservesOldRefreshTokenWhenAbsent 验证上游未轮换
// refresh_token 时不会把已有值覆盖成空。
func TestApplyCredentialUpdatePreservesOldRefreshTokenWhenAbsent(t *testing.T) {
	account := newKiroTestAccount(5)
	p := NewKiroTokenProvider(newKiroRefreshRepo(account), nil)

	p.applyCredentialUpdate(account, &KiroTokenInfo{
		AccessToken: "access-v2",
		ExpiresAt:   time.Now().Add(time.Hour),
		// RefreshToken 故意留空：上游本次未轮换
	})

	if got := account.Credentials["refresh_token"]; got != "refresh-v1" {
		t.Fatalf("refresh_token = %v, want original refresh-v1 preserved", got)
	}
	if got := account.Credentials["access_token"]; got != "access-v2" {
		t.Fatalf("access_token = %v, want access-v2", got)
	}
}
