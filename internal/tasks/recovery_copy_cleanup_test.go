package tasks

import (
	"bytes"
	"context"
	"errors"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"log"
	"pdfnest-backend/config"
	"pdfnest-backend/internal/models"
	"pdfnest-backend/internal/storage"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRecoveryAttemptKeysAreDistinctAndCanonical(t *testing.T) {
	a := uuid.NewString()
	seen := map[string]bool{}
	for i := 1; i <= 5; i++ {
		key := recoveryAttemptKey("../../victim", a, i)
		if seen[key] || strings.Contains(key, "victim") {
			t.Fatal("attempt identity collision")
		}
		seen[key] = true
	}
}

func TestRecoveryCopyCleanupRetainsTombstoneForLateCompletion(t *testing.T) {
	db := openArtifactRecoveryQualificationDB(t)
	action := models.ArtifactRecoveryAction{ID: uuid.NewString(), TaskID: "synthetic-" + uuid.NewString(), AdminUserID: uuid.NewString(), OperationKey: uuid.NewString(), RequestFingerprint: strings.Repeat("a", 64), Action: "recover", State: "recovery_deferred", OwnerIdentity: "owner-A", ArtifactSHA256: strings.Repeat("b", 64), ArtifactBytes: 5, OriginalExpiresAt: time.Now().Add(time.Hour), Attempts: 2}
	if err := db.Create(&action).Error; err != nil {
		t.Fatal(err)
	}
	row := RecoveryCopyObligation{ActionID: action.ID, Attempt: 1, TaskID: action.TaskID, OwnerIdentity: action.OwnerIdentity, ObjectKey: recoveryAttemptKey(action.TaskID, action.ID, 1), Digest: action.ArtifactSHA256, Bytes: 5, NextCheck: time.Now().Add(-time.Hour)}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		db.Delete(&RecoveryCopyObligation{}, "action_id = ?", action.ID)
		db.Delete(&models.ArtifactRecoveryAction{}, "id = ?", action.ID)
	}()
	calls := 0
	remove := func(ctx context.Context, key string) error {
		calls++
		if key != row.ObjectKey {
			t.Fatal("wrong key")
		}
		if calls == 1 {
			return errors.New("AccessDenied retention")
		}
		return nil
	}
	if err := sweepRecoveryCopies(context.Background(), db, 20, remove); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&row, "action_id = ?", action.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.LastDeleteConfirmedAt != nil || row.LastErrorCode == "" {
		t.Fatal("retention denial misreported")
	}
	for i := 0; i < 2; i++ {
		db.Model(&row).Update("next_check", time.Now().Add(-time.Hour))
		if err := sweepRecoveryCopies(context.Background(), db, 20, remove); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatalf("late PUT must remain collectible: calls=%d", calls)
	}
	var count int64
	db.Model(&RecoveryCopyObligation{}).Where("action_id = ?", action.ID).Count(&count)
	if count != 1 {
		t.Fatal("lost durable tombstone")
	}
}

func TestRecoveryCopyCleanupConcurrentWorkersAndTampering(t *testing.T) {
	db := openArtifactRecoveryQualificationDB(t)
	action := models.ArtifactRecoveryAction{ID: uuid.NewString(), TaskID: "synthetic-" + uuid.NewString(), AdminUserID: uuid.NewString(), OperationKey: uuid.NewString(), RequestFingerprint: strings.Repeat("a", 64), Action: "recover", State: "recovery_deferred", OwnerIdentity: "owner-A", ArtifactSHA256: strings.Repeat("b", 64), ArtifactBytes: 5, OriginalExpiresAt: time.Now().Add(time.Hour), Attempts: 1}
	if err := db.Create(&action).Error; err != nil {
		t.Fatal(err)
	}
	row := RecoveryCopyObligation{ActionID: action.ID, Attempt: 1, TaskID: action.TaskID, OwnerIdentity: action.OwnerIdentity, ObjectKey: recoveryAttemptKey(action.TaskID, action.ID, 1), Digest: action.ArtifactSHA256, Bytes: 5, NextCheck: time.Now().Add(-time.Hour)}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		db.Delete(&RecoveryCopyObligation{}, "action_id = ?", action.ID)
		db.Delete(&models.ArtifactRecoveryAction{}, "id = ?", action.ID)
	}()
	var mu sync.Mutex
	calls := 0
	remove := func(context.Context, string) error {
		mu.Lock()
		calls++
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		return nil
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- sweepRecoveryCopies(context.Background(), db, 1, remove) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("duplicate concurrent cleanup %d", calls)
	}
	db.Model(&row).Updates(map[string]any{"object_key": "outputs/tasks/victim.md", "next_check": time.Now().Add(-time.Hour)})
	if err := sweepRecoveryCopies(context.Background(), db, 1, remove); err != nil {
		t.Fatal(err)
	}
	var quarantined RecoveryCopyObligation
	db.First(&quarantined, "action_id = ?", action.ID)
	if quarantined.LastErrorCode != "identity_mismatch" || !quarantined.NextCheck.After(time.Now()) {
		t.Fatal("tampered obligation not quarantined")
	}
	if calls != 1 {
		t.Fatal("deleted unrelated key")
	}
}

