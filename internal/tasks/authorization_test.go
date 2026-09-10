package tasks

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/require"
	"pdfnest-backend/internal/identity"
)

func TestTaskStatusAndProgressRequireOwner(t *testing.T) {
	_, registry, cleanup := setupDownloadTestRedis(t)
	defer cleanup()
	_, err := registry.SetWithKey("private-task", "COMPLETED", 100, "private/output.pdf", "", "owner-a")
	require.NoError(t, err)
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityIDKey, c.Get("X-Test-Identity"))
		return c.Next()
	})
	RegisterRoutes(app)
	for _, path := range []string{"/v1/tasks/private-task", "/v1/tasks/private-task/progress"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("X-Test-Identity", "owner-b")
			req.Header.Set("Connection", "Upgrade")
			req.Header.Set("Upgrade", "websocket")
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusForbidden, resp.StatusCode)
		})
	}
	for _, owner := range []string{"owner-a", ""} {
		req := httptest.NewRequest(http.MethodGet, "/v1/tasks/private-task", nil)
		req.Header.Set("X-Test-Identity", owner)
		resp, err := app.Test(req)
		require.NoError(t, err)
		resp.Body.Close()
		if owner == "owner-a" {
			require.Equal(t, http.StatusOK, resp.StatusCode)
		} else {
			require.Equal(t, http.StatusForbidden, resp.StatusCode)
		}
	}
}
