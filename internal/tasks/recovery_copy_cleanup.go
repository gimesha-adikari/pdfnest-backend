package tasks

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"pdfnest-backend/config"
	"pdfnest-backend/internal/models"
	"pdfnest-backend/internal/storage"
)

// RecoveryCopyObligation is written BEFORE I/O. Each attempt owns a distinct,
// non-reassignable key. Tombstones are retained and rechecked after deletion:
// a timed-out provider PUT can become visible after an earlier successful DELETE.
type RecoveryCopyObligation struct {
	ActionID              string `gorm:"primaryKey;type:uuid"`
	Attempt               int    `gorm:"primaryKey"`
	TaskID                string
	OwnerIdentity         string
	ObjectKey             string `gorm:"uniqueIndex"`
	Digest                string
	Bytes                 int64
	NextCheck             time.Time `gorm:"index"`
	LastDeleteConfirmedAt *time.Time
	LastErrorCode         string
}

func recoveryAttemptKey(taskID, actionID string, attempt int) string {
	if attempt == 1 {
		return artifactRecoveryObjectKey(taskID, actionID)
	}
	return artifactRecoveryObjectKey(taskID, fmt.Sprintf("%s:attempt:%d", actionID, attempt))
}

func recoveryCopySchemaReady(db *gorm.DB) error {
	var n int64
	if err := db.Raw("SELECT count(*) FROM backend_durability_schema WHERE version = ?", "20261105_recovery_copies_v1").Scan(&n).Error; err != nil {
		return err
	}
	if n != 1 {
		return errors.New("recovery copy cleanup migration required")
	}
	return nil
}

func recordRecoveryCopy(ctx context.Context, action models.ArtifactRecoveryAction) error {
	if err := recoveryCopySchemaReady(config.DB.WithContext(ctx)); err != nil {
		return err
	}
	if action.LeaseExpiresAt == nil || action.RecoveryKey != recoveryAttemptKey(action.TaskID, action.ID, action.Attempts) {
		return ErrRecoveryInvalidRequest
	}
	row := RecoveryCopyObligation{ActionID: action.ID, Attempt: action.Attempts, TaskID: action.TaskID, OwnerIdentity: action.OwnerIdentity, ObjectKey: action.RecoveryKey, Digest: action.ArtifactSHA256, Bytes: action.ArtifactBytes, NextCheck: action.LeaseExpiresAt.Add(time.Minute)}
	return config.DB.WithContext(ctx).Create(&row).Error
}

// SweepRecoveryCopies is bounded and serializes each key across replicas with a
// database row lock. It never trusts a public key or changes task/billing state.
func SweepRecoveryCopies(ctx context.Context, db *gorm.DB, limit int) error {
	return sweepRecoveryCopies(ctx, db, limit, storage.DeleteObject)
}
func sweepRecoveryCopies(ctx context.Context, db *gorm.DB, limit int, remove func(context.Context, string) error) error {
	if limit < 1 || limit > 100 {
		return ErrRecoveryInvalidRequest
	}
	if err := recoveryCopySchemaReady(db.WithContext(ctx)); err != nil {
		return err
	}
	for i := 0; i < limit; i++ {
		found := false
		err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var now time.Time
			if err := tx.Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
				return err
			}
			var row RecoveryCopyObligation
			err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("next_check <= clock_timestamp()").Order("next_check, action_id, attempt").First(&row).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			found = true
			var action models.ArtifactRecoveryAction
			if err := tx.First(&action, "id = ?", row.ActionID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return tx.Model(&row).Updates(map[string]any{"next_check": now.Add(24 * time.Hour), "last_error_code": "identity_mismatch"}).Error
				}
				return err
			}
			if row.TaskID != action.TaskID || row.OwnerIdentity != action.OwnerIdentity || row.Digest != action.ArtifactSHA256 || row.Bytes != action.ArtifactBytes || row.Attempt < 1 || row.Attempt > action.Attempts || row.ObjectKey != recoveryAttemptKey(row.TaskID, row.ActionID, row.Attempt) {
				return tx.Model(&row).Updates(map[string]any{"next_check": now.Add(24 * time.Hour), "last_error_code": "identity_mismatch"}).Error
			}
			// Successful copies remain available as evidence until the original retention
			// deadline. Active attempts are never reclaimed before their lease expires.
			next := now.Add(time.Hour)
			if action.Attempts == row.Attempt && ((action.State == "processing" && action.LeaseExpiresAt != nil && action.LeaseExpiresAt.Add(time.Minute).After(now)) || (action.State == "recovered_pending_retention_decision" && action.OriginalExpiresAt.After(now))) {
				return tx.Model(&row).Update("next_check", next).Error
			}
			var referenced int64
			if err := tx.Model(&models.TaskCompletion{}).Where("result_key = ?", row.ObjectKey).Count(&referenced).Error; err != nil {
				return err
			}
			if referenced != 0 {
				return tx.Model(&row).Updates(map[string]any{"next_check": next, "last_error_code": "referenced_object"}).Error
			}
			deleteCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			err = remove(deleteCtx, row.ObjectKey)
			updates := map[string]any{"next_check": next, "last_error_code": ""}
			if err != nil {
				updates["last_error_code"] = "storage_delete_deferred"
			} else {
				updates["last_delete_confirmed_at"] = now
			}
			// Even a confirmed absence is not terminal: late uploads remain collectible.
			return tx.Model(&row).Updates(updates).Error
		})
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
	}
	return nil
}

func StartRecoveryCopyCleanupWorker(ctx context.Context, db *gorm.DB) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if recoveryCopySchemaReady(db.WithContext(ctx)) != nil {
					continue
				}
				sweepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				if err := SweepRecoveryCopies(sweepCtx, db, 20); err != nil {
					log.Printf("[RECOVERY COPY CLEANUP] retry required")
				}
				cancel()
			}
		}
	}()
}
