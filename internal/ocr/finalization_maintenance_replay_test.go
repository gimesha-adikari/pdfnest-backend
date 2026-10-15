package ocr

import (
	"strings"
	"testing"
	"time"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/billing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestOCRSuccessReplayMaintenanceCharacterization executes a deterministic characterization
// of the dormant OCR finalization repository's success replay logic across synthetic
// recovery metadata, revision, and chronology changes.
//
// M1-S2-16 cases A-F are retained. M1-S2-17 changes only case D's original-time
// replay expectation and adds decision chronology, terminal guards and lock barriers.
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

		before := observeReplay(t, observer, job)

		// Replay original request with the persisted decision timestamp
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			return r.succeedInTx(tx, req)
		}))

		// Replay original request with a later timestamp
		laterReq := req
		laterReq.Now = req.Now.Add(5 * time.Second)
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			return r.succeedInTx(tx, laterReq)
		}))

		// The resulting revision is also an allowed acknowledgment of this decision.
		resultReq := req
		resultReq.ExpectedRevision = 5
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, resultReq) }))
		require.Equal(t, before, observeReplay(t, observer, job))

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

		before := observeReplay(t, observer, job)

		// Replay original request
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			return r.succeedInTx(tx, req)
		}))

		require.Equal(t, before, observeReplay(t, observer, job))

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

		before := observeReplay(t, observer, job)

		// Retry original success request (which carries ExpectedRevision = 4, ExpectedState = "ready")
		// The current repository checks:
		// req.ExpectedRevision != job.Revision && req.ExpectedRevision != job.Revision-1
		// Here: ExpectedRevision(4) != 6 AND ExpectedRevision(4) != (6 - 1 = 5)
		// Expected characterization: Rejected with errFinalizationRevision.
		err = db.Transaction(func(tx *gorm.DB) error {
			return r.succeedInTx(tx, req)
		})
		require.ErrorIs(t, err, errFinalizationRevision, "replay must be rejected when maintenance has incremented revision beyond job.Revision-1")

		require.Equal(t, before, observeReplay(t, observer, job))

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
			"updated_at":           maintUpdate,
			"recovery_claim":       strings.Repeat("c", 64),
			"recovery_lease_until": maintUpdate.Add(time.Minute),
		}).Error)

		before := observeReplay(t, observer, job)

		// PR #21 rejected this original-time replay with errFinalizationBinding.
		// M1-S2-17 uses DecidedAt for an exact already-succeeded acknowledgment.
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			return r.succeedInTx(tx, req)
		}))

		// A later timestamp remains valid too; neither retry rewrites maintenance.
		freshReq := req
		freshReq.Now = maintUpdate.Add(time.Second)
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			return r.succeedInTx(tx, freshReq)
		}))
		require.Equal(t, before, observeReplay(t, observer, job))

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

		before := observeReplay(t, observer, job)

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
		require.Equal(t, before, observeReplay(t, observer, job))

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

			before := observeReplay(t, observer, job)
			wrongReq := req
			wrongReq.OwnerID = uuid.NewString()
			err := db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, wrongReq) })
			require.ErrorIs(t, err, errFinalizationBinding)
			require.Equal(t, before, observeReplay(t, observer, job))
			assertFoundation(t, observer, job, "succeeded", "committed", 7, 50, 1, 5)
		})

		// 2. Wrong Reservation
		t.Run("wrong_reservation", func(t *testing.T) {
			db, observer := foundationDB(t)
			job, _ := foundationReady(t, db, 0)
			req := foundationRequest(job)
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, req) }))

			before := observeReplay(t, observer, job)
			wrongReq := req
			wrongReq.ReservationID = uuid.NewString()
			err := db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, wrongReq) })
			require.ErrorIs(t, err, errFinalizationBinding)
			require.Equal(t, before, observeReplay(t, observer, job))
			assertFoundation(t, observer, job, "succeeded", "committed", 7, 50, 1, 5)
		})

		// 3. Wrong Claim
		t.Run("wrong_claim", func(t *testing.T) {
			db, observer := foundationDB(t)
			job, _ := foundationReady(t, db, 0)
			req := foundationRequest(job)
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, req) }))

			before := observeReplay(t, observer, job)
			wrongReq := req
			wrongReq.ProcessingClaim = uuid.NewString()
			err := db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, wrongReq) })
			require.ErrorIs(t, err, errFinalizationBinding)
			require.Equal(t, before, observeReplay(t, observer, job))
			assertFoundation(t, observer, job, "succeeded", "committed", 7, 50, 1, 5)
		})

		// 4. Conflicting Artifact Descriptor
		t.Run("conflicting_artifact", func(t *testing.T) {
			db, observer := foundationDB(t)
			job, _ := foundationReady(t, db, 0)
			req := foundationRequest(job)
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, req) }))

			before := observeReplay(t, observer, job)
			conflictReq := req
			conflictReq.Artifact.SHA256 = strings.Repeat("d", 64)
			err := db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, conflictReq) })
			require.ErrorIs(t, err, errFinalizationBinding)
			require.Equal(t, before, observeReplay(t, observer, job))
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

			before := observeReplay(t, observer, job)

			// Attempt succeedInTx on cancelled task
			err := db.Transaction(func(tx *gorm.DB) error {
				return r.succeedInTx(tx, req)
			})
			require.ErrorIs(t, err, errFinalizationTerminal)
			require.Equal(t, before, observeReplay(t, observer, job))
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

			before := observeReplay(t, observer, job)

			// Attempt succeedInTx on failed task
			err := db.Transaction(func(tx *gorm.DB) error {
				return r.succeedInTx(tx, req)
			})
			require.ErrorIs(t, err, errFinalizationTerminal)
			require.Equal(t, before, observeReplay(t, observer, job))
			assertFoundation(t, observer, job, "failed", "released", 0, 50, 0, 5)
		})
	})

	// A timestamp between artifact verification and the actual decision must
	// fail even after maintenance; artifact chronology alone is insufficient.
	t.Run("pre_decision_timestamp", func(t *testing.T) {
		db, observer := foundationDB(t)
		job, _ := foundationReady(t, db, 0)
		req := foundationRequest(job)
		req.Now = req.Now.Add(10 * time.Second)
		r := finalizationRepository{}
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, req) }))
		require.NoError(t, db.Model(&ocrFinalization{}).Where("task_id = ?", job.TaskID).Updates(map[string]any{"updated_at": req.Now.Add(time.Minute)}).Error)
		before := observeReplay(t, observer, job)
		require.NotNil(t, before.Task.DecidedAt)
		require.True(t, req.Now.Equal(*before.Task.DecidedAt))
		req.Now = req.Now.Add(-time.Microsecond)
		require.True(t, req.Now.After(req.Artifact.VerifiedAt))
		require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, req) }), errFinalizationBinding)
		require.Equal(t, before, observeReplay(t, observer, job))
		assertFoundation(t, observer, job, "succeeded", "committed", 7, 50, 1, 5)
	})

	t.Run("terminal_lifecycle_update_guard", func(t *testing.T) {
		for _, state := range []string{"succeeded", "cancelled", "failed"} {
			t.Run(state, func(t *testing.T) {
				db, observer := foundationDB(t)
				job, _ := foundationReady(t, db, 0)
				req := foundationRequest(job)
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
					if state == "succeeded" {
						return (finalizationRepository{}).succeedInTx(tx, req)
					}
					return endFoundation(tx, req, state, "CONTROLLED_STOP")
				}))
				before := observeReplay(t, observer, job)
				updates := map[string]any{"recovery_claim": strings.Repeat("f", 64), "artifact_byte_size": int64(129), "decided_at": req.Now.Add(time.Second)}
				var guardErr error
				// Commit even after the helper rejects, proving the guard itself
				// prevents mutation rather than relying on caller rollback.
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
					locked, err := lockFinalization(tx, foundationRequest(before.Task))
					if err != nil {
						return err
					}
					guardErr = updateLifecycle(tx, locked, updates, req.Now.Add(2*time.Second))
					return nil
				}))
				require.ErrorIs(t, guardErr, errFinalizationTerminal)
				require.NotContains(t, updates, "revision")
				require.NotContains(t, updates, "updated_at")
				require.Equal(t, before, observeReplay(t, observer, job))
				used, logs, reservation := 0, 0, "released"
				if state == "succeeded" {
					used, logs, reservation = 7, 1, "committed"
				}
				assertFoundation(t, observer, job, state, reservation, used, 50, logs, 5)
			})
		}
	})

	// Original case F retains owner/reservation/claim/artifact and terminal
	// rejection tests. Add the missing replay-specific state/revision/time and
	// reservation-status boundaries, rather than duplicating that matrix.
	t.Run("additional_replay_bindings", func(t *testing.T) {
		for _, scenario := range []string{"wrong_task", "wrong_state", "stale_revision", "future_revision", "zero_time", "released_reservation", "reserved_reservation", "unknown_reservation"} {
			t.Run(scenario, func(t *testing.T) {
				db, observer := foundationDB(t)
				job, _ := foundationReady(t, db, 0)
				req := foundationRequest(job)
				r := finalizationRepository{}
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, req) }))
				wrong := req
				wantErr := errFinalizationBinding
				resState := "committed"
				switch scenario {
				case "wrong_task":
					other, _ := foundationReady(t, db, 0)
					otherBefore := observeReplay(t, observer, other)
					t.Cleanup(func() { require.Equal(t, otherBefore, observeReplay(t, observer, other)) })
					wrong.TaskID = other.TaskID
				case "wrong_state":
					wrong.ExpectedState = "succeeded"
					wantErr = errFinalizationRevision
				case "stale_revision":
					wrong.ExpectedRevision = 3
					wantErr = errFinalizationRevision
				case "future_revision":
					wrong.ExpectedRevision = 6
					wantErr = errFinalizationRevision
				case "zero_time":
					wrong.Now = time.Time{}
				default:
					// Unsupported privileged SQL corruption is not a release API.
					resState = strings.TrimSuffix(scenario, "_reservation")
					require.NoError(t, db.Model(&config.BillingReservation{}).Where("id = ?", job.ReservationID).Update("status", resState).Error)
					wantErr = billing.ErrReservationState
				}
				before := observeReplay(t, observer, job)
				require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, wrong) }), wantErr)
				require.Equal(t, before, observeReplay(t, observer, job))
				assertFoundation(t, observer, job, "succeeded", resState, 7, 50, 1, 5)
			})
		}
	})

	t.Run("maintenance_replay_lock_barrier", func(t *testing.T) {
		for _, rollback := range []bool{false, true} {
			t.Run(map[bool]string{false: "maintenance_commits", true: "maintenance_rolls_back"}[rollback], func(t *testing.T) {
				db, observer := foundationDB(t)
				job, _ := foundationReady(t, db, 0)
				req := foundationRequest(job)
				r := finalizationRepository{}
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, req) }))
				before := observeReplay(t, observer, job)
				maintenance := db.Begin()
				require.NoError(t, maintenance.Error)
				defer maintenance.Rollback()
				var maintenancePID int
				require.NoError(t, maintenance.Raw("SELECT pg_backend_pid()").Scan(&maintenancePID).Error)
				require.NoError(t, maintenance.Model(&ocrFinalization{}).Where("task_id = ?", job.TaskID).Updates(map[string]any{
					"updated_at": req.Now.Add(time.Minute), "recovery_claim": strings.Repeat("e", 64),
					"recovery_lease_until": req.Now.Add(5 * time.Minute),
				}).Error)
				// Expected committed state comes from SQL, before replay runs.
				maintained := observeReplay(t, maintenance, job)
				require.Equal(t, before, observeReplay(t, observer, job), "uncommitted maintenance must be invisible")
				pid, result := make(chan int, 1), make(chan error, 1)
				go func() {
					result <- db.Transaction(func(tx *gorm.DB) error {
						var id int
						if err := tx.Raw("SELECT pg_backend_pid()").Scan(&id).Error; err != nil {
							return err
						}
						pid <- id
						return r.succeedInTx(tx, req)
					})
				}()
				select {
				case id := <-pid:
					waitForDatabaseLock(t, observer, id)
					var blockedByMaintenance bool
					require.NoError(t, observer.Raw("SELECT ? = ANY(pg_blocking_pids(?))", maintenancePID, id).Scan(&blockedByMaintenance).Error)
					require.True(t, blockedByMaintenance)
				case <-time.After(5 * time.Second):
					t.Fatal("replay did not reach row-lock barrier")
				}
				if rollback {
					require.NoError(t, maintenance.Rollback().Error)
				} else {
					require.NoError(t, maintenance.Commit().Error)
				}
				require.NoError(t, receiveFoundationResult(t, result))
				if rollback {
					require.Equal(t, before, observeReplay(t, observer, job))
				} else {
					require.Equal(t, maintained, observeReplay(t, observer, job))
				}
				assertFoundation(t, observer, job, "succeeded", "committed", 7, 50, 1, 5)
			})
		}
	})
}

