package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/auth"
	"pdfnest-backend/internal/models"
	"pdfnest-backend/internal/storage"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestArtifactRecoveryAdminWorkflowIsAuthorizedAuditedAndNonDestructive(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL DATABASE_URL")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&config.User{}, &models.TaskCompletion{}, &models.WorkerResultOwnership{}, &models.ArtifactRecoveryAction{}))
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS public.backend_durability_schema(version varchar(64) PRIMARY KEY, applied_at timestamptz NOT NULL);
		INSERT INTO public.backend_durability_schema(version, applied_at) VALUES
		('20261029_backend_durability_v1', now()), ('20261105_artifact_recovery_v1', now()) ON CONFLICT DO NOTHING`).Error)
	previousDB := config.DB
	config.DB = db
	t.Cleanup(func() {
		config.DB = previousDB
		if pool, err := db.DB(); err == nil {
			_ = pool.Close()
		}
	})
	t.Setenv("JWT_SECRET", "synthetic-artifact-recovery-test-secret")
	t.Setenv("APP_ENV", "test")
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("ARTIFACT_RECOVERY_COPY_ENABLED", "")
	storageRoot := t.TempDir()
	t.Setenv("LOCAL_STORAGE_DIR", storageRoot)

	adminUser := config.User{ID: uuid.NewString(), Email: "recovery-admin-" + uuid.NewString() + "@example.invalid", Role: "admin", Status: "active", EmailVerified: true}
	regularUser := config.User{ID: uuid.NewString(), Email: "recovery-user-" + uuid.NewString() + "@example.invalid", Role: "user", Status: "active", EmailVerified: true}
	require.NoError(t, db.Create(&adminUser).Error)
	require.NoError(t, db.Create(&regularUser).Error)
	t.Cleanup(func() {
		_ = db.Delete(&models.ArtifactRecoveryAction{}, "task_id LIKE ?", "hold-%").Error
		_ = db.Delete(&models.TaskCompletion{}, "id LIKE ?", "hold-%").Error
		_ = db.Delete(&models.WorkerResultOwnership{}, "worker_job_id LIKE ?", "hold-%").Error
		_ = db.Delete(&config.User{}, "id IN ?", []string{adminUser.ID, regularUser.ID}).Error
	})

	taskID := "hold-" + uuid.NewString()
	ownerIdentity := "user:" + regularUser.ID
	sourceKey := "jobs/markdown/output/" + taskID + ".md"
	contents := []byte("# Synthetic held task\n")
	digestBytes := sha256.Sum256(contents)
	digest := hex.EncodeToString(digestBytes[:])
	_, _, err = storage.SaveLocalStream(context.Background(), sourceKey, bytes.NewReader(contents))
	require.NoError(t, err)
	keepKey := "synthetic-unrelated/keep.txt"
	_, _, err = storage.SaveLocalStream(context.Background(), keepKey, bytes.NewReader([]byte("keep")))
	require.NoError(t, err)
	expiresAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	row := models.TaskCompletion{
		ID: taskID, OwnerIdentity: ownerIdentity, DownloadToken: uuid.NewString(), State: "artifact_recovery_hold",
		ResultKey: "outputs/tasks/" + taskID + "/../unrelated.txt", ArtifactSHA256: digest,
		ArtifactBytes: int64(len(contents)), ExpiresAt: expiresAt,
	}
	owned := models.WorkerResultOwnership{
		ID: uuid.NewString(), RequestID: uuid.NewString(), RequestHash: "synthetic-request-hash", WorkerJobID: taskID,
		OwnerIdentity: ownerIdentity, SubmitPath: "/api/v1/markdown/convert", SourceKeys: "[]", State: "terminal",
		StatusJSON: `{"status":"succeeded"}`, ResultKey: sourceKey,
	}
	require.NoError(t, db.Create(&row).Error)
	require.NoError(t, db.Create(&owned).Error)

	app := fiber.New()
	RegisterRoutes(app, NewController())
	issue := func(user config.User) *http.Cookie {
		token, err := auth.NewService().GenerateToken(user.ID, user.Role, user.SessionVersion)
		require.NoError(t, err)
		return &http.Cookie{Name: "auth_token", Value: token}
	}
	request := func(method, route, idempotencyKey, body string, user *config.User) (int, map[string]any) {
		req := httptest.NewRequest(method, route, bytes.NewBufferString(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if idempotencyKey != "" {
			req.Header.Set("Idempotency-Key", idempotencyKey)
		}
		if user != nil {
			req.AddCookie(issue(*user))
		}
		resp, err := app.Test(req, 5000)
		require.NoError(t, err)
		defer resp.Body.Close()
		var result map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&result)
		return resp.StatusCode, result
	}

	status, _ := request(http.MethodPost, "/admin/artifact-recovery/"+taskID, "", `{"action":"verify","reason":"review owner evidence"}`, nil)
	require.Equal(t, fiber.StatusUnauthorized, status)
	status, _ = request(http.MethodPost, "/admin/artifact-recovery/"+taskID, "", `{"action":"verify","reason":"review owner evidence"}`, &regularUser)
	require.Equal(t, fiber.StatusForbidden, status)

	status, payload := request(http.MethodGet, "/admin/artifact-recovery/"+taskID, "", "", &adminUser)
	require.Equal(t, fiber.StatusOK, status)
	require.Equal(t, false, payload["identity_valid"])
	require.NotContains(t, string(mustJSON(payload)), row.ResultKey, "admin API must not reveal the original unsafe key")

	verifyKey := "verify-" + uuid.NewString()
	status, payload = request(http.MethodPost, "/admin/artifact-recovery/"+taskID, verifyKey, `{"action":"verify","reason":"verify the original owner-matched worker evidence"}`, &adminUser)
	require.Equal(t, fiber.StatusOK, status)
	require.Equal(t, "verified_owner_source", payload["state"])
	status, replay := request(http.MethodPost, "/admin/artifact-recovery/"+taskID, verifyKey, `{"action":"verify","reason":"verify the original owner-matched worker evidence"}`, &adminUser)
	require.Equal(t, fiber.StatusOK, status)
	require.Equal(t, true, replay["idempotent_replay"])
	status, _ = request(http.MethodPost, "/admin/artifact-recovery/"+taskID, verifyKey, `{"action":"close","reason":"different action must not reuse key"}`, &adminUser)
	require.Equal(t, fiber.StatusConflict, status)

	policyKey := "policy-" + uuid.NewString()
	status, payload = request(http.MethodPost, "/admin/artifact-recovery/"+taskID, policyKey, `{"action":"recover","reason":"recovery awaits retention policy approval"}`, &adminUser)
	require.Equal(t, fiber.StatusOK, status)
	require.Equal(t, "recovery_blocked_by_policy", payload["state"])
	var policyAction models.ArtifactRecoveryAction
	require.NoError(t, db.Where("operation_key = ?", policyKey).First(&policyAction).Error)
	require.NotEmpty(t, policyAction.RecoveryKey)
	require.False(t, storage.ObjectExists(context.Background(), policyAction.RecoveryKey), "disabled recovery cannot create an object")
	t.Setenv("ARTIFACT_RECOVERY_COPY_ENABLED", "true")

	recoverKey := "recover-" + uuid.NewString()
	status, payload = request(http.MethodPost, "/admin/artifact-recovery/"+taskID, recoverKey, `{"action":"recover","reason":"restore only verified owner-matched output"}`, &adminUser)
	require.Equal(t, fiber.StatusOK, status)
	require.Equal(t, "recovered_pending_retention_decision", payload["state"])
	var recovery models.ArtifactRecoveryAction
	require.NoError(t, db.Where("operation_key = ?", recoverKey).First(&recovery).Error)
	require.NotEmpty(t, recovery.RecoveryKey)
	require.NoError(t, storage.VerifyArtifact(context.Background(), recovery.RecoveryKey, digest, int64(len(contents))))
	status, replay = request(http.MethodPost, "/admin/artifact-recovery/"+taskID, recoverKey, `{"action":"recover","reason":"restore only verified owner-matched output"}`, &adminUser)
	require.Equal(t, fiber.StatusOK, status)
	require.Equal(t, true, replay["idempotent_replay"])

	closeKey := "close-" + uuid.NewString()
	status, payload = request(http.MethodPost, "/admin/artifact-recovery/"+taskID, closeKey, `{"action":"close","reason":"close case without deleting or publishing artifacts"}`, &adminUser)
	require.Equal(t, fiber.StatusOK, status)
	require.Equal(t, "case_closed_non_destructive", payload["state"])
	status, payload = request(http.MethodGet, "/admin/artifact-recovery/"+taskID, "", "", &adminUser)
	require.Equal(t, fiber.StatusOK, status)
	require.Equal(t, "closed_non_destructive", payload["case_status"])

	var after models.TaskCompletion
	require.NoError(t, db.First(&after, "id = ?", taskID).Error)
	require.Equal(t, "artifact_recovery_hold", after.State, "expired completion is never silently republished")
	require.Equal(t, row.ResultKey, after.ResultKey, "original artifact identity remains immutable")
	require.True(t, expiresAt.Equal(after.ExpiresAt), "operator recovery does not extend retention")
	require.True(t, storage.ObjectExists(context.Background(), keepKey), "unrelated objects are never deleted")
	require.False(t, storage.ObjectExists(context.Background(), row.ResultKey), "unsafe original key is never read or deleted")
	data, err := os.ReadFile(filepath.Join(storageRoot, filepath.FromSlash(recovery.RecoveryKey)))
	require.NoError(t, err)
	require.Equal(t, contents, data)

	var actions []models.ArtifactRecoveryAction
	require.NoError(t, db.Where("task_id = ?", taskID).Order("created_at ASC").Find(&actions).Error)
	require.Len(t, actions, 4)
	for _, action := range actions {
		require.Equal(t, adminUser.ID, action.AdminUserID)
		require.NotEmpty(t, action.Reason)
		require.Equal(t, ownerIdentity, action.OwnerIdentity)
		require.Equal(t, row.ResultKey, action.OriginalResultKey)
	}
}

func mustJSON(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}
