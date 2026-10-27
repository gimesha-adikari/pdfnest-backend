package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"pdfnest-backend/internal/studio/vdm"
	"pdfnest-backend/internal/studiosource"
	"strconv"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/studio/models"
	"pdfnest-backend/internal/temp"
)

type sourceAdmissionRepo struct {
	Repository
	created      int
	doc          *models.StudioDocument
	session      *models.StudioSession
	asset        *models.StudioAsset
	version      *models.StudioVersion
	fail         error
	beforeCreate func()
}

func (r *sourceAdmissionRepo) CreateDocumentWithInitialVersion(ctx context.Context, doc *models.StudioDocument, asset *models.StudioAsset, version *models.StudioVersion, session *models.StudioSession) error {
	if r.beforeCreate != nil {
		r.beforeCreate()
	}
	if r.fail != nil {
		return r.fail
	}
	r.created++
	r.doc = doc
	r.asset = asset
	r.session = session
	r.version = version
	return nil
}
func sourceAdmissionApp(t *testing.T) (*fiber.App, *sourceAdmissionRepo) {
	t.Helper()
	t.Setenv("APP_ENV", "test")
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_STORAGE_DIR", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	temp.ResetDirForTesting()
	t.Cleanup(temp.ResetDirForTesting)
	repo := &sourceAdmissionRepo{}
	ctrl := NewController(NewService(repo), nil, NewTileRenderer(repo), nil, nil)
	app := fiber.New(fiber.Config{BodyLimit: 100 << 20})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityKey, identity.Identity{ID: "synthetic-owner", Type: identity.TypeGuest})
		return c.Next()
	})
	RegisterRoutes(app.Group("/api"), ctrl)
	return app, repo
}
func sourceAdmissionJSON(t *testing.T, app *fiber.App, req CreateSessionRequest) (int, map[string]any) {
	t.Helper()
	data, err := json.Marshal(req)
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions", bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(request, 10000)
	require.NoError(t, err)
	defer resp.Body.Close()
	var payload map[string]any
	data, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	if json.Unmarshal(data, &payload) != nil {
		payload = map[string]any{"error": string(data)}
	}
	return resp.StatusCode, payload
}
func TestStudioSourceAdmissionMissingStoredObject(t *testing.T) {
	app, repo := sourceAdmissionApp(t)
	status, payload := sourceAdmissionJSON(t, app, CreateSessionRequest{FileName: "missing.pdf", FileSize: 1, SourceAssetID: uuid.NewString(), SourceR2Key: storage.NewOwnedKey("synthetic-owner", "studio_source", ".pdf")})
	t.Logf("missing-object registration HTTP %d; durable creations=%d", status, repo.created)
	require.Equal(t, http.StatusNotFound, status, payload)
	require.Zero(t, repo.created)
}

func (r *sourceAdmissionRepo) TouchSession(context.Context, uuid.UUID) error { return nil }
func (r *sourceAdmissionRepo) GetSession(context.Context, uuid.UUID) (*models.StudioSession, error) {
	return r.session, nil
}
func (r *sourceAdmissionRepo) GetDocument(context.Context, uuid.UUID) (*models.StudioDocument, error) {
	return r.doc, nil
}
func (r *sourceAdmissionRepo) GetVersion(context.Context, uuid.UUID) (*models.StudioVersion, error) {
	return r.version, nil
}
func (r *sourceAdmissionRepo) CreateAsset(_ context.Context, a *models.StudioAsset) error {
	if r.fail != nil {
		return r.fail
	}
	r.created++
	r.asset = a
	return nil
}

func sourceAdmissionStored(t *testing.T, data []byte) string {
	t.Helper()
	key := storage.NewOwnedKey("synthetic-owner", "studio_source", ".pdf")
	path := filepath.Join(storage.GetLocalStorageDir(), key)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	require.NoError(t, os.WriteFile(path, data, 0600))
	return key
}
func sourceAdmissionCopies(t *testing.T) []string {
	t.Helper()
	var paths []string
	_ = filepath.WalkDir(storage.GetLocalStorageDir(), func(path string, d os.DirEntry, e error) error {
		if e == nil && !d.IsDir() && strings.Contains(path, "/studio/sources/") {
			paths = append(paths, path)
		}
		return nil
	})
	return paths
}
func sourceAdmissionNoTemps(t *testing.T) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(os.TempDir(), "platen-studio-source-*"))
	require.NoError(t, err)
	require.Empty(t, paths)
	staged, err := filepath.Glob(filepath.Join(temp.GetDir(), "pdfnest-upload-*"))
	require.NoError(t, err)
	require.Empty(t, staged)
}

