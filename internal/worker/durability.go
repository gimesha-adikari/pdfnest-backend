package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"pdfnest-backend/internal/models"
	"pdfnest-backend/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const workerJSONLimit int64 = 8 << 20
const WorkerResultRetention = 24 * time.Hour

var ErrDurabilityUnavailable = errors.New("worker ownership ledger unavailable")
var ErrDispatchUncertain = errors.New("worker submission outcome requires reconciliation")

// DurabilityStore has no dependency on Redis. Its DB must be migrated before
// installation. The optional HTTP client is for bounded reconciliation requests.
type DurabilityStore struct {
	DB           *gorm.DB
	HTTP         *http.Client
	BaseURL      string
	DeleteObject func(context.Context, string) error
}

var durabilityMu sync.RWMutex
var durableStore *DurabilityStore

func ConfigureDurability(db *gorm.DB) error {
	if db == nil || !db.Migrator().HasTable(&models.WorkerResultOwnership{}) {
		return ErrDurabilityUnavailable
	}
	SetDurabilityStore(&DurabilityStore{DB: db})
	return nil
}
func SetDurabilityStore(store *DurabilityStore) {
	durabilityMu.Lock()
	durableStore = store
	durabilityMu.Unlock()
}
func currentDurabilityStore() *DurabilityStore {
	durabilityMu.RLock()
	defer durabilityMu.RUnlock()
	return durableStore
}
func durabilityRequired() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV"))) {
	case "canary", "staging", "production":
		return true
	}
	return false
}
func durableContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
}

func queuedSubmission(p string) bool {
	switch p {
	case "/internal/ocr/v2/jobs", "/api/v1/editor/extract", "/api/v1/editor/compile", "/api/v1/markup/highlight", "/api/v1/markup/underline", "/api/v1/markup/strikeout", "/api/v1/markdown/convert", "/api/v1/jobs/submit":
		return true
	}
	return false
}
func readBounded(r io.Reader, limit int64) ([]byte, error) {
	b, e := io.ReadAll(io.LimitReader(r, limit+1))
	if e != nil {
		return nil, e
	}
	if int64(len(b)) > limit {
		return nil, errors.New("worker response exceeds bounded envelope")
	}
	return b, nil
}
func responseJSON(req *http.Request, b []byte) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(b)), ContentLength: int64(len(b)), Request: req}
}

