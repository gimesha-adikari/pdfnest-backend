package health

import (
	"context"
	"log"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

var startedAt = time.Now()

type Controller struct {
	dbPing        func(context.Context) error
	redisPing     func(context.Context) error
	workerProbe   func(context.Context) (string, map[string]bool, error)
	workerProbeMu sync.Mutex
	workerCacheMu sync.Mutex
	workerCache   WorkerReadiness
	workerCacheAt time.Time
	workerCached  bool
}

const workerReadinessCacheTTL = 10 * time.Second
const workerReadinessProbeTimeout = 2500 * time.Millisecond

// WorkerReadiness contains only browser-safe aggregate state and capabilities.
type WorkerReadiness struct {
	Status       string          `json:"status"`
	Capabilities map[string]bool `json:"capabilities"`
}

var workerCapabilities = []string{"document_processing", "ocr", "office_to_pdf", "pdf_to_office"}

func NewController() *Controller {
	return &Controller{workerProbe: probeWorkerReadiness}
}

func NewControllerWithDependencies(db *gorm.DB, redisClient *redis.Client) *Controller {
	controller := &Controller{workerProbe: probeWorkerReadiness}
	if db != nil {
		if sqlDB, err := db.DB(); err == nil {
			controller.dbPing = func(ctx context.Context) error { return sqlDB.PingContext(ctx) }
		}
	}
	if redisClient != nil {
		controller.redisPing = func(ctx context.Context) error { return redisClient.Ping(ctx).Err() }
	}
	return controller
}

// NewControllerWithPingers makes readiness behavior testable without a live
// database or Redis instance.
func NewControllerWithPingers(dbPing func(context.Context) error, redisPing func(context.Context) error) *Controller {
	return &Controller{dbPing: dbPing, redisPing: redisPing}
}

func NewControllerWithWorkerProbe(probe func(context.Context) (string, map[string]bool, error)) *Controller {
	return &Controller{workerProbe: probe}
}

func (h *Controller) Health(c *fiber.Ctx) error {
	hostname, _ := os.Hostname()

	return c.JSON(fiber.Map{
		"status": "healthy",

		"service": fiber.Map{
			"name":        "platen-pdf-backend",
			"description": "Platen PDF Backend API",
			"version":     getEnv("APP_VERSION", "development"),
			"environment": getEnv("APP_ENV", "development"),
		},

		"server": fiber.Map{
			"time":      time.Now().UTC().Format(time.RFC3339),
			"uptime":    time.Since(startedAt).Round(time.Second).String(),
			"hostname":  hostname,
			"goVersion": runtime.Version(),
		},

		"links": fiber.Map{
			"landing":  "/",
			"frontend": os.Getenv("FRONTEND_URL"),
			"api":      "/api",
		},
	})
}

func (h *Controller) Ready(c *fiber.Ctx) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	dependencies := fiber.Map{"database": "ready", "redis": "ready"}
	ready := true
	if h.dbPing == nil || h.dbPing(ctx) != nil {
		dependencies["database"] = "not_ready"
		ready = false
	}
	if h.redisPing == nil || h.redisPing(ctx) != nil {
		dependencies["redis"] = "not_ready"
		ready = false
	}

	status := fiber.StatusOK
	if !ready {
		log.Printf("[BACKEND READINESS] database=%s redis=%s", dependencies["database"], dependencies["redis"])
		status = fiber.StatusServiceUnavailable
	}
	return c.Status(status).JSON(fiber.Map{
		"ready":        ready,
		"status":       map[bool]string{true: "ready", false: "not_ready"}[ready],
		"dependencies": dependencies,
	})
}

func (h *Controller) WorkerReady(c *fiber.Ctx) error {
	readiness := h.workerReadiness(c.Context())
	status := fiber.StatusOK
	if readiness.Status == "not_ready" {
		status = fiber.StatusServiceUnavailable
	}
	return c.Status(status).JSON(readiness)
}

func (h *Controller) workerReadiness(parent context.Context) WorkerReadiness {
	if cached, ok := h.cachedWorkerReadiness(time.Now()); ok {
		return cached
	}
	if h.workerProbe == nil || !h.workerProbeMu.TryLock() {
		return unknownWorkerReadiness()
	}
	defer h.workerProbeMu.Unlock()
	if cached, ok := h.cachedWorkerReadiness(time.Now()); ok {
		return cached
	}

	ctx, cancel := context.WithTimeout(parent, workerReadinessProbeTimeout)
	defer cancel()
	status, rawCapabilities, err := h.workerProbe(ctx)
	if err != nil || (status != "ready" && status != "not_ready") {
		readiness := unknownWorkerReadiness()
		h.storeWorkerReadiness(readiness, time.Now())
		return readiness
	}
	capabilities := make(map[string]bool, len(workerCapabilities))
	for _, capability := range workerCapabilities {
		capabilities[capability] = rawCapabilities[capability]
	}
	readiness := WorkerReadiness{Status: status, Capabilities: capabilities}
	h.storeWorkerReadiness(readiness, time.Now())
	return readiness
}

func (h *Controller) cachedWorkerReadiness(now time.Time) (WorkerReadiness, bool) {
	h.workerCacheMu.Lock()
	defer h.workerCacheMu.Unlock()
	if !h.workerCached || now.Sub(h.workerCacheAt) >= workerReadinessCacheTTL {
		return WorkerReadiness{}, false
	}
	return cloneWorkerReadiness(h.workerCache), true
}

func (h *Controller) storeWorkerReadiness(readiness WorkerReadiness, at time.Time) {
	h.workerCacheMu.Lock()
	defer h.workerCacheMu.Unlock()
	h.workerCache = cloneWorkerReadiness(readiness)
	h.workerCacheAt = at
	h.workerCached = true
}

func cloneWorkerReadiness(readiness WorkerReadiness) WorkerReadiness {
	capabilities := make(map[string]bool, len(readiness.Capabilities))
	for key, value := range readiness.Capabilities {
		capabilities[key] = value
	}
	return WorkerReadiness{Status: readiness.Status, Capabilities: capabilities}
}

func unknownWorkerReadiness() WorkerReadiness {
	capabilities := make(map[string]bool, len(workerCapabilities))
	for _, capability := range workerCapabilities {
		capabilities[capability] = false
	}
	return WorkerReadiness{Status: "unknown", Capabilities: capabilities}
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
