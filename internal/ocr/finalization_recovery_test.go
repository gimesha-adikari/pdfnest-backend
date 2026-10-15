package ocr

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/billing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Synthetic trusted adapter only; no production caller or scheduler exists.
type fixtureRecoveryVerifier struct {
	task string
	err  error
}

func (v fixtureRecoveryVerifier) VerifyRecoveryTask(context.Context) (string, error) {
	return v.task, v.err
}

func recoveryRequestFor(t *testing.T, job ocrFinalization) recoveryRequest {
	t.Helper()
	authority, err := trustedRecovery(context.Background(), fixtureRecoveryVerifier{task: job.TaskID})
	require.NoError(t, err)
	return recoveryRequest{TaskID: job.TaskID, OwnerID: job.OwnerID, ReservationID: job.ReservationID,
		ExpectedState: job.State, ExpectedRevision: job.Revision, Now: job.UpdatedAt, Authority: authority}
}

func recoveryTerminal(t *testing.T, db *gorm.DB, state string, used int) (ocrFinalization, finalizationRequest, recoveryRequest) {
	t.Helper()
	// Recovery needs real database expiry, not the shared fixtures' seconds of
	// future lifecycle time. Exercise the same repository transitions using
	// explicit microsecond logical steps; do not change the shared fixtures.
	job, _, _ := foundationAccepted(t, db, used)
	r := finalizationRepository{}
	for _, stage := range []string{"claim", "intent", "ready"} {
		req := foundationRequest(job)
		req.Now = job.UpdatedAt.Add(time.Microsecond)
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			switch stage {
			case "claim":
				_, err := r.claimInTx(tx, req, job.CreatedAt.Add(time.Minute))
				return err
			case "intent":
				req.Artifact = syntheticIntent(job)
				return r.recordIntentInTx(tx, req)
			default:
				req.Artifact.VerifiedAt = req.Now
				return r.readyInTx(tx, req, metadataOnlyReadiness(req))
			}
		}))
		reloadFoundation(t, db, &job)
	}
	success := foundationRequest(job)
	success.Now = job.UpdatedAt.Add(time.Microsecond)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if state == "succeeded" {
			return (finalizationRepository{}).succeedInTx(tx, success)
		}
		return endFoundation(tx, success, state, "CONTROLLED_STOP")
	}))
	reloadFoundation(t, db, &job)
	require.True(t, recoveryDatabaseNow(t, db).After(job.UpdatedAt), "logical lifecycle must precede real recovery eligibility")
	req := recoveryRequestFor(t, job)
	// Explicit synthetic obligation, not a production scheduling/retention rule.
	require.NoError(t, db.Model(&ocrFinalization{}).Where("task_id = ?", job.TaskID).UpdateColumns(map[string]any{
		"next_attempt_at": req.Now, "cleanup_not_before": req.Now.Add(24 * time.Hour), "download_expires_at": req.Now.Add(time.Hour),
	}).Error)
	return job, success, req
}

func recoveryDatabaseNow(t *testing.T, db *gorm.DB) time.Time {
	t.Helper()
	var now time.Time
	require.NoError(t, db.Raw("SELECT clock_timestamp()").Scan(&now).Error)
	return now
}

// Controlled fixture expiration changes no clocks, lifecycle fields or tokens.
func expireRecoveryLease(t *testing.T, db *gorm.DB, job ocrFinalization) time.Time {
	t.Helper()
	var expired time.Time
	require.NoError(t, db.Raw("UPDATE ocr_finalizations SET recovery_lease_until = clock_timestamp() - interval '1 microsecond' WHERE task_id = ? RETURNING recovery_lease_until", job.TaskID).Scan(&expired).Error)
	return expired
}

func acquireRecovery(t *testing.T, db *gorm.DB, req recoveryRequest, until time.Time) string {
	t.Helper()
	var token string
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var err error
		token, err = (finalizationRepository{}).acquireRecoveryInTx(tx, req, until)
		return err
	}))
	decoded, err := hex.DecodeString(token)
	require.NoError(t, err)
	require.Len(t, decoded, 32, "256-bit cryptographically generated claim")
	return token
}

func assertRecoveryOwnership(t *testing.T, observer *gorm.DB, job ocrFinalization, before replayObservation, token string, until *time.Time) {
	t.Helper()
	actual := observeReplay(t, observer, job)
	require.Equal(t, token, actual.Task.RecoveryClaim)
	assertOptionalTime(t, until, actual.Task.RecoveryLeaseUntil)
	// Exactly these two fields may differ. Compare the complete terminal,
	// scheduling, retention, allocation, subscription and usage snapshots.
	actual.Task.RecoveryClaim = before.Task.RecoveryClaim
	actual.Task.RecoveryLeaseUntil = before.Task.RecoveryLeaseUntil
	require.Equal(t, before, actual)
}

