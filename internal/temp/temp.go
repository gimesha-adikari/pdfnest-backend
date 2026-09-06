package temp

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	mu      sync.RWMutex
	tempDir string
)

// verifyWritable tests if a directory can be written to by creating and removing a probe file.
func verifyWritable(dir string) bool {
	probe := filepath.Join(dir, fmt.Sprintf(".probe-%d-%d", os.Getpid(), time.Now().UnixNano()))
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return false
	}
	_ = f.Close()
	_ = os.Remove(probe)
	return true
}

// ensureWritableDir checks if dir exists and is verified writable with 0700 permissions.
// If it does not exist, it tries to create it with 0700.
func ensureWritableDir(dir string) bool {
	if info, err := os.Stat(dir); err == nil {
		if !info.IsDir() {
			return false
		}
		return verifyWritable(dir)
	}

	if err := os.MkdirAll(dir, 0700); err != nil {
		return false
	}
	return verifyWritable(dir)
}

// resolveTempDir determines and verifies a private, process-owned writable temp directory.
func resolveTempDir() string {
	// 1. Try deterministic dedicated directory: /tmp/pdfnest-temp
	primary := filepath.Join(os.TempDir(), "pdfnest-temp")
	if ensureWritableDir(primary) {
		return primary
	}

	// 2. If primary exists but is not writable (e.g. root-owned, permission denied),
	// or failed to create, create a private process-owned directory with 0700 permissions.
	fallbackPattern := fmt.Sprintf("pdfnest-temp-p%d-*", os.Getpid())
	subDir, err := os.MkdirTemp(os.TempDir(), fallbackPattern)
	if err == nil && ensureWritableDir(subDir) {
		log.Printf("[TEMP] Primary temp dir %s was not writable; using process-owned fallback: %s", primary, subDir)
		return subDir
	}

	// 3. Fallback to system temp dir if all else fails
	log.Printf("[TEMP WARNING] Neither primary %s nor private temp dir could be verified writable; falling back to %s", primary, os.TempDir())
	return os.TempDir()
}

// GetDir returns the dedicated, process-owned, verified writable temporary directory path.
// It automatically ensures the directory exists and remains writable, self-healing if removed.
func GetDir() string {
	mu.RLock()
	current := tempDir
	mu.RUnlock()

	if current != "" {
		if info, err := os.Stat(current); err == nil && info.IsDir() {
			return current
		}
	}

	mu.Lock()
	defer mu.Unlock()

	// Double-check under write lock
	if tempDir != "" {
		if info, err := os.Stat(tempDir); err == nil && info.IsDir() {
			return tempDir
		}
	}

	tempDir = resolveTempDir()
	return tempDir
}

// ResetDirForTesting resets the cached temp directory for unit tests.
func ResetDirForTesting() {
	mu.Lock()
	defer mu.Unlock()
	tempDir = ""
}

// CreateTemp creates a new temporary file in the dedicated PDFNest temporary directory.
func CreateTemp(prefix, suffix string) (*os.File, error) {
	dir := GetDir()
	return os.CreateTemp(dir, prefix+"-*"+suffix)
}

// MkdirTemp creates a new temporary directory in the dedicated PDFNest temporary directory.
func MkdirTemp(prefix string) (string, error) {
	dir := GetDir()
	return os.MkdirTemp(dir, prefix+"-")
}

