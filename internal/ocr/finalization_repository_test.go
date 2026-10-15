package ocr

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/billing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// No handler or startup schema list registers this table. This helper is the
// sole migration path, behind the existing loopback/test_ database guard.
func foundationDB(t *testing.T) (*gorm.DB, *gorm.DB) {
	t.Helper()
	dsn := os.Getenv("PLATEN_M1_BILLING_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("disposable PostgreSQL not configured; foundation not qualified")
	}
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"postgres", "postgresql"}, parsed.Scheme)
	require.Contains(t, []string{"127.0.0.1", "::1"}, parsed.Hostname(), "use an explicit loopback address")
	require.True(t, strings.HasPrefix(strings.TrimPrefix(parsed.Path, "/"), "test_"))
	for key := range parsed.Query() {
		require.Contains(t, []string{"sslmode", "connect_timeout"}, key, "do not override the guarded host/database/search path")
	}
	db := qualificationBillingDB(t)
	require.NoError(t, db.AutoMigrate(&ocrFinalization{}))
	observer, err := gorm.Open(postgres.Open(os.Getenv("PLATEN_M1_BILLING_TEST_DATABASE_URL")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := observer.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(2)
	pool.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	return db, observer
}

func installDecisionFailure(tx *gorm.DB, state, reservationState string) error {
	// Constants are test-owned. The function/trigger creation rolls back with the
	// induced error and is never registered in a managed migration.
	sql := fmt.Sprintf(`CREATE OR REPLACE FUNCTION pg_temp.reject_foundation_decision() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN
	IF NEW.state = '%s' AND EXISTS (SELECT 1 FROM billing_reservations WHERE id = NEW.reservation_id AND status = '%s') THEN
	RAISE EXCEPTION 'foundation guard after settlement';
	END IF;
	RETURN NEW;
	END $$`, state, reservationState)
	if err := tx.Exec(sql).Error; err != nil {
		return err
	}
	return tx.Exec(`CREATE TRIGGER foundation_decision_guard BEFORE UPDATE ON ocr_finalizations FOR EACH ROW EXECUTE FUNCTION pg_temp.reject_foundation_decision()`).Error
}

func waitForDatabaseLock(t *testing.T, observer *gorm.DB, pid int) {
	t.Helper()
	require.Eventually(t, func() bool {
		var blocked bool
		err := observer.Raw("SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid = ? AND wait_event_type = 'Lock')", pid).Scan(&blocked).Error
		return err == nil && blocked
	}, 5*time.Second, 5*time.Millisecond, "second writer must actually wait on a PostgreSQL lock")
}

func receiveFoundationResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("foundation worker did not finish")
		return nil
	}
}

func TestOCRFoundationCompetingDecisions(t *testing.T) {
	for _, used := range []int{0, 8, 5} {
		for _, scenario := range []string{"finalize_then_cancel", "cancel_then_finalize", "rollback_then_cancel", "two_finalizers"} {
			t.Run(fmt.Sprintf("used_%d/%s", used, scenario), func(t *testing.T) {
				t.Setenv("BILLING_MODE", "normal")
				db, observer := foundationDB(t)
				job, _ := foundationReady(t, db, used)
				req := foundationRequest(job)
				r := finalizationRepository{}
				first := db.Begin()
				require.NoError(t, first.Error)
				defer first.Rollback()
				if scenario == "cancel_then_finalize" {
					require.NoError(t, r.endInTx(first, req, "cancelled", "CONTROLLED_STOP"))
				} else {
					require.NoError(t, r.succeedInTx(first, req))
				}
				assertFoundation(t, observer, job, "ready", "reserved", used, 50, 0, 1)
				pid := make(chan int, 1)
				result := make(chan error, 1)
				go func() {
					result <- db.Transaction(func(tx *gorm.DB) error {
						var id int
						if err := tx.Raw("SELECT pg_backend_pid()").Scan(&id).Error; err != nil {
							return err
						}
						pid <- id
						if scenario == "two_finalizers" || scenario == "cancel_then_finalize" {
							return r.succeedInTx(tx, req)
						}
						return r.endInTx(tx, req, "cancelled", "CONTROLLED_STOP")
					})
				}()
				select {
				case id := <-pid:
					waitForDatabaseLock(t, observer, id)
				case <-time.After(5 * time.Second):
					t.Fatal("second connection did not enter")
				}
				if scenario == "rollback_then_cancel" {
					require.NoError(t, first.Rollback().Error)
				} else {
					require.NoError(t, first.Commit().Error)
				}
				err := receiveFoundationResult(t, result)
				if scenario == "two_finalizers" || scenario == "rollback_then_cancel" {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, errFinalizationTerminal)
				}
				if scenario == "cancel_then_finalize" || scenario == "rollback_then_cancel" {
					assertFoundation(t, observer, job, "cancelled", "released", used, 50, 0, 2)
				} else {
					wantUsed, wantCredits := 7, 50
					if used == 8 {
						wantUsed, wantCredits = 8, 43
					}
					if used == 5 {
						wantUsed, wantCredits = 8, 46
					}
					assertFoundation(t, observer, job, "succeeded", "committed", wantUsed, wantCredits, 1, 2)
				}
			})
		}
	}
}

