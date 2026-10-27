package studio

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/google/uuid"
	"image"
	"image/jpeg"
	"mime/multipart"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/studio/models"
	"pdfnest-backend/internal/studio/vdm"
	"runtime"
	"strings"
	"time"

	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/require"
	"pdfnest-backend/internal/worker"
)

type previewLimitTransport func(*http.Request) (*http.Response, error)

func (f previewLimitTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func previewLimitFile(t *testing.T, size int64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "synthetic.pdf")
	file, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(size)) // Sparse geometry; no large memory buffer.
	require.NoError(t, file.Close())
	return path
}

func TestStudioPreviewBodyLimitRejectsBeforeTransport(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("PDFNEST_WORKER_URL", "http://127.0.0.1:1")
	original := worker.Client
	t.Cleanup(func() { worker.Client = original })
	attempts := 0
	worker.Client = &http.Client{Transport: previewLimitTransport(func(req *http.Request) (*http.Response, error) {
		attempts++
		if err := worker.SignRequestWithSecret(req, "synthetic-test-secret"); err != nil {
			return nil, err
		}
		defer req.Body.Close()
		return nil, errors.New("unexpected transport")
	})}
	renderer := NewTileRenderer(nil).(*studioTileRenderer)
	_, err := renderer.defaultRenderWithWorker(context.Background(), previewLimitFile(t, worker.MaxRequestBodyBytes), 1, 72)
	require.Error(t, err)
	t.Logf("preview error: %v", err)
	require.Zero(t, attempts, "oversized source must be rejected before multipart signing/transport")
}

func TestStudioPreviewBodyLimitPublicResponse(t *testing.T) {
	t.Setenv("PDFNEST_WORKER_URL", "http://127.0.0.1:1")
	original := worker.Client
	t.Cleanup(func() { worker.Client = original })
	worker.Client = &http.Client{Transport: previewLimitTransport(func(req *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("stage worker request body: %w", worker.ErrRequestBodyTooLarge)
	})}
	renderer := NewTileRenderer(nil).(*studioTileRenderer)
	_, renderErr := renderer.defaultRenderWithWorker(context.Background(), previewLimitFile(t, 10), 1, 72)
	require.Error(t, renderErr)
	app := fiber.New()
	ctrl := &Controller{}
	app.Get("/tile", func(c *fiber.Ctx) error { return ctrl.mapError(c, renderErr) })
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/tile", nil))
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	t.Logf("HTTP %d: %s", resp.StatusCode, body)
	require.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
	require.Contains(t, string(body), "PREVIEW_SOURCE_TOO_LARGE")
}

// An embedded nil Repository makes unexpected database access fail the test.
// Only the actual preview read path is implemented; no database is required.
type previewLimitRepository struct {
	Repository
	session    *models.StudioSession
	version    *models.StudioVersion
	asset      *models.StudioAsset
	assetReads int
}

func (r *previewLimitRepository) GetSession(context.Context, uuid.UUID) (*models.StudioSession, error) {
	return r.session, nil
}
func (r *previewLimitRepository) GetVersion(context.Context, uuid.UUID) (*models.StudioVersion, error) {
	return r.version, nil
}
func (r *previewLimitRepository) GetAsset(context.Context, string) (*models.StudioAsset, error) {
	r.assetReads++
	return r.asset, nil
}

func previewLimitEndpoint(t *testing.T, sourceSize int64) (*fiber.App, *previewLimitRepository, string, string) {
	t.Helper()
	t.Setenv("APP_ENV", "test")
	t.Setenv("STORAGE_MODE", "local")
	root := t.TempDir()
	t.Setenv("LOCAL_STORAGE_DIR", root)
	source := previewLimitFile(t, sourceSize)
	key := "synthetic.pdf"
	require.NoError(t, os.Rename(source, filepath.Join(root, key)))
	owner := "synthetic-preview-owner"
	docID, sessionID, versionID := uuid.New(), uuid.New(), uuid.New()
	assetID := "synthetic-source"
	model := vdm.DocumentModel{PageCount: 1, Pages: []vdm.PageDescriptor{{PageID: "page-one", SourceAssetID: &assetID, SourcePageNumber: 1, Dimensions: &vdm.Dimensions{Width: 20, Height: 30}}}}
	raw, err := json.Marshal(model)
	require.NoError(t, err)
	repo := &previewLimitRepository{
		session: &models.StudioSession{ID: sessionID, DocumentID: docID, GuestTokenHash: HashGuestToken(owner), ExpiresAt: time.Now().Add(time.Hour)},
		version: &models.StudioVersion{ID: versionID, DocumentID: docID, VirtualModel: models.JSON(raw)},
		asset:   &models.StudioAsset{ID: assetID, DocumentID: docID, R2Key: key, ByteSize: sourceSize},
	}
	ctrl := &Controller{renderer: NewTileRenderer(repo)}
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		id := owner
		if c.Get("X-Test-Owner") != "" {
			id = c.Get("X-Test-Owner")
		}
		c.Locals(identity.LocalIdentityKey, identity.Identity{ID: id, Type: identity.TypeGuest})
		return c.Next()
	})
	RegisterRoutes(app.Group("/api"), ctrl)
	url := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/page-one/tile?scale=1", sessionID, versionID)
	return app, repo, url, root
}

