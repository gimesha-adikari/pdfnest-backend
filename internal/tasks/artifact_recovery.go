package tasks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/models"
	"pdfnest-backend/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	recoveryActionLease       = 5 * time.Minute
	recoveryActionMaxAttempts = 5
)

var (
	ErrRecoveryCaseNotFound        = errors.New("artifact recovery case not found")
	ErrRecoveryCaseNotHeld         = errors.New("artifact recovery case is not held")
	ErrRecoveryIdempotencyConflict = errors.New("artifact recovery idempotency key conflicts")
	ErrRecoveryActionInProgress    = errors.New("artifact recovery action is already in progress")
	ErrRecoveryAttemptsExhausted   = errors.New("artifact recovery action retry limit reached")
	ErrRecoveryInvalidRequest      = errors.New("invalid artifact recovery request")
)

type ArtifactRecoverySummary struct {
	TaskID        string                          `json:"task_id"`
	State         string                          `json:"state"`
	ExpiresAt     time.Time                       `json:"expires_at"`
	ArtifactBytes int64                           `json:"artifact_bytes"`
	IdentityValid bool                            `json:"identity_valid"`
	CaseStatus    string                          `json:"case_status"`
	Actions       []ArtifactRecoveryActionSummary `json:"actions"`
}

type ArtifactRecoveryActionSummary struct {
	Action    string    `json:"action"`
	State     string    `json:"state"`
	Reason    string    `json:"reason"`
	AdminID   string    `json:"admin_id"`
	CreatedAt time.Time `json:"created_at"`
}

type ArtifactRecoveryOutcome struct {
	State            string `json:"state"`
	IdempotentReplay bool   `json:"idempotent_replay"`
}

type recoverySource struct {
	key  string
	path string
	free func()
}

func ListArtifactRecoveryHolds(ctx context.Context, limit int, before time.Time) ([]ArtifactRecoverySummary, error) {
	if err := config.ValidateArtifactRecoverySchema(config.DB); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		return nil, ErrRecoveryInvalidRequest
	}
	query := config.DB.WithContext(ctx).Where("state = ?", "artifact_recovery_hold").Order("updated_at DESC, id ASC").Limit(limit)
	if !before.IsZero() {
		query = query.Where("updated_at < ?", before)
	}
	var rows []models.TaskCompletion
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	return summarizeRecoveryHolds(ctx, rows)
}

func GetArtifactRecoveryHold(ctx context.Context, taskID string) (*ArtifactRecoverySummary, error) {
	if err := config.ValidateArtifactRecoverySchema(config.DB); err != nil {
		return nil, err
	}
	var row models.TaskCompletion
	if err := config.DB.WithContext(ctx).Where("id = ? AND state = ?", taskID, "artifact_recovery_hold").First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRecoveryCaseNotFound
		}
		return nil, err
	}
	items, err := summarizeRecoveryHolds(ctx, []models.TaskCompletion{row})
	if err != nil {
		return nil, err
	}
	return &items[0], nil
}

func summarizeRecoveryHolds(ctx context.Context, rows []models.TaskCompletion) ([]ArtifactRecoverySummary, error) {
	result := make([]ArtifactRecoverySummary, 0, len(rows))
	if len(rows) == 0 {
		return result, nil
	}
	ids := make([]string, 0, len(rows))
	byID := make(map[string]int, len(rows))
	for _, row := range rows {
		byID[row.ID] = len(result)
		ids = append(ids, row.ID)
		caseStatus := "open"
		result = append(result, ArtifactRecoverySummary{
			TaskID: row.ID, State: row.State, ExpiresAt: row.ExpiresAt,
			ArtifactBytes: row.ArtifactBytes, IdentityValid: validTaskArtifactKey(row.ID, row.ResultKey),
			CaseStatus: caseStatus, Actions: []ArtifactRecoveryActionSummary{},
		})
	}
	var actions []models.ArtifactRecoveryAction
	if err := config.DB.WithContext(ctx).Where("task_id IN ?", ids).Order("created_at ASC, id ASC").Find(&actions).Error; err != nil {
		return nil, err
	}
	for _, action := range actions {
		index, ok := byID[action.TaskID]
		if !ok {
			continue
		}
		result[index].Actions = append(result[index].Actions, ArtifactRecoveryActionSummary{
			Action: action.Action, State: action.State, Reason: action.Reason,
			AdminID: action.AdminUserID, CreatedAt: action.CreatedAt,
		})
		if action.State == "case_closed_non_destructive" {
			result[index].CaseStatus = "closed_non_destructive"
		}
	}
	return result, nil
}

