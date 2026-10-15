package ocr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"time"

	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/uploads"

	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"gorm.io/gorm"
)

var (
	errArtifactEvidence              = errors.New("OCR artifact evidence rejected")
	errArtifactWriteUncertain        = errors.New("OCR artifact write outcome uncertain; retain intent")
	errArtifactInspectionUnavailable = errors.New("OCR artifact inspection unavailable or timed out")
	errArtifactTemporary             = errors.New("OCR artifact verification temporary file failure")
)

// Only a trusted processor chooses logical facts. This factory supplies the
// unpredictable ID/destination; callers cannot provide a source or output path.
func newArtifactIntent(task, owner, claim string, facts artifactDescriptor) (artifactDescriptor, error) {
	if task == "" || owner == "" || len(claim) != 64 || facts.ArtifactID != "" || facts.ArtifactKey != "" || !facts.VerifiedAt.IsZero() {
		return artifactDescriptor{}, errArtifactEvidence
	}
	check := facts
	check.ArtifactID, check.ArtifactKey = "intent", "intent"
	if !validIntent(check) {
		return artifactDescriptor{}, errArtifactEvidence
	}
	facts.ArtifactID = uuid.NewString()
	facts.ArtifactKey = artifactPrefix(task, owner, claim) + facts.ArtifactID + ".pdf"
	return facts, nil
}
func artifactPrefix(task, owner, claim string) string {
	binding, _ := json.Marshal([4]string{"platen/ocr/output/v1", owner, task, claim})
	digest := sha256.Sum256(binding)
	return "ocr-finalization/outputs/" + hex.EncodeToString(digest[:]) + "/"
}
func artifactKeyMatches(task, owner, claim string, a artifactDescriptor) bool {
	id, err := uuid.Parse(a.ArtifactID)
	return err == nil && id.Version() == 4 && id.String() == a.ArtifactID && a.ArtifactKey == artifactPrefix(task, owner, claim)+a.ArtifactID+".pdf"
}

// A captured intent is a copy of a locked current row; its fields have no public
// constructor. It is not an authorization token for an HTTP client.
type artifactVerificationIntent struct {
	request                         finalizationRequest
	createdAt, intentAt, leaseUntil time.Time
}

func (finalizationRepository) captureArtifactInTx(tx *gorm.DB, req finalizationRequest) (artifactVerificationIntent, error) {
	job, err := lockFinalization(tx, req)
	if err != nil {
		return artifactVerificationIntent{}, err
	}
	if err := checkWorker(job, req); err != nil {
		return artifactVerificationIntent{}, err
	}
	if job.State != "processing" || job.ArtifactIntentAt == nil || !validIntent(job.Artifact) || !job.Artifact.VerifiedAt.IsZero() || !artifactKeyMatches(job.TaskID, job.OwnerID, job.ProcessingClaim, job.Artifact) {
		return artifactVerificationIntent{}, errArtifactEvidence
	}
	if !sameArtifact(req.Artifact, job.Artifact) {
		return artifactVerificationIntent{}, errArtifactEvidence
	}
	return artifactVerificationIntent{request: req, createdAt: job.CreatedAt, intentAt: *job.ArtifactIntentAt, leaseUntil: *job.ProcessingLeaseUntil}, nil
}
func (i artifactVerificationIntent) valid(now time.Time) bool {
	return i.request.ExpectedState == "processing" && i.request.ExpectedRevision > 0 && !i.createdAt.IsZero() && !i.intentAt.IsZero() && !now.Before(i.request.Now) && !now.Before(i.intentAt) && now.After(i.createdAt) && now.Before(i.leaseUntil) && validIntent(i.request.Artifact) && i.request.Artifact.VerifiedAt.IsZero() && artifactKeyMatches(i.request.TaskID, i.request.OwnerID, i.request.ProcessingClaim, i.request.Artifact)
}

type artifactEvidence struct {
	task, owner, claim string
	revision           int64
	descriptor         artifactDescriptor
	verified           bool
}

func (e artifactEvidence) matches(job *ocrFinalization, req finalizationRequest) bool {
	return e.verified && e.task == job.TaskID && e.owner == job.OwnerID && e.claim == job.ProcessingClaim && e.revision == job.Revision && sameArtifact(e.descriptor, req.Artifact)
}

type artifactStore interface {
	persist(context.Context, string, string) error
	read(context.Context, string, int64) (*storage.ArtifactFile, error)
}
type selectedArtifactStore struct{}

func (selectedArtifactStore) persist(ctx context.Context, key, path string) error {
	return storage.SaveObjectFile(ctx, key, path, "application/pdf")
}
func (selectedArtifactStore) read(ctx context.Context, key string, limit int64) (*storage.ArtifactFile, error) {
	return storage.ReadArtifact(ctx, key, limit)
}

// No production caller constructs this verifier. Bounds are explicit; the page
// limit also respects the existing OCR setting. No retention/cleanup policy.
type artifactVerifier struct {
	store            artifactStore
	maxBytes         int64
	maxPages         int
	inspectionBudget time.Duration
	now              func() time.Time
}

