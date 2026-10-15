package ocr

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pdfnest-backend/internal/storage"

	"github.com/google/uuid"
	"github.com/jung-kurt/gofpdf"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func artifactEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_STORAGE_DIR", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("MAX_PAGES_OCR", "150")
}
func artifactPDF(t *testing.T, marker string, pages int) string {
	t.Helper()
	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetCompression(false)
	for i := 0; i < pages; i++ {
		pdf.AddPage()
		pdf.SetFont("Helvetica", "", 12)
		pdf.Cell(100, 10, fmt.Sprintf("%s page %d", marker, i+1))
	}
	path := filepath.Join(t.TempDir(), "synthetic.pdf")
	require.NoError(t, pdf.OutputFileAndClose(path))
	return path
}
func artifactFacts(t *testing.T, path string, pages int) artifactDescriptor {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	digest := sha256.Sum256(data)
	return artifactDescriptor{SHA256: hex.EncodeToString(digest[:]), ByteSize: int64(len(data)), PageCount: pages, ContentType: "application/pdf", ValidationVersion: 1}
}
func evidenceFixture(t *testing.T, path string, pages int) (artifactVerificationIntent, artifactVerifier) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	task, owner, claim := uuid.NewString(), uuid.NewString(), strings.Repeat("c", 64)
	facts, err := newArtifactIntent(task, owner, claim, artifactFacts(t, path, pages))
	require.NoError(t, err)
	req := finalizationRequest{TaskID: task, OwnerID: owner, ReservationID: uuid.NewString(), ExpectedState: "processing", ExpectedRevision: 3, ProcessingClaim: claim, Artifact: facts, Now: now.Add(3 * time.Second)}
	intent := artifactVerificationIntent{request: req, createdAt: now, intentAt: now.Add(2 * time.Second), leaseUntil: now.Add(time.Minute)}
	verifier, err := newArtifactVerifier(8*1024*1024, 5*time.Second)
	require.NoError(t, err)
	verifier.now = func() time.Time { return req.Now }
	return intent, verifier
}
func requireStorageKind(t *testing.T, err error, kind storage.ArtifactReadKind) {
	t.Helper()
	var typed *storage.ArtifactReadError
	require.ErrorAs(t, err, &typed)
	require.Equal(t, kind, typed.Kind)
}
func requireNoVerificationTemps(t *testing.T) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "ocr-artifact-read-*"))
	require.NoError(t, err)
	require.Empty(t, files)
}

type faultArtifactStore struct {
	persistHook func(context.Context, string, string) error
	readHook    func(context.Context, string, int64) (*storage.ArtifactFile, error)
}

func (s faultArtifactStore) persist(ctx context.Context, key, path, digest string, size int64) error {
	if s.persistHook != nil {
		return s.persistHook(ctx, key, path)
	}
	return (selectedArtifactStore{}).persist(ctx, key, path, digest, size)
}
func (s faultArtifactStore) read(ctx context.Context, key string, limit int64) (*storage.ArtifactFile, error) {
	if s.readHook != nil {
		return s.readHook(ctx, key, limit)
	}
	return (selectedArtifactStore{}).read(ctx, key, limit)
}