func TestOCRRecoveryClaims(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	r := finalizationRepository{}
	t.Run("acquire_renew_relinquish_and_replay", func(t *testing.T) {
		for _, fixture := range []struct {
			name, state                  string
			initial, used, credits, logs int
		}{
			{"plan", "succeeded", 0, 7, 50, 1},
			{"credits", "succeeded", 8, 8, 43, 1},
			{"mixed", "succeeded", 5, 8, 46, 1},
			{"cancelled", "cancelled", 0, 0, 50, 0},
			{"failed", "failed", 0, 0, 50, 0},
		} {
			t.Run(fixture.name, func(t *testing.T) {
				db, observer := foundationDB(t)
				job, success, req := recoveryTerminal(t, db, fixture.state, fixture.initial)
				before := observeReplay(t, observer, job)
				until := req.Now.Add(time.Minute)
				token := acquireRecovery(t, db, req, until)
				assertRecoveryOwnership(t, observer, job, before, token, &until)
				renewed := until.Add(time.Minute)
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.renewRecoveryInTx(tx, req, token, renewed) }))
				assertRecoveryOwnership(t, observer, job, before, token, &renewed)
				if fixture.state == "succeeded" {
					require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, success) }))
					assertRecoveryOwnership(t, observer, job, before, token, &renewed)
				}
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.relinquishRecoveryInTx(tx, req, token) }))
				assertRecoveryOwnership(t, observer, job, before, "", nil)
				require.Equal(t, before, observeReplay(t, observer, job), "relinquishment leaves the obligation due")
				if fixture.state == "succeeded" {
					require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, success) }))
				}
				reservation := "released"
				if fixture.state == "succeeded" {
					reservation = "committed"
				}
				assertFoundation(t, observer, job, fixture.state, reservation, fixture.used, fixture.credits, fixture.logs, 5)
			})
		}
	})

	t.Run("rollback_discards_returned_authority", func(t *testing.T) {
		db, observer := foundationDB(t)
		job, _, req := recoveryTerminal(t, db, "succeeded", 0)
		before := observeReplay(t, observer, job)
		until := req.Now.Add(time.Minute)
		tx := db.Begin()
		require.NoError(t, tx.Error)
		defer tx.Rollback()
		uncommitted, err := r.acquireRecoveryInTx(tx, req, until)
		require.NoError(t, err)
		require.NotEmpty(t, uncommitted)
		require.Equal(t, before, observeReplay(t, observer, job))
		require.NoError(t, tx.Rollback().Error)
		require.Equal(t, before, observeReplay(t, observer, job))
		require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return r.renewRecoveryInTx(tx, req, uncommitted, until.Add(time.Minute)) }), errRecoveryClaim)
		require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return r.relinquishRecoveryInTx(tx, req, uncommitted) }), errRecoveryClaim)
		token := acquireRecovery(t, db, req, until)
		require.NotEqual(t, uncommitted, token)
		assertRecoveryOwnership(t, observer, job, before, token, &until)
	})

	t.Run("expiry_takeover_fences_old_holder", func(t *testing.T) {
		db, observer := foundationDB(t)
		job, success, req := recoveryTerminal(t, db, "succeeded", 5)
		before := observeReplay(t, observer, job)
		until := req.Now.Add(time.Minute)
		old := acquireRecovery(t, db, req, until)
		expired := expireRecoveryLease(t, db, job)
		require.True(t, req.Now.Before(expired))
		until = recoveryDatabaseNow(t, db).Add(time.Minute)
		require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return r.renewRecoveryInTx(tx, req, old, until.Add(time.Minute)) }), errRecoveryClaim)
		require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return r.relinquishRecoveryInTx(tx, req, old) }), errRecoveryClaim)
		current := acquireRecovery(t, db, req, until)
		require.NotEqual(t, old, current)
		assertRecoveryOwnership(t, observer, job, before, current, &until)
		// The stale request time predates old expiry: rejection must be from
		// superseded ownership, not merely the old holder's expired timestamp.
		stale := req
		require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return r.renewRecoveryInTx(tx, stale, old, until.Add(time.Minute)) }), errRecoveryClaim)
		require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return r.relinquishRecoveryInTx(tx, stale, old) }), errRecoveryClaim)
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, success) }))
		assertRecoveryOwnership(t, observer, job, before, current, &until)
		renewed := until.Add(time.Minute)
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.renewRecoveryInTx(tx, req, current, renewed) }))
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.relinquishRecoveryInTx(tx, req, current) }))
		require.Equal(t, before, observeReplay(t, observer, job))
		assertFoundation(t, observer, job, "succeeded", "committed", 8, 46, 1, 5)
	})

	t.Run("cross_task_tokens", func(t *testing.T) {
		db, observer := foundationDB(t)
		a, _, reqA := recoveryTerminal(t, db, "succeeded", 0)
		b, _, reqB := recoveryTerminal(t, db, "succeeded", 0)
		untilA, untilB := reqA.Now.Add(time.Minute), reqB.Now.Add(time.Minute)
		tokenA, tokenB := acquireRecovery(t, db, reqA, untilA), acquireRecovery(t, db, reqB, untilB)
		require.NotEqual(t, tokenA, tokenB)
		beforeA, beforeB := observeReplay(t, observer, a), observeReplay(t, observer, b)
		// Each request has correct authority/identity for its own task. Knowing
		// another task's valid token must still grant no recovery ownership.
		require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return r.renewRecoveryInTx(tx, reqB, tokenA, untilB.Add(time.Minute)) }), errRecoveryClaim)
		require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return r.relinquishRecoveryInTx(tx, reqB, tokenA) }), errRecoveryClaim)
		require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return r.renewRecoveryInTx(tx, reqA, tokenB, untilA.Add(time.Minute)) }), errRecoveryClaim)
		require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return r.relinquishRecoveryInTx(tx, reqA, tokenB) }), errRecoveryClaim)
		require.Equal(t, beforeA, observeReplay(t, observer, a))
		require.Equal(t, beforeB, observeReplay(t, observer, b))
	})

	t.Run("renewal_and_relinquishment_rejections", func(t *testing.T) {
		for _, scenario := range []string{"wrong_claim", "empty_claim", "expired", "equal_deadline", "earlier_deadline", "zero_deadline", "rounded_deadline"} {
			t.Run(scenario, func(t *testing.T) {
				db, observer := foundationDB(t)
				job, _, req := recoveryTerminal(t, db, "succeeded", 0)
				until := req.Now.Add(time.Minute)
				token := acquireRecovery(t, db, req, until)
				next := until.Add(time.Minute)
				switch scenario {
				case "wrong_claim":
					token = uuid.NewString()
				case "empty_claim":
					token = ""
				case "expired":
					expireRecoveryLease(t, db, job)
				case "equal_deadline":
					next = until
				case "earlier_deadline":
					next = until.Add(-time.Second)
				case "zero_deadline":
					next = time.Time{}
				case "rounded_deadline":
					next = until.Add(time.Nanosecond)
				}
				before := observeReplay(t, observer, job)
				require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return r.renewRecoveryInTx(tx, req, token, next) }), errRecoveryClaim)
				if scenario == "wrong_claim" || scenario == "empty_claim" || scenario == "expired" {
					require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return r.relinquishRecoveryInTx(tx, req, token) }), errRecoveryClaim)
				}
				require.Equal(t, before, observeReplay(t, observer, job))
			})
		}
	})

	t.Run("authority_and_binding_rejections", func(t *testing.T) {
		for _, operation := range []string{"acquire", "renew", "relinquish"} {
			t.Run(operation, func(t *testing.T) {
				for _, scenario := range []string{"missing_authority", "unverified", "other_task_authority", "wrong_task", "wrong_owner", "wrong_reservation", "stale_state", "stale_revision", "zero_now", "before_lifecycle", "reservation_task_mismatch", "reservation_status_mismatch"} {
					t.Run(scenario, func(t *testing.T) {
						db, observer := foundationDB(t)
						job, _, req := recoveryTerminal(t, db, "succeeded", 0)
						other, _, otherReq := recoveryTerminal(t, db, "succeeded", 0)
						until := req.Now.Add(time.Minute)
						token := ""
						if operation != "acquire" {
							token = acquireRecovery(t, db, req, until)
						}
						switch scenario {
						case "missing_authority":
							req.Authority = recoveryAuthority{}
						case "unverified":
							req.Authority.verified = false
						case "other_task_authority":
							req.Authority = otherReq.Authority
						case "wrong_task":
							req.TaskID = other.TaskID
						case "wrong_owner":
							req.OwnerID = other.OwnerID
						case "wrong_reservation":
							req.ReservationID = other.ReservationID
						case "stale_state":
							req.ExpectedState = "ready"
						case "stale_revision":
							req.ExpectedRevision--
						case "zero_now":
							req.Now = time.Time{}
						case "before_lifecycle":
							req.Now = job.UpdatedAt.Add(-time.Microsecond)
						case "reservation_task_mismatch":
							require.NoError(t, db.Model(&config.BillingReservation{}).Where("id = ?", job.ReservationID).UpdateColumn("task_id", other.TaskID).Error)
						case "reservation_status_mismatch":
							require.NoError(t, db.Model(&config.BillingReservation{}).Where("id = ?", job.ReservationID).UpdateColumn("status", "released").Error)
						}
						before, otherBefore := observeReplay(t, observer, job), observeReplay(t, observer, other)
						err := db.Transaction(func(tx *gorm.DB) error {
							switch operation {
							case "acquire":
								_, err := r.acquireRecoveryInTx(tx, req, until)
								return err
							case "renew":
								return r.renewRecoveryInTx(tx, req, token, until.Add(time.Minute))
							default:
								return r.relinquishRecoveryInTx(tx, req, token)
							}
						})
						require.Error(t, err)
						require.Equal(t, before, observeReplay(t, observer, job))
						require.Equal(t, otherBefore, observeReplay(t, observer, other))
					})
				}
			})
		}
	})

	t.Run("acquisition_eligibility", func(t *testing.T) {
		for _, scenario := range []string{"unscheduled", "future", "accepted", "processing", "ready", "deadline_now", "deadline_before", "deadline_zero", "rounded_deadline", "active_claim", "claim_without_lease", "lease_without_claim"} {
			t.Run(scenario, func(t *testing.T) {
				db, observer := foundationDB(t)
				var job ocrFinalization
				var req recoveryRequest
				if scenario == "accepted" || scenario == "processing" || scenario == "ready" {
					job, _, _ = foundationAccepted(t, db, 0)
					if scenario != "accepted" {
						claimFoundation(t, db, &job)
					}
					if scenario == "ready" {
						intentFoundation(t, db, &job)
						ready := foundationRequest(job)
						ready.Artifact.VerifiedAt = ready.Now
						require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.readyInTx(tx, ready, metadataOnlyReadiness(ready)) }))
						reloadFoundation(t, db, &job)
					}
					req = recoveryRequestFor(t, job)
					require.NoError(t, db.Model(&ocrFinalization{}).Where("task_id = ?", job.TaskID).UpdateColumn("next_attempt_at", req.Now).Error)
				} else {
					job, _, req = recoveryTerminal(t, db, "succeeded", 0)
				}
				until := req.Now.Add(time.Minute)
				switch scenario {
				case "unscheduled":
					require.NoError(t, db.Model(&ocrFinalization{}).Where("task_id = ?", job.TaskID).UpdateColumn("next_attempt_at", nil).Error)
				case "future":
					require.NoError(t, db.Model(&ocrFinalization{}).Where("task_id = ?", job.TaskID).UpdateColumn("next_attempt_at", until).Error)
				case "deadline_now":
					until = req.Now
				case "deadline_before":
					until = req.Now.Add(-time.Second)
				case "deadline_zero":
					until = time.Time{}
				case "rounded_deadline":
					until = req.Now.Add(time.Nanosecond)
				case "active_claim":
					acquireRecovery(t, db, req, until)
				case "claim_without_lease":
					require.NoError(t, db.Model(&ocrFinalization{}).Where("task_id = ?", job.TaskID).UpdateColumn("recovery_claim", "inconsistent").Error)
				case "lease_without_claim":
					require.NoError(t, db.Model(&ocrFinalization{}).Where("task_id = ?", job.TaskID).UpdateColumn("recovery_lease_until", until).Error)
				}
				before := observeReplay(t, observer, job)
				err := db.Transaction(func(tx *gorm.DB) error { _, err := r.acquireRecoveryInTx(tx, req, until); return err })
				require.Error(t, err)
				require.Equal(t, before, observeReplay(t, observer, job))
			})
		}
	})
}

