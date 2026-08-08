package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"pdfnest-backend/config"
	"pdfnest-backend/internal/limiter"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
)

const (
	TaskKeyPrefix           = "pdfnest:tasks:"
	TaskTTL                 = 1 * time.Hour
	DefaultStuckTaskTimeout = 900 // 15 minutes default
)

type TaskStatus struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	Progress      int    `json:"progress"`
	ResultKey     string `json:"resultKey,omitempty"`
	ResultURL     string `json:"resultUrl,omitempty"`
	OwnerIdentity string `json:"ownerIdentity,omitempty"`
	ReservationID string `json:"reservationId,omitempty"`
	Error         string `json:"error,omitempty"`
	UpdatedAt     int64  `json:"updatedAt,omitempty"`
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

    if curStatus == 'COMPLETED' or curStatus == 'FAILED' then
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

if curStatus == 'COMPLETED' or curStatus == 'FAILED' then
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

type staleLuaResult struct {
	Result        string `json:"result"`
	ReservationID string `json:"reservationId"`
	Payload       string `json:"payload"`
}

var StaleTaskBillingHandler func(reservationID string)

func (r *TaskRegistry) GetWithTransition(id string) (*TaskStatus, bool, string, error) {
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
		return nil, false, "", nil
	}

	var status TaskStatus
	if err := json.Unmarshal([]byte(luaRes.Payload), &status); err != nil {
		return nil, false, "", fmt.Errorf("failed to unmarshal task status payload JSON: %w", err)
	}

	stalePerformed := (luaRes.Result == "STALE_TRANSITION_PERFORMED")
	if stalePerformed {
		if luaRes.ReservationID != "" && StaleTaskBillingHandler != nil {
			StaleTaskBillingHandler(luaRes.ReservationID)
		}
		_ = limiter.Default.Release(ctx, status.ID, status.OwnerIdentity)
	}

	return &status, stalePerformed, luaRes.ReservationID, nil
}

func (r *TaskRegistry) Get(id string) (*TaskStatus, error) {
	task, _, _, err := r.GetWithTransition(id)
	return task, err
}

func (r *TaskRegistry) SetWithKey(id string, status string, progress int, resultKey string, errStr string, ownerIdentity string, reservationID ...string) (bool, error) {
	client := r.getClient()
	if client == nil {
		log.Printf("[TASK REGISTRY ERROR] Redis client not configured for Set task %s", id)
		return false, errors.New("redis client not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	key := TaskKeyPrefix + strings.TrimSpace(id)

	var resID string
	if len(reservationID) > 0 && reservationID[0] != "" {
		resID = reservationID[0]
	} else if existingVal, err := client.Get(ctx, key).Result(); err == nil && existingVal != "" {
		var existingTask TaskStatus
		if err := json.Unmarshal([]byte(existingVal), &existingTask); err == nil && existingTask.ReservationID != "" {
			resID = existingTask.ReservationID
		}
	}

	task := &TaskStatus{
		ID:            id,
		Status:        status,
		Progress:      progress,
		ResultKey:     resultKey,
		Error:         errStr,
		UpdatedAt:     time.Now().Unix(),
		OwnerIdentity: ownerIdentity,
		ReservationID: resID,
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

func (r *TaskRegistry) Set(id string, status string, progress int, resultURL string, errStr string) error {
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
	if existingVal, err := client.Get(ctx, key).Result(); err == nil && existingVal != "" {
		var existingTask TaskStatus
		if err := json.Unmarshal([]byte(existingVal), &existingTask); err == nil {
			ownerIdentity = existingTask.OwnerIdentity
			reservationID = existingTask.ReservationID
			if resultKey == "" && existingTask.ResultKey != "" {
				resultKey = existingTask.ResultKey
			}
		}
	}

	task := &TaskStatus{
		ID:            id,
		Status:        status,
		Progress:      progress,
		ResultKey:     resultKey,
		ResultURL:     resultURL,
		Error:         errStr,
		UpdatedAt:     time.Now().Unix(),
		OwnerIdentity: ownerIdentity,
		ReservationID: reservationID,
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

func getTaskProgress(id string) *TaskStatus {
	task, err := Registry.Get(id)
	if err != nil {
		return &TaskStatus{ID: id, Status: "FAILED", Progress: 0, Error: "Task storage service unavailable"}
	}
	if task == nil {
		return &TaskStatus{ID: id, Status: "FAILED", Progress: 0, Error: "Task not found"}
	}
	return task
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
	return c.JSON(task)
}