func TestArtifactEvidenceLocalPDF(t *testing.T) {
	artifactEnvironment(t)
	path := artifactPDF(t, "S211_REAL_LOCAL", 2)
	intent, v := evidenceFixture(t, path, 2)
	require.NoError(t, v.persist(context.Background(), intent, path))
	first, err := v.verify(context.Background(), intent)
	require.NoError(t, err)
	require.True(t, first.verified)
	require.True(t, sameIntent(first.descriptor, intent.request.Artifact))
	require.Equal(t, 2, first.descriptor.PageCount)
	second, err := v.verify(context.Background(), intent)
	require.NoError(t, err)
	require.True(t, sameArtifact(first.descriptor, second.descriptor))
	require.True(t, intent.request.Artifact.VerifiedAt.IsZero(), "readback cannot mutate captured intent")
	var group sync.WaitGroup
	results := make(chan error, 4)
	for i := 0; i < 4; i++ {
		group.Add(1)
		go func() { defer group.Done(); _, err := v.verify(context.Background(), intent); results <- err }()
	}
	group.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	requireNoVerificationTemps(t)
	// Independent inspection uses the exact persisted object, not only the source.
	persistent := filepath.Join(storage.GetLocalStorageDir(), filepath.FromSlash(intent.request.Artifact.ArtifactKey))
	for _, command := range []struct {
		name     string
		args     []string
		contains string
	}{
		{"qpdf", []string{"--check", persistent}, "No syntax or stream encoding errors found"},
		{"pdfinfo", []string{persistent}, "Pages:           2"},
		{"pdftotext", []string{persistent, "-"}, "S211_REAL_LOCAL"},
	} {
		if _, err := exec.LookPath(command.name); err != nil {
			t.Fatalf("independent artifact inspector unavailable: %s", command.name)
		}
		data, err := exec.Command(command.name, command.args...).CombinedOutput()
		require.NoError(t, err, string(data))
		require.Contains(t, string(data), command.contains)
	}
}
func TestArtifactEvidenceInvalidBytes(t *testing.T) {
	for _, scenario := range []string{"digest", "size", "page_count", "empty", "html", "binary", "malformed", "corrupt_stream", "truncated", "encrypted", "empty_password_encrypted", "missing", "read_failure", "cleanup_failure", "expired_claim", "page_limit", "byte_limit"} {
		t.Run(scenario, func(t *testing.T) {
			artifactEnvironment(t)
			path := artifactPDF(t, "S211_INVALID_CASE", 1)
			switch scenario {
			case "empty":
				require.NoError(t, os.WriteFile(path, nil, 0600))
			case "html":
				require.NoError(t, os.WriteFile(path, []byte("<html>synthetic HTTP 200 error</html>"), 0600))
			case "binary":
				require.NoError(t, os.WriteFile(path, []byte{1, 2, 3, 4, 5}, 0600))
			case "malformed":
				require.NoError(t, os.WriteFile(path, []byte("%PDF-1.7\nnot a structural PDF"), 0600))
			case "corrupt_stream":
				compressed := gofpdf.New("P", "mm", "A4", "")
				compressed.SetCompression(true)
				compressed.AddPage()
				compressed.SetFont("Helvetica", "", 12)
				compressed.Cell(100, 10, "S211_CORRUPT_STREAM")
				require.NoError(t, compressed.OutputFileAndClose(path))
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				start := bytes.Index(data, []byte("stream\n"))
				require.Greater(t, start, 0)
				data[start+len("stream\n")] ^= 0xff
				require.NoError(t, os.WriteFile(path, data, 0600))
			case "truncated":
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, data[:len(data)/2], 0600))
			case "encrypted", "empty_password_encrypted":
				encrypted := filepath.Join(t.TempDir(), "encrypted.pdf")
				password := "synthetic-input"
				if scenario == "empty_password_encrypted" {
					password = ""
				}
				data, err := exec.Command("qpdf", "--encrypt", password, "synthetic-owner", "128", "--use-aes=y", "--", path, encrypted).CombinedOutput()
				require.NoError(t, err, string(data))
				path = encrypted
			}
			// Empty bytes have an intentionally false positive-size intent so they reach
			// independent readback instead of being rejected solely at metadata creation.
			facts := artifactFacts(t, path, 1)
			if facts.ByteSize == 0 {
				facts.ByteSize = 1
			}
			validPath := artifactPDF(t, "S211_INTENT", 1)
			intent, v := evidenceFixture(t, validPath, 1)
			facts.ArtifactID, facts.ArtifactKey = intent.request.Artifact.ArtifactID, intent.request.Artifact.ArtifactKey
			intent.request.Artifact = facts
			switch scenario {
			case "digest":
				intent.request.Artifact.SHA256 = strings.Repeat("0", 64)
			case "size":
				intent.request.Artifact.ByteSize++
			case "page_count":
				intent.request.Artifact.PageCount = 2
			case "expired_claim":
				v.now = func() time.Time { return intent.leaseUntil }
			case "page_limit":
				intent.request.Artifact.PageCount = 2
				v.maxPages = 1
			case "byte_limit":
				v.maxBytes = 10
			}
			if scenario != "missing" {
				seedPrivilegedArtifact(t, intent.request.Artifact.ArtifactKey, path)
			}
			if scenario == "read_failure" {
				v.store = faultArtifactStore{readHook: func(context.Context, string, int64) (*storage.ArtifactFile, error) {
					return nil, errors.New("synthetic transport failure with private internal details")
				}}
			}
			if scenario == "cleanup_failure" {
				v.store = faultArtifactStore{readHook: func(ctx context.Context, key string, limit int64) (*storage.ArtifactFile, error) {
					snapshot, err := storage.ReadArtifact(ctx, key, limit)
					if err != nil {
						return nil, err
					}
					parent := filepath.Dir(snapshot.Path)
					require.NoError(t, os.Chmod(parent, 0500))
					t.Cleanup(func() { _ = os.Chmod(parent, 0700); _ = snapshot.Cleanup() })
					return snapshot, nil
				}}
			}
			proof, err := v.verify(context.Background(), intent)
			require.Error(t, err)
			require.False(t, proof.verified)
			if scenario == "missing" {
				requireStorageKind(t, err, storage.ArtifactMissing)
			}
			if scenario == "read_failure" {
				requireStorageKind(t, err, storage.ArtifactUnavailable)
				require.NotContains(t, err.Error(), "private internal details")
			}
			if scenario == "cleanup_failure" {
				require.ErrorIs(t, err, errArtifactTemporary)
				leftovers, globErr := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "ocr-artifact-read-*"))
				require.NoError(t, globErr)
				require.Len(t, leftovers, 1)
				require.NoError(t, os.Chmod(os.Getenv("TMPDIR"), 0700))
				require.NoError(t, os.Remove(leftovers[0]))
			}
			requireNoVerificationTemps(t)
		})
	}
}
func TestArtifactEvidenceKeyPolicy(t *testing.T) {
	artifactEnvironment(t)
	path := artifactPDF(t, "S211_KEY", 1)
	a, v := evidenceFixture(t, path, 1)
	b, _ := evidenceFixture(t, path, 1)
	require.NotEqual(t, a.request.Artifact.ArtifactKey, b.request.Artifact.ArtifactKey)
	for _, scenario := range []string{"other_task", "other_owner", "other_claim", "old_attempt_key", "absolute", "traversal", "source_prefix", "noncanonical", "client_key"} {
		t.Run(scenario, func(t *testing.T) {
			candidate := a
			switch scenario {
			case "other_task":
				candidate.request.TaskID = b.request.TaskID
			case "other_owner":
				candidate.request.OwnerID = b.request.OwnerID
			case "other_claim":
				candidate.request.ProcessingClaim = strings.Repeat("d", 64)
			case "old_attempt_key":
				candidate.request.Artifact = b.request.Artifact
			case "absolute":
				candidate.request.Artifact.ArtifactKey = path
			case "traversal":
				candidate.request.Artifact.ArtifactKey = "ocr-finalization/outputs/../source.pdf"
			case "source_prefix":
				candidate.request.Artifact.ArtifactKey = "owned/source/image_to_text_pdf/input.pdf"
			case "noncanonical":
				candidate.request.Artifact.ArtifactKey = " " + a.request.Artifact.ArtifactKey
			case "client_key":
				facts := artifactFacts(t, path, 1)
				facts.ArtifactKey = "client-chosen.pdf"
				_, err := newArtifactIntent(a.request.TaskID, a.request.OwnerID, a.request.ProcessingClaim, facts)
				require.Error(t, err)
				return
			}
			_, err := v.verify(context.Background(), candidate)
			require.Error(t, err)
			require.Error(t, v.persist(context.Background(), candidate, path))
		})
	}
	newer, err := newArtifactIntent(a.request.TaskID, a.request.OwnerID, a.request.ProcessingClaim, artifactFacts(t, path, 1))
	require.NoError(t, err)
	require.NotEqual(t, a.request.Artifact.ArtifactKey, newer.ArtifactKey)
}
func TestArtifactEvidenceUncertainWritesAndReplacement(t *testing.T) {
	for _, applied := range []bool{false, true} {
		t.Run(fmt.Sprintf("write_applied_%t", applied), func(t *testing.T) {
			artifactEnvironment(t)
			path := artifactPDF(t, "S211_ACK", 1)
			intent, v := evidenceFixture(t, path, 1)
			writes := 0
			v.store = faultArtifactStore{persistHook: func(ctx context.Context, key, path string) error {
				writes++
				if applied {
					if err := storage.CreateArtifactIfAbsent(ctx, key, path, intent.request.Artifact.SHA256, intent.request.Artifact.ByteSize); err != nil {
						return err
					}
				}
				return errors.New("controlled lost write acknowledgement")
			}}
			require.ErrorIs(t, v.persist(context.Background(), intent, path), errArtifactWriteUncertain)
			require.Equal(t, 1, writes)
			proof, err := v.verify(context.Background(), intent)
			if applied {
				require.NoError(t, err)
				require.True(t, proof.verified)
			} else {
				requireStorageKind(t, err, storage.ArtifactMissing)
			}
			// No cleanup/delete/retry-write follows an ambiguous PUT.
			require.Equal(t, 1, writes)
			requireNoVerificationTemps(t)
		})
	}
	t.Run("replacement_between_reads", func(t *testing.T) {
		artifactEnvironment(t)
		original := artifactPDF(t, "S211_FIRST", 1)
		replacement := artifactPDF(t, "S211_SECOND", 1)
		intent, v := evidenceFixture(t, original, 1)
		require.NoError(t, v.persist(context.Background(), intent, original))
		first, err := v.verify(context.Background(), intent)
		require.NoError(t, err)
		seedPrivilegedArtifact(t, intent.request.Artifact.ArtifactKey, replacement)
		second, err := v.verify(context.Background(), intent)
		require.Error(t, err)
		require.False(t, second.verified)
		require.True(t, first.verified, "earlier evidence is point-in-time, not physical immutability")
		requireNoVerificationTemps(t)
	})
}

