package tasks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/models"
	"pdfnest-backend/internal/storage"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestArtifactRecoveryObjectKeyDoesNotEmbedUntrustedTaskID(t *testing.T) {
	actionID := uuid.NewString()
	key := artifactRecoveryObjectKey("../../another-owner/output", actionID)
	if !storage.CanonicalObjectKey(key) {
		t.Fatalf("recovery key is not canonical: %q", key)
	}
	if strings.Contains(key, "another-owner") || strings.Contains(key, "..") {
		t.Fatalf("recovery key embeds untrusted task ID: %q", key)
	}
	if key == artifactRecoveryObjectKey("different-task", actionID) {
		t.Fatal("different task identities must not share a recovery key")
	}
}

func TestFinishArtifactRecoveryActionFencesStaleAndExpiredAttempts(t *testing.T) {
	db := openArtifactRecoveryQualificationDB(t)
	taskID := "recovery-fence-" + uuid.NewString()
	expiresAt := time.Now().UTC().Truncate(time.Microsecond).Add(time.Hour)
	snapshot := models.TaskCompletion{
		ID: taskID, OwnerIdentity: "user:synthetic-owner", DownloadToken: uuid.NewString(),
		State: "artifact_recovery_hold", ArtifactSHA256: strings.Repeat("a", 64), ArtifactBytes: 1,
		ResultKey: "outputs/tasks/" + taskID + "/original.md", ExpiresAt: expiresAt,
	}
	if err := db.Create(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	action := models.ArtifactRecoveryAction{
		ID: uuid.NewString(), OperationKey: "fence-" + uuid.NewString(), RequestFingerprint: strings.Repeat("b", 64),
		TaskID: taskID, AdminUserID: uuid.NewString(), Action: "recover", State: "processing", Reason: "synthetic lease fencing test",
		OwnerIdentity: snapshot.OwnerIdentity, OriginalResultKey: snapshot.ResultKey, ArtifactSHA256: snapshot.ArtifactSHA256,
		ArtifactBytes: snapshot.ArtifactBytes, OriginalExpiresAt: snapshot.ExpiresAt, Attempts: 2,
	}
	newLease := time.Now().UTC().Truncate(time.Microsecond).Add(4 * time.Minute)
	action.LeaseExpiresAt = &newLease
	if err := db.Create(&action).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Delete(&models.ArtifactRecoveryAction{}, "task_id = ?", taskID).Error
		_ = db.Delete(&models.TaskCompletion{}, "id = ?", taskID).Error
	})

	stale := action
	stale.Attempts = 1
	if err := assertArtifactRecoveryActionLease(context.Background(), stale, snapshot); err != ErrRecoveryLeaseLost {
		t.Fatalf("stale attempt must not begin a recovery copy, got %v", err)
	}
	if _, err := finishRecoveryAction(context.Background(), stale, snapshot, "recovered_pending_retention_decision", snapshot.ResultKey, "", "recovery-key"); err != ErrRecoveryLeaseLost {
		t.Fatalf("stale attempt must be fenced, got %v", err)
	}
	var unchanged models.ArtifactRecoveryAction
	if err := db.First(&unchanged, "id = ?", action.ID).Error; err != nil {
		t.Fatal(err)
	}
	if unchanged.State != "processing" || unchanged.Attempts != 2 || unchanged.LeaseExpiresAt == nil || !unchanged.LeaseExpiresAt.Equal(newLease) {
		t.Fatalf("stale attempt changed the newer lease: %+v", unchanged)
	}

	expired := newLease.Add(-5 * time.Minute)
	if err := db.Model(&models.ArtifactRecoveryAction{}).Where("id = ?", action.ID).Update("lease_expires_at", expired).Error; err != nil {
		t.Fatal(err)
	}
	currentAttempt := action
	currentAttempt.LeaseExpiresAt = &expired
	if _, err := finishRecoveryAction(context.Background(), currentAttempt, snapshot, "recovered_pending_retention_decision", snapshot.ResultKey, "", "recovery-key"); err != ErrRecoveryLeaseLost {
		t.Fatalf("expired lease must not finalize before a retry, got %v", err)
	}
	if err := db.Model(&models.ArtifactRecoveryAction{}).Where("id = ?", action.ID).Update("lease_expires_at", newLease).Error; err != nil {
		t.Fatal(err)
	}
	currentAttempt.LeaseExpiresAt = &newLease
	if _, err := finishRecoveryAction(context.Background(), currentAttempt, snapshot, "recovered_pending_retention_decision", snapshot.ResultKey, "", "recovery-key"); err != nil {
		t.Fatalf("current attempt should finalize while its lease is valid: %v", err)
	}
	if err := db.First(&unchanged, "id = ?", action.ID).Error; err != nil {
		t.Fatal(err)
	}
	if unchanged.State != "recovered_pending_retention_decision" || unchanged.Attempts != 2 {
		t.Fatalf("current attempt did not finalize as expected: %+v", unchanged)
	}
}

