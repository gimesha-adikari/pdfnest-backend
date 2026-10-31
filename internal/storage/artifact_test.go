package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestArtifactSnapshotProtectsHashUploadFromSourceMutation(t *testing.T) {
	studioStorageLocal(t)
	source := filepath.Join(t.TempDir(), "generated.txt")
	require.NoError(t, os.WriteFile(source, []byte("original generated result"), 0600))
	path, digest, size, cleanup, err := StageArtifactSource(t.Context(), source)
	require.NoError(t, err)
	defer cleanup()
	require.NoError(t, os.WriteFile(source, []byte("mutated after fingerprint"), 0600))
	require.NoError(t, SaveVerifiedArtifact(t.Context(), "outputs/tasks/test/result.txt", path, digest, size, "text/plain"))
	require.NoError(t, VerifyArtifact(t.Context(), "outputs/tasks/test/result.txt", digest, size))
	data, err := os.ReadFile(filepath.Join(GetLocalStorageDir(), "outputs/tasks/test/result.txt"))
	require.NoError(t, err)
	require.Equal(t, "original generated result", string(data))
	require.NoError(t, os.WriteFile(path, []byte("changed snapshot"), 0600))
	require.ErrorIs(t, SaveVerifiedArtifact(t.Context(), "outputs/tasks/test/other.txt", path, digest, size, "text/plain"), ErrArtifactIntegrity)
}

func TestArtifactCanonicalStoreOnlyAndBounds(t *testing.T) {
	root := studioStorageLocal(t)
	key := "outputs/tasks/test/result.txt"
	data := []byte("generated result")
	digest := artifactDigest(data)
	for _, alias := range []string{"outputs//tasks/test/result.txt", "outputs/tasks/test/./result.txt", "outputs/tasks/test/../result.txt", " outputs/tasks/test/result.txt", `outputs/tasks/test\result.txt`} {
		require.ErrorIs(t, VerifyArtifact(t.Context(), alias, digest, int64(len(data))), ErrArtifactIntegrity)
	}
	t.Chdir(t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Dir(key), 0700))
	require.NoError(t, os.WriteFile(key, data, 0600))
	require.ErrorIs(t, VerifyArtifact(t.Context(), key, digest, int64(len(data))), ErrObjectNotFound, "cwd bytes cannot satisfy canonical ownership")
	require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.Dir(key)), 0700))
	abs, err := filepath.Abs(key)
	require.NoError(t, err)
	require.NoError(t, os.Symlink(abs, filepath.Join(root, key)))
	require.ErrorIs(t, VerifyArtifact(t.Context(), key, digest, int64(len(data))), ErrArtifactIntegrity)
	source := filepath.Join(root, "large-plaintext")
	f, err := os.Create(source)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(MaxBufferedArtifactBytes+1))
	require.NoError(t, f.Close())
	f, err = os.Open(source)
	require.NoError(t, err)
	h := sha256.New()
	_, err = io.Copy(h, f)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.NoError(t, VerifyArtifact(t.Context(), "large-plaintext", hex.EncodeToString(h.Sum(nil)), MaxBufferedArtifactBytes+1), "plaintext output ceiling must not be borrowed from per-input limits")
}

func TestArtifactRemotePlainAndEncryptedContentVerification(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		for _, scenario := range []string{"valid", "legacy_text", "altered", "truncated", "excess", "empty", "missing", "denied"} {
			t.Run(fmt.Sprintf("encrypted=%v/%s", encrypted, scenario), func(t *testing.T) {
				t.Setenv("FILE_ENCRYPTION_KEY", "")
				if encrypted {
					t.Setenv("FILE_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
				}
				plain := []byte("# Known Markdown result\n")
				payload := append([]byte(nil), plain...)
				if encrypted && scenario != "legacy_text" {
					var err error
					payload, err = encryptData(payload)
					require.NoError(t, err)
				}
				switch scenario {
				case "altered":
					payload[len(payload)-1] ^= 1
				case "truncated":
					payload = payload[:len(payload)-1]
				case "excess":
					payload = append(payload, []byte(strings.Repeat("x", 4096))...)
				case "empty":
					payload = nil
				}
				studioRemoteStore(t, func(w http.ResponseWriter, r *http.Request) {
					if scenario == "missing" || scenario == "denied" {
						code, status := "NoSuchKey", 404
						if scenario == "denied" {
							code, status = "AccessDenied", 403
						}
						w.Header().Set("Content-Type", "application/xml")
						w.WriteHeader(status)
						fmt.Fprintf(w, "<Error><Code>%s</Code><Message>synthetic</Message></Error>", code)
						return
					}
					w.Header().Set("ETag", `"unrelated-and-unchanged"`)
					w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
					w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
					w.Write(payload)
				})
				err := VerifyArtifact(t.Context(), "outputs/tasks/test/result.md", artifactDigest(plain), int64(len(plain)))
				switch scenario {
				case "valid", "legacy_text":
					require.NoError(t, err)
				case "missing":
					require.ErrorIs(t, err, ErrObjectNotFound)
				case "denied":
					require.Error(t, err)
					require.NotErrorIs(t, err, ErrObjectNotFound)
				default:
					require.ErrorIs(t, err, ErrArtifactIntegrity)
				}
				require.Zero(t, len(artifactBuffers))
			})
		}
	}
}

func TestArtifactRemoteReadDeadline(t *testing.T) {
	studioRemoteStore(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	require.Error(t, VerifyArtifact(ctx, "outputs/tasks/test/result.txt", artifactDigest([]byte("x")), 1))
	require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
}

func TestArtifactEncryptedCapacityIsExplicitAndDoesNotAllocateOrRead(t *testing.T) {
	t.Setenv("FILE_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	studioRemoteStore(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("over-budget encrypted artifact must not start a body read")
		w.WriteHeader(403)
	})
	require.ErrorIs(t, VerifyArtifact(t.Context(), "outputs/tasks/test/result.pdf", strings.Repeat("0", 64), MaxBufferedArtifactBytes+1), ErrArtifactCapacity)
	require.Zero(t, len(artifactBuffers))
}