func TestOCRFoundationReleaseAndReserveContention(t *testing.T) {
	for _, scenario := range []string{"reserve_waits_on_settlement", "release_waits_on_settlement", "settlement_waits_on_release", "cancel_update_rollback"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("BILLING_MODE", "normal")
			db, observer := foundationDB(t)
			job, _ := foundationReady(t, db, 0)
			req := foundationRequest(job)
			r := finalizationRepository{}
			if scenario == "cancel_update_rollback" {
				err := db.Transaction(func(tx *gorm.DB) error {
					if err := installDecisionFailure(tx, "cancelled", "released"); err != nil {
						return err
					}
					return r.endInTx(tx, req, "cancelled", "CONTROLLED_STOP")
				})
				require.ErrorContains(t, err, "foundation guard after settlement")
				assertFoundation(t, observer, job, "ready", "reserved", 0, 50, 0, 1)
				return
			}
			first := db.Begin()
			require.NoError(t, first.Error)
			defer first.Rollback()
			if scenario == "settlement_waits_on_release" {
				require.NoError(t, first.Model(&config.BillingReservation{}).Where("id = ?", job.ReservationID).Update("status", "released").Error)
			} else {
				require.NoError(t, r.succeedInTx(first, req))
			}
			pid := make(chan int, 1)
			result := make(chan error, 1)
			if scenario == "settlement_waits_on_release" {
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
			} else {
				// Observe the real legacy wrapper's connection immediately before
				// its locking statement. Do not substitute a settlement fake.
				callback := fmt.Sprintf("foundation_pid_%s", uuid.NewString())
				hook := func(tx *gorm.DB) {
					if (scenario == "reserve_waits_on_settlement" && tx.Statement.Table != "subscriptions") || (scenario == "release_waits_on_settlement" && tx.Statement.Table != "billing_reservations") {
						return
					}
					var id int
					if err := tx.Session(&gorm.Session{NewDB: true}).Raw("SELECT pg_backend_pid()").Scan(&id).Error; err != nil {
						tx.AddError(err)
						return
					}
					select {
					case pid <- id:
					default:
					}
				}
				if scenario == "reserve_waits_on_settlement" {
					require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callback, hook))
					defer db.Callback().Query().Remove(callback)
				} else {
					require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callback, hook))
					defer db.Callback().Update().Remove(callback)
				}
				go func() {
					if scenario == "release_waits_on_settlement" {
						result <- billing.Default.Release(job.ReservationID)
						return
					}
					res, err := billing.Default.ReserveWithTaskID(job.OwnerID, billing.ImageToTextPDF, 0, 1, "/foundation-concurrent-reserve", uuid.NewString())
					if err == nil && (res.PlanUnits != 1 || res.CreditUnits != 6) {
						err = fmt.Errorf("expected post-settlement allocation 1 plan/6 credits, got %d/%d", res.PlanUnits, res.CreditUnits)
					}
					result <- err
				}()
			}
			select {
			case id := <-pid:
				waitForDatabaseLock(t, observer, id)
			case <-time.After(5 * time.Second):
				t.Fatal("competing writer did not reach barrier")
			}
			require.NoError(t, first.Commit().Error)
			err := receiveFoundationResult(t, result)
			if scenario == "settlement_waits_on_release" {
				require.ErrorIs(t, err, billing.ErrReservationReleased)
				assertFoundation(t, observer, job, "ready", "released", 0, 50, 0, 1)
			} else {
				require.NoError(t, err)
				assertFoundation(t, observer, job, "succeeded", "committed", 7, 50, 1, 2)
			}
		})
	}
}

