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

func (finalizationRepository) createInTx(tx *gorm.DB, j *ocrFinalization) error {
	if err := billing.RequirePostgresTransaction(tx); err != nil {
		return err
	}
	if j == nil || j.ProtocolVersion != 1 || j.Revision != 1 || j.CreatedAt.IsZero() || j.ToolName != billing.ImageToTextPDF.Name {
		return errFinalizationBinding
	}
	switch j.State {
	case "accepted", "processing":
	case "ready":
		if !validArtifact(j.Artifact) || j.Artifact.VerifiedAt.After(j.CreatedAt) || j.ProcessingClaim == "" {
			return errFinalizationBinding
		}
	default:
		return errFinalizationTerminal
	}
	res, err := billing.Default.LockReservationInTx(tx, j.billingBinding())
	if err != nil {
		return err
	}
	if res.Status != "reserved" {
		return errFinalizationBinding
	}
	return tx.Create(j).Error
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
	if job.ProtocolVersion != 1 || job.OwnerID != req.OwnerID || job.ReservationID != req.ReservationID || job.ToolName != billing.ImageToTextPDF.Name || job.ProcessingClaim != req.ProcessingClaim || req.Now.Before(job.CreatedAt) {
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
	if err := checkRevision(job, req); err != nil {
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

func (finalizationRepository) endInTx(tx *gorm.DB, req finalizationRequest, state, code string) error {
	if state != "cancelled" && state != "failed" {
		return errFinalizationBinding
	}
	// Errors are bounded codes, never worker/PDF/credential content.
	if ok, _ := regexp.MatchString(`^[A-Z][A-Z0-9_]{0,63}$`, code); !ok {
		return errFinalizationBinding
	}
	job, err := lockFinalization(tx, req)
	if err != nil {
		return err
	}
	if job.State == "succeeded" || job.State == "cancelled" || job.State == "failed" {
		return errFinalizationTerminal
	}
	if err := checkRevision(job, req); err != nil {
		return err
	}
	if err := billing.Default.ReleaseReservationInTx(tx, job.billingBinding(), req.Now); err != nil {
		return err
	}
	return updateDecision(tx, job, state, code, req.Now)
}

func updateDecision(tx *gorm.DB, job *ocrFinalization, state, code string, now time.Time) error {
	result := tx.Model(&ocrFinalization{}).Where("task_id = ? AND revision = ? AND state = ?", job.TaskID, job.Revision, job.State).
		Updates(map[string]any{"state": state, "revision": job.Revision + 1, "safe_code": code, "decided_at": now, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errFinalizationRevision
	}
	return nil
}
