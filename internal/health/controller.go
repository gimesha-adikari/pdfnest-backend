package health

import (
	"os"
	"runtime"
	"time"

	"github.com/gofiber/fiber/v2"
)

var startedAt = time.Now()

type Controller struct{}

func NewController() *Controller {
	return &Controller{}
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

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
