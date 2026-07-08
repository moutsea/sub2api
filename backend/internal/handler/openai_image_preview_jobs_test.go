package handler

import (
	"context"
	"testing"
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