func TestOCRRecoveryTransactionAndAuthority(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	for _, verifier := range []recoveryVerifier{nil, fixtureRecoveryVerifier{}} {
		_, err := trustedRecovery(context.Background(), verifier)
		require.ErrorIs(t, err, errFinalizationBinding)
	}
	sentinel := errors.New("synthetic recovery verifier rejection")
	_, err := trustedRecovery(context.Background(), fixtureRecoveryVerifier{err: sentinel})
	require.ErrorIs(t, err, sentinel)
	db, observer := foundationDB(t)
	job, _, req := recoveryTerminal(t, db, "succeeded", 0)
	until := req.Now.Add(time.Minute)
	before := observeReplay(t, observer, job)
	r := finalizationRepository{}
	for _, handle := range []*gorm.DB{nil, db} {
		_, err := r.acquireRecoveryInTx(handle, req, until)
		require.ErrorIs(t, err, billing.ErrTransactionRequired)
		require.ErrorIs(t, r.renewRecoveryInTx(handle, req, "claim", until), billing.ErrTransactionRequired)
		require.ErrorIs(t, r.relinquishRecoveryInTx(handle, req, "claim"), billing.ErrTransactionRequired)
	}
	require.Equal(t, before, observeReplay(t, observer, job))
}

type recoveryResult struct {
	token string
	err   error
}

