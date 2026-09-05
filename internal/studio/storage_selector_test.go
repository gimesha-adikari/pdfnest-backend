package studio

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/storage"
)

func TestStudioPersistenceUsesLocalStorageWhenR2EnvironmentIsPresent(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("STORAGE_MODE", "local")
	localRoot := t.TempDir()
	t.Setenv("LOCAL_STORAGE_DIR", localRoot)
	// These deliberately unusable values make an accidental remote write fail
	// instead of silently reaching a developer's configured bucket.
	t.Setenv("R2_BUCKET", "stale-development-bucket")
	t.Setenv("R2_ACCESS_KEY", "stale-access-key")
	t.Setenv("R2_SECRET_KEY", "stale-secret-key")
	t.Setenv("R2_ENDPOINT", "127.0.0.1:1")

	ctx := context.Background()
	fixturePath := filepath.Join("..", "..", "..", "benchmarks", "rendering", "corpus", "standard_a4_10p.pdf")
	fixture, err := os.ReadFile(fixturePath)
	require.NoError(t, err)

	sourceKey := storage.BuildKey("studio/sources", ".pdf")
	require.NoError(t, persistStudioSource(ctx, fixturePath, sourceKey, "application/pdf"))
	requireFileAtStorageKey(t, localRoot, sourceKey, fixture)

	sourcePath, sourceCleanup, err := storage.ResolveObject(ctx, sourceKey, "studio-selector-source", ".pdf")
	require.NoError(t, err)
	defer sourceCleanup()
	resolvedSource, err := os.ReadFile(sourcePath)
	require.NoError(t, err)
	require.Equal(t, fixture, resolvedSource)

	pdfKey := storage.BuildKey("studio/materialized", ".pdf")
	require.NoError(t, persistStudioPDF(ctx, fixturePath, pdfKey))
	requireFileAtStorageKey(t, localRoot, pdfKey, fixture)

	payload := []byte(`{"operation":"editor_extract","document_id":"` + uuid.NewString() + `"}`)
	payloadKey, err := stageStudioJobBytes(ctx, payload, uuid.New(), "payload")
	require.NoError(t, err)
	requireFileAtStorageKey(t, localRoot, payloadKey, payload)

	cleanupStudioSource(ctx, sourceKey)
	cleanupStudioObject(ctx, pdfKey)
	cleanupStudioObject(ctx, payloadKey)
	for _, key := range []string{sourceKey, pdfKey, payloadKey} {
		_, statErr := os.Stat(filepath.Join(localRoot, filepath.FromSlash(key)))
		require.Truef(t, os.IsNotExist(statErr), "expected local cleanup for %s, got %v", key, statErr)
	}
}

func requireFileAtStorageKey(t *testing.T, localRoot, key string, want []byte) {
	t.Helper()
	path := filepath.Join(localRoot, filepath.FromSlash(key))
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.True(t, bytes.Equal(want, got), "stored bytes must match source bytes")
}
