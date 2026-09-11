package edit

import (
	"bytes"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/storage"
	"testing"
)

type ownershipService struct {
	Service
	record    *WorkerJobRecord
	downloads int
}

func (s *ownershipService) GetJobStatus(string) (*WorkerJobRecord, error) { return s.record, nil }
func (s *ownershipService) GetJobDownload(string) (*http.Response, error) {
	s.downloads++
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewBufferString("%PDF-result"))}, nil
}
func TestEditorRejectsForeignJobsAndSourceReferences(t *testing.T) {
	key := storage.NewOwnedKey("owner", "editor_source", ".pdf")
	svc := &ownershipService{record: &WorkerJobRecord{Payload: map[string]any{"source_key": key}}}
	ctrl := NewController(svc)
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityKey, identity.Identity{ID: c.Get("X-Owner"), Type: identity.TypeGuest})
		return c.Next()
	})
	app.Get("/jobs/:job_id", ctrl.HandleJobStatus)
	app.Get("/jobs/:job_id/download", ctrl.HandleJobDownload)
	app.Post("/compile", ctrl.HandleCompilePDF)
	app.Get("/file", ctrl.HandleGetFile)
	for _, endpoint := range []string{"/jobs/id", "/jobs/id/download", "/file?path=" + key} {
		req := httptest.NewRequest("GET", endpoint, nil)
		req.Header.Set("X-Owner", "foreign")
		resp, err := app.Test(req)
		require.NoError(t, err)
		require.Equal(t, 403, resp.StatusCode)
		resp.Body.Close()
	}
	req := httptest.NewRequest("POST", "/compile", bytes.NewBufferString(`{"source_tracker":"`+key+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Owner", "foreign")
	resp, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, 403, resp.StatusCode)
	resp.Body.Close()
	require.Zero(t, svc.downloads)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/jobs/id/download", nil)
		req.Header.Set("X-Owner", "owner")
		resp, err := app.Test(req)
		require.NoError(t, err)
		require.Equal(t, 200, resp.StatusCode)
		resp.Body.Close()
	}
	require.Equal(t, 2, svc.downloads)
}
