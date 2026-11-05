package user

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/gofiber/fiber/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"pdfnest-backend/internal/auth"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"pdfnest-backend/config"
	analyzerModels "pdfnest-backend/internal/analyzer/models"
	backendModels "pdfnest-backend/internal/models"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/studio"
	studioModels "pdfnest-backend/internal/studio/models"
)

func cleanupDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("BACKEND_DURABILITY_TEST_DSN")
	if dsn == "" {
		t.Skip("requires explicit private BACKEND_DURABILITY_TEST_DSN")
	}
	require.Contains(t, dsn, "host=/tmp/platen-")
	require.Contains(t, dsn, "user=platen_")
	root, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	var address sql.NullString
	require.NoError(t, root.QueryRow("SELECT inet_server_addr()::text").Scan(&address))
	require.False(t, address.Valid)
	name := "account_cleanup_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = root.Exec(`CREATE DATABASE "` + name + `"`)
	require.NoError(t, err)
	fields := strings.Fields(dsn)
	for i, field := range fields {
		if strings.HasPrefix(field, "dbname=") {
			fields[i] = "dbname=" + name
		}
	}
	db, err := gorm.Open(postgres.Open(strings.Join(fields, " ")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, config.RunSchemaMigrations(db))
	require.NoError(t, db.AutoMigrate(&config.AccountStorageCleanup{}))
	t.Cleanup(func() {
		pool, _ := db.DB()
		_ = pool.Close()
		_, _ = root.Exec(`DROP DATABASE "` + name + `" WITH (FORCE)`)
		_ = root.Close()
	})
	return db
}

func TestAccountDeletionOutboxRollsBackAndRetainsActiveOwnership(t *testing.T) {
	db := cleanupDB(t)
	ctx := context.Background()
	id := uuid.NewString()
	u := config.User{ID: id, Email: id + "@example.invalid", Status: "active", EmailVerified: true}
	require.NoError(t, db.Create(&u).Error)
	task := backendModels.TaskCompletion{ID: uuid.NewString(), OwnerIdentity: id, DownloadToken: "token", State: "active", ResultKey: "outputs/tasks/a/result.pdf"}
	require.NoError(t, db.Create(&task).Error)
	require.NoError(t, db.Exec(`CREATE OR REPLACE FUNCTION fail_cleanup_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$`).Error)
	require.NoError(t, db.Exec(`CREATE TRIGGER fail_cleanup_test BEFORE INSERT ON account_storage_cleanups FOR EACH ROW EXECUTE FUNCTION fail_cleanup_test()`).Error)
	err := deleteAccountWithCleanup(ctx, db, id, 0)
	require.Error(t, err)
	var persisted config.User
	require.NoError(t, db.First(&persisted, "id = ?", id).Error)
	require.NoError(t, db.Exec(`DROP TRIGGER fail_cleanup_test ON account_storage_cleanups`).Error)
	require.NoError(t, db.Exec(`DROP FUNCTION fail_cleanup_test()`).Error)
	require.NoError(t, deleteAccountWithCleanup(ctx, db, id, 0))
	require.ErrorIs(t, db.First(&persisted, "id = ?", id).Error, gorm.ErrRecordNotFound)
	var intent config.AccountStorageCleanup
	require.NoError(t, db.First(&intent, "user_id = ?", id).Error)
	w := NewAccountCleanupWorker(db)
	require.Error(t, w.RunOnceAt(ctx, time.Now().Add(time.Hour)))
	var check backendModels.TaskCompletion
	require.NoError(t, db.First(&check, "id = ?", task.ID).Error)
	require.Equal(t, "active", check.State)
	require.NoError(t, db.Model(&task).Update("state", "completed").Error)
	require.NoError(t, w.RunOnceAt(ctx, time.Now().Add(2*time.Hour)))
	require.NoError(t, db.First(&check, "id = ?", task.ID).Error)
	require.Equal(t, "deleted", check.State)
	var physical []studioModels.StudioStorageCleanupTask
	require.NoError(t, db.Where("object_key = ?", task.ResultKey).Find(&physical).Error)
	require.Len(t, physical, 1)
	require.NoError(t, w.RunOnceAt(ctx, time.Now().Add(3*time.Hour)))
	require.NoError(t, db.Where("object_key = ?", task.ResultKey).Find(&physical).Error)
	require.Len(t, physical, 1)
}