// durableRoundTrip surrounds the signed request, preserving its exact bytes.
// A failed ownership write prevents dispatch. A lost ACK never triggers resend.
func (s *DurabilityStore) durableRoundTrip(req *http.Request, base http.RoundTripper) (*http.Response, error) {
	if req.Method == http.MethodPost && queuedSubmission(req.URL.Path) {
		body, err := readBounded(req.Body, workerJSONLimit)
		if err != nil {
			return nil, err
		}
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
		var payload map[string]any
		if err = json.Unmarshal(body, &payload); err != nil {
			return nil, errors.New("invalid worker dispatch JSON")
		}
		digest := sha256.Sum256(body)
		hash := hex.EncodeToString(digest[:])
		knownJobID := ""
		if req.URL.Path == "/api/v1/jobs/submit" {
			actor, _ := payload["actor_name"].(string)
			args, ok := payload["args"].([]any)
			if actor != "pdf_to_markdown_job" || !ok || len(args) < 2 {
				return nil, errors.New("unsupported durable actor dispatch")
			}
			knownJobID, _ = args[0].(string)
			source, _ := args[1].(string)
			if !validJobID(knownJobID) || source != "jobs/markdown/source/"+knownJobID+".pdf" {
				return nil, errors.New("invalid durable actor dispatch")
			}
			payload["source_key"] = source
			payload["request_id"] = knownJobID
		}
		requestID, _ := payload["request_id"].(string)
		if requestID == "" {
			requestID = req.Header.Get("X-Backend-Dispatch-ID")
		}
		if requestID == "" {
			requestID = uuid.NewString()
		}
		owner, _ := payload["owner_identity"].(string)
		profile, _ := payload["profile"].(string)
		sources := collectSourceKeys(payload)
		encoded, _ := json.Marshal(sources)
		row := models.WorkerResultOwnership{ID: uuid.NewString(), RequestID: requestID, RequestHash: hash, OwnerIdentity: owner, SubmitPath: req.URL.Path, Profile: profile, SourceKeys: string(encoded), State: "submitting", WorkerJobID: knownJobID, NextAttemptAt: time.Now()}
		dbctx, cancel := durableContext(req.Context())
		defer cancel()
		var created *gorm.DB
		admissionErr := s.DB.WithContext(dbctx).Transaction(func(tx *gorm.DB) error {
			if knownJobID != "" {
				var task models.TaskCompletion
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&task, "id = ?", knownJobID).Error; err != nil {
					return err
				}
				if task.State != "active" {
					return errors.New("task no longer admits external dispatch")
				}
			}

			if err := lockStorageKeys(tx, sources); err != nil {
				return err
			}
			if len(sources) > 0 {
				if tx.Migrator().HasTable("studio_storage_object_retirements") {
					var retired int64
					if err := tx.Table("studio_storage_object_retirements").Where("object_key IN ?", sources).Count(&retired).Error; err != nil {
						return err
					}
					if retired > 0 {
						return errors.New("worker source key is retired")
					}
				}
				var deleted int64
				if err := tx.Model(&models.WorkerResultOwnership{}).Where("state IN ? AND (result_key IN ? OR metadata_key IN ?)", []string{"deleting", "deleted"}, sources, sources).Count(&deleted).Error; err != nil {
					return err
				}
				if deleted > 0 {
					return errors.New("worker input retention expired")
				}
			}
			created = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "request_id"}}, DoNothing: true}).Create(&row)
			return created.Error
		})
		if admissionErr != nil {
			return nil, ErrDurabilityUnavailable
		}
		if created.RowsAffected == 0 {
			row = models.WorkerResultOwnership{}
			if err = s.DB.WithContext(dbctx).First(&row, "request_id = ?", requestID).Error; err != nil {
				return nil, ErrDurabilityUnavailable
			}
			if row.RequestHash != hash || row.SubmitPath != req.URL.Path || row.OwnerIdentity != owner {
				return nil, errors.New("worker dispatch idempotency conflict")
			}
			if row.WorkerJobID == "" || row.State == "submitting" || row.State == "uncertain" {
				return nil, ErrDispatchUncertain
			}
			ack, _ := json.Marshal(map[string]any{"success": true, "job_id": row.WorkerJobID, "status": "queued", "owner_identity": row.OwnerIdentity, "profile": row.Profile})
			return responseJSON(req, ack), nil
		}
		resp, err := base.RoundTrip(req)
		if err != nil {
			return nil, ErrDispatchUncertain
		}
		data, readErr := readBounded(resp.Body, workerJSONLimit)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, ErrDispatchUncertain
		}
		resp.Body = io.NopCloser(bytes.NewReader(data))
		dbctx, ackCancel := durableContext(req.Context())
		defer ackCancel()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			// Even a server error can follow admission. Preserve the dispatch/input pin.
			_ = s.DB.WithContext(dbctx).Model(&row).Updates(map[string]any{"state": "uncertain", "last_error": "worker admission response requires reconciliation"}).Error
			return resp, nil
		}
		var ack struct {
			JobID string `json:"job_id"`
		}
		decodeErr := json.Unmarshal(data, &ack)
		if ack.JobID == "" && knownJobID != "" {
			var success struct {
				Success bool `json:"success"`
			}
			if json.Unmarshal(data, &success) == nil && success.Success {
				ack.JobID = knownJobID
			}
		}
		if decodeErr != nil || !validJobID(ack.JobID) || (knownJobID != "" && ack.JobID != knownJobID) {
			return nil, ErrDispatchUncertain
		}
		if err = s.DB.WithContext(dbctx).Model(&row).Updates(map[string]any{"worker_job_id": ack.JobID, "state": "active", "status_json": string(data)}).Error; err != nil {
			return nil, ErrDispatchUncertain
		}
		return resp, nil
	}
	jobID, kind := jobReadPath(req.URL.Path)
	if req.Method == http.MethodGet && jobID != "" && req.Context().Value(reconciliationContextKey{}) != true {
		var ownership models.WorkerResultOwnership
		err := s.DB.WithContext(req.Context()).First(&ownership, "worker_job_id = ?", jobID).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrDurabilityUnavailable
		}
		if err == nil && ownership.ExpiresAt != nil && !ownership.ExpiresAt.After(time.Now()) {
			result := responseJSON(req, []byte(`{"detail":{"code":"RESULT_EXPIRED","message":"Worker result retention expired"}}`))
			result.StatusCode = http.StatusGone
			return result, nil
		}
	}
	resp, err := base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if jobID == "" || req.Method != http.MethodGet {
		return resp, nil
	}
	if resp.StatusCode == http.StatusNotFound {
		if fallback, e := s.fallback(req, jobID, kind); e != nil {
			_ = resp.Body.Close()
			return nil, e
		} else if fallback != nil {
			_ = resp.Body.Close()
			return fallback, nil
		}
	}
	if kind == "status" && resp.StatusCode == http.StatusOK {
		data, e := readBounded(resp.Body, workerJSONLimit)
		_ = resp.Body.Close()
		if e != nil {
			return nil, e
		}
		resp.Body = io.NopCloser(bytes.NewReader(data))
		if e = s.recordStatus(req.Context(), jobID, data, time.Now()); e != nil {
			return nil, e
		}
	}
	return resp, nil
}
func validJobID(id string) bool { _, err := uuid.Parse(id); return err == nil && len(id) == 36 }
func collectSourceKeys(payload map[string]any) []string {
	var keys []string
	for _, name := range []string{"source_key", "payload_key", "pages_json_key"} {
		if key, ok := payload[name].(string); ok && key != "" {
			keys = append(keys, key)
		}
	}
	if files, ok := payload["source_files"].([]any); ok {
		for _, v := range files {
			if f, ok := v.(map[string]any); ok {
				if key, ok := f["source_key"].(string); ok {
					keys = append(keys, key)
				}
			}
		}
	}
	return keys
}
func jobReadPath(p string) (string, string) {
	for _, prefix := range []string{"/internal/ocr/v2/jobs/", "/api/v1/jobs/", "/api/v1/editor/jobs/", "/api/v1/markup/jobs/"} {
		if strings.HasPrefix(p, prefix) {
			parts := strings.Split(strings.TrimPrefix(p, prefix), "/")
			if len(parts) > 0 && validJobID(parts[0]) {
				if len(parts) == 1 {
					return parts[0], "status"
				}
				if len(parts) == 2 && (parts[1] == "result" || parts[1] == "download") {
					return parts[0], "result"
				}
			}
		}
	}
	return "", ""
}

