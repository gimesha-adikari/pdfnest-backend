package worker

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"time"
)

// AnalyzerWorker manages the bounded concurrency daemon consuming and executing repository analyzer jobs.
type AnalyzerWorker struct {
	cfg       WorkerConfig
	queue     JobQueue
	sem       chan struct{}
	wg        sync.WaitGroup
	stopCh    chan struct{}
	stopped   bool
	activeMu  sync.Mutex
	activeMap map[string]struct{}
	mu        sync.Mutex
}

// NewAnalyzerWorker initializes an AnalyzerWorker with the provided queue and configuration.
func NewAnalyzerWorker(cfg WorkerConfig, queue JobQueue) (*AnalyzerWorker, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	if queue == nil {
		return nil, fmt.Errorf("job queue is required")
	}

	return &AnalyzerWorker{
		cfg:       cfg,
		queue:     queue,
		sem:       make(chan struct{}, cfg.Concurrency),
		stopCh:    make(chan struct{}),
		activeMap: make(map[string]struct{}),
	}, nil
}

// Start begins consuming jobs from the queue until the context is cancelled or Stop is called.
func (w *AnalyzerWorker) Start(ctx context.Context) error {
	log.Printf("[Analyzer Worker] Started daemon with concurrency=%d, queue=%s, timeout=%s",
		w.cfg.Concurrency, w.cfg.QueueName, w.cfg.JobTimeout)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[Analyzer Worker] Context cancelled, initiating shutdown")
			return ctx.Err()
		case <-w.stopCh:
			log.Printf("[Analyzer Worker] Stop signal received, initiating shutdown")
			return nil
		default:
		}

		// Acquire bounded concurrency slot
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.stopCh:
			return nil
		case w.sem <- struct{}{}:
		}

		// Receive job from queue
		job, receiptHandle, err := w.queue.Receive(ctx)
		if err != nil {
			<-w.sem // Release slot on receive error
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("[Analyzer Worker] Queue receive error: %v", err)
			time.Sleep(200 * time.Millisecond)
			continue
		}

		if job == nil {
			<-w.sem // Release slot when no job was available in this polling cycle
			continue
		}

		// Dispatch job execution
		w.wg.Add(1)
		go func(j *AnalyzerJob, handle string) {
			defer func() {
				<-w.sem
				w.wg.Done()
			}()
			w.processJob(ctx, j, handle)
		}(job, receiptHandle)
	}
}

func (w *AnalyzerWorker) processJob(parentCtx context.Context, job *AnalyzerJob, receiptHandle string) {
	w.trackJobStart(job.TaskID)
	defer w.trackJobEnd(job.TaskID)

	// Recover panics to isolate individual job crashes
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[Analyzer Worker] PANIC recovered in task %s: %v\nStack: %s",
				job.TaskID, r, string(debug.Stack()))
			_ = w.queue.PublishProgress(context.Background(), job.TaskID, TaskProgress{
				TaskID:          job.TaskID,
				SessionID:       job.SessionID,
				Status:          StatusFailed,
				ProgressPercent: 100,
				StageMessage:    "Internal execution panic",
				ErrorMessage:    fmt.Sprintf("internal worker panic: %v", r),
				UpdatedAt:       time.Now().UTC(),
			})
			_ = w.queue.Nack(context.Background(), receiptHandle, false)
		}
	}()

	log.Printf("[Analyzer Worker] Processing task=%s session=%s source=%s",
		job.TaskID, job.SessionID, job.SourceType)

	jobCtx, cancel := context.WithTimeout(parentCtx, w.cfg.JobTimeout)
	defer cancel()

	progressHandler := func(status TaskStatus, percent int, msg string) {
		progress := TaskProgress{
			TaskID:          job.TaskID,
			SessionID:       job.SessionID,
			Status:          status,
			ProgressPercent: percent,
			StageMessage:    msg,
			UpdatedAt:       time.Now().UTC(),
		}
		_ = w.queue.PublishProgress(jobCtx, job.TaskID, progress)
	}

	res, err := ExecutePipeline(jobCtx, job, w.cfg.SandboxBaseDir, progressHandler)
	if err != nil {
		log.Printf("[Analyzer Worker] Task %s failed: %v", job.TaskID, err)
		_ = w.queue.PublishProgress(context.Background(), job.TaskID, TaskProgress{
			TaskID:          job.TaskID,
			SessionID:       job.SessionID,
			Status:          StatusFailed,
			ProgressPercent: 100,
			StageMessage:    "Analysis failed",
			ErrorMessage:    err.Error(),
			UpdatedAt:       time.Now().UTC(),
		})
		_ = w.queue.Nack(context.Background(), receiptHandle, false)
		return
	}

	// Publish canonical result and acknowledge job
	if pubErr := w.queue.PublishResult(context.Background(), job.TaskID, res); pubErr != nil {
		log.Printf("[Analyzer Worker] Failed to publish result for task %s: %v", job.TaskID, pubErr)
		_ = w.queue.Nack(context.Background(), receiptHandle, true)
		return
	}

	_ = w.queue.PublishProgress(context.Background(), job.TaskID, TaskProgress{
		TaskID:          job.TaskID,
		SessionID:       job.SessionID,
		Status:          StatusCompleted,
		ProgressPercent: 100,
		StageMessage:    "Analysis completed successfully",
		Result:          res,
		UpdatedAt:       time.Now().UTC(),
	})

	_ = w.queue.Ack(context.Background(), receiptHandle)
	log.Printf("[Analyzer Worker] Task %s completed successfully in %dms (files=%d, bytes=%d)",
		job.TaskID, res.Provenance.DurationMs, res.Metrics.TotalFiles, res.Metrics.TotalBytes)
}

func (w *AnalyzerWorker) trackJobStart(taskID string) {
	w.activeMu.Lock()
	defer w.activeMu.Unlock()
	w.activeMap[taskID] = struct{}{}
}

func (w *AnalyzerWorker) trackJobEnd(taskID string) {
	w.activeMu.Lock()
	defer w.activeMu.Unlock()
	delete(w.activeMap, taskID)
}

// ActiveJobCount returns the number of concurrently running analysis jobs.
func (w *AnalyzerWorker) ActiveJobCount() int {
	w.activeMu.Lock()
	defer w.activeMu.Unlock()
	return len(w.activeMap)
}

// Stop signals the worker to finish currently processing jobs and gracefully terminate within timeout.
func (w *AnalyzerWorker) Stop(timeout time.Duration) error {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return nil
	}
	w.stopped = true
	close(w.stopCh)
	w.mu.Unlock()

	log.Printf("[Analyzer Worker] Waiting for active jobs to complete (timeout=%s)...", timeout)

	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Printf("[Analyzer Worker] All active jobs finished cleanly")
	case <-time.After(timeout):
		log.Printf("[Analyzer Worker] Graceful shutdown timeout exceeded, forcing stop")
	}

	return w.queue.Close()
}
