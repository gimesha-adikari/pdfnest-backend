package studio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/studio/models"
	"pdfnest-backend/internal/studio/vdm"
)

// ApplyOperationRequest encapsulates mutation parameters for dispatching a Studio operation.
type ApplyOperationRequest struct {
	BaseVersionID   uuid.UUID         `json:"base_version_id"`
	IdempotencyKey  string            `json:"idempotency_key"`
	OperationName   string            `json:"operation_name"`
	Parameters      json.RawMessage   `json:"parameters"`
	TargetPageIDs   []string          `json:"target_page_ids,omitempty"`
	NewVirtualModel vdm.DocumentModel `json:"new_virtual_model"`
	IsMaterialized  bool              `json:"is_materialized"`
}

// ApplyOperationResult holds the resulting version, operation, and idempotency status.
type ApplyOperationResult struct {
	Version            *models.StudioVersion   `json:"version"`
	Operation          *models.StudioOperation `json:"operation"`
	IsIdempotentReplay bool                    `json:"is_idempotent_replay"`
}

// Service defines domain orchestration methods for Studio V2.
type Service interface {
	CreateDocument(ctx context.Context, ident identity.Identity, fileName string, fileSize int64, initialPageCount int, sourceAssetID string, sourceR2Key string, initialVDM vdm.DocumentModel) (*models.StudioDocument, *models.StudioSession, *models.StudioVersion, error)
	GetSession(ctx context.Context, sessionID uuid.UUID, ident identity.Identity) (*models.StudioSession, *models.StudioDocument, *models.StudioVersion, error)
	ApplyOperation(ctx context.Context, sessionID uuid.UUID, ident identity.Identity, req ApplyOperationRequest) (*ApplyOperationResult, error)
	Undo(ctx context.Context, sessionID uuid.UUID, ident identity.Identity) (*models.StudioVersion, error)
	Redo(ctx context.Context, sessionID uuid.UUID, ident identity.Identity) (*models.StudioVersion, error)
	CheckoutVersion(ctx context.Context, sessionID uuid.UUID, ident identity.Identity, targetVersionID uuid.UUID) (*models.StudioVersion, error)
	GetVersionHistory(ctx context.Context, sessionID uuid.UUID, ident identity.Identity) ([]models.StudioVersion, []models.StudioOperation, error)
	RegisterSnapshot(ctx context.Context, docID uuid.UUID, versionID uuid.UUID, assetID string, r2Key string, byteSize int64, pageCount int) (*models.StudioSnapshot, error)
	RegisterAsset(ctx context.Context, docID uuid.UUID, assetID string, assetType string, r2Key string, byteSize int64, mimeType string) (*models.StudioAsset, error)
	CreateExport(ctx context.Context, sessionID uuid.UUID, ident identity.Identity, exportFormat string, r2Key string, byteSize int64, expiresAt time.Time) (*models.StudioExport, error)
}

type studioService struct {
	repo Repository
}

// NewService initializes a Studio V2 domain service.
func NewService(repo Repository) Service {
	return &studioService{repo: repo}
}

// HashGuestToken computes a SHA-256 digest for guest identity tokens.
func HashGuestToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func (s *studioService) validateSessionAccess(sess *models.StudioSession, ident identity.Identity) error {
	now := time.Now().UTC()
	if now.After(sess.ExpiresAt) {
		return ErrSessionExpired
	}

	if ident.IsUser() {
		userUUID, err := uuid.Parse(ident.ID)
		if err != nil || sess.UserID == nil || *sess.UserID != userUUID {
			return ErrUnauthorized
		}
		return nil
	}

	// Guest identity check
	guestHash := HashGuestToken(ident.ID)
	if sess.GuestTokenHash != guestHash {
		return ErrUnauthorized
	}
	return nil
}

