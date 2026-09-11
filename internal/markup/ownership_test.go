package markup

import (
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/storage"
	"testing"
)

type ownershipService struct {
	Service
	record *WorkerJobRecord
}

func (s *ownershipService) GetJobStatus(string) (*WorkerJobRecord, error) { return s.record, nil }
func TestMarkupDeniesForeignJobAndFileReads(t *testing.T) {
	key := storage.NewOwnedKey("owner", "markup_source", ".pdf")
	svc := &ownershipService{record: &WorkerJobRecord{Payload: map[string]any{"source_key": key}}}
	ctrl := NewController(svc)
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityKey, identity.Identity{ID: c.Get("X-Owner"), Type: identity.TypeGuest})
		return c.Next()
	})
	app.Get("/jobs/:job_id", ctrl.HandleJobStatus)
	app.Get("/jobs/:job_id/download", ctrl.HandleJobDownload)
	app.Get("/file", ctrl.HandleGetFile)
	for _, endpoint := range []string{"/jobs/id", "/jobs/id/download", "/file?path=" + key} {
		req := httptest.NewRequest("GET", endpoint, nil)
		req.Header.Set("X-Owner", "foreign")
		resp, err := app.Test(req)
		require.NoError(t, err)
		require.Equal(t, 403, resp.StatusCode)
		resp.Body.Close()
	}
	req := httptest.NewRequest("GET", "/jobs/id", nil)
	req.Header.Set("X-Owner", "owner")
	resp, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
	resp.Body.Close()
}
