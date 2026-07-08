package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	imagePreviewJobTimeout = 10 * time.Minute
	imagePreviewJobTTL     = 30 * time.Minute
	imagePreviewJobMaxSize = 200
)

type imagePreviewJobStatus string

const (
	imagePreviewJobQueued    imagePreviewJobStatus = "queued"
	imagePreviewJobRunning   imagePreviewJobStatus = "running"
	imagePreviewJobSucceeded imagePreviewJobStatus = "succeeded"
	imagePreviewJobFailed    imagePreviewJobStatus = "failed"
	imagePreviewJobCanceled  imagePreviewJobStatus = "canceled"
)

type imagePreviewJob struct {
	ID          string
	APIKeyID    int64
	Status      imagePreviewJobStatus
	StatusCode  int
	ContentType string
	Result      json.RawMessage
	Error       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	CompletedAt *time.Time
	cancel      context.CancelFunc
}

type imagePreviewJobStore struct {
	mu   sync.RWMutex
	jobs map[string]*imagePreviewJob
}

func newImagePreviewJobStore() *imagePreviewJobStore {
	return &imagePreviewJobStore{jobs: make(map[string]*imagePreviewJob)}
}

func (s *imagePreviewJobStore) create(apiKeyID int64) *imagePreviewJob {
	now := time.Now()
	job := &imagePreviewJob{
		ID:        "imgjob_" + uuid.NewString(),
		APIKeyID:  apiKeyID,
		Status:    imagePreviewJobQueued,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = job
	s.cleanupLocked(now)
	return job
}

func (s *imagePreviewJobStore) get(id string) (*imagePreviewJob, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, ok := s.jobs[id]
	if !ok {
		return nil, false
	}
	return cloneImagePreviewJob(job), true
}

func (s *imagePreviewJobStore) update(id string, fn func(*imagePreviewJob)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job, ok := s.jobs[id]; ok {
		fn(job)
		job.UpdatedAt = time.Now()
	}
}

func (s *imagePreviewJobStore) cancel(id string, apiKeyID int64) (*imagePreviewJob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok || job.APIKeyID != apiKeyID {
		return nil, false
	}
	if job.Status == imagePreviewJobQueued || job.Status == imagePreviewJobRunning {
		if job.cancel != nil {
			job.cancel()
		}
		now := time.Now()
		job.Status = imagePreviewJobCanceled
		job.Error = "Image preview job was canceled"
		job.UpdatedAt = now
		job.CompletedAt = &now
	}
	return cloneImagePreviewJob(job), true
}

func (s *imagePreviewJobStore) cleanupLocked(now time.Time) {
	for id, job := range s.jobs {
		if now.Sub(job.UpdatedAt) > imagePreviewJobTTL {
			delete(s.jobs, id)
		}
	}
	if len(s.jobs) <= imagePreviewJobMaxSize {
		return
	}
	jobs := make([]*imagePreviewJob, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].UpdatedAt.Before(jobs[j].UpdatedAt)
	})
	for len(s.jobs) > imagePreviewJobMaxSize && len(jobs) > 0 {
		job := jobs[0]
		jobs = jobs[1:]
		delete(s.jobs, job.ID)
	}
}

type imagePreviewJobResponse struct {
	ID          string                `json:"id"`
	Status      imagePreviewJobStatus `json:"status"`
	StatusCode  int                   `json:"status_code,omitempty"`
	ContentType string                `json:"content_type,omitempty"`
	Result      json.RawMessage       `json:"result,omitempty"`
	Error       string                `json:"error,omitempty"`
	CreatedAt   time.Time             `json:"created_at"`
	UpdatedAt   time.Time             `json:"updated_at"`
	CompletedAt *time.Time            `json:"completed_at,omitempty"`
}

func imagePreviewJobSnapshot(job *imagePreviewJob) imagePreviewJobResponse {
	if job == nil {
		return imagePreviewJobResponse{}
	}
	return imagePreviewJobResponse{
		ID:          job.ID,
		Status:      job.Status,
		StatusCode:  job.StatusCode,
		ContentType: job.ContentType,
		Result:      job.Result,
		Error:       job.Error,
		CreatedAt:   job.CreatedAt,
		UpdatedAt:   job.UpdatedAt,
		CompletedAt: job.CompletedAt,
	}
}

func cloneImagePreviewJob(job *imagePreviewJob) *imagePreviewJob {
	if job == nil {
		return nil
	}
	cloned := *job
	if job.Result != nil {
		cloned.Result = append(json.RawMessage(nil), job.Result...)
	}
	cloned.cancel = nil
	return &cloned
}

func (h *OpenAIGatewayHandler) ensureImagePreviewJobs() *imagePreviewJobStore {
	if h.imagePreviewJobs == nil {
		h.imagePreviewJobs = newImagePreviewJobStore()
	}
	return h.imagePreviewJobs
}

func (h *OpenAIGatewayHandler) CreateImagePreviewJob(c *gin.Context) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	body, err := c.GetRawData()
	if err != nil || len(body) == 0 {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}

	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	userRole, _ := middleware2.GetUserRoleFromContext(c)
	job := h.ensureImagePreviewJobs().create(apiKey.ID)
	snapshot := imagePreviewJobSnapshot(job)

	headers := c.Request.Header.Clone()
	remoteAddr := c.Request.RemoteAddr
	go h.runImagePreviewJob(job.ID, body, headers, remoteAddr, apiKey, subject, subscription, userRole)

	c.JSON(http.StatusAccepted, snapshot)
}

