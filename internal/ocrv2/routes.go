package ocrv2

import (
	"pdfnest-backend/internal/idempotency"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/limiter"
	"pdfnest-backend/internal/middleware"
	"pdfnest-backend/internal/uploads"

	"github.com/gofiber/fiber/v2"
)

func RegisterRoutes(router fiber.Router, controller *Controller) {
	group := router.Group("/v2/ocr", middleware.Protect(), bindAuthenticatedIdentity(), uploads.Prepare(), limiter.Default.Middleware())
	group.Post("/text", controller.Text)
	group.Get("/text/capabilities", controller.Capabilities)
	group.Post("/text/jobs", idempotency.UseWithReplay(nil, controller.ReplayJob), controller.CreateJob)
	group.Get("/text/jobs/:job_id", controller.JobStatus)
	group.Get("/text/jobs/:job_id/result", controller.JobResult)
	group.Delete("/text/jobs/:job_id", controller.CancelJob)
	group.Get("/searchable-pdf/capabilities", controller.SearchableCapabilities)
	group.Post("/searchable-pdf/jobs", idempotency.UseWithReplay(nil, controller.ReplaySearchableJob), controller.CreateSearchableJob)
	group.Get("/searchable-pdf/jobs/:job_id", controller.JobStatus)
	group.Get("/searchable-pdf/jobs/:job_id/result", controller.SearchableJobResult)
	group.Delete("/searchable-pdf/jobs/:job_id", controller.CancelJob)
	group.Get("/structured/capabilities", controller.StructuredCapabilities)
	group.Post("/document-extraction-v2/jobs", idempotency.UseWithReplay(nil, controller.ReplayStructuredJob), controller.CreateStructuredJob)
	group.Get("/document-extraction-v2/jobs/:job_id", controller.JobStatus)
	group.Get("/document-extraction-v2/jobs/:job_id/result", controller.StructuredJobResult)
	group.Delete("/document-extraction-v2/jobs/:job_id", controller.CancelJob)
	group.Post("/pdf-to-markdown-v2/jobs", idempotency.UseWithReplay(nil, controller.ReplayStructuredJob), controller.CreateStructuredJob)
	group.Get("/pdf-to-markdown-v2/jobs/:job_id", controller.JobStatus)
	group.Get("/pdf-to-markdown-v2/jobs/:job_id/result", controller.StructuredJobResult)
	group.Delete("/pdf-to-markdown-v2/jobs/:job_id", controller.CancelJob)
	group.Get("/markup/capabilities", controller.MarkupCapabilities)
	group.Post("/markup/highlight/jobs", idempotency.UseWithReplay(nil, controller.ReplayMarkupJob), controller.CreateMarkupJob)
	group.Post("/markup/underline/jobs", idempotency.UseWithReplay(nil, controller.ReplayMarkupJob), controller.CreateMarkupJob)
	group.Post("/markup/strikeout/jobs", idempotency.UseWithReplay(nil, controller.ReplayMarkupJob), controller.CreateMarkupJob)
	group.Get("/markup/jobs/:job_id", controller.JobStatus)
	group.Get("/markup/jobs/:job_id/result", controller.MarkupJobResult)
	group.Delete("/markup/jobs/:job_id", controller.CancelJob)
}

func bindAuthenticatedIdentity() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if userID, ok := c.Locals("user_id").(string); ok && userID != "" {
			c.Locals(identity.LocalIdentityIDKey, userID)
		}
		return c.Next()
	}
}
