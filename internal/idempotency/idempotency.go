package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"pdfnest-backend/config"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/uploads"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
)

const (
	// The reservation expires if admission does not create a task.
	ProcessingTTL = 15 * time.Minute
	CreatedTTL    = 24 * time.Hour
)

type Record struct {
	State         string `json:"state"`
	TaskID        string `json:"taskId,omitempty"`
	Fingerprint   string `json:"fingerprint"`
	CreatedAt     int64  `json:"createdAt"`
	OwnerIdentity string `json:"ownerIdentity"`
}

func CalculateFingerprint(c *fiber.Ctx) (string, error) {
	hasher := sha256.New()
	hasher.Write([]byte(c.Path()))

	var filePath string
	var fileName string
	var fileSize int64

	if file, err := uploads.MustFile(c, "file"); err == nil && file != nil && file.Path != "" {
		filePath = file.Path
		fileName = file.Header.Filename
		fileSize = file.Header.Size
	} else if fh, err := c.FormFile("file"); err == nil && fh != nil {
		fileName = fh.Filename
		fileSize = fh.Size
	}

	if filePath != "" {
		f, err := os.Open(filePath)
		if err == nil {
			fileHasher := sha256.New()
			_, _ = io.Copy(fileHasher, f)
			f.Close()
			fullFileHash := hex.EncodeToString(fileHasher.Sum(nil))
			hasher.Write([]byte(fmt.Sprintf("%s:%d:%s", fileName, fileSize, fullFileHash)))
		}
	} else if fileName != "" {
		hasher.Write([]byte(fmt.Sprintf("%s:%d", fileName, fileSize)))
	}

	if len(c.Body()) > 0 {
		hasher.Write(c.Body())
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func Use(redisClient *redis.Client) fiber.Handler {
	return func(c *fiber.Ctx) error {
		client := redisClient
		if client == nil {
			client = config.Redis
		}

		key := strings.TrimSpace(c.Get("Idempotency-Key"))
		if key == "" {
			key = strings.TrimSpace(c.Get("X-Idempotency-Key"))
		}
		if key == "" {
			return c.Next()
		}

		identityID, _ := c.Locals(identity.LocalIdentityIDKey).(string)
		if identityID == "" {
			identityID = c.IP()
		}

		fingerprint, err := CalculateFingerprint(c)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"code":    "BAD_REQUEST",
				"message": "Failed to calculate request fingerprint: " + err.Error(),
			})
		}

		redisKey := fmt.Sprintf("pdfnest:idempotency:%s:%s", identityID, key)

		initialRecord := Record{
			State:         "PROCESSING",
			Fingerprint:   fingerprint,
			CreatedAt:     time.Now().Unix(),
			OwnerIdentity: identityID,
		}
		data, _ := json.Marshal(initialRecord)

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		if client == nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"code":    "TASK_STORAGE_UNAVAILABLE",
				"message": "Task persistence service is temporarily unavailable.",
			})
		}

		setOK, err := client.SetNX(ctx, redisKey, string(data), ProcessingTTL).Result()
		if err != nil {
			log.Printf("[IDEMPOTENCY ERROR] Redis SetNX failed for key %s: %v", redisKey, err)
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"code":    "TASK_STORAGE_UNAVAILABLE",
				"message": "Task persistence service is temporarily unavailable.",
			})
		}

		if !setOK {
			existingVal, err := client.Get(ctx, redisKey).Result()
			if err != nil {
				return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
					"code":    "TASK_STORAGE_UNAVAILABLE",
					"message": "Task persistence service is temporarily unavailable.",
				})
			}

			var rec Record
			if err := json.Unmarshal([]byte(existingVal), &rec); err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
					"error": "Failed to parse existing idempotency record",
				})
			}

			if rec.Fingerprint != fingerprint {
				return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{
					"code":  "IDEMPOTENCY_KEY_REUSE_WITH_DIFFERENT_PAYLOAD",
					"error": "The provided Idempotency-Key was previously used for a different request payload.",
				})
			}

			if rec.State == "CREATED" && rec.TaskID != "" {
				return c.Status(fiber.StatusAccepted).JSON(fiber.Map{
					"taskId": rec.TaskID,
				})
			}

			if rec.State == "PROCESSING" {
				c.Set("Retry-After", "2")
				return c.Status(fiber.StatusConflict).JSON(fiber.Map{
					"code":    "IDEMPOTENCY_REQUEST_IN_PROGRESS",
					"message": "A request with this Idempotency-Key is already being processed.",
				})
			}
		}

		c.Locals("idempotency_redis_key", redisKey)
		c.Locals("idempotency_fingerprint", fingerprint)
		c.Locals("idempotency_owner", identityID)

		return c.Next()
	}
}

func SetTaskID(c *fiber.Ctx, taskID string, redisClient *redis.Client) error {
	redisKey, _ := c.Locals("idempotency_redis_key").(string)
	fingerprint, _ := c.Locals("idempotency_fingerprint").(string)
	owner, _ := c.Locals("idempotency_owner").(string)

	if redisKey == "" {
		return nil
	}

	client := redisClient
	if client == nil {
		client = config.Redis
	}

	rec := Record{
		State:         "CREATED",
		TaskID:        taskID,
		Fingerprint:   fingerprint,
		CreatedAt:     time.Now().Unix(),
		OwnerIdentity: owner,
	}
	data, _ := json.Marshal(rec)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if client == nil {
		return errors.New("redis client not configured")
	}

	return client.Set(ctx, redisKey, string(data), CreatedTTL).Err()
}

const atomicReleaseIdempotencyLua = `
local key = KEYS[1]
local val = redis.call('GET', key)

if val and val ~= '' then
    local rec = cjson.decode(val)
    if rec.state == 'PROCESSING' then
        redis.call('DEL', key)
        return 1
    end
end

return 0
`

func Release(c *fiber.Ctx, redisClient *redis.Client) {
	redisKey, _ := c.Locals("idempotency_redis_key").(string)
	if redisKey == "" {
		return
	}

	client := redisClient
	if client == nil {
		client = config.Redis
	}

	if client == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, _ = client.Eval(ctx, atomicReleaseIdempotencyLua, []string{redisKey}).Result()
}
