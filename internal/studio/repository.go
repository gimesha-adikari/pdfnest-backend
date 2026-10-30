package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"pdfnest-backend/internal/studio/models"
)

// Repository defines data access interfaces for Studio V2 persistence.
type Repository interface {
	WithTransaction(ctx context.Context, fn func(txRepo Repository, tx *gorm.DB) error) error
	CreateDocumentWithInitialVersion(ctx context.Context, doc *models.StudioDocument, asset *models.StudioAsset, ver *models.StudioVersion, sess *models.StudioSession) error
	GetSession(ctx context.Context, sessionID uuid.UUID) (*models.StudioSession, error)
	ListUserSessions(ctx context.Context, userID uuid.UUID, query string, sort string, offset int, limit int) ([]models.StudioSession, int64, error)
	UpdateSessionTitle(ctx context.Context, sessionID uuid.UUID, title string) error
	GetDocument(ctx context.Context, docID uuid.UUID) (*models.StudioDocument, error)
	GetVersion(ctx context.Context, verID uuid.UUID) (*models.StudioVersion, error)
	LockSession(ctx context.Context, sessionID uuid.UUID) (*models.StudioSession, error)
	LockJob(ctx context.Context, jobID uuid.UUID) (*models.StudioJob, error)
	FindOperationByIdempotencyKey(ctx context.Context, docID uuid.UUID, key string) (*models.StudioOperation, *models.StudioVersion, error)
	CreateVersionAndOperation(ctx context.Context, ver *models.StudioVersion, op *models.StudioOperation, sessID uuid.UUID, parentVerID *uuid.UUID) error
	CreateDetachedVersionAndOperation(ctx context.Context, ver *models.StudioVersion, op *models.StudioOperation) error
	UpdateSessionActiveVersion(ctx context.Context, sessID uuid.UUID, verID uuid.UUID) error
	UpdateVersionPreferredChild(ctx context.Context, parentVerID uuid.UUID, childVerID uuid.UUID) error
	GetVersionHistory(ctx context.Context, docID uuid.UUID) ([]models.StudioVersion, []models.StudioOperation, error)
	CreateSnapshot(ctx context.Context, snap *models.StudioSnapshot) error
	GetSnapshot(ctx context.Context, snapID uuid.UUID) (*models.StudioSnapshot, error)
	CreateAsset(ctx context.Context, asset *models.StudioAsset) error
	GetAsset(ctx context.Context, assetID string) (*models.StudioAsset, error)
	CreateExport(ctx context.Context, export *models.StudioExport) error
	GetExport(ctx context.Context, exportID uuid.UUID) (*models.StudioExport, error)
	TouchSession(ctx context.Context, sessID uuid.UUID) error
	CreateJob(ctx context.Context, job *models.StudioJob) error
	GetJob(ctx context.Context, jobID uuid.UUID) (*models.StudioJob, error)
	ListReconciliationJobs(ctx context.Context, limit int) ([]models.StudioJob, error)
	FindJobByIdempotencyKey(ctx context.Context, docID uuid.UUID, key string) (*models.StudioJob, error)
	SaveJob(ctx context.Context, job *models.StudioJob) error
	CreateEditorState(ctx context.Context, state *models.StudioEditorState) error
	GetEditorState(ctx context.Context, id uuid.UUID) (*models.StudioEditorState, error)
	GetEditorStateByExtractJob(ctx context.Context, jobID uuid.UUID) (*models.StudioEditorState, error)
	GetLatestEditorStateForVersion(ctx context.Context, sessionID, baseVersionID uuid.UUID) (*models.StudioEditorState, error)
	DeleteSessionWorkspace(ctx context.Context, sessionID uuid.UUID, documentID uuid.UUID) ([]models.StudioStorageCleanupTask, error)
	EnqueueExpiredStorageCleanup(ctx context.Context, now time.Time, limit int) error
	CreateStorageCleanupTasks(ctx context.Context, keys []string) ([]models.StudioStorageCleanupTask, error)
	ClaimStorageCleanupTasks(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]models.StudioStorageCleanupTask, error)
	DeleteStorageCleanupTask(ctx context.Context, taskID uuid.UUID) error
	RescheduleStorageCleanupTask(ctx context.Context, taskID uuid.UUID, attempts int, nextAttemptAt time.Time, lastError string) error
}

