package landing

import (
	"embed"
	"html/template"
	"os"

	"github.com/gofiber/fiber/v2"
)

//go:embed templates/*.html
var templateFS embed.FS

var landingTemplate = template.Must(
	template.ParseFS(templateFS, "templates/landing.html"),
)

type pageData struct {
	FrontendURL string
	BackendURL  string
}

func landingPage(c *fiber.Ctx) error {
	data := pageData{
		FrontendURL: getEnv("FRONTEND_URL", "http://localhost:3000"),
		BackendURL:  getEnv("BACKEND_URL", "http://localhost:8080"),
	}

	c.Type("html")
	return landingTemplate.Execute(c.Response().BodyWriter(), data)
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
