package tasks

import (
	"log"
	"os"
	"time"
)

func StartCleanupWorker(checkInterval time.Duration, fileTTL time.Duration) {
	ticker := time.NewTicker(checkInterval)

	go func() {
		log.Printf("[CLEANUP ENGINE] Automated background disk sweeping daemon initialized (TTL: %v)", fileTTL)
		for range ticker.C {
			sweepExpiredTasks(fileTTL)
		}
	}()
}

func sweepExpiredTasks(ttl time.Duration) {
	Registry.mu.Lock()
	defer Registry.mu.Unlock()

	now := time.Now()
	evictionCount := 0

	log.Printf("[CLEANUP ENGINE] Scanning workspace environment tasks maps for stale artifacts...")

	for id, task := range Registry.tasks {
		if task.Status == "COMPLETED" || task.Status == "FAILED" {

			if task.ResultURL != "" {
				fileInfo, err := os.Stat(task.ResultURL)
				if err != nil {
					if os.IsNotExist(err) {
						delete(Registry.tasks, id)
						evictionCount++
					}
					continue
				}

				if now.Sub(fileInfo.ModTime()) > ttl {
					log.Printf("[CLEANUP ENGINE] Removing expired background task asset on node: %s", task.ResultURL)

					if err := os.Remove(task.ResultURL); err != nil && !os.IsNotExist(err) {
						log.Printf("[CLEANUP ENGINE ERROR] Failed to wipe expired file at %s: %v", task.ResultURL, err)
					}

					delete(Registry.tasks, id)
					evictionCount++
				}
			} else {
				delete(Registry.tasks, id)
				evictionCount++
			}
		}
	}

	if evictionCount > 0 {
		log.Printf("[CLEANUP ENGINE] Successfully collected and reclaimed workspace capacity. Evicted keys count: %d", evictionCount)
	}
}
