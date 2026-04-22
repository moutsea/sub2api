package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	openAICapacityCooldown         = 45 * time.Second
	openAICapacityPreReadWindow    = 150 * time.Millisecond
	openAICapacityPreReadMaxEvents = 6
)

var errOpenAIInitialStreamTimeout = errors.New("stream data interval timeout")

type openAIStreamScanEvent struct {
	line string
	err  error
}

func isOpenAICapacityError(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return false
	}

	switch {
	case strings.Contains(lower, "selected model is at capacity"):
		return true
	case strings.Contains(lower, "capacity on this model"):
		return true
	case strings.Contains(lower, "model is at capacity"):
		return true
	case strings.Contains(lower, "at capacity") && strings.Contains(lower, "different model"):
		return true
	default:
		return false
	}
}

func extractOpenAICapacityMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}

	candidates := []string{
		strings.TrimSpace(extractUpstreamErrorMessage(body)),
		strings.TrimSpace(gjson.GetBytes(body, "response.error.message").String()),
		strings.TrimSpace(gjson.GetBytes(body, "response.status_details.message").String()),
		strings.TrimSpace(gjson.GetBytes(body, "response.status_details.error.message").String()),
		strings.TrimSpace(gjson.GetBytes(body, "error.message").String()),
		strings.TrimSpace(gjson.GetBytes(body, "message").String()),
	}

	for _, candidate := range candidates {
		if isOpenAICapacityError(candidate) {
			return candidate
		}
	}

	raw := strings.TrimSpace(string(body))
	if isOpenAICapacityError(raw) {
		return raw
	}
	return ""
}

func extractOpenAICapacityMessageFromSSELine(line string) string {
	if !openaiSSEDataRe.MatchString(line) {
		return ""
	}
	data := strings.TrimSpace(openaiSSEDataRe.ReplaceAllString(line, ""))
	if data == "" || data == "[DONE]" {
		return ""
	}
	return extractOpenAICapacityMessage([]byte(data))
}

func extractOpenAICapacityMessageFromSSEBody(body string) string {
	if strings.TrimSpace(body) == "" {
		return ""
	}
	for _, line := range strings.Split(body, "\n") {
		if msg := extractOpenAICapacityMessageFromSSELine(line); msg != "" {
			return msg
		}
	}
	return extractOpenAICapacityMessage([]byte(body))
}

func shouldPreReadOpenAICapacity(account *Account) bool {
	return account != nil && account.IsOpenAI()
}

func preReadOpenAIStreamEvents(
	ctx context.Context,
	events <-chan openAIStreamScanEvent,
	initialTimeout time.Duration,
) ([]openAIStreamScanEvent, string, bool, error) {
	var initialTimer *time.Timer
	var initialCh <-chan time.Time
	if initialTimeout > 0 {
		initialTimer = time.NewTimer(initialTimeout)
		initialCh = initialTimer.C
		defer initialTimer.Stop()
	}

	var buffered []openAIStreamScanEvent
	for len(buffered) == 0 {
		select {
		case <-ctx.Done():
			return nil, "", false, ctx.Err()
		case <-initialCh:
			return nil, "", false, errOpenAIInitialStreamTimeout
		case ev, ok := <-events:
			if !ok {
				return nil, "", true, nil
			}
			if ev.err != nil {
				return nil, "", false, ev.err
			}
			buffered = append(buffered, ev)
			if msg := extractOpenAICapacityMessageFromSSELine(ev.line); msg != "" {
				return buffered, msg, false, nil
			}
		}
	}

	if openAICapacityPreReadWindow <= 0 || openAICapacityPreReadMaxEvents <= 1 {
		return buffered, "", false, nil
	}

	drainTimer := time.NewTimer(openAICapacityPreReadWindow)
	defer drainTimer.Stop()

	for len(buffered) < openAICapacityPreReadMaxEvents {
		select {
		case <-ctx.Done():
			return buffered, "", false, ctx.Err()
		case <-drainTimer.C:
			return buffered, "", false, nil
		case ev, ok := <-events:
			if !ok {
				return buffered, "", true, nil
			}
			if ev.err != nil {
				return buffered, "", false, ev.err
			}
			buffered = append(buffered, ev)
			if msg := extractOpenAICapacityMessageFromSSELine(ev.line); msg != "" {
				return buffered, msg, false, nil
			}
		}
	}

	return buffered, "", false, nil
}

func (s *RateLimitService) HandleOpenAICapacityError(ctx context.Context, account *Account, statusCode int, responseBody []byte) bool {
	if s == nil || account == nil || !account.IsOpenAI() || account.IsOpenAIApiKey() {
		return false
	}

	msg := extractOpenAICapacityMessage(responseBody)
	if msg == "" {
		return false
	}

	now := time.Now()
	until := now.Add(openAICapacityCooldown)
	state := &TempUnschedState{
		UntilUnix:       until.Unix(),
		TriggeredAtUnix: now.Unix(),
		StatusCode:      statusCode,
		MatchedKeyword:  "openai_model_capacity",
		RuleIndex:       -2,
		ErrorMessage:    truncateTempUnschedMessage(responseBody, tempUnschedMessageMaxBytes),
	}
	if state.ErrorMessage == "" {
		state.ErrorMessage = msg
	}

	reason := state.ErrorMessage
	if raw, err := json.Marshal(state); err == nil {
		reason = string(raw)
	}

	if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, until, reason); err != nil {
		slog.Warn("openai_capacity_temp_unsched_set_failed", "account_id", account.ID, "error", err)
		return false
	}

	if s.tempUnschedCache != nil {
		if err := s.tempUnschedCache.SetTempUnsched(ctx, account.ID, state); err != nil {
			slog.Warn("openai_capacity_temp_unsched_cache_set_failed", "account_id", account.ID, "error", err)
		}
	}

	slog.Info("openai_capacity_temp_unschedulable", "account_id", account.ID, "until", until, "status_code", statusCode)
	return true
}

func (s *OpenAIGatewayService) newOpenAICapacityFailoverError(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	requestID string,
	message string,
	detail string,
) *UpstreamFailoverError {
	sanitizedMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(message))
	if sanitizedMsg == "" {
		sanitizedMsg = "Selected model is at capacity. Please try a different model."
	}

	detail = strings.TrimSpace(detail)
	if detail == "" {
		detail = sanitizedMsg
	}
	if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
		maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
		if maxBytes <= 0 {
			maxBytes = 2048
		}
		detail = truncateString(detail, maxBytes)
	} else {
		detail = ""
	}

	setOpsUpstreamError(c, http.StatusTooManyRequests, sanitizedMsg, detail)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: http.StatusTooManyRequests,
		UpstreamRequestID:  requestID,
		Kind:               "failover",
		Message:            sanitizedMsg,
		Detail:             detail,
	})

	if s.rateLimitService != nil {
		s.rateLimitService.HandleOpenAICapacityError(ctx, account, http.StatusTooManyRequests, []byte(sanitizedMsg))
	}

	return &UpstreamFailoverError{
		StatusCode: http.StatusTooManyRequests,
		Message:    sanitizedMsg,
	}
}