func TestStudioSourceAdmissionStoredValidationAndMetadata(t *testing.T) {
	for _, claimed := range []int64{1, 250 << 20} {
		t.Run(fmt.Sprint(claimed), func(t *testing.T) {
			app, repo := sourceAdmissionApp(t)
			t.Setenv("TMPDIR", t.TempDir())
			pdf := createDeterministicTestPDF(t)
			key := sourceAdmissionStored(t, pdf)
			assetID := uuid.NewString()
			status, payload := sourceAdmissionJSON(t, app, CreateSessionRequest{FileName: "source.pdf", FileSize: claimed, InitialPageCount: 999999, SourceAssetID: assetID, SourceR2Key: key, InitialVDM: vdm.DocumentModel{PageCount: 1, Pages: []vdm.PageDescriptor{{PageID: "fake-page", SourceAssetID: &assetID, SourcePageNumber: 999999, Dimensions: &vdm.Dimensions{Width: 1, Height: 1}}}}})
			require.Equal(t, 201, status, payload)
			require.Equal(t, 1, repo.created)
			require.Equal(t, int64(len(pdf)), repo.doc.FileSize)
			require.Equal(t, 2, repo.doc.InitialPageCount)
			require.NotEqual(t, assetID, repo.asset.ID)
			require.NotEqual(t, key, repo.asset.R2Key)
			require.True(t, strings.HasPrefix(repo.asset.R2Key, "studio/sources/"))
			snapshot, err := os.ReadFile(filepath.Join(storage.GetLocalStorageDir(), repo.asset.R2Key))
			require.NoError(t, err)
			require.Equal(t, pdf, snapshot)
			var state vdm.DocumentModel
			require.NoError(t, json.Unmarshal(repo.version.VirtualModel, &state))
			require.Len(t, state.Pages, 2)
			require.Equal(t, 1, state.Pages[0].SourcePageNumber)
			require.Greater(t, state.Pages[0].Dimensions.Width, float64(500))
			require.NotEqual(t, "fake-page", state.Pages[0].PageID)
			sourceAdmissionNoTemps(t)
		})
	}
}
func TestStudioSourceAdmissionAdoptionIsImmutable(t *testing.T) {
	app, repo := sourceAdmissionApp(t)
	t.Setenv("TMPDIR", t.TempDir())
	pdf := createDeterministicTestPDF(t)
	key := sourceAdmissionStored(t, pdf)
	// Replace the mutable upload after acquisition and validation, immediately
	// before the document transaction. The registered key must retain the snapshot.
	repo.beforeCreate = func() {
		require.NoError(t, os.WriteFile(filepath.Join(storage.GetLocalStorageDir(), key), []byte("untrusted replacement"), 0600))
	}
	status, payload := sourceAdmissionJSON(t, app, CreateSessionRequest{FileName: "source.pdf", SourceAssetID: uuid.NewString(), SourceR2Key: key})
	require.Equal(t, 201, status, payload)
	privateKey := repo.asset.R2Key
	persisted, err := os.ReadFile(filepath.Join(storage.GetLocalStorageDir(), privateKey))
	require.NoError(t, err)
	require.Equal(t, pdf, persisted)
	require.False(t, storage.IsOwnedKey(privateKey, "synthetic-owner", "studio_source"))
	require.NoError(t, os.WriteFile(filepath.Join(storage.GetLocalStorageDir(), key), []byte("later PUT replacement"), 0600))
	persisted, err = os.ReadFile(filepath.Join(storage.GetLocalStorageDir(), privateKey))
	require.NoError(t, err)
	require.Equal(t, pdf, persisted)
	sourceAdmissionNoTemps(t)
}
func TestStudioSourceAdmissionInvalidAndUnauthorized(t *testing.T) {
	for _, scenario := range []string{"header", "corrupt", "empty", "encrypted", "page-limit", "foreign", "wrong-purpose", "traversal", "arbitrary", "private-source-key", "foreign-vdm", "foreign-overlay", "unreadable"} {
		t.Run(scenario, func(t *testing.T) {
			app, repo := sourceAdmissionApp(t)
			t.Setenv("TMPDIR", t.TempDir())
			pdf := createDeterministicTestPDF(t)
			switch scenario {
			case "header":
				pdf = []byte("not PDF")
			case "corrupt":
				pdf = []byte("%PDF-1.7\ncorrupt synthetic")
			case "empty":
				pdf = nil
			case "encrypted":
				dir := t.TempDir()
				input, output := filepath.Join(dir, "in.pdf"), filepath.Join(dir, "encrypted.pdf")
				require.NoError(t, os.WriteFile(input, pdf, 0600))
				require.NoError(t, api.EncryptFile(input, output, model.NewAESConfiguration("synthetic-user", "synthetic-owner", 128)))
				var err error
				pdf, err = os.ReadFile(output)
				require.NoError(t, err)
			case "page-limit":
				t.Setenv("MAX_PAGES_GENERAL", "1")
			}
			key := sourceAdmissionStored(t, pdf)
			request := CreateSessionRequest{FileName: "source.pdf", FileSize: 1, InitialPageCount: 1, SourceAssetID: uuid.NewString(), SourceR2Key: key}
			expected := 400
			switch scenario {
			case "foreign":
				request.SourceR2Key = storage.NewOwnedKey("foreign-owner", "studio_source", ".pdf")
				expected = 403
			case "wrong-purpose":
				request.SourceR2Key = storage.NewOwnedKey("synthetic-owner", "repository_analyzer", ".pdf")
				expected = 403
			case "traversal":
				request.SourceR2Key = key + "/../other.pdf"
				expected = 403
			case "arbitrary":
				request.SourceR2Key = "unregistered/arbitrary.pdf"
				expected = 403
			case "private-source-key":
				request.SourceR2Key = "studio/sources/existing.pdf"
				expected = 403
			case "foreign-vdm":
				foreign := uuid.NewString()
				request.InitialVDM = vdm.DocumentModel{Pages: []vdm.PageDescriptor{{SourceAssetID: &foreign}}}
				expected = 403
			case "foreign-overlay":
				request.InitialVDM = vdm.DocumentModel{Pages: []vdm.PageDescriptor{{Overlays: []vdm.Overlay{{AssetR2Key: "private/foreign.png"}}}}}
				expected = 403
			case "unreadable":
				require.NoError(t, os.Chmod(filepath.Join(storage.GetLocalStorageDir(), key), 0000))
				expected = 503
			}
			status, payload := sourceAdmissionJSON(t, app, request)
			require.Equal(t, expected, status, payload)
			require.Zero(t, repo.created)
			require.Empty(t, sourceAdmissionCopies(t))
			sourceAdmissionNoTemps(t)
			require.FileExists(t, filepath.Join(storage.GetLocalStorageDir(), key), "admission rejection must not delete caller's uploaded object")
			body, _ := json.Marshal(payload)
			require.NotContains(t, string(body), storage.GetLocalStorageDir())
			require.NotContains(t, string(body), key)
		})
	}
}
func TestStudioSourceAdmissionBlankAndRegistrationFailure(t *testing.T) {
	app, repo := sourceAdmissionApp(t)
	t.Setenv("TMPDIR", t.TempDir())
	status, payload := sourceAdmissionJSON(t, app, CreateSessionRequest{FileName: "blank.pdf", FileSize: 250 << 20})
	require.Equal(t, 201, status, payload)
	require.Nil(t, repo.asset)
	require.Zero(t, repo.doc.FileSize)
	app, repo = sourceAdmissionApp(t)
	pdf := createDeterministicTestPDF(t)
	key := sourceAdmissionStored(t, pdf)
	repo.fail = errors.New("database SECRET /private/internal-path")
	status, payload = sourceAdmissionJSON(t, app, CreateSessionRequest{FileName: "source.pdf", SourceAssetID: uuid.NewString(), SourceR2Key: key})
	require.Equal(t, 503, status, payload)
	require.Zero(t, repo.created)
	require.Empty(t, sourceAdmissionCopies(t))
	require.FileExists(t, filepath.Join(storage.GetLocalStorageDir(), key))
	body, _ := json.Marshal(payload)
	require.NotContains(t, string(body), "SECRET")
	require.NotContains(t, string(body), "/private/")
	sourceAdmissionNoTemps(t)
}

