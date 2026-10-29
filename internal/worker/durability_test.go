package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pdfnest-backend/internal/models"
	"pdfnest-backend/internal/storage"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func durabilityDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("WORKER_DURABILITY_TEST_DSN")
	if dsn == "" {
		t.Skip("disposable PostgreSQL DSN not supplied")
	}
	root, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	schema := "worker_durability_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err = root.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(postgres.Open(dsn+" search_path="+schema), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&models.WorkerResultOwnership{}, &models.TaskCompletion{}); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		"CREATE TABLE studio_storage_object_retirements (object_key varchar(512) PRIMARY KEY, created_at timestamptz)",
		"CREATE TABLE studio_assets (id text PRIMARY KEY, r2_key text)",
		"CREATE TABLE studio_exports (id text PRIMARY KEY, r2_key text)",
		"CREATE TABLE studio_jobs (id text PRIMARY KEY, worker_job_id text, reconciled_at timestamptz, source_key text, payload_key text, pending_object_keys jsonb)",
	} {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		SetDurabilityStore(nil)
		sql, _ := db.DB()
		_ = sql.Close()
		_ = root.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		sql, _ = root.DB()
		_ = sql.Close()
	})
	return db
}
func testDurableClient(t *testing.T, s *DurabilityStore, handler http.HandlerFunc) *http.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	s.BaseURL = server.URL
	SetDurabilityStore(s)
	return &http.Client{Transport: &signingTransport{transport: server.Client().Transport}, Timeout: 5 * time.Second}
}
func postDurable(t *testing.T, c *http.Client, url, requestID, source string) (*http.Response, error) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"request_id": requestID, "owner_identity": "user:owned", "profile": "OCR_TEXT_V2", "source_key": source})
	req, _ := http.NewRequest(http.MethodPost, url+"/internal/ocr/v2/jobs", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	return c.Do(req)
}
func readResponse(t *testing.T, r *http.Response) []byte {
	t.Helper()
	defer r.Body.Close()
	b, e := io.ReadAll(r.Body)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func seedTerminal(t *testing.T, db *gorm.DB, key string, expiry time.Time) models.WorkerResultOwnership {
	t.Helper()
	id := uuid.NewString()
	if key == "" {
		key = "jobs/ocr_v2/results/" + id + ".json"
	}
	row := models.WorkerResultOwnership{ID: uuid.NewString(), RequestID: uuid.NewString(), RequestHash: "hash", WorkerJobID: id, OwnerIdentity: "user:owned", SubmitPath: "/internal/ocr/v2/jobs", Profile: "OCR_TEXT_V2", SourceKeys: `["owned/source.pdf"]`, State: "terminal", ResultKey: key, ExpiresAt: &expiry, NextAttemptAt: expiry, StatusJSON: `{"job_id":"` + id + `","status":"succeeded","owner_identity":"user:owned","result_key":"` + key + `"}`}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}
func TestDurabilityAdmissionPrecedesDispatchAndDuplicateDoesNotResubmit(t *testing.T) {
	db := durabilityDB(t)
	s := &DurabilityStore{DB: db}
	var calls atomic.Int32
	jobID := uuid.NewString()
	c := testDurableClient(t, s, func(w http.ResponseWriter, r *http.Request) {
		var count int64
		if err := db.Model(&models.WorkerResultOwnership{}).Where("state = ?", "submitting").Count(&count).Error; err != nil || count != 1 {
			t.Errorf("network preceded admission: count=%d err=%v", count, err)
		}
		body, _ := io.ReadAll(r.Body)
		digest := bodySignature(r.Method, r.URL.Path, r.Header.Get("X-Worker-Timestamp"), r.Header.Get("X-Worker-Nonce"), r.Header.Get("X-Worker-Body-Hash"), GetWorkerSharedSecret())
		if digest != r.Header.Get("X-Worker-Signature") {
			t.Error("body signature changed")
		}
		if !bytes.Contains(body, []byte(`"owner_identity":"user:owned"`)) {
			t.Error("body changed")
		}
		calls.Add(1)
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"job_id":"`+jobID+`","status":"queued","owner_identity":"user:owned"}`)
	})
	for i := 0; i < 2; i++ {
		r, e := postDurable(t, c, s.BaseURL, "stable-request", "owned/source.pdf")
		if e != nil {
			t.Fatal(e)
		}
		b := readResponse(t, r)
		if !bytes.Contains(b, []byte(jobID)) {
			t.Fatal(string(b))
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("duplicate dispatched %d times", calls.Load())
	}
	var row models.WorkerResultOwnership
	if err := db.First(&row).Error; err != nil || row.State != "active" || row.WorkerJobID != jobID {
		t.Fatalf("ACK not durable: %+v %v", row, err)
	}
}
func TestDurabilityAmbiguousACKSurvivesRestartAndNeverBlindlyRetries(t *testing.T) {
	db := durabilityDB(t)
	s := &DurabilityStore{DB: db}
	var calls atomic.Int32
	c := testDurableClient(t, s, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		h := w.(http.Hijacker)
		conn, _, _ := h.Hijack()
		_ = conn.Close()
	})
	if _, err := postDurable(t, c, s.BaseURL, "lost-ack", "owned/source.pdf"); err == nil {
		t.Fatal("uncertain dispatch accepted")
	}
	SetDurabilityStore(&DurabilityStore{DB: db})
	if _, err := postDurable(t, c, s.BaseURL, "lost-ack", "owned/source.pdf"); err == nil {
		t.Fatal("uncertain dispatch retried")
	}
	if calls.Load() != 1 {
		t.Fatalf("uncertain dispatch resent %d", calls.Load())
	}
	var row models.WorkerResultOwnership
	_ = db.First(&row).Error
	if row.State != "submitting" || row.SourceKeys != `["owned/source.pdf"]` {
		t.Fatalf("uncertain source pin lost: %+v", row)
	}
}
func TestDurabilityPollingAndRedisExpiryFallbackPreservesOwnerAndDownload(t *testing.T) {
	db := durabilityDB(t)
	t.Setenv("APP_ENV", "test")
	t.Setenv("LOCAL_STORAGE_DIR", t.TempDir())
	t.Setenv("STORAGE_MODE", "local")
	s := &DurabilityStore{DB: db}
	jobID := uuid.NewString()
	key := "jobs/ocr_v2/results/" + jobID + ".json"
	expired := atomic.Bool{}
	c := testDurableClient(t, s, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(202)
			_, _ = io.WriteString(w, `{"job_id":"`+jobID+`","status":"queued"}`)
			return
		}
		if expired.Load() {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"job_id":"`+jobID+`","status":"succeeded","owner_identity":"user:owned","profile":"OCR_TEXT_V2","result_key":"`+key+`"}`)
	})
	r, err := postDurable(t, c, s.BaseURL, "poll-request", "owned/source.pdf")
	if err != nil {
		t.Fatal(err)
	}
	_ = readResponse(t, r)
	if err = storage.SaveObjectBytes(context.Background(), key, []byte(`{"text":"retained"}`), "application/json"); err != nil {
		t.Fatal(err)
	}
	s.HTTP = c
	if err = s.runOnce(context.Background(), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	expired.Store(true)
	SetDurabilityStore(&DurabilityStore{DB: db})
	for _, suffix := range []string{"", "/result"} {
		req, _ := http.NewRequest(http.MethodGet, s.BaseURL+"/internal/ocr/v2/jobs/"+jobID+suffix, nil)
		r, err = c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b := readResponse(t, r)
		if r.StatusCode != 200 {
			t.Fatalf("expired metadata lost durable result: %d %s", r.StatusCode, b)
		}
		if suffix == "" && !bytes.Contains(b, []byte(`"owner_identity":"user:owned"`)) {
			t.Fatal("owner lost")
		}
		if suffix != "" && !bytes.Contains(b, []byte("retained")) {
			t.Fatal("artifact lost")
		}
	}
}
func TestDurabilityCleanupRetryRestartAndDuplicate(t *testing.T) {
	db := durabilityDB(t)
	now := time.Now().UTC()
	row := seedTerminal(t, db, "", now.Add(-time.Hour))
	var deletes atomic.Int32
	s := &DurabilityStore{DB: db, DeleteObject: func(context.Context, string) error {
		if deletes.Add(1) == 1 {
			return errors.New("storage unavailable")
		}
		return nil
	}}
	if err := s.cleanup(context.Background(), row.ID, now); err != nil {
		t.Fatal(err)
	}
	_ = db.First(&row, "id = ?", row.ID).Error
	if row.State != "deleting" || row.Attempts != 1 || row.LastError == "" {
		t.Fatalf("delete intent not durable: %+v", row)
	}
	s = &DurabilityStore{DB: db, DeleteObject: s.DeleteObject}
	for i := 0; i < 2; i++ {
		if err := s.cleanup(context.Background(), row.ID, now.Add(2*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.First(&row, "id = ?", row.ID).Error
	if row.State != "deleted" || deletes.Load() != 2 {
		t.Fatalf("duplicate cleanup: %+v deletes=%d", row, deletes.Load())
	}
}

func TestDurabilityCancelledTaskForwardsAfterRestartAndLostAcknowledgement(t *testing.T) {
	for _, scenario := range []string{"cancelled", "completed", "different_owner"} {
		t.Run(scenario, func(t *testing.T) {
			db := durabilityDB(t)
			t.Setenv("APP_ENV", "test")
			now := time.Now().UTC()
			id := uuid.NewString()
			owned := models.WorkerResultOwnership{ID: uuid.NewString(), RequestID: uuid.NewString(), RequestHash: "hash", WorkerJobID: id, OwnerIdentity: "user:owner", SubmitPath: "/api/v1/jobs/submit", SourceKeys: `[]`, State: "active", NextAttemptAt: now.Add(-time.Second)}
			if err := db.Create(&owned).Error; err != nil {
				t.Fatal(err)
			}
			task := models.TaskCompletion{ID: id, OwnerIdentity: owned.OwnerIdentity, State: "cancelled", ExpiresAt: now.Add(time.Hour)}
			if scenario == "completed" {
				task.State = "completed"
			}
			if scenario == "different_owner" {
				task.OwnerIdentity = "user:other"
			}
			if err := db.Create(&task).Error; err != nil {
				t.Fatal(err)
			}
			var cancellations atomic.Int32
			store := &DurabilityStore{DB: db}
			client := testDurableClient(t, store, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					if r.URL.Path != "/api/v1/jobs/"+id+"/cancel" || r.Header.Get("X-Owner-Identity") != owned.OwnerIdentity || r.Header.Get("X-Worker-Body-Hash") == "" {
						t.Error("cancellation lost exact ownership or signing")
					}
					if cancellations.Add(1) == 1 {
						w.WriteHeader(503)
						return
					}
					_, _ = io.WriteString(w, `{"id":"`+id+`","status":"cancel_requested"}`)
					return
				}
				_, _ = io.WriteString(w, `{"id":"`+id+`","status":"queued"}`)
			})
			store.HTTP = client
			first := store.runOnce(t.Context(), now)
			restarted := &DurabilityStore{DB: db, HTTP: client, BaseURL: store.BaseURL}
			second := restarted.runOnce(t.Context(), now.Add(2*time.Minute))
			if scenario == "cancelled" {
				if first == nil || second != nil || cancellations.Load() != 2 {
					t.Fatalf("durable cancel not retried: first=%v second=%v sends=%d", first, second, cancellations.Load())
				}
			} else if first != nil || second != nil || cancellations.Load() != 0 {
				t.Fatalf("unowned or terminal task cancelled: %v %v sends=%d", first, second, cancellations.Load())
			}
			if err := db.First(&owned, "id = ?", owned.ID).Error; err != nil {
				t.Fatal(err)
			}
			if owned.State != "active" || owned.ResultKey != "" {
				t.Fatal("cancellation prematurely released result ownership")
			}
		})
	}
}
func TestDurabilityCleanupPinsUnsettledAndSharedArtifacts(t *testing.T) {
	db := durabilityDB(t)
	now := time.Now()
	row := seedTerminal(t, db, "", now.Add(-time.Hour))
	var deletes atomic.Int32
	s := &DurabilityStore{DB: db, DeleteObject: func(context.Context, string) error { deletes.Add(1); return nil }}
	task := models.TaskCompletion{ID: "settlement", OwnerIdentity: row.OwnerIdentity, DownloadToken: "token", State: "publishing", ResultKey: "outputs/tasks/settlement/output.pdf", ExpiresAt: now.Add(-time.Hour)}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.cleanup(context.Background(), row.ID, now); err != nil {
		t.Fatal(err)
	}
	if deletes.Load() != 0 {
		t.Fatal("unsettled artifact deleted")
	}
	_ = db.Model(&task).Update("state", "completed").Error
	pin := models.WorkerResultOwnership{ID: uuid.NewString(), RequestID: uuid.NewString(), RequestHash: "h", SourceKeys: mustJSONStringArray(row.ResultKey), SubmitPath: "/api/v1/editor/extract", State: "submitting"}
	if err := db.Create(&pin).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.cleanup(context.Background(), row.ID, now); err != nil {
		t.Fatal(err)
	}
	if deletes.Load() != 0 {
		t.Fatal("shared live input deleted")
	}
	if err := db.Model(&pin).Update("state", "deleted").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.cleanup(context.Background(), row.ID, now.Add(16*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if deletes.Load() != 1 {
		t.Fatal("unreferenced artifact never reclaimed")
	}
}
func TestDurabilityRejectsSourceAliasesForeignKeysAndTraversal(t *testing.T) {
	id := uuid.NewString()
	row := models.WorkerResultOwnership{WorkerJobID: id, SubmitPath: "/internal/ocr/v2/jobs", SourceKeys: `["jobs/ocr_v2/results/` + id + `.json"]`}
	for _, key := range []string{"owned/live-source.pdf", "jobs/ocr_v2/results/" + uuid.NewString() + ".json", "jobs/ocr_v2/results/" + id + ".json", "jobs/ocr_v2/results/../source.json"} {
		if validResultKey(&row, key) {
			t.Fatalf("unsafe key accepted: %q", key)
		}
	}
	row.SourceKeys = "[]"
	if !validResultKey(&row, "jobs/ocr_v2/results/"+id+".json") {
		t.Fatal("canonical result rejected")
	}
}
func TestDurabilityCleanupInterruptedTransactionRetriesExactKey(t *testing.T) {
	db := durabilityDB(t)
	now := time.Now()
	row := seedTerminal(t, db, "", now.Add(-time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	s := &DurabilityStore{DB: db, DeleteObject: func(context.Context, string) error { calls.Add(1); cancel(); return nil }}
	if err := s.cleanup(ctx, row.ID, now); err == nil {
		t.Fatal("cancelled transaction unexpectedly committed")
	}
	_ = db.First(&row, "id = ?", row.ID).Error
	if row.State != "deleting" {
		t.Fatalf("rollback lost committed delete intent: %s", row.State)
	}
	s.DeleteObject = func(context.Context, string) error { calls.Add(1); return nil }
	if err := s.cleanup(context.Background(), row.ID, now); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("interrupted delete not retried")
	}
}
func TestDurabilityConcurrentAdmissionPinsBeforeCleanup(t *testing.T) {
	db := durabilityDB(t)
	now := time.Now()
	row := seedTerminal(t, db, "", now.Add(-time.Hour))
	entered := make(chan struct{})
	release := make(chan struct{})
	dispatchDone := make(chan error, 1)
	var deletes atomic.Int32
	s := &DurabilityStore{DB: db, DeleteObject: func(context.Context, string) error { deletes.Add(1); return nil }}
	c := testDurableClient(t, s, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(202)
		_, _ = io.WriteString(w, `{"job_id":"`+uuid.NewString()+`","status":"queued"}`)
	})
	go func() {
		r, err := postDurable(t, c, s.BaseURL, "overlap", row.ResultKey)
		if err == nil {
			_ = r.Body.Close()
		}
		dispatchDone <- err
	}()
	<-entered
	if err := s.cleanup(context.Background(), row.ID, now); err != nil {
		t.Fatal(err)
	}
	if deletes.Load() != 0 {
		t.Fatal("admitted input removed while external dispatch blocked")
	}
	close(release)
	if err := <-dispatchDone; err != nil {
		t.Fatal(err)
	}
}
func TestManagedDispatchFailsClosedWithoutLedger(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	SetDurabilityStore(nil)
	t.Cleanup(func() { SetDurabilityStore(nil) })
	calls := atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	c := &http.Client{Transport: &signingTransport{transport: server.Client().Transport}}
	if _, err := postDurable(t, c, server.URL, "request", "owned/source.pdf"); err == nil {
		t.Fatal("managed dispatch without ledger succeeded")
	}
	if calls.Load() != 0 {
		t.Fatal("unowned managed dispatch reached Worker")
	}
}

func TestDurabilityConcurrentCleanupDeletesOnlyOnce(t *testing.T) {
	db := durabilityDB(t)
	now := time.Now()
	row := seedTerminal(t, db, "", now.Add(-time.Hour))
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	var calls atomic.Int32
	s := &DurabilityStore{DB: db, DeleteObject: func(context.Context, string) error { calls.Add(1); close(entered); <-release; return nil }}
	go func() { done <- s.cleanup(context.Background(), row.ID, now) }()
	<-entered
	if err := s.cleanup(context.Background(), row.ID, now); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("concurrent cleanup duplicated physical delete")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestDurabilityRetentionLockDefersWithoutErasureClaim(t *testing.T) {
	db := durabilityDB(t)
	now := time.Now()
	row := seedTerminal(t, db, "", now.Add(-time.Hour))
	s := &DurabilityStore{DB: db, DeleteObject: func(context.Context, string) error {
		return &storage.RetentionLockedError{ProviderCode: "ObjectLockedByBucketPolicy"}
	}}
	if err := s.cleanup(context.Background(), row.ID, now); err != nil {
		t.Fatal(err)
	}
	_ = db.First(&row, "id = ?", row.ID).Error
	if row.State != "deleting" || row.LastError != "physical storage deletion deferred by provider retention" || row.NextAttemptAt.Before(now.Add(23*time.Hour)) {
		t.Fatalf("retained object marked erased: %+v", row)
	}
}
func TestDurabilityExpiredResultAccessIsNotRestored(t *testing.T) {
	db := durabilityDB(t)
	now := time.Now()
	row := seedTerminal(t, db, "", now.Add(-time.Hour))
	s := &DurabilityStore{DB: db}
	c := testDurableClient(t, s, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	resp, err := c.Get(s.BaseURL + "/internal/ocr/v2/jobs/" + row.WorkerJobID)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatal("retention expired access restored")
	}
}
func TestDurabilityCleanupWinningRacePreventsSubsequentDispatch(t *testing.T) {
	db := durabilityDB(t)
	now := time.Now()
	row := seedTerminal(t, db, "", now.Add(-time.Hour))
	entered := make(chan struct{})
	release := make(chan struct{})
	deleted := make(chan error, 1)
	var network atomic.Int32
	s := &DurabilityStore{DB: db, DeleteObject: func(context.Context, string) error { close(entered); <-release; return nil }}
	c := testDurableClient(t, s, func(w http.ResponseWriter, r *http.Request) { network.Add(1); w.WriteHeader(202) })
	go func() { deleted <- s.cleanup(context.Background(), row.ID, now) }()
	<-entered
	admitted := make(chan error, 1)
	go func() {
		r, err := postDurable(t, c, s.BaseURL, "late-source-reuse", row.ResultKey)
		if r != nil {
			_ = r.Body.Close()
		}
		admitted <- err
	}()
	close(release)
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	if err := <-admitted; err == nil {
		t.Fatal("expired source reuse admitted")
	}
	if network.Load() != 0 {
		t.Fatal("deleted source dispatched")
	}
}

func TestDurabilityFailedSnapshotCannotAuthorizeDeletionDuringActorRetry(t *testing.T) {
	db := durabilityDB(t)
	now := time.Now()
	row := seedTerminal(t, db, "", now.Add(-time.Hour))
	if err := db.Model(&row).Updates(map[string]any{"state": "active", "expires_at": nil}).Error; err != nil {
		t.Fatal(err)
	}
	s := &DurabilityStore{DB: db, DeleteObject: func(context.Context, string) error { t.Error("failed actor may retry; must not delete"); return nil }}
	failed := []byte(`{"job_id":"` + row.WorkerJobID + `","status":"failed","owner_identity":"user:owned","result_key":"` + row.ResultKey + `"}`)
	if err := s.recordStatus(context.Background(), row.WorkerJobID, failed, now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.cleanup(context.Background(), row.ID, now); err != nil {
		t.Fatal(err)
	}
	_ = db.First(&row, "id = ?", row.ID).Error
	if row.State != "uncertain_result" {
		t.Fatalf("failed actor unpinned: %+v", row)
	}
	resumed := []byte(`{"job_id":"` + row.WorkerJobID + `","status":"running","owner_identity":"user:owned"}`)
	if err := s.recordStatus(context.Background(), row.WorkerJobID, resumed, now); err != nil {
		t.Fatal(err)
	}
	rowID := row.ID
	row = models.WorkerResultOwnership{}
	_ = db.First(&row, "id = ?", rowID).Error
	if row.State != "active" || row.ExpiresAt != nil {
		t.Fatalf("retry failed to restore active pin: %+v", row)
	}
}

func TestDurabilityStudioPublishedAssetPinsWorkerOutput(t *testing.T) {
	db := durabilityDB(t)
	now := time.Now()
	row := seedTerminal(t, db, "", now.Add(-time.Hour))
	var deletes atomic.Int32
	if err := db.Exec("INSERT INTO studio_assets(id,r2_key) VALUES (?,?)", "published", row.ResultKey).Error; err != nil {
		t.Fatal(err)
	}
	s := &DurabilityStore{DB: db, DeleteObject: func(context.Context, string) error { deletes.Add(1); return nil }}
	if err := s.cleanup(context.Background(), row.ID, now); err != nil {
		t.Fatal(err)
	}
	if deletes.Load() != 0 {
		t.Fatal("published Studio asset erased by Worker retention")
	}
}
func TestDurabilityRetirementSurvivesInterruptedDeleteAndBlocksSourceReuse(t *testing.T) {
	db := durabilityDB(t)
	now := time.Now()
	row := seedTerminal(t, db, "", now.Add(-time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	s := &DurabilityStore{DB: db, DeleteObject: func(context.Context, string) error { cancel(); return nil }}
	if err := s.cleanup(ctx, row.ID, now); err == nil {
		t.Fatal("interrupted physical delete acknowledged")
	}
	var retired int64
	if err := db.Table("studio_storage_object_retirements").Where("object_key = ?", row.ResultKey).Count(&retired).Error; err != nil || retired != 1 {
		t.Fatalf("retirement lost after crash: count=%d err=%v", retired, err)
	}
	calls := atomic.Int32{}
	c := testDurableClient(t, s, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(202) })
	if _, err := postDurable(t, c, s.BaseURL, "reuse-retired", row.ResultKey); err == nil {
		t.Fatal("retired key reused")
	}
	if calls.Load() != 0 {
		t.Fatal("retired key reached Worker")
	}
}

func TestDurabilityInputRollbackRetainsAmbiguousDispatchAcrossRestart(t *testing.T) {
	db := durabilityDB(t)
	key := "jobs/ocr_v2/input/owned.pdf"
	row := models.WorkerResultOwnership{ID: uuid.NewString(), RequestID: "ambiguous-input", RequestHash: "hash", SubmitPath: "/internal/ocr/v2/jobs", SourceKeys: mustJSONStringArray(key), State: "submitting"}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	SetDurabilityStore(&DurabilityStore{DB: db})
	pinned, err := InputMayBeReferenced(context.Background(), key)
	if err != nil || !pinned {
		t.Fatalf("ambiguous input rollback allowed: pinned=%v err=%v", pinned, err)
	}
	pinned, err = InputMayBeReferenced(context.Background(), "jobs/ocr_v2/input/unsubmitted.pdf")
	if err != nil || pinned {
		t.Fatalf("unsubmitted isolated input cannot roll back: pinned=%v err=%v", pinned, err)
	}
	if err := db.Model(&row).Update("state", "deleted").Error; err != nil {
		t.Fatal(err)
	}
	pinned, err = InputMayBeReferenced(context.Background(), key)
	if err != nil || pinned {
		t.Fatalf("released input remains pinned: pinned=%v err=%v", pinned, err)
	}
}

func TestDurabilityLegacyActorKnownIdentityRecoversLostACK(t *testing.T) {
	db := durabilityDB(t)
	jobID := uuid.NewString()
	key := "jobs/markdown/output/generated.md"
	var submitted atomic.Int32
	var lost atomic.Bool
	lost.Store(true)
	s := &DurabilityStore{DB: db}
	c := testDurableClient(t, s, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			submitted.Add(1)
			var count int64
			_ = db.Model(&models.WorkerResultOwnership{}).Where("worker_job_id = ? AND state = ?", jobID, "submitting").Count(&count).Error
			if count != 1 {
				t.Error("known actor identity absent before network")
			}
			if lost.Load() {
				conn, _, _ := w.(http.Hijacker).Hijack()
				_ = conn.Close()
				return
			}
			_, _ = io.WriteString(w, `{"success":true}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"`+jobID+`","status":"succeeded","result":{"artifact_key":"`+key+`"}}`)
	})
	if err := db.Create(&models.TaskCompletion{ID: jobID, OwnerIdentity: "user:owned", DownloadToken: "token", State: "active"}).Error; err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"actor_name": "pdf_to_markdown_job", "owner_identity": "user:owned", "args": []any{jobID, "jobs/markdown/source/" + jobID + ".pdf", "password", "eng", false, false, "document.pdf"}, "kwargs": map[string]any{}})
	submit := func() error {
		req, _ := http.NewRequest(http.MethodPost, s.BaseURL+"/api/v1/jobs/submit", bytes.NewReader(payload))
		resp, err := c.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		return err
	}
	if err := submit(); err == nil {
		t.Fatal("lost actor ACK accepted")
	}
	if err := submit(); err == nil {
		t.Fatal("uncertain actor resubmitted")
	}
	if submitted.Load() != 1 {
		t.Fatal("actor dispatched twice")
	}
	// A new process can poll the durable known identity without resubmitting.
	restarted := &DurabilityStore{DB: db, HTTP: c, BaseURL: s.BaseURL}
	SetDurabilityStore(restarted)
	if err := restarted.runOnce(context.Background(), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var row models.WorkerResultOwnership
	if err := db.First(&row, "worker_job_id = ?", jobID).Error; err != nil {
		t.Fatal(err)
	}
	if row.State != "terminal" || row.ResultKey != key {
		t.Fatalf("lost ACK not reconciled: %+v", row)
	}
	if err := submit(); err != nil {
		t.Fatal(err)
	}
	if submitted.Load() != 1 {
		t.Fatal("recovered actor dispatched twice")
	}
}

func TestDurabilityPinnedBatchCannotStarveLaterCleanup(t *testing.T) {
	db := durabilityDB(t)
	now := time.Now()
	for i := 0; i < 51; i++ {
		row := seedTerminal(t, db, "", now.Add(-time.Hour))
		pin := models.WorkerResultOwnership{ID: uuid.NewString(), RequestID: uuid.NewString(), RequestHash: "h", SourceKeys: mustJSONStringArray(row.ResultKey), SubmitPath: "/api/v1/editor/extract", State: "uncertain"}
		if err := db.Create(&pin).Error; err != nil {
			t.Fatal(err)
		}
	}
	eligible := seedTerminal(t, db, "", now.Add(-time.Minute))
	var deletes atomic.Int32
	s := &DurabilityStore{DB: db, DeleteObject: func(context.Context, string) error { deletes.Add(1); return nil }}
	// Two finite batches must reach the later unpinned result without dropping
	// any ownership hold in the older batch.
	for i := 0; i < 2; i++ {
		if err := s.runOnce(context.Background(), now); err != nil {
			t.Fatal(err)
		}
	}
	var row models.WorkerResultOwnership
	if err := db.First(&row, "id = ?", eligible.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.State != "deleted" || deletes.Load() != 1 {
		t.Fatalf("pinned batch starved later result: %s deletes=%d", row.State, deletes.Load())
	}
}
