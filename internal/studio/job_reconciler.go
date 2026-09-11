package studio

import (
	"context"
	"log"
	"time"
)

const (
	studioJobReconciliationBatchSize = 10
	studioJobReconciliationInterval  = 10 * time.Second
	studioJobReconciliationTimeout   = 45 * time.Second
)

// StartJobReconciliationWorker starts the bounded, durable Studio job sweep.
// The worker deliberately shares the coordinator's reconciliation path with
// GET so abandoned browser sessions cannot strand successful worker output.
func StartJobReconciliationWorker(ctx context.Context, coordinator StudioJobCoordinator, interval time.Duration) {
	if interval <= 0 {
		interval = studioJobReconciliationInterval
	}
	go func() {
		run := func() {
			runCtx, cancel := context.WithTimeout(ctx, studioJobReconciliationTimeout)
			completed, err := coordinator.ReconcilePending(runCtx, studioJobReconciliationBatchSize)
			cancel()
			if err != nil {
				log.Printf("[STUDIO JOB RECONCILIATION] completed=%d error=%v", completed, err)
			} else if completed > 0 {
				log.Printf("[STUDIO JOB RECONCILIATION] completed=%d", completed)
			}
		}
		run()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
}
