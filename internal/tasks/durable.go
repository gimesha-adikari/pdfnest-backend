package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/models"
	"pdfnest-backend/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrSettlementPending = errors.New("artifact settlement requires reconciliation")
var ErrTaskTerminal = errors.New("task already terminal")

// SettleTaskBilling must atomically commit database reservations using tx.
// Guest finalization must return positive outcome evidence on every retry.
var SettleTaskBilling func(context.Context, *gorm.DB, *models.TaskCompletion) error

func (r *TaskRegistry) AdmitDurable(ctx context.Context, id, owner, reservationID, kind string) error {
	if config.DB == nil {
		return errors.New("durable task database unavailable")
	}
	task, err := r.Get(id)
	if err != nil {
		return err
	}
	if task == nil {
		return errors.New("task projection unavailable before admission")
	}
	record := models.TaskCompletion{ID: id, OwnerIdentity: owner, DownloadToken: task.DownloadToken, ReservationID: reservationID, ReservationKind: kind, State: "active", ExpiresAt: time.Now().Add(TaskTTL)}
	if record.DownloadToken == "" {
		record.DownloadToken = uuid.NewString()
	}
	return config.DB.WithContext(ctx).Create(&record).Error
}

func durableTask(ctx context.Context, id string) (*models.TaskCompletion, error) {
	if config.DB == nil {
		return nil, nil
	}
	var row models.TaskCompletion
	err := config.DB.WithContext(ctx).Where("id = ?", id).Limit(1).Find(&row).Error
	if err == nil && row.ID == "" {
		return nil, nil
	}
	return &row, err
}

func durableStatus(row *models.TaskCompletion) *TaskStatus {
	status := strings.ToUpper(row.State)
	progress := 0
	if row.State == "publishing" {
		status = "PROCESSING"
		progress = 95
	}
	if row.State == "active" {
		status = "PROCESSING"
	}
	if row.State == "completed" {
		progress = 100
	}
	if row.State == "deleting" || row.State == "deleted" {
		status = "EXPIRED"
	}
	return &TaskStatus{ID: row.ID, Status: status, Progress: progress, ResultKey: row.ResultKey, ResultURL: row.ResultURL, OwnerIdentity: row.OwnerIdentity, DownloadToken: row.DownloadToken, ReservationID: row.ReservationID, ReservationKind: row.ReservationKind, Error: row.Error, UpdatedAt: row.UpdatedAt.Unix()}
}

