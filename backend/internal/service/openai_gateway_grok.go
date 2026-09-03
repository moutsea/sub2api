package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/gin-gonic/gin"
)

const grokDefaultModel = xai.DefaultTextModel

const grokUsageSnapshotWriteInterval = 30 * time.Second

type grokSnapshotWriteState struct {
	lastWrite   time.Time
	fingerprint string
}

func (s *OpenAIGatewayService) forwardGrokResponses(ctx context.Context, c *gin.Context, account *Account, body []byte, originalModel string, reqStream bool, startTime time.Time) (*OpenAIForwardResult, error) {
	if account == nil || !account.IsGrok() {
		return nil, fmt.Errorf("account is not a Grok account")
	}
	if account.Type != AccountTypeOAuth && account.Type != AccountTypeAPIKey {
		err := fmt.Errorf("unsupported Grok account type: %s", account.Type)
		writeGrokOpenAIError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, err
	}
	upstreamModel := resolveGrokRequestModel(account, originalModel)
	if err := validateGrokTextModel(c, upstreamModel); err != nil {
		return nil, err
	}
	clearGrokResponsesClientToolMapping(c)
	patchedBody, toolMapping, err := patchGrokResponsesBodyWithClientTools(body, upstreamModel)
	if err != nil {
		writeGrokOpenAIError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, err
	}
	setGrokResponsesClientToolMapping(c, toolMapping)
	patchedBody, err = applyGrokPromptCacheKey(patchedBody, c, body, upstreamModel)
	if err != nil {
		writeGrokOpenAIError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, err
	}
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		writeGrokOpenAIError(c, http.StatusBadGateway, "upstream_error", "Unable to obtain Grok credentials")
		return nil, err
	}
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	baseURL := account.GetGrokBaseURL()
	if s.cfg != nil {
		normalizedBaseURL, err := s.validateUpstreamBaseURL(baseURL)
		if err != nil {
			writeGrokOpenAIError(c, http.StatusBadGateway, "upstream_error", "Invalid Grok upstream URL")
			return nil, err
		}
		baseURL = normalizedBaseURL
	}
	requestCtx, releaseUpstreamCtx := grokUpstreamContext(ctx, reqStream)
	defer releaseUpstreamCtx()
	upstreamReq, err := buildGrokResponsesRequest(requestCtx, c, account, patchedBody, token, baseURL)
	if err != nil {
		writeGrokOpenAIError(c, http.StatusBadGateway, "upstream_error", "Failed to build Grok upstream request")
		return nil, err
	}
	if c != nil && c.Request != nil {
		c.Set(OpsUpstreamRequestBodyKey, string(patchedBody))
	}
	s.applyGrokRequestJitter(ctx)
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	for retry := 0; retry < 1 && err == nil && resp != nil && resp.Body != nil; retry++ {
		if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusUnprocessableEntity {
			break
		}
		statusCode := resp.StatusCode
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()
		if !isGrokReplayDecodeError(statusCode, respBody) {
			resp.Body = io.NopCloser(bytes.NewReader(respBody))
			break
		}
		retryBody, changed := stripGrokReplayEncryptedContent(patchedBody)
		if !changed {
			resp.Body = io.NopCloser(bytes.NewReader(respBody))
			break
		}
		patchedBody = retryBody
		if c != nil {
			c.Set(OpsUpstreamRequestBodyKey, string(patchedBody))
		}
		upstreamReq, err = buildGrokResponsesRequest(requestCtx, c, account, patchedBody, token, baseURL)
		if err != nil {
			break
		}
		s.applyGrokRequestJitter(ctx)
		resp, err = s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	}
	if err != nil {
		safeErr := sanitizeUpstreamErrorMessage(err.Error())
		setOpsUpstreamError(c, 0, safeErr, "")
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: 0,
			Kind:               "request_error",
			Message:            safeErr,
		})
		writeGrokOpenAIError(c, http.StatusBadGateway, "upstream_error", "Upstream request failed")
		return nil, fmt.Errorf("grok upstream request failed: %w", err)
	}
	if resp == nil {
		err := errors.New("grok upstream returned an empty response")
		writeGrokOpenAIError(c, http.StatusBadGateway, "upstream_error", "Upstream request failed")
		return nil, err
	}
	if resp.Body == nil {
		err := errors.New("grok upstream returned an empty response body")
		writeGrokOpenAIError(c, http.StatusBadGateway, "upstream_error", "Upstream request failed")
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	s.updateGrokUsageSnapshot(ctx, account.ID, xai.ParseQuotaHeaders(resp.Header, resp.StatusCode))
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		if s.shouldFailoverGrokUpstreamError(resp.StatusCode, respBody) {
			if s.rateLimitService != nil {
				s.rateLimitService.HandleUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody)
			}
			return nil, &UpstreamFailoverError{StatusCode: resp.StatusCode, Message: sanitizeUpstreamErrorMessage(extractUpstreamErrorMessage(respBody))}
		}
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		return s.handleErrorResponse(ctx, resp, c, account)
	}

	var usage *OpenAIUsage
	var firstTokenMs *int
	if reqStream {
		maxLineSize := defaultMaxLineSize
		if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
			maxLineSize = s.cfg.Gateway.MaxLineSize
		}
		resp.Body = newGrokResponsesPingFilterBody(resp.Body, account, maxLineSize)
		streamResult, err := s.handleStreamingResponse(ctx, resp, c, account, startTime, originalModel, upstreamModel, "")
		if err != nil {
			return nil, err
		}
		usage = streamResult.usage
		firstTokenMs = streamResult.firstTokenMs
	} else {
		usage, err = s.handleNonStreamingResponse(ctx, resp, c, account, originalModel, upstreamModel, "")
		if err != nil {
			return nil, err
		}
	}
	if usage == nil {
		usage = &OpenAIUsage{}
	}
	return &OpenAIForwardResult{
		RequestID:    firstNonEmptyGrokHeader(resp.Header, "x-request-id", "xai-request-id"),
		Usage:        *usage,
		Model:        upstreamModel,
		Stream:       reqStream,
		Duration:     time.Since(startTime),
		FirstTokenMs: firstTokenMs,
	}, nil
}

