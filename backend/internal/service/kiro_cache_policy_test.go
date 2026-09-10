package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

func TestApplyKiroSimulatedCacheReadRatio(t *testing.T) {
	tests := []struct {
		name      string
		ratio     float64
		wantRead  int
		wantWrite int
	}{
		{name: "half", ratio: 0.5, wantRead: 50, wantWrite: 20},
		{name: "zero", ratio: 0, wantRead: 0, wantWrite: 20},
		{name: "invalid", ratio: 2, wantRead: 100, wantWrite: 20},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ratio := test.ratio
			svc := &KiroGatewayService{cfg: &config.Config{Kiro: config.KiroConfig{SimulatedCacheReadRatio: &ratio}}}
			read, write := svc.applyKiroSimulatedCacheReadRatio(100, 20)
			if read != test.wantRead || write != test.wantWrite {
				t.Fatalf("got read=%d write=%d, want read=%d write=%d", read, write, test.wantRead, test.wantWrite)
			}
		})
	}
}