func newArtifactVerifier(maxBytes int64, inspectionBudget time.Duration) (artifactVerifier, error) {
	if maxBytes <= 0 || maxBytes > 250*1024*1024 || inspectionBudget <= 0 {
		return artifactVerifier{}, errArtifactEvidence
	}
	return artifactVerifier{store: selectedArtifactStore{}, maxBytes: maxBytes, maxPages: uploads.GetEnvInt("MAX_PAGES_OCR", 150), inspectionBudget: inspectionBudget, now: time.Now}, nil
}
func (v artifactVerifier) persist(ctx context.Context, intent artifactVerificationIntent, path string) error {
	if v.store == nil || v.now == nil || !intent.valid(v.now()) || ctx.Err() != nil {
		return errArtifactEvidence
	}
	file, err := os.Stat(path)
	if err != nil || !file.Mode().IsRegular() || file.Size() <= 0 || file.Size() > v.maxBytes {
		return errArtifactEvidence
	}
	// Existing adapters allow replacement. No false write-once/immutable claim:
	// exclusive writer fencing or conditional versions remain an activation gate.
	if err := v.store.persist(ctx, intent.request.Artifact.ArtifactKey, path); err != nil {
		return errArtifactWriteUncertain
	}
	return nil
}
func (v artifactVerifier) verify(ctx context.Context, intent artifactVerificationIntent) (proof artifactEvidence, err error) {
	if v.store == nil || v.now == nil || v.maxBytes <= 0 || v.maxPages <= 0 || !intent.valid(v.now()) || ctx.Err() != nil {
		return artifactEvidence{}, errArtifactEvidence
	}
	expected := intent.request.Artifact
	if expected.ByteSize > v.maxBytes || expected.PageCount > v.maxPages {
		return artifactEvidence{}, errArtifactEvidence
	}
	snapshot, err := v.store.read(ctx, expected.ArtifactKey, v.maxBytes)
	if err != nil {
		var classified *storage.ArtifactReadError
		if errors.As(err, &classified) {
			return artifactEvidence{}, err
		}
		return artifactEvidence{}, &storage.ArtifactReadError{Kind: storage.ArtifactUnavailable}
	}
	if snapshot == nil || snapshot.Cleanup == nil {
		return artifactEvidence{}, errArtifactTemporary
	}
	defer func() {
		if cleanErr := snapshot.Cleanup(); cleanErr != nil {
			proof = artifactEvidence{}
			err = errors.Join(err, errArtifactTemporary)
		}
	}()
	file, err := os.Open(snapshot.Path)
	if err != nil {
		return artifactEvidence{}, errArtifactTemporary
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			proof = artifactEvidence{}
			err = errors.Join(err, errArtifactTemporary)
		}
	}()
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(file, v.maxBytes+1))
	if err != nil || size <= 0 || size != expected.ByteSize || size > v.maxBytes || hex.EncodeToString(hash.Sum(nil)) != expected.SHA256 {
		return artifactEvidence{}, errArtifactEvidence
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return artifactEvidence{}, errArtifactTemporary
	}
	// Structural validation uses the existing pdfcpu dependency, not a signature
	// alone. It establishes readable ordinary PDF structure, not OCR correctness.
	conf := model.NewDefaultConfiguration()
	conf.Cmd = model.VALIDATE
	conf.ValidationMode = model.ValidationStrict
	parsed, err := api.ReadAndValidate(file, conf)
	if err != nil || parsed == nil || parsed.Encrypt != nil || parsed.PageCount <= 0 || parsed.PageCount != expected.PageCount || parsed.PageCount > v.maxPages {
		return artifactEvidence{}, errArtifactEvidence
	}
	// pdfcpu structural validation does not necessarily decode page streams.
	// Ghostscript already ships in the backend image. Null-page inspection fails
	// on decoding errors/warnings, produces no artifact, and has an explicit budget.
	if v.inspectionBudget <= 0 {
		return artifactEvidence{}, errArtifactInspectionUnavailable
	}
	inspectionCtx, cancel := context.WithTimeout(ctx, v.inspectionBudget)
	defer cancel()
	cmd := exec.CommandContext(inspectionCtx, "gs", "-q", "-dSAFER", "-dBATCH", "-dNOPAUSE", "-dPDFSTOPONERROR", "-dPDFSTOPONWARNING", "-sDEVICE=nullpage", "-f", snapshot.Path)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if inspectErr := cmd.Run(); inspectErr != nil {
		if inspectionCtx.Err() != nil || errors.Is(inspectErr, exec.ErrNotFound) {
			return artifactEvidence{}, errArtifactInspectionUnavailable
		}
		return artifactEvidence{}, errArtifactEvidence
	}
	verifiedAt := v.now().UTC().Truncate(time.Microsecond)
	if ctx.Err() != nil || !intent.valid(verifiedAt) {
		return artifactEvidence{}, errArtifactEvidence
	}
	expected.VerifiedAt = verifiedAt
	return artifactEvidence{task: intent.request.TaskID, owner: intent.request.OwnerID, claim: intent.request.ProcessingClaim, revision: intent.request.ExpectedRevision, descriptor: expected, verified: true}, nil
}
