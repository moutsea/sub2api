package service

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Observe a single native Gemini hop without consuming continuation tokens or
// changing response parts. A truncated stream is billable only through its last
// checkpoint; a terminal response also confirms any trailing usage metadata.
// https://ai.google.dev/gemini-api/docs/generate-content/long-decode#understand-billing
type geminiNativeStreamBody struct {
	body      io.ReadCloser
	scanner   *bufio.Scanner
	pending   []byte
	usage     map[string]any
	finished  bool
	rejected  bool
	done      bool
	mu        sync.Mutex // The Antigravity reader may still be exiting on timeout.
	confirmed ClaudeUsage
}

func newGeminiNativeStreamBody(body io.ReadCloser, maxLineSize int) *geminiNativeStreamBody {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), maxLineSize)
	return &geminiNativeStreamBody{body: body, scanner: scanner, usage: map[string]any{}}
}

func (r *geminiNativeStreamBody) Close() error { return r.body.Close() }

func (r *geminiNativeStreamBody) confirmedUsage() ClaudeUsage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.confirmed
}

func (r *geminiNativeStreamBody) partialResult(requestID, model, tier string, stream bool, start time.Time) *ForwardResult {
	if r == nil {
		return nil
	}
	usage := r.confirmedUsage()
	if usage.InputTokens+usage.OutputTokens+usage.CacheReadInputTokens == 0 {
		return nil
	}
	return &ForwardResult{RequestID: requestID, Model: model, ServiceTier: tier,
		Stream: stream, Usage: usage, Duration: time.Since(start), PartialUsage: true}
}

func (r *geminiNativeStreamBody) endError() error {
	if !r.finished && !r.rejected {
		return fmt.Errorf("Gemini stream ended without a finish reason: %w", io.ErrUnexpectedEOF)
	}
	return io.EOF
}

func (r *geminiNativeStreamBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(r.pending) == 0 {
		if r.done {
			return 0, r.endError()
		}
		if !r.scanner.Scan() {
			if err := r.scanner.Err(); err != nil {
				return 0, err
			}
			r.done = true
			return 0, r.endError()
		}
		line := r.scanner.Text()
		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "[DONE]" {
				r.done = true
				if err := r.endError(); err != io.EOF {
					return 0, err
				}
			} else if payload != "" {
				response, err := unwrapGeminiResponse([]byte(payload))
				if err != nil {
					return 0, fmt.Errorf("invalid Gemini stream event: %w", err)
				}
				r.observe(response)
			}
		}
		// Preserve native finishReason, continuationToken, and all part fields.
		r.pending = append([]byte(line), '\n')
		if r.done {
			// The next Read ends the stream, so terminate the [DONE] SSE event here.
			r.pending = append(r.pending, '\n')
		}
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

func (r *geminiNativeStreamBody) observe(response map[string]any) {
	candidates, _ := response["candidates"].([]any)
	if len(candidates) == 0 {
		feedback, _ := response["promptFeedback"].(map[string]any)
		reason, _ := feedback["blockReason"].(string)
		if reason = strings.TrimSpace(reason); reason != "" && reason != "BLOCK_REASON_UNSPECIFIED" {
			r.rejected = true
		}
	}
	if metadata, ok := response["usageMetadata"].(map[string]any); ok {
		for key, value := range metadata {
			if count, ok := asInt(value); ok && count >= 0 {
				r.usage[key] = count
			}
		}
	}
	checkpoint := false
	if len(candidates) > 0 {
		candidate, _ := candidates[0].(map[string]any)
		token, _ := candidate["continuationToken"].(string)
		checkpoint = token != ""
		if reason, _ := candidate["finishReason"].(string); isGeminiTerminalFinishReason(reason) {
			r.finished = true
		}
	}
	if !r.rejected && (checkpoint || r.finished) {
		usage := extractGeminiUsage(map[string]any{"usageMetadata": r.usage})
		r.mu.Lock()
		r.confirmed = *usage
		r.mu.Unlock()
	}
}

func writeGeminiNativeStreamError(c *gin.Context, message string) {
	// Use the Google error envelope inside SSE, matching the native protocol.
	payload, _ := json.Marshal(gin.H{"error": gin.H{
		"code": http.StatusBadGateway, "status": "UNAVAILABLE", "message": message,
	}})
	_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", payload)
	c.Writer.Flush()
}