func previewLimitReplaceClient(t *testing.T, transport previewLimitTransport) {
	t.Helper()
	t.Setenv("PDFNEST_WORKER_URL", "http://127.0.0.1:1")
	original := worker.Client
	t.Cleanup(func() { worker.Client = original })
	worker.Client = &http.Client{Transport: transport}
}

func TestStudioPreviewBodyLimitMultipartOverhead(t *testing.T) {
	// Reject even just below 128 MiB: the serialized body still cannot fit.
	for _, size := range []int64{worker.MaxRequestBodyBytes, worker.MaxRequestBodyBytes - 1, worker.MaxRequestBodyBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			spool := t.TempDir()
			t.Setenv("TMPDIR", spool)
			previewLimitReplaceClient(t, func(*http.Request) (*http.Response, error) {
				t.Error("must not start Worker request")
				return nil, errors.New("unexpected")
			})
			renderer := NewTileRenderer(nil).(*studioTileRenderer)
			source := previewLimitFile(t, size)
			_, err := renderer.defaultRenderWithWorker(context.Background(), source, 1, 72)
			require.ErrorIs(t, err, ErrPreviewSourceTooLarge)
			entries, err := os.ReadDir(spool)
			require.NoError(t, err)
			require.Empty(t, entries)
			assertPreviewLimitNoResources(t, filepath.Dir(source))
		})
	}
	// The same writer/filename/boundary must produce exactly the measured length.
	for _, name := range []string{"source.pdf", `quoted"file.pdf`, "unicode-λ.pdf"} {
		var framing, actual bytes.Buffer
		probe := multipart.NewWriter(&framing)
		require.NoError(t, writePreviewMultipart(probe, bytes.NewReader(nil), name, 123, 144.25))
		writer := multipart.NewWriter(&actual)
		require.NoError(t, writer.SetBoundary(probe.Boundary()))
		content := []byte("synthetic input")
		require.NoError(t, writePreviewMultipart(writer, bytes.NewReader(content), name, 123, 144.25))
		require.Equal(t, framing.Len()+len(content), actual.Len())
	}
}

func TestStudioPreviewBodyLimitEndpointAndOwnership(t *testing.T) {
	for _, scenario := range []string{"oversize", "wrong-session-owner", "wrong-asset-document", "expired-session", "wrong-version-document"} {
		t.Run(scenario, func(t *testing.T) {
			app, repo, url, _ := previewLimitEndpoint(t, worker.MaxRequestBodyBytes)
			previewLimitReplaceClient(t, func(*http.Request) (*http.Response, error) {
				t.Error("must not call Worker")
				return nil, errors.New("unexpected")
			})
			req := httptest.NewRequest(http.MethodGet, url, nil)
			expected := http.StatusRequestEntityTooLarge
			switch scenario {
			case "wrong-session-owner":
				req.Header.Set("X-Test-Owner", "someone-else")
				expected = http.StatusForbidden
			case "wrong-asset-document":
				repo.asset.DocumentID = uuid.New()
				expected = http.StatusForbidden
			case "expired-session":
				repo.session.ExpiresAt = time.Now().Add(-time.Hour)
				expected = http.StatusUnauthorized
			case "wrong-version-document":
				repo.version.DocumentID = uuid.New()
				expected = http.StatusBadRequest
			}
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, expected, resp.StatusCode)
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			if scenario == "oversize" {
				var payload map[string]string
				require.NoError(t, json.Unmarshal(body, &payload))
				require.Equal(t, map[string]string{"code": "PREVIEW_SOURCE_TOO_LARGE", "error": "This PDF is too large for the current preview processing limit."}, payload)
				require.NotContains(t, string(body), repo.asset.R2Key)
			}
			if scenario == "wrong-session-owner" || scenario == "expired-session" || scenario == "wrong-version-document" {
				require.Zero(t, repo.assetReads)
			}
		})
	}
}