// Insert sparse PDF whitespace before xref, then fix startxref. Unlike padding
// after EOF, this preserves a readable PDF and avoids a 50 MiB memory buffer.
func sourceAdmissionSizedPDF(t *testing.T, size int64) string {
	t.Helper()
	data := createDeterministicTestPDF(t)
	xref := bytes.LastIndex(data, []byte("\nxref\n")) + 1
	require.Greater(t, xref, 0)
	tail := data[xref:]
	marker := bytes.LastIndex(tail, []byte("startxref\n"))
	require.Greater(t, marker, 0)
	oldLine := strings.Split(string(tail[marker+10:]), "\n")[0]
	newOffset := size - int64(len(tail)) - int64(len(strconv.FormatInt(size, 10))-len(oldLine))
	for range 3 {
		newOffset = size - int64(len(tail)) - int64(len(strconv.FormatInt(newOffset, 10))-len(oldLine))
	}
	ending := bytes.Replace(tail, []byte("startxref\n"+oldLine), []byte("startxref\n"+strconv.FormatInt(newOffset, 10)), 1)
	path := filepath.Join(t.TempDir(), "sized.pdf")
	file, err := os.Create(path)
	require.NoError(t, err)
	_, err = file.Write(data[:xref])
	require.NoError(t, err)
	_, err = file.Seek(newOffset, io.SeekStart)
	require.NoError(t, err)
	_, err = file.Write(ending)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, size, info.Size())
	return path
}
func TestStudioSourceAdmissionActualBoundary(t *testing.T) {
	t.Setenv("MAX_STUDIO_UPLOAD_BYTES", "999999999")
	for _, size := range []int64{studiosource.MaxPDFBytes - 1, studiosource.MaxPDFBytes, studiosource.MaxPDFBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			path := sourceAdmissionSizedPDF(t, size)
			actual, pages, err := validateStudioPDFUpload(path)
			if size > studiosource.MaxPDFBytes {
				require.ErrorIs(t, err, ErrSourceTooLarge)
			} else {
				require.NoError(t, err)
				require.Equal(t, size, actual)
				require.Equal(t, 2, pages)
			}
		})
	}
}
func sourceAdmissionMultipart(t *testing.T, app *fiber.App, url, path string, assetKind ...string) (int, map[string]any) {
	t.Helper()
	file, err := os.Open(path)
	require.NoError(t, err)
	defer file.Close()
	var framing bytes.Buffer
	writer := multipart.NewWriter(&framing)
	if len(assetKind) > 0 {
		require.NoError(t, writer.WriteField("asset_kind", assetKind[0]))
	}
	_, err = writer.CreateFormFile("file", "source.pdf")
	require.NoError(t, err)
	header := append([]byte(nil), framing.Bytes()...)
	framing.Reset()
	require.NoError(t, writer.Close())
	tail := append([]byte(nil), framing.Bytes()...)
	info, err := file.Stat()
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, url, io.MultiReader(bytes.NewReader(header), file, bytes.NewReader(tail)))
	req.ContentLength = int64(len(header)+len(tail)) + info.Size()
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := app.Test(req, 30000)
	require.NoError(t, err)
	defer resp.Body.Close()
	var payload map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
	return resp.StatusCode, payload
}
func TestStudioSourceAdmissionDirectAndSecondary(t *testing.T) {
	app, repo := sourceAdmissionApp(t)
	t.Setenv("TMPDIR", t.TempDir())
	path := filepath.Join(t.TempDir(), "small.pdf")
	require.NoError(t, os.WriteFile(path, createDeterministicTestPDF(t), 0600))
	status, payload := sourceAdmissionMultipart(t, app, "/api/studio/v1/sessions/from-upload", path)
	require.Equal(t, 201, status, payload)
	require.Equal(t, 1, repo.created)
	status, payload = sourceAdmissionMultipart(t, app, "/api/studio/v1/sessions/"+repo.session.ID.String()+"/assets", path)
	require.Equal(t, 201, status, payload)
	require.Equal(t, 2, repo.created)
	exact := sourceAdmissionSizedPDF(t, studiosource.MaxPDFBytes)
	status, payload = sourceAdmissionMultipart(t, app, "/api/studio/v1/sessions/from-upload", exact)
	require.Equal(t, 201, status, payload)
	require.Equal(t, studiosource.MaxPDFBytes, repo.doc.FileSize)
	require.Equal(t, 3, repo.created)
	// Multipart framing exceeds 50 MiB; the cap is on PDF bytes, not framing.
	// An actual file above the limit fits global ingress but must fail Studio.
	large := sourceAdmissionSizedPDF(t, studiosource.MaxPDFBytes+1)
	for _, url := range []string{"/api/studio/v1/sessions/from-upload", "/api/studio/v1/sessions/" + repo.session.ID.String() + "/assets"} {
		status, payload = sourceAdmissionMultipart(t, app, url, large)
		require.Equal(t, 413, status, payload)
		require.Equal(t, "STUDIO_SOURCE_TOO_LARGE", payload["code"])
		require.Equal(t, 3, repo.created)
	}
	sourceAdmissionNoTemps(t)
}

