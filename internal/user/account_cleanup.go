package user

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"pdfnest-backend/config"
	analyzerModels "pdfnest-backend/internal/analyzer/models"
	"pdfnest-backend/internal/authn"
	backendModels "pdfnest-backend/internal/models"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/studio"
	studioModels "pdfnest-backend/internal/studio/models"
)

func deleteAccountWithCleanup(ctx context.Context, db *gorm.DB, id string, version int64) error {
	if _, err := uuid.Parse(id); err != nil {
		return authn.ErrInvalid
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var account config.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&account, "id = ?", id).Error; err != nil {
			return err
		}
		if account.SessionVersion != version || account.Status != "active" || !account.EmailVerified {
			return authn.ErrInvalid
		}
		now := time.Now().UTC()
		// Commit retry authority before authentication/ownership can disappear.
		intent := config.AccountStorageCleanup{UserID: id, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&intent).Error; err != nil {
			return err
		}
		if err := tx.Model(&account).Updates(map[string]any{"status": "deleted", "session_version": gorm.Expr("session_version + 1"), "updated_at": now}).Error; err != nil {
			return err
		}
		return tx.Delete(&account).Error
	})
}

type AccountCleanupWorker struct{ db *gorm.DB }

func NewAccountCleanupWorker(db *gorm.DB) *AccountCleanupWorker { return &AccountCleanupWorker{db: db} }
func StartAccountCleanupWorker(ctx context.Context, db *gorm.DB) {
	if err := config.ValidateAccountCleanupSchema(db); err != nil {
		log.Printf("[ACCOUNT CLEANUP] unavailable: %v", err)
		return
	}
	w := NewAccountCleanupWorker(db)
	go func() {
		w.RunOnceAt(ctx, time.Now().UTC())
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if err := w.RunOnceAt(ctx, now.UTC()); err != nil {
					log.Printf("[ACCOUNT CLEANUP] retry required: %v", err)
				}
			}
		}
	}()
}