// applyGrokPromptCacheKey isolates client-provided cache seeds by API key and
// model before they reach xAI. A seed is only generated when the request has a
// stable session/cache identifier; requests without one remain uncached rather
// than accidentally sharing a prompt prefix across tenants.
func applyGrokPromptCacheKey(body []byte, c *gin.Context, source []byte, model string) ([]byte, error) {
	apiKey := getAPIKeyFromGinContext(c)
	if apiKey == nil || apiKey.ID <= 0 {
		return body, nil
	}
	seed := grokCacheSeed(c, source)
	if seed == "" {
		return body, nil
	}
	isolated := sha256.Sum256([]byte(fmt.Sprintf("grok-prompt-cache:v1:%d:%s:%s", apiKey.ID, strings.ToLower(strings.TrimSpace(model)), seed)))
	var request map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&request); err != nil {
		return nil, fmt.Errorf("invalid json request body: %w", err)
	}
	request["prompt_cache_key"] = hex.EncodeToString(isolated[:])
	return json.Marshal(request)
}

func grokCacheSeed(c *gin.Context, body []byte) string {
	if c != nil && c.Request != nil {
		for _, header := range []string{"X-Claude-Code-Session-Id", "session_id", "conversation_id"} {
			if value := strings.TrimSpace(c.GetHeader(header)); value != "" {
				return value
			}
		}
	}
	var request map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	if decoder.Decode(&request) != nil {
		return ""
	}
	if value, ok := request["prompt_cache_key"].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	if value, ok := request["previous_response_id"].(string); ok {
		if seed := grokPreviousResponseCacheSeed(value); seed != "" {
			return seed
		}
	}
	if metadata, ok := request["metadata"].(map[string]any); ok {
		for _, field := range []string{"session_id", "user_id", "conversation_id"} {
			if value, ok := metadata[field].(string); ok && strings.TrimSpace(value) != "" {
				value = strings.TrimSpace(value)
				if field == "user_id" {
					var nested map[string]any
					if json.Unmarshal([]byte(value), &nested) == nil {
						for _, nestedField := range []string{"session_id", "conversation_id"} {
							if nestedValue, ok := nested[nestedField].(string); ok && strings.TrimSpace(nestedValue) != "" {
								return strings.TrimSpace(nestedValue)
							}
						}
					}
				}
				return value
			}
		}
	}
	if c != nil && c.Request != nil {
		if value := strings.TrimSpace(c.GetHeader("X-Grok-Conv-Id")); value != "" {
			return value
		}
	}
	return ""
}

func grokPreviousResponseCacheSeed(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || !strings.HasPrefix(strings.ToLower(value), "resp_") {
		return ""
	}
	return "grok-prev-resp:" + value
}

func applyGrokCacheHeaders(headers http.Header, cacheKey string) {
	if headers == nil {
		return
	}
	cacheKey = strings.TrimSpace(cacheKey)
	if cacheKey == "" {
		headers.Del("X-Grok-Conv-Id")
		return
	}
	headers.Set("X-Grok-Conv-Id", cacheKey)
}

func isGrokReplayDecodeError(statusCode int, body []byte) bool {
	return isGrokInvalidEncryptedContentResponse(statusCode, body) ||
		isGrokCompactionReplayDecodeError(statusCode, body)
}

func stripGrokReplayEncryptedContent(body []byte) ([]byte, bool) {
	if sanitized, changed, err := sanitizeGrokCompactionReplayBody(body); err == nil {
		return sanitized, changed
	}

	// Keep the retry path best-effort for malformed or provider-specific input
	// shapes. If the structured sanitizer cannot decode the body, retain the
	// historical recursive fallback instead of turning an upstream 400 into a
	// local serialization error.
	var request map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&request) != nil {
		return body, false
	}
	changed := false
	var walk func(any)
	walk = func(value any) {
		switch node := value.(type) {
		case map[string]any:
			if _, ok := node["encrypted_content"]; ok {
				delete(node, "encrypted_content")
				changed = true
			}
			for _, child := range node {
				walk(child)
			}
		case []any:
			for _, child := range node {
				walk(child)
			}
		}
	}
	walk(request)
	if !changed {
		return body, false
	}
	patched, err := json.Marshal(request)
	if err != nil {
		return body, false
	}
	return patched, true
}

func grokStructuredErrorMessageCandidates(body []byte) []string {
	var payload any
	if json.Unmarshal(body, &payload) != nil {
		if text := strings.TrimSpace(string(body)); text != "" {
			return []string{text}
		}
		return nil
	}
	var candidates []string
	var walk func(any, string)
	walk = func(value any, key string) {
		switch node := value.(type) {
		case map[string]any:
			for childKey, child := range node {
				walk(child, strings.ToLower(strings.TrimSpace(childKey)))
			}
		case []any:
			for _, child := range node {
				walk(child, key)
			}
		case string:
			if key == "" || key == "message" || key == "error" || key == "detail" || key == "reason" || key == "code" {
				if text := strings.TrimSpace(node); text != "" {
					candidates = append(candidates, text)
				}
			}
		}
	}
	walk(payload, "")
	if len(candidates) == 0 {
		if text := strings.TrimSpace(string(body)); text != "" {
			candidates = append(candidates, text)
		}
	}
	return candidates
}

func isGrokInvalidEncryptedContentResponse(statusCode int, body []byte) bool {
	if statusCode != http.StatusBadRequest && statusCode != http.StatusUnprocessableEntity {
		return false
	}
	code := grokErrorCode(body)
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "invalid_encrypted_content", "invalid-compaction", "invalid_compaction", "compaction_decode_error":
		return true
	case "":
	case "invalid-argument":
		// xAI has used invalid-argument as the envelope for decrypt failures.
	default:
		return false
	}
	for _, candidate := range grokStructuredErrorMessageCandidates(body) {
		text := strings.ToLower(candidate)
		if strings.Contains(text, "encrypted_content") &&
			(strings.Contains(text, "decrypt") || strings.Contains(text, "unmodified")) {
			return true
		}
		if strings.Contains(text, "decode the compaction blob") || strings.Contains(text, "deserialize the compaction blob") {
			return true
		}
	}
	return false
}

