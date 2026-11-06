package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"pdfnest-backend/config"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/limiter"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const (
	TaskKeyPrefix           = "pdfnest:tasks:"
	TaskTTL                 = 1 * time.Hour
	DefaultStuckTaskTimeout = 900 // 15 minutes default
)

type TaskStatus struct {
	ID              string `json:"id"`
	Status          string `json:"status"`
	Progress        int    `json:"progress"`
	ResultKey       string `json:"resultKey,omitempty"`
	ResultURL       string `json:"resultUrl,omitempty"`
	OwnerIdentity   string `json:"ownerIdentity,omitempty"`
	ReservationID   string `json:"reservationId,omitempty"`
	ReservationKind string `json:"reservationKind,omitempty"`
	DownloadToken   string `json:"downloadToken,omitempty"`
	Error           string `json:"error,omitempty"`
	UpdatedAt       int64  `json:"updatedAt,omitempty"`
}

type TaskStatusResponse struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	Progress      int    `json:"progress"`
	DownloadToken string `json:"downloadToken,omitempty"`
	Error         string `json:"error,omitempty"`
	UpdatedAt     int64  `json:"updatedAt,omitempty"`
}

func publicTaskStatus(task *TaskStatus) *TaskStatusResponse {
	if task == nil {
		return nil
	}
	return &TaskStatusResponse{
		ID:            task.ID,
		Status:        task.Status,
		Progress:      task.Progress,
		DownloadToken: task.DownloadToken,
		Error:         task.Error,
		UpdatedAt:     task.UpdatedAt,
	}
}

type TaskRegistry struct {
	client *redis.Client
}

var Registry = &TaskRegistry{}

func (r *TaskRegistry) getClient() *redis.Client {
	if r.client != nil {
		return r.client
	}
	return config.Redis
}

func getStuckTaskTimeout() int {
	valStr := strings.TrimSpace(os.Getenv("STUCK_TASK_TIMEOUT_SECONDS"))
	if valStr == "" {
		return DefaultStuckTaskTimeout
	}
	val, err := strconv.Atoi(valStr)
	if err != nil || val <= 0 {
		return DefaultStuckTaskTimeout
	}
	return val
}

const atomicSetTaskLua = `
local key = KEYS[1]
local newValStr = ARGV[1]
local ttlSeconds = tonumber(ARGV[2]) or 3600

local newRecord = cjson.decode(newValStr)
local val = redis.call('GET', key)

if val and val ~= '' then
    local current = cjson.decode(val)
    local curStatus = current.status
    local newStatus = newRecord.status

    if curStatus == 'COMPLETED' or curStatus == 'FAILED' or curStatus == 'CANCELLED' then
        if curStatus == newStatus then
            return 'REJECTED'
        end
        return 'REJECTED'
    end

    if curStatus == 'PROCESSING' and newStatus == 'PROCESSING' then
        local curProg = tonumber(current.progress) or 0
        local newProg = tonumber(newRecord.progress) or 0
        if newProg < curProg then
            newRecord.progress = curProg
            newValStr = cjson.encode(newRecord)
        end
    end
end

redis.call('SET', key, newValStr, 'EX', ttlSeconds)
return 'ACCEPTED'
`

const atomicGetCheckStaleLua = `
local key = KEYS[1]
local maxStaleSeconds = tonumber(ARGV[1])
local now = tonumber(ARGV[2])
local ttlSeconds = tonumber(ARGV[3]) or 3600

local val = redis.call('GET', key)
if not val or val == '' then
    return cjson.encode({ result = "NOT_FOUND" })
end

local data = cjson.decode(val)
local curStatus = data.status

if curStatus == 'COMPLETED' or curStatus == 'FAILED' or curStatus == 'CANCELLED' then
    return cjson.encode({ result = "TERMINAL_ALREADY", payload = val })
end

local updatedAt = tonumber(data.updatedAt) or 0
if (now - updatedAt) > maxStaleSeconds then
    data.status = 'FAILED'
    data.error = 'Task processing execution timed out or server restarted unexpectedly.'
    data.updatedAt = now
    local newValStr = cjson.encode(data)
    redis.call('SET', key, newValStr, 'EX', ttlSeconds)
    return cjson.encode({
        result = "STALE_TRANSITION_PERFORMED",
        reservationId = data.reservationId or "",
        payload = newValStr
    })
end

return cjson.encode({ result = "HEALTHY", payload = val })
`