type gormRepository struct {
	db *gorm.DB
}

// NewRepository initializes a GORM-backed Studio repository.
func NewRepository(db *gorm.DB) Repository {
	return &gormRepository{db: db}
}

func (r *gormRepository) WithTransaction(ctx context.Context, fn func(txRepo Repository, tx *gorm.DB) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txRepo := &gormRepository{db: tx}
		return fn(txRepo, tx)
	})
}

func (r *gormRepository) CreateDocumentWithInitialVersion(
	ctx context.Context,
	doc *models.StudioDocument,
	asset *models.StudioAsset,
	ver *models.StudioVersion,
	sess *models.StudioSession,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(doc).Error; err != nil {
			return err
		}
		if asset != nil {
			if err := (&gormRepository{db: tx}).CreateAsset(ctx, asset); err != nil {
				return err
			}
		}
		if err := tx.Create(ver).Error; err != nil {
			return err
		}
		if err := tx.Create(sess).Error; err != nil {
			return err
		}
		return nil
	})
}

func (r *gormRepository) GetSession(ctx context.Context, sessionID uuid.UUID) (*models.StudioSession, error) {
	var sess models.StudioSession
	err := r.db.WithContext(ctx).First(&sess, "id = ?", sessionID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSessionNotFound
		}
		return nil, err
	}
	return &sess, nil
}

func (r *gormRepository) ListUserSessions(ctx context.Context, userID uuid.UUID, query string, sort string, offset int, limit int) ([]models.StudioSession, int64, error) {
	base := r.db.WithContext(ctx).Model(&models.StudioSession{}).Where("studio_sessions.user_id = ?", userID).Preload("Document").Preload("ActiveVersion")
	countQuery := r.db.WithContext(ctx).Model(&models.StudioSession{}).Where("studio_sessions.user_id = ?", userID)
	if query != "" {
		like := "%" + query + "%"
		join := "JOIN studio_documents ON studio_documents.id = studio_sessions.document_id"
		base = base.Joins(join).Where("studio_sessions.title ILIKE ? OR studio_documents.original_file_name ILIKE ?", like, like)
		countQuery = countQuery.Joins(join).Where("studio_sessions.title ILIKE ? OR studio_documents.original_file_name ILIKE ?", like, like)
	}
	var total int64
	if err := countQuery.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var sessions []models.StudioSession
	order := "studio_sessions.last_accessed_at DESC"
	if sort == "created" {
		order = "studio_sessions.created_at DESC"
	}
	if err := base.Order(order).Offset(offset).Limit(limit).Find(&sessions).Error; err != nil {
		return nil, 0, err
	}
	return sessions, total, nil
}

func (r *gormRepository) UpdateSessionTitle(ctx context.Context, sessionID uuid.UUID, title string) error {
	return r.db.WithContext(ctx).Model(&models.StudioSession{}).Where("id = ?", sessionID).Update("title", title).Error
}

func (r *gormRepository) GetDocument(ctx context.Context, docID uuid.UUID) (*models.StudioDocument, error) {
	var doc models.StudioDocument
	err := r.db.WithContext(ctx).First(&doc, "id = ?", docID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrDocumentNotFound
		}
		return nil, err
	}
	return &doc, nil
}

func (r *gormRepository) GetVersion(ctx context.Context, verID uuid.UUID) (*models.StudioVersion, error) {
	var ver models.StudioVersion
	err := r.db.WithContext(ctx).First(&ver, "id = ?", verID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVersionNotFound
		}
		return nil, err
	}
	return &ver, nil
}