// The first transaction has already written/locked the task. Prove the second
// operation waits on that exact PostgreSQL backend before releasing the first.
func waitRecoveryWriter(t *testing.T, db, observer, first *gorm.DB, operation func(*gorm.DB) (string, error)) <-chan recoveryResult {
	t.Helper()
	var firstPID int
	require.NoError(t, first.Raw("SELECT pg_backend_pid()").Scan(&firstPID).Error)
	pid := make(chan int, 1)
	result := make(chan recoveryResult, 1)
	go func() {
		var token string
		err := db.Transaction(func(tx *gorm.DB) error {
			var id int
			if err := tx.Raw("SELECT pg_backend_pid()").Scan(&id).Error; err != nil {
				return err
			}
			pid <- id
			var err error
			token, err = operation(tx)
			return err
		})
		result <- recoveryResult{token: token, err: err}
	}()
	select {
	case id := <-pid:
		waitForDatabaseLock(t, observer, id)
		var blocked bool
		require.NoError(t, observer.Raw("SELECT ? = ANY(pg_blocking_pids(?))", firstPID, id).Scan(&blocked).Error)
		require.True(t, blocked, "the recovery owner must block this writer")
	case <-time.After(5 * time.Second):
		t.Fatal("recovery writer did not enter transaction")
	}
	return result
}

