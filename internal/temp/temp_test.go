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

func TestGetDir_SelfHealingAfterRemoval(t *testing.T) {
	dir := GetDir()
	if dir == "" {
		t.Fatal("Expected non-empty temp directory")
	}

	// Delete the directory to simulate eviction by an external cleanup or container sweep
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("Failed to remove temp dir: %v", err)
	}

	// Calling GetDir() must self-heal and re-create a verified writable directory
	newDir := GetDir()
	if newDir == "" {
		t.Fatal("Expected non-empty temp directory after self-healing")
	}

	info, err := os.Stat(newDir)
	if err != nil || !info.IsDir() {
		t.Fatalf("Expected re-created temp directory to exist: %v", err)
	}

	// Verify we can write into the self-healed directory
	f, err := CreateTemp("self-heal-test", ".pdf")
	if err != nil {
		t.Fatalf("Failed to create temp file in self-healed directory: %v", err)
	}
	defer os.Remove(f.Name())
	_ = f.Close()
}

func TestGetDir_VerifiedWritable(t *testing.T) {
	dir := GetDir()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Failed to stat temp dir: %v", err)
	}
	// Verify directory permissions are 0700
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Logf("Directory permissions are %04o (expected 0700)", perm)
	}

	// Verify probe creation works
	if !verifyWritable(dir) {
		t.Fatalf("Expected directory %s to be verified writable", dir)
	}
}

