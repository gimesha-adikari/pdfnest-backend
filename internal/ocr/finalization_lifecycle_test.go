package ocr

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The pre-change characterization (recorded in the task report) rejected T1>T0
// and required a worker claim for cancellation. These tests assert the new
// dormant contract, without claiming any HTTP route has adopted it.
func TestOCRLifecycleProgression(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	db, observer := foundationDB(t)
	job, _, token := foundationAccepted(t, db, 0)
	require.NotEmpty(t, token)
	require.NotEqual(t, token, job.CancellationCapabilityHash)
	assertFoundation(t, observer, job, "accepted", "reserved", 0, 50, 0, 1)
	claimFoundation(t, db, &job)
	assertFoundation(t, observer, job, "processing", "reserved", 0, 50, 0, 2)
	intentFoundation(t, db, &job)
	assertFoundation(t, observer, job, "processing", "reserved", 0, 50, 0, 3)
	req := foundationRequest(job)
	req.Artifact.VerifiedAt = req.Now
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return (finalizationRepository{}).readyInTx(tx, req, metadataOnlyReadiness(req))
	}))
	reloadFoundation(t, observer, &job)
	assertFoundation(t, observer, job, "ready", "reserved", 0, 50, 0, 4)
	require.True(t, job.Artifact.VerifiedAt.After(job.CreatedAt))
	require.True(t, job.ProcessingStartedAt.Before(*job.ArtifactIntentAt))
	require.True(t, job.ArtifactIntentAt.Before(job.Artifact.VerifiedAt))
	req = foundationRequest(job)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return (finalizationRepository{}).succeedInTx(tx, req) }))
	assertFoundation(t, observer, job, "succeeded", "committed", 7, 50, 1, 5)
}

func TestOCRLifecycleReadinessRejections(t *testing.T) {
	for _, scenario := range []string{"before_creation", "before_intent", "zero_verification", "future_verification", "missing_digest", "missing_key", "bad_format", "zero_pages", "zero_size", "bad_validation", "conflicting_identity", "wrong_claim", "stale_revision", "expired_claim", "rollback"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("BILLING_MODE", "normal")
			db, observer := foundationDB(t)
			job, _, _ := foundationAccepted(t, db, 0)
			claimFoundation(t, db, &job)
			intentFoundation(t, db, &job)
			req := foundationRequest(job)
			req.Artifact.VerifiedAt = req.Now
			switch scenario {
			case "before_creation":
				req.Artifact.VerifiedAt = job.CreatedAt.Add(-time.Second)
			case "before_intent":
				req.Artifact.VerifiedAt = job.CreatedAt.Add(time.Second)
			case "zero_verification":
				req.Artifact.VerifiedAt = time.Time{}
			case "future_verification":
				req.Artifact.VerifiedAt = req.Now.Add(time.Second)
			case "missing_digest":
				req.Artifact.SHA256 = ""
			case "missing_key":
				req.Artifact.ArtifactKey = ""
			case "bad_format":
				req.Artifact.ContentType = "text/html"
			case "zero_pages":
				req.Artifact.PageCount = 0
			case "zero_size":
				req.Artifact.ByteSize = 0
			case "bad_validation":
				req.Artifact.ValidationVersion = 2
			case "conflicting_identity":
				req.Artifact.ArtifactID = uuid.NewString()
			case "wrong_claim":
				req.ProcessingClaim = uuid.NewString()
			case "stale_revision":
				req.ExpectedRevision--
			case "expired_claim":
				req.Now = *job.ProcessingLeaseUntil
				req.Artifact.VerifiedAt = req.Now
			}
			err := db.Transaction(func(tx *gorm.DB) error {
				if scenario == "rollback" {
					if err := installDecisionFailure(tx, "ready", "reserved"); err != nil {
						return err
					}
				}
				return (finalizationRepository{}).readyInTx(tx, req, metadataOnlyReadiness(req))
			})
			require.Error(t, err)
			if scenario == "rollback" {
				require.ErrorContains(t, err, "foundation guard after settlement")
			}
			assertFoundation(t, observer, job, "processing", "reserved", 0, 50, 0, 3)
		})
	}
}