// Only Worker actor output namespaces are accepted. Source/payload snapshots,
// arbitrary object keys and traversal can never become deletion candidates.
func validResultKey(row *models.WorkerResultOwnership, key string) bool {
	if key == "" {
		return true
	}
	if path.Clean(key) != key || strings.Contains(key, "\\") || strings.HasPrefix(key, "/") {
		return false
	}
	var sources []string
	if json.Unmarshal([]byte(row.SourceKeys), &sources) != nil {
		return false
	}
	for _, source := range sources {
		if key == source {
			return false
		}
	}
	id := row.WorkerJobID
	switch row.SubmitPath {
	case "/internal/ocr/v2/jobs":
		allowed := []string{"jobs/ocr_v2/results/" + id + ".json", "jobs/ocr_v2/searchable_pdf/" + id + ".pdf"}
		for _, profile := range []string{"document_extraction_v2", "pdf_markdown_v2"} {
			allowed = append(allowed, "jobs/ocr_v2/structured/"+profile+"/"+id+".json")
		}
		for _, action := range []string{"highlight", "underline", "strikeout"} {
			for _, ext := range []string{".pdf", ".json"} {
				allowed = append(allowed, "jobs/ocr_v2/markup/"+action+"/"+id+ext)
			}
		}
		for _, candidate := range allowed {
			if candidate == key {
				return true
			}
		}
	case "/api/v1/editor/compile":
		return strings.HasPrefix(key, "jobs/editor/output/") && path.Ext(key) == ".pdf" && !strings.Contains(strings.TrimPrefix(key, "jobs/editor/output/"), "/")
	case "/api/v1/markup/highlight", "/api/v1/markup/underline", "/api/v1/markup/strikeout":
		prefix := "jobs/markup/" + path.Base(row.SubmitPath) + "/output/"
		return strings.HasPrefix(key, prefix) && path.Ext(key) == ".pdf" && !strings.Contains(strings.TrimPrefix(key, prefix), "/")
	case "/api/v1/markdown/convert", "/api/v1/jobs/submit":
		return strings.HasPrefix(key, "jobs/markdown/output/") && path.Ext(key) == ".md" && !strings.Contains(strings.TrimPrefix(key, "jobs/markdown/output/"), "/")
	}
	return false
}
func terminalStatus(state string) bool {
	return state == "succeeded" || state == "failed" || state == "cancelled"
}
func (s *DurabilityStore) recordStatus(ctx context.Context, id string, data []byte, now time.Time) error {
	var status struct {
		ID        string         `json:"id"`
		JobID     string         `json:"job_id"`
		Status    string         `json:"status"`
		Owner     string         `json:"owner_identity"`
		ResultKey string         `json:"result_key"`
		Result    map[string]any `json:"result"`
	}
	if json.Unmarshal(data, &status) != nil {
		return errors.New("invalid worker status")
	}
	if status.ID != "" && status.ID != id || status.JobID != "" && status.JobID != id {
		return errors.New("worker status identity mismatch")
	}
	dbctx, cancel := durableContext(ctx)
	defer cancel()
	return s.DB.WithContext(dbctx).Transaction(func(tx *gorm.DB) error {
		var row models.WorkerResultOwnership
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "worker_job_id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if row.State == "deleted" || row.State == "deleting" {
			return nil
		}
		if row.SubmitPath == "/internal/ocr/v2/jobs" && row.OwnerIdentity != "" && status.Owner != row.OwnerIdentity {
			return errors.New("worker status owner mismatch")
		}
		key := status.ResultKey
		metadata := ""
		if status.Result != nil {
			if key == "" {
				key, _ = status.Result["artifact_key"].(string)
			}
			metadata, _ = status.Result["metadata_key"].(string)
		}
		if !validResultKey(&row, key) || !validResultKey(&row, metadata) {
			return errors.New("worker result is outside dispatch ownership")
		}
		// A previously terminal snapshot cannot be resurrected by stale responses.
		if row.State == "terminal" {
			return nil
		}
		updates := map[string]any{"state": "active", "status_json": string(data), "next_attempt_at": now.Add(time.Minute), "last_error": ""}
		if terminalStatus(status.Status) {
			if row.ResultKey != "" && key != "" && row.ResultKey != key {
				return errors.New("worker retry changed owned artifact; reconciliation required")
			}
			updates["state"] = "terminal"
			// A failed/cancelled Redis status can precede a Dramatiq retry or
			// late actor. Keep its cleanup pinned until positive success.
			if status.Status != "succeeded" {
				updates["state"] = "uncertain_result"
			}
			updates["result_key"] = key
			updates["metadata_key"] = metadata
			updates["expires_at"] = now.Add(WorkerResultRetention)
		} else if row.State == "uncertain_result" {
			updates["state"] = "active"
			updates["expires_at"] = nil
		}
		return tx.Model(&row).Updates(updates).Error
	})
}