const atomicCancelTaskLua = `
local key = KEYS[1]
local owner = ARGV[1]
local capability = ARGV[2]
local now = tonumber(ARGV[3])
local ttlSeconds = tonumber(ARGV[4]) or 3600
local authorizationMode = ARGV[5] or 'fallback'
local expectedOwner = ARGV[6] or ''
local requiresAccountCheck = ARGV[7] == '1'

local val = redis.call('GET', key)
if not val or val == '' then
    return cjson.encode({ result = "NOT_FOUND" })
end

local data = cjson.decode(val)
local curStatus = data.status
local taskOwner = data.ownerIdentity or ''
if taskOwner ~= expectedOwner then
    return cjson.encode({ result = "TASK_CHANGED", payload = val })
end

local ownerMatches = taskOwner ~= '' and owner and owner ~= '' and taskOwner == owner
local capabilityMatches = data.downloadToken and data.downloadToken ~= '' and capability and capability ~= '' and data.downloadToken == capability
if not ownerMatches and not capabilityMatches then
    return cjson.encode({ result = "UNAUTHORIZED", payload = val })
end
-- A Redis-only account task has no durable row to serialize with account
-- deletion. It may transition only while the owner's database row is locked
-- and its deletion ledger has been checked in that transaction.
if requiresAccountCheck and authorizationMode == 'fallback' then
    return cjson.encode({ result = "AUTHORIZATION_UNAVAILABLE", payload = val })
end

if curStatus == 'COMPLETED' or curStatus == 'FAILED' or curStatus == 'CANCELLED' then
    return cjson.encode({ result = "TERMINAL_ALREADY", payload = val })
end

data.status = 'CANCELLED'
data.error = 'Task processing cancelled by user.'
data.updatedAt = now
local newValStr = cjson.encode(data)
redis.call('SET', key, newValStr, 'EX', ttlSeconds)

return cjson.encode({ result = "CANCELLED_SUCCESS", payload = newValStr })
`

type staleLuaResult struct {
	Result        string `json:"result"`
	ReservationID string `json:"reservationId"`
	Payload       string `json:"payload"`
}

var StaleTaskBillingHandler func(reservationID string)
var CommitTaskBillingHandler func(reservationID string)
var StaleTaskBillingHandlerWithKind func(reservationID, reservationKind string)
var CommitTaskBillingHandlerWithKind func(reservationID, reservationKind string)
var CancelTaskBillingHandler func(reservationID string)
var CancelTaskBillingHandlerWithKind func(reservationID, reservationKind string)