func TestStudioSourceAdmissionStoredOversizeAndCancellation(t *testing.T) {
	app, repo := sourceAdmissionApp(t)
	large := sourceAdmissionSizedPDF(t, studiosource.MaxPDFBytes+1)
	key := storage.NewOwnedKey("synthetic-owner", "studio_source", ".pdf")
	destination := filepath.Join(storage.GetLocalStorageDir(), key)
	require.NoError(t, os.MkdirAll(filepath.Dir(destination), 0700))
	require.NoError(t, os.Rename(large, destination))
	status, payload := sourceAdmissionJSON(t, app, CreateSessionRequest{FileName: "small.pdf", FileSize: 1, InitialPageCount: 1, SourceAssetID: uuid.NewString(), SourceR2Key: key})
	require.Equal(t, 413, status, payload)
	require.Equal(t, "STUDIO_SOURCE_TOO_LARGE", payload["code"])
	require.Zero(t, repo.created)
	require.FileExists(t, destination)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, _, err := NewService(repo).CreateDocumentFromStoredSource(ctx, identity.Identity{ID: "synthetic-owner", Type: identity.TypeGuest}, key, "source.pdf")
	require.Error(t, err)
	require.Zero(t, repo.created)
	sourceAdmissionNoTemps(t)
}

func TestController_StoredSourceAdmission_ActualDatabase(t *testing.T) {
	t.Setenv("APP_ENV", "test")
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_STORAGE_DIR", t.TempDir())
	app, repo, _, _, db := setupTestApp(t)
	guest := "test_guest_default"
	pdf := createDeterministicTestPDF(t)
	key := storage.NewOwnedKey(guest, "studio_source", ".pdf")
	path := filepath.Join(storage.GetLocalStorageDir(), key)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	require.NoError(t, os.WriteFile(path, pdf, 0600))
	req := CreateSessionRequest{FileName: "source.pdf", FileSize: 1, InitialPageCount: 999999, SourceAssetID: uuid.NewString(), SourceR2Key: key}
	status, payload := sourceAdmissionJSON(t, app, req)
	require.Equal(t, 201, status, payload)
	raw, err := json.Marshal(payload["session"])
	require.NoError(t, err)
	var session models.StudioSession
	require.NoError(t, json.Unmarshal(raw, &session))
	doc, err := repo.GetDocument(context.Background(), session.DocumentID)
	require.NoError(t, err)
	require.Equal(t, int64(len(pdf)), doc.FileSize)
	require.Equal(t, 2, doc.InitialPageCount)
	var asset models.StudioAsset
	require.NoError(t, db.Where("document_id = ? AND asset_type = ?", doc.ID, "source_pdf").First(&asset).Error)
	require.NotEqual(t, key, asset.R2Key)
	copy, err := os.ReadFile(filepath.Join(storage.GetLocalStorageDir(), asset.R2Key))
	require.NoError(t, err)
	require.Equal(t, pdf, copy)
	var before, after int64
	require.NoError(t, db.Model(&models.StudioDocument{}).Count(&before).Error)
	req.SourceR2Key = storage.NewOwnedKey(guest, "studio_source", ".pdf")
	status, payload = sourceAdmissionJSON(t, app, req)
	require.Equal(t, 404, status, payload)
	require.NoError(t, db.Model(&models.StudioDocument{}).Count(&after).Error)
	require.Equal(t, before, after)
}