func (s *DurabilityStore) fallback(req *http.Request, id, kind string) (*http.Response, error) {
	var row models.WorkerResultOwnership
	err := s.DB.WithContext(req.Context()).First(&row, "worker_job_id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrDurabilityUnavailable
	}
	if row.ExpiresAt == nil || !row.ExpiresAt.After(time.Now()) || (row.State != "terminal" && row.State != "uncertain_result") {
		return nil, nil
	}
	if kind == "status" {
		if row.StatusJSON != "" {
			return responseJSON(req, []byte(row.StatusJSON)), nil
		}
		return nil, nil
	}
	if row.ResultKey == "" || !validResultKey(&row, row.ResultKey) {
		return nil, nil
	}
	// Public services still perform their existing owner/session/token checks.
	// This is the authenticated backend-to-Worker adapter, never a public route.
	local, cleanup, err := storage.ResolveObject(req.Context(), row.ResultKey, "worker-durable-result", path.Ext(row.ResultKey))
	if err != nil {
		return nil, errors.New("worker result storage temporarily unavailable")
	}
	file, err := os.Open(local)
	if err != nil {
		cleanup()
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		cleanup()
		return nil, err
	}
	if info.Size() > MaxRequestBodyBytes {
		_ = file.Close()
		cleanup()
		return nil, errors.New("worker artifact exceeds bounded envelope")
	}
	contentType := "application/json"
	if path.Ext(row.ResultKey) == ".pdf" {
		contentType = "application/pdf"
	}
	if path.Ext(row.ResultKey) == ".md" {
		contentType = "text/markdown"
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: &ownedResultBody{ReadCloser: file, cleanup: cleanup}, ContentLength: info.Size(), Request: req}, nil
}

