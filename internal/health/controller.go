package health

import (
	"context"
	"log"
	"os"
	"runtime"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

var startedAt = time.Now()

type Controller struct {
	dbPing    func(context.Context) error
	redisPing func(context.Context) error
}

func NewController() *Controller {
	return &Controller{}
}

func NewControllerWithDependencies(db *gorm.DB, redisClient *redis.Client) *Controller {
	controller := &Controller{}
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

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