func ExecuteArtifactRecoveryAction(ctx context.Context, taskID, adminID, operationKey, action, reason string) (*ArtifactRecoveryOutcome, error) {
	if err := config.ValidateArtifactRecoverySchema(config.DB); err != nil {
		return nil, err
	}
	operationKey = strings.TrimSpace(operationKey)
	reason = strings.TrimSpace(reason)
	action = strings.TrimSpace(action)
	if len(taskID) == 0 || len(taskID) > 128 || len(adminID) == 0 || len(operationKey) == 0 || len(operationKey) > 128 || len(reason) < 8 || len(reason) > 500 {
		return nil, ErrRecoveryInvalidRequest
	}
	if _, err := uuid.Parse(adminID); err != nil {
		return nil, ErrRecoveryInvalidRequest
	}
	if action != "verify" && action != "recover" && action != "close" {
		return nil, ErrRecoveryInvalidRequest
	}
	fingerprint := recoveryRequestFingerprint(taskID, adminID, action, reason)
	now := time.Now().UTC()
	var record models.ArtifactRecoveryAction
	var snapshot models.TaskCompletion
	replay := false
	err := config.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", taskID).First(&snapshot).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRecoveryCaseNotFound
			}
			return err
		}
		if snapshot.State != "artifact_recovery_hold" {
			return ErrRecoveryCaseNotHeld
		}
		claim := models.ArtifactRecoveryAction{
			ID: uuid.NewString(), OperationKey: operationKey, RequestFingerprint: fingerprint,
			TaskID: taskID, AdminUserID: adminID, Action: action, State: "processing", Reason: reason,
			OwnerIdentity: snapshot.OwnerIdentity, OriginalResultKey: snapshot.ResultKey,
			ArtifactSHA256: snapshot.ArtifactSHA256, ArtifactBytes: snapshot.ArtifactBytes,
			OriginalExpiresAt: snapshot.ExpiresAt, Attempts: 1,
		}
		if action == "recover" {
			claim.RecoveryKey = artifactRecoveryObjectKey(taskID, claim.ID)
		}
		lease := now.Add(recoveryActionLease)
		claim.LeaseExpiresAt = &lease
		created := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "operation_key"}}, DoNothing: true}).Create(&claim)
		if created.Error != nil {
			return created.Error
		}
		if created.RowsAffected == 1 {
			record = claim
		} else {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("operation_key = ?", operationKey).First(&record).Error; err != nil {
				return err
			}
			if record.RequestFingerprint != fingerprint || record.TaskID != taskID || record.AdminUserID != adminID {
				return ErrRecoveryIdempotencyConflict
			}
			if action == "recover" && record.RecoveryKey != artifactRecoveryObjectKey(taskID, record.ID) {
				return errors.New("artifact recovery ledger identity mismatch")
			}
			if record.State != "processing" {
				replay = true
				return nil
			}
			if record.LeaseExpiresAt != nil && record.LeaseExpiresAt.After(now) {
				return ErrRecoveryActionInProgress
			}
			if record.Attempts >= recoveryActionMaxAttempts {
				return ErrRecoveryAttemptsExhausted
			}
			var other int64
			if err := tx.Model(&models.ArtifactRecoveryAction{}).Where("task_id = ? AND operation_key <> ? AND state = ? AND lease_expires_at > ?", taskID, operationKey, "processing", now).Count(&other).Error; err != nil {
				return err
			}
			if other != 0 {
				return ErrRecoveryActionInProgress
			}
			record.Attempts++
			record.LeaseExpiresAt = &lease
			if err := tx.Model(&record).Updates(map[string]any{"attempts": record.Attempts, "lease_expires_at": lease, "updated_at": now}).Error; err != nil {
				return err
			}
		}
		if created.RowsAffected == 1 {
			var other int64
			if err := tx.Model(&models.ArtifactRecoveryAction{}).Where("task_id = ? AND operation_key <> ? AND state = ? AND lease_expires_at > ?", taskID, operationKey, "processing", now).Count(&other).Error; err != nil {
				return err
			}
			if other != 0 {
				return ErrRecoveryActionInProgress
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if replay {
		return &ArtifactRecoveryOutcome{State: record.State, IdempotentReplay: true}, nil
	}
	if action == "close" {
		return finishRecoveryAction(ctx, record, snapshot, "case_closed_non_destructive", "", "")
	}
	if action == "recover" && strings.TrimSpace(os.Getenv("ARTIFACT_RECOVERY_COPY_ENABLED")) != "true" {
		return finishRecoveryAction(ctx, record, snapshot, "recovery_blocked_by_policy", "", "retention_policy_not_enabled", record.RecoveryKey)
	}

	source, evidenceState, errorCode := inspectRecoverySource(ctx, &snapshot)
	if errorCode != "" {
		return finishRecoveryAction(ctx, record, snapshot, evidenceState, "", errorCode)
	}
	defer source.free()
	if action == "verify" {
		return finishRecoveryAction(ctx, record, snapshot, "verified_owner_source", source.key, "")
	}

	recoveryKey := artifactRecoveryObjectKey(snapshot.ID, record.ID)
	if err := storage.SaveVerifiedArtifact(ctx, recoveryKey, source.path, snapshot.ArtifactSHA256, snapshot.ArtifactBytes, "text/markdown; charset=utf-8"); err != nil {
		return finishRecoveryAction(ctx, record, snapshot, "recovery_deferred", source.key, "storage_write_unavailable", recoveryKey)
	}
	if err := storage.VerifyArtifact(ctx, recoveryKey, snapshot.ArtifactSHA256, snapshot.ArtifactBytes); err != nil {
		state, code := "recovery_deferred", "storage_verification_unavailable"
		if errors.Is(err, storage.ErrArtifactIntegrity) || errors.Is(err, storage.ErrObjectNotFound) {
			state, code = "recovery_integrity_failed", "recovery_integrity_failed"
		}
		return finishRecoveryAction(ctx, record, snapshot, state, source.key, code, recoveryKey)
	}
	return finishRecoveryAction(ctx, record, snapshot, "recovered_pending_retention_decision", source.key, "", recoveryKey)
}

func artifactRecoveryObjectKey(taskID, actionID string) string {
	identity := sha256.Sum256([]byte(taskID + "\x00" + actionID))
	return fmt.Sprintf("outputs/tasks/recovery/%x.md", identity)
}

func finishRecoveryAction(ctx context.Context, record models.ArtifactRecoveryAction, snapshot models.TaskCompletion, state, sourceKey, errorCode string, recoveryKey ...string) (*ArtifactRecoveryOutcome, error) {
	err := config.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current models.TaskCompletion
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", snapshot.ID).First(&current).Error; err != nil {
			return err
		}
		if current.State != "artifact_recovery_hold" || current.OwnerIdentity != snapshot.OwnerIdentity || current.ResultKey != snapshot.ResultKey || current.ArtifactSHA256 != snapshot.ArtifactSHA256 || current.ArtifactBytes != snapshot.ArtifactBytes || !current.ExpiresAt.Equal(snapshot.ExpiresAt) {
			return ErrRecoveryCaseNotHeld
		}
		var action models.ArtifactRecoveryAction
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND state = ?", record.ID, "processing").First(&action).Error; err != nil {
			return err
		}
		updates := map[string]any{"state": state, "source_key": sourceKey, "last_error_code": errorCode, "lease_expires_at": nil, "updated_at": time.Now().UTC()}
		if len(recoveryKey) > 0 {
			updates["recovery_key"] = recoveryKey[0]
		}
		return tx.Model(&action).Updates(updates).Error
	})
	if err != nil {
		return nil, err
	}
	return &ArtifactRecoveryOutcome{State: state}, nil
}