func isGrokCompactionReplayDecodeError(statusCode int, body []byte) bool {
	if (statusCode != http.StatusBadRequest && statusCode != http.StatusUnprocessableEntity) || len(body) == 0 {
		return false
	}
	for _, candidate := range grokStructuredErrorMessageCandidates(body) {
		text := strings.ToLower(candidate)
		decodeSignal := strings.Contains(text, "decode") || strings.Contains(text, "deserialize") || strings.Contains(text, "decoder")
		replaySignal := strings.Contains(text, "compaction") || strings.Contains(text, "summary") || strings.Contains(text, "encrypted_content") || strings.Contains(text, "response history")
		if decodeSignal && replaySignal {
			return true
		}
	}
	return false
}

// sanitizeGrokCompactionReplayBody removes opaque reasoning replay state while
// retaining visible summaries. This is used only after xAI explicitly rejects
// encrypted/compaction state, so ordinary requests are left byte-for-byte
// unchanged.
func sanitizeGrokCompactionReplayBody(body []byte) ([]byte, bool, error) {
	var request map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&request); err != nil {
		return nil, false, err
	}
	input, ok := request["input"]
	if !ok {
		return body, false, nil
	}
	var items []any
	switch value := input.(type) {
	case []any:
		items = value
	case map[string]any:
		items = []any{value}
	default:
		return body, false, nil
	}

	changed := false
	filtered := make([]any, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			filtered = append(filtered, raw)
			continue
		}
		typ := strings.ToLower(strings.TrimSpace(grokStringValue(item["type"])))
		switch typ {
		case "compaction", "compaction_summary":
			if _, hasEncrypted := item["encrypted_content"]; !hasEncrypted {
				filtered = append(filtered, item)
				continue
			}
			changed = true
			if summary := grokReplaySummaryText(item["summary"]); summary != "" {
				filtered = append(filtered, map[string]any{
					"type": "message", "role": "user",
					"content": []any{map[string]any{
						"type": "input_text",
						"text": "<conversation_summary>\n" + summary + "\n</conversation_summary>",
					}},
				})
			}
			continue
		case "reasoning":
			summaryText := grokReplaySummaryText(item["summary"])
			if _, hasEncrypted := item["encrypted_content"]; hasEncrypted {
				delete(item, "encrypted_content")
				changed = true
			}
			if content, exists := item["content"]; exists && content == nil {
				delete(item, "content")
				changed = true
			}
			if summaryText == "" {
				changed = true
				continue
			}
		}
		filtered = append(filtered, item)
	}
	if changed {
		request["input"] = filtered
	}
	if previousID, _ := request["previous_response_id"].(string); strings.TrimSpace(previousID) != "" && !grokRequestHasFunctionCallOutput(filtered) {
		delete(request, "previous_response_id")
		changed = true
	}
	if !changed {
		return body, false, nil
	}
	patched, err := json.Marshal(request)
	if err != nil {
		return nil, false, err
	}
	return patched, true, nil
}

func grokReplaySummaryText(value any) string {
	parts, ok := value.([]any)
	if !ok {
		return ""
	}
	texts := make([]string, 0, len(parts))
	for _, raw := range parts {
		part, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if text := strings.TrimSpace(grokStringValue(part["text"])); text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n")
}

func grokRequestHasFunctionCallOutput(items []any) bool {
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typ := strings.ToLower(strings.TrimSpace(grokStringValue(item["type"])))
		role := strings.ToLower(strings.TrimSpace(grokStringValue(item["role"])))
		if role == "tool" || role == "function" || strings.HasSuffix(typ, "_call_output") || typ == "function_call_output" {
			return true
		}
	}
	return false
}

func resolveGrokRequestModel(account *Account, requestedModel string) string {
	if account == nil {
		return grokDefaultModel
	}
	requestedModel = strings.TrimSpace(requestedModel)
	mappedModel, matched := resolveGrokAccountMapping(account, requestedModel)
	if matched && strings.TrimSpace(mappedModel) != "" {
		return canonicalGrokModelID(mappedModel)
	}
	if mappedModel == "" {
		mappedModel = requestedModel
	}
	canonicalModel := canonicalGrokModelID(mappedModel)
	if xai.IsGrokModelID(canonicalModel) {
		return canonicalModel
	}
	return grokDefaultModel
}

func validateGrokTextModel(c *gin.Context, model string) error {
	err := grokTextModelValidationError(model)
	if err == nil {
		return nil
	}
	writeGrokOpenAIError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
	return err
}

func grokTextModelValidationError(model string) error {
	if !xai.IsGrokImagineModel(model) {
		return nil
	}
	return fmt.Errorf("model %s is an image/video model and is not available on the text endpoint; use the corresponding media endpoint instead", strings.TrimSpace(model))
}

// resolveGrokAccountMapping applies exact, canonical-alias, and wildcard
// account mappings. Wildcards are selected by the longest matching pattern so
// map iteration order cannot change the target model.
func resolveGrokAccountMapping(account *Account, requestedModel string) (string, bool) {
	if account == nil {
		return requestedModel, false
	}
	mapping := account.GetModelMapping()
	if len(mapping) == 0 {
		return requestedModel, false
	}
	if mapped, ok := mapping[requestedModel]; ok {
		return mapped, true
	}
	canonical := xai.ResolveModelID(requestedModel)
	if canonical != requestedModel {
		if mapped, ok := mapping[canonical]; ok {
			return mapped, true
		}
	}
	matchedPattern := ""
	matchedValue := ""
	for pattern, value := range mapping {
		if !strings.ContainsAny(pattern, "*?") {
			continue
		}
		for _, candidate := range []string{requestedModel, canonical} {
			matched, err := path.Match(pattern, candidate)
			if err == nil && matched && len(pattern) > len(matchedPattern) {
				matchedPattern = pattern
				matchedValue = value
			}
		}
	}
	if matchedPattern != "" {
		return matchedValue, true
	}
	return requestedModel, false
}

func isGrokModelSupportedByAccount(account *Account, requestedModel string) bool {
	if account == nil || !account.IsGrok() {
		return false
	}
	if len(account.GetModelMapping()) == 0 {
		return true
	}
	_, matched := resolveGrokAccountMapping(account, strings.TrimSpace(requestedModel))
	return matched
}

func canonicalGrokModelID(model string) string {
	return xai.ResolveModelID(model)
}