func TestOCRLifecycleCancellationAuthority(t *testing.T) {
	for _, stage := range []string{"accepted", "processing", "intent", "ready"} {
		for _, actor := range []string{"owner", "capability", "wrong_owner", "wrong_capability", "unverified_owner", "empty"} {
			t.Run(stage+"/"+actor, func(t *testing.T) {
				t.Setenv("BILLING_MODE", "normal")
				db, observer := foundationDB(t)
				job, _, token := foundationAccepted(t, db, 0)
				if stage != "accepted" {
					claimFoundation(t, db, &job)
				}
				if stage == "intent" || stage == "ready" {
					intentFoundation(t, db, &job)
				}
				if stage == "ready" {
					req := foundationRequest(job)
					req.Artifact.VerifiedAt = req.Now
					require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
						return (finalizationRepository{}).readyInTx(tx, req, metadataOnlyReadiness(req))
					}))
					reloadFoundation(t, db, &job)
				}
				authority := fixtureOwner(job.OwnerID)
				switch actor {
				case "capability":
					authority = capabilityCancellation(token)
				case "wrong_owner":
					authority = fixtureOwner(uuid.NewString())
				case "wrong_capability":
					authority = capabilityCancellation(token[:63] + "!")
				case "unverified_owner":
					authority = cancellationAuthority{owner: job.OwnerID}
				case "empty":
					authority = cancellationAuthority{}
				}
				stale := foundationRequest(job)
				req := cancellationRequest{TaskID: job.TaskID, ExpectedState: job.State, ExpectedRevision: job.Revision, Now: stale.Now, Authority: authority}
				r := finalizationRepository{}
				err := db.Transaction(func(tx *gorm.DB) error { return r.cancelInTx(tx, req) })
				if actor == "owner" || actor == "capability" {
					require.NoError(t, err)
					assertFoundation(t, observer, job, "cancelled", "released", 0, 50, 0, job.Revision+1)
					var cancelled ocrFinalization
					require.NoError(t, observer.First(&cancelled, "task_id = ?", job.TaskID).Error)
					require.Empty(t, cancelled.ProcessingClaim)
					require.Nil(t, cancelled.ProcessingLeaseUntil)
					require.Error(t, db.Transaction(func(tx *gorm.DB) error { _, err := r.claimInTx(tx, stale, stale.Now.Add(time.Minute)); return err }))
					require.Error(t, db.Transaction(func(tx *gorm.DB) error { return r.recordIntentInTx(tx, stale) }))
					require.Error(t, db.Transaction(func(tx *gorm.DB) error { return r.readyInTx(tx, stale, metadataOnlyReadiness(stale)) }))
					require.Error(t, db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, stale) }))
					assertFoundation(t, observer, job, "cancelled", "released", 0, 50, 0, job.Revision+1)
				} else {
					require.ErrorIs(t, err, errFinalizationBinding)
					assertFoundation(t, observer, job, job.State, "reserved", 0, 50, 0, job.Revision)
				}
			})
		}
	}
	t.Run("verifier_errors", func(t *testing.T) {
		_, err := ownerCancellation(context.Background(), nil)
		require.Error(t, err)
		_, err = ownerCancellation(context.Background(), fixtureIdentity{})
		require.Error(t, err)
		sentinel := errors.New("synthetic authentication rejection")
		_, err = ownerCancellation(context.Background(), fixtureIdentity{owner: uuid.NewString(), err: sentinel})
		require.ErrorIs(t, err, sentinel)
	})
}

func TestOCRLifecycleIntentAndClaimRejections(t *testing.T) {
	for _, scenario := range []string{"unclaimed", "stale_claim", "stale_revision", "expired_claim", "missing_digest", "missing_key", "zero_pages", "bad_validation", "already_verified", "replaced_intent", "ready_without_intent"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("BILLING_MODE", "normal")
			db, observer := foundationDB(t)
			job, _, _ := foundationAccepted(t, db, 0)
			if scenario != "unclaimed" {
				claimFoundation(t, db, &job)
			}
			if scenario == "replaced_intent" {
				intentFoundation(t, db, &job)
			}
			req := foundationRequest(job)
			source := job
			if source.ProcessingClaim == "" {
				source.ProcessingClaim = strings.Repeat("a", 64)
			}
			req.Artifact = syntheticIntent(source)
			switch scenario {
			case "stale_claim":
				req.ProcessingClaim = uuid.NewString()
			case "stale_revision":
				req.ExpectedRevision--
			case "expired_claim":
				req.Now = *job.ProcessingLeaseUntil
			case "missing_digest":
				req.Artifact.SHA256 = ""
			case "missing_key":
				req.Artifact.ArtifactKey = ""
			case "zero_pages":
				req.Artifact.PageCount = 0
			case "bad_validation":
				req.Artifact.ValidationVersion = 2
			case "already_verified":
				req.Artifact.VerifiedAt = req.Now
			}
			require.Error(t, db.Transaction(func(tx *gorm.DB) error {
				if scenario == "ready_without_intent" {
					req.Artifact.VerifiedAt = req.Now
					return (finalizationRepository{}).readyInTx(tx, req, metadataOnlyReadiness(req))
				}
				return (finalizationRepository{}).recordIntentInTx(tx, req)
			}))
			assertFoundation(t, observer, job, job.State, "reserved", 0, 50, 0, job.Revision)
		})
	}
}

