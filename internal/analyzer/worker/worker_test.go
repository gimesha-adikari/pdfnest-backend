package worker

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pdfnest-backend/internal/analyzer/engine"
)

func TestWorkerBoundedConcurrency(t *testing.T) {
	q := NewMemoryJobQueue(20)
	zipPath := createTestRepoZip(t)

	cfg := DefaultWorkerConfig()
	cfg.Concurrency = 4
	cfg.SandboxBaseDir = t.TempDir()

	w, err := NewAnalyzerWorker(cfg, q)
	require.NoError(t, err)

	var maxConcurrentObserved int32

	// Push 8 valid jobs
	for i := 1; i <= 8; i++ {
		job := &AnalyzerJob{
			JobVersion:        JobVersion1,
			TaskID:            fmt.Sprintf("task-concurrent-%d", i),
			SessionID:         fmt.Sprintf("session-concurrent-%d", i),
			SourceType:        engine.SourceTypeZip,
			StagedArchivePath: zipPath,
		}
		require.NoError(t, q.Push(job))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Monitor active concurrency in background
	stopMonitor := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopMonitor:
				return
			case <-ticker.C:
				current := int32(w.ActiveJobCount())
				for {
					max := atomic.LoadInt32(&maxConcurrentObserved)
					if current <= max || atomic.CompareAndSwapInt32(&maxConcurrentObserved, max, current) {
						break
					}
				}
			}
		}
	}()

	// Start worker in background
	go func() {
		_ = w.Start(ctx)
	}()

	// Wait for jobs to complete
	require.Eventually(t, func() bool {
		for i := 1; i <= 8; i++ {
			if _, exists := q.GetResult(fmt.Sprintf("task-concurrent-%d", i)); !exists {
				return false
			}
		}
		return true
	}, 10*time.Second, 100*time.Millisecond)

	close(stopMonitor)
	assert.NoError(t, w.Stop(5*time.Second))

	// Verify maximum concurrency never exceeded configured capacity of 4
	observed := atomic.LoadInt32(&maxConcurrentObserved)
	assert.True(t, observed <= 4, "Maximum active concurrent jobs (%d) must not exceed 4", observed)
}

func TestWorkerJobTimeoutHandling(t *testing.T) {
	q := NewMemoryJobQueue(10)

	cfg := DefaultWorkerConfig()
	cfg.JobTimeout = 10 * time.Millisecond // Ultra short timeout to force deadline exceeded
	cfg.SandboxBaseDir = t.TempDir()

	w, err := NewAnalyzerWorker(cfg, q)
	require.NoError(t, err)

	job := &AnalyzerJob{
		JobVersion: JobVersion1,
		TaskID:     "task-timeout-1",
		SessionID:  "session-timeout-1",
		SourceType: engine.SourceTypeGit,
		GitURL:     "https://github.com/facebook/react.git",
	}
	require.NoError(t, q.Push(job))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = w.Start(ctx)
	}()

	// Wait for task to be marked as failed
	require.Eventually(t, func() bool {
		p, exists := q.GetProgress("task-timeout-1")
		return exists && p.Status == StatusFailed
	}, 5*time.Second, 50*time.Millisecond)

	assert.NoError(t, w.Stop(2*time.Second))
}
