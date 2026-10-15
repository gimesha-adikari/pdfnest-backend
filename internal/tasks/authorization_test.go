package tasks

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/require"
	"pdfnest-backend/internal/identity"
)

func TestTaskStatusAndProgressRequireOwner(t *testing.T) {
	_, registry, cleanup := setupDownloadTestRedis(t)
	defer cleanup()
	_, err := registry.SetWithDownloadToken("private-task", "COMPLETED", 100, "private/output.pdf", "", "owner-a", "synthetic-capability", "synthetic-reservation")
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

	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/private-task?token=synthetic-capability", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var publicStatus map[string]json.RawMessage
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&publicStatus))
	require.Contains(t, publicStatus, "downloadToken")
	require.Contains(t, publicStatus, "status")
	for _, internalField := range []string{"resultKey", "resultUrl", "ownerIdentity", "reservationId", "reservationKind"} {
		require.NotContains(t, publicStatus, internalField, "status response must not expose internal task field %q", internalField)
	}
}

func TestOwnerlessTaskRequiresCapabilityForStatusAndDownload(t *testing.T) {
	_, registry, cleanup := setupDownloadTestRedis(t)
	defer cleanup()

	const taskID = "legacy-ownerless-task"
	result, err := os.CreateTemp(t.TempDir(), "ownerless-result-*.pdf")
	require.NoError(t, err)
	_, err = result.WriteString("%PDF-1.4 synthetic task output")
	require.NoError(t, err)
	require.NoError(t, result.Close())

	require.NoError(t, registry.Set(taskID, "COMPLETED", 100, result.Name(), ""))
	task, err := registry.Get(taskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	require.Empty(t, task.OwnerIdentity)
	require.NotEmpty(t, task.DownloadToken)

	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityIDKey, c.Get("X-Test-Identity"))
		return c.Next()
	})
	RegisterRoutes(app)

	for _, path := range []string{fmt.Sprintf("/v1/tasks/%s", taskID), fmt.Sprintf("/v1/download/%s", taskID)} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.Header.Set("X-Test-Identity", "unrelated-identity")
			response, requestErr := app.Test(request)
			require.NoError(t, requestErr)
			response.Body.Close()
			require.Equal(t, http.StatusForbidden, response.StatusCode)

			request = httptest.NewRequest(http.MethodGet, path+"?token="+task.DownloadToken, nil)
			request.Header.Set("X-Test-Identity", "unrelated-identity")
			response, requestErr = app.Test(request)
			require.NoError(t, requestErr)
			response.Body.Close()
			require.Equal(t, http.StatusOK, response.StatusCode)
		})
	}

	request := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/v1/tasks/%s", taskID), nil)
	request.Header.Set("X-Test-Identity", "unrelated-identity")
	response, requestErr := app.Test(request)
	require.NoError(t, requestErr)
	var cancelResponse map[string]json.RawMessage
	require.NoError(t, json.NewDecoder(response.Body).Decode(&cancelResponse))
	response.Body.Close()
	require.Equal(t, http.StatusForbidden, response.StatusCode)
	require.NotContains(t, cancelResponse, "downloadToken")

	request = httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/v1/tasks/%s?token=%s", taskID, task.DownloadToken), nil)
	request.Header.Set("X-Test-Identity", "unrelated-identity")
	response, requestErr = app.Test(request)
	require.NoError(t, requestErr)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	var authorizedCancel map[string]json.RawMessage
	require.NoError(t, json.NewDecoder(response.Body).Decode(&authorizedCancel))
	var terminalStatus string
	require.NoError(t, json.Unmarshal(authorizedCancel["status"], &terminalStatus))
	require.Equal(t, "COMPLETED", terminalStatus)
	require.Equal(t, `"`+task.DownloadToken+`"`, string(authorizedCancel["downloadToken"]))

	stillCompleted, err := registry.Get(taskID)
	require.NoError(t, err)
	require.NotNil(t, stillCompleted)
	require.Equal(t, "COMPLETED", stillCompleted.Status)
}