func TestOCRLifecycleCompetingClaims(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "first_commits", true: "first_rolls_back"}[rollback], func(t *testing.T) {
			t.Setenv("BILLING_MODE", "normal")
			db, observer := foundationDB(t)
			job, _, _ := foundationAccepted(t, db, 0)
			req := foundationRequest(job)
			until := job.CreatedAt.Add(time.Minute)
			r := finalizationRepository{}
			first := db.Begin()
			defer first.Rollback()
			claimA, err := r.claimInTx(first, req, until)
			require.NoError(t, err)
			assertFoundation(t, observer, job, "accepted", "reserved", 0, 50, 0, 1)
			pid := make(chan int, 1)
			result := make(chan error, 1)
			claimB := make(chan string, 1)
			go func() {
				result <- db.Transaction(func(tx *gorm.DB) error {
					var id int
					if err := tx.Raw("SELECT pg_backend_pid()").Scan(&id).Error; err != nil {
						return err
					}
					pid <- id
					claim, err := r.claimInTx(tx, req, until)
					claimB <- claim
					return err
				})
			}()
			select {
			case id := <-pid:
				waitForDatabaseLock(t, observer, id)
			case <-time.After(5 * time.Second):
				t.Fatal("claim writer did not reach barrier")
			}
			if rollback {
				require.NoError(t, first.Rollback().Error)
			} else {
				require.NoError(t, first.Commit().Error)
			}
			err = receiveFoundationResult(t, result)
			if rollback {
				require.NoError(t, err)
				require.NotEqual(t, claimA, <-claimB)
			} else {
				require.ErrorIs(t, err, errFinalizationRevision)
				require.Empty(t, <-claimB)
			}
			reloadFoundation(t, observer, &job)
			assertFoundation(t, observer, job, "processing", "reserved", 0, 50, 0, 2)
			if !rollback {
				require.Equal(t, claimA, job.ProcessingClaim)
			}
			// Passing time does not grant an accepted-state retry or a new claim.
			req.Now = until.Add(time.Second)
			require.Error(t, db.Transaction(func(tx *gorm.DB) error { _, err := r.claimInTx(tx, req, req.Now.Add(time.Second)); return err }))
			assertFoundation(t, observer, job, "processing", "reserved", 0, 50, 0, 2)
		})
	}
}

func TestOCRLifecycleReadinessCancellationLocks(t *testing.T) {
	for _, scenario := range []string{"ready_then_cancel", "cancel_then_ready", "ready_rollback_then_cancel"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("BILLING_MODE", "normal")
			db, observer := foundationDB(t)
			job, _, token := foundationAccepted(t, db, 0)
			claimFoundation(t, db, &job)
			intentFoundation(t, db, &job)
			req := foundationRequest(job)
			req.Artifact.VerifiedAt = req.Now
			cancel := cancellationRequest{TaskID: job.TaskID, ExpectedState: job.State, ExpectedRevision: job.Revision, Now: req.Now, Authority: capabilityCancellation(token)}
			r := finalizationRepository{}
			first := db.Begin()
			defer first.Rollback()
			if scenario == "cancel_then_ready" {
				require.NoError(t, r.cancelInTx(first, cancel))
			} else {
				require.NoError(t, r.readyInTx(first, req, metadataOnlyReadiness(req)))
			}
			assertFoundation(t, observer, job, "processing", "reserved", 0, 50, 0, 3)
			pid := make(chan int, 1)
			result := make(chan error, 1)
			go func() {
				result <- db.Transaction(func(tx *gorm.DB) error {
					var id int
					if err := tx.Raw("SELECT pg_backend_pid()").Scan(&id).Error; err != nil {
						return err
					}
					pid <- id
					if scenario == "cancel_then_ready" {
						return r.readyInTx(tx, req, metadataOnlyReadiness(req))
					}
					return r.cancelInTx(tx, cancel)
				})
			}()
			select {
			case id := <-pid:
				waitForDatabaseLock(t, observer, id)
			case <-time.After(5 * time.Second):
				t.Fatal("lifecycle writer did not reach barrier")
			}
			if scenario == "ready_rollback_then_cancel" {
				require.NoError(t, first.Rollback().Error)
			} else {
				require.NoError(t, first.Commit().Error)
			}
			err := receiveFoundationResult(t, result)
			switch scenario {
			case "ready_then_cancel":
				require.ErrorIs(t, err, errFinalizationRevision)
				reloadFoundation(t, observer, &job)
				assertFoundation(t, observer, job, "ready", "reserved", 0, 50, 0, 4)
				cancel.ExpectedState, cancel.ExpectedRevision = job.State, job.Revision
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.cancelInTx(tx, cancel) }))
				assertFoundation(t, observer, job, "cancelled", "released", 0, 50, 0, 5)
			case "cancel_then_ready":
				require.ErrorIs(t, err, errFinalizationTerminal)
				assertFoundation(t, observer, job, "cancelled", "released", 0, 50, 0, 4)
			default:
				require.NoError(t, err)
				assertFoundation(t, observer, job, "cancelled", "released", 0, 50, 0, 4)
			}
		})
	}
}

