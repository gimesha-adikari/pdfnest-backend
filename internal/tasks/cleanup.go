package tasks

import (
	"log"
	"os"
	"path/filepath"
	"pdfnest-backend/internal/temp"
	"strings"
	"time"
)

var allowedTempPrefixes = []string{
	"pdfnest-",
	"split-",
	"merged-",
	"inserted-",
	"deleted-",
	"numbered-",
	"texted-",
	"reorder-",
	"cropped-",
	"rotated-",
	"duplicated-",
	"metadata-",
	"watermarked-",
	"pdf-page-",
	"html-compiled-",
	"md-compiled-",
	"img-compiled-",
	"code-compiled-",
	"office-compiled-",
	"ocr-compiled-",
	"r2-img-",
	"compressed-",
	"extracted-",
	"tmp-",
	"chromedp-runner-",
}

func isPDFNestTempName(name string) bool {
	for _, prefix := range allowedTempPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func StartCleanupWorker(checkInterval time.Duration, fileTTL time.Duration) {
	ticker := time.NewTicker(checkInterval)

	go func() {
		log.Printf("[CLEANUP ENGINE] Automated background disk sweeping daemon initialized (TTL: %v)", fileTTL)
		for range ticker.C {
			sweepExpiredTempFiles(fileTTL)
		}
	}()
}

func sweepDir(dirPath string, ttl time.Duration, checkPrefix bool) int {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return 0
	}

	now := time.Now()
	evictionCount := 0

	for _, entry := range entries {
		name := entry.Name()
		if checkPrefix && !isPDFNestTempName(name) {
			continue
		}

		fullPath := filepath.Join(dirPath, name)

		lstatInfo, err := os.Lstat(fullPath)
		if err != nil {
			continue
		}

		// Symlink safety: do not follow symlinks outside target directory
		if lstatInfo.Mode()&os.ModeSymlink != 0 {
			_ = os.Remove(fullPath)
			evictionCount++
			continue
		}

		if now.Sub(lstatInfo.ModTime()) > ttl {
			if lstatInfo.IsDir() {
				if err := os.RemoveAll(fullPath); err == nil {
					evictionCount++
				}
			} else {
				if err := os.Remove(fullPath); err == nil {
					evictionCount++
				}
			}
		}
	}

	return evictionCount
}

func sweepExpiredTempFiles(ttl time.Duration) {
	evictionCount := 0

	// Sweep the dedicated workspace before scanning the shared system temp directory.
	dedicatedDir := temp.GetDir()
	if dedicatedDir != os.TempDir() {
		evictionCount += sweepDir(dedicatedDir, ttl, false)
	}

	// The shared temp directory is restricted to known application prefixes.
	evictionCount += sweepDir(os.TempDir(), ttl, true)

	if evictionCount > 0 {
		log.Printf("[CLEANUP ENGINE] Successfully collected and reclaimed workspace capacity. Evicted temp files count: %d", evictionCount)
	}
}