func TestAccountCleanupLeaseRecoveryAndCrossOwnerPin(t *testing.T) {
	db := cleanupDB(t)
	ctx := context.Background()
	id := uuid.NewString()
	other := uuid.NewString()
	now := time.Now().UTC()
	key := "outputs/tasks/" + uuid.NewString() + "/result.pdf"
	require.NoError(t, db.Create(&config.User{ID: id, Email: id + "@example.invalid", Status: "active", EmailVerified: true}).Error)
	task := backendModels.TaskCompletion{ID: uuid.NewString(), OwnerIdentity: id, DownloadToken: "token", State: "completed", ResultKey: key}
	require.NoError(t, db.Create(&task).Error)
	pin := backendModels.WorkerResultOwnership{ID: uuid.NewString(), RequestID: uuid.NewString(), RequestHash: "hash", OwnerIdentity: other, SubmitPath: "/internal/ocr/v2/jobs", SourceKeys: `["` + key + `"]`, State: "active", NextAttemptAt: now}
	require.NoError(t, db.Create(&pin).Error)
	require.NoError(t, deleteAccountWithCleanup(ctx, db, id, 0))
	future := now.Add(time.Hour)
	require.NoError(t, db.Model(&config.AccountStorageCleanup{}).Where("user_id = ?", id).Updates(map[string]any{"lease_token": uuid.NewString(), "lease_expires_at": future}).Error)
	w := NewAccountCleanupWorker(db)
	require.NoError(t, w.RunOnceAt(ctx, now.Add(time.Minute)))
	var unchanged backendModels.TaskCompletion
	require.NoError(t, db.First(&unchanged, "id = ?", task.ID).Error)
	require.Equal(t, "completed", unchanged.State)
	require.NoError(t, w.RunOnceAt(ctx, now.Add(2*time.Hour)))
	var queued studioModels.StudioStorageCleanupTask
	require.NoError(t, db.First(&queued, "object_key = ?", key).Error)
	calls := 0
	worker := studio.NewStorageCleanupWorker(studio.NewRepository(db), studio.StorageObjectDeleterFunc(func(context.Context, string) error { calls++; return nil }))
	require.False(t, worker.RunTaskAt(ctx, queued, now.Add(3*time.Hour)))
	require.Zero(t, calls)
	require.NoError(t, db.Model(&pin).Update("state", "deleted").Error)
	require.True(t, worker.RunTaskAt(ctx, queued, now.Add(4*time.Hour)))
	require.Equal(t, 1, calls)
	require.NoError(t, db.First(&unchanged, "id = ?", task.ID).Error)
	require.Equal(t, key, unchanged.ResultKey, "immutable artifact and billing evidence survives")
}

