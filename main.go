package main

import (
	"log"
	"os"
	"path/filepath"
	"pdfnest-backend/config"
	"pdfnest-backend/internal/admin"
	"pdfnest-backend/internal/auth"
	"pdfnest-backend/internal/billing"
	"pdfnest-backend/internal/content"
	"pdfnest-backend/internal/conversion"
	"pdfnest-backend/internal/edit"
	"pdfnest-backend/internal/health"
	"pdfnest-backend/internal/landing"
	"pdfnest-backend/internal/markup"
	"pdfnest-backend/internal/ocr"
	"pdfnest-backend/internal/optimize"
	"pdfnest-backend/internal/security"
	"pdfnest-backend/internal/structure"
	"pdfnest-backend/internal/tasks"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"

	"github.com/joho/godotenv"
)

func main() {
	dir, err := os.Getwd()
	if err == nil {
		log.Printf("[DEBUG] Current working directory of the process is: %s", dir)
		log.Printf("[DEBUG] Expecting .env file to be here: %s", filepath.Join(dir, ".env"))
	}

	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found; using Render environment variables.")
	}

	config.ConnectDB()
	content.SeedSiteContent()

	app := fiber.New(fiber.Config{
		BodyLimit:    100 * 1024 * 1024,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 120 * time.Second,
	})

	app.Use(recover.New())

	tasks.StartCleanupWorker(5*time.Minute, 30*time.Minute)

	allowedOrigins := os.Getenv("ALLOWED_ORIGINS")
	if allowedOrigins == "" {
		allowedOrigins = "http://localhost:3000"
	}

	app.Use(cors.New(cors.Config{
		AllowOrigins:     allowedOrigins,
		AllowHeaders:     "Origin, Content-Type, Accept, Authorization",
		AllowMethods:     "GET, POST, PUT, PATCH, DELETE, OPTIONS",
		AllowCredentials: true,
	}))

	app.Use(logger.New(logger.Config{
		Format: "[${time}] ${status} - ${method} ${path} (${latency})\n",
	}))

	tasks.RegisterRoutes(app)

	landing.RegisterRoutes(app)

	apiGroup := app.Group("/api")

	// Core Identity Infrastructure Package Mounting Domain Blocks
	authService := auth.NewService()
	authController := auth.NewController(authService)
	auth.RegisterRoutes(apiGroup, authController)

	adminController := admin.NewController()
	admin.RegisterRoutes(apiGroup, adminController)

	billingController := billing.NewController()
	billing.RegisterRoutes(apiGroup, billingController)

	// Domain 1: Security (Lock/Unlock)
	securityService := security.NewService()
	securityController := security.NewController(securityService)
	security.RegisterRoutes(apiGroup, securityController)

	// Domain 2: Optimization (Compress)
	optimizeService := optimize.NewService()
	optimizeController := optimize.NewController(optimizeService)
	optimize.RegisterRoutes(apiGroup, optimizeController)

	// Domain 3: Document Structure (Merge/Split/Delete)
	structureService := structure.NewService()
	structureController := structure.NewController(structureService)
	structure.RegisterRoutes(apiGroup, structureController)

	// Domain 4: Document Conversion (PDF to Img / Img to PDF)
	conversionService := conversion.NewService()
	conversionController := conversion.NewController(conversionService)
	conversion.RegisterRoutes(apiGroup, conversionController)

	// Domain 5: Extraction (PDF to Text)
	ocrService := ocr.NewService()
	ocrController := ocr.NewController(ocrService)
	ocr.RegisterRoutes(apiGroup, ocrController)

	// Domain 6: Edit (PDF edit)
	editService := edit.NewService()
	editController := edit.NewController(editService)
	edit.RegisterRoutes(apiGroup, editController)

	// Domain 7: Markup (Highlight / Underline / Strikeout)
	markupService := markup.NewService()
	markupController := markup.NewController(markupService)
	markup.RegisterRoutes(apiGroup, markupController)

	contentController := content.NewController()
	content.RegisterRoutes(apiGroup, contentController)

	healthController := health.NewController()
	health.RegisterRoutes(apiGroup, healthController)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Platen PDF Engine starting securely on port %s...", port)
	if err := app.Listen(":" + port); err != nil {
		log.Fatalf("Server dynamic socket capture failed: %v", err)
	}
}
