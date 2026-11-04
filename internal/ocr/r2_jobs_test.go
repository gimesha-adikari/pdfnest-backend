package ocr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/tasks"

	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
)

type blockedR2JobService struct {
	started chan struct{}
	release chan struct{}
}

type completedR2JobService struct{ outputPath string }

func (s *completedR2JobService) ExtractTextFromPDF(context.Context, string, string) (string, error) {
	return "", errors.New("unexpected extraction call")
}

func (s *completedR2JobService) ImageToTextPDF(context.Context, []string, string) (string, error) {
	return "", errors.New("unexpected image conversion call")
}

func (s *completedR2JobService) ImageToTextPDFFromR2(context.Context, []R2ImageRef, string) (string, error) {
	return s.outputPath, nil
}

func writeSyntheticOnePagePDF(t *testing.T) string {
	t.Helper()
	var document strings.Builder
	document.WriteString("%PDF-1.4\n")
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> /Contents 4 0 R >>",
		"<< /Length 0 >>\nstream\n\nendstream",
	}
	offsets := make([]int, len(objects)+1)
	for index, object := range objects {
		number := index + 1
		offsets[number] = document.Len()
		fmt.Fprintf(&document, "%d 0 obj\n%s\nendobj\n", number, object)
	}
	xrefOffset := document.Len()
	fmt.Fprintf(&document, "xref\n0 %d\n", len(offsets))
	document.WriteString("0000000000 65535 f \n")
	for index := 1; index < len(offsets); index++ {
		fmt.Fprintf(&document, "%010d 00000 n \n", offsets[index])
	}
	fmt.Fprintf(&document, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xrefOffset)
	outputPath := filepath.Join(t.TempDir(), "synthetic-output.pdf")
	if err := os.WriteFile(outputPath, []byte(document.String()), 0600); err != nil {
		t.Fatal(err)
	}
	return outputPath
}

func (s *blockedR2JobService) ExtractTextFromPDF(context.Context, string, string) (string, error) {
	return "", errors.New("unexpected extraction call")
}

func (s *blockedR2JobService) ImageToTextPDF(context.Context, []string, string) (string, error) {
	return "", errors.New("unexpected image conversion call")
}

