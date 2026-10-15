package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const geminiContinuationHopKey = "_sub2api_continuation_hop"

type geminiPromptBlockedError struct {
	reason string
}

func (e *geminiPromptBlockedError) Error() string {
	return "Prompt blocked by upstream: " + e.reason
}

// A prompt rejection is not a retryable transport failure. Streaming callers
// have already sent HTTP headers, so they use the same type/message in SSE.
func geminiClaudeReadError(err error, stream bool) (int, string, string) {
	var blocked *geminiPromptBlockedError
	if errors.As(err, &blocked) {
		return http.StatusBadRequest, "invalid_request_error", blocked.Error()
	}
	errorType := "upstream_error"
	if stream {
		errorType = "api_error"
	}
	return http.StatusBadGateway, errorType, "Failed to read upstream stream"
}

// geminiContinuationBody presents bounded continuation hops as one SSE stream
// to the Claude converters. Native Gemini callers retain control of their loop.
// Usage is cumulative within each hop and additive across hops:
// https://ai.google.dev/gemini-api/docs/generate-content/long-decode#count-tokens
type geminiContinuationBody struct {
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex // Close may race with the upstream scanner's Read.
	body      io.ReadCloser
	scanner   *bufio.Scanner
	next      func(context.Context, string) (*http.Response, error)
	maxTokens int
	hops      int
	seen      map[string]bool
	previous  map[string]int
	usage     map[string]int
	finish    string
	token     string
	pending   []byte
	done      bool
	hopEnded  bool
	wrapped   bool
	confirmed ClaudeUsage // Protected by mu; may be read while the scanner exits.
}

func newGeminiContinuationBody(ctx context.Context, body io.ReadCloser, maxTokens int, next func(context.Context, string) (*http.Response, error)) io.ReadCloser {
	ctx, cancel := context.WithCancel(ctx)
	r := &geminiContinuationBody{
		ctx: ctx, cancel: cancel, next: next, maxTokens: maxTokens, hops: 1,
		seen: map[string]bool{}, previous: map[string]int{},
	}
	r.startHop(body)
	return r
}

func (r *geminiContinuationBody) startHop(body io.ReadCloser) {
	r.mu.Lock()
	r.body = body
	r.mu.Unlock()
	if r.ctx.Err() != nil {
		_ = body.Close()
	}
	r.scanner = bufio.NewScanner(body)
	r.scanner.Buffer(make([]byte, 64*1024), defaultMaxLineSize)
	r.usage = map[string]int{}
	r.finish, r.token = "", ""
	r.hopEnded = false
	r.wrapped = false
}

// Delay the terminal event until trailing usage chunks have been consumed.
// Antigravity's Claude converter emits message_stop as soon as it sees it.
func (r *geminiContinuationBody) finishHop(reason string) error {
	usage := make(map[string]int, len(r.usage)+len(r.previous))
	for key, count := range r.previous {
		usage[key] = count
	}
	for key, count := range r.usage {
		usage[key] += count
	}
	response := map[string]any{
		"candidates":             []any{map[string]any{"finishReason": reason}},
		"usageMetadata":          usage,
		geminiContinuationHopKey: r.hops,
	}
	if r.wrapped {
		response = map[string]any{"response": response}
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return err
	}
	r.pending = append(append([]byte("data: "), encoded...), '\n', '\n')
	r.done = true
	return nil
}

func (r *geminiContinuationBody) Close() error {
	r.cancel()
	return r.closeCurrentBody()
}

func (r *geminiContinuationBody) closeCurrentBody() error {
	r.mu.Lock()
	body := r.body
	r.mu.Unlock()
	return body.Close()
}

func (r *geminiContinuationBody) confirmUsage() {
	counts := make(map[string]any, len(r.previous)+len(r.usage))
	for key, count := range r.previous {
		counts[key] = count
	}
	for key, count := range r.usage {
		counts[key] = r.previous[key] + count
	}
	usage := extractGeminiUsage(map[string]any{"usageMetadata": counts})
	r.mu.Lock()
	r.confirmed = *usage
	r.mu.Unlock()
}

// Only confirmed hops/checkpoints survive a failure. Do not bill speculative
// tokens from an unfinished stream or add the completed hop a second time.
func geminiPartialForwardResult(body io.ReadCloser, requestID, model string, stream bool, start time.Time) *ForwardResult {
	reader, ok := body.(*geminiContinuationBody)
	if !ok {
		return nil
	}
	reader.mu.Lock()
	usage := reader.confirmed
	reader.mu.Unlock()
	if usage.InputTokens+usage.OutputTokens+usage.CacheReadInputTokens == 0 {
		return nil
	}
	return &ForwardResult{RequestID: requestID, Model: model, Stream: stream,
		Usage: usage, Duration: time.Since(start), PartialUsage: true}
}