func capturedSQLArtifact(t *testing.T, db *gorm.DB, path string) (ocrFinalization, artifactVerificationIntent, artifactVerifier) {
	t.Helper()
	job, _, _ := foundationAccepted(t, db, 0)
	claimFoundation(t, db, &job)
	facts, err := newArtifactIntent(job.TaskID, job.OwnerID, job.ProcessingClaim, artifactFacts(t, path, 1))
	require.NoError(t, err)
	req := foundationRequest(job)
	req.Artifact = facts
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return (finalizationRepository{}).recordIntentInTx(tx, req) }))
	reloadFoundation(t, db, &job)
	req = foundationRequest(job)
	var intent artifactVerificationIntent
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var err error
		intent, err = (finalizationRepository{}).captureArtifactInTx(tx, req)
		return err
	}))
	v, err := newArtifactVerifier(8*1024*1024, 5*time.Second)
	require.NoError(t, err)
	v.now = func() time.Time { return req.Now }
	return job, intent, v
}
func TestArtifactEvidenceSQLReadiness(t *testing.T) {
	for _, scenario := range []string{"success", "metadata_without_evidence", "cancel_before_readback", "cancel_after_readback", "lease_expires_during_readback", "conflicting_descriptor", "wrong_task_evidence", "ready_update_rollback"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("BILLING_MODE", "normal")
			artifactEnvironment(t)
			db, observer := foundationDB(t)
			path := artifactPDF(t, "S211_SQL", 1)
			job, intent, v := capturedSQLArtifact(t, db, path)
			req := intent.request
			r := finalizationRepository{}
			require.NoError(t, v.persist(context.Background(), intent, path))
			cancelled := false
			cancel := func() {
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return endFoundation(tx, req, "cancelled", "USER_CANCELLED") }))
				cancelled = true
			}
			if scenario == "cancel_before_readback" {
				cancel()
			}
			if scenario == "lease_expires_during_readback" {
				clock := req.Now
				v.now = func() time.Time { return clock }
				v.store = faultArtifactStore{readHook: func(ctx context.Context, key string, limit int64) (*storage.ArtifactFile, error) {
					snapshot, err := storage.ReadArtifact(ctx, key, limit)
					clock = intent.leaseUntil
					return snapshot, err
				}}
			}
			proof, err := v.verify(context.Background(), intent)
			if scenario == "lease_expires_during_readback" {
				require.Error(t, err)
				require.False(t, proof.verified)
				assertFoundation(t, observer, job, "processing", "reserved", 0, 50, 0, 3)
				return
			}
			require.NoError(t, err)
			req.Artifact = proof.descriptor
			if scenario == "cancel_after_readback" {
				cancel()
			}
			if scenario == "metadata_without_evidence" {
				proof = artifactEvidence{}
			}
			if scenario == "conflicting_descriptor" {
				req.Artifact.SHA256 = strings.Repeat("0", 64)
			}
			if scenario == "wrong_task_evidence" {
				other, otherIntent, _ := capturedSQLArtifact(t, db, path)
				req = otherIntent.request
				req.Artifact.VerifiedAt = proof.descriptor.VerifiedAt
				err := db.Transaction(func(tx *gorm.DB) error { return r.readyInTx(tx, req, proof) })
				require.Error(t, err)
				assertFoundation(t, observer, other, "processing", "reserved", 0, 50, 0, 3)
				assertFoundation(t, observer, job, "processing", "reserved", 0, 50, 0, 3)
				return
			}
			err = db.Transaction(func(tx *gorm.DB) error {
				if scenario == "ready_update_rollback" {
					if err := installDecisionFailure(tx, "ready", "reserved"); err != nil {
						return err
					}
				}
				return r.readyInTx(tx, req, proof)
			})
			if scenario == "success" {
				require.NoError(t, err)
				reloadFoundation(t, observer, &job)
				assertFoundation(t, observer, job, "ready", "reserved", 0, 50, 0, 4)
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, foundationRequest(job)) }))
				assertFoundation(t, observer, job, "succeeded", "committed", 7, 50, 1, 5)
			} else {
				require.Error(t, err)
				if cancelled {
					assertFoundation(t, observer, job, "cancelled", "released", 0, 50, 0, 4)
				} else {
					assertFoundation(t, observer, job, "processing", "reserved", 0, 50, 0, 3)
				}
			}
			requireNoVerificationTemps(t)
		})
	}
}