func TestArtifactRecoveryCloseClaimFencesConcurrentCompletion(t *testing.T) {
	db := openArtifactRecoveryQualificationDB(t)
	taskID := "recovery-close-race-" + uuid.NewString()
	adminID := uuid.NewString()
	expiresAt := time.Now().UTC().Truncate(time.Microsecond).Add(time.Hour)
	lease := time.Now().UTC().Truncate(time.Microsecond).Add(4 * time.Minute)
	snapshot := models.TaskCompletion{
		ID: taskID, OwnerIdentity: "user:synthetic-owner", DownloadToken: uuid.NewString(),
		State: "artifact_recovery_hold", ArtifactSHA256: strings.Repeat("a", 64), ArtifactBytes: 1,
		ResultKey: "outputs/tasks/" + taskID + "/original.md", ExpiresAt: expiresAt,
	}
	if err := db.Create(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	verify := models.ArtifactRecoveryAction{
		ID: uuid.NewString(), OperationKey: "verify-race-" + uuid.NewString(), RequestFingerprint: strings.Repeat("b", 64),
		TaskID: taskID, AdminUserID: adminID, Action: "verify", State: "processing", Reason: "synthetic concurrent close fencing",
		OwnerIdentity: snapshot.OwnerIdentity, OriginalResultKey: snapshot.ResultKey, ArtifactSHA256: snapshot.ArtifactSHA256,
		ArtifactBytes: snapshot.ArtifactBytes, OriginalExpiresAt: snapshot.ExpiresAt, Attempts: 1, LeaseExpiresAt: &lease,
	}
	closeAction := models.ArtifactRecoveryAction{
		ID: uuid.NewString(), OperationKey: "close-race-" + uuid.NewString(), RequestFingerprint: strings.Repeat("c", 64),
		TaskID: taskID, AdminUserID: adminID, Action: "close", State: "processing", Reason: "synthetic concurrent close fencing",
		OwnerIdentity: snapshot.OwnerIdentity, OriginalResultKey: snapshot.ResultKey, ArtifactSHA256: snapshot.ArtifactSHA256,
		ArtifactBytes: snapshot.ArtifactBytes, OriginalExpiresAt: snapshot.ExpiresAt, Attempts: 1, LeaseExpiresAt: &lease,
	}
	if err := db.Create(&verify).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&closeAction).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Delete(&models.ArtifactRecoveryAction{}, "task_id = ?", taskID).Error
		_ = db.Delete(&models.TaskCompletion{}, "id = ?", taskID).Error
	})

	if _, err := finishRecoveryAction(context.Background(), verify, snapshot, "verified_owner_source", snapshot.ResultKey, ""); err != ErrRecoveryCaseClosed {
		t.Fatalf("recovery completing after a close claim must be rejected, got %v", err)
	}
	var unchanged models.ArtifactRecoveryAction
	if err := db.First(&unchanged, "id = ?", verify.ID).Error; err != nil {
		t.Fatal(err)
	}
	if unchanged.State != "processing" {
		t.Fatalf("concurrent recovery attempt was finalized after close claim: %+v", unchanged)
	}
	if _, err := finishRecoveryAction(context.Background(), closeAction, snapshot, "case_closed_non_destructive", "", ""); err != nil {
		t.Fatalf("close action should finish its terminal decision: %v", err)
	}
	if _, err := ExecuteArtifactRecoveryAction(context.Background(), taskID, adminID, "after-close-"+uuid.NewString(), "verify", "new verification after terminal case closure"); err != ErrRecoveryCaseClosed {
		t.Fatalf("new operation key must not reopen a closed case, got %v", err)
	}
}