func TestStudioSourceAdmissionImageAssetsUnchanged(t *testing.T) {
	app, repo := sourceAdmissionApp(t)
	pdf := filepath.Join(t.TempDir(), "source.pdf")
	require.NoError(t, os.WriteFile(pdf, createDeterministicTestPDF(t), 0600))
	status, payload := sourceAdmissionMultipart(t, app, "/api/studio/v1/sessions/from-upload", pdf)
	require.Equal(t, 201, status, payload)
	path := filepath.Join(t.TempDir(), "image.png")
	file, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, png.Encode(file, image.NewRGBA(image.Rect(0, 0, 2, 2))))
	require.NoError(t, file.Close())
	for _, kind := range []string{"watermark_image", "signature_image"} {
		status, payload = sourceAdmissionMultipart(t, app, "/api/studio/v1/sessions/"+repo.session.ID.String()+"/assets", path, kind)
		require.Equal(t, 201, status, payload)
		require.Equal(t, kind, repo.asset.AssetType)
		require.Equal(t, "image/png", repo.asset.MimeType)
		require.FileExists(t, filepath.Join(storage.GetLocalStorageDir(), repo.asset.R2Key))
	}
	require.Equal(t, int64(20<<20), studioWatermarkImageMaxBytes)
	sourceAdmissionNoTemps(t)
}