func (r *gormRepository) LockSession(ctx context.Context, sessionID uuid.UUID) (*models.StudioSession, error) {
	var sess models.StudioSession
	err := r.db.WithContext(ctx).
		Raw("SELECT id, user_id, guest_token_hash, document_id, active_version_id, created_at, last_accessed_at, expires_at FROM studio_sessions WHERE id = ? AND deleted_at IS NULL FOR UPDATE", sessionID).
		Scan(&sess).Error
	if err != nil {
		return nil, err
	}
	if sess.ID == uuid.Nil {
		return nil, ErrSessionNotFound
	}
	return &sess, nil
}

func (r *gormRepository) FindOperationByIdempotencyKey(ctx context.Context, docID uuid.UUID, key string) (*models.StudioOperation, *models.StudioVersion, error) {
	var op models.StudioOperation
	err := r.db.WithContext(ctx).Where("document_id = ? AND idempotency_key = ?", docID, key).First(&op).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	var ver models.StudioVersion
	err = r.db.WithContext(ctx).First(&ver, "id = ?", op.VersionID).Error
	if err != nil {
		return nil, nil, err
	}
	return &op, &ver, nil
}

func (r *gormRepository) CreateVersionAndOperation(
	ctx context.Context,
	ver *models.StudioVersion,
	op *models.StudioOperation,
	sessID uuid.UUID,
	parentVerID *uuid.UUID,
) error {
	if err := r.db.WithContext(ctx).Create(ver).Error; err != nil {
		return err
	}
	if err := r.db.WithContext(ctx).Create(op).Error; err != nil {
		return err
	}
	if err := r.db.WithContext(ctx).Model(&models.StudioSession{}).
		Where("id = ?", sessID).
		Updates(map[string]interface{}{
			"active_version_id": ver.ID,
			"last_accessed_at":  time.Now().UTC(),
		}).Error; err != nil {
		return err
	}
	if parentVerID != nil && *parentVerID != uuid.Nil {
		if err := r.db.WithContext(ctx).Model(&models.StudioVersion{}).
			Where("id = ?", *parentVerID).
			Update("preferred_child_id", ver.ID).Error; err != nil {
			return err
		}
	}
	return nil
}

func (r *gormRepository) CreateDetachedVersionAndOperation(ctx context.Context, ver *models.StudioVersion, op *models.StudioOperation) error {
	if err := r.db.WithContext(ctx).Create(ver).Error; err != nil {
		return err
	}
	return r.db.WithContext(ctx).Create(op).Error
}

func (r *gormRepository) UpdateSessionActiveVersion(ctx context.Context, sessID uuid.UUID, verID uuid.UUID) error {
	return r.db.WithContext(ctx).Model(&models.StudioSession{}).
		Where("id = ?", sessID).
		Updates(map[string]interface{}{
			"active_version_id": verID,
			"last_accessed_at":  time.Now().UTC(),
		}).Error
}

func (r *gormRepository) UpdateVersionPreferredChild(ctx context.Context, parentVerID uuid.UUID, childVerID uuid.UUID) error {
	return r.db.WithContext(ctx).Model(&models.StudioVersion{}).
		Where("id = ?", parentVerID).
		Update("preferred_child_id", childVerID).Error
}

func (r *gormRepository) GetVersionHistory(ctx context.Context, docID uuid.UUID) ([]models.StudioVersion, []models.StudioOperation, error) {
	var versions []models.StudioVersion
	if err := r.db.WithContext(ctx).Where("document_id = ?", docID).Order("version_number ASC, created_at ASC").Find(&versions).Error; err != nil {
		return nil, nil, err
	}

	var operations []models.StudioOperation
	if err := r.db.WithContext(ctx).Where("document_id = ?", docID).Order("created_at ASC").Find(&operations).Error; err != nil {
		return nil, nil, err
	}
	return versions, operations, nil
}