func (r *TaskRegistry) GetWithTransition(id string) (*TaskStatus, bool, string, error) {
	durable, durableErr := durableTask(context.Background(), id)
	if durableErr != nil {
		return nil, false, "", durableErr
	}
	if durable != nil && durable.State != "active" {
		task := durableStatus(durable)
		if durable.State == "completed" && !time.Now().Before(durable.ExpiresAt) {
			task.Status = "EXPIRED"
			task.ResultKey = ""
			task.ResultURL = ""
		}
		return task, false, "", nil
	}

	client := r.getClient()
	if client == nil {
		return nil, false, "", errors.New("redis client not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	key := TaskKeyPrefix + strings.TrimSpace(id)
	stuckTimeout := getStuckTaskTimeout()
	now := time.Now().Unix()

	res, err := client.Eval(ctx, atomicGetCheckStaleLua, []string{key}, stuckTimeout, now, int(TaskTTL.Seconds())).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, false, "", nil
		}
		return nil, false, "", fmt.Errorf("failed to read task state from redis: %w", err)
	}

	valStr, ok := res.(string)
	if !ok || valStr == "" {
		return nil, false, "", nil
	}

	var luaRes staleLuaResult
	if err := json.Unmarshal([]byte(valStr), &luaRes); err != nil {
		// Fallback for legacy raw TaskStatus JSON payload
		var status TaskStatus
		if errLegacy := json.Unmarshal([]byte(valStr), &status); errLegacy == nil {
			return &status, false, "", nil
		}
		return nil, false, "", fmt.Errorf("failed to unmarshal task status JSON: %w", err)
	}

	if luaRes.Result == "NOT_FOUND" || luaRes.Payload == "" {
		if durable != nil {
			return durableStatus(durable), false, "", nil
		}
		return nil, false, "", nil
	}

	var status TaskStatus
	if err := json.Unmarshal([]byte(luaRes.Payload), &status); err != nil {
		return nil, false, "", fmt.Errorf("failed to unmarshal task status payload JSON: %w", err)
	}

	if durable != nil && status.Status == "COMPLETED" {
		// An actor's Redis mirror cannot bypass durable artifact settlement.
		return durableStatus(durable), false, "", nil
	}

	stalePerformed := (luaRes.Result == "STALE_TRANSITION_PERFORMED")
	if stalePerformed {
		if durable != nil {
			row, changed, err := transitionDurable(context.Background(), id, "", "failed", status.Error)
			if err != nil {
				return nil, false, "", err
			}
			if !changed && row != nil {
				return durableStatus(row), false, "", nil
			}
		}
		if luaRes.ReservationID != "" {
			if StaleTaskBillingHandlerWithKind != nil {
				StaleTaskBillingHandlerWithKind(luaRes.ReservationID, status.ReservationKind)
			} else if StaleTaskBillingHandler != nil {
				StaleTaskBillingHandler(luaRes.ReservationID)
			}
		}
		_ = limiter.Default.Release(ctx, status.ID, status.OwnerIdentity)
	}

	return &status, stalePerformed, luaRes.ReservationID, nil
}

func (r *TaskRegistry) Get(id string) (*TaskStatus, error) {
	task, _, _, err := r.GetWithTransition(id)
	return task, err
}

