package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestImagePreviewJobStoreCancelRequiresOwner(t *testing.T) {
	store := newImagePreviewJobStore()
	job := store.create(101)

	if _, ok := store.cancel(job.ID, 202); ok {
		t.Fatal("cancel with a different API key should be rejected")
	}

	got, ok := store.get(job.ID)
	if !ok {
		t.Fatal("job should still exist")
	}
	if got.Status != imagePreviewJobQueued {
		t.Fatalf("status = %s, want %s", got.Status, imagePreviewJobQueued)
	}

	if _, ok := store.cancel(job.ID, 101); !ok {
		t.Fatal("owner should be able to cancel job")
	}
	got, ok = store.get(job.ID)
	if !ok {
		t.Fatal("job should still exist after cancel")
	}
	if got.Status != imagePreviewJobCanceled {
		t.Fatalf("status = %s, want %s", got.Status, imagePreviewJobCanceled)
	}
}

func TestImagePreviewJobStoreCancelInvokesCancelFunc(t *testing.T) {
	store := newImagePreviewJobStore()
	job := store.create(101)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store.update(job.ID, func(job *imagePreviewJob) {
		job.Status = imagePreviewJobRunning
		job.cancel = cancel
	})

	if _, ok := store.cancel(job.ID, 101); !ok {
		t.Fatal("owner should be able to cancel running job")
	}
	if ctx.Err() == nil {
		t.Fatal("expected job context to be canceled")
	}
}

func TestNormalizeImagePreviewJobEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		want     string
		wantOkay bool
	}{
		{name: "empty defaults to generations", input: "", want: imagePreviewJobGenerationsEndpoint, wantOkay: true},
		{name: "generations alias", input: "generations", want: imagePreviewJobGenerationsEndpoint, wantOkay: true},
		{name: "generations absolute", input: "/v1/images/generations", want: imagePreviewJobGenerationsEndpoint, wantOkay: true},
		{name: "edits alias", input: "edits", want: imagePreviewJobEditsEndpoint, wantOkay: true},
		{name: "edits absolute", input: "/v1/images/edits", want: imagePreviewJobEditsEndpoint, wantOkay: true},
		{name: "reject other endpoint", input: "/v1/chat/completions", want: "", wantOkay: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := normalizeImagePreviewJobEndpoint(tt.input)
			if ok != tt.wantOkay {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOkay)
			}
			if got != tt.want {
				t.Fatalf("endpoint = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestImagePreviewJobEndpointFromRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("header endpoint", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/image-preview/jobs", nil)
		c.Request.Header.Set("X-Image-Preview-Endpoint", "edits")

		got, ok := imagePreviewJobEndpointFromRequest(c)
		if !ok {
			t.Fatal("expected endpoint to be accepted")
		}
		if got != imagePreviewJobEditsEndpoint {
			t.Fatalf("endpoint = %q, want %q", got, imagePreviewJobEditsEndpoint)
		}
	})

	t.Run("multipart defaults to edits", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/image-preview/jobs", nil)
		c.Request.Header.Set("Content-Type", "multipart/form-data; boundary=test")

		got, ok := imagePreviewJobEndpointFromRequest(c)
		if !ok {
			t.Fatal("expected endpoint to be accepted")
		}
		if got != imagePreviewJobEditsEndpoint {
			t.Fatalf("endpoint = %q, want %q", got, imagePreviewJobEditsEndpoint)
		}
	})
}