func (r *gormRepository) CreateSnapshot(ctx context.Context, snap *models.StudioSnapshot) error {
	return r.db.WithContext(ctx).Create(snap).Error
}

func (r *gormRepository) GetSnapshot(ctx context.Context, snapID uuid.UUID) (*models.StudioSnapshot, error) {
	var snap models.StudioSnapshot
	err := r.db.WithContext(ctx).First(&snap, "id = ?", snapID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSnapshotNotFound
		}
		return nil, err
	}
	return &snap, nil
}

func (r *gormRepository) CreateAsset(ctx context.Context, asset *models.StudioAsset) error {
	return r.withActiveObjectKeys(ctx, []string{asset.R2Key}, func(tx *gorm.DB) error { return tx.Create(asset).Error })
}

func (r *gormRepository) GetAsset(ctx context.Context, assetID string) (*models.StudioAsset, error) {
	var asset models.StudioAsset
	err := r.db.WithContext(ctx).First(&asset, "id = ?", assetID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAssetNotFound
		}
		return nil, err
	}
	return &asset, nil
}

func (r *gormRepository) CreateExport(ctx context.Context, export *models.StudioExport) error {
	return r.withActiveObjectKeys(ctx, []string{export.R2Key}, func(tx *gorm.DB) error { return tx.Create(export).Error })
}

func (r *gormRepository) GetExport(ctx context.Context, exportID uuid.UUID) (*models.StudioExport, error) {
	var export models.StudioExport
	err := r.db.WithContext(ctx).First(&export, "id = ?", exportID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrExportNotFound
		}
		return nil, err
	}
	return &export, nil
}

func (r *gormRepository) TouchSession(ctx context.Context, sessID uuid.UUID) error {
	return r.db.WithContext(ctx).Model(&models.StudioSession{}).
		Where("id = ?", sessID).
		Update("last_accessed_at", time.Now().UTC()).Error
}

func (r *gormRepository) CreateJob(ctx context.Context, job *models.StudioJob) error {
	return r.saveJobWithObjectOwnership(ctx, job, false)
}

func (r *gormRepository) GetJob(ctx context.Context, jobID uuid.UUID) (*models.StudioJob, error) {
	var job models.StudioJob
	if err := r.db.WithContext(ctx).First(&job, "id = ?", jobID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrJobNotFound
		}
		return nil, err
	}
	return &job, nil
}

func (r *gormRepository) LockJob(ctx context.Context, jobID uuid.UUID) (*models.StudioJob, error) {
	var job models.StudioJob
	if err := r.db.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&job, "id = ?", jobID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrJobNotFound
		}
		return nil, err
	}
	return &job, nil
}

func (r *gormRepository) ListReconciliationJobs(ctx context.Context, limit int) ([]models.StudioJob, error) {
	if limit <= 0 {
		limit = 25
	}
	var jobs []models.StudioJob
	eligible := []string{"queued", "running", "processing", "cancel_requested", "succeeded"}
	err := r.db.WithContext(ctx).
		Where("status IN ? AND worker_job_id <> '' AND reconciled_at IS NULL", eligible).
		Order("created_at ASC").Limit(limit).Find(&jobs).Error
	return jobs, err
}

func (r *gormRepository) FindJobByIdempotencyKey(ctx context.Context, docID uuid.UUID, key string) (*models.StudioJob, error) {
	var job models.StudioJob
	if err := r.db.WithContext(ctx).Where("document_id = ? AND idempotency_key = ?", docID, key).First(&job).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &job, nil
}

func (r *gormRepository) SaveJob(ctx context.Context, job *models.StudioJob) error {
	return r.saveJobWithObjectOwnership(ctx, job, true)
}