func TestStudioPreviewBodyLimitSmallSignedPreview(t *testing.T) {
	spool := t.TempDir()
	t.Setenv("TMPDIR", spool)
	pdf := createDeterministicTestPDF(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/api/v1/render/page" {
			t.Error("unexpected endpoint")
			w.WriteHeader(404)
			return
		}
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		digest := sha256.Sum256(raw)
		require.Equal(t, hex.EncodeToString(digest[:]), req.Header.Get("X-Worker-Body-Hash"))
		canonical := strings.Join([]string{req.Method, req.URL.RequestURI(), req.Header.Get("X-Worker-Timestamp"), req.Header.Get("X-Worker-Nonce"), req.Header.Get("X-Worker-Body-Hash")}, "\n")
		mac := hmac.New(sha256.New, []byte("synthetic-secret"))
		mac.Write([]byte(canonical))
		require.Equal(t, hex.EncodeToString(mac.Sum(nil)), req.Header.Get("X-Worker-Signature"))
		req.Body = io.NopCloser(bytes.NewReader(raw))
		require.NoError(t, req.ParseMultipartForm(1<<20))
		defer req.MultipartForm.RemoveAll()
		f, _, err := req.FormFile("file")
		require.NoError(t, err)
		defer f.Close()
		received, err := io.ReadAll(f)
		require.NoError(t, err)
		require.Equal(t, pdf, received)
		require.Equal(t, "1", req.FormValue("page"))
		require.Equal(t, "72.00", req.FormValue("dpi"))
		require.NoError(t, jpeg.Encode(w, image.NewRGBA(image.Rect(0, 0, 20, 30)), nil))
	}))
	defer server.Close()
	t.Setenv("PDFNEST_WORKER_URL", server.URL)
	t.Setenv("WORKER_SHARED_SECRET", "synthetic-secret")
	app, _, url, root := previewLimitEndpoint(t, int64(len(pdf)))
	require.NoError(t, os.WriteFile(filepath.Join(root, "synthetic.pdf"), pdf, 0600))
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, url, nil), 5000)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	img, err := jpeg.Decode(resp.Body)
	require.NoError(t, err)
	require.Equal(t, image.Rect(0, 0, 20, 30), img.Bounds())
	entries, err := os.ReadDir(spool)
	require.NoError(t, err)
	require.Empty(t, entries)
	assertPreviewLimitNoResources(t, root)
}

func TestStudioPreviewBodyLimitFinalSignerAndErrorClasses(t *testing.T) {
	for _, tc := range []struct {
		name         string
		transportErr error
		status       int
		expected     int
		code         string
	}{
		{"signer-size", worker.ErrRequestBodyTooLarge, 0, 413, "PREVIEW_SOURCE_TOO_LARGE"},
		{"spool-capacity", worker.ErrBodySpoolCapacity, 0, 500, ""},
		{"timeout", context.DeadlineExceeded, 0, 504, "RENDER_TIMEOUT"},
		{"cancelled", context.Canceled, 0, 500, ""},
		{"ordinary", errors.New("synthetic render failure"), 0, 500, ""},
		{"worker-size", nil, 413, 413, "PREVIEW_SOURCE_TOO_LARGE"},
		{"busy", nil, 429, 429, "RENDER_WORKER_BUSY"},
		{"unavailable", nil, 503, 429, "RENDER_WORKER_BUSY"},
		{"worker-timeout", nil, 504, 504, "RENDER_TIMEOUT"},
		{"invalid-source", nil, 422, 500, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, _, url, root := previewLimitEndpoint(t, 100)
			previewLimitReplaceClient(t, func(req *http.Request) (*http.Response, error) {
				// Deliberately do not read the pipe: all early failures must unblock/join it.
				if tc.transportErr != nil {
					return nil, fmt.Errorf("synthetic private path /private/input.pdf: %w", tc.transportErr)
				}
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("synthetic private Worker detail"))}, nil
			})
			resp, err := app.Test(httptest.NewRequest(http.MethodGet, url, nil), 5000)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, tc.expected, resp.StatusCode)
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			if tc.code != "" {
				require.Contains(t, string(body), tc.code)
			}
			if tc.expected == 413 {
				require.NotContains(t, string(body), "/private/")
				require.NotContains(t, string(body), "Worker detail")
			}
			assertPreviewLimitNoResources(t, root)
		})
	}
}

