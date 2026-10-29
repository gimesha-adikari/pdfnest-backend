package billing

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/models"
	"pdfnest-backend/internal/storage"
	studioModels "pdfnest-backend/internal/studio/models"
	"pdfnest-backend/internal/tasks"

	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

func completionFixture(t *testing.T, guest bool) (*gorm.DB, *redis.Client, *miniredis.Miniredis, *models.TaskCompletion, string) {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("explicit disposable DATABASE_URL required")
	}
	t.Setenv("BILLING_MODE", "normal")
	t.Setenv("APP_ENV", "test")
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_STORAGE_DIR", t.TempDir())
	db := setupTestDB(t)
	if err := db.AutoMigrate(&studioModels.StudioStorageObjectRetirement{}, &models.WorkerResultOwnership{}); err != nil {
		t.Fatal(err)
	}
	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	oldRedis := config.Redis
	oldGuest := GuestQuota
	oldDB := config.DB
	oldSettle := tasks.SettleTaskBilling
	config.Redis = client
	Initialize(NewGuestQuotaStore(client, 24*time.Hour))
	t.Cleanup(func() {
		config.Redis = oldRedis
		GuestQuota = oldGuest
		config.DB = oldDB
		tasks.SettleTaskBilling = oldSettle
		client.Close()
	})
	id := uuid.NewString()
	t.Cleanup(func() { db.Delete(&models.TaskCompletion{}, "id = ?", id) })
	owner := uuid.NewString()
	reservationID := ""
	kind := ReservationKindDatabase
	if guest {
		res, err := GuestQuota.ReserveAsync(t.Context(), owner, ExtractTextPDF, 1, 0, "test")
		if err != nil {
			t.Fatal(err)
		}
		reservationID = res.ID
		kind = ReservationKindGuest
	} else {
		userID, _ := createTestUserAndSub(db)
		res, err := Default.ReserveWithTaskID(userID, ExtractTextPDF, 1, 0, "test", id)
		if err != nil {
			t.Fatal(err)
		}
		reservationID = res.ID
	}
	if ok, err := tasks.Registry.SetWithKeyAndBilling(id, "PENDING", 0, "", "", owner, reservationID, string(kind)); err != nil || !ok {
		t.Fatalf("register: %v", err)
	}
	if err := tasks.Registry.AdmitDurable(t.Context(), id, owner, reservationID, string(kind)); err != nil {
		t.Fatal(err)
	}
	var row models.TaskCompletion
	if err := db.First(&row, "id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "result.txt")
	if err := os.WriteFile(src, []byte("durable result\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return db, client, srv, &row, src
}
func completionRow(t *testing.T, db *gorm.DB, id string) models.TaskCompletion {
	t.Helper()
	var row models.TaskCompletion
	if err := db.First(&row, "id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	return row
}
func assertReserved(t *testing.T, db *gorm.DB, id, state string) {
	t.Helper()
	var res config.BillingReservation
	if err := db.First(&res, "id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	if res.Status != state {
		t.Fatalf("reservation=%s want=%s", res.Status, state)
	}
}

func TestDurableCompletionDatabaseAtomicRecoveryAndProjectionLoss(t *testing.T) {
	db, client, _, row, src := completionFixture(t, false)
	original := tasks.SettleTaskBilling
	failOnce := true
	tasks.SettleTaskBilling = func(ctx context.Context, tx *gorm.DB, r *models.TaskCompletion) error {
		if err := original(ctx, tx, r); err != nil {
			return err
		}
		if failOnce {
			failOnce = false
			return errors.New("crash after charge before SQL COMMIT")
		}
		return nil
	}
	key := "outputs/tasks/" + row.ID + "/result.txt"
	if err := tasks.Registry.PublishArtifact(t.Context(), row.ID, key, src, "text/plain"); !errors.Is(err, tasks.ErrSettlementPending) {
		t.Fatalf("expected uncertain settlement: %v", err)
	}
	assertReserved(t, db, row.ReservationID, "reserved")
	if got := completionRow(t, db, row.ID).State; got != "publishing" {
		t.Fatalf("state=%s", got)
	}
	// Even an expired retention clock cannot reclaim an unresolved settlement.
	db.Model(&models.TaskCompletion{}).Where("id = ?", row.ID).Update("expires_at", time.Now().Add(-time.Hour))
	tasks.SettleTaskBilling = func(context.Context, *gorm.DB, *models.TaskCompletion) error { return errors.New("billing outage") }
	_ = tasks.Registry.SweepDurableCompletions(t.Context(), time.Now())
	if !storage.ObjectExists(t.Context(), key) {
		t.Fatal("unsettled artifact deleted")
	}
	tasks.SettleTaskBilling = original
	if err := tasks.Registry.ReconcileCompletion(t.Context(), row.ID); err != nil {
		t.Fatal(err)
	}
	assertReserved(t, db, row.ReservationID, "committed")
	for i := 0; i < 3; i++ {
		if err := tasks.Registry.ReconcileCompletion(t.Context(), row.ID); err != nil {
			t.Fatal(err)
		}
	}
	var res config.BillingReservation
	db.First(&res, "id = ?", row.ReservationID)
	var charges int64
	db.Model(&config.UsageLog{}).Where("user_id = ?", res.UserID).Count(&charges)
	if charges != 1 {
		t.Fatalf("charges=%d", charges)
	}
	client.Del(t.Context(), tasks.TaskKeyPrefix+row.ID)
	task, err := tasks.Registry.Get(row.ID)
	if err != nil || task == nil || task.Status != "COMPLETED" {
		t.Fatalf("durable fallback: %+v %v", task, err)
	}
	app := fiber.New()
	app.Get("/:id", func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityIDKey, row.OwnerIdentity)
		return tasks.HandleTaskDownload(c)
	})
	response, err := app.Test(httptest.NewRequest("GET", "/"+row.ID, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 || string(data) != "durable result\n" {
		t.Fatalf("download %d %q", response.StatusCode, data)
	}
}

func TestDurableCompletionUnavailableArtifactCannotCharge(t *testing.T) {
	db, _, _, row, _ := completionFixture(t, false)
	key := "outputs/tasks/" + row.ID + "/result.txt"
	err := tasks.Registry.PublishArtifact(t.Context(), row.ID, key, "/does/not/exist", "text/plain")
	if !errors.Is(err, tasks.ErrSettlementPending) {
		t.Fatalf("err=%v", err)
	}
	assertReserved(t, db, row.ReservationID, "reserved")
	if err := tasks.Registry.ReconcileCompletion(t.Context(), row.ID); !errors.Is(err, tasks.ErrSettlementPending) {
		t.Fatal(err)
	}
	assertReserved(t, db, row.ReservationID, "reserved")
}

func TestDurableCompletionGuestLostAcknowledgementReceipt(t *testing.T) {
	db, client, _, row, src := completionFixture(t, true)
	original := tasks.SettleTaskBilling
	first := true
	tasks.SettleTaskBilling = func(ctx context.Context, tx *gorm.DB, r *models.TaskCompletion) error {
		if err := original(ctx, tx, r); err != nil {
			return err
		}
		if first {
			first = false
			return errors.New("Redis committed but acknowledgment lost")
		}
		return nil
	}
	key := "outputs/tasks/" + row.ID + "/result.txt"
	if err := tasks.Registry.PublishArtifact(t.Context(), row.ID, key, src, "text/plain"); !errors.Is(err, tasks.ErrSettlementPending) {
		t.Fatalf("err=%v", err)
	}
	if completionRow(t, db, row.ID).State != "publishing" {
		t.Fatal("ambiguous row not pinned")
	}
	// Competing release must preserve the already chosen publishing outcome.
	if err := Default.ReleaseAsync(t.Context(), row.ReservationID, ReservationKindGuest); err != nil {
		t.Fatal(err)
	}
	tasks.SettleTaskBilling = original
	for i := 0; i < 3; i++ {
		if err := tasks.Registry.ReconcileCompletion(t.Context(), row.ID); err != nil {
			t.Fatal(err)
		}
	}
	state, err := client.HGetAll(t.Context(), GuestQuota.stateKey(row.OwnerIdentity)).Result()
	if err != nil {
		t.Fatal(err)
	}
	if state["used_3h"] != strconv.Itoa(ExtractTextPDF.Units(1, 0)) || state["pending_3h"] != "0" {
		t.Fatalf("guest counters: %#v", state)
	}
	if got := client.Get(t.Context(), GuestQuota.outcomeKey(row.ReservationID)).Val(); got != "committed" {
		t.Fatalf("receipt=%q", got)
	}
	if ttl := client.TTL(t.Context(), GuestQuota.outcomeKey(row.ReservationID)).Val(); ttl != -1 {
		t.Fatalf("receipt must survive recovery: %v", ttl)
	}
}

func TestDurableCompletionGuestMissingReservationIsNotChargeProof(t *testing.T) {
	db, client, _, row, src := completionFixture(t, true)
	client.Del(t.Context(), GuestQuota.resKey(row.ReservationID))
	err := tasks.Registry.PublishArtifact(t.Context(), row.ID, "outputs/tasks/"+row.ID+"/result.txt", src, "text/plain")
	if !errors.Is(err, tasks.ErrSettlementPending) {
		t.Fatalf("missing receipt accepted: %v", err)
	}
	if completionRow(t, db, row.ID).State != "publishing" {
		t.Fatal("unconfirmed settlement published")
	}
}

func TestDurableCompletionPublicationWinsConcurrentCancellationAndRelease(t *testing.T) {
	db, _, _, row, src := completionFixture(t, false)
	entered := make(chan struct{})
	finish := make(chan struct{})
	original := tasks.SettleTaskBilling
	tasks.SettleTaskBilling = func(ctx context.Context, tx *gorm.DB, r *models.TaskCompletion) error {
		close(entered)
		<-finish
		return original(ctx, tx, r)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	done := make(chan error, 1)
	go func() {
		defer wg.Done()
		done <- tasks.Registry.PublishArtifact(context.Background(), row.ID, "outputs/tasks/"+row.ID+"/result.txt", src, "text/plain")
	}()
	<-entered
	cancelDone := make(chan string, 1)
	releaseDone := make(chan error, 1)
	go func() {
		result, _, err := tasks.Registry.CancelTask(row.ID, row.OwnerIdentity)
		if err != nil {
			cancelDone <- err.Error()
		} else {
			cancelDone <- result
		}
	}()
	go func() { releaseDone <- Default.Release(row.ReservationID) }()
	close(finish)
	wg.Wait()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if result := <-cancelDone; result != "TERMINAL_ALREADY" {
		t.Fatalf("cancel=%s", result)
	}
	if err := <-releaseDone; err != nil {
		t.Fatal(err)
	}
	assertReserved(t, db, row.ReservationID, "committed")
	if completionRow(t, db, row.ID).State != "completed" {
		t.Fatal("completion lost")
	}
}

func TestDurableCompletionCancellationWinsBeforePublication(t *testing.T) {
	db, _, _, row, src := completionFixture(t, false)
	result, _, err := tasks.Registry.CancelTask(row.ID, row.OwnerIdentity)
	if err != nil || result != "CANCELLED_SUCCESS" {
		t.Fatalf("%s %v", result, err)
	}
	if err := Default.Release(row.ReservationID); err != nil {
		t.Fatal(err)
	}
	key := "outputs/tasks/" + row.ID + "/result.txt"
	if err := tasks.Registry.PublishArtifact(t.Context(), row.ID, key, src, "text/plain"); !errors.Is(err, tasks.ErrTaskTerminal) {
		t.Fatalf("err=%v", err)
	}
	assertReserved(t, db, row.ReservationID, "released")
	if storage.ObjectExists(t.Context(), key) {
		t.Fatal("cancelled task published storage")
	}
}

func TestDurableCompletionRetentionRetryAndDuplicateCleanup(t *testing.T) {
	db, _, _, row, src := completionFixture(t, false)
	key := "outputs/tasks/" + row.ID + "/result.txt"
	if err := tasks.Registry.PublishArtifact(t.Context(), row.ID, key, src, "text/plain"); err != nil {
		t.Fatal(err)
	}
	if err := tasks.Registry.SweepDurableCompletions(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if !storage.ObjectExists(t.Context(), key) {
		t.Fatal("live download removed")
	}
	db.Model(&models.TaskCompletion{}).Where("id = ?", row.ID).Update("expires_at", time.Now().Add(-time.Minute))
	// Model a storage deletion error with a nonempty directory at the owned key.
	path := filepath.Join(storage.GetLocalStorageDir(), filepath.FromSlash(key))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(path, "blocked"), []byte("x"), 0600)
	if err := tasks.Registry.SweepDurableCompletions(t.Context(), time.Now()); err == nil {
		t.Fatal("expected physical deletion error")
	}
	if completionRow(t, db, row.ID).State != "deleting" {
		t.Fatal("deletion intent lost")
	}
	os.RemoveAll(path)
	for i := 0; i < 2; i++ {
		if err := tasks.Registry.SweepDurableCompletions(t.Context(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if completionRow(t, db, row.ID).State != "deleted" {
		t.Fatal("restart retry did not finish")
	}
}

func TestDurableCompletionFreeModeHasNoSettlementDependency(t *testing.T) {
	db, _, _, original, src := completionFixture(t, false)
	t.Setenv("BILLING_MODE", "free")
	allocation, err := Default.ReserveAsync(t.Context(), "free-user", "", ExtractTextPDF, 1, 0, "free-test", uuid.NewString())
	if err != nil || allocation.ID != "" {
		t.Fatalf("free reservation: %+v %v", allocation, err)
	}
	row := models.TaskCompletion{ID: uuid.NewString(), OwnerIdentity: original.OwnerIdentity, DownloadToken: uuid.NewString(), State: "active", ReservationKind: string(allocation.Kind), ExpiresAt: time.Now().Add(tasks.TaskTTL)}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Delete(&row) })
	tasks.SettleTaskBilling = func(context.Context, *gorm.DB, *models.TaskCompletion) error {
		t.Error("free completion attempted billing")
		return errors.New("unexpected free charge")
	}
	if err := tasks.Registry.PublishArtifact(t.Context(), row.ID, "outputs/tasks/"+row.ID+"/result.txt", src, "text/plain"); err != nil {
		t.Fatal(err)
	}
	if completionRow(t, db, row.ID).State != "completed" {
		t.Fatal("free artifact unavailable")
	}
}

func TestDurableCompletionDuplicatePublicationCannotOverwriteSettledArtifact(t *testing.T) {
	db, _, _, row, src := completionFixture(t, false)
	key := "outputs/tasks/" + row.ID + "/result.txt"
	if err := tasks.Registry.PublishArtifact(t.Context(), row.ID, key, src, "text/plain"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("replacement bytes must never publish"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := tasks.Registry.PublishArtifact(t.Context(), row.ID, key, src, "text/plain"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(completionRow(t, db, row.ID).ResultURL)
	if err != nil || string(data) != "durable result\n" {
		t.Fatalf("settled artifact mutated: %q %v", data, err)
	}
}

func TestDurableCompletionRedisUnavailableDoesNotInvalidateDurableSuccess(t *testing.T) {
	db, _, srv, row, src := completionFixture(t, false)
	srv.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := tasks.Registry.PublishArtifact(ctx, row.ID, "outputs/tasks/"+row.ID+"/result.txt", src, "text/plain"); err != nil {
		t.Fatal(err)
	}
	assertReserved(t, db, row.ReservationID, "committed")
	task, err := tasks.Registry.Get(row.ID)
	if err != nil || task == nil || task.Status != "COMPLETED" {
		t.Fatalf("Redis failure hid durable artifact: %+v %v", task, err)
	}
}

func TestDurableCompletionJanitorAndExpiredReservationCannotReleasePublishing(t *testing.T) {
	db, client, _, row, src := completionFixture(t, false)
	tasks.SettleTaskBilling = func(context.Context, *gorm.DB, *models.TaskCompletion) error {
		return errors.New("settlement unavailable")
	}
	key := "outputs/tasks/" + row.ID + "/result.txt"
	if err := tasks.Registry.PublishArtifact(t.Context(), row.ID, key, src, "text/plain"); !errors.Is(err, tasks.ErrSettlementPending) {
		t.Fatalf("err=%v", err)
	}
	before := time.Now().Add(-2 * time.Hour)
	if err := db.Model(&config.BillingReservation{}).Where("id = ?", row.ReservationID).Updates(map[string]interface{}{"expires_at": before, "created_at": before}).Error; err != nil {
		t.Fatal(err)
	}
	client.Del(t.Context(), tasks.TaskKeyPrefix+row.ID)
	SweepAbandonedReservations(time.Minute)
	assertReserved(t, db, row.ReservationID, "reserved")
	var res config.BillingReservation
	db.First(&res, "id = ?", row.ReservationID)
	totals, err := activeReservationTotals(db, res.UserID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if totals.Units < res.Units {
		t.Fatalf("expired unresolved reservation lost from quota accounting: %+v %+v", totals, res)
	}
	if !storage.ObjectExists(t.Context(), key) {
		t.Fatal("janitor removed unresolved artifact")
	}
}

func TestDurableCompletionUncertainRetryCannotChangeChosenArtifactIdentity(t *testing.T) {
	db, _, _, row, src := completionFixture(t, false)
	tasks.SettleTaskBilling = func(context.Context, *gorm.DB, *models.TaskCompletion) error { return errors.New("settlement outage") }
	key := "outputs/tasks/" + row.ID + "/result.txt"
	if err := tasks.Registry.PublishArtifact(t.Context(), row.ID, key, src, "text/plain"); !errors.Is(err, tasks.ErrSettlementPending) {
		t.Fatal(err)
	}
	chosen := completionRow(t, db, row.ID)
	if err := os.Remove(chosen.ResultURL); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("different retry artifact"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := tasks.Registry.PublishArtifact(t.Context(), row.ID, key, src, "text/plain"); err == nil {
		t.Fatal("different artifact replaced pinned identity")
	}
	after := completionRow(t, db, row.ID)
	if after.ArtifactSHA256 != chosen.ArtifactSHA256 || after.ArtifactBytes != chosen.ArtifactBytes {
		t.Fatal("chosen fingerprint changed")
	}
	assertReserved(t, db, row.ReservationID, "reserved")
	if storage.ObjectExists(t.Context(), key) {
		t.Fatal("unconfirmed retry artifact published")
	}
}

func TestDurableCompletionCancellationRequiresOwnerEvenForEmptyCaller(t *testing.T) {
	db, _, _, row, _ := completionFixture(t, false)
	for _, caller := range []string{"", uuid.NewString()} {
		result, _, err := tasks.Registry.CancelTask(row.ID, caller)
		if err != nil || result != "UNAUTHORIZED" {
			t.Fatalf("caller %q result=%s err=%v", caller, result, err)
		}
		if completionRow(t, db, row.ID).State != "active" {
			t.Fatal("unauthorized caller changed durable task")
		}
	}
}

func TestDurableCompletionExpiredActiveCrashReleasesOnceAndRejectsLatePublisher(t *testing.T) {
	db, client, _, row, src := completionFixture(t, false)
	now := time.Now()
	if err := db.Model(row).Update("expires_at", now.Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	client.Del(t.Context(), tasks.TaskKeyPrefix+row.ID)
	if err := tasks.Registry.SweepDurableCompletions(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if completionRow(t, db, row.ID).State != "failed" {
		t.Fatal("crashed active task not recovered")
	}
	assertReserved(t, db, row.ReservationID, "released")
	if err := tasks.Registry.PublishArtifact(t.Context(), row.ID, "outputs/tasks/"+row.ID+"/result.txt", src, "text/plain"); !errors.Is(err, tasks.ErrTaskTerminal) {
		t.Fatalf("late publisher revived expired task: %v", err)
	}
	if err := tasks.Registry.SweepDurableCompletions(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	assertReserved(t, db, row.ReservationID, "released")
}

func TestDurableCompletionUncertainBatchCannotStarveExpiredCompletedCleanup(t *testing.T) {
	db, _, _, row, src := completionFixture(t, false)
	key := "outputs/tasks/" + row.ID + "/result.txt"
	if err := tasks.Registry.PublishArtifact(t.Context(), row.ID, key, src, "text/plain"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	db.Model(row).Update("expires_at", now.Add(-time.Hour))
	var held []models.TaskCompletion
	var ids []string
	for i := 0; i < 101; i++ {
		id := uuid.NewString()
		ids = append(ids, id)
		held = append(held, models.TaskCompletion{ID: id, OwnerIdentity: "held", DownloadToken: "token", State: "publishing", ResultKey: "outputs/tasks/" + id + "/missing", UpdatedAt: now.Add(-time.Hour)})
	}
	if err := db.Create(&held).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Delete(&models.TaskCompletion{}, "id IN ?", ids) })
	_ = tasks.Registry.SweepDurableCompletions(t.Context(), now)
	if completionRow(t, db, row.ID).State != "deleted" || storage.ObjectExists(t.Context(), key) {
		t.Fatal("uncertain publishing batch starved completed cleanup")
	}
}

func TestDurableCompletionWorkerSnapshotRecoversAfterRedisExpiryWithoutDoubleCharge(t *testing.T) {
	db, client, _, row, _ := completionFixture(t, false)
	key := "jobs/markdown/output/" + uuid.NewString() + ".md"
	if err := storage.SaveObjectBytes(t.Context(), key, []byte("# Recovered artifact\n"), "text/markdown"); err != nil {
		t.Fatal(err)
	}
	owned := models.WorkerResultOwnership{ID: uuid.NewString(), RequestID: uuid.NewString(), RequestHash: "hash", WorkerJobID: row.ID, OwnerIdentity: row.OwnerIdentity, SubmitPath: "/api/v1/jobs/submit", SourceKeys: `[]`, State: "terminal", ResultKey: key, StatusJSON: `{"status":"succeeded"}`}
	if err := db.Create(&owned).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Delete(&owned) })
	client.Del(t.Context(), tasks.TaskKeyPrefix+row.ID)
	for i := 0; i < 2; i++ {
		if err := tasks.Registry.SweepDurableCompletions(t.Context(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	result := completionRow(t, db, row.ID)
	if result.State != "completed" || !storage.ObjectExists(t.Context(), result.ResultKey) {
		t.Fatalf("snapshot not recovered: %+v", result)
	}
	assertReserved(t, db, row.ReservationID, "committed")
	var reservation config.BillingReservation
	db.First(&reservation, "id = ?", row.ReservationID)
	var count int64
	db.Model(&config.UsageLog{}).Where("user_id = ?", reservation.UserID).Count(&count)
	if count != 1 {
		t.Fatalf("recovery charged %d times", count)
	}
}

func TestDurableCompletionCleanupProtectsSharedWorkerInputAndRetiresKey(t *testing.T) {
	db, _, _, row, src := completionFixture(t, false)
	key := "outputs/tasks/" + row.ID + "/result.txt"
	if err := tasks.Registry.PublishArtifact(t.Context(), row.ID, key, src, "text/plain"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	db.Model(row).Update("expires_at", now.Add(-time.Hour))
	encoded, _ := json.Marshal([]string{key})
	pin := models.WorkerResultOwnership{ID: uuid.NewString(), RequestID: uuid.NewString(), RequestHash: "hash", SubmitPath: "/api/v1/editor/extract", SourceKeys: string(encoded), State: "uncertain"}
	if err := db.Create(&pin).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Delete(&pin) })
	if err := tasks.Registry.SweepDurableCompletions(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if !storage.ObjectExists(t.Context(), key) {
		t.Fatal("live shared input deleted")
	}
	db.Model(&pin).Update("state", "deleted")
	if err := tasks.Registry.SweepDurableCompletions(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if storage.ObjectExists(t.Context(), key) {
		t.Fatal("unreferenced completed artifact not deleted")
	}
	var retired int64
	db.Table("studio_storage_object_retirements").Where("object_key = ?", key).Count(&retired)
	if retired != 1 {
		t.Fatal("deletion lacks durable immutable retirement")
	}
}

func TestDurableCompletionExternalAdmissionWinsExpiredRecoveryRace(t *testing.T) {
	db, _, _, row, _ := completionFixture(t, false)
	now := time.Now()
	db.Model(row).Update("expires_at", now.Add(-time.Hour))
	observed := make(chan struct{})
	resume := make(chan struct{})
	var once sync.Once
	name := "external-admission-race-" + uuid.NewString()
	if err := db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "worker_result_ownerships" {
			once.Do(func() { close(observed); <-resume })
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Query().Remove(name) })
	done := make(chan error, 1)
	go func() { done <- tasks.Registry.SweepDurableCompletions(context.Background(), now) }()
	select {
	case <-observed:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not inspect admission")
	}
	owned := models.WorkerResultOwnership{ID: uuid.NewString(), RequestID: uuid.NewString(), RequestHash: "hash", WorkerJobID: row.ID, SourceKeys: `[]`, SubmitPath: "/api/v1/jobs/submit", State: "uncertain"}
	if err := db.Create(&owned).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Delete(&owned) })
	close(resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if completionRow(t, db, row.ID).State != "active" {
		t.Fatal("concurrent admitted actor was expired")
	}
	assertReserved(t, db, row.ReservationID, "reserved")
}
