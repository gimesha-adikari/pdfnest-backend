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
	// Status, result, and cancellation are owner-scoped control-plane reads;
	// they must not consume a second heavy execution lease while a worker job is
	// running. Execution admission is attached only to compute POST routes.
	textIdentity := identity.Resolve(identityStore)
	textBindIdentity := bindAuthenticatedIdentity()
	router.Post("/v2/ocr/text/jobs", textIdentity, textBindIdentity, uploads.Prepare(), limiter.Default.Middleware(), idempotency.UseWithReplay(nil, controller.ReplayJob), billing.Use(billing.ExtractTextPDF), controller.CreateJob)
	router.Get("/v2/ocr/text/jobs/:job_id", textIdentity, textBindIdentity, controller.JobStatus)
	router.Get("/v2/ocr/text/jobs/:job_id/result", textIdentity, textBindIdentity, controller.JobResult)
	router.Delete("/v2/ocr/text/jobs/:job_id", textIdentity, textBindIdentity, controller.CancelJob)

	// Structured extraction has the same bounded PDF input, durable owner checks,
	// and worker resource limits as OCR Text V2. Keep authenticated billing
	// unchanged while applying the existing anonymous quota to guest jobs.
	structuredIdentity := identity.Resolve(identityStore)
	structuredBindIdentity := bindAuthenticatedIdentity()
	router.Post("/v2/ocr/document-extraction-v2/jobs", structuredIdentity, structuredBindIdentity, uploads.Prepare(), limiter.Default.Middleware(), idempotency.UseWithReplay(nil, controller.ReplayStructuredJob), billing.UseGuestOnly(billing.ExtractTextPDF), controller.CreateStructuredJob)
	router.Get("/v2/ocr/document-extraction-v2/jobs/:job_id", structuredIdentity, structuredBindIdentity, controller.JobStatus)
	router.Get("/v2/ocr/document-extraction-v2/jobs/:job_id/result", structuredIdentity, structuredBindIdentity, controller.StructuredJobResult)
	router.Delete("/v2/ocr/document-extraction-v2/jobs/:job_id", structuredIdentity, structuredBindIdentity, controller.CancelJob)

	// Searchable PDF capabilities contain only safe product metadata and are
	// public so guests can configure the workspace before submitting.
	router.Get("/v2/ocr/searchable-pdf/capabilities", controller.SearchableCapabilities)
	searchableIdentity := identity.Resolve(identityStore)
	searchableBindIdentity := bindAuthenticatedIdentity()
	router.Post("/v2/ocr/searchable-pdf/jobs", searchableIdentity, searchableBindIdentity, uploads.Prepare(), limiter.Default.Middleware(), idempotency.UseWithReplay(nil, controller.ReplaySearchableJob), billing.UseGuestOnly(billing.ImageToTextPDF), controller.CreateSearchableJob)
	router.Get("/v2/ocr/searchable-pdf/jobs/:job_id", searchableIdentity, searchableBindIdentity, controller.JobStatus)
	router.Get("/v2/ocr/searchable-pdf/jobs/:job_id/result", searchableIdentity, searchableBindIdentity, controller.SearchableJobResult)
	router.Delete("/v2/ocr/searchable-pdf/jobs/:job_id", searchableIdentity, searchableBindIdentity, controller.CancelJob)
	router.Get("/v2/ocr/markup/capabilities", controller.MarkupCapabilities)

	protected := middleware.Protect()
	protectedBindIdentity := bindAuthenticatedIdentity()
	protectedExecution := limiter.Default.Middleware()
	protectedUpload := uploads.Prepare()
	router.Post("/v2/ocr/text", protected, protectedBindIdentity, protectedUpload, protectedExecution, controller.Text)
	router.Post("/v2/ocr/markup/highlight/jobs", protected, protectedBindIdentity, protectedUpload, protectedExecution, idempotency.UseWithReplay(nil, controller.ReplayMarkupJob), controller.CreateMarkupJob)
	router.Post("/v2/ocr/markup/underline/jobs", protected, protectedBindIdentity, protectedUpload, protectedExecution, idempotency.UseWithReplay(nil, controller.ReplayMarkupJob), controller.CreateMarkupJob)
	router.Post("/v2/ocr/markup/strikeout/jobs", protected, protectedBindIdentity, protectedUpload, protectedExecution, idempotency.UseWithReplay(nil, controller.ReplayMarkupJob), controller.CreateMarkupJob)
	router.Get("/v2/ocr/markup/jobs/:job_id", protected, protectedBindIdentity, controller.JobStatus)
	router.Get("/v2/ocr/markup/jobs/:job_id/result", protected, protectedBindIdentity, controller.MarkupJobResult)
	router.Delete("/v2/ocr/markup/jobs/:job_id", protected, protectedBindIdentity, controller.CancelJob)
	router.Post("/v2/ocr/pdf-to-markdown-v2/jobs", protected, protectedBindIdentity, protectedUpload, protectedExecution, idempotency.UseWithReplay(nil, controller.ReplayStructuredJob), controller.CreateStructuredJob)
	router.Get("/v2/ocr/pdf-to-markdown-v2/jobs/:job_id", protected, protectedBindIdentity, controller.JobStatus)
	router.Get("/v2/ocr/pdf-to-markdown-v2/jobs/:job_id/result", protected, protectedBindIdentity, controller.StructuredJobResult)
	router.Delete("/v2/ocr/pdf-to-markdown-v2/jobs/:job_id", protected, protectedBindIdentity, controller.CancelJob)
}

func bindAuthenticatedIdentity() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if userID, ok := c.Locals("user_id").(string); ok && userID != "" {
			c.Locals(identity.LocalIdentityIDKey, userID)
		}
		return c.Next()
	}
}