func (s *OpenAIGatewayService) updateGrokUsageSnapshot(ctx context.Context, accountID int64, snapshot *xai.QuotaSnapshot) {
	if s == nil || s.accountRepo == nil || accountID <= 0 || snapshot == nil {
		return
	}
	normalized := *snapshot
	normalized.UpdatedAt = ""
	payload, err := json.Marshal(&normalized)
	if err != nil {
		return
	}
	digest := sha256.Sum256(payload)
	fingerprint := hex.EncodeToString(digest[:])
	attemptAt := time.Now()
	s.grokSnapshotMu.Lock()
	if s.grokSnapshotWrites == nil {
		s.grokSnapshotWrites = make(map[int64]grokSnapshotWriteState)
	}
	state, exists := s.grokSnapshotWrites[accountID]
	if exists && state.fingerprint == fingerprint && attemptAt.Sub(state.lastWrite) < grokUsageSnapshotWriteInterval {
		s.grokSnapshotMu.Unlock()
		return
	}
	s.grokSnapshotWrites[accountID] = grokSnapshotWriteState{lastWrite: attemptAt, fingerprint: fingerprint}
	s.grokSnapshotMu.Unlock()

	// Quota headers are passive telemetry. Persisting them must not turn a
	// successful upstream response into a failed request if the metadata write
	// is unavailable.
	var persistErr error
	if telemetryRepo, ok := s.accountRepo.(AccountTelemetryRepository); ok {
		persistErr = telemetryRepo.UpdateGrokUsageSnapshot(ctx, accountID, snapshot)
	} else {
		persistErr = s.accountRepo.UpdateExtra(ctx, accountID, map[string]any{
			grokQuotaSnapshotExtraKey: snapshot,
		})
	}
	if persistErr != nil {
		s.grokSnapshotMu.Lock()
		if current, ok := s.grokSnapshotWrites[accountID]; ok && current.lastWrite == attemptAt && current.fingerprint == fingerprint {
			delete(s.grokSnapshotWrites, accountID)
		}
		s.grokSnapshotMu.Unlock()
	}
}

func patchGrokResponsesBody(body []byte, upstreamModel string) ([]byte, error) {
	var request map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&request); err != nil {
		return nil, fmt.Errorf("invalid json request body: %w", err)
	}
	request["model"] = upstreamModel
	for _, field := range []string{"prompt_cache_retention", "safety_identifier", "metadata"} {
		delete(request, field)
	}
	deleteGrokResponsesField(request, "external_web_access")
	convertGrokCompactionItems(request)
	sanitizeGrokResponsesInput(request)
	sanitizeGrokResponsesTools(request)
	if strings.EqualFold(upstreamModel, "grok-4.5") || strings.EqualFold(upstreamModel, "grok-4.5-latest") {
		for _, field := range []string{"presence_penalty", "presencePenalty", "frequency_penalty", "frequencyPenalty", "stop"} {
			delete(request, field)
		}
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(upstreamModel)), "grok-4.20") {
		delete(request, "logprobs")
		delete(request, "top_logprobs")
	}
	normalizeGrokResponsesReasoning(request, upstreamModel)
	return json.Marshal(request)
}

func convertGrokCompactionItems(request map[string]any) {
	if request == nil {
		return
	}
	inputValue, exists := request["input"]
	if !exists {
		return
	}
	var items []any
	switch value := inputValue.(type) {
	case []any:
		items = value
	case map[string]any:
		items = []any{value}
	default:
		return
	}
	converted := make([]any, 0, len(items))
	changed := false
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok || !isGrokCompactionType(grokStringValue(item["type"])) {
			converted = append(converted, raw)
			continue
		}
		changed = true
		if encrypted := strings.TrimSpace(grokStringValue(item["encrypted_content"])); encrypted != "" {
			converted = append(converted, map[string]any{
				"type": "reasoning", "summary": []any{}, "encrypted_content": encrypted,
			})
		}
		if summary := grokReplaySummaryText(item["summary"]); summary != "" {
			converted = append(converted, map[string]any{
				"type": "message", "role": "user", "content": []any{map[string]any{
					"type": "input_text", "text": "<conversation_summary>\n" + summary + "\n</conversation_summary>",
				}},
			})
		}
	}
	if changed {
		request["input"] = converted
	}
}

func isGrokCompactionType(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "compaction", "compaction_summary":
		return true
	default:
		return false
	}
}

var grokResponsesSupportedToolTypes = map[string]struct{}{
	"code_execution": {}, "code_interpreter": {}, "collections_search": {},
	"file_search": {}, "function": {}, "mcp": {}, "shell": {}, "tool_search": {},
	"web_search": {}, "x_search": {},
}

const grokSafeFunctionParameters = `{"type":"object","properties":{},"additionalProperties":true}`

// sanitizeGrokResponsesInput removes private Responses-Lite carriers and
// explicit null fields that xAI's untagged input decoder rejects. Tools carried
// by additional_tools are promoted to the top-level tools array in order.
func sanitizeGrokResponsesInput(request map[string]any) {
	inputValue, exists := request["input"]
	if !exists {
		return
	}
	input, ok := inputValue.([]any)
	if !ok {
		if item, isObject := inputValue.(map[string]any); isObject {
			input = []any{item}
			request["input"] = input
		} else {
			return
		}
	}
	filtered := make([]any, 0, len(input))
	var promoted []any
	for _, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok {
			filtered = append(filtered, raw)
			continue
		}
		if strings.EqualFold(strings.TrimSpace(grokStringValue(item["type"])), "additional_tools") {
			if tools, ok := item["tools"].([]any); ok {
				promoted = append(promoted, tools...)
			}
			continue
		}
		removeGrokInputNulls(item)
		filtered = append(filtered, item)
	}
	if len(filtered) != len(input) {
		request["input"] = filtered
	}
	if len(promoted) > 0 {
		if existing, ok := request["tools"].([]any); ok {
			request["tools"] = mergeGrokTools(existing, promoted)
		} else {
			request["tools"] = mergeGrokTools(nil, promoted)
		}
	}
	normalizeGrokReplayInput(request)
}