func (r *geminiContinuationBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(r.pending) == 0 {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
		if r.done {
			return 0, io.EOF
		}
		if r.hopEnded || !r.scanner.Scan() {
			if err := r.scanner.Err(); err != nil {
				return 0, err
			}
			if r.finish == "" {
				return 0, fmt.Errorf("Gemini stream ended without a finish reason")
			}
			if r.finish != "CONTINUATION" {
				if err := r.finishHop(r.finish); err != nil {
					return 0, err
				}
				continue
			}
			if r.previous["candidatesTokenCount"]+r.previous["thoughtsTokenCount"]+r.usage["candidatesTokenCount"]+r.usage["thoughtsTokenCount"] >= r.maxTokens {
				if err := r.finishHop("MAX_TOKENS"); err != nil {
					return 0, err
				}
				continue
			}
			if r.token == "" || r.seen[r.token] || r.hops >= 64 || r.usage["candidatesTokenCount"]+r.usage["thoughtsTokenCount"] <= 0 {
				return 0, fmt.Errorf("invalid Gemini continuation checkpoint or hop limit reached")
			}
			r.seen[r.token] = true
			for key, count := range r.usage {
				r.previous[key] += count
			}
			_ = r.closeCurrentBody()
			resp, err := r.next(r.ctx, r.token)
			if err != nil {
				return 0, fmt.Errorf("Gemini continuation request failed: %w", err)
			}
			if resp == nil || resp.Body == nil {
				return 0, fmt.Errorf("empty Gemini continuation response")
			}
			if resp.StatusCode != http.StatusOK {
				_ = resp.Body.Close()
				return 0, fmt.Errorf("Gemini continuation returned HTTP %d", resp.StatusCode)
			}
			r.hops++
			r.startHop(resp.Body)
			continue
		}
		line := strings.TrimSpace(r.scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			// Keep comments and blank lines visible to the outer reader's
			// idle timer, including during long stretches of model thinking.
			r.pending = append([]byte(r.scanner.Text()), '\n')
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			r.hopEnded = true
			continue
		}
		if payload == "" {
			r.pending = []byte("data:\n\n")
			continue
		}
		var envelope map[string]any
		if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
			return 0, fmt.Errorf("invalid Gemini continuation stream: %w", err)
		}
		if envelope == nil {
			return 0, fmt.Errorf("empty Gemini continuation event")
		}
		response := envelope
		if inner, ok := envelope["response"].(map[string]any); ok {
			response = inner
			r.wrapped = true
		}
		candidates, _ := response["candidates"].([]any)
		if len(candidates) == 0 {
			feedback, _ := response["promptFeedback"].(map[string]any)
			reason, _ := feedback["blockReason"].(string)
			if reason = strings.TrimSpace(reason); reason != "" && reason != "BLOCK_REASON_UNSPECIFIED" {
				// This is a complete rejection with no candidate/finishReason.
				// Preserve earlier confirmed hops, but do not bill this rejection.
				return 0, &geminiPromptBlockedError{reason: reason}
			}
		}
		if metadata, ok := response["usageMetadata"].(map[string]any); ok {
			for _, key := range []string{"promptTokenCount", "candidatesTokenCount", "thoughtsTokenCount", "cachedContentTokenCount", "totalTokenCount"} {
				if count, ok := asInt(metadata[key]); ok && count >= 0 {
					r.usage[key] = count
				}
				metadata[key] = r.previous[key] + r.usage[key]
			}
		}
		if len(candidates) > 0 {
			candidate, _ := candidates[0].(map[string]any)
			if token, _ := candidate["continuationToken"].(string); token != "" {
				r.token = token
			}
			// The default enum value does not finish a hop or confirm usage.
			// It also must not overwrite a terminal reason in a trailing chunk.
			if reason, _ := candidate["finishReason"].(string); isGeminiTerminalFinishReason(reason) {
				r.finish = reason
			}
			if checkpoint, _ := candidate["continuationToken"].(string); checkpoint != "" {
				r.confirmUsage()
			}
			delete(candidate, "finishReason")
			delete(candidate, "continuationToken")
		}
		if r.finish != "" {
			r.confirmUsage()
		}
		response[geminiContinuationHopKey] = r.hops
		encoded, err := json.Marshal(envelope)
		if err != nil {
			return 0, err
		}
		r.pending = append(append([]byte("data: "), encoded...), '\n', '\n')
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

func withGeminiContinuationToken(body []byte, token string) ([]byte, error) {
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, err
	}
	inner := request
	if wrapped, ok := request["request"].(map[string]any); ok {
		inner = wrapped
	}
	inner["continuationToken"] = token
	return json.Marshal(request)
}

func replayGeminiContinuationRequest(ctx context.Context, original *http.Request, token string) (*http.Request, error) {
	if original == nil || original.GetBody == nil {
		return nil, fmt.Errorf("Gemini continuation request is not replayable")
	}
	body, err := original.GetBody()
	if err != nil {
		return nil, err
	}
	defer body.Close()
	payload, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	payload, err = withGeminiContinuationToken(payload, token)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, original.Method, original.URL.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header = original.Header.Clone()
	return req, nil
}
