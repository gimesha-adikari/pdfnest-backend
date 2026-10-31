package conversion_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/storage"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// This catches routes bypassing the configured local store through storage.Default.
// Run in a fresh process for the pre-fix reproduction: Default caches R2 settings.
func TestPDFToMarkdownLocalStorageDoesNotRequireR2(t *testing.T) {
	t.Setenv("APP_ENV", "test")
	t.Setenv("BILLING_MODE", "free")
	for _, name := range []string{"R2_BUCKET", "R2_ENDPOINT", "R2_ACCESS_KEY", "R2_SECRET_KEY"} {
		t.Setenv(name, "")
	}
	app, _, _, _ := newPDFToMarkdownRouteTestApp(t)
	for _, route := range []string{"/api/conversion/pdf-to-markdown", "/api/conversion/pdf-to-markdown-async"} {
		t.Run(filepath.Base(route), func(t *testing.T) {
			status, body := postPDFToMarkdownRoute(t, app, route, uuid.NewString(), string(identity.TypeGuest))
			require.Equal(t, fiber.StatusAccepted, status, body)
			var accepted struct {
				TaskID string `json:"task_id"`
			}
			require.NoError(t, json.Unmarshal([]byte(body), &accepted))
			require.NotEmpty(t, accepted.TaskID)
			data, err := os.ReadFile(filepath.Join(storage.GetLocalStorageDir(), "jobs", "markdown", "source", accepted.TaskID+".pdf"))
			require.NoError(t, err)
			require.Equal(t, singlePagePDF(), data, "the actor must receive the bytes in the active shared local store")
		})
	}
}
