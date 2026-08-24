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
		return nil, err
	}
	return &export, nil
}

func (r *gormRepository) TouchSession(ctx context.Context, sessID uuid.UUID) error {
	return r.db.WithContext(ctx).Model(&models.StudioSession{}).
		Where("id = ?", sessID).
		Update("last_accessed_at", time.Now().UTC()).Error
}