// PublishArtifact persists the ownership decision BEFORE its first storage
// write. Any failed/ambiguous write or settlement leaves the row pinned; retries
// reconcile the same key. Never delete an artifact after an uncertain ACK.
func (r *TaskRegistry) PublishArtifact(ctx context.Context, id, key, source, contentType string) error {
	if config.DB == nil {
		return errors.New("durable task database unavailable")
	}
	if !validTaskArtifactKey(id, key) {
		return errors.New("invalid task artifact key")
	}
	existing, lookupErr := durableTask(ctx, id)
	if lookupErr != nil {
		return lookupErr
	}
	if existing != nil && existing.State == "completed" {
		if existing.ResultKey != key {
			return ErrTaskTerminal
		}
		return r.ReconcileCompletion(ctx, id)
	}
	snapshot, fingerprint, size, cleanup, err := storage.StageArtifactSource(ctx, source)
	if err != nil {
		// A previous publisher may have stored the exact chosen bytes and lost
		// its acknowledgement or local source before settlement. Reconcile that
		// durable identity without requiring the vanished process-local file.
		// A different key must never finalize a previous publication by accident.
		if existing != nil && existing.State == "publishing" && existing.ResultKey == key {
			return r.ReconcileCompletion(ctx, id)
		}
		return fmt.Errorf("%w: artifact source: %v", ErrSettlementPending, err)
	}
	defer cleanup()
	err = config.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row models.TaskCompletion
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "id = ?", id).Error; err != nil {
			return err
		}
		if row.State == "completed" {
			// Terminal retries return the immutable existing outcome. They never
			// replace a charged artifact, irrespective of the caller's new source.
			if row.ResultKey == key {
				return nil
			}
			return ErrTaskTerminal
		}
		if row.State != "active" && row.State != "publishing" {
			return ErrTaskTerminal
		}
		if row.ResultKey != "" && row.ResultKey != key {
			return errors.New("artifact key already chosen")
		}
		if row.State == "publishing" && row.ResultKey != "" && (row.ArtifactSHA256 != fingerprint || row.ArtifactBytes != size) {
			return errors.New("artifact identity already chosen")
		}

		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?,81807))", key).Error; err != nil {
			return err
		}
		var retired int64
		if tx.Migrator().HasTable("studio_storage_object_retirements") {
			if err := tx.Table("studio_storage_object_retirements").Where("object_key = ?", key).Count(&retired).Error; err != nil {
				return err
			}
			if retired != 0 {
				return errors.New("artifact key is permanently retired")
			}
		}
		row.ArtifactSHA256 = fingerprint
		row.ArtifactBytes = size
		row.State = "publishing"
		row.ResultKey = key
		row.ResultURL = "r2://" + key
		if !storage.RemoteStorageEnabled() {
			row.ResultURL = filepath.Join(storage.GetLocalStorageDir(), filepath.FromSlash(key))
		}
		return tx.Save(&row).Error
	})
	if err != nil {
		return err
	}
	// Reacquire the durable row for the write so duplicate publishers cannot
	// overwrite an artifact after another replica settles it. Local writes use
	// atomic rename; object-store uploads publish only their completed object.
	err = config.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row models.TaskCompletion
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "id = ?", id).Error; err != nil {
			return err
		}
		if row.State == "completed" {
			return nil
		}
		if row.State != "publishing" || row.ResultKey != key {
			return ErrTaskTerminal
		}
		proofErr := storage.VerifyArtifact(ctx, key, row.ArtifactSHA256, row.ArtifactBytes)
		if proofErr == nil {
			return nil
		}
		if !errors.Is(proofErr, storage.ErrObjectNotFound) {
			return proofErr
		}
		return storage.SaveVerifiedArtifact(ctx, key, snapshot, row.ArtifactSHA256, row.ArtifactBytes, contentType)
	})
	if err != nil {
		return fmt.Errorf("%w: artifact write: %v", ErrSettlementPending, err)
	}
	return r.ReconcileCompletion(ctx, id)
}

// ReconcileCompletion keeps the ownership row locked across exact content proof
// and settlement. All application reclamation and reservation release paths
// coordinate through the same row. A process crash rolls back DB settlement;
// guest Redis receipts preserve the outcome of an ambiguous remote ACK.
func (r *TaskRegistry) ReconcileCompletion(ctx context.Context, id string) error {
	if config.DB == nil {
		return errors.New("durable task database unavailable")
	}
	var completed *models.TaskCompletion
	err := config.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row models.TaskCompletion
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "id = ?", id).Error; err != nil {
			return err
		}
		if row.State == "completed" {
			if !validTaskArtifactKey(row.ID, row.ResultKey) {
				return ErrSettlementPending
			}
			if err := storage.VerifyArtifact(ctx, row.ResultKey, row.ArtifactSHA256, row.ArtifactBytes); err != nil {
				return fmt.Errorf("%w: artifact verification: %w", ErrSettlementPending, err)
			}
			completed = &row
			return nil
		}
		if row.State == "active" {
			return ErrSettlementPending
		}
		if row.State != "publishing" {
			return ErrTaskTerminal
		}
		if !validTaskArtifactKey(row.ID, row.ResultKey) {
			return ErrSettlementPending
		}
		proofErr := storage.VerifyArtifact(ctx, row.ResultKey, row.ArtifactSHA256, row.ArtifactBytes)
		if errors.Is(proofErr, storage.ErrObjectNotFound) {
			// Reuse only an already retained, known successful Worker result.
			// Native jobs without durable generated output stay safely pinned.
			proofErr = recoverTaskArtifact(ctx, tx, &row)
			if proofErr == nil {
				proofErr = storage.VerifyArtifact(ctx, row.ResultKey, row.ArtifactSHA256, row.ArtifactBytes)
			}
		}
		if proofErr != nil {
			return fmt.Errorf("%w: artifact verification: %w", ErrSettlementPending, proofErr)
		}
		if row.ReservationID != "" {
			if SettleTaskBilling == nil {
				return errors.New("durable billing finalizer unavailable")
			}
			if err := SettleTaskBilling(ctx, tx, &row); err != nil {
				return fmt.Errorf("%w: %v", ErrSettlementPending, err)
			}
		}
		row.State = "completed"
		row.ExpiresAt = time.Now().Add(TaskTTL)
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		completed = &row
		return nil
	})
	if err != nil {
		return err
	}
	// Projection ACK cannot invalidate durable success or authorize deletion.
	if client := r.getClient(); client != nil {
		data, err := marshalDurableStatus(completed)
		if err != nil {
			return err
		}
		if err := client.Set(ctx, TaskKeyPrefix+id, data, time.Until(completed.ExpiresAt)).Err(); err != nil {
			log.Printf("[TASK PROJECTION] %s: %v", id, err)
		}
	}
	return nil
}

