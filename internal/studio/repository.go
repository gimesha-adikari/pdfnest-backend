package studio

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"pdfnest-backend/internal/studio/models"
)

// Repository defines data access interfaces for Studio V2 persistence.
type Repository interface {
	WithTransaction(ctx context.Context, fn func(txRepo Repository, tx *gorm.DB) error) error
	CreateDocumentWithInitialVersion(ctx context.Context, doc *models.StudioDocument, asset *models.StudioAsset, ver *models.StudioVersion, sess *models.StudioSession) error
	GetSession(ctx context.Context, sessionID uuid.UUID) (*models.StudioSession, error)
	GetDocument(ctx context.Context, docID uuid.UUID) (*models.StudioDocument, error)
	GetVersion(ctx context.Context, verID uuid.UUID) (*models.StudioVersion, error)
	LockSession(ctx context.Context, sessionID uuid.UUID) (*models.StudioSession, error)
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
	FindJobByIdempotencyKey(ctx context.Context, docID uuid.UUID, key string) (*models.StudioJob, error)
	SaveJob(ctx context.Context, job *models.StudioJob) error
	CreateEditorState(ctx context.Context, state *models.StudioEditorState) error
	GetEditorState(ctx context.Context, id uuid.UUID) (*models.StudioEditorState, error)
	GetEditorStateByExtractJob(ctx context.Context, jobID uuid.UUID) (*models.StudioEditorState, error)
	DeleteSessionWorkspace(ctx context.Context, sessionID uuid.UUID, documentID uuid.UUID) ([]string, error)
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
			if err := tx.Create(asset).Error; err != nil {
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
		Raw("SELECT id, user_id, guest_token_hash, document_id, active_version_id, created_at, last_accessed_at, expires_at FROM studio_sessions WHERE id = ? FOR UPDATE", sessionID).
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
	return r.db.WithContext(ctx).Create(asset).Error
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
	return r.db.WithContext(ctx).Create(export).Error
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
	return r.db.WithContext(ctx).Create(job).Error
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
	return r.db.WithContext(ctx).Save(job).Error
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

// DeleteSessionWorkspace removes the database-owned Studio workspace in FK-safe
// order. Storage keys are returned for best-effort object cleanup after commit;
// no client-provided document or storage identifier is accepted.
func (r *gormRepository) DeleteSessionWorkspace(ctx context.Context, sessionID uuid.UUID, documentID uuid.UUID) ([]string, error) {
	var keys []string
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
	return keys, err
}
