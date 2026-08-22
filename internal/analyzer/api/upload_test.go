package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/models"
	"pdfnest-backend/internal/analyzer/worker"
)

func createValidZipBuffer(t *testing.T) []byte {
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)

	w, err := zw.Create("package.json")
	require.NoError(t, err)
	_, err = w.Write([]byte(`{"name": "test-app", "version": "1.0.0"}`))
	require.NoError(t, err)

	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func TestUploadArchive_AtomicIngestionAndSessionCreation(t *testing.T) {
	tempStorageDir := t.TempDir()
	t.Setenv("ANALYZER_STORAGE_DIR", tempStorageDir)

	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rClient.Close()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "host=localhost user=postgres password=2021 dbname=pdfnest port=5432 sslmode=disable"
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Skip("PostgreSQL database unavailable, skipping test")
	}
	require.NoError(t, db.AutoMigrate(&models.AnalyzerSession{}))

	svc := NewService(db, rClient, worker.DefaultQueueName)
	ctrl := NewController(svc)
	app := fiber.New()
	RegisterRoutes(app.Group("/api"), ctrl)

	// 1. Attempt creating session with NON-EXISTENT storageKey -> Must be rejected with 404
	badSessionReq := CreateSessionRequest{
		SourceType: engine.SourceTypeZip,
		StorageKey: "repositories/raw/non-existent-archive.zip",
	}
	body, _ := json.Marshal(badSessionReq)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/analyzer/sessions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Platen-Fingerprint", "test-user")
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	var errResp APIError
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
	assert.Equal(t, "STORAGE_OBJECT_NOT_FOUND", errResp.Code)

	// 2. Perform Multipart Upload of a real ZIP
	zipData := createValidZipBuffer(t)
	bodyBuf := new(bytes.Buffer)
	mpWriter := multipart.NewWriter(bodyBuf)

	part, err := mpWriter.CreateFormFile("file", "my-app.zip")
	require.NoError(t, err)
	_, err = io.Copy(part, bytes.NewReader(zipData))
	require.NoError(t, err)
	require.NoError(t, mpWriter.Close())

	uploadReq := httptest.NewRequest(http.MethodPost, "/api/v1/analyzer/upload", bodyBuf)
	uploadReq.Header.Set("Content-Type", mpWriter.FormDataContentType())
	uploadReq.Header.Set("X-Platen-Fingerprint", "test-user")
	uploadResp, err := app.Test(uploadReq)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, uploadResp.StatusCode)

	var upData UploadArchiveResponse
	require.NoError(t, json.NewDecoder(uploadResp.Body).Decode(&upData))
	assert.NotEmpty(t, upData.StorageKey)
	assert.Equal(t, "my-app.zip", upData.FileName)
	assert.Equal(t, "my-app", upData.RepositoryName)
	assert.NotEmpty(t, upData.SHA256)
	assert.Equal(t, int64(len(zipData)), upData.Size)

	// 3. Create Session with the verified StorageKey -> Must succeed with 200 OK
	validSessionReq := CreateSessionRequest{
		SourceType:     engine.SourceTypeZip,
		StorageKey:     upData.StorageKey,
		RepositoryName: upData.RepositoryName,
	}
	body, _ = json.Marshal(validSessionReq)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/analyzer/sessions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Platen-Fingerprint", "test-user")
	resp, err = app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	var sessResp SessionResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&sessResp))
	assert.NotEmpty(t, sessResp.SessionID)
	assert.Equal(t, upData.StorageKey, sessResp.StorageKey)
	assert.Equal(t, "my-app", sessResp.RepositoryName)
}

func TestUploadArchive_InvalidFileFormatRejection(t *testing.T) {
	tempStorageDir := t.TempDir()
	t.Setenv("ANALYZER_STORAGE_DIR", tempStorageDir)

	svc := NewService(nil, nil, worker.DefaultQueueName)
	ctrl := NewController(svc)
	app := fiber.New()
	RegisterRoutes(app.Group("/api"), ctrl)

	// Upload invalid non-zip content (plain text)
	bodyBuf := new(bytes.Buffer)
	mpWriter := multipart.NewWriter(bodyBuf)
	part, err := mpWriter.CreateFormFile("file", "fake.zip")
	require.NoError(t, err)
	_, err = part.Write([]byte("This is not a zip file"))
	require.NoError(t, err)
	require.NoError(t, mpWriter.Close())

	req := httptest.NewRequest(http.MethodPost, "/api/v1/analyzer/upload", bodyBuf)
	req.Header.Set("Content-Type", mpWriter.FormDataContentType())
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var errResp APIError
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
	assert.Equal(t, "INVALID_ARCHIVE", errResp.Code)
}