func TestStudioPreviewBodyLimitFileGrowthSignerCleanup(t *testing.T) {
	spool := t.TempDir()
	t.Setenv("TMPDIR", spool)
	source := previewLimitFile(t, 1)
	previewLimitReplaceClient(t, func(req *http.Request) (*http.Response, error) {
		// Controlled growth after Stat exercises the real final signer safeguard.
		require.NoError(t, os.Truncate(source, worker.MaxRequestBodyBytes))
		return nil, worker.SignRequestWithSecret(req, "synthetic-test-secret")
	})
	renderer := NewTileRenderer(nil).(*studioTileRenderer)
	_, err := renderer.defaultRenderWithWorker(context.Background(), source, 1, 72)
	require.ErrorIs(t, err, ErrPreviewSourceTooLarge)
	require.ErrorIs(t, err, worker.ErrRequestBodyTooLarge)
	entries, err := os.ReadDir(spool)
	require.NoError(t, err)
	require.Empty(t, entries)
	assertPreviewLimitNoResources(t, filepath.Dir(source))
}

func assertPreviewLimitNoResources(t *testing.T, root string) {
	t.Helper()
	// Linux descriptor evidence is scoped to this test's files, avoiding unrelated
	// runtime/network descriptors. The producer is joined before returning.
	if fds, err := os.ReadDir("/proc/self/fd"); err == nil {
		for _, fd := range fds {
			target, _ := os.Readlink(filepath.Join("/proc/self/fd", fd.Name()))
			require.False(t, strings.HasPrefix(target, root+string(os.PathSeparator)), "leaked input descriptor: %s", target)
		}
	}
	stack := make([]byte, 1<<20)
	n := runtime.Stack(stack, true)
	require.NotContains(t, string(stack[:n]), "studio.writePreviewMultipart(", "multipart producer must be joined")
}

func TestStudioPreviewBodyLimitExactEnvelopeBoundary(t *testing.T) {
	for _, extra := range []int64{0, 1} {
		t.Run(fmt.Sprint(extra), func(t *testing.T) {
			var framing bytes.Buffer
			probe := multipart.NewWriter(&framing)
			require.NoError(t, writePreviewMultipart(probe, bytes.NewReader(nil), "synthetic.pdf", 1, 72))
			// Multipart's generated boundaries have a fixed length; our actual writer
			// shares its probe's exact value, not merely the same boundary length.
			source := previewLimitFile(t, worker.MaxRequestBodyBytes-int64(framing.Len())+extra)
			called := false
			previewLimitReplaceClient(t, func(*http.Request) (*http.Response, error) { called = true; return nil, worker.ErrBodySpoolCapacity })
			renderer := NewTileRenderer(nil).(*studioTileRenderer)
			_, err := renderer.defaultRenderWithWorker(context.Background(), source, 1, 72)
			if extra == 0 {
				require.True(t, called)
				require.ErrorIs(t, err, worker.ErrBodySpoolCapacity)
			} else {
				require.False(t, called)
				require.ErrorIs(t, err, ErrPreviewSourceTooLarge)
			}
			assertPreviewLimitNoResources(t, filepath.Dir(source))
		})
	}
}

func TestStudioPreviewBodyLimitCancellationAndBuildFailureCleanup(t *testing.T) {
	for _, scenario := range []string{"cancel-during-signing", "invalid-worker-url"} {
		t.Run(scenario, func(t *testing.T) {
			spool := t.TempDir()
			t.Setenv("TMPDIR", spool)
			source := previewLimitFile(t, 128<<10)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			called := false
			previewLimitReplaceClient(t, func(req *http.Request) (*http.Response, error) {
				called = true
				cancel() // Deterministic cancellation before signing reads the pipe.
				return nil, worker.SignRequestWithSecret(req, "synthetic-secret")
			})
			if scenario == "invalid-worker-url" {
				t.Setenv("PDFNEST_WORKER_URL", "://invalid")
			}
			renderer := NewTileRenderer(nil).(*studioTileRenderer)
			_, err := renderer.defaultRenderWithWorker(ctx, source, 1, 72)
			require.Error(t, err)
			if scenario == "cancel-during-signing" {
				require.ErrorIs(t, err, context.Canceled)
				require.True(t, called)
			} else {
				require.False(t, called)
			}
			entries, err := os.ReadDir(spool)
			require.NoError(t, err)
			require.Empty(t, entries)
			assertPreviewLimitNoResources(t, filepath.Dir(source))
		})
	}
}