func TestArtifactRecoveryConcurrentRetryAndRestartHelper(t *testing.T) {
	if os.Getenv("ARTIFACT_RECOVERY_RESTART_HELPER") == "1" {
		runArtifactRecoveryRestartHelper(t)
		return
	}
	db := openArtifactRecoveryQualificationDB(t)
	t.Setenv("APP_ENV", "test")
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("ARTIFACT_RECOVERY_COPY_ENABLED", "")
	storageRoot := t.TempDir()
	t.Setenv("LOCAL_STORAGE_DIR", storageRoot)

	taskID := "recovery-retry-" + uuid.NewString()
	adminID := uuid.NewString()
	operationKey := "retry-" + uuid.NewString()
	reason := "recover an expired operation after concurrent retry"
	resultKey := "outputs/tasks/" + taskID + "/original.md"
	contents := []byte("# Synthetic recovery retry\n")
	digestBytes := sha256.Sum256(contents)
	digest := hex.EncodeToString(digestBytes[:])
	if _, _, err := storage.SaveLocalStream(context.Background(), resultKey, strings.NewReader(string(contents))); err != nil {
		t.Fatal(err)
	}
	snapshot := models.TaskCompletion{
		ID: taskID, OwnerIdentity: "user:synthetic-owner", DownloadToken: uuid.NewString(), State: "artifact_recovery_hold",
		ArtifactSHA256: digest, ArtifactBytes: int64(len(contents)), ResultKey: resultKey, ExpiresAt: time.Now().UTC().Truncate(time.Microsecond).Add(time.Hour),
	}
	if err := db.Create(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	oldLease := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	claim := models.ArtifactRecoveryAction{
		ID: uuid.NewString(), OperationKey: operationKey, RequestFingerprint: recoveryRequestFingerprint(taskID, adminID, "verify", reason),
		TaskID: taskID, AdminUserID: adminID, Action: "verify", State: "processing", Reason: reason,
		OwnerIdentity: snapshot.OwnerIdentity, OriginalResultKey: resultKey, ArtifactSHA256: digest,
		ArtifactBytes: int64(len(contents)), OriginalExpiresAt: snapshot.ExpiresAt, Attempts: 1, LeaseExpiresAt: &oldLease,
	}
	if err := db.Create(&claim).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Delete(&models.ArtifactRecoveryAction{}, "task_id = ?", taskID).Error
		_ = db.Delete(&models.TaskCompletion{}, "id = ?", taskID).Error
	})

	start := make(chan struct{})
	type result struct {
		outcome *ArtifactRecoveryOutcome
		err     error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			outcome, err := ExecuteArtifactRecoveryAction(context.Background(), taskID, adminID, operationKey, "verify", reason)
			results <- result{outcome: outcome, err: err}
		}()
	}
	close(start)
	var outcomes []result
	for range 2 {
		outcomes = append(outcomes, <-results)
	}
	for _, got := range outcomes {
		if got.err != nil && got.err != ErrRecoveryActionInProgress {
			t.Fatalf("concurrent retry returned unexpected error: %v", got.err)
		}
		if got.err == nil && got.outcome == nil {
			t.Fatal("successful retry returned no outcome")
		}
	}
	var recovered models.ArtifactRecoveryAction
	if err := db.First(&recovered, "id = ?", claim.ID).Error; err != nil {
		t.Fatal(err)
	}
	if recovered.State != "verified_owner_source" || recovered.Attempts != 2 {
		t.Fatalf("concurrent retries did not produce exactly one fenced attempt: %+v", recovered)
	}

	// Simulate process death after an incomplete copy: the durable attempt row is
	// expired, while the deterministic destination contains only partial bytes.
	restartTaskID := "recovery-restart-" + uuid.NewString()
	restartAdminID := uuid.NewString()
	restartOperationKey := "restart-" + uuid.NewString()
	restartReason := "resume an interrupted verified artifact copy"
	restartSourceKey := "outputs/tasks/" + restartTaskID + "/original.md"
	if _, _, err := storage.SaveLocalStream(context.Background(), restartSourceKey, strings.NewReader(string(contents))); err != nil {
		t.Fatal(err)
	}
	restartSnapshot := models.TaskCompletion{
		ID: restartTaskID, OwnerIdentity: "user:synthetic-owner", DownloadToken: uuid.NewString(), State: "artifact_recovery_hold",
		ArtifactSHA256: digest, ArtifactBytes: int64(len(contents)), ResultKey: restartSourceKey, ExpiresAt: time.Now().UTC().Truncate(time.Microsecond).Add(time.Hour),
	}
	if err := db.Create(&restartSnapshot).Error; err != nil {
		t.Fatal(err)
	}
	restartActionID := uuid.NewString()
	restartRecoveryKey := artifactRecoveryObjectKey(restartTaskID, restartActionID)
	if _, _, err := storage.SaveLocalStream(context.Background(), restartRecoveryKey, strings.NewReader("partial interrupted object")); err != nil {
		t.Fatal(err)
	}
	restartClaim := models.ArtifactRecoveryAction{
		ID: restartActionID, OperationKey: restartOperationKey,
		RequestFingerprint: recoveryRequestFingerprint(restartTaskID, restartAdminID, "recover", restartReason),
		TaskID:             restartTaskID, AdminUserID: restartAdminID, Action: "recover", State: "processing", Reason: restartReason,
		OwnerIdentity: restartSnapshot.OwnerIdentity, OriginalResultKey: restartSourceKey, ArtifactSHA256: digest,
		ArtifactBytes: int64(len(contents)), OriginalExpiresAt: restartSnapshot.ExpiresAt,
		RecoveryKey: restartRecoveryKey, Attempts: 1, LeaseExpiresAt: &oldLease,
	}
	if err := db.Create(&restartClaim).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Delete(&models.ArtifactRecoveryAction{}, "task_id = ?", restartTaskID).Error
		_ = db.Delete(&models.TaskCompletion{}, "id = ?", restartTaskID).Error
	})
	t.Setenv("ARTIFACT_RECOVERY_RESTART_HELPER", "1")
	t.Setenv("ARTIFACT_RECOVERY_RESTART_TASK_ID", restartTaskID)
	t.Setenv("ARTIFACT_RECOVERY_RESTART_ADMIN_ID", restartAdminID)
	t.Setenv("ARTIFACT_RECOVERY_RESTART_OPERATION_KEY", restartOperationKey)
	t.Setenv("ARTIFACT_RECOVERY_RESTART_REASON", restartReason)
	t.Setenv("ARTIFACT_RECOVERY_COPY_ENABLED", "true")
	cmd := exec.Command(os.Args[0], "-test.run=^TestArtifactRecoveryConcurrentRetryAndRestartHelper$")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("recovery restart subprocess failed: %v\n%s", err, output)
	}
	if err := storage.VerifyArtifact(context.Background(), restartRecoveryKey, digest, int64(len(contents))); err != nil {
		t.Fatalf("restarted process did not replace interrupted copy with verified bytes: %v", err)
	}
	var restarted models.ArtifactRecoveryAction
	if err := db.First(&restarted, "id = ?", restartActionID).Error; err != nil {
		t.Fatal(err)
	}
	if restarted.State != "recovered_pending_retention_decision" || restarted.Attempts != 2 || restarted.RecoveryKey != restartRecoveryKey {
		t.Fatalf("restarted recovery did not retain deterministic identity and one retry: %+v", restarted)
	}
}