type fixtureDeadline struct {
	task string
	err  error
}

func (v fixtureDeadline) VerifyDeadlineTask(context.Context) (string, error) { return v.task, v.err }
func TestOCRLifecycleFailureAuthorities(t *testing.T) {
	for _, scenario := range []string{"worker_processing", "worker_intent", "worker_wrong_claim", "worker_bad_code", "deadline_accepted", "deadline_processing", "deadline_early", "deadline_wrong_task", "deadline_unverified"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("BILLING_MODE", "normal")
			db, observer := foundationDB(t)
			job, _, _ := foundationAccepted(t, db, 0)
			if scenario != "deadline_accepted" {
				claimFoundation(t, db, &job)
			}
			if scenario == "worker_intent" {
				intentFoundation(t, db, &job)
			}
			r := finalizationRepository{}
			req := foundationRequest(job)
			wantSuccess := scenario == "worker_processing" || scenario == "worker_intent" || scenario == "deadline_accepted" || scenario == "deadline_processing"
			err := db.Transaction(func(tx *gorm.DB) error {
				switch scenario {
				case "worker_processing", "worker_intent":
					return r.failWorkerInTx(tx, req, "PROCESSING_FAILED")
				case "worker_wrong_claim":
					req.ProcessingClaim = uuid.NewString()
					return r.failWorkerInTx(tx, req, "PROCESSING_FAILED")
				case "worker_bad_code":
					return r.failWorkerInTx(tx, req, "unsafe error text")
				}
				authority, err := trustedDeadline(context.Background(), fixtureDeadline{task: job.TaskID})
				if err != nil {
					return err
				}
				if scenario == "deadline_accepted" {
					var expires time.Time
					if err := tx.Raw("SELECT expires_at FROM billing_reservations WHERE id = ?", job.ReservationID).Scan(&expires).Error; err != nil {
						return err
					}
					req.Now = expires
				}
				if scenario == "deadline_processing" {
					req.Now = *job.ProcessingLeaseUntil
					require.Error(t, r.failWorkerInTx(tx, req, "PROCESSING_FAILED"))
				}
				if scenario == "deadline_wrong_task" {
					authority.taskID = uuid.NewString()
				}
				if scenario == "deadline_unverified" {
					authority.verified = false
				}
				return r.failDeadlineInTx(tx, cancellationRequest{TaskID: job.TaskID, ExpectedState: job.State, ExpectedRevision: job.Revision, Now: req.Now}, authority)
			})
			if wantSuccess {
				require.NoError(t, err)
				assertFoundation(t, observer, job, "failed", "released", 0, 50, 0, job.Revision+1)
				var row ocrFinalization
				require.NoError(t, observer.First(&row, "task_id = ?", job.TaskID).Error)
				require.Empty(t, row.ProcessingClaim)
				require.Nil(t, row.ProcessingLeaseUntil)
			} else {
				require.Error(t, err)
				assertFoundation(t, observer, job, job.State, "reserved", 0, 50, 0, job.Revision)
			}
		})
	}
	_, err := trustedDeadline(context.Background(), nil)
	require.Error(t, err)
	_, err = trustedDeadline(context.Background(), fixtureDeadline{})
	require.Error(t, err)
	_, err = trustedDeadline(context.Background(), fixtureDeadline{err: errors.New("synthetic rejection")})
	require.Error(t, err)
}

func TestOCRLifecycleCapabilityIsolation(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	db, observer := foundationDB(t)
	a, _, tokenA := foundationAccepted(t, db, 0)
	b, _, tokenB := foundationAccepted(t, db, 0)
	require.NotEqual(t, tokenA, tokenB)
	req := cancellationRequest{TaskID: b.TaskID, ExpectedState: b.State, ExpectedRevision: b.Revision, Now: b.CreatedAt.Add(time.Second), Authority: capabilityCancellation(tokenA)}
	require.Error(t, db.Transaction(func(tx *gorm.DB) error { return (finalizationRepository{}).cancelInTx(tx, req) }))
	assertFoundation(t, observer, a, "accepted", "reserved", 0, 50, 0, 1)
	assertFoundation(t, observer, b, "accepted", "reserved", 0, 50, 0, 1)
}

