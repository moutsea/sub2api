package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// newJitterService creates a minimal KiroGatewayService for jitter tests.
func newJitterService(minMs, maxMs int) *KiroGatewayService {
	return &KiroGatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{
				RequestJitterMinMs: minMs,
				RequestJitterMaxMs: maxMs,
			},
		},
	}
}

func TestApplyRequestJitter_Disabled(t *testing.T) {
	// max=0 means jitter is disabled; should return immediately.
	svc := newJitterService(0, 0)
	ctx := context.Background()

	start := time.Now()
	svc.applyRequestJitter(ctx)
	elapsed := time.Since(start)

	if elapsed > 5*time.Millisecond {
		t.Errorf("expected immediate return when jitter disabled, took %v", elapsed)
	}
}

func TestApplyRequestJitter_MinEqualsMax(t *testing.T) {
	// min=max=50 should sleep ~50ms.
	svc := newJitterService(50, 50)
	ctx := context.Background()

	start := time.Now()
	svc.applyRequestJitter(ctx)
	elapsed := time.Since(start)

	if elapsed < 40*time.Millisecond {
		t.Errorf("expected ~50ms sleep, got %v (too short)", elapsed)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("expected ~50ms sleep, got %v (too long)", elapsed)
	}
}

func TestApplyRequestJitter_Range(t *testing.T) {
	// min=20, max=80 should sleep between 20-80ms.
	svc := newJitterService(20, 80)
	ctx := context.Background()

	// Run multiple times to exercise the random range.
	for i := 0; i < 5; i++ {
		start := time.Now()
		svc.applyRequestJitter(ctx)
		elapsed := time.Since(start)

		// Allow some OS scheduling tolerance.
		if elapsed < 15*time.Millisecond {
			t.Errorf("iteration %d: expected >=20ms sleep, got %v", i, elapsed)
		}
		if elapsed > 150*time.Millisecond {
			t.Errorf("iteration %d: expected <=80ms sleep, got %v", i, elapsed)
		}
	}
}

func TestApplyRequestJitter_ContextCancelled(t *testing.T) {
	// With a large jitter window but already-cancelled context, should return immediately.
	svc := newJitterService(500, 1000)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before calling

	start := time.Now()
	svc.applyRequestJitter(ctx)
	elapsed := time.Since(start)

	if elapsed > 10*time.Millisecond {
		t.Errorf("expected immediate return on cancelled context, took %v", elapsed)
	}
}

func TestApplyRequestJitter_NilConfig(t *testing.T) {
	// nil config should return immediately without panic.
	svc := &KiroGatewayService{cfg: nil}
	ctx := context.Background()

	start := time.Now()
	svc.applyRequestJitter(ctx)
	elapsed := time.Since(start)

	if elapsed > 5*time.Millisecond {
		t.Errorf("expected immediate return with nil config, took %v", elapsed)
	}
}

func TestApplyRequestJitter_MinGreaterThanMax(t *testing.T) {
	// When min > max, the method clamps min to max. Should sleep ~30ms.
	svc := newJitterService(100, 30)
	ctx := context.Background()

	start := time.Now()
	svc.applyRequestJitter(ctx)
	elapsed := time.Since(start)

	if elapsed < 20*time.Millisecond {
		t.Errorf("expected ~30ms sleep (clamped), got %v (too short)", elapsed)
	}
	if elapsed > 80*time.Millisecond {
		t.Errorf("expected ~30ms sleep (clamped), got %v (too long)", elapsed)
	}
}

func TestApplyRequestJitter_ContextCancelledDuringSleep(t *testing.T) {
	// Context cancelled mid-sleep should abort early.
	svc := newJitterService(500, 500)
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after 30ms — well before the 500ms jitter would complete.
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	svc.applyRequestJitter(ctx)
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Errorf("expected early return on context cancel (~30ms), got %v", elapsed)
	}
}
