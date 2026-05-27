package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/gin-gonic/gin"
)

const (
	kiroDynamicProbeModelOpus47    = "claude-opus-4-7"
	kiroDynamicFallbackModelOpus46 = "claude-opus-4-6"
	kiroOpus47InitialTimeout       = 40 * time.Second
	kiroOpus47CancelGrace          = 2 * time.Second

	kiroModelCapabilitySupported   = "supported"
	kiroModelCapabilityUnsupported = "unsupported"
	kiroModelCapabilityCacheTTL    = 6 * time.Hour
)

type kiroModelCapabilityKey struct {
	AccountID      int64
	RequestedModel string
}

type kiroModelCapabilityState struct {
	Status    string
	CheckedAt time.Time
}

func shouldAutoDetectKiroModel(account *Account, requestedModel string) bool {
	return account != nil && account.IsKiro() && !account.IsKiroApiKey() && isKiroOpus47Model(requestedModel)
}

func isKiroOpus47Model(model string) bool {
	switch strings.TrimSpace(strings.ToLower(model)) {
	case kiroDynamicProbeModelOpus47, "claude-opus-4.7":
		return true
	default:
		return false
	}
}

func shouldFallbackSlowKiroOpus47(account *Account, requestedModel, upstreamModel string) bool {
	return shouldAutoDetectKiroModel(account, requestedModel) && isKiroOpus47Model(upstreamModel)
}

func kiroOpus47InitialResponseTimeout(account *Account, requestedModel, upstreamModel string) time.Duration {
	if !shouldFallbackSlowKiroOpus47(account, requestedModel, upstreamModel) {
		return 0
	}
	return kiroOpus47InitialTimeout
}

func kiroOpus47RemainingInitialResponseTimeout(account *Account, requestedModel, upstreamModel string, startTime time.Time) time.Duration {
	timeout := kiroOpus47InitialResponseTimeout(account, requestedModel, upstreamModel)
	if timeout <= 0 || startTime.IsZero() {
		return timeout
	}
	remaining := time.Until(startTime.Add(timeout))
	if remaining <= 0 {
		return time.Nanosecond
	}
	return remaining
}

type kiroOpus47InitialResponseTimeoutError struct {
	Phase   string
	Timeout time.Duration
}

func (e *kiroOpus47InitialResponseTimeoutError) Error() string {
	if e == nil {
		return "kiro opus 4.7 initial response timeout"
	}
	phase := strings.TrimSpace(e.Phase)
	if phase == "" {
		phase = "initial_response"
	}
	return fmt.Sprintf("kiro opus 4.7 %s timeout after %s", phase, e.Timeout)
}

func newKiroOpus47InitialResponseTimeoutError(phase string, timeout time.Duration) error {
	return &kiroOpus47InitialResponseTimeoutError{
		Phase:   phase,
		Timeout: timeout,
	}
}

func isKiroOAuthOnlyModel(requestedModel string) bool {
	model := strings.TrimSpace(strings.ToLower(requestedModel))
	return kiro.IsOAuthModelSupported(model) && !strings.HasPrefix(model, "claude-")
}

func (s *KiroGatewayService) resolveKiroUpstreamModel(account *Account, requestedModel string) string {
	if !shouldAutoDetectKiroModel(account, requestedModel) {
		return requestedModel
	}
	status, ok := s.getKiroModelCapability(account.ID, requestedModel)
	if ok && status == kiroModelCapabilityUnsupported {
		return kiroDynamicFallbackModelOpus46
	}
	return requestedModel
}

func (s *KiroGatewayService) getKiroModelCapability(accountID int64, requestedModel string) (string, bool) {
	if s == nil {
		return "", false
	}

	key := kiroModelCapabilityKey{AccountID: accountID, RequestedModel: requestedModel}
	raw, ok := s.modelCapabilityCache.Load(key)
	if !ok {
		return "", false
	}

	state, ok := raw.(kiroModelCapabilityState)
	if !ok || state.Status == "" || state.CheckedAt.IsZero() {
		s.modelCapabilityCache.Delete(key)
		return "", false
	}
	if time.Since(state.CheckedAt) > kiroModelCapabilityCacheTTL {
		s.modelCapabilityCache.Delete(key)
		return "", false
	}
	if state.Status != kiroModelCapabilitySupported && state.Status != kiroModelCapabilityUnsupported {
		s.modelCapabilityCache.Delete(key)
		return "", false
	}

	return state.Status, true
}

