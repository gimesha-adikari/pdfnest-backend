package limiter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"pdfnest-backend/config"
	"pdfnest-backend/internal/identity"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

var ErrCapacityExhausted = errors.New("server processing capacity reached")
var ErrUserCapacityExhausted = errors.New("user processing capacity reached")

const (
	DefaultGlobalLimit   = 4
	DefaultIdentityLimit = 2
	DefaultLeaseTTL      = 600 // 10 minutes

	LeaseKeyPrefix       = "pdfnest:limiter:lease:"
	ActiveGlobalKey      = "pdfnest:limiter:active"
	ActiveIdentityPrefix = "pdfnest:limiter:identity:"
)

const atomicAcquireLua = `
local leaseKey = KEYS[1]
local activeKey = KEYS[2]
local identityKey = KEYS[3]

local taskId = ARGV[1]
local identityId = ARGV[2]
local globalCap = tonumber(ARGV[3])
local identityCap = tonumber(ARGV[4])
local ttlSeconds = tonumber(ARGV[5]) or 600
local now = tonumber(ARGV[6])
local expiresAt = now + ttlSeconds

local existingLease = redis.call('GET', leaseKey)
if existingLease and existingLease ~= '' then
    local data = cjson.decode(existingLease)
    if data and data.taskId == taskId then
        redis.call('EXPIRE', leaseKey, ttlSeconds)
        redis.call('ZADD', activeKey, expiresAt, taskId)
        redis.call('ZADD', identityKey, expiresAt, taskId)
        return cjson.encode({ status = "ACCEPTED", reason = "ALREADY_ACQUIRED", expiresAt = expiresAt })
    end
end

redis.call('ZREMRANGEBYSCORE', activeKey, '-inf', '(' .. now)
redis.call('ZREMRANGEBYSCORE', identityKey, '-inf', '(' .. now)

local globalCount = redis.call('ZCARD', activeKey)
if globalCount >= globalCap then
    return cjson.encode({ status = "REJECTED", reason = "GLOBAL_CAPACITY_EXHAUSTED", active = globalCount, max = globalCap })
end

local identityCount = redis.call('ZCARD', identityKey)
if identityCount >= identityCap then
    return cjson.encode({ status = "REJECTED", reason = "IDENTITY_CAPACITY_EXHAUSTED", active = identityCount, max = identityCap })
end

local leaseData = cjson.encode({
    taskId = taskId,
    identityId = identityId,
    acquiredAt = now,
    expiresAt = expiresAt
})

redis.call('SET', leaseKey, leaseData, 'EX', ttlSeconds)
redis.call('ZADD', activeKey, expiresAt, taskId)
redis.call('ZADD', identityKey, expiresAt, taskId)

return cjson.encode({ status = "ACCEPTED", reason = "ACQUIRED", expiresAt = expiresAt })
`

const atomicReleaseLua = `
local leaseKey = KEYS[1]
local activeKey = KEYS[2]
local identityKey = KEYS[3]

local taskId = ARGV[1]
local identityId = ARGV[2]

local existingLease = redis.call('GET', leaseKey)
if existingLease and existingLease ~= '' then
    local data = cjson.decode(existingLease)
    if data and data.taskId == taskId then
        redis.call('DEL', leaseKey)
        redis.call('ZREM', activeKey, taskId)
        redis.call('ZREM', identityKey, taskId)
        return cjson.encode({ status = "RELEASED" })
    else
        return cjson.encode({ status = "MISMATCH" })
    end
end

redis.call('ZREM', activeKey, taskId)
redis.call('ZREM', identityKey, taskId)
return cjson.encode({ status = "NO_OP" })
`

const atomicRenewLua = `
local leaseKey = KEYS[1]
local activeKey = KEYS[2]
local identityKey = KEYS[3]

local taskId = ARGV[1]
local identityId = ARGV[2]
local ttlSeconds = tonumber(ARGV[3]) or 600
local now = tonumber(ARGV[4])
local expiresAt = now + ttlSeconds

local existingLease = redis.call('GET', leaseKey)
if not existingLease or existingLease == '' then
    return cjson.encode({ status = "EXPIRED" })
end

local data = cjson.decode(existingLease)
if not data or data.taskId ~= taskId then
    return cjson.encode({ status = "MISMATCH" })
end

data.expiresAt = expiresAt
data.renewedAt = now
local newData = cjson.encode(data)

redis.call('SET', leaseKey, newData, 'EX', ttlSeconds)
redis.call('ZADD', activeKey, expiresAt, taskId)
redis.call('ZADD', identityKey, expiresAt, taskId)

return cjson.encode({ status = "RENEWED", expiresAt = expiresAt })
`