func (r *gormRepository) CreateEditorState(ctx context.Context, state *models.StudioEditorState) error {
	return r.db.WithContext(ctx).Create(state).Error
}
func (r *gormRepository) GetEditorState(ctx context.Context, id uuid.UUID) (*models.StudioEditorState, error) {
	var state models.StudioEditorState
	if err := r.db.WithContext(ctx).First(&state, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrEditorStateNotFound
		}
		return nil, err
	}
	return &state, nil
}
func (r *gormRepository) GetEditorStateByExtractJob(ctx context.Context, jobID uuid.UUID) (*models.StudioEditorState, error) {
	var state models.StudioEditorState
	if err := r.db.WithContext(ctx).Where("extract_job_id = ?", jobID).First(&state).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &state, nil
}
func (r *gormRepository) GetLatestEditorStateForVersion(ctx context.Context, sessionID, baseVersionID uuid.UUID) (*models.StudioEditorState, error) {
	var state models.StudioEditorState
	if err := r.db.WithContext(ctx).Where("session_id = ? AND base_version_id = ?", sessionID, baseVersionID).Order("created_at DESC").First(&state).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &state, nil
}

// DeleteSessionWorkspace removes the database-owned Studio workspace in FK-safe
// order and records every catalogued physical key as durable cleanup intent in
// the same transaction. No client-provided document or storage identifier is
// accepted.
func (r *gormRepository) DeleteSessionWorkspace(ctx context.Context, sessionID uuid.UUID, documentID uuid.UUID) ([]models.StudioStorageCleanupTask, error) {
	var keys []string
	var cleanupTasks []models.StudioStorageCleanupTask
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		repo := &gormRepository{db: tx}
		session, err := repo.LockSession(ctx, sessionID)
		if err != nil {
			return err
		}
		if session.DocumentID != documentID {
			return ErrUnauthorized
		}
		// A sibling session still owns this immutable document graph.
		var siblingCount int64
		if err := tx.Unscoped().Model(&models.StudioSession{}).Where("document_id = ? AND id <> ?", documentID, sessionID).Count(&siblingCount).Error; err != nil {
			return err
		}
		if siblingCount != 0 {
			return fmt.Errorf("%w: document has another session", ErrJobReconciliationFailed)
		}
		var active int64
		if err := tx.Model(&models.StudioJob{}).Where("(document_id = ? OR session_id = ?) AND (status NOT IN ? OR (status = 'succeeded' AND reconciled_at IS NULL))", documentID, sessionID, []string{"succeeded", "failed", "cancelled"}).Count(&active).Error; err != nil {
			return err
		}
		if active != 0 {
			return fmt.Errorf("%w: active or uncertain submission retains workspace", ErrJobReconciliationFailed)
		}
		var assets []models.StudioAsset
		if err := tx.Where("document_id = ?", documentID).Find(&assets).Error; err != nil {
			return err
		}
		for _, asset := range assets {
			if asset.R2Key != "" {
				keys = append(keys, asset.R2Key)
			}
		}

		var jobs []models.StudioJob
		if err := tx.Where("document_id = ? OR session_id = ?", documentID, sessionID).Find(&jobs).Error; err != nil {
			return err
		}
		for _, job := range jobs {
			if len(job.PendingObjectKeys) > 0 {
				var pending []string
				if err := json.Unmarshal(job.PendingObjectKeys, &pending); err != nil {
					return err
				}
				keys = append(keys, pending...)
			}
			if job.SourceKey != "" {
				keys = append(keys, job.SourceKey)
			}
			if job.PayloadKey != "" {
				keys = append(keys, job.PayloadKey)
			}
		}

		var exports []models.StudioExport
		if err := tx.Where("document_id = ?", documentID).Find(&exports).Error; err != nil {
			return err
		}
		for _, export := range exports {
			if export.R2Key != "" {
				keys = append(keys, export.R2Key)
			}
		}
		cleanupTasks, err = (&gormRepository{db: tx}).CreateStorageCleanupTasks(ctx, keys)
		if err != nil {
			return err
		}

		// The session's active-version FK is RESTRICT, so remove the session
		// before its document/version graph. Unscoped is intentional: discard
		// is a hard workspace deletion, not a recoverable edit.
		if err := tx.Unscoped().Where("id = ?", sessionID).Delete(&models.StudioSession{}).Error; err != nil {
			return err
		}
		if err := tx.Where("document_id = ? OR session_id = ?", documentID, sessionID).Delete(&models.StudioEditorState{}).Error; err != nil {
			return err
		}
		if err := tx.Where("document_id = ? OR session_id = ?", documentID, sessionID).Delete(&models.StudioJob{}).Error; err != nil {
			return err
		}
		if err := tx.Where("document_id = ?", documentID).Delete(&models.StudioExport{}).Error; err != nil {
			return err
		}

		var versions []models.StudioVersion
		if err := tx.Select("id").Where("document_id = ?", documentID).Find(&versions).Error; err != nil {
			return err
		}
		versionIDs := make([]uuid.UUID, 0, len(versions))
		for _, version := range versions {
			versionIDs = append(versionIDs, version.ID)
		}
		if len(versionIDs) > 0 {
			if err := tx.Where("version_id IN ?", versionIDs).Delete(&models.StudioSnapshot{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("document_id = ?", documentID).Delete(&models.StudioOperation{}).Error; err != nil {
			return err
		}
		if err := tx.Where("document_id = ?", documentID).Delete(&models.StudioVersion{}).Error; err != nil {
			return err
		}
		if err := tx.Where("document_id = ?", documentID).Delete(&models.StudioAsset{}).Error; err != nil {
			return err
		}
		if err := tx.Where("id = ?", documentID).Delete(&models.StudioDocument{}).Error; err != nil {
			return err
		}
		return nil
	})
	return cleanupTasks, err
}

// EnqueueExpiredStorageCleanup serializes reclamation with durable admission.
// Preparing, submitting and uncertain rows retain their entire graph indefinitely
// until explicit evidence establishes terminal state; elapsed time is not proof.
func (r *gormRepository) EnqueueExpiredStorageCleanup(ctx context.Context, now time.Time, limit int) error {
	if limit <= 0 || limit > storageCleanupBatchSize {
		limit = storageCleanupBatchSize
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		repo := &gormRepository{db: tx}

		// Exports can expire before their editing session does. Record their
		// opaque keys before removing the last catalogue reference.
		var exports []models.StudioExport
		if err := tx.Where("expires_at < ?", now).Order("expires_at ASC").Limit(limit).
			Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Find(&exports).Error; err != nil {
			return err
		}
		for _, export := range exports {
			if _, err := repo.CreateStorageCleanupTasks(ctx, []string{export.R2Key}); err != nil {
				return err
			}
			if err := tx.Delete(&export).Error; err != nil {
				return err
			}
		}
		// Mixed-version deployments may still have legacy submitters which
		// dispatch before registration. Operators must drain them before
		// explicitly enabling session reclamation on the new admission path.
		if os.Getenv("STUDIO_EXPIRED_SESSION_RECLAMATION_ENABLED") != "true" {
			return nil
		}
		var sessions []models.StudioSession
		if err := tx.Where("expires_at < ?", now).Order("expires_at ASC").Limit(limit).
			Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Find(&sessions).Error; err != nil {
			return err
		}
		for _, session := range sessions {
			// Export retention is independent. Access expiry must not shorten
			// an already promised deliverable's storage retention.
			var retainedExports int64
			if err := tx.Model(&models.StudioExport{}).Where("document_id = ? AND expires_at >= ?", session.DocumentID, now).Count(&retainedExports).Error; err != nil {
				return err
			}
			if retainedExports != 0 {
				continue
			}
			var protected int64
			if err := tx.Model(&models.StudioJob{}).Where("document_id = ? AND (status NOT IN ? OR (status = 'succeeded' AND reconciled_at IS NULL))", session.DocumentID, []string{"succeeded", "failed", "cancelled"}).Count(&protected).Error; err != nil {
				return err
			}
			if protected != 0 {
				continue
			}
			var siblings int64
			if err := tx.Unscoped().Model(&models.StudioSession{}).Where("document_id = ? AND id <> ?", session.DocumentID, session.ID).Count(&siblings).Error; err != nil {
				return err
			}
			if siblings != 0 {
				continue
			}
			if _, err := repo.DeleteSessionWorkspace(ctx, session.ID, session.DocumentID); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *gormRepository) CreateStorageCleanupTasks(ctx context.Context, keys []string) ([]models.StudioStorageCleanupTask, error) {
	uniqueKeys := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		uniqueKeys = append(uniqueKeys, key)
	}
	if len(uniqueKeys) == 0 {
		return nil, nil
	}
	now := time.Now().UTC()
	rows := make([]models.StudioStorageCleanupTask, 0, len(uniqueKeys))
	for _, key := range uniqueKeys {
		rows = append(rows, models.StudioStorageCleanupTask{ID: uuid.New(), ObjectKey: key, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now})
	}
	if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "object_key"}}, DoNothing: true}).Create(&rows).Error; err != nil {
		return nil, err
	}
	var persisted []models.StudioStorageCleanupTask
	if err := r.db.WithContext(ctx).Where("object_key IN ?", uniqueKeys).Find(&persisted).Error; err != nil {
		return nil, err
	}
	return persisted, nil
}

