package tasks

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func StartCleanupWorker(checkInterval time.Duration, fileTTL time.Duration) {
	ticker := time.NewTicker(checkInterval)

	go func() {
		log.Printf("[CLEANUP ENGINE] Automated background disk sweeping daemon initialized (TTL: %v)", fileTTL)
		for range ticker.C {
			sweepExpiredTempFiles(fileTTL)
		}
	}()
}

func sweepExpiredTempFiles(ttl time.Duration) {
	tempDir := os.TempDir()
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		return
	}

	now := time.Now()
	evictionCount := 0

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, "pdfnest-") || strings.HasPrefix(name, "compressed-") || strings.HasPrefix(name, "extracted-") || strings.HasPrefix(name, "web-compiled-") || strings.HasPrefix(name, "office-compiled-") {
			fullPath := filepath.Join(tempDir, name)
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if now.Sub(info.ModTime()) > ttl {
				if err := os.Remove(fullPath); err == nil {
					evictionCount++
				}
			}
		}
	}

	if evictionCount > 0 {
		log.Printf("[CLEANUP ENGINE] Successfully collected and reclaimed workspace capacity. Evicted temp files count: %d", evictionCount)
	}
}