type ownedResultBody struct {
	io.ReadCloser
	cleanup func()
	once    sync.Once
}

func (b *ownedResultBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.cleanup)
	return err
}

// workerPins protects unsettled tasks and unreconciled Studio jobs. Missing
// optional tables are allowed only in isolated tests/local setup; managed boot
// validates the complete migration separately. Query errors always pin.
func workerPins(tx *gorm.DB, row *models.WorkerResultOwnership, now time.Time) (bool, error) {
	var count int64
	for _, key := range []string{row.ResultKey, row.MetadataKey} {
		if key == "" {
			continue
		}
		for _, table := range []string{"studio_assets", "studio_exports"} {
			if tx.Migrator().HasTable(table) {
				if err := tx.Table(table).Where("r2_key = ?", key).Count(&count).Error; err != nil {
					return true, err
				}
				if count > 0 {
					return true, nil
				}
			}
		}
		if tx.Migrator().HasTable("studio_jobs") {
			if err := tx.Table("studio_jobs").Where("source_key = ? OR payload_key = ? OR pending_object_keys @> ?::jsonb", key, key, mustJSONStringArray(key)).Count(&count).Error; err != nil {
				return true, err
			}
			if count > 0 {
				return true, nil
			}
		}
	}
	if tx.Migrator().HasTable("studio_jobs") {
		if err := tx.Table("studio_jobs").Where("worker_job_id = ? AND reconciled_at IS NULL", row.WorkerJobID).Count(&count).Error; err != nil {
			return true, err
		}
		if count > 0 {
			return true, nil
		}
	}
	if tx.Migrator().HasTable(&models.TaskCompletion{}) {
		if err := tx.Model(&models.TaskCompletion{}).Where("id = ? AND state IN ?", row.WorkerJobID, []string{"active", "publishing"}).Count(&count).Error; err != nil {
			return true, err
		}
		if count > 0 {
			return true, nil
		}
		// Pending publication may still be consuming an owned Worker artifact. Pin
		// by owner as well as exact key until its durable settlement resolves.
		condition := "(result_key = ? OR result_key = ?) AND (state IN ? OR expires_at > ?)"
		if err := tx.Model(&models.TaskCompletion{}).Where(condition, row.ResultKey, row.MetadataKey, []string{"active", "publishing"}, now).Count(&count).Error; err != nil {
			return true, err
		}
		if count > 0 {
			return true, nil
		}
		if row.OwnerIdentity != "" {
			if err := tx.Model(&models.TaskCompletion{}).Where("owner_identity = ? AND state IN ?", row.OwnerIdentity, []string{"active", "publishing"}).Count(&count).Error; err != nil {
				return true, err
			}
			if count > 0 {
				return true, nil
			}
		}
	}
	// Shared output aliases are quarantined instead of guessing who owns them.
	if row.ResultKey != "" {
		if err := tx.Model(&models.WorkerResultOwnership{}).Where("id <> ? AND state <> ? AND (result_key = ? OR metadata_key = ?)", row.ID, "deleted", row.ResultKey, row.ResultKey).Count(&count).Error; err != nil {
			return true, err
		}
		if count > 0 {
			return true, nil
		}
	}
	return false, nil
}