func (r *gormRepository) ClaimStorageCleanupTasks(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]models.StudioStorageCleanupTask, error) {
	if limit <= 0 {
		limit = 50
	}
	if lease <= 0 {
		lease = 5 * time.Minute
	}
	var tasks []models.StudioStorageCleanupTask
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("next_attempt_at <= ? AND (lease_expires_at IS NULL OR lease_expires_at < ?)", now, now).
			Order("next_attempt_at ASC").Limit(limit).Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Find(&tasks).Error; err != nil {
			return err
		}
		if len(tasks) == 0 {
			return nil
		}
		leaseUntil := now.Add(lease)
		return tx.Model(&models.StudioStorageCleanupTask{}).Where("id IN ?", taskIDs(tasks)).Updates(map[string]interface{}{"lease_expires_at": leaseUntil, "updated_at": now}).Error
	})
	return tasks, err
}

func taskIDs(tasks []models.StudioStorageCleanupTask) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.ID)
	}
	return ids
}

func (r *gormRepository) DeleteStorageCleanupTask(ctx context.Context, taskID uuid.UUID) error {
	return r.db.WithContext(ctx).Delete(&models.StudioStorageCleanupTask{}, "id = ?", taskID).Error
}

func (r *gormRepository) RescheduleStorageCleanupTask(ctx context.Context, taskID uuid.UUID, attempts int, nextAttemptAt time.Time, lastError string) error {
	if len(lastError) > 512 {
		lastError = lastError[:512]
	}
	return r.db.WithContext(ctx).Model(&models.StudioStorageCleanupTask{}).Where("id = ?", taskID).Updates(map[string]interface{}{
		"attempt_count":    attempts,
		"next_attempt_at":  nextAttemptAt,
		"lease_expires_at": nil,
		"last_error":       lastError,
		"updated_at":       time.Now().UTC(),
	}).Error
}

