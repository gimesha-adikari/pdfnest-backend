package temp

import (
	"os"
	"path/filepath"
	"sync"
)

var (
	tempDirOnce sync.Once
	tempDir     string
)

// GetDir returns the dedicated PDFNest temporary directory path (/tmp/pdfnest-temp).
// It creates the directory with 0700 permissions on first access if it does not exist.
func GetDir() string {
	tempDirOnce.Do(func() {
		dir := filepath.Join(os.TempDir(), "pdfnest-temp")
		if err := os.MkdirAll(dir, 0700); err != nil {
			tempDir = os.TempDir()
		} else {
			tempDir = dir
		}
	})
	return tempDir
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
