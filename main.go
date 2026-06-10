package main

import (
	"pdfnest-backend/internal/conversion"
	"pdfnest-backend/internal/ocr"
	"pdfnest-backend/internal/optimize"
	"pdfnest-backend/internal/security"
	"pdfnest-backend/internal/structure"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
)

func main() {
	app := fiber.New(fiber.Config{
		BodyLimit: 100 * 1024 * 1024,
	})

	app.Use(cors.New())
	app.Use(logger.New())

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

	err := app.Listen(":8080")
	if err != nil {
		return
	}
}