// StorageObjectReferenced prevents one retired owner from erasing an object
// still catalogued by another document/export/job. New writes use fresh opaque
// keys, so reclamation never transfers an old key to a new owner.
func (r *gormRepository) StorageObjectReferenced(ctx context.Context, key string) (bool, error) {
	for _, query := range []*gorm.DB{
		r.db.WithContext(ctx).Model(&models.StudioAsset{}).Where("r2_key = ?", key),
		r.db.WithContext(ctx).Model(&models.StudioExport{}).Where("r2_key = ?", key),
		r.db.WithContext(ctx).Model(&models.StudioJob{}).Where("source_key = ? OR payload_key = ? OR pending_object_keys @> ?::jsonb", key, key, string(mustJSONKey(key))),
		r.db.WithContext(ctx).Table("task_completions").Where("result_key = ? AND state <> ?", key, "deleted"),
		r.db.WithContext(ctx).Table("worker_result_ownerships").Where("state <> 'deleted' AND (source_keys::jsonb @> ?::jsonb OR result_key = ? OR metadata_key = ?)", string(mustJSONKey(key)), key, key),
	} {
		var count int64
		if err := query.Count(&count).Error; err != nil {
			return true, err
		}
		if count > 0 {
			return true, nil
		}
	}
	return false, nil
}

