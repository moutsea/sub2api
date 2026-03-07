//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type rateLimitAccountRepoStub struct {
	mockAccountRepoForGemini
	setErrorCalls      int
	tempCalls          int
	setRateLimitedCall int
	lastErrorMsg       string
	lastRateLimitReset time.Time
}

func (r *rateLimitAccountRepoStub) SetError(ctx context.Context, id int64, errorMsg string) error {
	r.setErrorCalls++
	r.lastErrorMsg = errorMsg
	return nil
}

func (r *rateLimitAccountRepoStub) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	r.tempCalls++
	return nil
}

func (r *rateLimitAccountRepoStub) SetRateLimited(ctx context.Context, id int64, resetAt time.Time) error {
	r.setRateLimitedCall++
	r.lastRateLimitReset = resetAt
	return nil
}

type tokenCacheInvalidatorRecorder struct {
	accounts []*Account
	err      error
}

func (r *tokenCacheInvalidatorRecorder) InvalidateToken(ctx context.Context, account *Account) error {
	r.accounts = append(r.accounts, account)
	return r.err
}

func TestRateLimitService_HandleUpstreamError_OAuth401MarksError(t *testing.T) {
	tests := []struct {
		name     string
		platform string
	}{
		{name: "gemini", platform: PlatformGemini},
		{name: "antigravity", platform: PlatformAntigravity},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &rateLimitAccountRepoStub{}
			invalidator := &tokenCacheInvalidatorRecorder{}
			service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			service.SetTokenCacheInvalidator(invalidator)
			account := &Account{
				ID:       100,
				Platform: tt.platform,
				Type:     AccountTypeOAuth,
				Credentials: map[string]any{
					"temp_unschedulable_enabled": true,
					"temp_unschedulable_rules": []any{
						map[string]any{
							"error_code":       401,
							"keywords":         []any{"unauthorized"},
							"duration_minutes": 30,
							"description":      "custom rule",
						},
					},
				},
			}

			shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

			require.True(t, shouldDisable)
			require.Equal(t, 1, repo.setErrorCalls)
			require.Equal(t, 0, repo.tempCalls)
			require.Contains(t, repo.lastErrorMsg, "Authentication failed (401)")
			require.Len(t, invalidator.accounts, 1)
		})
	}
}

func TestRateLimitService_HandleUpstreamError_OAuth401InvalidatorError(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	invalidator := &tokenCacheInvalidatorRecorder{err: errors.New("boom")}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetTokenCacheInvalidator(invalidator)
	account := &Account{
		ID:       101,
		Platform: PlatformGemini,
		Type:     AccountTypeOAuth,
	}

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Len(t, invalidator.accounts, 1)
}

func TestRateLimitService_HandleUpstreamError_NonOAuth401(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	invalidator := &tokenCacheInvalidatorRecorder{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	service.SetTokenCacheInvalidator(invalidator)
	account := &Account{
		ID:       102,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
	}

	shouldDisable := service.HandleUpstreamError(context.Background(), account, 401, http.Header{}, []byte("unauthorized"))

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Empty(t, invalidator.accounts)
}

func TestRateLimitService_HandleUpstreamError_OpenAI429RetryAfter(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       201,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
	}

	start := time.Now()
	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		http.Header{"Retry-After": []string{"2"}},
		[]byte(`{"error":{"message":"Rate limit reached"}}`),
	)

	require.False(t, shouldDisable)
	require.Equal(t, 1, repo.setRateLimitedCall)
	require.WithinDuration(t, start.Add(2*time.Second), repo.lastRateLimitReset, 1500*time.Millisecond)
}

func TestRateLimitService_HandleUpstreamError_OpenAI429DefaultFallback(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       202,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
	}

	start := time.Now()
	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		http.Header{},
		[]byte(`{"error":{"message":"Rate limit exceeded"}}`),
	)

	require.False(t, shouldDisable)
	require.Equal(t, 1, repo.setRateLimitedCall)
	require.WithinDuration(t, start.Add(30*time.Second), repo.lastRateLimitReset, 2*time.Second)
}

func TestRateLimitService_HandleUpstreamError_Anthropic429UsesResetHeader(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       203,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
	}

	target := time.Now().Add(90 * time.Second).Truncate(time.Second)
	headers := http.Header{
		"Anthropic-Ratelimit-Unified-Reset": []string{strconv.FormatInt(target.Unix(), 10)},
	}
	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		headers,
		[]byte(`{"error":{"message":"Rate limit exceeded"}}`),
	)

	require.False(t, shouldDisable)
	require.Equal(t, 1, repo.setRateLimitedCall)
	require.WithinDuration(t, target, repo.lastRateLimitReset, time.Second)
}

func TestRateLimitService_HandleUpstreamError_Anthropic429HeaderPrecedence(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       205,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
	}

	target := time.Now().Add(80 * time.Second).Truncate(time.Second)
	headers := http.Header{
		"Anthropic-Ratelimit-Unified-Reset": []string{strconv.FormatInt(target.Unix(), 10)},
		"Retry-After":                       []string{"2"},
	}
	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		headers,
		[]byte(`{"error":{"message":"Rate limit exceeded"}}`),
	)

	require.False(t, shouldDisable)
	require.Equal(t, 1, repo.setRateLimitedCall)
	require.WithinDuration(t, target, repo.lastRateLimitReset, time.Second)
}

func TestRateLimitService_HandleUpstreamError_OpenAI429UsesOpenAIResetHeader(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       204,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
	}

	start := time.Now()
	headers := http.Header{
		"X-Ratelimit-Reset-Requests": []string{"1500ms"},
	}
	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		headers,
		[]byte(`{"error":{"message":"Rate limit reached"}}`),
	)

	require.False(t, shouldDisable)
	require.Equal(t, 1, repo.setRateLimitedCall)
	require.WithinDuration(t, start.Add(1500*time.Millisecond), repo.lastRateLimitReset, 800*time.Millisecond)
}

func TestRateLimitService_HandleUpstreamError_OpenAI429HeaderPrecedence(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{
		ID:       206,
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
	}

	start := time.Now()
	headers := http.Header{
		"Retry-After":                []string{"2"},
		"X-Ratelimit-Reset-Requests": []string{"1500ms"},
	}
	shouldDisable := service.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		headers,
		[]byte(`{"error":{"message":"Rate limit reached"}}`),
	)

	require.False(t, shouldDisable)
	require.Equal(t, 1, repo.setRateLimitedCall)
	require.WithinDuration(t, start.Add(2*time.Second), repo.lastRateLimitReset, 1500*time.Millisecond)
}
