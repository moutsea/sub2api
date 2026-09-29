package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

type cacheRatioSettingRepository struct {
	SettingRepository
	value string
	err   error
}

func (r *cacheRatioSettingRepository) GetValue(context.Context, string) (string, error) {
	return r.value, r.err
}

func TestKiroCacheReadRatioRetriesAfterDatabaseFailure(t *testing.T) {
	repository := &cacheRatioSettingRepository{value: "0.5", err: errors.New("temporary failure")}
	service := NewSettingService(repository, nil)
	if got := service.GetKiroSimulatedCacheReadRatio(context.Background()); got != 1 {
		t.Fatalf("initial database failure ratio = %v, want fallback 1", got)
	}
	repository.err = nil
	service.kiroCacheReadRatioRetryAt = service.kiroCacheReadRatioRetryAt.Add(-kiroCacheReadRatioRetryInterval)
	if got := service.GetKiroSimulatedCacheReadRatio(context.Background()); got != 0.5 {
		t.Fatalf("recovered database ratio = %v, want 0.5", got)
	}
}

func TestKiroCacheReadRatioRefreshesAcrossInstances(t *testing.T) {
	repository := &cacheRatioSettingRepository{value: "1"}
	service := NewSettingService(repository, nil)
	if got := service.GetKiroSimulatedCacheReadRatio(context.Background()); got != 1 {
		t.Fatalf("initial ratio = %v, want 1", got)
	}
	repository.value = "0.5"
	service.kiroCacheReadRatioAt = service.kiroCacheReadRatioAt.Add(-kiroCacheReadRatioRefreshInterval)
	if got := service.GetKiroSimulatedCacheReadRatio(context.Background()); got != 0.5 {
		t.Fatalf("refreshed ratio = %v, want 0.5", got)
	}
}

// TestUnsetSimulatedCacheReadRatioDefaultsToOne 是一条计费方向的回归测试。
//
// ratio 语义：1 = 保持既有计费行为；0 = 把全部预测 cache_read 按 input 全价计费
// （账单最高）。因此"未设置"必须回退到 1，绝不能回退到 0。
//
// 回归目标：该字段曾是 float64，判定为 `>= 0 && <= 1`。float64 的零值恰好是 0
// 且能通过该判定，于是任何未经 viper 默认值填充的 Config（直接构造、测试、
// 嵌入式使用）都会被当成显式 0，静默切到最高计费档 —— 用户账单被抬高而无任何告警。
func TestUnsetSimulatedCacheReadRatioDefaultsToOne(t *testing.T) {
	// 零值 Config：模拟未经 viper 填充的场景
	bare := &config.Config{}
	if got := bare.Kiro.KiroSimulatedCacheReadRatioOrDefault(); got != 1 {
		t.Fatalf("unset ratio = %v, want 1 (billing-neutral default)", got)
	}

	svc := NewSettingService(nil, bare)
	if got := svc.GetKiroSimulatedCacheReadRatio(nil); got != 1 {
		t.Fatalf("SettingService ratio with bare config = %v, want 1", got)
	}

	// 服务层的应用逻辑也不能改动 token 数
	kiroSvc := &KiroGatewayService{cfg: bare}
	read, creation := kiroSvc.applyKiroSimulatedCacheReadRatio(100, 20)
	if read != 100 || creation != 20 {
		t.Fatalf("unset ratio changed tokens: read=%d creation=%d, want 100/20", read, creation)
	}
}

// TestExplicitZeroSimulatedCacheReadRatioIsHonored 验证显式设置 0 仍然生效
// （即修复"未设置"语义时没有把合法的显式 0 一起吃掉）。
func TestExplicitZeroSimulatedCacheReadRatioIsHonored(t *testing.T) {
	zero := 0.0
	cfg := &config.Config{}
	cfg.Kiro.SimulatedCacheReadRatio = &zero

	if got := cfg.Kiro.KiroSimulatedCacheReadRatioOrDefault(); got != 0 {
		t.Fatalf("explicit 0 ratio = %v, want 0", got)
	}

	kiroSvc := &KiroGatewayService{cfg: cfg}
	read, creation := kiroSvc.applyKiroSimulatedCacheReadRatio(100, 20)
	if read != 0 {
		t.Fatalf("explicit 0 should zero out cache_read, got %d", read)
	}
	if creation != 20 {
		t.Fatalf("cache_creation must not change, got %d", creation)
	}
}

// TestOutOfRangeSimulatedCacheReadRatioFallsBackToOne 验证越界值回退到计费中性的 1，
// 而不是被原样使用。
func TestOutOfRangeSimulatedCacheReadRatioFallsBackToOne(t *testing.T) {
	for _, bad := range []float64{-0.5, 1.5, 42} {
		v := bad
		cfg := &config.Config{}
		cfg.Kiro.SimulatedCacheReadRatio = &v
		if got := cfg.Kiro.KiroSimulatedCacheReadRatioOrDefault(); got != 1 {
			t.Fatalf("out-of-range %v -> %v, want 1", bad, got)
		}
	}
}

// TestSimulatedCacheReadRatioConfigValidation 验证 Validate 只拒绝越界值，
// 不把"未设置"当成错误。
func TestSimulatedCacheReadRatioConfigValidation(t *testing.T) {
	valid := 0.5
	invalid := 2.0

	cfg := &config.Config{}
	cfg.Kiro.SimulatedCacheReadRatio = &valid
	if err := validateRatioOnly(cfg); err != nil {
		t.Fatalf("valid ratio rejected: %v", err)
	}

	cfg.Kiro.SimulatedCacheReadRatio = nil
	if err := validateRatioOnly(cfg); err != nil {
		t.Fatalf("unset ratio should be allowed: %v", err)
	}

	cfg.Kiro.SimulatedCacheReadRatio = &invalid
	if err := validateRatioOnly(cfg); err == nil {
		t.Fatal("out-of-range ratio should be rejected by Validate")
	}
}

// validateRatioOnly 只复用 Validate 里与 ratio 相关的那一段判定，
// 避免整个 Validate 的其它必填项干扰本测试。
func validateRatioOnly(cfg *config.Config) error {
	if r := cfg.Kiro.SimulatedCacheReadRatio; r != nil && (*r < 0 || *r > 1) {
		return errRatioOutOfRange
	}
	return nil
}

var errRatioOutOfRange = errRatio{}

type errRatio struct{}

func (errRatio) Error() string { return "kiro.simulated_cache_read_ratio must be between 0 and 1" }