type AcquireResult struct {
	Status    string `json:"status"`
	Reason    string `json:"reason"`
	Active    int    `json:"active"`
	Max       int    `json:"max"`
	ExpiresAt int64  `json:"expiresAt"`
}

type Governor struct {
	client          *redis.Client
	globalLimit     int
	identityLimit   int
	leaseTTLSeconds int
	mu              sync.Mutex
	fallbackActive  int
}

func GetEnvInt(key string, defaultValue int) int {
	valStr := strings.TrimSpace(os.Getenv(key))
	if valStr == "" {
		return defaultValue
	}
	val, err := strconv.Atoi(valStr)
	if err != nil || val <= 0 {
		return defaultValue
	}
	return val
}

func NewGovernor() *Governor {
	globalCap := GetEnvInt("GLOBAL_HEAVY_EXECUTION_LIMIT", GetEnvInt("MAX_CONCURRENT_HEAVY_JOBS", DefaultGlobalLimit))
	identityCap := GetEnvInt("PER_IDENTITY_HEAVY_EXECUTION_LIMIT", DefaultIdentityLimit)
	leaseTTL := GetEnvInt("HEAVY_LEASE_TTL_SECONDS", DefaultLeaseTTL)

	return &Governor{
		client:          nil,
		globalLimit:     globalCap,
		identityLimit:   identityCap,
		leaseTTLSeconds: leaseTTL,
	}
}

func NewGovernorWithCapacity(capacity int) *Governor {
	if capacity <= 0 {
		capacity = DefaultGlobalLimit
	}
	return &Governor{
		client:          nil,
		globalLimit:     capacity,
		identityLimit:   DefaultIdentityLimit,
		leaseTTLSeconds: DefaultLeaseTTL,
	}
}

var Default = NewGovernor()

func (g *Governor) getClient() *redis.Client {
	if g.client != nil {
		return g.client
	}
	return config.Redis
}

func (g *Governor) Acquire(ctx context.Context, taskID string, identityID string) (bool, string, error) {
	client := g.getClient()
	taskID = strings.TrimSpace(taskID)
	identityID = strings.TrimSpace(identityID)

	if taskID == "" {
		taskID = "task-" + uuid.New().String()
	}
	if identityID == "" {
		identityID = "guest:anonymous"
	}

	if client == nil {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.fallbackActive >= g.globalLimit {
			return false, "GLOBAL_CAPACITY_EXHAUSTED", nil
		}
		g.fallbackActive++
		return true, "ACQUIRED_FALLBACK", nil
	}

	leaseKey := LeaseKeyPrefix + taskID
	activeKey := ActiveGlobalKey
	identityKey := ActiveIdentityPrefix + identityID
	now := time.Now().Unix()

	res, err := client.Eval(
		ctx,
		atomicAcquireLua,
		[]string{leaseKey, activeKey, identityKey},
		taskID,
		identityID,
		g.globalLimit,
		g.identityLimit,
		g.leaseTTLSeconds,
		now,
	).Result()

	if err != nil {
		log.Printf("[LIMITER ERROR] Redis Eval failed for task %s (identity %s): %v", taskID, identityID, err)
		return false, "REDIS_ERROR", err
	}

	resStr, ok := res.(string)
	if !ok || resStr == "" {
		return false, "MALFORMED_REDIS_RESPONSE", fmt.Errorf("unexpected lua result format: %v", res)
	}

	var result AcquireResult
	if err := json.Unmarshal([]byte(resStr), &result); err != nil {
		return false, "UNMARSHAL_ERROR", err
	}

	if result.Status == "ACCEPTED" {
		log.Printf("[LIMITER SUCCESS] Lease acquired for task %s (identity: %s, reason: %s)", taskID, identityID, result.Reason)
		return true, result.Reason, nil
	}

	log.Printf("[LIMITER REJECTED] Task %s rejected for identity %s (reason: %s)", taskID, identityID, result.Reason)
	return false, result.Reason, nil
}