func (r *TaskRegistry) SetWithDownloadToken(id string, status string, progress int, resultKey string, errStr string, ownerIdentity string, downloadToken string, reservationID ...string) (bool, error) {
	if allowed, err := allowDurableUpdate(id, status, errStr); err != nil || !allowed {
		return false, err
	}

	client := r.getClient()
	if client == nil {
		log.Printf("[TASK REGISTRY ERROR] Redis client not configured for Set task %s", id)
		return false, errors.New("redis client not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	key := TaskKeyPrefix + strings.TrimSpace(id)

	var resID string
	var resKind string
	if len(reservationID) > 0 && reservationID[0] != "" {
		resID = reservationID[0]
	}
	if len(reservationID) > 1 && reservationID[1] != "" {
		resKind = reservationID[1]
	}
	dlToken := downloadToken
	if dlToken == "" {
		if existingVal, err := client.Get(ctx, key).Result(); err == nil && existingVal != "" {
			var existingTask TaskStatus
			if err := json.Unmarshal([]byte(existingVal), &existingTask); err == nil {
				if resID == "" {
					resID = existingTask.ReservationID
				}
				if resKind == "" {
					resKind = existingTask.ReservationKind
				}
				dlToken = existingTask.DownloadToken
			}
		}
	}
	if dlToken == "" {
		dlToken = uuid.New().String()
	}

	task := &TaskStatus{
		ID:              id,
		Status:          status,
		Progress:        progress,
		ResultKey:       resultKey,
		Error:           errStr,
		UpdatedAt:       time.Now().Unix(),
		OwnerIdentity:   ownerIdentity,
		ReservationID:   resID,
		ReservationKind: resKind,
		DownloadToken:   dlToken,
	}

	if resultKey != "" {
		task.ResultURL = "r2://" + resultKey
	}

	data, err := json.Marshal(task)
	if err != nil {
		log.Printf("[TASK REGISTRY ERROR] Failed to marshal task %s: %v", id, err)
		return false, err
	}

	res, err := client.Eval(ctx, atomicSetTaskLua, []string{key}, string(data), int(TaskTTL.Seconds())).Result()
	if err != nil {
		log.Printf("[TASK REGISTRY ERROR] Redis Set (atomic) failed for task %s: %v", id, err)
		return false, err
	}

	resStr, ok := res.(string)
	if ok && resStr == "ACCEPTED" {
		return true, nil
	}
	return false, nil
}

func (r *TaskRegistry) SetWithKey(id string, status string, progress int, resultKey string, errStr string, ownerIdentity string, reservationID ...string) (bool, error) {
	var resID, resKind string
	if len(reservationID) > 0 {
		resID = reservationID[0]
	}
	if len(reservationID) > 1 {
		resKind = reservationID[1]
	}
	return r.setWithKey(id, status, progress, resultKey, errStr, ownerIdentity, resID, resKind)
}

// SetWithResultURL preserves explicit task owners and local filesystem paths
// without constructing an object key.
func (r *TaskRegistry) SetWithResultURL(id string, status string, progress int, resultURL string, errStr string, ownerIdentity string, reservationID ...string) (bool, error) {
	var resID, resKind string
	if len(reservationID) > 0 {
		resID = reservationID[0]
	}
	if len(reservationID) > 1 {
		resKind = reservationID[1]
	}
	return r.setWithResultURL(id, status, progress, "", resultURL, errStr, ownerIdentity, resID, resKind)
}

// SetWithKeyAndBilling records the reservation store with the task at
// creation time. Later status updates preserve both fields from Redis.
func (r *TaskRegistry) SetWithKeyAndBilling(id string, status string, progress int, resultKey string, errStr string, ownerIdentity string, reservationID string, reservationKind string) (bool, error) {
	resultURL := ""
	if resultKey != "" {
		resultURL = "r2://" + resultKey
	}
	return r.setWithResultURL(id, status, progress, resultKey, resultURL, errStr, ownerIdentity, reservationID, reservationKind)
}

func (r *TaskRegistry) setWithKey(id string, status string, progress int, resultKey string, errStr string, ownerIdentity string, reservationID string, reservationKind string) (bool, error) {
	resultURL := ""
	if resultKey != "" {
		resultURL = "r2://" + resultKey
	}
	return r.setWithResultURL(id, status, progress, resultKey, resultURL, errStr, ownerIdentity, reservationID, reservationKind)
}

func (r *TaskRegistry) setWithResultURL(id string, status string, progress int, resultKey string, resultURL string, errStr string, ownerIdentity string, reservationID string, reservationKind string) (bool, error) {
	if allowed, err := allowDurableUpdate(id, status, errStr); err != nil || !allowed {
		return false, err
	}

	client := r.getClient()
	if client == nil {
		log.Printf("[TASK REGISTRY ERROR] Redis client not configured for Set task %s", id)
		return false, errors.New("redis client not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	key := TaskKeyPrefix + strings.TrimSpace(id)

	var dlToken string
	resID := reservationID
	resKind := reservationKind
	if existingVal, err := client.Get(ctx, key).Result(); err == nil && existingVal != "" {
		var existingTask TaskStatus
		if err := json.Unmarshal([]byte(existingVal), &existingTask); err == nil {
			if resID == "" {
				resID = existingTask.ReservationID
			}
			if resKind == "" {
				resKind = existingTask.ReservationKind
			}
			dlToken = existingTask.DownloadToken
		}
	}
	if dlToken == "" {
		dlToken = uuid.New().String()
	}

	task := &TaskStatus{
		ID:              id,
		Status:          status,
		Progress:        progress,
		ResultKey:       resultKey,
		ResultURL:       resultURL,
		Error:           errStr,
		UpdatedAt:       time.Now().Unix(),
		OwnerIdentity:   ownerIdentity,
		ReservationID:   resID,
		ReservationKind: resKind,
		DownloadToken:   dlToken,
	}

	if resultKey != "" && task.ResultURL == "" {
		task.ResultURL = "r2://" + resultKey
	}

	data, err := json.Marshal(task)
	if err != nil {
		log.Printf("[TASK REGISTRY ERROR] Failed to marshal task %s: %v", id, err)
		return false, err
	}

	res, err := client.Eval(ctx, atomicSetTaskLua, []string{key}, string(data), int(TaskTTL.Seconds())).Result()
	if err != nil {
		log.Printf("[TASK REGISTRY ERROR] Redis Set (atomic) failed for task %s: %v", id, err)
		return false, err
	}

	resStr, ok := res.(string)
	if ok && resStr == "ACCEPTED" {
		return true, nil
	}
	return false, nil
}

func (r *TaskRegistry) Set(id string, status string, progress int, resultURL string, errStr string) error {
	if allowed, err := allowDurableUpdate(id, status, errStr); err != nil || !allowed {
		return err
	}

	var resultKey string
	if strings.HasPrefix(resultURL, "r2://") {
		resultKey = strings.TrimPrefix(resultURL, "r2://")
	}

	client := r.getClient()
	if client == nil {
		log.Printf("[TASK REGISTRY ERROR] Redis client not configured for Set task %s", id)
		return errors.New("redis client not configured")
	}

	// Read existing task to preserve OwnerIdentity and ReservationID if available
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	key := TaskKeyPrefix + strings.TrimSpace(id)
	var ownerIdentity string
	var reservationID string
	var reservationKind string
	var dlToken string
	if existingVal, err := client.Get(ctx, key).Result(); err == nil && existingVal != "" {
		var existingTask TaskStatus
		if err := json.Unmarshal([]byte(existingVal), &existingTask); err == nil {
			ownerIdentity = existingTask.OwnerIdentity
			reservationID = existingTask.ReservationID
			reservationKind = existingTask.ReservationKind
			dlToken = existingTask.DownloadToken
			if resultKey == "" && existingTask.ResultKey != "" {
				resultKey = existingTask.ResultKey
			}
		}
	}
	if dlToken == "" {
		dlToken = uuid.New().String()
	}

	task := &TaskStatus{
		ID:              id,
		Status:          status,
		Progress:        progress,
		ResultKey:       resultKey,
		ResultURL:       resultURL,
		Error:           errStr,
		UpdatedAt:       time.Now().Unix(),
		OwnerIdentity:   ownerIdentity,
		ReservationID:   reservationID,
		ReservationKind: reservationKind,
		DownloadToken:   dlToken,
	}

	if resultKey != "" && task.ResultURL == "" {
		task.ResultURL = "r2://" + resultKey
	}

	data, err := json.Marshal(task)
	if err != nil {
		log.Printf("[TASK REGISTRY ERROR] Failed to marshal task %s: %v", id, err)
		return err
	}

	res, err := client.Eval(ctx, atomicSetTaskLua, []string{key}, string(data), int(TaskTTL.Seconds())).Result()
	if err != nil {
		log.Printf("[TASK REGISTRY ERROR] Redis Set (atomic) failed for task %s: %v", id, err)
		return err
	}

	_ = res
	return nil
}

type cancelLuaResult struct {
	Result  string `json:"result"`
	Payload string `json:"payload"`
}

func (r *TaskRegistry) CancelTask(id string, ownerIdentity string, capabilityToken ...string) (string, *TaskStatus, error) {
	token := ""
	if len(capabilityToken) > 0 {
		token = strings.TrimSpace(capabilityToken[0])
	}
	return r.CancelTaskForIdentity(id, ownerIdentity, "", token)
}

// CancelTaskForIdentity carries the trusted request identity type into the
// durable and Redis state-transition boundaries. CancelTask remains as a
// compatibility wrapper for internal callers that do not have HTTP identity
// metadata.
func (r *TaskRegistry) CancelTaskForIdentity(id, ownerIdentity, requesterType, capabilityToken string) (string, *TaskStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	token := strings.TrimSpace(capabilityToken)

	result, task, durableErr := cancelDurable(ctx, id, ownerIdentity, token, requesterType)
	if durableErr != nil {
		return "ERROR", nil, durableErr
	}
	if result != "" {
		if result == "CANCELLED_SUCCESS" && task != nil {
			needsAccountCheck := requiresAccountDeletionCheck(task.OwnerIdentity, ownerIdentity, requesterType)
			_, _, _ = r.evalTaskCancellation(ctx, id, ownerIdentity, token, "durable", task.OwnerIdentity, needsAccountCheck)
		}
		return result, task, nil
	}
	return r.cancelRedisFallback(ctx, id, ownerIdentity, requesterType, token)
}

func cancellationGrantMatches(task *TaskStatus, ownerIdentity, capabilityToken string) bool {
	if task == nil {
		return false
	}
	ownerMatches := task.OwnerIdentity != "" && ownerIdentity != "" && task.OwnerIdentity == ownerIdentity
	capabilityMatches := task.DownloadToken != "" && capabilityToken != "" && task.DownloadToken == capabilityToken
	return ownerMatches || capabilityMatches
}

func (r *TaskRegistry) cancelRedisFallback(ctx context.Context, id, ownerIdentity, requesterType, token string) (string, *TaskStatus, error) {
	client := r.getClient()
	if client == nil {
		return "ERROR", nil, errors.New("redis client not configured")
	}
	key := TaskKeyPrefix + strings.TrimSpace(id)
	serialized, err := client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return "NOT_FOUND", nil, nil
		}
		return "ERROR", nil, fmt.Errorf("failed to inspect task before cancellation: %w", err)
	}
	var cached TaskStatus
	if err := json.Unmarshal(serialized, &cached); err != nil {
		return "ERROR", nil, fmt.Errorf("failed to unmarshal cached task before cancellation: %w", err)
	}
	if cached.ID != strings.TrimSpace(id) {
		return "ERROR", nil, errors.New("cached task identity does not match cancellation key")
	}
	if !cancellationGrantMatches(&cached, ownerIdentity, token) {
		return "UNAUTHORIZED", &cached, nil
	}

	needsAccountCheck := requiresAccountDeletionCheck(cached.OwnerIdentity, ownerIdentity, requesterType)
	if !needsAccountCheck {
		return r.evalTaskCancellation(ctx, id, ownerIdentity, token, "fallback", cached.OwnerIdentity, false)
	}
	if config.DB == nil {
		return "AUTHORIZATION_UNAVAILABLE", &cached, nil
	}

	var result string
	var task *TaskStatus
	err = config.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		accountAllowed, err := lockActiveAccountForCancellation(ctx, tx, cached.OwnerIdentity, requesterType)
		if err != nil {
			return err
		}
		if !accountAllowed {
			result = "UNAUTHORIZED"
			task = &cached
			return nil
		}
		result, task, err = r.evalTaskCancellation(ctx, id, ownerIdentity, token, "account-locked", cached.OwnerIdentity, true)
		return err
	})
	if err != nil {
		return "ERROR", nil, err
	}
	return result, task, nil
}

