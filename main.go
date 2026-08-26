package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"pdfnest-backend/config"
	"pdfnest-backend/internal/admin"
	analyzerApi "pdfnest-backend/internal/analyzer/api"
	analyzerWorker "pdfnest-backend/internal/analyzer/worker"
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
	"pdfnest-backend/internal/studio"
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
	billing.StartJanitorSweeper(15 * time.Minute)

	allowedOrigins := os.Getenv("ALLOWED_ORIGINS")
	if allowedOrigins == "" {
		allowedOrigins = "http://localhost:3000"
	}

	app.Use(cors.New(cors.Config{
		AllowOrigins:     allowedOrigins,
		AllowHeaders:     "Origin, Content-Type, Accept, Authorization, X-Platen-Fingerprint,Idempotency-Key",
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

	analyzerQueueName := os.Getenv("ANALYZER_QUEUE")
	analyzerService := analyzerApi.NewService(config.DB, config.Redis, analyzerQueueName)
	analyzerController := analyzerApi.NewController(analyzerService)
	analyzerApi.RegisterRoutes(toolGroup, analyzerController)

	studioRepo := studio.NewRepository(config.DB)
	studioService := studio.NewService(studioRepo)
	studioCoordinator := studio.NewOperationCoordinator(studioRepo)
	studioRenderer := studio.NewTileRenderer(studioRepo)
	studioController := studio.NewController(studioService, studioCoordinator, studioRenderer)
	studio.RegisterRoutes(toolGroup, studioController)

	// Start background watchdog for stale task reconciliation and worker unavailability monitoring
	watchdogCtx, watchdogCancel := context.WithCancel(context.Background())
	analyzerService.StartWatchdog(watchdogCtx)

	// Automatic Embedded Analyzer Worker Daemon (default: enabled unless ANALYZER_EMBEDDED_WORKER is false)
	enableEmbedded := os.Getenv("ANALYZER_EMBEDDED_WORKER")
	if enableEmbedded != "false" && enableEmbedded != "0" {
		workerCfg := analyzerWorker.DefaultWorkerConfig()
		if analyzerQueueName != "" {
			workerCfg.QueueName = analyzerQueueName
		}
		if baseDir := os.Getenv("ANALYZER_SANDBOX_BASE_DIR"); baseDir != "" {
			workerCfg.SandboxBaseDir = baseDir
		}
		jobQueue, err := analyzerWorker.NewRedisJobQueueWithClient(config.Redis, workerCfg)
		if err != nil {
			log.Printf("[PDFNest Backend] Failed to initialize embedded analyzer job queue: %v", err)
		} else {
			embeddedWorker, err := analyzerWorker.NewAnalyzerWorker(workerCfg, jobQueue)
			if err != nil {
				log.Printf("[PDFNest Backend] Failed to create embedded analyzer worker: %v", err)
			} else {
				workerCtx, workerCancel := context.WithCancel(context.Background())
				go func() {
					log.Printf("[PDFNest Backend] Starting Embedded Analyzer Worker (worker_id=%s, concurrency=%d, queue=%s)...",
						embeddedWorker.WorkerID(), workerCfg.Concurrency, workerCfg.QueueName)
					if err := embeddedWorker.Start(workerCtx); err != nil && err != context.Canceled {
						log.Printf("[PDFNest Backend] Embedded worker stopped with error: %v", err)
					}
				}()
				app.Hooks().OnShutdown(func() error {
					workerCancel()
					watchdogCancel()
					return embeddedWorker.Stop(5 * time.Second)
				})
			}
		}
	} else {
		log.Println("[PDFNest Backend] Embedded Analyzer Worker disabled (ANALYZER_EMBEDDED_WORKER=false). Relying on external worker daemons.")
		app.Hooks().OnShutdown(func() error {
			watchdogCancel()
			return nil
		})
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Platen PDF Engine starting securely on port %s...", port)
	if err := app.Listen(":" + port); err != nil {
		log.Fatalf("Server dynamic socket capture failed: %v", err)
	}
}