func (g *Governor) Release(ctx context.Context, taskID string, identityID string) error {
	client := g.getClient()
	taskID = strings.TrimSpace(taskID)
	identityID = strings.TrimSpace(identityID)

	if taskID == "" {
		return nil
	}
	if identityID == "" {
		identityID = "guest:anonymous"
	}

	if client == nil {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.fallbackActive > 0 {
			g.fallbackActive--
		}
		return nil
	}

	leaseKey := LeaseKeyPrefix + taskID
	activeKey := ActiveGlobalKey
	identityKey := ActiveIdentityPrefix + identityID

	res, err := client.Eval(
		ctx,
		atomicReleaseLua,
		[]string{leaseKey, activeKey, identityKey},
		taskID,
		identityID,
	).Result()

	if err != nil {
		log.Printf("[LIMITER ERROR] Redis Release failed for task %s: %v", taskID, err)
		return err
	}

	_ = res
	log.Printf("[LIMITER RELEASED] Lease released for task %s (identity: %s)", taskID, identityID)
	return nil
}

func (g *Governor) Renew(ctx context.Context, taskID string, identityID string) (bool, error) {
	client := g.getClient()
	taskID = strings.TrimSpace(taskID)
	identityID = strings.TrimSpace(identityID)

	if taskID == "" {
		return false, errors.New("empty taskID")
	}
	if identityID == "" {
		identityID = "guest:anonymous"
	}

	if client == nil {
		return true, nil
	}

	leaseKey := LeaseKeyPrefix + taskID
	activeKey := ActiveGlobalKey
	identityKey := ActiveIdentityPrefix + identityID
	now := time.Now().Unix()

	res, err := client.Eval(
		ctx,
		atomicRenewLua,
		[]string{leaseKey, activeKey, identityKey},
		taskID,
		identityID,
		g.leaseTTLSeconds,
		now,
	).Result()

	if err != nil {
		return false, err
	}

	resStr, ok := res.(string)
	if !ok || resStr == "" {
		return false, nil
	}

	var result struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(resStr), &result); err == nil && result.Status == "RENEWED" {
		return true, nil
	}

	return false, nil
}

func (g *Governor) AcquireWithRelease(ctx context.Context, taskID string, identityID string) (func(), bool, error) {
	acquired, reason, err := g.Acquire(ctx, taskID, identityID)
	if err != nil || !acquired {
		return func() {}, false, err
	}

	var once sync.Once
	releaseFunc := func() {
		once.Do(func() {
			relCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = g.Release(relCtx, taskID, identityID)
		})
	}
	_ = reason
	return releaseFunc, true, nil
}

func (g *Governor) TryAcquire() (func(), bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	taskID := "legacy-" + uuid.New().String()
	release, ok, err := g.AcquireWithRelease(ctx, taskID, "guest:anonymous")
	if err != nil || !ok {
		return func() {}, false
	}
	return release, true
}

func (g *Governor) ActiveCount() int {
	client := g.getClient()
	if client == nil {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.fallbackActive
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	now := time.Now().Unix()
	_ = client.ZRemRangeByScore(ctx, ActiveGlobalKey, "-inf", fmt.Sprintf("(%d", now)).Err()
	count, err := client.ZCard(ctx, ActiveGlobalKey).Result()
	if err != nil {
		return 0
	}
	return int(count)
}

func (g *Governor) Capacity() int {
	return g.globalLimit
}

func (g *Governor) Middleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		identityID, _ := c.Locals(identity.LocalIdentityIDKey).(string)
		identityID = strings.TrimSpace(identityID)
		if identityID == "" {
			identityID = "guest:" + c.IP()
		}

		syncTaskID := "sync-" + uuid.New().String()
		release, ok, err := g.AcquireWithRelease(c.Context(), syncTaskID, identityID)

		if err != nil {
			log.Printf("[LIMITER FAIL-CLOSED] Redis error during admission check: %v", err)
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"code":    "TASK_STORAGE_UNAVAILABLE",
				"message": "Task capacity storage service is temporarily unavailable. Please retry.",
			})
		}

		if !ok {
			c.Set("Retry-After", "5")
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"code":        "SERVER_BUSY",
				"message":     "Server processing capacity reached. Please try again in a few seconds.",
				"retry_after": 5,
			})
		}

		defer release()
		return c.Next()
	}
}