// RunDurabilityOnce bounds both polling and reclamation. Deletion executes
// under a row lock; crashes roll back the receipt and retry the same exact keys.
// Ambiguous submissions and metadata-expired nonterminal jobs remain pinned.
func RunDurabilityOnce(ctx context.Context, now time.Time) error {
	s := currentDurabilityStore()
	if s == nil || s.DB == nil {
		return ErrDurabilityUnavailable
	}
	return s.runOnce(ctx, now)
}
func (s *DurabilityStore) runOnce(ctx context.Context, now time.Time) error {
	var rows []models.WorkerResultOwnership
	if err := s.DB.WithContext(ctx).Where("state IN ? AND worker_job_id <> '' AND next_attempt_at <= ?", []string{"active", "uncertain_result", "submitting", "uncertain"}, now).Order("next_attempt_at, id").Limit(50).Find(&rows).Error; err != nil {
		return err
	}
	var firstErr error
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		endpoint := "/api/v1/jobs/" + row.WorkerJobID
		if row.SubmitPath == "/internal/ocr/v2/jobs" {
			endpoint = "/internal/ocr/v2/jobs/" + row.WorkerJobID
		}
		base := s.BaseURL
		if base == "" {
			base = GetWorkerURL()
		}
		pollctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		req, err := http.NewRequestWithContext(context.WithValue(pollctx, reconciliationContextKey{}, true), http.MethodGet, strings.TrimRight(base, "/")+endpoint, nil)
		if err == nil {
			client := s.HTTP
			if client == nil {
				client = Client
			}
			var resp *http.Response
			resp, err = client.Do(req)
			if err == nil {
				data, readErr := readBounded(resp.Body, workerJSONLimit)
				_ = resp.Body.Close()
				err = readErr
				if err == nil && resp.StatusCode == http.StatusOK {
					err = s.recordStatus(ctx, row.WorkerJobID, data, now)
				} else if err == nil {
					err = fmt.Errorf("worker status reconciliation unavailable (%d)", resp.StatusCode)
				}
			}
		}
		cancel()
		if err != nil {
			// Keep the dispatch alive; never infer actor termination from a Redis 404.
			updateErr := s.DB.WithContext(ctx).Model(&models.WorkerResultOwnership{}).Where("id = ? AND state IN ?", row.ID, []string{"active", "uncertain_result", "submitting", "uncertain"}).Updates(map[string]any{"next_attempt_at": now.Add(time.Minute), "last_error": "worker status requires reconciliation"}).Error
			if firstErr == nil {
				if updateErr != nil {
					firstErr = updateErr
				} else {
					firstErr = err
				}
			}
		}
	}
	rows = nil
	if err := s.DB.WithContext(ctx).Where("state IN ? AND expires_at <= ? AND next_attempt_at <= ?", []string{"terminal", "deleting"}, now, now).Order("next_attempt_at, id").Limit(50).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if err := s.cleanup(ctx, row.ID, now); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Retire ownership and commit immutable tombstones BEFORE external deletion.
