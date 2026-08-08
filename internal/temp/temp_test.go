package temp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetDir_CreatesDedicatedTempDir(t *testing.T) {
	dir := GetDir()
	if dir == "" {
		t.Fatal("Expected non-empty temp directory path")
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Failed to stat temp directory: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("Expected %s to be a directory", dir)
	}
}

func TestCreateTemp_CreatesFileInDedicatedDir(t *testing.T) {
	f, err := CreateTemp("test-file", ".pdf")
	if err != nil {
		t.Fatalf("CreateTemp failed: %v", err)
	}
	defer os.Remove(f.Name())
	_ = f.Close()

	expectedParent := GetDir()
	actualParent := filepath.Dir(f.Name())

	if actualParent != expectedParent {
		t.Errorf("Expected file parent %s, got %s", expectedParent, actualParent)
	}
}

func TestMkdirTemp_CreatesSubdirInDedicatedDir(t *testing.T) {
	subDir, err := MkdirTemp("test-subdir")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(subDir)

	expectedParent := GetDir()
	actualParent := filepath.Dir(subDir)

	if actualParent != expectedParent {
		t.Errorf("Expected subdir parent %s, got %s", expectedParent, actualParent)
	}
}
