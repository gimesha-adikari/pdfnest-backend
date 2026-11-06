package tasks

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"mime"
	"os"
	"path/filepath"
	"pdfnest-backend/config"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/storage"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

func HandleTaskDownload(c *fiber.Ctx) error {
	id := c.Params("id")
	task, err := Registry.Get(id)
	if err != nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"code":    "TASK_STORAGE_UNAVAILABLE",
			"message": "Task persistence service is temporarily unavailable.",
		})
	}

	if task == nil || task.Status != "COMPLETED" {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"code":    "FILE_NOT_FOUND",
			"message": "The requested asset is either expired or still processing.",
		})
	}

	// Task output is scoped to the identity that created the task.
	requesterID, _ := c.Locals(identity.LocalIdentityIDKey).(string)
	if requesterID == "" {
		requesterID = c.IP()
	}
	if !isAuthorizedOwner(c, task, requesterID) {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
			"code":    "FORBIDDEN",
			"message": "You are not authorized to download this task artifact.",
		})
	}

	durable, durableErr := durableTask(c.UserContext(), id)
	if durableErr != nil {
		return c.SendStatus(fiber.StatusServiceUnavailable)
	}

	key := task.ResultKey
	if durable != nil {
		if durable.State != "completed" {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"code":    "FILE_NOT_FOUND",
				"message": "The requested asset is either expired or still processing.",
			})
		}
		key = durable.ResultKey
		if key == "" {
			if quarantineErr := Registry.quarantineArtifact(c.UserContext(), id); quarantineErr != nil {
				return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
					"code":    "TASK_STORAGE_UNAVAILABLE",
					"message": "Task persistence service is temporarily unavailable.",
				})
			}
			return c.Status(fiber.StatusGone).JSON(fiber.Map{
				"code":    "ASSET_REMOVED",
				"message": "The completed output is unavailable and has been retained for recovery.",
			})
		}
	}
	if key == "" && strings.HasPrefix(task.ResultURL, "r2://") {
		key = strings.TrimPrefix(task.ResultURL, "r2://")
	}

	if durable == nil && key != "" && !storage.RemoteStorageEnabled() {
		task.ResultURL = filepath.Join(storage.GetLocalStorageDir(), filepath.FromSlash(key))
		key = ""
	}

	if key != "" {
		ext := filepath.Ext(key)
		var tmpPath string
		if durable != nil {
			var cleanup func()
			tmpPath, cleanup, err = storage.ResolveVerifiedArtifact(c.UserContext(), key, durable.ArtifactSHA256, durable.ArtifactBytes)
			if err != nil {
				if errors.Is(err, storage.ErrArtifactIntegrity) || errors.Is(err, storage.ErrObjectNotFound) {
					if quarantineErr := Registry.quarantineArtifact(c.UserContext(), id); quarantineErr != nil {
						return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
							"code":    "TASK_STORAGE_UNAVAILABLE",
							"message": "Task persistence service is temporarily unavailable.",
						})
					}
					return c.Status(fiber.StatusGone).JSON(fiber.Map{
						"code":    "ASSET_REMOVED",
						"message": "The completed output is unavailable and has been retained for recovery.",
					})
				}
				return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
					"code":    "STORAGE_UNAVAILABLE",
					"message": "The requested output could not be verified; retry later.",
				})
			}
			defer cleanup()
		} else {
			r2Store, storeErr := storage.Default()
			if storeErr != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
					"code":    "STORAGE_ERROR",
					"message": "Failed to initialize cloud storage client.",
				})
			}
			tmpPath, err = r2Store.DownloadToTemp(key, "dl-task-", ext)
			if err != nil {
				return c.Status(fiber.StatusGone).JSON(fiber.Map{
					"code":    "ASSET_REMOVED",
					"message": "The requested asset has expired or is no longer available in cloud storage.",
				})
			}
			defer func() {
				_ = os.Remove(tmpPath)
			}()
		}

		if durable == nil && task.ReservationID != "" && (CommitTaskBillingHandlerWithKind != nil || CommitTaskBillingHandler != nil) {
			if CommitTaskBillingHandlerWithKind != nil {
				CommitTaskBillingHandlerWithKind(task.ReservationID, task.ReservationKind)
			} else {
				CommitTaskBillingHandler(task.ReservationID)
			}
		}

		// The object key is server-controlled, so its extension is safe for content-type selection.
		contentType := "application/octet-stream"
		switch strings.ToLower(ext) {
		case ".pdf":
			contentType = "application/pdf"
		case ".txt":
			contentType = "text/plain; charset=utf-8"
		case ".md":
			contentType = "text/markdown; charset=utf-8"
		case ".json":
			contentType = "application/json"
		default:
			if customType := mime.TypeByExtension(ext); customType != "" {
				contentType = customType
			}
		}

		c.Set("Content-Type", contentType)
		c.Attachment(filepath.Base(key))
		return c.SendFile(tmpPath)
	}

	if durable != nil {
		if quarantineErr := Registry.quarantineArtifact(c.UserContext(), id); quarantineErr != nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"code":    "TASK_STORAGE_UNAVAILABLE",
				"message": "Task persistence service is temporarily unavailable.",
			})
		}
		return c.Status(fiber.StatusGone).JSON(fiber.Map{
			"code":    "ASSET_REMOVED",
			"message": "The completed output is unavailable and has been retained for recovery.",
		})
	}

	// Retain the local fallback while tasks created before object storage migration can expire.
	filePath := task.ResultURL
	if info, err := os.Stat(filePath); err != nil || info.IsDir() {
		return c.Status(fiber.StatusGone).JSON(fiber.Map{
			"code":    "ASSET_REMOVED",
			"message": "The requested temporary asset has been cleaned from cache storage.",
		})
	}

	if durable == nil && task.ReservationID != "" && (CommitTaskBillingHandlerWithKind != nil || CommitTaskBillingHandler != nil) {
		if CommitTaskBillingHandlerWithKind != nil {
			CommitTaskBillingHandlerWithKind(task.ReservationID, task.ReservationKind)
		} else {
			CommitTaskBillingHandler(task.ReservationID)
		}
	}

	c.Set("Content-Type", "application/octet-stream")
	c.Attachment(filepath.Base(filePath))
	return c.SendFile(filePath)
}