func (s *studioService) CreateDocument(
	ctx context.Context,
	ident identity.Identity,
	fileName string,
	fileSize int64,
	initialPageCount int,
	sourceAssetID string,
	sourceR2Key string,
	initialVDM vdm.DocumentModel,
) (*models.StudioDocument, *models.StudioSession, *models.StudioVersion, error) {
	if err := initialVDM.Validate(); err != nil {
		return nil, nil, nil, err
	}

	vdmBytes, err := initialVDM.ToJSON()
	if err != nil {
		return nil, nil, nil, err
	}

	docID := uuid.New()
	verID := uuid.New()
	sessID := uuid.New()
	now := time.Now().UTC()

	doc := &models.StudioDocument{
		ID:               docID,
		OriginalFileName: fileName,
		FileSize:         fileSize,
		InitialPageCount: initialPageCount,
		CreatedAt:        now,
	}

	var asset *models.StudioAsset
	if sourceAssetID != "" && sourceR2Key != "" {
		asset = &models.StudioAsset{
			ID:         sourceAssetID,
			DocumentID: docID,
			AssetType:  "source_pdf",
			R2Key:      sourceR2Key,
			ByteSize:   fileSize,
			MimeType:   "application/pdf",
			CreatedAt:  now,
		}
	}

	ver := &models.StudioVersion{
		ID:             verID,
		DocumentID:     docID,
		VersionNumber:  0,
		Status:         "ready",
		OperationType:  "initial_upload",
		VirtualModel:   models.JSON(vdmBytes),
		IsMaterialized: true,
		CreatedAt:      now,
	}

	var userID *uuid.UUID
	var guestHash string
	if ident.IsUser() {
		u, err := uuid.Parse(ident.ID)
		if err == nil {
			userID = &u
		}
	} else {
		guestHash = HashGuestToken(ident.ID)
	}

	sess := &models.StudioSession{
		ID:              sessID,
		UserID:          userID,
		GuestTokenHash:  guestHash,
		DocumentID:      docID,
		ActiveVersionID: verID,
		CreatedAt:       now,
		LastAccessedAt:  now,
		ExpiresAt:       now.Add(24 * time.Hour),
	}

	if err := s.repo.CreateDocumentWithInitialVersion(ctx, doc, asset, ver, sess); err != nil {
		return nil, nil, nil, err
	}

	return doc, sess, ver, nil
}

func (s *studioService) GetSession(ctx context.Context, sessionID uuid.UUID, ident identity.Identity) (*models.StudioSession, *models.StudioDocument, *models.StudioVersion, error) {
	sess, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, nil, nil, err
	}

	if err := s.validateSessionAccess(sess, ident); err != nil {
		return nil, nil, nil, err
	}

	doc, err := s.repo.GetDocument(ctx, sess.DocumentID)
	if err != nil {
		return nil, nil, nil, err
	}

	ver, err := s.repo.GetVersion(ctx, sess.ActiveVersionID)
	if err != nil {
		return nil, nil, nil, err
	}

	_ = s.repo.TouchSession(ctx, sess.ID)
	return sess, doc, ver, nil
}

