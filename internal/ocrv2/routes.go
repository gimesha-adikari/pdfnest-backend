package ocrv2

import (
	"pdfnest-backend/internal/billing"
	"pdfnest-backend/internal/idempotency"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/limiter"
	"pdfnest-backend/internal/middleware"
	"pdfnest-backend/internal/uploads"

	"github.com/gofiber/fiber/v2"
)

func RegisterRoutes(router fiber.Router, controller *Controller, stores ...*identity.Store) {
	identityStore := identity.GetStore()
	if len(stores) > 0 && stores[0] != nil {
		identityStore = stores[0]
	}

	// Capabilities are a product-safe projection of installed language choices
	// and processing preferences. They contain no user, storage, or worker data,
	// so guests can render the picker before deciding whether to submit a job.
	router.Get("/v2/ocr/text/capabilities", controller.Capabilities)
	router.Get("/v2/ocr/structured/capabilities", controller.StructuredCapabilities)

	// OCR Text V2 reuses the established guest identity, quota, limiter,
	// idempotency, and owner-scoped result path. Structured extraction below
	// uses the same bounded guest contract; other V2 profiles remain protected.
	textGroup := router.Group("/v2/ocr/text", identity.Resolve(identityStore), bindAuthenticatedIdentity(), uploads.Prepare(), limiter.Default.Middleware())
	textGroup.Post("/jobs", idempotency.UseWithReplay(nil, controller.ReplayJob), billing.Use(billing.ExtractTextPDF), controller.CreateJob)
	textGroup.Get("/jobs/:job_id", controller.JobStatus)
	textGroup.Get("/jobs/:job_id/result", controller.JobResult)
	textGroup.Delete("/jobs/:job_id", controller.CancelJob)

	// Structured extraction has the same bounded PDF input, durable owner checks,
	// and worker resource limits as OCR Text V2. Keep authenticated billing
	// unchanged while applying the existing anonymous quota to guest jobs.
	structuredGroup := router.Group("/v2/ocr", identity.Resolve(identityStore), bindAuthenticatedIdentity(), uploads.Prepare(), limiter.Default.Middleware())
	structuredGroup.Post("/document-extraction-v2/jobs", idempotency.UseWithReplay(nil, controller.ReplayStructuredJob), billing.UseGuestOnly(billing.ExtractTextPDF), controller.CreateStructuredJob)
	structuredGroup.Get("/document-extraction-v2/jobs/:job_id", controller.JobStatus)
	structuredGroup.Get("/document-extraction-v2/jobs/:job_id/result", controller.StructuredJobResult)
	structuredGroup.Delete("/document-extraction-v2/jobs/:job_id", controller.CancelJob)

	group := router.Group("/v2/ocr", middleware.Protect(), bindAuthenticatedIdentity(), uploads.Prepare(), limiter.Default.Middleware())
	group.Post("/text", controller.Text)
	group.Get("/searchable-pdf/capabilities", controller.SearchableCapabilities)
	group.Post("/searchable-pdf/jobs", idempotency.UseWithReplay(nil, controller.ReplaySearchableJob), controller.CreateSearchableJob)
	group.Get("/searchable-pdf/jobs/:job_id", controller.JobStatus)
	group.Get("/searchable-pdf/jobs/:job_id/result", controller.SearchableJobResult)
	group.Delete("/searchable-pdf/jobs/:job_id", controller.CancelJob)
	group.Get("/markup/capabilities", controller.MarkupCapabilities)
	group.Post("/markup/highlight/jobs", idempotency.UseWithReplay(nil, controller.ReplayMarkupJob), controller.CreateMarkupJob)
	group.Post("/markup/underline/jobs", idempotency.UseWithReplay(nil, controller.ReplayMarkupJob), controller.CreateMarkupJob)
	group.Post("/markup/strikeout/jobs", idempotency.UseWithReplay(nil, controller.ReplayMarkupJob), controller.CreateMarkupJob)
	group.Get("/markup/jobs/:job_id", controller.JobStatus)
	group.Get("/markup/jobs/:job_id/result", controller.MarkupJobResult)
	group.Delete("/markup/jobs/:job_id", controller.CancelJob)
	group.Post("/pdf-to-markdown-v2/jobs", idempotency.UseWithReplay(nil, controller.ReplayStructuredJob), controller.CreateStructuredJob)
	group.Get("/pdf-to-markdown-v2/jobs/:job_id", controller.JobStatus)
	group.Get("/pdf-to-markdown-v2/jobs/:job_id/result", controller.StructuredJobResult)
	group.Delete("/pdf-to-markdown-v2/jobs/:job_id", controller.CancelJob)
}

func bindAuthenticatedIdentity() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if userID, ok := c.Locals("user_id").(string); ok && userID != "" {
			c.Locals(identity.LocalIdentityIDKey, userID)
		}
		return c.Next()
	}
}