func validTaskArtifactKey(id, key string) bool {
	return storage.CanonicalObjectKey(key) && strings.HasPrefix(key, "outputs/tasks/"+id+"/") && !strings.Contains(key, "..")
}

func recoverTaskArtifact(ctx context.Context, tx *gorm.DB, row *models.TaskCompletion) error {
	var owned models.WorkerResultOwnership
	if err := tx.Where("worker_job_id = ? AND owner_identity = ? AND state = ?", row.ID, row.OwnerIdentity, "terminal").Limit(1).Find(&owned).Error; err != nil {
		return err
	}
	var status struct {
		Status string `json:"status"`
	}
	if owned.ID == "" || json.Unmarshal([]byte(owned.StatusJSON), &status) != nil || status.Status != "succeeded" {
		return ErrSettlementPending
	}
	if owned.SubmitPath != "/api/v1/jobs/submit" && owned.SubmitPath != "/api/v1/markdown/convert" {
		return ErrSettlementPending
	}
	const prefix = "jobs/markdown/output/"
	name := strings.TrimPrefix(owned.ResultKey, prefix)
	if !storage.CanonicalObjectKey(owned.ResultKey) || !strings.HasPrefix(owned.ResultKey, prefix) || strings.Contains(name, "/") || !strings.HasSuffix(name, ".md") {
		return ErrSettlementPending
	}
	source, cleanup, err := storage.ResolveVerifiedArtifact(ctx, owned.ResultKey, row.ArtifactSHA256, row.ArtifactBytes)
	if err != nil {
		return err
	}
	defer cleanup()
	return storage.SaveVerifiedArtifact(ctx, row.ResultKey, source, row.ArtifactSHA256, row.ArtifactBytes, "text/markdown; charset=utf-8")
}

// WithReservationRelease serializes a release against artifact publication.
// Return nil for a protected reservation without changing its chosen outcome.
func WithReservationRelease(ctx context.Context, reservationID string, fn func(*gorm.DB) error) error {
	if config.DB == nil {
		return fn(nil)
	}
	return config.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row models.TaskCompletion
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("reservation_id = ?", reservationID).Limit(1).Find(&row).Error
		if err != nil {
			return err
		}
		if row.ID != "" && (row.State == "publishing" || row.State == "completed" || row.State == "deleting") {
			return nil
		}
		if row.ID != "" && row.State == "active" {
			row.State = "failed"
			row.Error = "Task reservation was released before publication."
			if err := tx.Save(&row).Error; err != nil {
				return err
			}
		}
		return fn(tx)
	})
}

// transitionDurable arbitrates failure with the publishing decision.
func transitionDurable(ctx context.Context, id, owner, state, message string) (*models.TaskCompletion, bool, error) {
	if config.DB == nil {
		return nil, false, nil
	}
	var row models.TaskCompletion
	changed := false
	err := config.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "id = ?", id).Error; err != nil {
			return err
		}
		if owner != "" && row.OwnerIdentity != "" && owner != row.OwnerIdentity {
			return nil
		}
		if row.State == "failed" && state == "failed" && message != "" {
			row.Error = message
			changed = true
			return tx.Save(&row).Error
		}
		if row.State != "active" {
			return nil
		}
		row.State = state
		row.Error = message
		changed = true
		return tx.Save(&row).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	return &row, changed, err
}