// normalizeGrokReplayInput accepts the tool/reasoning item aliases emitted by
// OpenAI-compatible clients and rewrites them to xAI's ModelInput variants.
// The two-pass call-ID pairing prevents a tool result that precedes its call in
// replay history from becoming orphaned.
func normalizeGrokReplayInput(request map[string]any) {
	items, ok := request["input"].([]any)
	if !ok {
		return
	}
	callIDs, outputIDs := pairGrokReplayCallIDs(items)
	pendingOutputImages := make([]grokToolOutputImage, 0)
	filtered := make([]any, 0, len(items))
	for index, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			filtered = appendGrokToolOutputImageMessage(filtered, pendingOutputImages)
			pendingOutputImages = pendingOutputImages[:0]
			if text := strings.TrimSpace(grokStringValue(raw)); text != "" {
				filtered = append(filtered, map[string]any{
					"type": "message", "role": "user", "content": text,
				})
			} else if raw != nil {
				filtered = append(filtered, raw)
			}
			continue
		}
		typ := strings.ToLower(strings.TrimSpace(grokStringValue(item["type"])))
		role := strings.ToLower(strings.TrimSpace(grokStringValue(item["role"])))
		if role == "tool" || role == "function" || isGrokReplayOutputType(typ) {
			callID := outputIDs[index]
			if callID == "" {
				callID = fmt.Sprintf("grok_replay_call_%d", index+1)
			}
			explicitImages := extractGrokToolOutputImages(item, callID)
			output := firstGrokValue(item["output"], item["content"], item["results"])
			normalizedOutput, nestedImages := normalizeGrokToolOutput(output, callID)
			item["type"] = "function_call_output"
			item["call_id"] = callID
			item["output"] = normalizedOutput
			for _, field := range []string{"role", "tool_call_id", "id", "content", "results", "images"} {
				delete(item, field)
			}
			filtered = append(filtered, item)
			pendingOutputImages = append(pendingOutputImages, explicitImages...)
			pendingOutputImages = append(pendingOutputImages, nestedImages...)
			continue
		}
		filtered = appendGrokToolOutputImageMessage(filtered, pendingOutputImages)
		pendingOutputImages = pendingOutputImages[:0]

		switch typ {
		case "text", "input_text", "output_text":
			text := strings.TrimSpace(grokStringValue(item["text"]))
			if text == "" {
				continue
			}
			if role == "" {
				role = "user"
			}
			item = map[string]any{"type": "message", "role": role, "content": text}
			typ = "message"
		case "custom_tool_call", "tool_search_call":
			item["type"] = "function_call"
			typ = "function_call"
		}
		if typ == "" && role != "" {
			typ = "message"
			item["type"] = typ
		}
		if typ == "message" {
			if role == "" {
				role = "user"
				item["role"] = role
			}
			content, keep := sanitizeGrokMessageContent(item["content"])
			if !keep {
				continue
			}
			item["content"] = content
			if role == "assistant" && !grokIsCompleteOutputMessage(item) {
				if text, collapsed := collapseGrokAssistantOutputText(content); collapsed {
					item["content"] = text
				}
				delete(item, "id")
				delete(item, "status")
			}
		}
		if isGrokReplayCallType(typ) {
			callID := callIDs[index]
			if callID == "" {
				callID = fmt.Sprintf("grok_replay_call_%d", index+1)
			}
			name := strings.TrimSpace(grokStringValue(item["name"]))
			arguments := firstGrokValue(item["arguments"], item["input"], item["query"])
			if fn, isObject := item["function"].(map[string]any); isObject {
				if name == "" {
					name = strings.TrimSpace(grokStringValue(fn["name"]))
				}
				if arguments == nil {
					arguments = fn["arguments"]
				}
			}
			if arguments == nil {
				arguments = map[string]any{}
			}
			if _, hasInput := item["input"]; hasInput && item["arguments"] == nil {
				arguments = map[string]any{"input": arguments}
			}
			if name == "" {
				if typ == "tool_search_call" {
					name = "tool_search"
				} else {
					name = "unknown_tool"
				}
			}
			item["type"] = "function_call"
			item["call_id"] = callID
			item["name"] = name
			item["arguments"] = grokReplayValueString(arguments, "{}")
			for _, field := range []string{"id", "status", "tool_call_id", "function", "input", "query"} {
				delete(item, field)
			}
			filtered = append(filtered, item)
			continue
		}
		if typ == "reasoning" {
			delete(item, "status")
			if content, exists := item["content"]; exists && content == nil {
				delete(item, "content")
			}
		}
		if typ != "function_call" && typ != "function_call_output" {
			delete(item, "call_id")
		}
		filtered = append(filtered, item)
	}
	filtered = appendGrokToolOutputImageMessage(filtered, pendingOutputImages)
	request["input"] = filtered
}

type grokToolOutputImage struct {
	callID string
	url    string
}

func normalizeGrokToolOutput(value any, callID string) (string, []grokToolOutputImage) {
	stripped, images, keep := stripGrokToolOutputImages(value, callID)
	if len(images) == 0 {
		return grokReplayValueString(value, "(empty)"), nil
	}
	if !keep {
		return "(empty)", images
	}
	if parts, ok := stripped.([]any); ok {
		texts := make([]string, 0, len(parts))
		for _, raw := range parts {
			part, ok := raw.(map[string]any)
			if !ok {
				return grokReplayValueString(stripped, "(empty)"), images
			}
			partType := strings.ToLower(strings.TrimSpace(grokStringValue(part["type"])))
			if partType != "text" && partType != "input_text" && partType != "output_text" {
				return grokReplayValueString(stripped, "(empty)"), images
			}
			if text := strings.TrimSpace(grokStringValue(part["text"])); text != "" {
				texts = append(texts, text)
			}
		}
		if len(texts) > 0 {
			return strings.Join(texts, "\n"), images
		}
	}
	return grokReplayValueString(stripped, "(empty)"), images
}