func (s *KiroGatewayService) setKiroModelCapability(accountID int64, requestedModel, status string) {
	if s == nil {
		return
	}
	if status != kiroModelCapabilitySupported && status != kiroModelCapabilityUnsupported {
		return
	}

	s.modelCapabilityCache.Store(kiroModelCapabilityKey{
		AccountID:      accountID,
		RequestedModel: requestedModel,
	}, kiroModelCapabilityState{
		Status:    status,
		CheckedAt: time.Now(),
	})
}

func (s *KiroGatewayService) maybeFallbackUnsupportedKiroModel(account *Account, requestedModel, upstreamModel, errorMsg string) (string, bool) {
	if !shouldAutoDetectKiroModel(account, requestedModel) || !isKiroOpus47Model(upstreamModel) {
		return "", false
	}
	if !isKiroUnsupportedModelError(errorMsg, requestedModel, kiro.GetModelID(requestedModel)) {
		return "", false
	}

	s.setKiroModelCapability(account.ID, requestedModel, kiroModelCapabilityUnsupported)
	return kiroDynamicFallbackModelOpus46, true
}

func (s *KiroGatewayService) markKiroModelSupported(account *Account, requestedModel, upstreamModel string) {
	if !shouldAutoDetectKiroModel(account, requestedModel) || !isKiroOpus47Model(upstreamModel) {
		return
	}
	s.setKiroModelCapability(account.ID, requestedModel, kiroModelCapabilitySupported)
}

func rewriteTopLevelModelJSON(body []byte, model string) ([]byte, error) {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("parse request body: %w", err)
	}
	modelBytes, err := json.Marshal(model)
	if err != nil {
		return nil, fmt.Errorf("serialize fallback model: %w", err)
	}
	req["model"] = modelBytes
	rewritten, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("serialize fallback request body: %w", err)
	}
	return rewritten, nil
}

func isKiroUnsupportedModelError(errorMsg string, modelVariants ...string) bool {
	lowerMsg := strings.ToLower(strings.TrimSpace(errorMsg))
	if lowerMsg == "" {
		return false
	}

	modelMatched := false
	for _, variant := range modelVariants {
		if variant == "" {
			continue
		}
		if strings.Contains(lowerMsg, strings.ToLower(variant)) {
			modelMatched = true
			break
		}
	}
	unsupportedPatterns := []string{
		"unsupported model",
		"unsupported foundation model",
		"model not found",
		"unknown model",
		"invalid model",
		"unrecognized model",
		"is not supported",
		"isn't supported",
		"not supported",
		"not available",
		"not enabled",
		"not valid",
	}
	patternMatched := false
	for _, pattern := range unsupportedPatterns {
		if strings.Contains(lowerMsg, pattern) {
			patternMatched = true
			break
		}
	}
	if !patternMatched {
		return false
	}

	if modelMatched {
		return true
	}

	// Some upstreams return only a generic invalid-model message without echoing
	// the requested model name, e.g.:
	// "Invalid request: Invalid model. Please select a different model to continue."
	// This path is only used by the Opus 4.7 auto-detect fallback, so allowing
	// this high-signal generic form is safe and lets us fall back to 4.6.
	genericInvalidModelPatterns := []string{
		"invalid model",
		"select a different model",
	}
	for _, pattern := range genericInvalidModelPatterns {
		if strings.Contains(lowerMsg, pattern) {
			return true
		}
	}

	return false
}

func (s *KiroGatewayService) prepareCodeWhispererPayload(
	claudeReq *kiro.ClaudeRequest,
	profileArn string,
	ginCtx *gin.Context,
	upstreamModel string,
) (*kiro.CodeWhispererRequest, []byte, error) {
	if claudeReq == nil {
		return nil, nil, fmt.Errorf("claude request is nil")
	}

	reqCopy := *claudeReq
	reqCopy.Model = upstreamModel
	kiro.ApplyThinkingDefaultsFromModelName(&reqCopy)

	cwReq, err := kiro.TransformClaudeToCodeWhisperer(&reqCopy, profileArn, ginCtx)
	if err != nil {
		return nil, nil, err
	}

	reqBody, err := json.Marshal(cwReq)
	if err != nil {
		return nil, nil, err
	}

	return cwReq, reqBody, nil
}
