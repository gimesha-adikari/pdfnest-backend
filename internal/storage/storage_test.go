package storage

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestSaveLocalStreamRejectsManagedEnvironment(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("LOCAL_STORAGE_DIR", t.TempDir())

	_, _, err := SaveLocalStream(context.Background(), "jobs/test.txt", strings.NewReader("test"))
	if err == nil || !strings.Contains(err.Error(), "disabled in managed environments") {
		t.Fatalf("expected managed local-storage guard, got %v", err)
	}
}

func TestSaveLocalFileUsesDevelopmentLocalNamespace(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("ANALYZER_STORAGE_DIR", "")
	t.Setenv("LOCAL_STORAGE_DIR", t.TempDir())

	sourcePath := t.TempDir() + "/source.txt"
	if err := os.WriteFile(sourcePath, []byte("test"), 0600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := SaveLocalFile(context.Background(), "jobs/test.txt", sourcePath); err != nil {
		t.Fatalf("save local file: %v", err)
	}
	if !ObjectExists(context.Background(), "jobs/test.txt") {
		t.Fatal("expected saved development object to exist")
	}
}