func runArtifactRecoveryRestartHelper(t *testing.T) {
	t.Helper()
	openArtifactRecoveryQualificationDB(t)
	t.Setenv("APP_ENV", "test")
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("ARTIFACT_RECOVERY_COPY_ENABLED", "true")
	outcome, err := ExecuteArtifactRecoveryAction(
		context.Background(), os.Getenv("ARTIFACT_RECOVERY_RESTART_TASK_ID"),
		os.Getenv("ARTIFACT_RECOVERY_RESTART_ADMIN_ID"),
		os.Getenv("ARTIFACT_RECOVERY_RESTART_OPERATION_KEY"), "recover",
		os.Getenv("ARTIFACT_RECOVERY_RESTART_REASON"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if outcome == nil || outcome.State != "recovered_pending_retention_decision" {
		t.Fatalf("unexpected recovery outcome after process restart: %+v", outcome)
	}
}

func openArtifactRecoveryQualificationDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("ARTIFACT_RECOVERY_TEST_DSN"))
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL ARTIFACT_RECOVERY_TEST_DSN")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.TaskCompletion{}, &models.WorkerResultOwnership{}, &models.ArtifactRecoveryAction{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS public.backend_durability_schema(version varchar(64) PRIMARY KEY, applied_at timestamptz NOT NULL);
		INSERT INTO public.backend_durability_schema(version, applied_at) VALUES
		('20261029_backend_durability_v1', now()), ('20261105_artifact_recovery_v1', now()) ON CONFLICT DO NOTHING`).Error; err != nil {
		t.Fatal(err)
	}
	previousDB := config.DB
	config.DB = db
	t.Cleanup(func() {
		config.DB = previousDB
		if pool, err := db.DB(); err == nil {
			_ = pool.Close()
		}
	})
	return db
}
