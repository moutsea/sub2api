package service

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"time"
)

const (
	defaultGrokStreamIdleTimeout   = 180 * time.Second
	defaultGrokNonStreamingTimeout = 10 * time.Minute
)

func resolveGrokStreamIdleTimeout(configuredSeconds int, account *Account) time.Duration {
	if configuredSeconds > 0 {
		return time.Duration(configuredSeconds) * time.Second
	}
	if account != nil && account.IsGrok() {
		return defaultGrokStreamIdleTimeout
	}
	return 0
}

func grokStreamIdleFailoverError(account *Account, idle time.Duration) *UpstreamFailoverError {
	if idle <= 0 {
		idle = defaultGrokStreamIdleTimeout
	}
	return &UpstreamFailoverError{
		StatusCode: http.StatusBadGateway,
		Message:    fmt.Sprintf("Grok upstream stream idle timeout after %s", idle.Round(time.Second)),
	}
}

// detachUpstreamContext keeps request values while preventing a client
// disconnect from cancelling a slow upstream request. The response path still
// uses the original context, so callers can stop writing as soon as the client
// goes away without abandoning the upstream connection prematurely.
func detachUpstreamContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return context.Background(), func() {}
	}
	return context.WithoutCancel(ctx), func() {}
}

func grokUpstreamContext(ctx context.Context, stream bool) (context.Context, context.CancelFunc) {
	base, _ := detachUpstreamContext(ctx)
	if stream {
		return base, func() {}
	}
	return context.WithTimeout(base, defaultGrokNonStreamingTimeout)
}

func (s *OpenAIGatewayService) applyGrokRequestJitter(ctx context.Context) {
	if s == nil || s.cfg == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	minMs := s.cfg.Gateway.RequestJitterMinMs
	maxMs := s.cfg.Gateway.RequestJitterMaxMs
	if maxMs <= 0 {
		return
	}
	if minMs < 0 {
		minMs = 0
	}
	if maxMs < minMs {
		maxMs = minMs
	}
	delayMs := minMs
	if maxMs > minMs {
		delayMs += rand.Intn(maxMs - minMs + 1)
	}
	timer := time.NewTimer(time.Duration(delayMs) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}