func (r *TaskRegistry) evalTaskCancellation(ctx context.Context, id, ownerIdentity, token, authorizationMode, expectedOwner string, requiresAccountCheck bool) (string, *TaskStatus, error) {
	client := r.getClient()
	if client == nil {
		return "ERROR", nil, errors.New("redis client not configured")
	}
	needsAccountCheck := 0
	if requiresAccountCheck {
		needsAccountCheck = 1
	}
	key := TaskKeyPrefix + strings.TrimSpace(id)
	now := time.Now().Unix()
	res, err := client.Eval(ctx, atomicCancelTaskLua, []string{key}, ownerIdentity, token, now, int(TaskTTL.Seconds()), authorizationMode, expectedOwner, needsAccountCheck).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return "NOT_FOUND", nil, nil
		}
		return "ERROR", nil, fmt.Errorf("failed to cancel task in redis: %w", err)
	}
	valStr, ok := res.(string)
	if !ok || valStr == "" {
		return "NOT_FOUND", nil, nil
	}
	var luaRes cancelLuaResult
	if err := json.Unmarshal([]byte(valStr), &luaRes); err != nil {
		return "ERROR", nil, fmt.Errorf("failed to unmarshal cancel response: %w", err)
	}

	var status *TaskStatus
	if luaRes.Payload != "" {
		var s TaskStatus
		if err := json.Unmarshal([]byte(luaRes.Payload), &s); err == nil && s.ID != "" {
			status = &s
		}
	}

	if luaRes.Result == "CANCELLED_SUCCESS" {
		log.Printf("[FORENSIC %s] Task Marked CANCELLED: %s (owner: %s)", time.Now().UTC().Format(time.RFC3339Nano), id, ownerIdentity)
	}

	return luaRes.Result, status, nil
}