// A crash after DELETE cannot reopen a catalog registration window.
func (s *DurabilityStore) cleanup(ctx context.Context, id string, now time.Time) error {
	claimed := false
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row models.WorkerResultOwnership
		err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).First(&row, "id = ?", id).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if (row.State != "terminal" && row.State != "deleting") || row.ExpiresAt == nil || row.ExpiresAt.After(now) || row.NextAttemptAt.After(now) {
			return nil
		}
		if row.State == "deleting" {
			claimed = true
			return nil
		}
		if err := lockStorageKeys(tx, []string{row.ResultKey, row.MetadataKey}); err != nil {
			return err
		}
		pinned, err := workerPins(tx, &row, now)
		if err != nil {
			return err
		}
		if pinned {
			return tx.Model(&row).Update("next_attempt_at", now.Add(15*time.Minute)).Error
		}
		if !validResultKey(&row, row.ResultKey) || !validResultKey(&row, row.MetadataKey) {
			return errors.New("unsafe result cleanup reference")
		}
		for _, key := range []string{row.ResultKey, row.MetadataKey} {
			if key == "" {
				continue
			}
			var count int64
			if err := tx.Model(&models.WorkerResultOwnership{}).Where("id <> ? AND state <> ? AND source_keys::jsonb @> ?::jsonb", row.ID, "deleted", mustJSONStringArray(key)).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return tx.Model(&row).Update("next_attempt_at", now.Add(15*time.Minute)).Error
			}
			if tx.Migrator().HasTable("studio_storage_object_retirements") {
				if err := tx.Table("studio_storage_object_retirements").Clauses(clause.OnConflict{DoNothing: true}).Create(map[string]any{"object_key": key, "created_at": now}).Error; err != nil {
					return err
				}
			}
		}
		if err := tx.Model(&row).Update("state", "deleting").Error; err != nil {
			return err
		}
		claimed = true
		return nil
	})
	if err != nil || !claimed {
		return err
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row models.WorkerResultOwnership
		err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).First(&row, "id = ?", id).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if row.State != "deleting" || row.NextAttemptAt.After(now) {
			return nil
		}
		if !validResultKey(&row, row.ResultKey) || !validResultKey(&row, row.MetadataKey) {
			return errors.New("unsafe retired result reference")
		}
		row.Attempts++
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		del := s.DeleteObject
		if del == nil {
			del = storage.DeleteObject
		}
		for _, key := range []string{row.ResultKey, row.MetadataKey} {
			if key == "" {
				continue
			}
			if err := del(ctx, key); err != nil {
				delay := time.Minute
				message := "physical storage deletion requires retry"
				if storage.IsRetentionLockedError(err) {
					delay = 24 * time.Hour
					message = "physical storage deletion deferred by provider retention"
				}
				return tx.Model(&row).Updates(map[string]any{"next_attempt_at": now.Add(delay), "last_error": message}).Error
			}
		}
		return tx.Model(&row).Updates(map[string]any{"state": "deleted", "last_error": "", "status_json": ""}).Error
	})
}
func mustJSONStringArray(key string) string { b, _ := json.Marshal([]string{key}); return string(b) }
func StartDurability(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if err := RunDurabilityOnce(ctx, now); err != nil {
					log.Printf("Worker durable ownership reconciliation deferred: %v", err)
				}
			}
		}
	}()
}

// Every Worker dispatch and reclamation serializes overlapping storage keys.
// An admitted input pin commits before cleanup can inspect its references.
func lockStorageKeys(tx *gorm.DB, keys []string) error {
	ordered := append([]string(nil), keys...)
	sort.Strings(ordered)
	for i, key := range ordered {
		if key == "" || (i > 0 && key == ordered[i-1]) {
			continue
		}
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 81807))", key).Error; err != nil {
			return err
		}
	}
	return nil
}

type reconciliationContextKey struct{}

// InputMayBeReferenced is the rollback guard for callers that staged an input
// before dispatch. An HTTP error does not prove that a Worker failed admission.
// Missing managed authority and database errors conservatively retain inputs.
func InputMayBeReferenced(ctx context.Context, key string) (bool, error) {
	s := currentDurabilityStore()
	if s == nil || s.DB == nil {
		if durabilityRequired() {
			return true, ErrDurabilityUnavailable
		}
		return false, nil
	}
	dbctx, cancel := durableContext(ctx)
	defer cancel()
	var count int64
	err := s.DB.WithContext(dbctx).Model(&models.WorkerResultOwnership{}).Where("state <> ? AND source_keys::jsonb @> ?::jsonb", "deleted", mustJSONStringArray(key)).Count(&count).Error
	if err != nil {
		return true, ErrDurabilityUnavailable
	}
	return count > 0, nil
}