func receiveRecovery(t *testing.T, result <-chan recoveryResult) recoveryResult {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(10 * time.Second):
		t.Fatal("recovery writer did not finish")
		return recoveryResult{}
	}
}

func TestOCRRecoveryContention(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	r := finalizationRepository{}
	t.Run("competing_acquisition", func(t *testing.T) {
		for _, rollback := range []bool{false, true} {
			t.Run(map[bool]string{false: "winner_commits", true: "winner_rolls_back"}[rollback], func(t *testing.T) {
				db, observer := foundationDB(t)
				job, _, req := recoveryTerminal(t, db, "succeeded", 0)
				before := observeReplay(t, observer, job)
				until := req.Now.Add(time.Minute)
				first := db.Begin()
				require.NoError(t, first.Error)
				defer first.Rollback()
				tokenA, err := r.acquireRecoveryInTx(first, req, until)
				require.NoError(t, err)
				require.Equal(t, before, observeReplay(t, observer, job), "no partial/uncommitted ownership visible")
				result := waitRecoveryWriter(t, db, observer, first, func(tx *gorm.DB) (string, error) {
					return r.acquireRecoveryInTx(tx, req, until)
				})
				if rollback {
					require.NoError(t, first.Rollback().Error)
				} else {
					require.NoError(t, first.Commit().Error)
				}
				got := receiveRecovery(t, result)
				if rollback {
					require.NoError(t, got.err)
					require.NotEmpty(t, got.token)
					require.NotEqual(t, tokenA, got.token)
					assertRecoveryOwnership(t, observer, job, before, got.token, &until)
					require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return r.renewRecoveryInTx(tx, req, tokenA, until.Add(time.Minute)) }), errRecoveryClaim)
				} else {
					require.ErrorIs(t, got.err, errRecoveryClaim)
					require.Empty(t, got.token)
					assertRecoveryOwnership(t, observer, job, before, tokenA, &until)
				}
			})
		}
	})
	t.Run("stale_holder_waits_on_takeover", func(t *testing.T) {
		for _, operation := range []string{"renew", "relinquish"} {
			for _, rollback := range []bool{false, true} {
				name := operation + map[bool]string{false: "/takeover_commits", true: "/takeover_rolls_back"}[rollback]
				t.Run(name, func(t *testing.T) {
					db, observer := foundationDB(t)
					job, success, req := recoveryTerminal(t, db, "succeeded", 0)
					before := observeReplay(t, observer, job)
					until := req.Now.Add(time.Minute)
					old := acquireRecovery(t, db, req, until)
					oldReq := req
					takeover := req
					next := until.Add(time.Minute)
					first := db.Begin()
					require.NoError(t, first.Error)
					defer first.Rollback()
					// Expiry is controlled SQL fixture state, not request time
					// travel. Rollback restores the genuinely unexpired old lease.
					expired := expireRecoveryLease(t, first, job)
					require.True(t, oldReq.Now.Before(expired))
					newToken, err := r.acquireRecoveryInTx(first, takeover, next)
					require.NoError(t, err)
					require.NotEqual(t, old, newToken)
					assertRecoveryOwnership(t, observer, job, before, old, &until)
					result := waitRecoveryWriter(t, db, observer, first, func(tx *gorm.DB) (string, error) {
						if operation == "renew" {
							return "", r.renewRecoveryInTx(tx, oldReq, old, next)
						}
						return "", r.relinquishRecoveryInTx(tx, oldReq, old)
					})
					if rollback {
						require.NoError(t, first.Rollback().Error)
					} else {
						require.NoError(t, first.Commit().Error)
					}
					got := receiveRecovery(t, result)
					if rollback {
						// Both expiry setup and takeover rolled back: the old lease
						// is still unexpired according to the actual database clock.
						require.NoError(t, got.err)
						if operation == "renew" {
							assertRecoveryOwnership(t, observer, job, before, old, &next)
						} else {
							assertRecoveryOwnership(t, observer, job, before, "", nil)
						}
						require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return r.relinquishRecoveryInTx(tx, takeover, newToken) }), errRecoveryClaim)
					} else {
						require.ErrorIs(t, got.err, errRecoveryClaim)
						assertRecoveryOwnership(t, observer, job, before, newToken, &next)
						require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.renewRecoveryInTx(tx, takeover, newToken, next.Add(time.Minute)) }))
					}
					current := observeReplay(t, observer, job)
					require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, success) }))
					require.Equal(t, current, observeReplay(t, observer, job))
					assertFoundation(t, observer, job, "succeeded", "committed", 7, 50, 1, 5)
				})
			}
		}
	})
}

