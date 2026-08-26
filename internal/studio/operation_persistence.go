package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/studio/models"
	"pdfnest-backend/internal/studio/vdm"
)

type operationMutation struct {
	BaseVersionID  uuid.UUID
	IdempotencyKey string
	OperationName  string
	Parameters     json.RawMessage
	TargetPageIDs  []string
	IsMaterialized bool
}

type operationDeriver func(base *vdm.DocumentModel) (*vdm.DocumentModel, error)

// persistOperation is the single atomic version/operation persistence boundary.
// The legacy arbitrary-VDM endpoint and the typed command coordinator share this
// transaction, while only the coordinator derives its VDM from the locked base.
func persistOperation(
	ctx context.Context,
	repo Repository,
	sessionID uuid.UUID,
	ident identity.Identity,
	mutation operationMutation,
	derive operationDeriver,
) (*ApplyOperationResult, error) {
	if mutation.BaseVersionID == uuid.Nil || mutation.IdempotencyKey == "" || mutation.OperationName == "" {
		return nil, ErrInvalidOperation
	}

	canonicalParameters, err := canonicalJSON(mutation.Parameters)
	if err != nil {
		return nil, ErrInvalidOperation
	}

	var result ApplyOperationResult
	err = repo.WithTransaction(ctx, func(txRepo Repository, _ *gorm.DB) error {
		sess, err := txRepo.LockSession(ctx, sessionID)
		if err != nil {
			return err
		}
		if err := validateSessionAccess(sess, ident); err != nil {
			return err
		}

		existingOp, existingVer, err := txRepo.FindOperationByIdempotencyKey(ctx, sess.DocumentID, mutation.IdempotencyKey)
		if err != nil {
			return err
		}
		if existingOp != nil || existingVer != nil {
			if existingOp == nil || existingVer == nil || !sameOperationEnvelope(existingOp, existingVer, mutation, canonicalParameters) {
				return ErrIdempotencyConflict
			}
			result = ApplyOperationResult{Version: existingVer, Operation: existingOp, IsIdempotentReplay: true}
			return nil
		}

		if sess.ActiveVersionID != mutation.BaseVersionID {
			return ErrInvalidBaseVersion
		}

		baseVer, err := txRepo.GetVersion(ctx, mutation.BaseVersionID)
		if err != nil {
			return err
		}
		if baseVer.DocumentID != sess.DocumentID {
			return ErrInvalidBaseVersion
		}
		baseModel, err := vdm.FromJSON(baseVer.VirtualModel)
		if err != nil {
			return err
		}
		derivedModel, err := derive(baseModel)
		if err != nil {
			return err
		}
		if derivedModel == nil || derivedModel.DocumentID != baseModel.DocumentID {
			return ErrInvalidOperation
		}

		newVerID := uuid.New()
		derivedModel.VersionID = newVerID.String()
		newVDMBytes, err := derivedModel.ToJSON()
		if err != nil {
			return err
		}

		now := time.Now().UTC()
		newVer := &models.StudioVersion{
			ID:              newVerID,
			DocumentID:      sess.DocumentID,
			ParentVersionID: &mutation.BaseVersionID,
			VersionNumber:   baseVer.VersionNumber + 1,
			Status:          "ready",
			OperationType:   mutation.OperationName,
			VirtualModel:    models.JSON(newVDMBytes),
			IsMaterialized:  mutation.IsMaterialized,
			CreatedAt:       now,
		}

		var targetPagesJSON models.JSON
		if len(mutation.TargetPageIDs) > 0 {
			targetPagesJSON, err = json.Marshal(mutation.TargetPageIDs)
			if err != nil {
				return err
			}
		}
		op := &models.StudioOperation{
			ID:             uuid.New(),
			DocumentID:     sess.DocumentID,
			VersionID:      newVerID,
			IdempotencyKey: mutation.IdempotencyKey,
			OperationName:  mutation.OperationName,
			Parameters:     models.JSON(canonicalParameters),
			TargetPageIDs:  targetPagesJSON,
			CreatedAt:      now,
		}

		if err := txRepo.CreateVersionAndOperation(ctx, newVer, op, sessionID, &mutation.BaseVersionID); err != nil {
			return err
		}
		result = ApplyOperationResult{Version: newVer, Operation: op}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func canonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var value interface{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, ErrInvalidOperation
	}
	return json.Marshal(value)
}

func sameOperationEnvelope(op *models.StudioOperation, ver *models.StudioVersion, mutation operationMutation, canonicalParameters []byte) bool {
	if op.OperationName != mutation.OperationName || ver.IsMaterialized != mutation.IsMaterialized || ver.ParentVersionID == nil || *ver.ParentVersionID != mutation.BaseVersionID {
		return false
	}
	existingParameters, err := canonicalJSON(json.RawMessage(op.Parameters))
	if err != nil || !bytes.Equal(existingParameters, canonicalParameters) {
		return false
	}
	var existingTargets []string
	if len(op.TargetPageIDs) > 0 {
		if err := json.Unmarshal(op.TargetPageIDs, &existingTargets); err != nil {
			return false
		}
	}
	return stringSlicesEqual(existingTargets, mutation.TargetPageIDs)
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