func stripGrokToolOutputImages(value any, callID string) (any, []grokToolOutputImage, bool) {
	switch typed := value.(type) {
	case []any:
		filtered := make([]any, 0, len(typed))
		images := make([]grokToolOutputImage, 0)
		for _, item := range typed {
			stripped, nested, keep := stripGrokToolOutputImages(item, callID)
			images = append(images, nested...)
			if keep {
				filtered = append(filtered, stripped)
			}
		}
		return filtered, images, len(filtered) > 0
	case map[string]any:
		partType := strings.ToLower(strings.TrimSpace(grokStringValue(typed["type"])))
		if partType == "image" || partType == "image_url" || partType == "input_image" {
			url := grokToolOutputImageURL(typed)
			if url == "" || isEmptyGrokBase64DataURI(url) {
				return nil, nil, false
			}
			return nil, []grokToolOutputImage{{callID: callID, url: url}}, false
		}
		filtered := make(map[string]any, len(typed))
		for key, item := range typed {
			filtered[key] = item
		}
		images := make([]grokToolOutputImage, 0)
		if rawImages, exists := filtered["images"]; exists {
			if values, ok := rawImages.([]any); ok {
				for _, rawImage := range values {
					url := grokToolOutputImageURL(rawImage)
					if url != "" && !isEmptyGrokBase64DataURI(url) {
						images = append(images, grokToolOutputImage{callID: callID, url: url})
					}
				}
			}
			delete(filtered, "images")
		}
		for _, field := range []string{"content", "output", "results"} {
			item, exists := filtered[field]
			if !exists {
				continue
			}
			stripped, nested, keep := stripGrokToolOutputImages(item, callID)
			images = append(images, nested...)
			if keep {
				filtered[field] = stripped
			} else {
				delete(filtered, field)
			}
		}
		return filtered, images, len(filtered) > 0
	default:
		return value, nil, value != nil
	}
}

func grokToolOutputImageURL(value any) string {
	switch image := value.(type) {
	case string:
		return strings.TrimSpace(image)
	case map[string]any:
		for _, field := range []string{"url", "image_url", "file_url"} {
			raw := image[field]
			if text, ok := raw.(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
			if nested, ok := raw.(map[string]any); ok {
				if text := strings.TrimSpace(grokStringValue(nested["url"])); text != "" {
					return text
				}
			}
		}
	}
	return ""
}

func extractGrokToolOutputImages(item map[string]any, callID string) []grokToolOutputImage {
	values, ok := item["images"].([]any)
	if !ok {
		return nil
	}
	images := make([]grokToolOutputImage, 0, len(values))
	for _, raw := range values {
		url := grokToolOutputImageURL(raw)
		if url != "" && !isEmptyGrokBase64DataURI(url) {
			images = append(images, grokToolOutputImage{callID: callID, url: url})
		}
	}
	return images
}

func appendGrokToolOutputImageMessage(items []any, images []grokToolOutputImage) []any {
	if len(images) == 0 {
		return items
	}
	content := make([]any, 0, len(images)*2)
	lastCallID := ""
	for _, image := range images {
		if image.callID != lastCallID {
			content = append(content, map[string]any{
				"type": "input_text", "text": fmt.Sprintf("[Tool output media for call %s]", image.callID),
			})
			lastCallID = image.callID
		}
		content = append(content, map[string]any{"type": "input_image", "image_url": image.url})
	}
	return append(items, map[string]any{"type": "message", "role": "user", "content": content})
}

func sanitizeGrokMessageContent(content any) (any, bool) {
	switch value := content.(type) {
	case nil:
		return nil, false
	case string:
		return value, strings.TrimSpace(value) != ""
	case []any:
		filtered := make([]any, 0, len(value))
		for _, rawPart := range value {
			part, ok := rawPart.(map[string]any)
			if !ok {
				if text, isText := rawPart.(string); !isText || strings.TrimSpace(text) != "" {
					filtered = append(filtered, rawPart)
				}
				continue
			}
			switch strings.ToLower(strings.TrimSpace(grokStringValue(part["type"]))) {
			case "text", "input_text", "output_text":
				if strings.TrimSpace(grokStringValue(part["text"])) == "" {
					continue
				}
			case "image_url", "input_image":
				if !grokContentPartHasImageURL(part) {
					continue
				}
			}
			filtered = append(filtered, part)
		}
		return filtered, len(filtered) > 0
	default:
		return content, true
	}
}

func grokContentPartHasImageURL(part map[string]any) bool {
	for _, field := range []string{"file_id", "file_data"} {
		if strings.TrimSpace(grokStringValue(part[field])) != "" {
			return true
		}
	}
	for _, field := range []string{"image_url", "file_url"} {
		raw := part[field]
		if text, ok := raw.(string); ok {
			return strings.TrimSpace(text) != "" && !isEmptyGrokBase64DataURI(text)
		}
		if nested, ok := raw.(map[string]any); ok {
			text := strings.TrimSpace(grokStringValue(nested["url"]))
			return text != "" && !isEmptyGrokBase64DataURI(text)
		}
	}
	return false
}

func isEmptyGrokBase64DataURI(raw string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	if !strings.HasPrefix(lower, "data:") {
		return false
	}
	marker := strings.Index(lower, ";base64,")
	return marker >= 0 && strings.TrimSpace(lower[marker+len(";base64,"):]) == ""
}

func grokIsCompleteOutputMessage(item map[string]any) bool {
	return strings.TrimSpace(grokStringValue(item["id"])) != "" && strings.TrimSpace(grokStringValue(item["status"])) != ""
}

func collapseGrokAssistantOutputText(content any) (string, bool) {
	parts, ok := content.([]any)
	if !ok || len(parts) == 0 {
		return "", false
	}
	var text strings.Builder
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok || strings.ToLower(strings.TrimSpace(grokStringValue(part["type"]))) != "output_text" {
			return "", false
		}
		value, ok := part["text"].(string)
		if !ok {
			return "", false
		}
		text.WriteString(value)
	}
	if strings.TrimSpace(text.String()) == "" {
		return "", false
	}
	return text.String(), true
}