func TestRecoveryCopyProtectsActiveLeaseAndRetainedEvidence(t *testing.T) {
	db := openArtifactRecoveryQualificationDB(t)
	future := time.Now().Add(time.Hour)
	action := models.ArtifactRecoveryAction{ID: uuid.NewString(), TaskID: "synthetic-" + uuid.NewString(), AdminUserID: uuid.NewString(), OperationKey: uuid.NewString(), RequestFingerprint: strings.Repeat("a", 64), Action: "recover", State: "processing", OwnerIdentity: "owner-A", ArtifactSHA256: strings.Repeat("b", 64), ArtifactBytes: 5, OriginalExpiresAt: future, Attempts: 1, LeaseExpiresAt: &future}
	action.RecoveryKey = recoveryAttemptKey(action.TaskID, action.ID, 1)
	if err := db.Create(&action).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		db.Delete(&RecoveryCopyObligation{}, "action_id = ?", action.ID)
		db.Delete(&models.ArtifactRecoveryAction{}, "id = ?", action.ID)
	}()
	if err := recordRecoveryCopy(context.Background(), action); err != nil {
		t.Fatal(err)
	}
	remove := func(context.Context, string) error { t.Fatal("removed live evidence"); return nil }
	for _, state := range []string{"processing", "recovered_pending_retention_decision"} {
		db.Model(&action).Update("state", state)
		db.Model(&RecoveryCopyObligation{}).Where("action_id = ?", action.ID).Update("next_check", time.Now().Add(-time.Hour))
		if err := sweepRecoveryCopies(context.Background(), db, 10, remove); err != nil {
			t.Fatal(err)
		}
	}
	// Download expiry is not an approved physical retention policy for a
	// verified recovery copy awaiting the owner's retention decision.
	db.Model(&action).Updates(map[string]any{"state": "recovered_pending_retention_decision", "original_expires_at": time.Now().Add(-time.Hour)})
	db.Model(&RecoveryCopyObligation{}).Where("action_id = ?", action.ID).Update("next_check", time.Now().Add(-time.Hour))
	if err := sweepRecoveryCopies(context.Background(), db, 10, remove); err != nil {
		t.Fatal(err)
	}
	// A catalogued reference pins the object even if it belongs to a different
	// owner; a cleanup action must never authorize deleting that owner's file.
	pinned := models.TaskCompletion{ID: "pinned-" + uuid.NewString(), OwnerIdentity: "owner-B", ResultKey: action.RecoveryKey, State: "completed", ExpiresAt: future}
	if err := db.Create(&pinned).Error; err != nil {
		t.Fatal(err)
	}
	defer db.Delete(&models.TaskCompletion{}, "id = ?", pinned.ID)
	db.Model(&action).Update("state", "recovery_deferred")
	db.Model(&RecoveryCopyObligation{}).Where("action_id = ?", action.ID).Update("next_check", time.Now().Add(-time.Hour))
	if err := sweepRecoveryCopies(context.Background(), db, 10, remove); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	action.Attempts = 2
	action.RecoveryKey = recoveryAttemptKey(action.TaskID, action.ID, 2)
	if err := recordRecoveryCopy(cancelled, action); err == nil {
		t.Fatal("cancelled intent accepted")
	}
	var n int64
	db.Model(&RecoveryCopyObligation{}).Where("action_id = ?", action.ID).Count(&n)
	if n != 1 {
		t.Fatal("cancelled upload intent changed")
	}
}

