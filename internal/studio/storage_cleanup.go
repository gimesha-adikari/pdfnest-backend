package studio

import (
	"context"
	"errors"
	"log"
	"time"

	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/studio/models"
)

const (
	storageCleanupBatchSize  = 50
	storageCleanupLease      = 5 * time.Minute
	storageCleanupInterval   = 30 * time.Second
	storageCleanupMaxBackoff = 15 * time.Minute
)

// StorageObjectDeleter is deliberately narrower than the storage subsystem so
// failure-injection tests can exercise durable cleanup without a live object
// store. Implementations must treat an absent object as success.
type StorageObjectDeleter interface {
	DeleteObject(context.Context, string) error
}

type StorageObjectDeleterFunc func(context.Context, string) error

func (f StorageObjectDeleterFunc) DeleteObject(ctx context.Context, key string) error {
	return f(ctx, key)
}

type storageCleanupWorker struct {
	repo    Repository
	deleter StorageObjectDeleter
}

// NewStorageCleanupWorker creates the process-safe retry worker used by the
// Studio service and backend lifecycle.
func NewStorageCleanupWorker(repo Repository, deleter StorageObjectDeleter) *storageCleanupWorker {
	if deleter == nil {
		deleter = StorageObjectDeleterFunc(deleteStudioObject)
	}
	return &storageCleanupWorker{repo: repo, deleter: deleter}
}

func (w *storageCleanupWorker) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = storageCleanupInterval
	}
	go func() {
		w.RunOnce(ctx)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				w.RunOnceAt(ctx, now.UTC())
			}
		}
	}()
}

func (w *storageCleanupWorker) RunOnce(ctx context.Context) {
	w.RunOnceAt(ctx, time.Now().UTC())
}

func (w *storageCleanupWorker) RunOnceAt(ctx context.Context, now time.Time) int {
	tasks, err := w.repo.ClaimStorageCleanupTasks(ctx, now, storageCleanupBatchSize, storageCleanupLease)
	if err != nil {
		log.Printf("[STUDIO STORAGE CLEANUP] claim failed: %v", err)
		return 0
	}
	succeeded := 0
	for _, task := range tasks {
		if w.RunTaskAt(ctx, task, now) {
			succeeded++
		}
	}
	if len(tasks) > 0 {
		log.Printf("[STUDIO STORAGE CLEANUP] processed=%d succeeded=%d pending=%d", len(tasks), succeeded, len(tasks)-succeeded)
	}
	return succeeded
}

func (w *storageCleanupWorker) RunTask(ctx context.Context, task models.StudioStorageCleanupTask) {
	w.RunTaskAt(ctx, task, time.Now().UTC())
}

func (w *storageCleanupWorker) RunTaskAt(ctx context.Context, task models.StudioStorageCleanupTask, now time.Time) bool {
	if err := w.deleter.DeleteObject(ctx, task.ObjectKey); err == nil {
		if deleteErr := w.repo.DeleteStorageCleanupTask(ctx, task.ID); deleteErr != nil {
			log.Printf("[STUDIO STORAGE CLEANUP] task completion failed: %v", deleteErr)
			return false
		}
		return true
	}
	attempts := task.AttemptCount + 1
	if err := w.repo.RescheduleStorageCleanupTask(ctx, task.ID, attempts, now.Add(storageCleanupBackoff(attempts)), "physical storage deletion failed"); err != nil {
		log.Printf("[STUDIO STORAGE CLEANUP] task reschedule failed: %v", err)
	}
	return false
}

func storageCleanupBackoff(attempts int) time.Duration {
	if attempts <= 1 {
		return 30 * time.Second
	}
	backoff := 30 * time.Second
	for i := 1; i < attempts && backoff < storageCleanupMaxBackoff; i++ {
		backoff *= 2
	}
	if backoff > storageCleanupMaxBackoff {
		return storageCleanupMaxBackoff
	}
	return backoff
}

func deleteStudioObject(ctx context.Context, key string) error {
	var errs []error
	if store, err := storage.Default(); err == nil && store != nil {
		if err := store.DeleteObject(ctx, key); err != nil {
			errs = append(errs, err)
		}
	}
	if err := storage.DeleteLocalObject(key); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