func TestOCRLifecycleSQLConstraints(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	db, observer := foundationDB(t)
	job, _ := foundationReady(t, db, 0)
	for _, update := range []map[string]any{
		{"artifact_intent_at": nil}, {"processing_started_at": nil}, {"ready_at": nil}, {"processing_lease_until": nil}, {"processing_claim": nil}, {"processing_claim": ""},
		{"artifact_verified_at": nil}, {"artifact_byte_size": nil}, {"artifact_page_count": nil}, {"artifact_content_type": nil}, {"artifact_validation_version": nil},
		{"cancellation_capability_hash": nil}, {"cancellation_capability_hash": ""}, {"artifact_verified_at": job.CreatedAt.Add(-time.Second)},
		{"artifact_verified_at": job.CreatedAt}, {"ready_at": job.ArtifactIntentAt}, {"decided_at": job.CreatedAt}, {"state": "cancelled"}, {"state": "processing"},
		{"updated_at": job.CreatedAt}, {"state": "invalid"},
	} {
		require.Error(t, db.Model(&ocrFinalization{}).Where("task_id = ?", job.TaskID).Updates(update).Error, "invalid SQL update: %v", update)
		assertFoundation(t, observer, job, "ready", "reserved", 0, 50, 0, 4)
	}
	other, _ := foundationReady(t, db, 0)
	require.Error(t, db.Model(&ocrFinalization{}).Where("task_id = ?", other.TaskID).Update("artifact_key", job.Artifact.ArtifactKey).Error)
	assertFoundation(t, observer, other, "ready", "reserved", 0, 50, 0, 4)
}

func TestOCRLifecycleAcceptanceAndLease(t *testing.T) {
	for _, scenario := range []string{"direct_processing", "direct_ready_later_verification", "direct_ready_equal_verification", "zero_created", "forged_claim", "prepopulated_intent", "future_placeholder", "reservation_expired", "duplicate_task", "lease_zero", "lease_after_reservation", "wrong_owner", "wrong_reservation"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("BILLING_MODE", "normal")
			db, observer := foundationDB(t)
			job, _, _ := foundationAccepted(t, db, 0)
			candidate := job
			candidate.CancellationCapabilityHash = ""
			r := finalizationRepository{}
			err := db.Transaction(func(tx *gorm.DB) error {
				switch scenario {
				case "direct_processing":
					candidate.State = "processing"
				case "direct_ready_later_verification", "direct_ready_equal_verification":
					candidate.State = "ready"
					candidate.Artifact = artifactDescriptor{ArtifactID: uuid.NewString(), ArtifactKey: "rejected-shortcut.pdf", SHA256: strings.Repeat("a", 64), ByteSize: 128, PageCount: 1, ContentType: "application/pdf", ValidationVersion: 1}
					candidate.Artifact.VerifiedAt = candidate.CreatedAt
					if scenario == "direct_ready_later_verification" {
						candidate.Artifact.VerifiedAt = candidate.CreatedAt.Add(time.Second)
					}
				case "zero_created":
					candidate.CreatedAt = time.Time{}
				case "forged_claim":
					candidate.ProcessingClaim = uuid.NewString()
				case "prepopulated_intent":
					candidate.Artifact = artifactDescriptor{ArtifactID: uuid.NewString(), ArtifactKey: "rejected-shortcut.pdf", SHA256: strings.Repeat("a", 64), ByteSize: 128, PageCount: 1, ContentType: "application/pdf", ValidationVersion: 1}
				case "future_placeholder":
					candidate.NextAttemptAt = &candidate.CreatedAt
				case "reservation_expired":
					candidate.CreatedAt = candidate.CreatedAt.Add(time.Hour)
				case "lease_zero":
					_, err := r.claimInTx(tx, foundationRequest(job), job.CreatedAt)
					return err
				case "lease_after_reservation":
					_, err := r.claimInTx(tx, foundationRequest(job), job.CreatedAt.Add(time.Hour))
					return err
				case "wrong_owner":
					candidate.OwnerID = uuid.NewString()
				case "wrong_reservation":
					candidate.ReservationID = uuid.NewString()
				}
				_, err := r.createInTx(tx, &candidate)
				return err
			})
			require.Error(t, err)
			assertFoundation(t, observer, job, "accepted", "reserved", 0, 50, 0, 1)
		})
	}
}