func (s *studioService) ApplyOperation(
	ctx context.Context,
	sessionID uuid.UUID,
	ident identity.Identity,
	req ApplyOperationRequest,
) (*ApplyOperationResult, error) {
	if req.IdempotencyKey == "" || req.OperationName == "" {
		return nil, ErrInvalidOperation
	}

	if err := req.NewVirtualModel.Validate(); err != nil {
		return nil, err
	}

	newVDMBytes, err := req.NewVirtualModel.ToJSON()
	if err != nil {
		return nil, err
	}

	var result ApplyOperationResult

	err = s.repo.WithTransaction(ctx, func(txRepo Repository, tx *gorm.DB) error {
		// 1. Acquire Session Row Lock (Serializes concurrent requests on same session)
		sess, err := txRepo.LockSession(ctx, sessionID)
		if err != nil {
			return err
		}

		// Validate ownership & expiration under lock
		if err := s.validateSessionAccess(sess, ident); err != nil {
			return err
		}

		// 2. Idempotency Lookup (Fast return for retried requests)
		existingOp, existingVer, err := txRepo.FindOperationByIdempotencyKey(ctx, sess.DocumentID, req.IdempotencyKey)
		if err != nil {
			return err
		}
		if existingOp != nil && existingVer != nil {
			result = ApplyOperationResult{
				Version:            existingVer,
				Operation:          existingOp,
				IsIdempotentReplay: true,
			}
			return nil
		}

		// 3. Base Version Validation (Optimistic Concurrency Control)
		if sess.ActiveVersionID != req.BaseVersionID {
			return ErrInvalidBaseVersion
		}

		baseVer, err := txRepo.GetVersion(ctx, req.BaseVersionID)
		if err != nil {
			return err
		}

		// 4. Create StudioVersion Record
		newVerID := uuid.New()
		now := time.Now().UTC()
		nextVersionNum := baseVer.VersionNumber + 1

		newVer := &models.StudioVersion{
			ID:              newVerID,
			DocumentID:      sess.DocumentID,
			ParentVersionID: &req.BaseVersionID,
			VersionNumber:   nextVersionNum,
			Status:          "ready",
			OperationType:   req.OperationName,
			VirtualModel:    models.JSON(newVDMBytes),
			IsMaterialized:  req.IsMaterialized,
			CreatedAt:       now,
		}

		var targetPagesJSON models.JSON
		if len(req.TargetPageIDs) > 0 {
			tpBytes, _ := json.Marshal(req.TargetPageIDs)
			targetPagesJSON = models.JSON(tpBytes)
		}

		// 5. Create StudioOperation Record
		opID := uuid.New()
		op := &models.StudioOperation{
			ID:             opID,
			DocumentID:     sess.DocumentID,
			VersionID:      newVerID,
			IdempotencyKey: req.IdempotencyKey,
			OperationName:  req.OperationName,
			Parameters:     models.JSON(req.Parameters),
			TargetPageIDs:  targetPagesJSON,
			CreatedAt:      now,
		}

		// 6. Atomically persist version, operation, update active pointer & parent preferred child
		if err := txRepo.CreateVersionAndOperation(ctx, newVer, op, sessionID, &req.BaseVersionID); err != nil {
			return err
		}

		result = ApplyOperationResult{
			Version:            newVer,
			Operation:          op,
			IsIdempotentReplay: false,
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *studioService) Undo(ctx context.Context, sessionID uuid.UUID, ident identity.Identity) (*models.StudioVersion, error) {
	var parentVer *models.StudioVersion

	err := s.repo.WithTransaction(ctx, func(txRepo Repository, tx *gorm.DB) error {
		sess, err := txRepo.LockSession(ctx, sessionID)
		if err != nil {
			return err
		}

		if err := s.validateSessionAccess(sess, ident); err != nil {
			return err
		}

		activeVer, err := txRepo.GetVersion(ctx, sess.ActiveVersionID)
		if err != nil {
			return err
		}

		if activeVer.ParentVersionID == nil || *activeVer.ParentVersionID == uuid.Nil {
			return ErrNoParentVersion
		}

		parent, err := txRepo.GetVersion(ctx, *activeVer.ParentVersionID)
		if err != nil {
			return err
		}

		if err := txRepo.UpdateSessionActiveVersion(ctx, sessionID, parent.ID); err != nil {
			return err
		}

		parentVer = parent
		return nil
	})

	if err != nil {
		return nil, err
	}
	return parentVer, nil
}

func (s *studioService) Redo(ctx context.Context, sessionID uuid.UUID, ident identity.Identity) (*models.StudioVersion, error) {
	var childVer *models.StudioVersion

	err := s.repo.WithTransaction(ctx, func(txRepo Repository, tx *gorm.DB) error {
		sess, err := txRepo.LockSession(ctx, sessionID)
		if err != nil {
			return err
		}

		if err := s.validateSessionAccess(sess, ident); err != nil {
			return err
		}

		activeVer, err := txRepo.GetVersion(ctx, sess.ActiveVersionID)
		if err != nil {
			return err
		}

		if activeVer.PreferredChildID == nil || *activeVer.PreferredChildID == uuid.Nil {
			return ErrNoRedoChild
		}

		child, err := txRepo.GetVersion(ctx, *activeVer.PreferredChildID)
		if err != nil {
			return err
		}

		if child.Status != "ready" {
			return ErrNoRedoChild
		}

		if err := txRepo.UpdateSessionActiveVersion(ctx, sessionID, child.ID); err != nil {
			return err
		}

		childVer = child
		return nil
	})

	if err != nil {
		return nil, err
	}
	return childVer, nil
}

func (s *studioService) CheckoutVersion(ctx context.Context, sessionID uuid.UUID, ident identity.Identity, targetVersionID uuid.UUID) (*models.StudioVersion, error) {
	var targetVer *models.StudioVersion

	err := s.repo.WithTransaction(ctx, func(txRepo Repository, tx *gorm.DB) error {
		sess, err := txRepo.LockSession(ctx, sessionID)
		if err != nil {
			return err
		}

		if err := s.validateSessionAccess(sess, ident); err != nil {
			return err
		}

		target, err := txRepo.GetVersion(ctx, targetVersionID)
		if err != nil {
			return err
		}

		if target.DocumentID != sess.DocumentID {
			return ErrInvalidBranchTarget
		}

		// Update parent's preferred child pointer to this checkout branch
		if target.ParentVersionID != nil && *target.ParentVersionID != uuid.Nil {
			_ = txRepo.UpdateVersionPreferredChild(ctx, *target.ParentVersionID, target.ID)
		}

		if err := txRepo.UpdateSessionActiveVersion(ctx, sessionID, target.ID); err != nil {
			return err
		}

		targetVer = target
		return nil
	})

	if err != nil {
		return nil, err
	}
	return targetVer, nil
}

func (s *studioService) GetVersionHistory(ctx context.Context, sessionID uuid.UUID, ident identity.Identity) ([]models.StudioVersion, []models.StudioOperation, error) {
	sess, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, nil, err
	}

	if err := s.validateSessionAccess(sess, ident); err != nil {
		return nil, nil, err
	}

	return s.repo.GetVersionHistory(ctx, sess.DocumentID)
}

func (s *studioService) RegisterSnapshot(
	ctx context.Context,
	docID uuid.UUID,
	versionID uuid.UUID,
	assetID string,
	r2Key string,
	byteSize int64,
	pageCount int,
) (*models.StudioSnapshot, error) {
	now := time.Now().UTC()
	snapID := uuid.New()

	snap := &models.StudioSnapshot{
		ID:        snapID,
		VersionID: versionID,
		AssetID:   assetID,
		PageCount: pageCount,
		CreatedAt: now,
	}

	asset := &models.StudioAsset{
		ID:         assetID,
		DocumentID: docID,
		AssetType:  "snapshot",
		R2Key:      r2Key,
		ByteSize:   byteSize,
		MimeType:   "application/pdf",
		CreatedAt:  now,
	}

	err := s.repo.WithTransaction(ctx, func(txRepo Repository, tx *gorm.DB) error {
		if err := txRepo.CreateAsset(ctx, asset); err != nil {
			return err
		}
		if err := txRepo.CreateSnapshot(ctx, snap); err != nil {
			return err
		}
		return tx.Model(&models.StudioVersion{}).
			Where("id = ?", versionID).
			Update("snapshot_id", snapID).Error
	})

	if err != nil {
		return nil, err
	}
	return snap, nil
}

func (s *studioService) RegisterAsset(
	ctx context.Context,
	docID uuid.UUID,
	assetID string,
	assetType string,
	r2Key string,
	byteSize int64,
	mimeType string,
) (*models.StudioAsset, error) {
	asset := &models.StudioAsset{
		ID:         assetID,
		DocumentID: docID,
		AssetType:  assetType,
		R2Key:      r2Key,
		ByteSize:   byteSize,
		MimeType:   mimeType,
		CreatedAt:  time.Now().UTC(),
	}

	if err := s.repo.CreateAsset(ctx, asset); err != nil {
		return nil, err
	}
	return asset, nil
}

func (s *studioService) CreateExport(
	ctx context.Context,
	sessionID uuid.UUID,
	ident identity.Identity,
	exportFormat string,
	r2Key string,
	byteSize int64,
	expiresAt time.Time,
) (*models.StudioExport, error) {
	sess, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	if err := s.validateSessionAccess(sess, ident); err != nil {
		return nil, err
	}

	export := &models.StudioExport{
		ID:           uuid.New(),
		DocumentID:   sess.DocumentID,
		VersionID:    sess.ActiveVersionID,
		ExportFormat: exportFormat,
		R2Key:        r2Key,
		ByteSize:     byteSize,
		ExpiresAt:    expiresAt,
		CreatedAt:    time.Now().UTC(),
	}

	if err := s.repo.CreateExport(ctx, export); err != nil {
		return nil, err
	}
	return export, nil
}