// Exercise CAS against stale snapshots directly, so removing either the stored
// lease or token predicate cannot be hidden by the public methods' row lock.
func TestOCRRecoveryCAS(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	db, observer := foundationDB(t)
	job, _, req := recoveryTerminal(t, db, "succeeded", 0)
	until := req.Now.Add(time.Minute)
	token := acquireRecovery(t, db, req, until)
	staleLease := observeReplay(t, observer, job).Task
	renewed := until.Add(time.Minute)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return (finalizationRepository{}).renewRecoveryInTx(tx, req, token, renewed) }))
	before := observeReplay(t, observer, job)
	require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return updateRecoveryOwnership(tx, &staleLease, "", nil) }), errRecoveryClaim)
	require.Equal(t, before, observeReplay(t, observer, job))
	staleToken := before.Task
	expireRecoveryLease(t, db, job)
	current := acquireRecovery(t, db, req, renewed.Add(time.Minute))
	require.NotEqual(t, token, current)
	before = observeReplay(t, observer, job)
	// Isolate the token predicate: even with knowledge of the current lease,
	// a superseded token cannot clear the winner's ownership.
	staleToken.RecoveryLeaseUntil = before.Task.RecoveryLeaseUntil
	require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return updateRecoveryOwnership(tx, &staleToken, "", nil) }), errRecoveryClaim)
	require.Equal(t, before, observeReplay(t, observer, job))
}