// Full durable snapshots catch any task, recovery, allocation, window-reset or
// usage-row rewrite. Normal calls use the independent observer pool; only the
// barrier test uses its maintenance transaction to capture expected uncommitted
// maintenance values before comparing the final independent observation.
type replayObservation struct {
	Task         ocrFinalization
	Reservation  config.BillingReservation
	Subscription config.Subscription
	Usage        []config.UsageLog
}

func observeReplay(t *testing.T, observer *gorm.DB, job ocrFinalization) replayObservation {
	t.Helper()
	var actual replayObservation
	require.NoError(t, observer.First(&actual.Task, "task_id = ?", job.TaskID).Error)
	require.NoError(t, observer.First(&actual.Reservation, "id = ?", job.ReservationID).Error)
	require.NoError(t, observer.First(&actual.Subscription, "user_id = ?", job.OwnerID).Error)
	require.NoError(t, observer.Where("user_id = ?", job.OwnerID).Order("id").Find(&actual.Usage).Error)
	var count int64
	require.NoError(t, observer.Model(&ocrFinalization{}).Where("owner_id = ?", job.OwnerID).Count(&count).Error)
	require.EqualValues(t, 1, count, "one authoritative task for this synthetic owner")
	require.NoError(t, observer.Model(&config.BillingReservation{}).Where("user_id = ?", job.OwnerID).Count(&count).Error)
	require.EqualValues(t, 1, count, "one exact reservation without duplicate allocation")
	return actual
}