func mustJSONKey(key string) []byte { value, _ := json.Marshal([]string{key}); return value }

// Keys use transaction-scoped advisory locks, sorted to make batches deadlock
// safe. Every catalog publisher and retirement decision shares this protocol.
func (r *gormRepository) withActiveObjectKeys(ctx context.Context, keys []string, publish func(*gorm.DB) error) error {
	keys = append([]string(nil), keys...)
	sort.Strings(keys)
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for index, key := range keys {
			if key == "" || (index > 0 && keys[index-1] == key) {
				continue
			}
			if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 81807))", key).Error; err != nil {
				return err
			}
			var retired int64
			if err := tx.Model(&models.StudioStorageObjectRetirement{}).Where("object_key = ?", key).Count(&retired).Error; err != nil {
				return err
			}
			if retired != 0 {
				return fmt.Errorf("%w: storage key is retired", ErrConflict)
			}
		}
		return publish(tx)
	})
}

func (r *gormRepository) saveJobWithObjectOwnership(ctx context.Context, job *models.StudioJob, update bool) error {
	keys := []string{job.SourceKey, job.PayloadKey}
	if len(job.PendingObjectKeys) > 0 {
		var pending []string
		if err := json.Unmarshal(job.PendingObjectKeys, &pending); err != nil {
			return err
		}
		keys = append(keys, pending...)
	}
	return r.withActiveObjectKeys(ctx, keys, func(tx *gorm.DB) error {
		if update {
			return tx.Save(job).Error
		}
		return tx.Create(job).Error
	})
}

// RetireStorageObject commits an irreversible key tombstone before storage I/O.
// A lost deletion acknowledgement leaves both retry intent and the tombstone;
// no process restart can admit a new owner of that retired key.
func (r *gormRepository) RetireStorageObject(ctx context.Context, key string) (bool, error) {
	retired := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 81807))", key).Error; err != nil {
			return err
		}
		referenced, err := (&gormRepository{db: tx}).StorageObjectReferenced(ctx, key)
		if err != nil {
			return err
		}
		if referenced {
			return nil
		}
		row := models.StudioStorageObjectRetirement{ObjectKey: key, CreatedAt: time.Now().UTC()}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
		retired = true
		return nil
	})
	return retired, err
}

// ReleasePreparedObject moves one definitely unpublished reconciliation object
// out of its planned-key list and into durable deletion intent atomically.
func (r *gormRepository) ReleasePreparedObject(ctx context.Context, key string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var jobs []models.StudioJob
		if err := tx.Where("pending_object_keys @> ?::jsonb", string(mustJSONKey(key))).Clauses(clause.Locking{Strength: "UPDATE"}).Find(&jobs).Error; err != nil {
			return err
		}
		for i := range jobs {
			var keys []string
			if err := json.Unmarshal(jobs[i].PendingObjectKeys, &keys); err != nil {
				return err
			}
			keep := make([]string, 0, len(keys))
			for _, candidate := range keys {
				if candidate != key {
					keep = append(keep, candidate)
				}
			}
			data, err := json.Marshal(keep)
			if err != nil {
				return err
			}
			jobs[i].PendingObjectKeys = data
			if err := (&gormRepository{db: tx}).SaveJob(ctx, &jobs[i]); err != nil {
				return err
			}
		}
		_, err := (&gormRepository{db: tx}).CreateStorageCleanupTasks(ctx, []string{key})
		return err
	})
}