// PR #23 accepted stale renew/relinquish, false future deadlines and future
// obligations, but rejected a genuinely expired takeover. The historical run
// is recorded in the qualification report; assert corrected outcomes here.
func TestOCRRecoveryClockCharacterization(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	r := finalizationRepository{}
	for _, scenario := range []string{"stale_renewal", "stale_relinquishment", "expired_appears_active", "false_future_deadline", "not_due_appears_due"} {
		t.Run(scenario, func(t *testing.T) {
			db, observer := foundationDB(t)
			job, _, req := recoveryTerminal(t, db, "succeeded", 0)
			until := recoveryDatabaseNow(t, db).Add(time.Minute)
			token := ""
			if scenario != "false_future_deadline" && scenario != "not_due_appears_due" {
				token = acquireRecovery(t, db, req, until)
				expired := expireRecoveryLease(t, db, job)
				require.True(t, req.Now.Before(expired))
				require.True(t, recoveryDatabaseNow(t, db).After(expired))
			}
			if scenario == "false_future_deadline" {
				until = recoveryDatabaseNow(t, db).Add(-time.Microsecond)
				require.True(t, until.After(req.Now))
			}
			if scenario == "not_due_appears_due" {
				due := recoveryDatabaseNow(t, db).Add(time.Minute)
				req.Now = due
				until = due.Add(time.Minute)
				require.NoError(t, db.Model(&ocrFinalization{}).Where("task_id = ?", job.TaskID).UpdateColumn("next_attempt_at", due).Error)
			}
			before := observeReplay(t, observer, job)
			switch scenario {
			case "stale_renewal":
				require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return r.renewRecoveryInTx(tx, req, token, until) }), errRecoveryClaim)
				require.Equal(t, before, observeReplay(t, observer, job))
			case "stale_relinquishment":
				require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return r.relinquishRecoveryInTx(tx, req, token) }), errRecoveryClaim)
				require.Equal(t, before, observeReplay(t, observer, job))
			case "expired_appears_active":
				claim := acquireRecovery(t, db, req, until)
				require.NotEqual(t, token, claim)
				assertRecoveryOwnership(t, observer, job, before, claim, &until)
			default:
				err := db.Transaction(func(tx *gorm.DB) error { _, err := r.acquireRecoveryInTx(tx, req, until); return err })
				require.ErrorIs(t, err, errRecoveryClaim)
				require.Equal(t, before, observeReplay(t, observer, job))
			}
			assertFoundation(t, observer, job, "succeeded", "committed", 7, 50, 1, 5)
		})
	}
}

// Actual expiry must pass while a writer waits, without changing claim/lease
// fixture state. Exercise waits on both required locks and both lock-release
// outcomes. The observed clocks prove that a transaction-time or pre-lock
// timestamp would produce the wrong eligibility decision.
func TestOCRRecoveryClockLockWait(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	r := finalizationRepository{}
	for _, lock := range []string{"task", "reservation"} {
		for _, operation := range []string{"renew", "relinquish", "acquire_deadline", "acquire_takeover"} {
			for _, rollback := range []bool{false, true} {
				name := lock + "/" + operation + map[bool]string{false: "/blocker_commits", true: "/blocker_rolls_back"}[rollback]
				t.Run(name, func(t *testing.T) {
					db, observer := foundationDB(t)
					job, success, req := recoveryTerminal(t, db, "succeeded", 0)
					before := observeReplay(t, observer, job)
					deadline := recoveryDatabaseNow(t, db).Add(2 * time.Second)
					token := ""
					if operation != "acquire_deadline" {
						token = acquireRecovery(t, db, req, deadline)
					}
					waitingState := observeReplay(t, observer, job)
					first := db.Begin()
					require.NoError(t, first.Error)
					defer first.Rollback()
					var lockedID string
					if lock == "task" {
						require.NoError(t, first.Raw("SELECT task_id FROM ocr_finalizations WHERE task_id = ? FOR UPDATE", job.TaskID).Scan(&lockedID).Error)
						require.Equal(t, job.TaskID, lockedID)
					} else {
						// This barrier never requests a task lock afterward.
						require.NoError(t, first.Raw("SELECT id FROM billing_reservations WHERE id = ? FOR UPDATE", job.ReservationID).Scan(&lockedID).Error)
						require.Equal(t, job.ReservationID, lockedID)
					}
					type sampledClock struct{ TransactionTime, BeforeLock time.Time }
					started := make(chan sampledClock, 1)
					proposed := deadline.Add(time.Minute)
					if operation == "acquire_deadline" {
						proposed = deadline
					}
					result := waitRecoveryWriter(t, db, observer, first, func(tx *gorm.DB) (string, error) {
						var sampled sampledClock
						if err := tx.Raw("SELECT transaction_timestamp() AS transaction_time, clock_timestamp() AS before_lock").Scan(&sampled).Error; err != nil {
							return "", err
						}
						started <- sampled
						switch operation {
						case "renew":
							return "", r.renewRecoveryInTx(tx, req, token, proposed)
						case "relinquish":
							return "", r.relinquishRecoveryInTx(tx, req, token)
						default:
							return r.acquireRecoveryInTx(tx, req, proposed)
						}
					})
					sampled := <-started // Sent before the already-observed lock wait.
					require.True(t, sampled.TransactionTime.Before(deadline))
					require.True(t, sampled.BeforeLock.Before(deadline))
					require.True(t, recoveryDatabaseNow(t, observer).Before(deadline), "writer must be blocked before expiry")
					require.Eventually(t, func() bool {
						var now time.Time
						err := observer.Raw("SELECT clock_timestamp()").Scan(&now).Error
						return err == nil && !now.Before(deadline)
					}, 5*time.Second, 5*time.Millisecond, "observe actual PostgreSQL expiry while the writer is blocked")
					require.Equal(t, waitingState, observeReplay(t, observer, job), "no partial ownership while blocked")
					if rollback {
						require.NoError(t, first.Rollback().Error)
					} else {
						require.NoError(t, first.Commit().Error)
					}
					got := receiveRecovery(t, result)
					if operation == "acquire_takeover" {
						require.NoError(t, got.err)
						require.NotEmpty(t, got.token)
						require.NotEqual(t, token, got.token)
						assertRecoveryOwnership(t, observer, job, before, got.token, &proposed)
						require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return r.renewRecoveryInTx(tx, req, token, proposed.Add(time.Minute)) }), errRecoveryClaim)
						require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return r.relinquishRecoveryInTx(tx, req, token) }), errRecoveryClaim)
					} else {
						require.ErrorIs(t, got.err, errRecoveryClaim)
						require.Empty(t, got.token)
						require.Equal(t, waitingState, observeReplay(t, observer, job))
					}
					current := observeReplay(t, observer, job)
					require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, success) }))
					require.Equal(t, current, observeReplay(t, observer, job))
					assertFoundation(t, observer, job, "succeeded", "committed", 7, 50, 1, 5)
				})
			}
		}
	}
}