func foundationReady(t *testing.T, db *gorm.DB, used int) (ocrFinalization, config.Subscription) {
	t.Helper()
	owner, initial := qualificationAccount(t, db, false)
	require.NoError(t, db.Model(&config.Subscription{}).Where("user_id = ?", owner).Updates(map[string]any{"used_units3h": used, "used_units_daily": used, "used_units_monthly": used}).Error)
	initial.UsedUnits3h = used
	initial.UsedUnitsDaily = used
	initial.UsedUnitsMonthly = used
	id := uuid.NewString()
	res, err := billing.Default.ReserveWithTaskID(owner, billing.ImageToTextPDF, 0, 1, "/foundation-test", id)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Microsecond)
	job := ocrFinalization{TaskID: id, ProtocolVersion: 1, OwnerID: owner, ReservationID: res.ID, ReservationKind: string(billing.ReservationKindDatabase), ToolName: billing.ImageToTextPDF.Name,
		Units: res.Units, PlanUnits: res.PlanUnits, CreditUnits: res.CreditUnits, State: "ready", Revision: 1, ProcessingClaim: uuid.NewString(), CreatedAt: now, UpdatedAt: now,
		Artifact: artifactDescriptor{ArtifactID: uuid.NewString(), ArtifactKey: "foundation-private/" + uuid.NewString() + ".pdf", SHA256: strings.Repeat("a", 64), ByteSize: 128, PageCount: 1, ContentType: "application/pdf", ValidationVersion: 1, VerifiedAt: now}}
	r := finalizationRepository{}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.createInTx(tx, &job) }))
	t.Cleanup(func() { db.Where("task_id = ?", id).Delete(&ocrFinalization{}) })
	return job, initial
}

func foundationRequest(job ocrFinalization) finalizationRequest {
	return finalizationRequest{TaskID: job.TaskID, OwnerID: job.OwnerID, ReservationID: job.ReservationID, ExpectedState: job.State, ExpectedRevision: job.Revision, ProcessingClaim: job.ProcessingClaim, Artifact: job.Artifact, Now: job.CreatedAt.Add(time.Second)}
}

// Read every durable assertion through an independent pool/connection, after
// transaction completion. Literal allocation/counter expectations are external
// to the settlement implementation.
func assertFoundation(t *testing.T, observer *gorm.DB, job ocrFinalization, state, resState string, used, credits, logs int, revision int64) {
	t.Helper()
	var row ocrFinalization
	var res config.BillingReservation
	var sub config.Subscription
	var count int64
	require.NoError(t, observer.First(&row, "task_id = ?", job.TaskID).Error)
	require.NoError(t, observer.First(&res, "id = ?", job.ReservationID).Error)
	require.NoError(t, observer.Where("user_id = ?", job.OwnerID).First(&sub).Error)
	require.NoError(t, observer.Model(&config.UsageLog{}).Where("user_id = ?", job.OwnerID).Count(&count).Error)
	require.Equal(t, state, row.State)
	require.Equal(t, resState, res.Status)
	require.Equal(t, revision, row.Revision)
	require.Equal(t, used, sub.UsedUnits3h)
	require.Equal(t, used, sub.UsedUnitsDaily)
	require.Equal(t, used, sub.UsedUnitsMonthly)
	require.Equal(t, credits, sub.CustomCredits)
	require.EqualValues(t, logs, count)
	require.Equal(t, job.OwnerID, row.OwnerID)
	require.Equal(t, job.ReservationID, row.ReservationID)
	require.Equal(t, job.Artifact.ArtifactID, row.Artifact.ArtifactID)
	require.Equal(t, job.Artifact.ArtifactKey, row.Artifact.ArtifactKey)
	require.Equal(t, job.Artifact.SHA256, row.Artifact.SHA256)
}