func pairGrokReplayCallIDs(items []any) (map[int]string, map[int]string) {
	callIDs := make(map[int]string)
	outputIDs := make(map[int]string)
	aliases := make(map[string]string)
	conflictingAliases := make(map[string]struct{})
	pendingCalls := make([]string, 0)
	nextSynthetic := 0
	synthetic := func() string {
		nextSynthetic++
		return fmt.Sprintf("grok_replay_call_%d", nextSynthetic)
	}
	registerAlias := func(alias, canonical string) {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			return
		}
		if _, conflict := conflictingAliases[alias]; conflict {
			return
		}
		if existing, exists := aliases[alias]; exists && existing != canonical {
			delete(aliases, alias)
			conflictingAliases[alias] = struct{}{}
			return
		}
		aliases[alias] = canonical
	}
	for index, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok || !isGrokReplayCallType(strings.ToLower(strings.TrimSpace(grokStringValue(item["type"])))) {
			continue
		}
		callID := firstGrokString(item["call_id"], item["tool_call_id"])
		if callID == "" {
			callID = synthetic()
		}
		callIDs[index] = callID
		pendingCalls = append(pendingCalls, callID)
		registerAlias(callID, callID)
		registerAlias(grokStringValue(item["call_id"]), callID)
		registerAlias(grokStringValue(item["tool_call_id"]), callID)
		registerAlias(grokStringValue(item["id"]), callID)
	}
	consumed := make(map[string]struct{})
	nextPending := 0
	consumeNext := func() string {
		for nextPending < len(pendingCalls) {
			callID := pendingCalls[nextPending]
			nextPending++
			if _, exists := consumed[callID]; exists {
				continue
			}
			consumed[callID] = struct{}{}
			return callID
		}
		return synthetic()
	}
	for index, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typ := strings.ToLower(strings.TrimSpace(grokStringValue(item["type"])))
		role := strings.ToLower(strings.TrimSpace(grokStringValue(item["role"])))
		if role != "tool" && role != "function" && !isGrokReplayOutputType(typ) {
			continue
		}
		alias := firstGrokString(item["call_id"], item["tool_call_id"], item["id"])
		if canonical := aliases[alias]; canonical != "" {
			if _, conflict := conflictingAliases[alias]; !conflict {
				outputIDs[index] = canonical
				consumed[canonical] = struct{}{}
				continue
			}
		}
		if explicit := firstGrokString(item["call_id"], item["tool_call_id"]); explicit != "" {
			outputIDs[index] = explicit
		} else {
			outputIDs[index] = consumeNext()
		}
	}
	return callIDs, outputIDs
}

func isGrokReplayCallType(typ string) bool {
	switch typ {
	case "function_call", "custom_tool_call", "tool_search_call":
		return true
	default:
		return false
	}
}

func isGrokReplayOutputType(typ string) bool {
	switch typ {
	case "function_call_output", "custom_tool_call_output", "tool_search_output", "tool_search_call_output":
		return true
	default:
		return false
	}
}

func firstGrokString(values ...any) string {
	for _, value := range values {
		if text := strings.TrimSpace(grokStringValue(value)); text != "" {
			return text
		}
	}
	return ""
}

func firstGrokValue(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func grokReplayValueString(value any, fallback string) string {
	if text, ok := value.(string); ok {
		if strings.TrimSpace(text) != "" {
			return text
		}
		return fallback
	}
	if value == nil {
		return fallback
	}
	encoded, err := json.Marshal(value)
	if err != nil || string(encoded) == "null" {
		return fallback
	}
	return string(encoded)
}

func mergeGrokTools(existing, promoted []any) []any {
	merged := make([]any, 0, len(existing)+len(promoted))
	seen := make(map[string]struct{}, len(existing)+len(promoted))
	for _, raw := range append(existing, promoted...) {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		key := grokToolDedupKey(tool)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, tool)
	}
	return merged
}

func grokToolDedupKey(tool map[string]any) string {
	typ := strings.TrimSpace(grokStringValue(tool["type"]))
	name := strings.TrimSpace(grokStringValue(tool["name"]))
	if name == "" {
		if nested, ok := tool["function"].(map[string]any); ok {
			name = strings.TrimSpace(grokStringValue(nested["name"]))
		}
	}
	if typ != "" && name != "" {
		return "type:" + typ + "\x00name:" + name
	}
	encoded, err := json.Marshal(tool)
	if err != nil {
		return fmt.Sprintf("ptr:%p", tool)
	}
	return "json:" + string(encoded)
}

func removeGrokInputNulls(value any) {
	switch node := value.(type) {
	case map[string]any:
		if strings.EqualFold(strings.TrimSpace(grokStringValue(node["type"])), "compaction") {
			return
		}
		for key, child := range node {
			if child == nil {
				delete(node, key)
				continue
			}
			removeGrokInputNulls(child)
		}
	case []any:
		for _, child := range node {
			removeGrokInputNulls(child)
		}
	}
}

func deleteGrokResponsesField(value any, field string) {
	switch node := value.(type) {
	case map[string]any:
		delete(node, field)
		for _, child := range node {
			deleteGrokResponsesField(child, field)
		}
	case []any:
		for _, child := range node {
			deleteGrokResponsesField(child, field)
		}
	}
}

func sanitizeGrokResponsesTools(request map[string]any) {
	rawTools, exists := request["tools"]
	if !exists {
		delete(request, "tool_choice")
		delete(request, "parallel_tool_calls")
		return
	}
	tools, ok := rawTools.([]any)
	if !ok {
		delete(request, "tools")
		delete(request, "tool_choice")
		delete(request, "parallel_tool_calls")
		return
	}
	filtered := make([]any, 0, len(tools))
	hasToolSearch := false
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typ := strings.TrimSpace(grokStringValue(tool["type"]))
		if _, supported := grokResponsesSupportedToolTypes[typ]; !supported {
			continue
		}
		if typ == "function" {
			if _, exists := tool["parameters"]; !exists || tool["parameters"] == nil {
				tool["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			if grokFunctionParametersHaveInvalidUnionRoot(tool["parameters"]) {
				var safeParameters map[string]any
				if err := json.Unmarshal([]byte(grokSafeFunctionParameters), &safeParameters); err == nil {
					tool["parameters"] = safeParameters
					if strict, ok := tool["strict"].(bool); ok && strict {
						tool["strict"] = false
					}
				}
			}
		}
		if typ == "tool_search" {
			hasToolSearch = true
		}
		filtered = append(filtered, tool)
	}
	if len(filtered) == 0 {
		delete(request, "tools")
		delete(request, "tool_choice")
		delete(request, "parallel_tool_calls")
		return
	}
	if !hasToolSearch {
		for _, raw := range filtered {
			if tool, ok := raw.(map[string]any); ok {
				delete(tool, "defer_loading")
			}
		}
	}
	request["tools"] = filtered
	if choice, ok := request["tool_choice"].(map[string]any); ok {
		choiceType := strings.TrimSpace(grokStringValue(choice["type"]))
		if choiceType == "function" {
			name := strings.TrimSpace(grokStringValue(choice["name"]))
			if nested, ok := choice["function"].(map[string]any); ok && name == "" {
				name = strings.TrimSpace(grokStringValue(nested["name"]))
			}
			found := false
			for _, raw := range filtered {
				tool, _ := raw.(map[string]any)
				if tool != nil && strings.EqualFold(grokStringValue(tool["type"]), "function") && strings.TrimSpace(grokStringValue(tool["name"])) == name {
					found = true
					break
				}
			}
			if !found {
				delete(request, "tool_choice")
			}
		} else if _, supported := grokResponsesSupportedToolTypes[choiceType]; !supported {
			delete(request, "tool_choice")
		}
	}
}

