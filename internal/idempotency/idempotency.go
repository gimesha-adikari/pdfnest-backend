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
	"sort"
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

	if uploadContext := uploads.FromCtx(c); uploadContext != nil {
		fields := make([]string, 0, len(uploadContext.Files))
		for field := range uploadContext.Files {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		for _, field := range fields {
			for index, file := range uploadContext.All(field) {
				if file == nil || file.Header == nil {
					continue
				}
				hasher.Write([]byte(fmt.Sprintf("file:%s:%d:%s:%d", field, index, file.Header.Filename, file.Header.Size)))
				if file.Path == "" {
					continue
				}
				f, err := os.Open(file.Path)
				if err != nil {
					continue
				}
				fileHasher := sha256.New()
				_, _ = io.Copy(fileHasher, f)
				_ = f.Close()
				hasher.Write([]byte(hex.EncodeToString(fileHasher.Sum(nil))))
			}
		}
	} else if fh, err := c.FormFile("file"); err == nil && fh != nil {
		hasher.Write([]byte(fmt.Sprintf("file:%s:%d", fh.Filename, fh.Size)))
	}

	if strings.HasPrefix(strings.ToLower(c.Get(fiber.HeaderContentType)), "multipart/form-data") {
		form, err := c.MultipartForm()
		if err != nil {
			return "", err
		}
		languageMode, languagePolicy := canonicalLanguagePolicy(form.Value)
		keys := make([]string, 0, len(form.Value))
		for key := range form.Value {
			if key == "language" || key == "languages" || key == "language_mode" {
				continue
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			values := append([]string(nil), form.Value[key]...)
			if key == "language" || key == "languages" {
				for index, value := range values {
					values[index] = canonicalLanguageField(value)
				}
			}
			sort.Strings(values)
			for _, value := range values {
				hasher.Write([]byte(fmt.Sprintf("field:%s=%s", key, value)))
			}
		}
		if languageMode != "" || languagePolicy != "" {
			hasher.Write([]byte(fmt.Sprintf("field:language_policy=%s:%s", languageMode, languagePolicy)))
		}
	} else if len(c.Body()) > 0 {
		hasher.Write(c.Body())
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func canonicalLanguagePolicy(values map[string][]string) (string, string) {
	mode := ""
	if raw := values["language_mode"]; len(raw) > 0 {
		mode = strings.ToUpper(strings.TrimSpace(raw[0]))
	}

	seen := make(map[string]struct{})
	for _, key := range []string{"language", "languages"} {
		for _, value := range values[key] {
			canonical := canonicalLanguageField(value)
			if canonical == "AUTO" {
				if mode == "" {
					mode = "AUTO"
				}
				continue
			}
			for _, token := range strings.Split(canonical, "+") {
				if token != "" {
					seen[token] = struct{}{}
				}
			}
		}
	}
	if mode == "" && len(seen) > 0 {
		mode = "EXPLICIT"
	}
	if mode == "AUTO" {
		return mode, "AUTO"
	}
	languages := make([]string, 0, len(seen))
	for language := range seen {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	return mode, strings.Join(languages, "+")
}

func canonicalLanguageField(value string) string {
	clean := strings.ToLower(strings.TrimSpace(value))
	if clean == "auto" || clean == "automatic" || clean == "detect" {
		return "AUTO"
	}
	parts := strings.FieldsFunc(clean, func(r rune) bool { return r == '+' || r == ',' || r == ' ' || r == '\t' })
	sort.Strings(parts)
	return strings.Join(parts, "+")
}

type ReplayHandler func(*fiber.Ctx, Record) error

func Use(redisClient *redis.Client) fiber.Handler {
	return UseWithReplay(redisClient, nil)
}

// UseWithReplay preserves the existing idempotency reservation semantics while
// allowing a product route to shape an idempotent replay response.
func UseWithReplay(redisClient *redis.Client, replay ReplayHandler) fiber.Handler {
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
				if replay != nil {
					return replay(c, rec)
				}
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