func TestOCRRecoveryClockPrecision(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	db, observer := foundationDB(t)
	job, _, req := recoveryTerminal(t, db, "succeeded", 0)
	before := observeReplay(t, observer, job)
	rawUntil := recoveryDatabaseNow(t, db).Add(time.Minute).Add(123 * time.Nanosecond)
	until := rawUntil.Truncate(time.Microsecond)
	token := acquireRecovery(t, db, req, rawUntil)
	assertRecoveryOwnership(t, observer, job, before, token, &until)
	current := observeReplay(t, observer, job)
	require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error {
		return (finalizationRepository{}).renewRecoveryInTx(tx, req, token, until.Add(time.Nanosecond))
	}), errRecoveryClaim)
	require.Equal(t, current, observeReplay(t, observer, job))
	// Exact equality is a pure predicate boundary. Standard repository APIs
	// above/below always obtain real PostgreSQL time; there is no injected clock.
	require.True(t, holdsRecovery(&current.Task, token, until.Add(-time.Nanosecond)))
	require.False(t, holdsRecovery(&current.Task, token, until))
	expired := expireRecoveryLease(t, db, job)
	current = observeReplay(t, observer, job)
	assertOptionalTime(t, &expired, current.Task.RecoveryLeaseUntil)
	require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return (finalizationRepository{}).relinquishRecoveryInTx(tx, req, token) }), errRecoveryClaim)
	require.Equal(t, current, observeReplay(t, observer, job))
}

// Demonstrate, rather than conceal, the remaining check-to-commit gap.
func TestOCRRecoveryClockDelayedCommit(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	db, observer := foundationDB(t)
	job, _, req := recoveryTerminal(t, db, "succeeded", 0)
	before := observeReplay(t, observer, job)
	until := recoveryDatabaseNow(t, db).Add(time.Second)
	tx := db.Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	token, err := (finalizationRepository{}).acquireRecoveryInTx(tx, req, until)
	require.NoError(t, err)
	require.True(t, recoveryDatabaseNow(t, observer).Before(until))
	require.Equal(t, before, observeReplay(t, observer, job))
	require.Eventually(t, func() bool {
		var now time.Time
		err := observer.Raw("SELECT clock_timestamp()").Scan(&now).Error
		return err == nil && !now.Before(until)
	}, 5*time.Second, 5*time.Millisecond)
	require.NoError(t, tx.Commit().Error)
	assertRecoveryOwnership(t, observer, job, before, token, &until)
	require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error {
		return (finalizationRepository{}).renewRecoveryInTx(tx, req, token, until.Add(time.Minute))
	}), errRecoveryClaim)
	require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return (finalizationRepository{}).relinquishRecoveryInTx(tx, req, token) }), errRecoveryClaim)
	next := until.Add(time.Minute)
	current := acquireRecovery(t, db, req, next)
	require.NotEqual(t, token, current)
	assertRecoveryOwnership(t, observer, job, before, current, &next)
}
