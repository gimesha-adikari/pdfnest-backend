package edit

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/models"
	"pdfnest-backend/internal/storage"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const workerExtractPath = "/api/v1/editor/extract"

type extractionRequestReceipt struct {
	State         string `json:"state"`
	JobID         string `json:"job_id,omitempty"`
	SourceTracker string `json:"source_tracker,omitempty"`
}

func extractionDispatchID(owner string, attempt uuid.UUID) string {
	// The same client key cannot collide with another owner's dispatch or with
	// another tool's durable request. The client cannot choose an object key.
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(storage.OwnedPrefix(owner, "editor_source")+"/"+attempt.String())).String()
}

func extractAttemptID(raw string) (uuid.UUID, error) {
	attempt, err := uuid.Parse(raw)
	if err != nil || attempt.Version() != 4 || attempt.String() != raw {
		return uuid.Nil, fiber.NewError(fiber.StatusBadRequest, "An extraction request ID must be a canonical UUID v4.")
	}
	return attempt, nil
}

func extractionReceipt(row models.WorkerResultOwnership, owner string) (extractionRequestReceipt, error) {
	var sources []string
	if json.Unmarshal([]byte(row.SourceKeys), &sources) != nil || len(sources) != 1 ||
		!storage.IsOwnedKey(sources[0], owner, "editor_source") {
		return extractionRequestReceipt{}, fiber.ErrForbidden
	}
	if row.State == "deleting" || row.State == "deleted" || row.ExpiresAt != nil && !row.ExpiresAt.After(time.Now()) {
		return extractionRequestReceipt{State: "expired"}, nil
	}
	if row.WorkerJobID == "" {
		return extractionRequestReceipt{State: "pending"}, nil
	}
	return extractionRequestReceipt{State: "accepted", JobID: row.WorkerJobID, SourceTracker: sources[0]}, nil
}

func readExtractionReceipt(ctx context.Context, owner string, attempt uuid.UUID) (extractionRequestReceipt, error) {
	if config.DB == nil {
		return extractionRequestReceipt{}, fiber.ErrServiceUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var row models.WorkerResultOwnership
	err := config.DB.WithContext(ctx).Select("state", "worker_job_id", "source_keys", "expires_at").
		Where("request_id = ? AND submit_path = ?", extractionDispatchID(owner, attempt), workerExtractPath).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return extractionRequestReceipt{State: "not_found"}, nil
	}
	if err != nil {
		return extractionRequestReceipt{}, fiber.ErrServiceUnavailable
	}
	return extractionReceipt(row, owner)
}

func (cr *Controller) HandleExtractionReceipt(c *fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	attempt, err := extractAttemptID(c.Params("request_id"))
	if err != nil {
		return err
	}
	receipt, err := readExtractionReceipt(c.UserContext(), identity.MustFromContext(c).ID, attempt)
	if err != nil {
		return err
	}
	return c.JSON(receipt)
}
