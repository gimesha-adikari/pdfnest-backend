package ocr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"pdfnest-backend/internal/storage"
)

func TestArtifactExclusiveIntentBinding(t *testing.T) {
	artifactEnvironment(t)
	path := artifactPDF(t, "S212_BOUND_INTENT", 2)
	intent, v := evidenceFixture(t, path, 2)
	for _, field := range []string{"task", "owner", "claim", "key", "artifact", "digest", "size", "expired"} {
		t.Run(field, func(t *testing.T) {
			candidate := intent
			switch field {
			case "task":
				candidate.request.TaskID = "another-task"
			case "owner":
				candidate.request.OwnerID = "another-owner"
			case "claim":
				candidate.request.ProcessingClaim = strings.Repeat("d", 64)
			case "key":
				candidate.request.Artifact.ArtifactKey = "/tmp/unsafe.pdf"
			case "artifact":
				candidate.request.Artifact.ArtifactID = "another-artifact"
			case "digest":
				candidate.request.Artifact.SHA256 = strings.Repeat("0", 64)
			case "size":
				candidate.request.Artifact.ByteSize++
			case "expired":
				candidate.leaseUntil = candidate.request.Now
			}
			require.Error(t, v.persist(context.Background(), candidate, path))
		})
	}
	require.NoError(t, v.persist(context.Background(), intent, path))
	var typed *storage.ArtifactCreateError
	require.ErrorAs(t, v.persist(context.Background(), intent, path), &typed)
	require.Equal(t, storage.ArtifactCreateExists, typed.Kind)
	proof, err := v.verify(context.Background(), intent)
	require.NoError(t, err)
	require.True(t, proof.verified)
	require.Equal(t, 2, proof.descriptor.PageCount)
	requireNoVerificationTemps(t)
}
func TestArtifactExclusiveLateSQLWrite(t *testing.T) {
	for _, scenario := range []string{"cancel_after_start", "lease_expires", "lost_ack", "lost_ack_read_unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("BILLING_MODE", "normal")
			artifactEnvironment(t)
			db, observer := foundationDB(t)
			path := artifactPDF(t, "S212_LATE_WRITE", 1)
			job, intent, v := capturedSQLArtifact(t, db, path)
			req := intent.request
			started, resume := make(chan struct{}), make(chan struct{})
			result := make(chan error, 1)
			facts := req.Artifact
			v.store = faultArtifactStore{persistHook: func(ctx context.Context, key, path string) error {
				close(started)
				<-resume
				err := storage.CreateArtifactIfAbsent(ctx, key, path, facts.SHA256, facts.ByteSize)
				if err == nil && strings.HasPrefix(scenario, "lost_ack") {
					return errors.New("synthetic lost acknowledgement")
				}
				return err
			}}
			go func() { result <- v.persist(context.Background(), intent, path) }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("write barrier not reached")
			}
			if scenario == "cancel_after_start" {
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return endFoundation(tx, req, "cancelled", "USER_CANCELLED") }))
			}
			// Advance the verifier clock while the upload is held at the storage
			// boundary, rather than only after the write has returned.
			if scenario == "lease_expires" {
				v.now = func() time.Time { return intent.leaseUntil }
			}
			close(resume)
			err := <-result
			if strings.HasPrefix(scenario, "lost_ack") {
				require.ErrorIs(t, err, errArtifactWriteUncertain)
			} else {
				require.NoError(t, err)
			}
			persistent := filepath.Join(storage.GetLocalStorageDir(), facts.ArtifactKey)
			_, err = os.Stat(persistent)
			require.NoError(t, err, "late/ambiguous output is retained, not deleted")
			if scenario == "lost_ack_read_unavailable" {
				v.store = faultArtifactStore{readHook: func(context.Context, string, int64) (*storage.ArtifactFile, error) {
					return nil, &storage.ArtifactReadError{Kind: storage.ArtifactUnavailable}
				}}
			}
			proof, err := v.verify(context.Background(), intent)
			if scenario == "lease_expires" || scenario == "lost_ack_read_unavailable" {
				require.Error(t, err)
				require.False(t, proof.verified)
				assertFoundation(t, observer, job, "processing", "reserved", 0, 50, 0, 3)
			} else {
				require.NoError(t, err)
				req.Artifact = proof.descriptor
				err = db.Transaction(func(tx *gorm.DB) error { return (finalizationRepository{}).readyInTx(tx, req, proof) })
				if scenario == "cancel_after_start" {
					require.Error(t, err)
					assertFoundation(t, observer, job, "cancelled", "released", 0, 50, 0, 4)
				} else {
					require.NoError(t, err)
					reloadFoundation(t, observer, &job)
					assertFoundation(t, observer, job, "ready", "reserved", 0, 50, 0, 4)
				}
			}
			_, err = os.Stat(persistent)
			require.NoError(t, err)
			requireNoVerificationTemps(t)
		})
	}
}