func (h *OpenAIGatewayHandler) GetImagePreviewJob(c *gin.Context) {
	job, ok := h.authorizedImagePreviewJob(c)
	if !ok {
		return
	}
	status := http.StatusOK
	if job.Status == imagePreviewJobQueued || job.Status == imagePreviewJobRunning {
		status = http.StatusAccepted
	}
	c.JSON(status, imagePreviewJobSnapshot(job))
}

func (h *OpenAIGatewayHandler) CancelImagePreviewJob(c *gin.Context) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}
	jobID := c.Param("id")
	job, ok := h.ensureImagePreviewJobs().cancel(jobID, apiKey.ID)
	if !ok {
		h.errorResponse(c, http.StatusNotFound, "not_found", "Image preview job not found")
		return
	}
	c.JSON(http.StatusOK, imagePreviewJobSnapshot(job))
}

func (h *OpenAIGatewayHandler) authorizedImagePreviewJob(c *gin.Context) (*imagePreviewJob, bool) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return nil, false
	}
	job, ok := h.ensureImagePreviewJobs().get(c.Param("id"))
	if !ok || job.APIKeyID != apiKey.ID {
		h.errorResponse(c, http.StatusNotFound, "not_found", "Image preview job not found")
		return nil, false
	}
	return job, true
}

func (h *OpenAIGatewayHandler) runImagePreviewJob(
	jobID string,
	body []byte,
	headers http.Header,
	remoteAddr string,
	apiKey *service.APIKey,
	subject middleware2.AuthSubject,
	subscription *service.UserSubscription,
	userRole string,
) {
	ctx, cancel := context.WithTimeout(context.Background(), imagePreviewJobTimeout)
	h.ensureImagePreviewJobs().update(jobID, func(job *imagePreviewJob) {
		if job.Status == imagePreviewJobCanceled {
			cancel()
			return
		}
		job.Status = imagePreviewJobRunning
		job.cancel = cancel
	})
	defer cancel()
	if ctx.Err() != nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[ImagePreviewJob] panic job_id=%s error=%v", jobID, r)
			now := time.Now()
			h.ensureImagePreviewJobs().update(jobID, func(job *imagePreviewJob) {
				job.Status = imagePreviewJobFailed
				job.StatusCode = http.StatusInternalServerError
				job.Error = fmt.Sprintf("image preview job panic: %v", r)
				job.CompletedAt = &now
			})
		}
	}()

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body)).WithContext(ctx)
	req.Header = headers.Clone()
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remoteAddr
	if apiKey != nil && apiKey.Group != nil {
		req = req.WithContext(context.WithValue(req.Context(), ctxkey.Group, apiKey.Group))
	}
	ginCtx.Request = req
	ginCtx.Set(string(middleware2.ContextKeyAPIKey), apiKey)
	ginCtx.Set(string(middleware2.ContextKeyUser), subject)
	if userRole != "" {
		ginCtx.Set(string(middleware2.ContextKeyUserRole), userRole)
	}
	if subscription != nil {
		ginCtx.Set(string(middleware2.ContextKeySubscription), subscription)
	}

	h.Images(ginCtx)

	now := time.Now()
	statusCode := recorder.Code
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	responseBody := bytes.TrimSpace(recorder.Body.Bytes())
	contentType := recorder.Header().Get("Content-Type")
	ctxErr := ctx.Err()
	responseSucceeded := statusCode >= 200 && statusCode < 300 && json.Valid(responseBody)
	h.ensureImagePreviewJobs().update(jobID, func(job *imagePreviewJob) {
		if job.Status == imagePreviewJobCanceled && !responseSucceeded {
			job.cancel = nil
			return
		}
		job.StatusCode = statusCode
		job.ContentType = contentType
		job.CompletedAt = &now
		job.cancel = nil

		if responseSucceeded {
			job.Status = imagePreviewJobSucceeded
			job.Error = ""
			job.Result = json.RawMessage(responseBody)
			return
		}

		if ctxErr != nil {
			if json.Valid(responseBody) {
				job.Result = json.RawMessage(responseBody)
			}
			if errors.Is(ctxErr, context.Canceled) {
				job.Status = imagePreviewJobCanceled
				job.Error = "Image preview job was canceled"
				return
			}
			if statusCode < http.StatusBadRequest {
				job.StatusCode = http.StatusGatewayTimeout
			}
			job.Status = imagePreviewJobFailed
			job.Error = "Image preview job timed out"
			if extracted := extractImagePreviewJobError(responseBody); extracted != "" {
				job.Error = extracted
			}
			return
		}

		if statusCode >= 200 && statusCode < 300 {
			job.Status = imagePreviewJobFailed
			job.Error = "Image preview job returned invalid JSON"
			return
		}
		job.Status = imagePreviewJobFailed
		if json.Valid(responseBody) {
			job.Result = json.RawMessage(responseBody)
		}
		job.Error = extractImagePreviewJobError(responseBody)
		if job.Error == "" {
			job.Error = http.StatusText(statusCode)
		}
	})
}

func extractImagePreviewJobError(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return string(body)
	}
	if errObj, ok := payload["error"].(map[string]any); ok {
		if msg, ok := errObj["message"].(string); ok {
			return msg
		}
	}
	for _, key := range []string{"message", "detail", "error"} {
		if msg, ok := payload[key].(string); ok {
			return msg
		}
	}
	return ""
}