// cancelDurable atomically authorizes and cancels a durable task record.
// If the record exists in PostgreSQL, authorization is verified atomically under the row update lock
// BEFORE ANY state transition or modification occurs.
// Returns:
//   result: "CANCELLED_SUCCESS", "TERMINAL_ALREADY", "UNAUTHORIZED", or "" if not found in DB or DB unavailable
//   task: the task status representation of the row (or nil)
//   err: DB error if any
func cancelDurable(ctx context.Context, id, owner, token string) (string, *TaskStatus, error) {
	if config.DB == nil {
		return "", nil, nil
	}
	var row models.TaskCompletion
	var result string
	err := config.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "id = ?", id).Error; err != nil {
			return err
		}
		ownerMatches := row.OwnerIdentity != "" && owner != "" && row.OwnerIdentity == owner
		capabilityMatches := row.DownloadToken != "" && token != "" && row.DownloadToken == token
		if !ownerMatches && !capabilityMatches {
			result = "UNAUTHORIZED"
			return nil
		}
		if row.State != "active" {
			result = "TERMINAL_ALREADY"
			return nil
		}
		row.State = "cancelled"
		row.Error = "Task processing cancelled by user."
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		result = "CANCELLED_SUCCESS"
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil, nil
	}
	if err != nil {
		return "ERROR", nil, err
	}
	return result, durableStatus(&row), nil
}

func (r *TaskRegistry) SweepDurableCompletions(ctx context.Context, now time.Time) error {
	if config.DB == nil {
		return errors.New("durable task database unavailable")
	}
	var firstErr error
	// Separate finite queues prevent indefinitely uncertain publication from
	// starving active recovery or expired completed-artifact reclamation.
	for _, state := range []string{"active", "publishing", "retained"} {
		var rows []models.TaskCompletion
		query := config.DB.WithContext(ctx)
		if state == "retained" {
			query = query.Where("state IN ? AND expires_at <= ?", []string{"completed", "deleting"}, now)
		} else {
			query = query.Where("state = ?", state)
		}
		if err := query.Order("updated_at ASC, id ASC").Limit(100).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}

			// Move attempted, still-pending records behind untouched records. This is
			// scheduling evidence only; it never authorizes deletion or settlement.
			if updateErr := config.DB.WithContext(ctx).Model(&models.TaskCompletion{}).Where("id = ? AND state = ?", row.ID, row.State).UpdateColumn("updated_at", now).Error; updateErr != nil {
				return updateErr
			}
			var err error
			switch row.State {
			case "active":
				err = r.recoverActiveTask(ctx, &row, now)
			case "publishing":
				if row.ResultKey == "" {
					err = r.reconcileExternalTask(ctx, &row)
				} else {
					err = r.ReconcileCompletion(ctx, row.ID)
				}
			default:
				err = r.retireTaskArtifact(ctx, row.ID, now)
			}
			if err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (r *TaskRegistry) recoverActiveTask(ctx context.Context, row *models.TaskCompletion, now time.Time) error {
	if config.DB.Migrator().HasTable(&models.WorkerResultOwnership{}) {
		var owned models.WorkerResultOwnership
		if err := config.DB.WithContext(ctx).Where("worker_job_id = ?", row.ID).Limit(1).Find(&owned).Error; err != nil {
			return err
		}
		if owned.ID != "" {
			return r.reconcileExternalTask(ctx, row)
		}
	}
	if row.ExpiresAt.After(now) {
		return nil
	}
	changed := false
	err := config.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current models.TaskCompletion
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "id = ?", row.ID).Error; err != nil {
			return err
		}
		if current.State != "active" || current.ExpiresAt.After(now) {
			return nil
		}

		if tx.Migrator().HasTable(&models.WorkerResultOwnership{}) {
			var admitted int64
			if err := tx.Model(&models.WorkerResultOwnership{}).Where("worker_job_id = ?", row.ID).Count(&admitted).Error; err != nil {
				return err
			}
			if admitted > 0 {
				return nil
			}
		}
		// An external admission racing recovery is protected by its known task row:
		// external dispatch must follow this row's active decision.
		current.State = "failed"
		current.Error = "Task expired before artifact publication."
		changed = true
		return tx.Save(&current).Error
	})
	if err != nil {
		return err
	}
	if changed && row.ReservationID != "" && StaleTaskBillingHandlerWithKind != nil {
		StaleTaskBillingHandlerWithKind(row.ReservationID, row.ReservationKind)
	}
	return nil
}

func StartDurableCompletionWorker(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				if err := Registry.SweepDurableCompletions(sweepCtx, time.Now()); err != nil {
					log.Printf("[TASK RECONCILIATION] %v", err)
				}
				cancel()
			}
		}
	}()
}

func marshalDurableStatus(row *models.TaskCompletion) ([]byte, error) {
	return json.Marshal(durableStatus(row))
}

