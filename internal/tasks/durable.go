package tasks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
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
	if !strings.HasPrefix(key, "outputs/tasks/"+id+"/") || strings.Contains(key, "..") {
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
	file, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("%w: artifact source: %v", ErrSettlementPending, err)
	}
	digest := sha256.New()
	size, hashErr := io.Copy(digest, file)
	file.Close()
	if hashErr != nil {
		return hashErr
	}
	fingerprint := hex.EncodeToString(digest.Sum(nil))
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
		if row.State == "publishing" && (row.ArtifactSHA256 != fingerprint || row.ArtifactBytes != size) {
			return errors.New("artifact identity already chosen")
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
		if storage.ObjectExists(ctx, key) {
			return nil
		}
		return storage.SaveObjectFile(ctx, key, source, contentType)
	})
	if err != nil {
		return fmt.Errorf("%w: artifact write: %v", ErrSettlementPending, err)
	}
	return r.ReconcileCompletion(ctx, id)
}

// ReconcileCompletion keeps the ownership row locked across availability proof
// and settlement. All application reclamation and reservation release paths
// coordinate through the same row. A process crash rolls back DB settlement;
// guest Redis receipts preserve the outcome of an ambiguous remote ACK.
func (r *TaskRegistry) ReconcileCompletion(ctx context.Context, id string) error {
	var completed *models.TaskCompletion
	err := config.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row models.TaskCompletion
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "id = ?", id).Error; err != nil {
			return err
		}
		if row.State == "completed" {
			completed = &row
			return nil
		}
		if row.State == "active" {
			return ErrSettlementPending
		}
		if row.State != "publishing" {
			return ErrTaskTerminal
		}
		if !storage.ObjectExists(ctx, row.ResultKey) {
			return ErrSettlementPending
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

// transitionDurable arbitrates cancellation/failure with the publishing decision.
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
		if (owner != "" || state == "cancelled") && row.OwnerIdentity != "" && owner != row.OwnerIdentity {
			return nil
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

func (r *TaskRegistry) SweepDurableCompletions(ctx context.Context, now time.Time) error {
	if config.DB == nil {
		return errors.New("durable task database unavailable")
	}
	var rows []models.TaskCompletion
	if err := config.DB.WithContext(ctx).Where("state = ? OR (state IN ? AND expires_at <= ?)", "publishing", []string{"completed", "deleting"}, now).Order("updated_at ASC").Limit(100).Find(&rows).Error; err != nil {
		return err
	}
	var firstErr error
	for _, row := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if row.State == "publishing" {
			if err := r.ReconcileCompletion(ctx, row.ID); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		err := config.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var current models.TaskCompletion
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "id = ?", row.ID).Error; err != nil {
				return err
			}
			if (current.State != "completed" && current.State != "deleting") || current.ExpiresAt.After(now) {
				return nil
			}
			// Ownership is exclusive to one task prefix; no shared inputs are accepted.
			if !strings.HasPrefix(current.ResultKey, "outputs/tasks/"+current.ID+"/") {
				return errors.New("invalid persisted artifact ownership")
			}
			if err := storage.DeleteObject(ctx, current.ResultKey); err != nil {
				return err
			}
			current.State = "deleted"
			return tx.Save(&current).Error
		})
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
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

func allowDurableUpdate(id, status string) (bool, error) {
	row, err := durableTask(context.Background(), id)
	if err != nil {
		return false, err
	}
	if row == nil {
		return true, nil
	}
	if status == "FAILED" || status == "CANCELLED" {
		_, changed, err := transitionDurable(context.Background(), id, "", strings.ToLower(status), "Task processing failed or cancelled.")
		return changed, err
	}
	if status == "COMPLETED" {
		return row.State == "completed", nil
	}
	return row.State == "active", nil
}