func TestAccountCleanupDiscoversStudioAnalyzerWorkerAndUnregisteredUploads(t *testing.T) {
	db := cleanupDB(t)
	ctx := context.Background()
	id := uuid.NewString()
	now := time.Now().UTC()
	t.Setenv("APP_ENV", "test")
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_STORAGE_DIR", t.TempDir())
	require.NoError(t, db.Create(&config.User{ID: id, Email: id + "@example.invalid", Status: "active", EmailVerified: true}).Error)
	uid := uuid.MustParse(id)
	docID := uuid.New()
	versionID := uuid.New()
	sessionID := uuid.New()
	prefix := "studio/test/" + uuid.NewString() + "/"
	doc := studioModels.StudioDocument{ID: docID, OriginalFileName: "source.pdf", FileSize: 1, InitialPageCount: 1}
	asset := studioModels.StudioAsset{ID: uuid.NewString(), DocumentID: docID, AssetType: "source_pdf", R2Key: prefix + "source.pdf", ByteSize: 1, MimeType: "application/pdf"}
	version := studioModels.StudioVersion{ID: versionID, DocumentID: docID, VersionNumber: 1, Status: "ready", OperationType: "initial", VirtualModel: studioModels.JSON(`{}`), CreatedAt: now}
	session := studioModels.StudioSession{ID: sessionID, UserID: &uid, DocumentID: docID, ActiveVersionID: versionID, CreatedAt: now, LastAccessedAt: now, ExpiresAt: now.Add(time.Hour)}
	repo := studio.NewRepository(db)
	require.NoError(t, repo.CreateDocumentWithInitialVersion(ctx, &doc, &asset, &version, &session))
	job := studioModels.StudioJob{ID: uuid.New(), DocumentID: docID, SessionID: sessionID, BaseVersionID: versionID, WorkerJobID: uuid.NewString(), JobType: "test", Status: "failed", IdempotencyKey: uuid.NewString(), Parameters: studioModels.JSON(`{}`), SourceKey: prefix + "job-source.pdf", PayloadKey: prefix + "payload.json", PendingObjectKeys: studioModels.JSON(`["` + prefix + `pending.pdf"]`), CreatedAt: now, UpdatedAt: now}
	require.NoError(t, db.Create(&job).Error)
	exp := studioModels.StudioExport{ID: uuid.New(), DocumentID: docID, VersionID: versionID, ExportFormat: "pdf", R2Key: prefix + "export.pdf", ByteSize: 1, ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	require.NoError(t, db.Create(&exp).Error)
	analyzerKey := "owned/legacy/" + uuid.NewString() + ".zip"
	as := analyzerModels.AnalyzerSession{ID: uuid.NewString(), OwnerIdentity: id, SourceType: "zip", StorageKey: &analyzerKey, RepositoryName: "repository", Status: "COMPLETED"}
	require.NoError(t, db.Create(&as).Error)
	worker := backendModels.WorkerResultOwnership{ID: uuid.NewString(), RequestID: uuid.NewString(), RequestHash: "hash", OwnerIdentity: id, SubmitPath: "/internal/ocr/v2/jobs", SourceKeys: `["` + prefix + `worker-source.pdf"]`, State: "terminal", ResultKey: prefix + "worker-result.pdf", MetadataKey: prefix + "metadata.json", NextAttemptAt: now}
	require.NoError(t, db.Create(&worker).Error)
	orphan := storage.NewOwnedKey(id, "image_to_text_pdf", ".png")
	_, _, err := storage.SaveLocalStream(ctx, orphan, strings.NewReader("image"))
	require.NoError(t, err)
	// Include already soft-deleted sessions; their graph must not become orphaned.
	require.NoError(t, db.Delete(&session).Error)
	require.NoError(t, deleteAccountWithCleanup(ctx, db, id, 0))
	require.NoError(t, NewAccountCleanupWorker(db).RunOnceAt(ctx, now.Add(time.Hour)))
	var keys []string
	require.NoError(t, db.Model(&studioModels.StudioStorageCleanupTask{}).Pluck("object_key", &keys).Error)
	require.ElementsMatch(t, []string{asset.R2Key, job.SourceKey, job.PayloadKey, prefix + "pending.pdf", exp.R2Key, analyzerKey, prefix + "worker-source.pdf", worker.ResultKey, worker.MetadataKey, orphan}, keys)
	var remaining int64
	require.NoError(t, db.Model(&studioModels.StudioDocument{}).Where("id = ?", docID).Count(&remaining).Error)
	require.Zero(t, remaining)
	require.NoError(t, db.Model(&analyzerModels.AnalyzerSession{}).Where("id = ?", as.ID).Count(&remaining).Error)
	require.Zero(t, remaining)
}

func TestDeleteAccountHTTPUsesAuthenticatedSessionAndFailsClosedWithoutLedger(t *testing.T) {
	db := cleanupDB(t)
	previous := config.DB
	config.DB = db
	t.Cleanup(func() { config.DB = previous })
	t.Setenv("JWT_SECRET", "account-delete-controller-test-secret")
	require.NoError(t, db.Exec(`CREATE TABLE backend_durability_schema(version text PRIMARY KEY, applied_at timestamptz NOT NULL); INSERT INTO backend_durability_schema VALUES ('20261105_account_cleanup_v1',now())`).Error)
	makeUser := func() config.User {
		u := config.User{ID: uuid.NewString(), Email: uuid.NewString() + "@example.invalid", Status: "active", EmailVerified: true}
		require.NoError(t, db.Create(&u).Error)
		return u
	}
	owner, other := makeUser(), makeUser()
	token, err := auth.NewService().GenerateToken(owner.ID, owner.Role, owner.SessionVersion)
	require.NoError(t, err)
	app := fiber.New()
	RegisterRoutes(app, NewController())
	request := func(token string) *http.Response {
		r := httptest.NewRequest(http.MethodDelete, "/user/account?user_id="+other.ID, nil)
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "auth_token", Value: token})
		}
		response, err := app.Test(r)
		require.NoError(t, err)
		return response
	}
	response := request("")
	require.Equal(t, 401, response.StatusCode)
	response.Body.Close()
	response = request(token)
	require.Equal(t, 200, response.StatusCode)
	var payload map[string]any
	require.NoError(t, json.NewDecoder(response.Body).Decode(&payload))
	response.Body.Close()
	require.Equal(t, "pending", payload["storage_cleanup"])
	var intent config.AccountStorageCleanup
	require.NoError(t, db.First(&intent, "user_id = ?", owner.ID).Error)
	var untouched config.User
	require.NoError(t, db.First(&untouched, "id = ?", other.ID).Error)
	response = request(token)
	require.Equal(t, 401, response.StatusCode)
	response.Body.Close()
	token, err = auth.NewService().GenerateToken(other.ID, other.Role, other.SessionVersion)
	require.NoError(t, err)
	require.NoError(t, db.Exec("DROP TABLE account_storage_cleanups").Error)
	response = request(token)
	require.Equal(t, 503, response.StatusCode)
	response.Body.Close()
	require.NoError(t, db.First(&untouched, "id = ?", other.ID).Error)
}
