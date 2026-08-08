package tasks

import (
	"os"
	"path/filepath"
	"pdfnest-backend/internal/temp"
	"testing"
	"time"
)

func TestJanitor_SweepAllPrefixesAndDirs(t *testing.T) {
	tmpDir := temp.GetDir()

	// 1. Create an expired PDFNest file (mtime = 2 hours ago)
	expiredFile := filepath.Join(tmpDir, "split-test-expired.pdf")
	_ = os.WriteFile(expiredFile, []byte("test content"), 0600)
	oldTime := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(expiredFile, oldTime, oldTime)

	// 2. Create an expired PDFNest directory (mtime = 2 hours ago)
	expiredDir := filepath.Join(tmpDir, "reorder-test-expired")
	_ = os.MkdirAll(expiredDir, 0700)
	_ = os.WriteFile(filepath.Join(expiredDir, "page1.pdf"), []byte("page"), 0600)
	_ = os.Chtimes(expiredDir, oldTime, oldTime)

	// 3. Create a recent active file (mtime = now)
	activeFile := filepath.Join(tmpDir, "split-test-active.pdf")
	_ = os.WriteFile(activeFile, []byte("active content"), 0600)
	defer os.Remove(activeFile)

	// Run janitor sweep with TTL = 1 hour
	sweepExpiredTempFiles(1 * time.Hour)

	// Assertions
	if _, err := os.Stat(expiredFile); !os.IsNotExist(err) {
		t.Errorf("Expected expired file %s to be deleted by janitor", expiredFile)
	}

	if _, err := os.Stat(expiredDir); !os.IsNotExist(err) {
		t.Errorf("Expected expired directory %s to be deleted by janitor", expiredDir)
	}

	if _, err := os.Stat(activeFile); os.IsNotExist(err) {
		t.Errorf("Expected active file %s to be preserved by janitor", activeFile)
	}
}

func TestJanitor_SymlinkSafety(t *testing.T) {
	tmpDir := temp.GetDir()

	// Create a target file outside with a non-PDFNest name
	targetFile := filepath.Join(os.TempDir(), "important-user-document.txt")
	_ = os.WriteFile(targetFile, []byte("important target"), 0600)
	defer os.Remove(targetFile)

	// Create a symlink inside dedicated directory pointing to targetFile
	symlinkPath := filepath.Join(tmpDir, "pdfnest-symlink.txt")
	_ = os.Symlink(targetFile, symlinkPath)
	defer os.Remove(symlinkPath)

	oldTime := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(symlinkPath, oldTime, oldTime)

	sweepExpiredTempFiles(1 * time.Hour)

	// Verify symlink was removed, but target file remains untouched
	if _, err := os.Stat(targetFile); os.IsNotExist(err) {
		t.Errorf("CRITICAL SECURITY DEFECT: Janitor followed symlink and deleted target file %s", targetFile)
	}
}
