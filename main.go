package main

import (
	"log"
	"os"
	"pdfnest-backend/internal/edit"
	"time"

	"pdfnest-backend/internal/conversion"
	"pdfnest-backend/internal/ocr"
	"pdfnest-backend/internal/optimize"
	"pdfnest-backend/internal/security"
	"pdfnest-backend/internal/structure"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
)

func main() {
	app := fiber.New(fiber.Config{
		BodyLimit:    100 * 1024 * 1024,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 120 * time.Second,
	})

	app.Use(recover.New())

	allowedOrigins := os.Getenv("ALLOWED_ORIGINS")
	if allowedOrigins == "" {
		allowedOrigins = "http://localhost:3000"
	}

	app.Use(cors.New(cors.Config{
		AllowOrigins:     allowedOrigins,
		AllowHeaders:     "Origin, Content-Type, Accept, Authorization",
		AllowMethods:     "GET, POST, PUT, DELETE, OPTIONS",
		AllowCredentials: true,
	}))

	app.Use(logger.New(logger.Config{
		Format: "[${time}] ${status} - ${method} ${path} (${latency})\n",
	}))

	apiGroup := app.Group("/api")

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

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("PDFNest Engine starting securely on port %s...", port)
	if err := app.Listen(":" + port); err != nil {
		log.Fatalf("Critical engine boot runtime error: %v", err)
	}
}