func allowDurableUpdate(id, status, message string) (bool, error) {
	row, err := durableTask(context.Background(), id)
	if err != nil {
		return false, err
	}
	if row == nil {
		return true, nil
	}
	if status == "FAILED" || status == "CANCELLED" {
		if message == "" {
			message = "Task processing failed or cancelled."
		}
		_, changed, err := transitionDurable(context.Background(), id, "", strings.ToLower(status), message)
		return changed, err
	}
	if status == "COMPLETED" {
		return row.State == "completed", nil
	}
	return row.State == "active", nil
}

func (r *TaskRegistry) reconcileExternalTask(ctx context.Context, row *models.TaskCompletion) error {
	var owned models.WorkerResultOwnership
	err := config.DB.WithContext(ctx).Where("worker_job_id = ?", row.ID).First(&owned).Error
	if err != nil {
		return err
	}
	if owned.State != "terminal" {
		return ErrSettlementPending
	}
	var status struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(owned.StatusJSON), &status); err != nil {
		return err
	}
	if status.Status == "succeeded" {
		if !strings.HasPrefix(owned.ResultKey, "jobs/markdown/output/") || strings.Contains(owned.ResultKey, "..") {
			return errors.New("unexpected legacy Worker artifact namespace")
		}
		path, cleanup, err := storage.ResolveObject(ctx, owned.ResultKey, "task-worker-result-", ".md")
		if err != nil {
			return err
		}
		defer cleanup()
		return r.PublishArtifact(ctx, row.ID, "outputs/tasks/"+row.ID+"/result.md", path, "text/markdown; charset=utf-8")
	}
	if status.Status != "failed" && status.Status != "cancelled" {
		return ErrSettlementPending
	}
	err = config.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current models.TaskCompletion
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "id = ?", row.ID).Error; err != nil {
			return err
		}
		if current.State != "active" || current.ResultKey != "" {
			return nil
		}
		current.State = status.Status
		current.Error = "Worker processing did not publish a usable artifact."
		return tx.Save(&current).Error
	})
	if err != nil {
		return err
	}
	if StaleTaskBillingHandlerWithKind != nil {
		StaleTaskBillingHandlerWithKind(row.ReservationID, row.ReservationKind)
	}
	return nil
}

func (r *TaskRegistry) retireTaskArtifact(ctx context.Context, id string, now time.Time) error {
	claimed := false
	err := config.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row models.TaskCompletion
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "id = ?", id).Error; err != nil {
			return err
		}
		if (row.State != "completed" && row.State != "deleting") || row.ExpiresAt.After(now) {
			return nil
		}
		if row.State == "deleting" {
			claimed = true
			return nil
		}
		key := row.ResultKey
		if !validTaskArtifactKey(id, key) {
			return errors.New("invalid persisted artifact ownership")
		}
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?,81807))", key).Error; err != nil {
			return err
		}
		for _, table := range []string{"studio_assets", "studio_exports"} {
			if !tx.Migrator().HasTable(table) {
				continue
			}
			var count int64
			if err := tx.Table(table).Where("r2_key = ?", key).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return nil
			}
		}
		if tx.Migrator().HasTable("studio_jobs") {
			encoded, _ := json.Marshal([]string{key})
			var count int64
			if err := tx.Table("studio_jobs").Where("source_key = ? OR payload_key = ? OR pending_object_keys @> ?::jsonb", key, key, string(encoded)).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return nil
			}
		}
		if tx.Migrator().HasTable(&models.WorkerResultOwnership{}) {
			encoded, _ := json.Marshal([]string{key})
			var count int64
			if err := tx.Model(&models.WorkerResultOwnership{}).Where("state <> ? AND (source_keys::jsonb @> ?::jsonb OR result_key = ? OR metadata_key = ?)", "deleted", string(encoded), key, key).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return nil
			}
		}
		// This tombstone and the deletion decision commit before storage I/O.
		if err := tx.Table("studio_storage_object_retirements").Clauses(clause.OnConflict{DoNothing: true}).Create(map[string]any{"object_key": key, "created_at": now}).Error; err != nil {
			return err
		}
		row.State = "deleting"
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		claimed = true
		return nil
	})
	if err != nil || !claimed {
		return err
	}
	return config.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row models.TaskCompletion
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "id = ?", id).Error; err != nil {
			return err
		}
		if row.State != "deleting" {
			return nil
		}
		if err := storage.DeleteObject(ctx, row.ResultKey); err != nil {
			return err
		}
		row.State = "deleted"
		return tx.Save(&row).Error
	})
}
