//go:build linux

package storage

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrivateOCRLocalMutation(t *testing.T) {
	root := localArtifactEnvironment(t)
	key, source, digest, size := createFixture(t)
	ctx := context.Background()
	require.NoError(t, CreateArtifactIfAbsent(ctx, key, source, digest, size))
	original, err := os.ReadFile(filepath.Join(root, key))
	require.NoError(t, err)
	saved := SaveObjectBytes(ctx, key, []byte("synthetic replacement"), "application/pdf")
	deleted := DeleteLocalObject(key)
	assert.ErrorIs(t, saved, ErrPrivateOCRNamespace)
	assert.ErrorIs(t, deleted, ErrPrivateOCRNamespace)
	if saved == nil || deleted == nil {
		t.FailNow()
	}
	actual, err := os.ReadFile(filepath.Join(root, key))
	require.NoError(t, err)
	require.Equal(t, original, actual)
}

type unreadNamespaceSource struct{}

func (unreadNamespaceSource) Read([]byte) (int, error) { panic("reserved save must not consume input") }
func TestPrivateOCRMutationVariants(t *testing.T) {
	root := localArtifactEnvironment(t)
	key, source, digest, size := createFixture(t)
	ctx := context.Background()
	require.NoError(t, CreateArtifactIfAbsent(ctx, key, source, digest, size))
	original, err := os.ReadFile(filepath.Join(root, key))
	require.NoError(t, err)
	var requests atomic.Int32
	remote := fixtureArtifactRemote(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) })
	variants := []string{key, "ocr-finalization/outputs", "ocr-finalization/outputs/", "ocr-finalization/outputs/nested/object.pdf", "/" + key, "  " + key + " ", strings.ReplaceAll(key, "/", "\\"), "./" + key, "a/../" + key, "ocr-finalization//outputs/./file.pdf", "ocr-finalization/outputs/../outside.pdf", "./ocr-finalization/outputs/../../outside.pdf", "ocr-finalization/outputs/%2e%2e/file.pdf"}
	for _, key := range variants {
		for name, mutate := range map[string]func() error{
			"local_stream":  func() error { _, _, err := SaveLocalStream(ctx, key, unreadNamespaceSource{}); return err },
			"local_file":    func() error { return SaveLocalFile(ctx, key, source) },
			"object_file":   func() error { return SaveObjectFile(ctx, key, source, "application/pdf") },
			"object_bytes":  func() error { return SaveObjectBytes(ctx, key, []byte("synthetic"), "application/pdf") },
			"local_delete":  func() error { return DeleteLocalObject(key) },
			"object_delete": func() error { return DeleteObject(ctx, key) },
			"remote_file":   func() error { return remote.UploadFile(source, key, "application/pdf") },
			"remote_bytes":  func() error { return remote.UploadBytes([]byte("synthetic"), key, "application/pdf") },
			"remote_delete": func() error { return remote.DeleteObject(ctx, key) },
			"presign": func() error {
				url, err := remote.PresignPutObject(ctx, key, "application/pdf", time.Minute)
				require.Empty(t, url)
				return err
			},
		} {
			require.ErrorIs(t, mutate(), ErrPrivateOCRNamespace, "%s %s", name, key)
		}
	}
	// Bulk preflight rejects the complete batch before signing its first item.
	for _, request := range []PresignRequest{
		{Prefix: "ocr-finalization/outputs", Files: []PresignFile{{Name: "file.pdf", Size: 1}}},
		{Prefix: "ocr-finalization", Files: []PresignFile{{Key: "ocr-finalization/ordinary.pdf", Name: "ordinary.pdf", Size: 1}, {Key: key, Name: "private.pdf", Size: 1}}},
	} {
		response, err := remote.PresignUploads(ctx, request, time.Minute)
		require.ErrorIs(t, err, ErrPrivateOCRNamespace)
		require.Nil(t, response)
	}

	t.Run("remote_selector_wrappers", func(t *testing.T) {
		t.Setenv("STORAGE_MODE", "remote")
		require.ErrorIs(t, SaveObjectFile(ctx, key, source, "application/pdf"), ErrPrivateOCRNamespace)
		require.ErrorIs(t, SaveObjectBytes(ctx, key, []byte("synthetic"), "application/pdf"), ErrPrivateOCRNamespace)
		require.ErrorIs(t, DeleteObject(ctx, key), ErrPrivateOCRNamespace)
		require.ErrorIs(t, DeleteLocalObject(key), ErrPrivateOCRNamespace)
	})
	require.Zero(t, requests.Load(), "rejections must precede SDK network work")
	actual, err := os.ReadFile(filepath.Join(root, key))
	require.NoError(t, err)
	require.Equal(t, original, actual)
	stages, _ := filepath.Glob(filepath.Join(root, "ocr-finalization/outputs/*/.upload-*"))
	require.Empty(t, stages)
	snapshot, err := ReadArtifact(ctx, key, size)
	require.NoError(t, err)
	data, err := os.ReadFile(snapshot.Path)
	require.NoError(t, err)
	require.Equal(t, original, data)
	require.NoError(t, snapshot.Cleanup())
	requireCreateKind(t, CreateArtifactIfAbsent(ctx, key, source, digest, size), ArtifactCreateExists)
}
func TestPrivateOCRNormalStorageCompatibility(t *testing.T) {
	root := localArtifactEnvironment(t)
	ctx := context.Background()
	_, source, _, _ := createFixture(t)
	t.Setenv("FILE_ENCRYPTION_KEY", "")
	var requests atomic.Int32
	remote := fixtureArtifactRemote(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.Method {
		case "PUT":
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("ETag", "\"synthetic\"")
		case "DELETE":
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	})
	for _, key := range []string{"ocr-finalization/outputs-other/file.pdf", "ocr-finalization/outputs2/file.pdf", "ocr-finalization/Outputs/file.pdf", "other/ocr-finalization/outputs/file.pdf", "jobs/ordinary.pdf"} {
		require.NoError(t, rejectPrivateOCRMutation(key))
		require.NoError(t, SaveObjectFile(ctx, key, source, "application/pdf"))
		require.NoError(t, SaveObjectBytes(ctx, key, []byte("replacement"), "application/pdf"))
		data, err := os.ReadFile(filepath.Join(root, key))
		require.NoError(t, err)
		require.Equal(t, "replacement", string(data))
		require.NoError(t, DeleteObject(ctx, key))
		require.NoError(t, remote.UploadFile(source, key, "application/pdf"))
		require.NoError(t, remote.UploadBytes([]byte("replacement"), key, "application/pdf"))
		require.NoError(t, remote.DeleteObject(ctx, key))
		url, err := remote.PresignPutObject(ctx, key, "application/pdf", time.Minute)
		require.NoError(t, err)
		require.NotEmpty(t, url)
	}
	require.EqualValues(t, 15, requests.Load())
	batch, err := remote.PresignUploads(ctx, PresignRequest{Prefix: "ordinary", Files: []PresignFile{{Name: "file.pdf", Size: 1}}}, time.Minute)
	require.NoError(t, err)
	require.Len(t, batch.Files, 1)
}
