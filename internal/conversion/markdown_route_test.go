package conversion_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"os/exec"
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

// A rendered temporary file is not a durable object key. Missing storage must
// never become COMPLETED, charge the owner, or offer an already-deleted result.
func TestMarkdownAsyncMissingStorageFailsClosed(t *testing.T) {
	// storage.Default caches initialization with sync.Once. Do not poison a
	// sibling test's loopback object store, or accidentally reuse its client.
	const childFlag = "PLATEN_TEST_MARKDOWN_NO_STORAGE_CHILD"
	if os.Getenv(childFlag) != "1" {
		executable, err := os.Executable()
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		command := exec.CommandContext(ctx, executable, "-test.run=^TestMarkdownAsyncMissingStorageFailsClosed$", "-test.count=1", "-test.timeout=45s", "-test.v")
		command.Env = append(os.Environ(), childFlag+"=1")
		output, err := command.CombinedOutput()
		require.NoError(t, err, string(output))
		if strings.Contains(string(output), "--- SKIP:") {
			t.Skip(string(output))
		}
		t.Log(string(output))
		return
	}
	found := false
	for _, name := range []string{"headless_shell", "chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
		if _, err := exec.LookPath(name); err == nil {
			found = true
			break
		}
	}
	if !found {
		t.Skip("local Chromium not installed")
	}
	t.Setenv("APP_ENV", "development")
	t.Setenv("BILLING_MODE", "normal")
	for _, name := range []string{"R2_ENDPOINT", "R2_BUCKET", "R2_ACCESS_KEY", "R2_SECRET_KEY"} {
		t.Setenv(name, "")
	}
	dir, err := os.MkdirTemp("/tmp", "md-route-storage-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("TMPDIR", dir)
	app, client, _, _ := newPDFToMarkdownRouteTestApp(t)
	owner := "f04-storage-" + uuid.NewString()
	status, body := postMarkdownPolicy(t, app, "/api/conversion/markdown-to-pdf-async", owner, "# Synthetic offline document\n\nValid text.")
	require.Equal(t, fiber.StatusAccepted, status, body)
	var accepted struct {
		TaskID string `json:"taskId"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &accepted))
	require.NotEmpty(t, accepted.TaskID)
	require.Eventually(t, func() bool {
		task, err := tasks.Registry.Get(accepted.TaskID)
		return err == nil && task != nil && (task.Status == "COMPLETED" || task.Status == "FAILED")
	}, 15*time.Second, 20*time.Millisecond)
	task, err := tasks.Registry.Get(accepted.TaskID)
	require.NoError(t, err)
	require.Equal(t, "FAILED", task.Status)
	require.Contains(t, task.Error, "storage")
	require.Empty(t, task.ResultKey)
	require.Empty(t, task.ResultURL)
	state, err := client.HGetAll(context.Background(), "platen:guestquota:state:"+owner).Result()
	require.NoError(t, err)
	require.Equal(t, "0", state["pending_3h"])
	require.Equal(t, "0", state["used_3h"])
	require.Eventually(t, func() bool {
		for _, pattern := range []string{"md-compiled-*", "md-browser-*", accepted.TaskID + "-*"} {
			files, err := filepath.Glob(filepath.Join(dir, pattern))
			if err != nil || len(files) != 0 {
				return false
			}
		}
		return true
	}, time.Second, 10*time.Millisecond)
}