func TestOCRFoundationAtomicDecisions(t *testing.T) {
	for _, allocation := range []struct {
		name                   string
		initial, used, credits int
	}{{"plan", 0, 7, 50}, {"credits", 8, 8, 43}, {"mixed", 5, 8, 46}} {
		for _, scenario := range []string{"success_retry", "cancel_first", "fail_first", "rollback", "conflicting_artifact", "wrong_owner", "wrong_reservation", "stale_revision", "wrong_state", "wrong_claim"} {
			t.Run(allocation.name+"/"+scenario, func(t *testing.T) {
				t.Setenv("BILLING_MODE", "normal")
				db, observer := foundationDB(t)
				job, _ := foundationReady(t, db, allocation.initial)
				req := foundationRequest(job)
				r := finalizationRepository{}
				if scenario == "cancel_first" || scenario == "fail_first" {
					state := "cancelled"
					if scenario == "fail_first" {
						state = "failed"
					}
					require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.endInTx(tx, req, state, "CONTROLLED_STOP") }))
					require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, req) }), errFinalizationTerminal)
					assertFoundation(t, observer, job, state, "released", allocation.initial, 50, 0, 2)
					return
				}
				if scenario == "rollback" {
					// Trigger aborts exactly at the decision UPDATE, after settlement.
					err := db.Transaction(func(tx *gorm.DB) error {
						if err := installDecisionFailure(tx, "succeeded", "committed"); err != nil {
							return err
						}
						return r.succeedInTx(tx, req)
					})
					require.ErrorContains(t, err, "foundation guard after settlement")
					assertFoundation(t, observer, job, "ready", "reserved", allocation.initial, 50, 0, 1)
					return
				}
				switch scenario {
				case "wrong_owner":
					req.OwnerID = uuid.NewString()
				case "wrong_reservation":
					req.ReservationID = uuid.NewString()
				case "stale_revision":
					req.ExpectedRevision = 0
				case "wrong_state":
					req.ExpectedState = "processing"
				case "wrong_claim":
					req.ProcessingClaim = uuid.NewString()
				}
				if strings.HasPrefix(scenario, "wrong_") || scenario == "stale_revision" {
					require.Error(t, db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, req) }))
					assertFoundation(t, observer, job, "ready", "reserved", allocation.initial, 50, 0, 1)
					return
				}
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, req) }))
				retry := req
				retry.Now = job.CreatedAt.Add(24 * time.Hour) // Replay cannot reset a window or charge again.
				if scenario == "conflicting_artifact" {
					retry.Artifact.SHA256 = strings.Repeat("b", 64)
				}
				err := db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, retry) })
				if scenario == "conflicting_artifact" {
					require.ErrorIs(t, err, errFinalizationBinding)
				} else {
					require.NoError(t, err)
				}
				require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return r.endInTx(tx, req, "cancelled", "TOO_LATE") }), errFinalizationTerminal)
				assertFoundation(t, observer, job, "succeeded", "committed", allocation.used, allocation.credits, 1, 2)
			})
		}
	}
}

func TestOCRFoundationCreateConstraints(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	db, observer := foundationDB(t)
	job, _ := foundationReady(t, db, 0)
	r := finalizationRepository{}
	for _, name := range []string{"duplicate_task", "duplicate_reservation", "wrong_binding", "bad_protocol", "bad_descriptor", "terminal_create", "autocommit"} {
		t.Run(name, func(t *testing.T) {
			candidate := job
			switch name {
			case "duplicate_reservation":
				candidate.TaskID = uuid.NewString()
			case "wrong_binding":
				candidate.OwnerID = uuid.NewString()
			case "bad_protocol":
				candidate.ProtocolVersion = 2
			case "bad_descriptor":
				candidate.Artifact.SHA256 = "invalid"
			case "terminal_create":
				candidate.State = "succeeded"
			}
			if name == "autocommit" {
				require.ErrorIs(t, r.createInTx(db, &candidate), billing.ErrTransactionRequired)
			} else {
				require.Error(t, db.Transaction(func(tx *gorm.DB) error { return r.createInTx(tx, &candidate) }))
			}
			assertFoundation(t, observer, job, "ready", "reserved", 0, 50, 0, 1)
		})
	}
	// Exercise the database constraint independently of repository validation.
	for _, updates := range []map[string]any{{"protocol_version": 2}, {"revision": 0}, {"state": "invalid"}, {"artifact_byte_size": 0}, {"reservation_kind": "guest"}, {"artifact_sha256": nil}, {"artifact_id": nil}, {"artifact_verified_at": time.Time{}}} {
		require.Error(t, db.Model(&ocrFinalization{}).Where("task_id = ?", job.TaskID).Updates(updates).Error)
	}
	duplicate := job
	duplicate.TaskID = uuid.NewString()
	duplicate.Artifact.ArtifactKey = "foundation-private/" + uuid.NewString() + ".pdf"
	require.Error(t, db.Create(&duplicate).Error, "the database independently enforces unique reservation binding")
}

