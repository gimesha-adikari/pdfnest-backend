package conversion_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/tasks"
)

func TestMarkdownRoutesPolicyFailureNeverPublishesOrCharges(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	dir, err := os.MkdirTemp("/tmp", "md-route-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TMPDIR", dir)
	app, client, _, _ := newPDFToMarkdownRouteTestApp(t)
	for _, route := range []string{"/api/conversion/markdown-to-pdf", "/api/conversion/markdown-to-pdf-async"} {
		t.Run(route, func(t *testing.T) {
			owner := "f04-" + uuid.NewString()
			status, body := postMarkdownPolicy(t, app, route, owner, "![forbidden](http://127.0.0.1:12345/x)")
			if strings.HasSuffix(route, "-async") {
				require.Equal(t, fiber.StatusAccepted, status, body)
				var accepted struct {
					TaskID string `json:"taskId"`
				}
				require.NoError(t, json.Unmarshal([]byte(body), &accepted))
				require.NotEmpty(t, accepted.TaskID)
				require.Eventually(t, func() bool {
					task, err := tasks.Registry.Get(accepted.TaskID)
					if err != nil || task == nil || task.Status != "FAILED" {
						return false
					}
					_, statErr := os.Stat(filepath.Join(dir, accepted.TaskID+"-input.md"))
					return os.IsNotExist(statErr)
				}, 5*time.Second, 10*time.Millisecond)
				task, err := tasks.Registry.Get(accepted.TaskID)
				require.NoError(t, err)
				require.Empty(t, task.ResultKey)
				require.Empty(t, task.ResultURL)
				require.Contains(t, task.Error, "markdown resource policy")
			} else {
				require.Equal(t, fiber.StatusBadRequest, status, body)
				require.Contains(t, body, "MARKDOWN_RESOURCE_FORBIDDEN")
				require.NotContains(t, body, "%PDF-")
			}
			state, err := client.HGetAll(context.Background(), "platen:guestquota:state:"+owner).Result()
			require.NoError(t, err)
			require.Equal(t, "0", state["pending_3h"])
			require.Equal(t, "0", state["used_3h"])
		})
	}
	for _, pattern := range []string{"md-compiled-*", "md-render-*", "*-input.md"} {
		files, err := filepath.Glob(filepath.Join(dir, pattern))
		require.NoError(t, err)
		require.Empty(t, files)
	}
}

func TestMarkdownRoutesRejectOversizedSourceBeforeAsyncCopy(t *testing.T) {
	t.Setenv("BILLING_MODE", "free")
	app, _, _, _ := newPDFToMarkdownRouteTestApp(t)
	for _, route := range []string{"/api/conversion/markdown-to-pdf", "/api/conversion/markdown-to-pdf-async"} {
		status, body := postMarkdownPolicy(t, app, route, "f04-size-"+uuid.NewString(), strings.Repeat("x", (2<<20)+1))
		// If run against the old handler, await its worker before restoring test globals.
		if status == fiber.StatusAccepted {
			var accepted struct {
				TaskID string `json:"taskId"`
			}
			require.NoError(t, json.Unmarshal([]byte(body), &accepted))
			require.Eventually(t, func() bool {
				task, err := tasks.Registry.Get(accepted.TaskID)
				return err == nil && task != nil && task.Status == "FAILED"
			}, 5*time.Second, 10*time.Millisecond)
		}
		require.Equal(t, fiber.StatusBadRequest, status, body)
		require.Contains(t, body, "MARKDOWN_RESOURCE_FORBIDDEN")
	}
}

func postMarkdownPolicy(t *testing.T, app *fiber.App, route, owner, source string) (int, string) {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("file", "input.md")
	require.NoError(t, err)
	_, err = io.WriteString(file, source)
	require.NoError(t, err)
	require.NoError(t, form.Close())
	request := httptest.NewRequest("POST", route, &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	request.Header.Set("X-Test-Identity", owner)
	request.Header.Set("X-Test-Identity-Type", string(identity.TypeGuest))
	request.Header.Set("Idempotency-Key", uuid.NewString())
	response, err := app.Test(request, 10000)
	require.NoError(t, err)
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, string(data)
}