func TestArtifactEvidenceReadBoundaryFailures(t *testing.T) {
	for _, scenario := range []string{"cancelled", "missing_snapshot", "inspector_missing", "inspector_timeout", "invalid_budget"} {
		t.Run(scenario, func(t *testing.T) {
			artifactEnvironment(t)
			path := artifactPDF(t, "S211_CONTEXT", 1)
			intent, v := evidenceFixture(t, path, 1)
			require.NoError(t, v.persist(context.Background(), intent, path))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "cancelled":
				cancel()
			case "missing_snapshot":
				v.store = faultArtifactStore{readHook: func(context.Context, string, int64) (*storage.ArtifactFile, error) {
					return &storage.ArtifactFile{Path: "missing-test-snapshot", Cleanup: func() error { return nil }}, nil
				}}
			case "inspector_missing":
				t.Setenv("PATH", t.TempDir())
			case "inspector_timeout":
				v.inspectionBudget = time.Nanosecond
			case "invalid_budget":
				v.inspectionBudget = 0
			}
			proof, err := v.verify(ctx, intent)
			require.Error(t, err)
			require.False(t, proof.verified)
			if strings.HasPrefix(scenario, "inspector_") || scenario == "invalid_budget" {
				require.ErrorIs(t, err, errArtifactInspectionUnavailable)
			}
			if scenario == "missing_snapshot" {
				require.ErrorIs(t, err, errArtifactTemporary)
			}
			requireNoVerificationTemps(t)
		})
	}
	_, err := newArtifactVerifier(0, 5*time.Second)
	require.Error(t, err)
	_, err = newArtifactVerifier(251*1024*1024, 5*time.Second)
	require.Error(t, err)
	_, err = newArtifactVerifier(1024, 0)
	require.Error(t, err)
}