func TestOCRFoundationEarlyCancellation(t *testing.T) {
	for _, state := range []string{"accepted", "processing"} {
		t.Run(state, func(t *testing.T) {
			t.Setenv("BILLING_MODE", "normal")
			db, observer := foundationDB(t)
			job, _ := foundationReady(t, db, 0)
			// Recreate a pre-artifact row: the supported repository creation path
			// must allow cancellation without a fabricated ready descriptor.
			require.NoError(t, db.Delete(&ocrFinalization{}, "task_id = ?", job.TaskID).Error)
			job.State = state
			job.Artifact = artifactDescriptor{}
			job.ProcessingClaim = ""
			r := finalizationRepository{}
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.createInTx(tx, &job) }))
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return r.endInTx(tx, foundationRequest(job), "cancelled", "USER_CANCELLED") }))
			assertFoundation(t, observer, job, "cancelled", "released", 0, 50, 0, 2)
		})
	}
}

func TestOCRFoundationReservationStateRejections(t *testing.T) {
	for _, state := range []string{"missing", "released", "committed"} {
		t.Run(state, func(t *testing.T) {
			t.Setenv("BILLING_MODE", "normal")
			db, observer := foundationDB(t)
			job, _ := foundationReady(t, db, 0)
			r := finalizationRepository{}
			req := foundationRequest(job)
			expected := billing.ErrReservationReleased
			switch state {
			case "missing":
				require.NoError(t, db.Delete(&config.BillingReservation{}, "id = ?", job.ReservationID).Error)
				expected = gorm.ErrRecordNotFound
			case "released":
				require.NoError(t, billing.Default.Release(job.ReservationID))
			case "committed":
				require.NoError(t, billing.Default.Commit(job.ReservationID))
				expected = billing.ErrReservationCommitted
			}
			require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return r.succeedInTx(tx, req) }), expected)
			if state != "missing" {
				used, logs := 0, 0
				if state == "committed" {
					used, logs = 7, 1
				}
				assertFoundation(t, observer, job, "ready", state, used, 50, logs, 1)
			} else {
				var row ocrFinalization
				require.NoError(t, observer.First(&row, "task_id = ?", job.TaskID).Error)
				require.Equal(t, "ready", row.State)
				require.EqualValues(t, 1, row.Revision)
				var count int64
				require.NoError(t, observer.Model(&config.UsageLog{}).Where("user_id = ?", job.OwnerID).Count(&count).Error)
				require.Zero(t, count)
				var sub config.Subscription
				var absent config.BillingReservation
				require.ErrorIs(t, observer.First(&absent, "id = ?", job.ReservationID).Error, gorm.ErrRecordNotFound)
				require.NoError(t, observer.Where("user_id = ?", job.OwnerID).First(&sub).Error)
				require.Zero(t, sub.UsedUnits3h)
				require.Zero(t, sub.UsedUnitsDaily)
				require.Zero(t, sub.UsedUnitsMonthly)
				require.Equal(t, 50, sub.CustomCredits)
				require.Equal(t, job.Artifact.ArtifactID, row.Artifact.ArtifactID)
				require.Equal(t, job.Artifact.ArtifactKey, row.Artifact.ArtifactKey)
				require.Equal(t, job.Artifact.SHA256, row.Artifact.SHA256)
			}
		})
	}
}
