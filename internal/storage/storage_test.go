package storage

import (
	"context"
	"os"
	"path/filepath"
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

func TestRemoteStorageSelectionIsExplicitInDevelopment(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("R2_BUCKET", "stale-development-bucket")
	t.Setenv("STORAGE_MODE", "")
	if RemoteStorageEnabled() {
		t.Fatal("expected development storage to remain local without explicit STORAGE_MODE")
	}

	t.Setenv("STORAGE_MODE", "r2")
	if !RemoteStorageEnabled() {
		t.Fatal("expected explicit development STORAGE_MODE=r2 to enable remote storage")
	}
}

func TestRemoteStorageRemainsRequiredInManagedEnvironments(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("STORAGE_MODE", "local")
	if !RemoteStorageEnabled() {
		t.Fatal("expected managed environments to require remote storage")
	}
}

func TestDefaultLocalStorageMatchesWorkerContract(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("STORAGE_MODE", "local")
	localRoot := t.TempDir()
	t.Setenv("LOCAL_STORAGE_DIR", localRoot)
	t.Setenv("ANALYZER_STORAGE_DIR", t.TempDir())
	if got := GetLocalStorageDir(); got != localRoot {
		t.Fatalf("expected shared local storage root %q to win over analyzer override, got %q", localRoot, got)
	}

	t.Setenv("LOCAL_STORAGE_DIR", "")
	t.Setenv("ANALYZER_STORAGE_DIR", "")
	if got, want := GetLocalStorageDir(), filepath.Join(os.TempDir(), "pdfnest-storage"); got != want {
		t.Fatalf("expected shared default root %q, got %q", want, got)
	}
}
