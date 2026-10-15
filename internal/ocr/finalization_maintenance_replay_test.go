package ocr

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestOCRSuccessReplayMaintenanceCharacterization executes a deterministic characterization
// of the dormant OCR finalization repository's success replay logic across synthetic
// recovery metadata, revision, and chronology changes.
//
// M1-S2-16: Characterizes whether later maintenance operations (such as cleanup lease claims)
// preserve or invalidate legitimate idempotency retries of already-settled tasks.
func TestOCRSuccessReplayMaintenanceCharacterization(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")

	// -------------------------------------------------------------------------
	// Case A: Baseline successful retry
	// -------------------------------------------------------------------------
	t.Run("case_a_baseline_successful_retry", func(t *testing.T) {
		db, observer := foundationDB(t)
		job, _ := foundationReady(t, db, 0)
		req := foundationRequest(job)
		r := finalizationRepository{}

		// Initial success transition: ready -> succeeded
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			return r.succeedInTx(tx, req)
		}))

		// Verify durable initial settlement
		assertFoundation(t, observer, job, "succeeded", "committed", 7, 50, 1, 5)

		// Replay original request with same timestamp (req.Now == job.UpdatedAt)
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			return r.succeedInTx(tx, req)
		}))

		// Replay original request with a later timestamp
		laterReq := req
		laterReq.Now = req.Now.Add(5 * time.Second)
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			return r.succeedInTx(tx, laterReq)
		}))

		// Durable assertions: no double billing, exactly 1 usage log, 50 credits, revision 5
		assertFoundation(t, observer, job, "succeeded", "committed", 7, 50, 1, 5)
	})

	// -------------------------------------------------------------------------
	// Case B: Maintenance-only metadata update without revision or UpdatedAt advance
	// -------------------------------------------------------------------------
	t.Run("case_b_maintenance_metadata_without_revision_advance", func(t *testing.T) {
		db, observer := foundationDB(t)
		job, _ := foundationReady(t, db, 0)
		req := foundationRequest(job)
		r := finalizationRepository{}

		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			return r.succeedInTx(tx, req)
		}))
		assertFoundation(t, observer, job, "succeeded", "committed", 7, 50, 1, 5)

		// Simulate maintenance updating only dormant recovery fields
		maintClaim, err := randomAuthority()
		require.NoError(t, err)
		nextAttempt := req.Now.Add(10 * time.Minute)
		leaseUntil := req.Now.Add(15 * time.Minute)
		cleanupNotBefore := req.Now.Add(24 * time.Hour)
		downloadExpiresAt := req.Now.Add(12 * time.Hour)

		require.NoError(t, db.Model(&ocrFinalization{}).Where("task_id = ?", job.TaskID).Updates(map[string]any{
			"updated_at":           req.Now,
			"next_attempt_at":      nextAttempt,
			"recovery_claim":       maintClaim,
			"recovery_lease_until": leaseUntil,
			"cleanup_not_before":   cleanupNotBefore,
			"download_expires_at":  downloadExpiresAt,
		}).Error)

		// Replay original request
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			return r.succeedInTx(tx, req)
		}))

		// Verify state, billing, and accounting remain unchanged
		assertFoundation(t, observer, job, "succeeded", "committed", 7, 50, 1, 5)

		// Verify recovery metadata was preserved and not wiped by replay
		var reloaded ocrFinalization
		require.NoError(t, observer.First(&reloaded, "task_id = ?", job.TaskID).Error)
		require.Equal(t, maintClaim, reloaded.RecoveryClaim)
		assertOptionalTime(t, &nextAttempt, reloaded.NextAttemptAt)
		assertOptionalTime(t, &leaseUntil, reloaded.RecoveryLeaseUntil)
		assertOptionalTime(t, &cleanupNotBefore, reloaded.CleanupNotBefore)
		assertOptionalTime(t, &downloadExpiresAt, reloaded.DownloadExpiresAt)
	})

	// -------------------------------------------------------------------------
	// Case C: Revision advanced by maintenance
	// -------------------------------------------------------------------------
	t.Run("case_c_maintenance_advances_revision", func(t *testing.T) {
		db, observer := foundationDB(t)
		job, _ := foundationReady(t, db, 0)
		req := foundationRequest(job)
		r := finalizationRepository{}

		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			return r.succeedInTx(tx, req)
		}))
		assertFoundation(t, observer, job, "succeeded", "committed", 7, 50, 1, 5)

		// Simulate a proposed maintenance operation that acquires a recovery lease and increments revision
		maintClaim, err := randomAuthority()
		require.NoError(t, err)
		leaseUntil := req.Now.Add(5 * time.Minute)

		require.NoError(t, db.Model(&ocrFinalization{}).Where("task_id = ?", job.TaskID).Updates(map[string]any{
			"updated_at":           req.Now,
			"revision":             6,
			"recovery_claim":       maintClaim,
			"recovery_lease_until": leaseUntil,
		}).Error)

		// Retry original success request (which carries ExpectedRevision = 4, ExpectedState = "ready")
		// The current repository checks:
		// req.ExpectedRevision != job.Revision && req.ExpectedRevision != job.Revision-1
		// Here: ExpectedRevision(4) != 6 AND ExpectedRevision(4) != (6 - 1 = 5)
		// Expected characterization: Rejected with errFinalizationRevision.
		err = db.Transaction(func(tx *gorm.DB) error {
			return r.succeedInTx(tx, req)
		})
		require.ErrorIs(t, err, errFinalizationRevision, "replay must be rejected when maintenance has incremented revision beyond job.Revision-1")

		// Authoritative SQL decision and billing remain committed and uncorrupted
		assertFoundation(t, observer, job, "succeeded", "committed", 7, 50, 1, 6)
	})

	// -------------------------------------------------------------------------
	// Case D: UpdatedAt advanced independently
	// -------------------------------------------------------------------------
	t.Run("case_d_maintenance_advances_updated_at", func(t *testing.T) {
		db, observer := foundationDB(t)
		job, _ := foundationReady(t, db, 0)
		req := foundationRequest(job)
		r := finalizationRepository{}

		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			return r.succeedInTx(tx, req)
		}))
		assertFoundation(t, observer, job, "succeeded", "committed", 7, 50, 1, 5)

		// Simulate maintenance advancing UpdatedAt while leaving Revision = 5
		maintUpdate := req.Now.Add(time.Minute)
		require.NoError(t, db.Model(&ocrFinalization{}).Where("task_id = ?", job.TaskID).Updates(map[string]any{
			"updated_at": maintUpdate,
		}).Error)

		// Retry using original request timestamp (req.Now predates maintUpdate)
		// lockFinalization checks: req.Now.Before(job.UpdatedAt)
		// Expected characterization: Rejected with errFinalizationBinding.
		err := db.Transaction(func(tx *gorm.DB) error {
			return r.succeedInTx(tx, req)
		})
		require.ErrorIs(t, err, errFinalizationBinding, "replay with original timestamp must be rejected when UpdatedAt was advanced by maintenance")

		// Contrast: a retry with fresh timestamp (after maintUpdate) succeeds because Revision is still 5
		freshReq := req
		freshReq.Now = maintUpdate.Add(time.Second)
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			return r.succeedInTx(tx, freshReq)
		}))

		// Authoritative SQL decision and billing remain committed and uncorrupted
		assertFoundation(t, observer, job, "succeeded", "committed", 7, 50, 1, 5)
	})

	// -------------------------------------------------------------------------
	// Case E: Multiple housekeeping revisions
	// -------------------------------------------------------------------------
	t.Run("case_e_multiple_housekeeping_revisions", func(t *testing.T) {
		db, observer := foundationDB(t)
		job, _ := foundationReady(t, db, 0)
		req := foundationRequest(job)
		r := finalizationRepository{}

		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			return r.succeedInTx(tx, req)
		}))
		assertFoundation(t, observer, job, "succeeded", "committed", 7, 50, 1, 5)

		// Simulate two successive maintenance operations that advance revision from 5 to 7
		require.NoError(t, db.Model(&ocrFinalization{}).Where("task_id = ?", job.TaskID).Updates(map[string]any{
			"updated_at": req.Now,
			"revision":   7,
		}).Error)

		// 1. Replay with pre-decision revision (4): 4 != 7 && 4 != 6 -> rejected
		err := db.Transaction(func(tx *gorm.DB) error {
			return r.succeedInTx(tx, req)
		})
		require.ErrorIs(t, err, errFinalizationRevision)

		// 2. Replay with immediate post-decision revision (5): 5 != 7 && 5 != 6 -> rejected
		postReq := req
		postReq.ExpectedRevision = 5
		err = db.Transaction(func(tx *gorm.DB) error {
			return r.succeedInTx(tx, postReq)
		})
		require.ErrorIs(t, err, errFinalizationRevision)

		// Authoritative SQL decision and billing remain committed and uncorrupted
		assertFoundation(t, observer, job, "succeeded", "committed", 7, 50, 1, 7)
	})

	// -------------------------------------------------------------------------
	// Case F: Strict rejection invariants still required
	// -------------------------------------------------------------------------
	t.Run("case_f_strict_rejection_invariants", func(t *testing.T) {
		r := finalizationRepository{}

		// 1. Wrong Owner
		t.Run("wrong_owner", func(t *testing.T) {
			db, observer := foundationDB(t)
			job, _ := foundationReady(t, db, 0)
			req := foundationRequest(job)
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, req) }))

			wrongReq := req
			wrongReq.OwnerID = uuid.NewString()
			err := db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, wrongReq) })
			require.ErrorIs(t, err, errFinalizationBinding)
			assertFoundation(t, observer, job, "succeeded", "committed", 7, 50, 1, 5)
		})

		// 2. Wrong Reservation
		t.Run("wrong_reservation", func(t *testing.T) {
			db, observer := foundationDB(t)
			job, _ := foundationReady(t, db, 0)
			req := foundationRequest(job)
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, req) }))

			wrongReq := req
			wrongReq.ReservationID = uuid.NewString()
			err := db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, wrongReq) })
			require.ErrorIs(t, err, errFinalizationBinding)
			assertFoundation(t, observer, job, "succeeded", "committed", 7, 50, 1, 5)
		})

		// 3. Wrong Claim
		t.Run("wrong_claim", func(t *testing.T) {
			db, observer := foundationDB(t)
			job, _ := foundationReady(t, db, 0)
			req := foundationRequest(job)
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, req) }))

			wrongReq := req
			wrongReq.ProcessingClaim = uuid.NewString()
			err := db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, wrongReq) })
			require.ErrorIs(t, err, errFinalizationBinding)
			assertFoundation(t, observer, job, "succeeded", "committed", 7, 50, 1, 5)
		})

		// 4. Conflicting Artifact Descriptor
		t.Run("conflicting_artifact", func(t *testing.T) {
			db, observer := foundationDB(t)
			job, _ := foundationReady(t, db, 0)
			req := foundationRequest(job)
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, req) }))

			conflictReq := req
			conflictReq.Artifact.SHA256 = strings.Repeat("d", 64)
			err := db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, conflictReq) })
			require.ErrorIs(t, err, errFinalizationBinding)
			assertFoundation(t, observer, job, "succeeded", "committed", 7, 50, 1, 5)
		})

		// 5. Cancelled Task Replay Rejection
		t.Run("cancelled_task", func(t *testing.T) {
			db, observer := foundationDB(t)
			job, _ := foundationReady(t, db, 0)
			req := foundationRequest(job)

			// Transition to cancelled first
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				return endFoundation(tx, req, "cancelled", "USER_CANCELLED")
			}))
			assertFoundation(t, observer, job, "cancelled", "released", 0, 50, 0, 5)

			// Attempt succeedInTx on cancelled task
			err := db.Transaction(func(tx *gorm.DB) error {
				return r.succeedInTx(tx, req)
			})
			require.ErrorIs(t, err, errFinalizationTerminal)
			assertFoundation(t, observer, job, "cancelled", "released", 0, 50, 0, 5)
		})

		// 6. Failed Task Replay Rejection
		t.Run("failed_task", func(t *testing.T) {
			db, observer := foundationDB(t)
			job, _ := foundationReady(t, db, 0)
			req := foundationRequest(job)

			// Transition to failed first
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				return endFoundation(tx, req, "failed", "DEADLINE_EXCEEDED")
			}))
			assertFoundation(t, observer, job, "failed", "released", 0, 50, 0, 5)

			// Attempt succeedInTx on failed task
			err := db.Transaction(func(tx *gorm.DB) error {
				return r.succeedInTx(tx, req)
			})
			require.ErrorIs(t, err, errFinalizationTerminal)
			assertFoundation(t, observer, job, "failed", "released", 0, 50, 0, 5)
		})
	})
}