func inspectRecoverySource(ctx context.Context, row *models.TaskCompletion) (recoverySource, string, string) {
	if row == nil || row.OwnerIdentity == "" || len(row.ArtifactSHA256) != 64 || row.ArtifactBytes < 0 {
		return recoverySource{}, "evidence_unavailable", "artifact_identity_missing"
	}
	if validTaskArtifactKey(row.ID, row.ResultKey) {
		path, cleanup, err := storage.ResolveVerifiedArtifact(ctx, row.ResultKey, row.ArtifactSHA256, row.ArtifactBytes)
		if err == nil {
			return recoverySource{key: row.ResultKey, path: path, free: cleanup}, "", ""
		}
		if !errors.Is(err, storage.ErrArtifactIntegrity) && !errors.Is(err, storage.ErrObjectNotFound) {
			return recoverySource{}, "evidence_deferred", "storage_read_unavailable"
		}
	}
	var owned models.WorkerResultOwnership
	if err := config.DB.WithContext(ctx).Where("worker_job_id = ? AND owner_identity = ? AND state = ?", row.ID, row.OwnerIdentity, "terminal").Limit(1).Find(&owned).Error; err != nil {
		return recoverySource{}, "evidence_deferred", "ownership_read_unavailable"
	}
	var status struct {
		Status string `json:"status"`
	}
	if owned.ID == "" || json.Unmarshal([]byte(owned.StatusJSON), &status) != nil || status.Status != "succeeded" || (owned.SubmitPath != "/api/v1/jobs/submit" && owned.SubmitPath != "/api/v1/markdown/convert") {
		return recoverySource{}, "evidence_unavailable", "owner_matched_success_missing"
	}
	const prefix = "jobs/markdown/output/"
	name := strings.TrimPrefix(owned.ResultKey, prefix)
	if !storage.CanonicalObjectKey(owned.ResultKey) || !strings.HasPrefix(owned.ResultKey, prefix) || strings.Contains(name, "/") || !strings.HasSuffix(name, ".md") {
		return recoverySource{}, "evidence_unavailable", "worker_artifact_key_invalid"
	}
	var sourceKeys []string
	if json.Unmarshal([]byte(owned.SourceKeys), &sourceKeys) != nil {
		return recoverySource{}, "evidence_unavailable", "worker_source_ownership_invalid"
	}
	for _, key := range sourceKeys {
		if key == owned.ResultKey {
			return recoverySource{}, "evidence_unavailable", "worker_artifact_is_input"
		}
	}
	path, cleanup, err := storage.ResolveVerifiedArtifact(ctx, owned.ResultKey, row.ArtifactSHA256, row.ArtifactBytes)
	if err != nil {
		if errors.Is(err, storage.ErrArtifactIntegrity) || errors.Is(err, storage.ErrObjectNotFound) {
			return recoverySource{}, "evidence_unavailable", "worker_artifact_unavailable_or_corrupt"
		}
		return recoverySource{}, "evidence_deferred", "storage_read_unavailable"
	}
	return recoverySource{key: owned.ResultKey, path: path, free: cleanup}, "", ""
}

func recoveryRequestFingerprint(taskID, adminID, action, reason string) string {
	data, _ := json.Marshal(struct {
		TaskID  string `json:"task_id"`
		AdminID string `json:"admin_id"`
		Action  string `json:"action"`
		Reason  string `json:"reason"`
	}{taskID, adminID, action, reason})
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