func grokFunctionParametersHaveInvalidUnionRoot(parameters any) bool {
	object, ok := parameters.(map[string]any)
	if !ok || object == nil {
		return false
	}
	for _, keyword := range []string{"anyOf", "oneOf"} {
		branches, ok := object[keyword].([]any)
		if !ok || len(branches) == 0 {
			continue
		}
		for _, branch := range branches {
			branchObject, ok := branch.(map[string]any)
			if !ok {
				return true
			}
			branchType, _ := branchObject["type"].(string)
			if !strings.EqualFold(strings.TrimSpace(branchType), "object") {
				return true
			}
		}
	}
	return false
}

func grokStringValue(value any) string {
	text, _ := value.(string)
	return text
}

func writeGrokOpenAIError(c *gin.Context, status int, errType, message string) {
	if c == nil || c.Writer == nil || c.Writer.Written() {
		return
	}
	c.JSON(status, gin.H{"error": gin.H{
		"type":    errType,
		"message": message,
	}})
}

func normalizeGrokResponsesReasoning(request map[string]any, model string) {
	if request == nil {
		return
	}
	if !grokSupportsReasoningEffort(model) {
		delete(request, "reasoning")
		delete(request, "reasoning_effort")
		delete(request, "reasoningEffort")
		return
	}
	if reasoning, ok := request["reasoning"].(map[string]any); ok {
		if effort, ok := reasoning["effort"].(string); ok {
			if normalized, keep := normalizeGrokReasoningEffort(effort, model); keep {
				reasoning["effort"] = normalized
			} else {
				delete(reasoning, "effort")
			}
		}
		if len(reasoning) == 0 {
			delete(request, "reasoning")
		}
	} else {
		delete(request, "reasoning")
	}

	for _, field := range []string{"reasoning_effort", "reasoningEffort"} {
		raw, exists := request[field]
		if !exists {
			continue
		}
		effort, ok := raw.(string)
		delete(request, field)
		if !ok {
			continue
		}
		if normalized, keep := normalizeGrokReasoningEffort(effort, model); keep {
			request["reasoning_effort"] = normalized
		}
	}
}

func normalizeGrokChatReasoning(request map[string]any, model string) bool {
	if request == nil {
		return false
	}
	_, hadReasoning := request["reasoning"]
	_, hadReasoningEffort := request["reasoning_effort"]
	_, hadCamelReasoningEffort := request["reasoningEffort"]
	var effort string
	if reasoning, ok := request["reasoning"].(map[string]any); ok {
		effort, _ = reasoning["effort"].(string)
	}
	if effort == "" {
		effort, _ = request["reasoning_effort"].(string)
	}
	if effort == "" {
		effort, _ = request["reasoningEffort"].(string)
	}
	if !hadReasoning && hadReasoningEffort && !hadCamelReasoningEffort {
		if normalized, keep := normalizeGrokReasoningEffort(effort, model); keep && strings.TrimSpace(effort) == normalized {
			return false
		}
	}
	delete(request, "reasoning")
	delete(request, "reasoningEffort")
	delete(request, "reasoning_effort")
	if normalized, keep := normalizeGrokReasoningEffort(effort, model); keep {
		request["reasoning_effort"] = normalized
		return hadReasoning || hadReasoningEffort || hadCamelReasoningEffort || effort != normalized
	}
	return hadReasoning || hadReasoningEffort || hadCamelReasoningEffort
}

func normalizeGrokReasoningEffort(raw, model string) (string, bool) {
	if !grokSupportsReasoningEffort(model) {
		return "", false
	}
	value := strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(raw)))
	switch value {
	case "none", "low", "medium", "high":
		return value, true
	case "minimal":
		return "low", true
	case "xhigh", "extrahigh":
		if grokSupportsXHighReasoningEffort(model) {
			return "xhigh", true
		}
		return "high", true
	case "max", "ultra":
		return "high", true
	default:
		return "", false
	}
}

func grokSupportsXHighReasoningEffort(model string) bool {
	model = strings.ToLower(xai.StripProviderPrefix(model))
	return model == "grok-4.6" || model == "grok-4.6-latest"
}

func grokSupportsReasoningEffort(model string) bool {
	model = strings.ToLower(xai.StripProviderPrefix(model))
	switch model {
	case "grok-4.3", "grok-4.3-latest", "grok-4.5", "grok-4.5-latest", "grok-4.6", "grok-4.6-latest",
		"grok-3-mini", "grok-3-mini-fast", "grok-4.20-0309-reasoning", "grok-4.20-reasoning", "grok-4.20-multi-agent-0309":
		return true
	default:
		return false
	}
}

func buildGrokResponsesRequest(ctx context.Context, c *gin.Context, account *Account, body []byte, token string, baseURLs ...string) (*http.Request, error) {
	baseURL := account.GetGrokBaseURL()
	if len(baseURLs) > 0 && strings.TrimSpace(baseURLs[0]) != "" {
		baseURL = baseURLs[0]
	}
	targetURL := xai.BuildResponsesURL(baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", "sub2api-grok/1.0")
	if c != nil && c.Request != nil {
		if value := strings.TrimSpace(c.GetHeader("OpenAI-Beta")); value != "" {
			req.Header.Set("OpenAI-Beta", value)
		}
	}
	account.ApplyHeaderOverrides(req.Header)
	if account.IsGrokOAuth() {
		xai.ApplyCLIProxyHeaders(req)
	}
	return req, nil
}