func isAuthorizedOwner(c *fiber.Ctx, task *TaskStatus, requesterID string) bool {
	requesterType, _ := c.Locals(identity.LocalIdentityType).(string)
	token := strings.TrimSpace(c.Query("token"))
	if token == "" {
		token = strings.TrimSpace(c.Get("X-Download-Token"))
	}
	return isAuthorizedOwnerForIdentity(c.UserContext(), task, requesterID, requesterType, token)
}

func isAuthorizedOwnerForIdentity(ctx context.Context, task *TaskStatus, requesterID, requesterType, token string) bool {
	if task == nil {
		return false
	}

	// An account task must be checked against the durable deletion ledger before
	// either its owner identity or its capability token can grant access. The
	// authenticated request type comes from identity.Resolve; UUID shape alone
	// cannot distinguish account IDs from guest IDs.
	guestOwnerMatch := requesterType == string(identity.TypeGuest) && task.OwnerIdentity != "" && task.OwnerIdentity == requesterID
	needsDeletionCheck := task.OwnerIdentity != "" && (requesterType == string(identity.TypeUser) || !guestOwnerMatch && isUUID(task.OwnerIdentity))
	if needsDeletionCheck && !accountOwnerNotDeleted(ctx, task.OwnerIdentity) {
		return false
	}

	if task.OwnerIdentity != "" && task.OwnerIdentity == requesterID {
		return true
	}

	// The capability token is the only authorization for legacy task records
	// without an owner identity, and remains a fallback for owned tasks when a
	// client's identity cookie is unavailable. Owned account tasks reached via
	// this fallback still passed the deletion-ledger check above.
	if token != "" && task.DownloadToken != "" && token == task.DownloadToken {
		return true
	}

	return false
}

func isUUID(value string) bool {
	_, err := uuid.Parse(value)
	return err == nil
}

func accountOwnerNotDeleted(ctx context.Context, ownerID string) bool {
	if config.DB == nil || !isUUID(ownerID) {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	var present bool
	if err := config.DB.WithContext(ctx).Raw("SELECT to_regclass('public.account_storage_cleanups') IS NOT NULL").Scan(&present).Error; err != nil || !present {
		return false
	}
	var deleted bool
	if err := config.DB.WithContext(ctx).Raw("SELECT EXISTS(SELECT 1 FROM public.account_storage_cleanups WHERE user_id = ?)", ownerID).Scan(&deleted).Error; err != nil {
		return false
	}
	return !deleted
}
