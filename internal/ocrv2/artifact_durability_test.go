package ocrv2

import (
	"context"
	"os"
	"strings"
	"testing"

	"pdfnest-backend/internal/models"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/worker"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestArtifactRollbackRetainsInputAfterAmbiguousWorkerSubmission(t *testing.T) {
	dsn := os.Getenv("WORKER_DURABILITY_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable PostgreSQL DSN not supplied")
	}
	root, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	schema := "ocr_worker_guard_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err = root.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(postgres.Open(dsn+" search_path="+schema), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		worker.SetDurabilityStore(nil)
		sql, _ := db.DB()
		_ = sql.Close()
		_ = root.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		sql, _ = root.DB()
		_ = sql.Close()
	})
	if err = db.AutoMigrate(&models.WorkerResultOwnership{}); err != nil {
		t.Fatal(err)
	}
	worker.SetDurabilityStore(&worker.DurabilityStore{DB: db})
	t.Setenv("APP_ENV", "test")
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_STORAGE_DIR", t.TempDir())
	key := "jobs/ocr_v2/input/retained.pdf"
	if err = storage.SaveObjectBytes(context.Background(), key, []byte("%PDF-test"), "application/pdf"); err != nil {
		t.Fatal(err)
	}
	row := models.WorkerResultOwnership{ID: uuid.NewString(), RequestID: uuid.NewString(), RequestHash: "hash", SubmitPath: "/internal/ocr/v2/jobs", SourceKeys: `["` + key + `"]`, State: "submitting"}
	if err = db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	store := objectArtifactStore{}
	if err = store.DeleteObject(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if !storage.ObjectExists(context.Background(), key) {
		t.Fatal("uncertain Worker input deleted on submit error")
	}
	if err = db.Model(&row).Update("state", "deleted").Error; err != nil {
		t.Fatal(err)
	}
	if err = store.DeleteObject(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if storage.ObjectExists(context.Background(), key) {
		t.Fatal("unreferenced input rollback stopped working")
	}
}