func TestRecoveryCopyCollectsLateUploadWithoutDeletingNewAttempt(t *testing.T) {
	db := openArtifactRecoveryQualificationDB(t)
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("APP_ENV", "test")
	t.Setenv("LOCAL_STORAGE_DIR", t.TempDir())
	action := models.ArtifactRecoveryAction{ID: uuid.NewString(), TaskID: "synthetic-" + uuid.NewString(), AdminUserID: uuid.NewString(), OperationKey: uuid.NewString(), RequestFingerprint: strings.Repeat("a", 64), Action: "recover", State: "recovery_deferred", OwnerIdentity: "owner-A", ArtifactSHA256: strings.Repeat("b", 64), ArtifactBytes: 5, OriginalExpiresAt: time.Now().Add(time.Hour), Attempts: 2}
	if err := db.Create(&action).Error; err != nil {
		t.Fatal(err)
	}
	row := RecoveryCopyObligation{ActionID: action.ID, Attempt: 1, TaskID: action.TaskID, OwnerIdentity: action.OwnerIdentity, ObjectKey: recoveryAttemptKey(action.TaskID, action.ID, 1), Digest: action.ArtifactSHA256, Bytes: 5, NextCheck: time.Now().Add(-time.Hour)}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		db.Delete(&RecoveryCopyObligation{}, "action_id = ?", action.ID)
		db.Delete(&models.ArtifactRecoveryAction{}, "id = ?", action.ID)
	}()
	newer := recoveryAttemptKey(action.TaskID, action.ID, 2)
	if _, _, err := storage.SaveLocalStream(context.Background(), newer, strings.NewReader("new attempt")); err != nil {
		t.Fatal(err)
	}
	if err := SweepRecoveryCopies(context.Background(), db, 10); err != nil {
		t.Fatal(err)
	}
	// Provider PUT becomes visible after the first DELETE already confirmed absence.
	if _, _, err := storage.SaveLocalStream(context.Background(), row.ObjectKey, strings.NewReader("late upload")); err != nil {
		t.Fatal(err)
	}
	db.Model(&row).Update("next_check", time.Now().Add(-time.Hour))
	if err := SweepRecoveryCopies(context.Background(), db, 10); err != nil {
		t.Fatal(err)
	}
	if storage.ObjectExists(context.Background(), row.ObjectKey) {
		t.Fatal("late orphan survived retry")
	}
	if !storage.ObjectExists(context.Background(), newer) {
		t.Fatal("new attempt erased")
	}
}

func TestRecoveryCopySQLFailureDoesNotLogPrivateObjectIdentity(t *testing.T) {
	db := openArtifactRecoveryQualificationDB(t)
	until := time.Now().Add(time.Minute)
	action := models.ArtifactRecoveryAction{ID: uuid.NewString(), TaskID: "synthetic-private-" + uuid.NewString(), AdminUserID: uuid.NewString(), OperationKey: uuid.NewString(), RequestFingerprint: strings.Repeat("a", 64), Action: "recover", State: "processing", OwnerIdentity: "synthetic-private-owner", ArtifactSHA256: strings.Repeat("b", 64), ArtifactBytes: 5, OriginalExpiresAt: until, Attempts: 1, LeaseExpiresAt: &until}
	action.RecoveryKey = recoveryAttemptKey(action.TaskID, action.ID, 1)
	if err := db.Create(&action).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		db.Delete(&RecoveryCopyObligation{}, "action_id = ?", action.ID)
		db.Delete(&models.ArtifactRecoveryAction{}, "id = ?", action.ID)
	}()
	var captured bytes.Buffer
	config.DB = db.Session(&gorm.Session{Logger: logger.New(log.New(&captured, "", 0), logger.Config{LogLevel: logger.Error})})
	if err := recordRecoveryCopy(context.Background(), action); err != nil {
		t.Fatal(err)
	}
	if err := recordRecoveryCopy(context.Background(), action); err == nil {
		t.Fatal("expected duplicate obligation SQL failure")
	}
	if captured.Len() != 0 {
		t.Fatalf("private recovery SQL entered logger: %s", captured.String())
	}
	var n int64
	db.Model(&RecoveryCopyObligation{}).Where("action_id = ?", action.ID).Count(&n)
	if n != 1 {
		t.Fatal("SQL failure lost durable obligation")
	}
}