// RunOnceAt leases at most ten accounts. A token fences an expired claimant;
// individual account work holds its row lock and has a finite overall deadline.
func (w *AccountCleanupWorker) RunOnceAt(ctx context.Context, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var rows []config.AccountStorageCleanup
	err := w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("next_attempt_at <= ? AND (lease_expires_at IS NULL OR lease_expires_at < ?)", now, now).Order("next_attempt_at,user_id").Limit(10).Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Find(&rows).Error; err != nil {
			return err
		}
		for i := range rows {
			until := now.Add(2 * time.Minute)
			rows[i].LeaseToken = uuid.NewString()
			rows[i].LeaseExpiresAt = &until
			if err := tx.Model(&rows[i]).Updates(map[string]any{"lease_token": rows[i].LeaseToken, "lease_expires_at": until}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	var first error
	for _, row := range rows {
		err := w.process(ctx, row, now)
		if err != nil {
			if first == nil {
				first = err
			}
			_ = w.db.WithContext(ctx).Model(&config.AccountStorageCleanup{}).Where("user_id = ? AND lease_token = ?", row.UserID, row.LeaseToken).Updates(map[string]any{"lease_expires_at": nil, "lease_token": nil, "next_attempt_at": now.Add(15 * time.Minute), "attempts": gorm.Expr("attempts + 1"), "last_error": "ownership discovery deferred"}).Error
		}
	}
	return first
}

func (w *AccountCleanupWorker) process(ctx context.Context, claimed config.AccountStorageCleanup, now time.Time) error {
	return w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var intent config.AccountStorageCleanup
		if err := tx.Where("user_id = ? AND lease_token = ?", claimed.UserID, claimed.LeaseToken).Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).First(&intent).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		var account config.User
		if err := tx.Unscoped().First(&account, "id = ?", intent.UserID).Error; err != nil {
			return err
		}
		if !account.DeletedAt.Valid {
			return fmt.Errorf("account still active")
		}
		// Never infer drainage from account deletion or a missing queue receipt.
		for _, q := range []*gorm.DB{
			tx.Model(&backendModels.TaskCompletion{}).Where("owner_identity = ? AND state IN ?", intent.UserID, []string{"active", "publishing", "deleting", "artifact_recovery_hold"}),
			tx.Model(&backendModels.WorkerResultOwnership{}).Where("owner_identity = ? AND state NOT IN ?", intent.UserID, []string{"terminal", "deleted"}),
			tx.Model(&studioModels.StudioJob{}).Where("session_id IN (SELECT id FROM studio_sessions WHERE user_id = ?) AND (status NOT IN ? OR (status = 'succeeded' AND reconciled_at IS NULL))", intent.UserID, []string{"succeeded", "failed", "cancelled"}),
		} {
			var n int64
			if err := q.Count(&n).Error; err != nil {
				return err
			}
			if n != 0 {
				return fmt.Errorf("active or uncertain work retains ownership")
			}
		}
		repo := studio.NewRepository(tx)
		enqueue := func(keys []string) error {
			for _, key := range keys {
				if key != "" && !storage.CanonicalObjectKey(key) {
					return fmt.Errorf("unsafe catalogued object key")
				}
			}
			_, err := repo.CreateStorageCleanupTasks(ctx, keys)
			return err
		}
		var tasks []backendModels.TaskCompletion
		if err := tx.Where("owner_identity = ? AND state <> ?", intent.UserID, "deleted").Order("id").Limit(25).Clauses(clause.Locking{Strength: "UPDATE"}).Find(&tasks).Error; err != nil {
			return err
		}
		for _, task := range tasks {
			if err := enqueue([]string{task.ResultKey}); err != nil {
				return err
			}
			if err := tx.Model(&task).Updates(map[string]any{"state": "deleted", "download_token": "", "result_url": ""}).Error; err != nil {
				return err
			}
		}
		var workers []backendModels.WorkerResultOwnership
		if err := tx.Where("owner_identity = ? AND state = ?", intent.UserID, "terminal").Order("id").Limit(25).Clauses(clause.Locking{Strength: "UPDATE"}).Find(&workers).Error; err != nil {
			return err
		}
		for _, worker := range workers {
			var sources []string
			if err := json.Unmarshal([]byte(worker.SourceKeys), &sources); err != nil {
				return err
			}
			keys := append(sources, worker.ResultKey, worker.MetadataKey)
			if err := enqueue(keys); err != nil {
				return err
			}
			if err := tx.Model(&worker).Updates(map[string]any{"state": "deleted", "status_json": ""}).Error; err != nil {
				return err
			}
		}
		var sessions []analyzerModels.AnalyzerSession
		if err := tx.Where("owner_identity = ?", intent.UserID).Order("id").Limit(25).Clauses(clause.Locking{Strength: "UPDATE"}).Find(&sessions).Error; err != nil {
			return err
		}
		for _, session := range sessions {
			if session.StorageKey != nil {
				if err := enqueue([]string{*session.StorageKey}); err != nil {
					return err
				}
			}
			if err := tx.Delete(&session).Error; err != nil {
				return err
			}
		}
		var studios []studioModels.StudioSession
		if err := tx.Unscoped().Where("user_id = ?", intent.UserID).Order("id").Limit(25).Clauses(clause.Locking{Strength: "UPDATE"}).Find(&studios).Error; err != nil {
			return err
		}
		for _, session := range studios {
			var siblings int64
			if err := tx.Unscoped().Model(&studioModels.StudioSession{}).Where("document_id = ? AND id <> ?", session.DocumentID, session.ID).Count(&siblings).Error; err != nil {
				return err
			}
			if siblings > 0 {
				if err := tx.Unscoped().Delete(&session).Error; err != nil {
					return err
				}
				continue
			}
			// The ordinary workspace deletion path locks only live sessions.
			// Restore visibility inside this transaction solely to reuse its
			// FK-safe graph/outbox operation; no restored row can commit.
			if session.DeletedAt.Valid {
				if err := tx.Unscoped().Model(&studioModels.StudioSession{}).Where("id = ?", session.ID).Update("deleted_at", nil).Error; err != nil {
					return err
				}
			}
			if _, err := repo.DeleteSessionWorkspace(ctx, session.ID, session.DocumentID); err != nil {
				return err
			}
		}
		// Catalogued keys commit with ownership removal. Prefix discovery handles
		// presigned uploads that never acquired a catalog row. Keep the small account
		// intent for periodic rescans because old upload grants/in-flight admissions
		// can finish after deletion; a quiet pass is not proof they cannot arrive.
		purposes := []string{"image_to_text_pdf", "repository_analyzer", "studio_source"}
		purpose := intent.ScanPurpose % len(purposes)
		keys, cursor, err := storage.ListOwnedObjects(ctx, intent.UserID, purposes[purpose], intent.ScanCursor, 50)
		if err != nil {
			return err
		}
		if err := enqueue(keys); err != nil {
			return err
		}
		delay := time.Minute
		if cursor == "" {
			purpose = (purpose + 1) % len(purposes)
			if purpose == 0 {
				delay = 24 * time.Hour
			}
		}
		if len(tasks) == 25 || len(workers) == 25 || len(sessions) == 25 || len(studios) == 25 {
			delay = time.Minute
		}
		return tx.Model(&intent).Updates(map[string]any{"scan_purpose": purpose, "scan_cursor": cursor, "lease_token": nil, "lease_expires_at": nil, "next_attempt_at": now.Add(delay), "last_error": "", "attempts": gorm.Expr("attempts + 1"), "updated_at": now}).Error
	})
}