func handleGetTaskStatus(c *fiber.Ctx) error {
	id := c.Params("id")
	task, err := Registry.Get(id)
	if err != nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"code":    "TASK_STORAGE_UNAVAILABLE",
			"message": "Task persistence service is temporarily unavailable. Please retry your request.",
		})
	}
	if task == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Task not found"})
	}
	if !canReadTask(c, task) {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"code": "FORBIDDEN", "message": "You are not authorized to view this task."})
	}
	return c.JSON(publicTaskStatus(task))
}

func canReadTask(c *fiber.Ctx, task *TaskStatus) bool {
	requesterID, _ := taskRequesterIdentity(c)
	return isAuthorizedOwner(c, task, requesterID)
}

func taskRequesterIdentity(c *fiber.Ctx) (string, string) {
	requesterID, _ := c.Locals(identity.LocalIdentityIDKey).(string)
	if requesterID == "" {
		requesterID = c.IP()
	}
	requesterType, _ := c.Locals(identity.LocalIdentityType).(string)
	return requesterID, requesterType
}

func handleCancelTask(c *fiber.Ctx) error {
	id := c.Params("id")
	requesterID, _ := c.Locals(identity.LocalIdentityIDKey).(string)
	if requesterID == "" {
		requesterID = c.IP()
	}
	requesterType, _ := c.Locals(identity.LocalIdentityType).(string)

	capability := strings.TrimSpace(c.Get("X-Download-Token"))
	result, task, err := Registry.CancelTaskForIdentity(id, requesterID, requesterType, capability)
	if err != nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"code":    "TASK_STORAGE_UNAVAILABLE",
			"message": "Task persistence service is temporarily unavailable.",
		})
	}

	if result == "NOT_FOUND" {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "Task not found"})
	}

	if result == "UNAUTHORIZED" {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
			"code":    "FORBIDDEN",
			"message": "You are not authorized to cancel this task.",
		})
	}
	if result == "AUTHORIZATION_UNAVAILABLE" || result == "TASK_CHANGED" {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"code":    "TASK_STORAGE_UNAVAILABLE",
			"message": "Task persistence service is temporarily unavailable.",
		})
	}
	if result == "CANCELLED_SUCCESS" && task != nil && task.ReservationID != "" {
		if CancelTaskBillingHandlerWithKind != nil {
			CancelTaskBillingHandlerWithKind(task.ReservationID, task.ReservationKind)
		} else if CancelTaskBillingHandler != nil {
			CancelTaskBillingHandler(task.ReservationID)
		}
	}

	return c.JSON(publicTaskStatus(task))
}