// A large logical PDF stream uses actual valid PDF syntax with bounded comments.
func TestArtifactEvidenceLargeAllowedPDF(t *testing.T) {
	artifactEnvironment(t)
	stream := "BT /F1 12 Tf 50 700 Td (S211_LARGE) Tj ET\n" + strings.Repeat("% bounded padding\n", 65536)
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>", fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream)}
	var output bytes.Buffer
	output.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, obj := range objects {
		offsets = append(offsets, output.Len())
		fmt.Fprintf(&output, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := output.Len()
	fmt.Fprintf(&output, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&output, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&output, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	path := filepath.Join(t.TempDir(), "large.pdf")
	require.NoError(t, os.WriteFile(path, output.Bytes(), 0600))
	intent, v := evidenceFixture(t, path, 1)
	require.Greater(t, intent.request.Artifact.ByteSize, int64(1024*1024))
	require.NoError(t, v.persist(context.Background(), intent, path))
	_, err := v.verify(context.Background(), intent)
	require.NoError(t, err)
	requireNoVerificationTemps(t)
}

// Test-only privileged filesystem mutation models corruption/replacement outside
// the storage APIs. Generic writers now correctly reject this private namespace.
func seedPrivilegedArtifact(t *testing.T, key, source string) {
	t.Helper()
	root := os.Getenv("LOCAL_STORAGE_DIR")
	require.NotEmpty(t, root)
	require.True(t, strings.HasPrefix(key, "ocr-finalization/outputs/"))
	require.Equal(t, key, filepath.ToSlash(filepath.Clean(key)))
	data, err := os.ReadFile(source)
	require.NoError(t, err)
	target := filepath.Join(root, filepath.FromSlash(key))
	require.NoError(t, os.MkdirAll(filepath.Dir(target), 0700))
	require.NoError(t, os.WriteFile(target, data, 0600))
}
