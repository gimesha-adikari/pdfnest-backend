package main

import (
	"log"
	"os"
	"path/filepath"
	"pdfnest-backend/config"
	"pdfnest-backend/internal/admin"
	"pdfnest-backend/internal/auth"
	"pdfnest-backend/internal/billing"
	"pdfnest-backend/internal/contact"
	"pdfnest-backend/internal/content"
	"pdfnest-backend/internal/conversion"
	"pdfnest-backend/internal/edit"
	"pdfnest-backend/internal/health"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/landing"
	"pdfnest-backend/internal/markup"
	"pdfnest-backend/internal/ocr"
	"pdfnest-backend/internal/optimize"
	"pdfnest-backend/internal/security"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/structure"
	"pdfnest-backend/internal/tasks"
	"pdfnest-backend/internal/user"
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

	config.ConnectDB()
	config.ConnectRedis()

	identityStore := identity.NewStore(
		config.Redis,
		90*24*time.Hour,
	)

	billing.Initialize(
		billing.NewGuestQuotaStore(
			config.Redis,
			90*24*time.Hour,
		),
	)

	app := fiber.New(fiber.Config{
		BodyLimit:    100 * 1024 * 1024,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 120 * time.Second,
	})

	app.Use(func(c *fiber.Ctx) error {
		log.Printf(">>> %s %s", c.Method(), c.OriginalURL())
		return c.Next()
	})

	app.Use(recover.New())

	tasks.StartCleanupWorker(5*time.Minute, 30*time.Minute)

	allowedOrigins := os.Getenv("ALLOWED_ORIGINS")
	if allowedOrigins == "" {
		allowedOrigins = "http://localhost:3000"
	}

	app.Use(cors.New(cors.Config{
		AllowOrigins:     allowedOrigins,
		AllowHeaders:     "Origin, Content-Type, Accept, Authorization, X-Platen-Fingerprint",
		AllowMethods:     "GET, POST, PUT, PATCH, DELETE, OPTIONS",
		AllowCredentials: true,
	}))

	app.Use(logger.New(logger.Config{
		Format: "[${time}] ${status} - ${method} ${path} (${latency})\n",
	}))

	landing.RegisterRoutes(app)

	apiGroup := app.Group("/api")

	authService := auth.NewService()
	authController := auth.NewController(authService)
	auth.RegisterRoutes(apiGroup, authController, identityStore)

	adminController := admin.NewController()
	admin.RegisterRoutes(apiGroup, adminController)

	billingController := billing.NewController()
	billing.RegisterRoutes(apiGroup, billingController)

	toolGroup := apiGroup.Group("", identity.Resolve(identityStore))

	tasks.RegisterRoutes(toolGroup)

	securityService := security.NewService()
	securityController := security.NewController(securityService)
	security.RegisterRoutes(toolGroup, securityController)

	optimizeService := optimize.NewService()
	optimizeController := optimize.NewController(optimizeService)
	optimize.RegisterRoutes(toolGroup, optimizeController)

	structureService := structure.NewService()
	structureController := structure.NewController(structureService)
	structure.RegisterRoutes(toolGroup, structureController)

	conversionService := conversion.NewService()
	conversionController := conversion.NewController(conversionService)
	conversion.RegisterRoutes(toolGroup, conversionController)

	ocrService := ocr.NewService()
	ocrController := ocr.NewController(ocrService)
	ocr.RegisterRoutes(toolGroup, ocrController)

	editService := edit.NewService()
	editController := edit.NewController(editService)
	edit.RegisterRoutes(toolGroup, editController)

	markupService := markup.NewService()
	markupController := markup.NewController(markupService)
	markup.RegisterRoutes(toolGroup, markupController)

	contentController := content.NewController()
	content.RegisterRoutes(apiGroup, contentController)

	healthController := health.NewController()
	health.RegisterRoutes(apiGroup, healthController)

	userController := user.NewController()
	user.RegisterRoutes(apiGroup, userController)

	storageController := storage.NewController()
	storage.RegisterRoutes(apiGroup, storageController)

	contactController := contact.NewController()
	contact.RegisterRoutes(apiGroup, contactController)

	contactAdminController := contact.NewAdminController()
	contact.RegisterAdminRoutes(apiGroup, contactAdminController)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Platen PDF Engine starting securely on port %s...", port)
	if err := app.Listen(":" + port); err != nil {
		log.Fatalf("Server dynamic socket capture failed: %v", err)
	}
}