func (s *blockedR2JobService) ImageToTextPDFFromR2(ctx context.Context, _ []R2ImageRef, _ string) (string, error) {
	s.started <- struct{}{}
	select {
	case <-s.release:
		return "", errors.New("controlled worker stop")
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestR2JobPersistsValidPDFAndKeepsOwnerForDownload(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("BILLING_MODE", "free")
	storageRoot := t.TempDir()
	t.Setenv("LOCAL_STORAGE_DIR", storageRoot)

	db, client := setupGuestAsyncIntegration(t)
	if db == nil || client == nil {
		t.Skip("isolated local PostgreSQL/Redis are required")
	}

	oldRegistry := tasks.Registry
	tasks.Registry = &tasks.TaskRegistry{}
	t.Cleanup(func() { tasks.Registry = oldRegistry })

	outputPath := writeSyntheticOnePagePDF(t)
	service := &completedR2JobService{outputPath: outputPath}
	controller := NewController(service)
	app := fiber.New()
	const owner = "synthetic-async-task-owner"
	app.Post("/api/ocr/jobs", func(c *fiber.Ctx) error {
		c.Locals("user_id", owner)
		return controller.HandleAsyncImageToTextPDFR2(c)
	})

	key := storage.NewOwnedKey(owner, "image_to_text_pdf", ".png")
	// Save a dummy object to satisfy existence check
	if err := storage.SaveObjectFile(context.Background(), key, outputPath, "image/png"); err != nil {
		t.Fatal(err)
	}

	body, err := json.Marshal(OCRR2JobRequest{
		Tool:  "image_to_text_pdf",
		Lang:  "eng",
		Files: []R2ImageRef{{Key: key, Name: "synthetic.png", Type: "image/png", Size: 10}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/ocr/jobs", bytes.NewReader(body))
	request.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	response, err := app.Test(request, 5_000)
	if err != nil {
		t.Fatalf("submit synthetic job: %v", err)
	}
	if response.StatusCode != fiber.StatusAccepted {
		defer response.Body.Close()
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("job submission failed: status=%d body=%s", response.StatusCode, payload)
	}
	var accepted struct {
		TaskID string `json:"taskId"`
	}
	if err := json.NewDecoder(response.Body).Decode(&accepted); err != nil {
		response.Body.Close()
		t.Fatalf("decode accepted job: %v", err)
	}
	response.Body.Close()
	if accepted.TaskID == "" {
		t.Fatal("accepted job did not return a task ID")
	}

	deadline := time.Now().Add(5 * time.Second)
	var task *tasks.TaskStatus
	for time.Now().Before(deadline) {
		task, err = tasks.Registry.Get(accepted.TaskID)
		if err == nil && task != nil && (task.Status == "COMPLETED" || task.Status == "FAILED" || task.Status == "CANCELLED") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil || task == nil || task.Status != "COMPLETED" {
		t.Fatalf("task did not complete successfully: task=%+v err=%v", task, err)
	}
	if task.OwnerIdentity != owner {
		t.Fatalf("completed R2 task lost owner: got %q want %q", task.OwnerIdentity, owner)
	}
	if !strings.HasPrefix(task.ResultURL, storageRoot+string(os.PathSeparator)) {
		t.Fatalf("local result should use persisted shared-storage path, got %+v", task)
	}
	if _, err := os.Stat(task.ResultURL); err != nil {
		t.Fatalf("completed task artifact was not durably persisted: %v", err)
	}
	// Under F-18 retention, the input image reference is retained and not deleted
	if !storage.ObjectExists(context.Background(), key) {
		t.Fatal("task-owned uploaded image should be retained under F-18")
	}

	downloadApp := fiber.New()
	downloadApp.Get("/api/v1/download/:id", func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityIDKey, c.Get("X-Test-Identity"))
		return tasks.HandleTaskDownload(c)
	})
	for _, testCase := range []struct {
		name       string
		identity   string
		token      string
		wantStatus int
	}{
		{name: "owner", identity: owner, wantStatus: fiber.StatusOK},
		{name: "other identity", identity: "different-synthetic-owner", wantStatus: fiber.StatusForbidden},
		{name: "capability holder", identity: "different-synthetic-owner", token: task.DownloadToken, wantStatus: fiber.StatusOK},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			target := "/api/v1/download/" + accepted.TaskID
			if testCase.token != "" {
				target += "?token=" + url.QueryEscape(testCase.token)
			}
			request := httptest.NewRequest(http.MethodGet, target, nil)
			request.Header.Set("X-Test-Identity", testCase.identity)
			response, err := downloadApp.Test(request)
			if err != nil {
				t.Fatalf("download task: %v", err)
			}
			defer response.Body.Close()
			if response.StatusCode != testCase.wantStatus {
				t.Fatalf("unexpected download status: got %d want %d", response.StatusCode, testCase.wantStatus)
			}
			if response.StatusCode == fiber.StatusOK {
				payload, err := io.ReadAll(response.Body)
				if err != nil || !bytes.HasPrefix(payload, []byte("%PDF-")) {
					t.Fatalf("download did not return the persisted PDF: err=%v", err)
				}
			}
		})
	}

	if err := os.Remove(task.ResultURL); err != nil {
		t.Fatalf("remove completed artifact for missing-object check: %v", err)
	}
	missingRequest := httptest.NewRequest(http.MethodGet, "/api/v1/download/"+accepted.TaskID, nil)
	missingRequest.Header.Set("X-Test-Identity", owner)
	missingResponse, err := downloadApp.Test(missingRequest)
	if err != nil {
		t.Fatalf("download task with missing artifact: %v", err)
	}
	defer missingResponse.Body.Close()
	if missingResponse.StatusCode != fiber.StatusGone {
		payload, _ := io.ReadAll(missingResponse.Body)
		t.Fatalf("missing local artifact must fail safely: status=%d response=%s", missingResponse.StatusCode, payload)
	}
	missingBody, _ := io.ReadAll(missingResponse.Body)
	if bytes.Contains(missingBody, []byte(storageRoot)) || bytes.Contains(missingBody, []byte(task.ResultURL)) {
		t.Fatal("missing-artifact response disclosed the storage path")
	}
}

func TestR2JobDoesNotCompleteWithMalformedPDFArtifact(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("BILLING_MODE", "free")
	storageRoot := t.TempDir()
	t.Setenv("LOCAL_STORAGE_DIR", storageRoot)

	db, client := setupGuestAsyncIntegration(t)
	if db == nil || client == nil {
		t.Skip("isolated local PostgreSQL/Redis are required")
	}

	oldRegistry := tasks.Registry
	tasks.Registry = &tasks.TaskRegistry{}
	t.Cleanup(func() { tasks.Registry = oldRegistry })

	outputPath := filepath.Join(t.TempDir(), "malformed-output.pdf")
	if err := os.WriteFile(outputPath, []byte("not a PDF"), 0600); err != nil {
		t.Fatal(err)
	}
	controller := NewController(&completedR2JobService{outputPath: outputPath})
	app := fiber.New()
	const owner = "synthetic-invalid-pdf-owner"
	app.Post("/api/ocr/jobs", func(c *fiber.Ctx) error {
		c.Locals("user_id", owner)
		return controller.HandleAsyncImageToTextPDFR2(c)
	})
	key := storage.NewOwnedKey(owner, "image_to_text_pdf", ".png")
	if err := storage.SaveObjectFile(context.Background(), key, outputPath, "image/png"); err != nil {
		t.Fatal(err)
	}

	body, err := json.Marshal(OCRR2JobRequest{
		Tool:  "image_to_text_pdf",
		Lang:  "eng",
		Files: []R2ImageRef{{Key: key, Name: "synthetic.png", Type: "image/png", Size: 10}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/ocr/jobs", bytes.NewReader(body))
	request.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	response, err := app.Test(request, 5_000)
	if err != nil {
		t.Fatalf("submit synthetic job: %v", err)
	}
	if response.StatusCode != fiber.StatusAccepted {
		defer response.Body.Close()
		t.Fatalf("invalid artifact submission unexpectedly failed synchronously: status=%d", response.StatusCode)
	}
	var accepted struct {
		TaskID string `json:"taskId"`
	}
	if err := json.NewDecoder(response.Body).Decode(&accepted); err != nil {
		response.Body.Close()
		t.Fatalf("decode accepted job: %v", err)
	}
	response.Body.Close()

	deadline := time.Now().Add(5 * time.Second)
	var task *tasks.TaskStatus
	for time.Now().Before(deadline) {
		task, err = tasks.Registry.Get(accepted.TaskID)
		if err == nil && task != nil && (task.Status == "COMPLETED" || task.Status == "FAILED" || task.Status == "CANCELLED") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil || task == nil || task.Status != "FAILED" {
		t.Fatalf("malformed result must fail before publication: task=%+v err=%v", task, err)
	}
	if task.OwnerIdentity != owner || task.ResultURL != "" || task.ResultKey != "" {
		t.Fatalf("failed task exposed or lost ownership of an artifact: %+v", task)
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("temporary malformed worker output was not removed: err=%v", err)
	}
	// Under F-18 retention, the input image reference is retained and not deleted
	if !storage.ObjectExists(context.Background(), key) {
		t.Fatal("task-owned uploaded image should be retained under F-18")
	}
}

func TestR2JobDoesNotStartUnlessOwnedTaskRegistrationSucceeds(t *testing.T) {
	t.Setenv("BILLING_MODE", "free")

	badClient := redis.NewClient(&redis.Options{
		Addr:         "127.0.0.1:1",
		MaxRetries:   -1,
		DialTimeout:  50 * time.Millisecond,
		ReadTimeout:  50 * time.Millisecond,
		WriteTimeout: 50 * time.Millisecond,
	})
	defer badClient.Close()

	oldRegistry := tasks.Registry
	oldRedis := config.Redis
	tasks.Registry = &tasks.TaskRegistry{}
	config.Redis = badClient
	t.Cleanup(func() {
		tasks.Registry = oldRegistry
		config.Redis = oldRedis
	})

	service := &blockedR2JobService{started: make(chan struct{}, 1), release: make(chan struct{})}
	controller := NewController(service)
	app := fiber.New()
	const owner = "synthetic-async-task-owner"
	app.Post("/api/ocr/jobs", func(c *fiber.Ctx) error {
		c.Locals("user_id", owner)
		return controller.HandleAsyncImageToTextPDFR2(c)
	})

	key := storage.NewOwnedKey(owner, "image_to_text_pdf", ".png")
	body, err := json.Marshal(OCRR2JobRequest{
		Tool:  "image_to_text_pdf",
		Lang:  "eng",
		Files: []R2ImageRef{{Key: key, Name: "synthetic.png", Type: "image/png", Size: 10}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/api/ocr/jobs", bytes.NewReader(body))
	request.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	response, err := app.Test(request, 5_000)
	if err != nil {
		t.Fatalf("submit synthetic job: %v", err)
	}
	_ = response.Body.Close()

	if response.StatusCode != fiber.StatusServiceUnavailable {
		select {
		case <-service.started:
			close(service.release)
		case <-time.After(100 * time.Millisecond):
		}
		t.Fatalf("task without a persisted owner was accepted: status=%d", response.StatusCode)
	}
	select {
	case <-service.started:
		close(service.release)
		t.Fatal("worker started although task registration failed")
	case <-time.After(100 * time.Millisecond):
	}
}
