package service

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	openAIImagesJSONHeartbeatDelay    = 85 * time.Second
	openAIImagesJSONHeartbeatInterval = 15 * time.Second
	openAIImagesJSONContentType       = "application/json; charset=utf-8"

	OpenAIImagesDisableJSONHeartbeatContextKey = "openai_images_disable_json_heartbeat"
)

// openAIImagesJSONHeartbeat keeps long-running synchronous image requests alive
// behind Cloudflare by writing JSON-valid leading whitespace before the final
// response body. Once it writes, the HTTP status is committed as 200, so late
// upstream failures are returned as OpenAI-compatible JSON error bodies.
type openAIImagesJSONHeartbeat struct {
	writer     gin.ResponseWriter
	flusher    http.Flusher
	requestCtx <-chan struct{}
	status     int
	delay      time.Duration
	interval   time.Duration
	stopCh     chan struct{}
	doneCh     chan struct{}
	stopOnce   sync.Once
	committed  atomic.Bool
}

func startOpenAIImagesJSONHeartbeat(c *gin.Context) *openAIImagesJSONHeartbeat {
	return startOpenAIImagesJSONHeartbeatWithTiming(
		c,
		http.StatusOK,
		openAIImagesJSONHeartbeatDelay,
		openAIImagesJSONHeartbeatInterval,
	)
}

func startOpenAIImagesJSONHeartbeatWithTiming(
	c *gin.Context,
	status int,
	delay time.Duration,
	interval time.Duration,
) *openAIImagesJSONHeartbeat {
	heartbeat := &openAIImagesJSONHeartbeat{
		status:   status,
		delay:    delay,
		interval: interval,
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
	if c != nil && c.GetBool(OpenAIImagesDisableJSONHeartbeatContextKey) {
		close(heartbeat.doneCh)
		return heartbeat
	}
	if c == nil || c.Writer == nil {
		close(heartbeat.doneCh)
		return heartbeat
	}
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		close(heartbeat.doneCh)
		return heartbeat
	}
	if heartbeat.status <= 0 {
		heartbeat.status = http.StatusOK
	}
	if heartbeat.delay < 0 {
		heartbeat.delay = 0
	}
	if heartbeat.interval <= 0 {
		heartbeat.interval = openAIImagesJSONHeartbeatInterval
	}
	heartbeat.writer = c.Writer
	heartbeat.flusher = flusher
	if c.Request != nil {
		heartbeat.requestCtx = c.Request.Context().Done()
	}

	go heartbeat.run()
	return heartbeat
}

func (h *openAIImagesJSONHeartbeat) run() {
	defer close(h.doneCh)
	if h.writer == nil || h.flusher == nil {
		return
	}

	delay := h.delay
	if h.writer.Written() && delay > h.interval {
		delay = h.interval
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-h.stopCh:
		return
	case <-h.requestCtx:
		return
	case <-timer.C:
	}

	if !h.writeWhitespace() {
		return
	}

	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	for {
		select {
		case <-h.stopCh:
			return
		case <-h.requestCtx:
			return
		case <-ticker.C:
			if !h.writeWhitespace() {
				return
			}
		}
	}
}

func (h *openAIImagesJSONHeartbeat) writeWhitespace() bool {
	if h.writer == nil || h.flusher == nil {
		return false
	}
	header := h.writer.Header()
	if strings.TrimSpace(header.Get("Content-Type")) == "" {
		header.Set("Content-Type", openAIImagesJSONContentType)
	}
	header.Set("Cache-Control", "no-cache")
	header.Set("X-Accel-Buffering", "no")
	if !h.writer.Written() {
		h.writer.WriteHeader(h.status)
	}
	if _, err := h.writer.Write([]byte(" \n")); err != nil {
		return false
	}
	h.committed.Store(true)
	h.flusher.Flush()
	return true
}

func (h *openAIImagesJSONHeartbeat) stop() bool {
	if h == nil {
		return false
	}
	h.stopOnce.Do(func() {
		close(h.stopCh)
	})
	<-h.doneCh
	return h.committed.Load()
}

func writeOpenAIImagesJSONResponse(
	c *gin.Context,
	heartbeat *openAIImagesJSONHeartbeat,
	status int,
	contentType string,
	body []byte,
) error {
	committed := heartbeat.stop()
	if strings.TrimSpace(contentType) == "" {
		contentType = openAIImagesJSONContentType
	}
	if committed || (c != nil && c.Writer != nil && c.Writer.Written()) {
		if c == nil || c.Writer == nil {
			return nil
		}
		if _, err := c.Writer.Write(body); err != nil {
			return err
		}
		if flusher, ok := c.Writer.(http.Flusher); ok {
			flusher.Flush()
		}
		return nil
	}
	c.Data(status, contentType, body)
	return nil
}

func finishOpenAIImagesError(
	c *gin.Context,
	heartbeat *openAIImagesJSONHeartbeat,
	err error,
	errType string,
	message string,
) error {
	committed := heartbeat.stop()
	if !committed && c != nil && c.Writer != nil && c.Writer.Written() {
		committed = true
	}
	if !committed {
		return err
	}
	if strings.TrimSpace(errType) == "" {
		errType = "upstream_error"
	}
	if strings.TrimSpace(message) == "" {
		message = "Upstream request failed"
	}
	writeOpenAIImagesErrorBody(c, errType, message)
	return err
}

func writeOpenAIImagesErrorBody(c *gin.Context, errType string, message string) {
	if c == nil || c.Writer == nil {
		return
	}
	payload, marshalErr := json.Marshal(map[string]any{
		"error": map[string]string{
			"type":    errType,
			"message": message,
		},
	})
	if marshalErr != nil {
		return
	}
	_, _ = c.Writer.Write(payload)
	if flusher, ok := c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
}
