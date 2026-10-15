package ocr

import (
	"errors"
	"regexp"
	"time"

	"pdfnest-backend/internal/billing"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	errFinalizationBinding  = errors.New("OCR finalization binding conflict")
	errFinalizationRevision = errors.New("OCR finalization revision or state conflict")
	errFinalizationTerminal = errors.New("OCR finalization already terminal")
)

// Unexported and unregistered. Callers must propagate errors and commit or
// rollback their transaction; these primitives never open independent ones.
type finalizationRepository struct{}

type finalizationRequest struct {
	TaskID, OwnerID, ReservationID string
	ExpectedState                  string
	ExpectedRevision               int64
	ProcessingClaim                string
	Artifact                       artifactDescriptor
	Now                            time.Time
}

func (j ocrFinalization) billingBinding() billing.ReservationBinding {
	return billing.ReservationBinding{ID: j.ReservationID, OwnerID: j.OwnerID, TaskID: j.TaskID, Kind: billing.ReservationKind(j.ReservationKind), ToolName: j.ToolName, Units: j.Units, PlanUnits: j.PlanUnits, CreditUnits: j.CreditUnits}
}

func validArtifact(a artifactDescriptor) bool {
	if a.ArtifactID == "" || a.ArtifactKey == "" || a.ByteSize <= 0 || a.PageCount <= 0 || a.ContentType != "application/pdf" || a.ValidationVersion != 1 || a.VerifiedAt.IsZero() || len(a.SHA256) != 64 {
		return false
	}
	for _, c := range a.SHA256 {
		if !(c >= 'a' && c <= 'f') && !(c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func sameArtifact(a, b artifactDescriptor) bool {
	return a.ArtifactID == b.ArtifactID && a.ArtifactKey == b.ArtifactKey && a.SHA256 == b.SHA256 && a.ByteSize == b.ByteSize && a.PageCount == b.PageCount && a.ContentType == b.ContentType && a.ValidationVersion == b.ValidationVersion && a.VerifiedAt.Equal(b.VerifiedAt)
}

// Acceptance is the only creation path. Verification cannot be backdated into
// creation; every prepared fixture must use the same lifecycle as future workers.
func (finalizationRepository) createInTx(tx *gorm.DB, j *ocrFinalization) (string, error) {
	if err := billing.RequirePostgresTransaction(tx); err != nil {
		return "", err
	}
	if j == nil || j.ProtocolVersion != 1 || j.Revision != 1 || j.CreatedAt.IsZero() || j.ToolName != billing.ImageToTextPDF.Name || j.State != "accepted" || j.ProcessingClaim != "" || j.ProcessingLeaseUntil != nil || j.ProcessingStartedAt != nil || j.ArtifactIntentAt != nil || j.ReadyAt != nil || j.DecidedAt != nil || j.Artifact != (artifactDescriptor{}) || j.CancellationCapabilityHash != "" || j.SafeCode != "" || j.NextAttemptAt != nil || j.RecoveryClaim != "" || j.RecoveryLeaseUntil != nil || j.DownloadExpiresAt != nil || j.CleanupNotBefore != nil {
		return "", errFinalizationBinding
	}
	res, err := billing.Default.LockReservationInTx(tx, j.billingBinding())
	if err != nil {
		return "", err
	}
	if res.Status != "reserved" || !j.CreatedAt.Before(res.ExpiresAt) {
		return "", errFinalizationBinding
	}
	token, err := randomAuthority()
	if err != nil {
		return "", err
	}
	j.CancellationCapabilityHash = capabilityDigest(token)
	j.UpdatedAt = j.CreatedAt
	if err := tx.Create(j).Error; err != nil {
		return "", err
	}
	return token, nil
}

// All timestamps are supplied by a trusted future coordinator, never an HTTP
// payload. This stage does not install that coordinator or a clock policy.
func (finalizationRepository) claimInTx(tx *gorm.DB, req finalizationRequest, until time.Time) (string, error) {
	job, err := lockFinalization(tx, req)
	if err != nil {
		return "", err
	}
	if err := checkRevision(job, req); err != nil {
		return "", err
	}
	if job.State != "accepted" || !until.After(req.Now) {
		return "", errFinalizationRevision
	}
	res, err := billing.Default.LockReservationInTx(tx, job.billingBinding())
	if err != nil {
		return "", err
	}
	if res.Status != "reserved" || !req.Now.Before(res.ExpiresAt) || until.After(res.ExpiresAt) {
		return "", errFinalizationBinding
	}
	claim, err := randomAuthority()
	if err != nil {
		return "", err
	}
	err = updateLifecycle(tx, job, map[string]any{"state": "processing", "processing_claim": claim, "processing_started_at": req.Now, "processing_lease_until": until}, req.Now)
	return claim, err
}

func validIntent(a artifactDescriptor) bool {
	a.VerifiedAt = time.Unix(1, 0).UTC()
	return validArtifact(a)
}
func sameIntent(a, b artifactDescriptor) bool {
	a.VerifiedAt, b.VerifiedAt = time.Time{}, time.Time{}
	return sameArtifact(a, b)
}
func checkWorker(job *ocrFinalization, req finalizationRequest) error {
	if terminal(job.State) {
		return errFinalizationTerminal
	}
	if err := checkRevision(job, req); err != nil {
		return err
	}
	if (job.State != "processing" && job.State != "ready") || req.ProcessingClaim == "" || job.ProcessingClaim != req.ProcessingClaim || job.ProcessingLeaseUntil == nil || !req.Now.Before(*job.ProcessingLeaseUntil) {
		return errFinalizationBinding
	}
	return nil
}

func (finalizationRepository) recordIntentInTx(tx *gorm.DB, req finalizationRequest) error {
	job, err := lockFinalization(tx, req)
	if err != nil {
		return err
	}
	if err := checkWorker(job, req); err != nil {
		return err
	}
	if job.State != "processing" || job.ArtifactIntentAt != nil || !artifactKeyMatches(job.TaskID, job.OwnerID, job.ProcessingClaim, req.Artifact) || !validIntent(req.Artifact) || !req.Artifact.VerifiedAt.IsZero() {
		return errFinalizationBinding
	}
	updates := artifactUpdates(req.Artifact)
	updates["artifact_intent_at"] = req.Now
	return updateLifecycle(tx, job, updates, req.Now)
}

// Readiness requires private evidence produced by independent object readback.
// Storage/PDF work happens outside this transaction; the locked state and claim
// are rechecked here. Evidence is point-in-time, not a storage immutability claim.
func (finalizationRepository) readyInTx(tx *gorm.DB, req finalizationRequest, proof artifactEvidence) error {
	job, err := lockFinalization(tx, req)
	if err != nil {
		return err
	}
	if err := checkWorker(job, req); err != nil {
		return err
	}
	if !proof.matches(job, req) || !artifactKeyMatches(job.TaskID, job.OwnerID, job.ProcessingClaim, req.Artifact) || job.State != "processing" || job.ArtifactIntentAt == nil || !validArtifact(req.Artifact) || !sameIntent(job.Artifact, req.Artifact) || !req.Artifact.VerifiedAt.After(job.CreatedAt) || req.Artifact.VerifiedAt.Before(*job.ArtifactIntentAt) || req.Artifact.VerifiedAt.After(req.Now) {
		return errFinalizationBinding
	}
	return updateLifecycle(tx, job, map[string]any{"state": "ready", "ready_at": req.Now, "artifact_verified_at": req.Artifact.VerifiedAt}, req.Now)
}

func artifactUpdates(a artifactDescriptor) map[string]any {
	return map[string]any{"artifact_id": a.ArtifactID, "artifact_key": a.ArtifactKey, "artifact_sha256": a.SHA256, "artifact_byte_size": a.ByteSize, "artifact_page_count": a.PageCount, "artifact_content_type": a.ContentType, "artifact_validation_version": a.ValidationVersion, "artifact_verified_at": a.VerifiedAt}
}
func terminal(state string) bool {
	return state == "succeeded" || state == "cancelled" || state == "failed"
}

func lockFinalization(tx *gorm.DB, req finalizationRequest) (*ocrFinalization, error) {
	if err := billing.RequirePostgresTransaction(tx); err != nil {
		return nil, err
	}
	if req.TaskID == "" || req.OwnerID == "" || req.ReservationID == "" || req.Now.IsZero() {
		return nil, errFinalizationBinding
	}
	var job ocrFinalization
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("task_id = ?", req.TaskID).First(&job).Error; err != nil {
		return nil, err
	}
	if job.ProtocolVersion != 1 || job.OwnerID != req.OwnerID || job.ReservationID != req.ReservationID || job.ToolName != billing.ImageToTextPDF.Name || req.Now.Before(job.UpdatedAt) {
		return nil, errFinalizationBinding
	}
	return &job, nil
}

func checkRevision(job *ocrFinalization, req finalizationRequest) error {
	if job.Revision != req.ExpectedRevision || job.State != req.ExpectedState {
		return errFinalizationRevision
	}
	return nil
}

func (finalizationRepository) succeedInTx(tx *gorm.DB, req finalizationRequest) error {
	job, err := lockFinalization(tx, req)
	if err != nil {
		return err
	}
	if !validArtifact(req.Artifact) || !sameArtifact(job.Artifact, req.Artifact) || req.Artifact.VerifiedAt.After(req.Now) {
		return errFinalizationBinding
	}
	if job.State == "succeeded" {
		if req.ProcessingClaim == "" || job.ProcessingClaim != req.ProcessingClaim {
			return errFinalizationBinding
		}
		// Only a retry of this same decision (original or resulting revision).
		if req.ExpectedState != "ready" || (req.ExpectedRevision != job.Revision && req.ExpectedRevision != job.Revision-1) {
			return errFinalizationRevision
		}
		res, err := billing.Default.LockReservationInTx(tx, job.billingBinding())
		if err != nil {
			return err
		}
		if res.Status != "committed" {
			return billing.ErrReservationState
		}
		return nil
	}
	if job.State == "cancelled" || job.State == "failed" {
		return errFinalizationTerminal
	}
	if err := checkWorker(job, req); err != nil {
		return err
	}
	if job.State != "ready" {
		return errFinalizationRevision
	}
	if err := billing.Default.SettleReservationInTx(tx, job.billingBinding(), req.Now); err != nil {
		return err
	}
	return updateDecision(tx, job, "succeeded", "", req.Now)
}

// Cancellation has no worker claim or reservation argument. The locked row
// supplies the exact billing binding after owner/capability authorization.
type cancellationRequest struct {
	TaskID           string
	ExpectedState    string
	ExpectedRevision int64
	Now              time.Time
	Authority        cancellationAuthority
}

func lockCancellation(tx *gorm.DB, req cancellationRequest) (*ocrFinalization, error) {
	if err := billing.RequirePostgresTransaction(tx); err != nil {
		return nil, err
	}
	if req.TaskID == "" || req.Now.IsZero() {
		return nil, errFinalizationBinding
	}
	var job ocrFinalization
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("task_id = ?", req.TaskID).First(&job).Error; err != nil {
		return nil, err
	}
	if job.ProtocolVersion != 1 || req.Now.Before(job.UpdatedAt) {
		return nil, errFinalizationBinding
	}
	return &job, nil
}
func (finalizationRepository) cancelInTx(tx *gorm.DB, req cancellationRequest) error {
	job, err := lockCancellation(tx, req)
	if err != nil {
		return err
	}
	if !req.Authority.permits(job) {
		return errFinalizationBinding
	}
	return endLocked(tx, job, req.ExpectedState, req.ExpectedRevision, "cancelled", "USER_CANCELLED", req.Now)
}
func (finalizationRepository) failWorkerInTx(tx *gorm.DB, req finalizationRequest, code string) error {
	job, err := lockFinalization(tx, req)
	if err != nil {
		return err
	}
	if err := checkWorker(job, req); err != nil {
		return err
	}
	return endLocked(tx, job, req.ExpectedState, req.ExpectedRevision, "failed", code, req.Now)
}
func (finalizationRepository) failDeadlineInTx(tx *gorm.DB, req cancellationRequest, authority deadlineAuthority) error {
	job, err := lockCancellation(tx, req)
	if err != nil {
		return err
	}
	if !authority.verified || authority.taskID != job.TaskID {
		return errFinalizationBinding
	}
	if terminal(job.State) {
		return errFinalizationTerminal
	}
	if err := checkRevision(job, finalizationRequest{ExpectedState: req.ExpectedState, ExpectedRevision: req.ExpectedRevision}); err != nil {
		return err
	}
	res, err := billing.Default.LockReservationInTx(tx, job.billingBinding())
	if err != nil {
		return err
	}
	if (job.ProcessingLeaseUntil == nil || req.Now.Before(*job.ProcessingLeaseUntil)) && req.Now.Before(res.ExpiresAt) {
		return errFinalizationBinding
	}
	return endLocked(tx, job, req.ExpectedState, req.ExpectedRevision, "failed", "DEADLINE_EXCEEDED", req.Now)
}
func endLocked(tx *gorm.DB, job *ocrFinalization, expectedState string, revision int64, state, code string, now time.Time) error {
	if terminal(job.State) {
		return errFinalizationTerminal
	}
	if err := checkRevision(job, finalizationRequest{ExpectedState: expectedState, ExpectedRevision: revision}); err != nil {
		return err
	}
	if ok, _ := regexp.MatchString(`^[A-Z][A-Z0-9_]{0,63}$`, code); !ok {
		return errFinalizationBinding
	}
	if err := billing.Default.ReleaseReservationInTx(tx, job.billingBinding(), now); err != nil {
		return err
	}
	return updateDecision(tx, job, state, code, now)
}

func updateDecision(tx *gorm.DB, job *ocrFinalization, state, code string, now time.Time) error {
	updates := map[string]any{"state": state, "safe_code": code, "decided_at": now}
	if state != "succeeded" {
		updates["processing_claim"] = ""
		updates["processing_lease_until"] = nil
	}
	return updateLifecycle(tx, job, updates, now)
}

func updateLifecycle(tx *gorm.DB, job *ocrFinalization, updates map[string]any, now time.Time) error {
	updates["revision"] = job.Revision + 1
	updates["updated_at"] = now
	result := tx.Model(&ocrFinalization{}).Where("task_id = ? AND revision = ? AND state = ?", job.TaskID, job.Revision, job.State).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errFinalizationRevision
	}
	return nil
}
