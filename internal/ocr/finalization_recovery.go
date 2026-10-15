package ocr

import (
	"context"
	"errors"
	"time"

	"pdfnest-backend/internal/billing"

	"gorm.io/gorm"
)

var errRecoveryClaim = errors.New("OCR recovery claim or lease conflict")

// A future trusted server adapter must authorize the recovery invocation and
// return its canonical task, never echo a request-controlled identity. No
// production adapter, scheduler or public capability is installed here.
type recoveryVerifier interface {
	VerifyRecoveryTask(context.Context) (string, error)
}

type recoveryAuthority struct {
	taskID   string
	verified bool
}

func trustedRecovery(ctx context.Context, verifier recoveryVerifier) (recoveryAuthority, error) {
	if verifier == nil {
		return recoveryAuthority{}, errFinalizationBinding
	}
	task, err := verifier.VerifyRecoveryTask(ctx)
	if err != nil {
		return recoveryAuthority{}, err
	}
	if task == "" {
		return recoveryAuthority{}, errFinalizationBinding
	}
	return recoveryAuthority{taskID: task, verified: true}, nil
}

type recoveryRequest struct {
	TaskID, OwnerID, ReservationID string
	ExpectedState                  string
	ExpectedRevision               int64
	Now                            time.Time
	Authority                      recoveryAuthority
}

// Recovery is terminal SQL bookkeeping only. Preserve task-first lock order,
// exact captured reservation identity/allocation and the terminal billing state.
func lockRecovery(tx *gorm.DB, req recoveryRequest) (*ocrFinalization, error) {
	if err := billing.RequirePostgresTransaction(tx); err != nil {
		return nil, err
	}
	if !req.Authority.verified || req.Authority.taskID == "" || req.Authority.taskID != req.TaskID {
		return nil, errFinalizationBinding
	}
	job, err := lockFinalization(tx, finalizationRequest{TaskID: req.TaskID, OwnerID: req.OwnerID, ReservationID: req.ReservationID, Now: req.Now})
	if err != nil {
		return nil, err
	}
	if !terminal(job.State) {
		return nil, errFinalizationRevision
	}
	if err := checkRevision(job, finalizationRequest{ExpectedState: req.ExpectedState, ExpectedRevision: req.ExpectedRevision}); err != nil {
		return nil, err
	}
	if job.DecidedAt == nil || job.DecidedAt.IsZero() {
		return nil, errFinalizationBinding
	}
	if (job.RecoveryClaim == "") != (job.RecoveryLeaseUntil == nil) || (job.RecoveryLeaseUntil != nil && job.RecoveryLeaseUntil.IsZero()) {
		return nil, errRecoveryClaim
	}
	res, err := billing.Default.LockReservationInTx(tx, job.billingBinding())
	if err != nil {
		return nil, err
	}
	wantStatus := "released"
	if job.State == "succeeded" {
		wantStatus = "committed"
	}
	if res.Status != wantStatus {
		return nil, billing.ErrReservationState
	}
	return job, nil
}

// The returned token is provisional until the caller commits. Rollback or an
// uncertain commit must never be treated as committed recovery authority. A
// claim grants no physical storage deletion permission and cannot revoke I/O.
func (finalizationRepository) acquireRecoveryInTx(tx *gorm.DB, req recoveryRequest, until time.Time) (string, error) {
	job, err := lockRecovery(tx, req)
	if err != nil {
		return "", err
	}
	// PostgreSQL persists microseconds. Validate the persisted deadline rather
	// than accepting an apparent extension that truncates to the same instant.
	until = until.Truncate(time.Microsecond)
	if job.NextAttemptAt == nil || job.NextAttemptAt.IsZero() || job.NextAttemptAt.After(req.Now) || !until.After(req.Now) || (job.RecoveryLeaseUntil != nil && req.Now.Before(*job.RecoveryLeaseUntil)) {
		return "", errRecoveryClaim
	}
	claim, err := randomAuthority()
	if err != nil {
		return "", err
	}
	if err := updateRecoveryOwnership(tx, job, claim, &until); err != nil {
		return "", err
	}
	return claim, nil
}

func (finalizationRepository) renewRecoveryInTx(tx *gorm.DB, req recoveryRequest, claim string, until time.Time) error {
	job, err := lockRecovery(tx, req)
	if err != nil {
		return err
	}
	until = until.Truncate(time.Microsecond)
	if !holdsRecovery(job, claim, req.Now) || !until.After(req.Now) || !until.After(*job.RecoveryLeaseUntil) {
		return errRecoveryClaim
	}
	return updateRecoveryOwnership(tx, job, claim, &until)
}

// Relinquishment drops ownership only. It does not acknowledge cleanup or
// provider success and must leave NextAttemptAt and all evidence untouched.
func (finalizationRepository) relinquishRecoveryInTx(tx *gorm.DB, req recoveryRequest, claim string) error {
	job, err := lockRecovery(tx, req)
	if err != nil {
		return err
	}
	if !holdsRecovery(job, claim, req.Now) {
		return errRecoveryClaim
	}
	return updateRecoveryOwnership(tx, job, "", nil)
}

func holdsRecovery(job *ocrFinalization, claim string, now time.Time) bool {
	return claim != "" && claim == job.RecoveryClaim && job.RecoveryLeaseUntil != nil && now.Before(*job.RecoveryLeaseUntil)
}

// Call only with the locked/validated row. CAS also fences stale snapshots by
// both the previous token and lease. UpdateColumns avoids GORM's automatic
// UpdatedAt advancement; lifecycle, scheduling and outcome evidence are untouched.
func updateRecoveryOwnership(tx *gorm.DB, job *ocrFinalization, claim string, until *time.Time) error {
	query := tx.Model(&ocrFinalization{}).Where("task_id = ? AND owner_id = ? AND reservation_id = ? AND state = ? AND revision = ? AND recovery_claim = ?", job.TaskID, job.OwnerID, job.ReservationID, job.State, job.Revision, job.RecoveryClaim)
	if job.RecoveryLeaseUntil == nil {
		query = query.Where("recovery_lease_until IS NULL")
	} else {
		query = query.Where("recovery_lease_until = ?", *job.RecoveryLeaseUntil)
	}
	result := query.UpdateColumns(map[string]any{"recovery_claim": claim, "recovery_lease_until": until})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errRecoveryClaim
	}
	return nil
}
