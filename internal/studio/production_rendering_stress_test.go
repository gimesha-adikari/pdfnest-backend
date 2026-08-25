package studio

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/studio/vdm"
)

// setupMockPyMuPDFWorkerServer spins up an authenticated HTTP mock worker server
func setupMockPyMuPDFWorkerServer(t *testing.T, secret string) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/render/page" {
			http.NotFound(w, r)
			return
		}

		sig := r.Header.Get("X-Worker-Signature")
		ts := r.Header.Get("X-Worker-Timestamp")
		nonce := r.Header.Get("X-Worker-Nonce")
		if sig == "" || ts == "" || nonce == "" {
			http.Error(w, "missing worker authentication", http.StatusUnauthorized)
			return
		}

		stringToSign := fmt.Sprintf("%s\n%s\n%s\n%s", r.Method, r.URL.Path, ts, nonce)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(stringToSign))
		if sig != hex.EncodeToString(mac.Sum(nil)) {
			http.Error(w, "invalid worker signature", http.StatusUnauthorized)
			return
		}

		if err := r.ParseMultipartForm(10 * 1024 * 1024); err != nil {
			http.Error(w, "invalid multipart form", http.StatusBadRequest)
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "missing file", http.StatusBadRequest)
			return
		}
		defer file.Close()
		pdfBytes, _ := io.ReadAll(file)
		pageNum, _ := strconv.Atoi(r.FormValue("page"))
		if pageNum < 1 {
			pageNum = 1
		}
		dpi, _ := strconv.ParseFloat(r.FormValue("dpi"), 64)
		if dpi <= 0 {
			dpi = 72.0
		}

		jpegBytes, err := renderPdfWithPyMuPDF(pdfBytes, pageNum, dpi)
		if err != nil {
			http.Error(w, fmt.Sprintf("render error: %v", err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(jpegBytes)
	}))
	return server
}

func TestStress_IdenticalConcurrentTilesSingleflight(t *testing.T) {
	secret := "stress-secret-" + uuid.New().String()
	t.Setenv("WORKER_SHARED_SECRET", secret)
	workerServer := setupMockPyMuPDFWorkerServer(t, secret)
	defer workerServer.Close()
	t.Setenv("PDFNEST_WORKER_URL", workerServer.URL)

	app, _, _, renderer, _ := setupTestApp(t)
	guestID := "guest_stress_sf_" + uuid.New().String()

	pdfBytes := createDeterministicTestPDF(t)
	pdfKey := fmt.Sprintf("stress_sf_%s.pdf", uuid.New().String())
	storageDir := storage.GetLocalStorageDir()
	_ = os.MkdirAll(storageDir, 0755)
	localPdfPath := filepath.Join(storageDir, pdfKey)
	_ = os.WriteFile(localPdfPath, pdfBytes, 0644)
	defer os.Remove(localPdfPath)

	assetID := "ast_" + uuid.New().String()
	p1ID := uuid.New().String()

	createBody, _ := json.Marshal(CreateSessionRequest{
		FileName:         "stress_sf.pdf",
		FileSize:         int64(len(pdfBytes)),
		InitialPageCount: 1,
		SourceAssetID:    assetID,
		SourceR2Key:      pdfKey,
		InitialVDM: vdm.DocumentModel{
			PageCount: 1,
			Pages: []vdm.PageDescriptor{
				{
					PageID:           p1ID,
					SourceAssetID:    &assetID,
					SourcePageNumber: 1,
					Dimensions:       &vdm.Dimensions{Width: 595.28, Height: 841.89},
					Rotation:         0,
					IsBlank:          false,
				},
			},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, err := app.Test(req, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	var initResp struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
		ActiveVersion struct {
			ID string `json:"id"`
		} `json:"active_version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&initResp)
	sessionID := initResp.Session.ID
	v0ID := initResp.ActiveVersion.ID

	// Fire 30 concurrent requests for the exact same tile on cold cache
	concurrency := 30
	tileURL := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.0&tile_x=50&tile_y=100&tile_w=150&tile_h=100", sessionID, v0ID, p1ID)

	var wg sync.WaitGroup
	startBarrier := make(chan struct{})
	statusCodes := make([]int, concurrency)
	payloads := make([][]byte, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-startBarrier
			r := httptest.NewRequest(http.MethodGet, tileURL, nil)
			r.Header.Set("X-Test-Guest-ID", guestID)
			res, testErr := app.Test(r, 10000)
			if testErr == nil {
				statusCodes[idx] = res.StatusCode
				b, _ := io.ReadAll(res.Body)
				payloads[idx] = b
			}
		}(i)
	}

	close(startBarrier)
	wg.Wait()

	for i := 0; i < concurrency; i++ {
		assert.Equal(t, http.StatusOK, statusCodes[i])
		assert.True(t, len(payloads[i]) > 0)
		assert.Equal(t, payloads[0], payloads[i], "All concurrent requests must return byte-identical JPEGs")
	}

	metrics := renderer.GetMetrics()
	// Singleflight must coalesce 30 concurrent requests into strictly 1 underlying render
	assert.Equal(t, uint64(1), metrics.UnderlyingRenders, "Strict singleflight: exactly 1 underlying render for 30 concurrent identical requests")
	assert.True(t, metrics.SingleflightCoalesced > 0, "Singleflight coalesced metric must be incremented")
}

func TestStress_DifferentSubTilesConcurrentLoad(t *testing.T) {
	secret := "stress-diff-tiles-" + uuid.New().String()
	t.Setenv("WORKER_SHARED_SECRET", secret)
	workerServer := setupMockPyMuPDFWorkerServer(t, secret)
	defer workerServer.Close()
	t.Setenv("PDFNEST_WORKER_URL", workerServer.URL)

	app, _, _, _, _ := setupTestApp(t)
	guestID := "guest_diff_tiles_" + uuid.New().String()

	pdfBytes := createDeterministicTestPDF(t)
	pdfKey := fmt.Sprintf("diff_tiles_%s.pdf", uuid.New().String())
	storageDir := storage.GetLocalStorageDir()
	_ = os.MkdirAll(storageDir, 0755)
	localPdfPath := filepath.Join(storageDir, pdfKey)
	_ = os.WriteFile(localPdfPath, pdfBytes, 0644)
	defer os.Remove(localPdfPath)

	assetID := "ast_" + uuid.New().String()
	p1ID := uuid.New().String()

	createBody, _ := json.Marshal(CreateSessionRequest{
		FileName:         "diff_tiles.pdf",
		FileSize:         int64(len(pdfBytes)),
		InitialPageCount: 1,
		SourceAssetID:    assetID,
		SourceR2Key:      pdfKey,
		InitialVDM: vdm.DocumentModel{
			PageCount: 1,
			Pages: []vdm.PageDescriptor{
				{
					PageID:           p1ID,
					SourceAssetID:    &assetID,
					SourcePageNumber: 1,
					Dimensions:       &vdm.Dimensions{Width: 595.28, Height: 841.89},
					Rotation:         0,
					IsBlank:          false,
				},
			},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ := app.Test(req, 5000)
	var initResp struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
		ActiveVersion struct {
			ID string `json:"id"`
		} `json:"active_version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&initResp)
	sessionID := initResp.Session.ID
	v0ID := initResp.ActiveVersion.ID

	// 12 distinct tiles across a grid (x: 0, 150, 300, y: 0, 200, 400, 600)
	type tileSpec struct {
		x, y, w, h int
	}
	specs := []tileSpec{
		{0, 0, 150, 150},
		{150, 0, 150, 150},
		{300, 0, 150, 150},
		{0, 200, 150, 150},
		{150, 200, 150, 150},
		{300, 200, 150, 150},
		{0, 400, 150, 150},
		{150, 400, 150, 150},
		{300, 400, 150, 150},
		{0, 600, 150, 150},
		{150, 600, 150, 150},
		{300, 600, 150, 150},
	}

	var wg sync.WaitGroup
	results := make([]int, len(specs))

	for i, sp := range specs {
		wg.Add(1)
		go func(idx int, s tileSpec) {
			defer wg.Done()
			tileURL := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.0&tile_x=%d&tile_y=%d&tile_w=%d&tile_h=%d", sessionID, v0ID, p1ID, s.x, s.y, s.w, s.h)
			r := httptest.NewRequest(http.MethodGet, tileURL, nil)
			r.Header.Set("X-Test-Guest-ID", guestID)
			res, err := app.Test(r, 10000)
			if err == nil {
				results[idx] = res.StatusCode
				b, _ := io.ReadAll(res.Body)
				img, decErr := jpeg.Decode(bytes.NewReader(b))
				if decErr == nil && img.Bounds().Dx() == s.w && img.Bounds().Dy() == s.h {
					results[idx] = http.StatusOK
				} else {
					results[idx] = http.StatusInternalServerError
				}
			}
		}(i, sp)
	}
	wg.Wait()

	for i := range specs {
		assert.Equal(t, http.StatusOK, results[i], "Sub-tile %d must render successfully with exact bounds", i)
	}
}

func TestStress_MultiPageAndMultiDocumentConcurrentLoad(t *testing.T) {
	secret := "stress-multidoc-" + uuid.New().String()
	t.Setenv("WORKER_SHARED_SECRET", secret)
	workerServer := setupMockPyMuPDFWorkerServer(t, secret)
	defer workerServer.Close()
	t.Setenv("PDFNEST_WORKER_URL", workerServer.URL)

	app, _, _, _, _ := setupTestApp(t)

	// Create 2 independent documents with 2 pages each
	createDoc := func(docIdx int) (string, string, string, string) {
		guestID := fmt.Sprintf("guest_doc_%d_%s", docIdx, uuid.New().String())
		pdfBytes := createDeterministicTestPDF(t)
		pdfKey := fmt.Sprintf("multidoc_%d_%s.pdf", docIdx, uuid.New().String())
		storageDir := storage.GetLocalStorageDir()
		_ = os.MkdirAll(storageDir, 0755)
		localPdfPath := filepath.Join(storageDir, pdfKey)
		_ = os.WriteFile(localPdfPath, pdfBytes, 0644)

		assetID := fmt.Sprintf("ast_%d_%s", docIdx, uuid.New().String())
		p1ID := uuid.New().String()
		p2ID := uuid.New().String()

		createBody, _ := json.Marshal(CreateSessionRequest{
			FileName:         fmt.Sprintf("doc_%d.pdf", docIdx),
			FileSize:         int64(len(pdfBytes)),
			InitialPageCount: 2,
			SourceAssetID:    assetID,
			SourceR2Key:      pdfKey,
			InitialVDM: vdm.DocumentModel{
				PageCount: 2,
				Pages: []vdm.PageDescriptor{
					{
						PageID:           p1ID,
						SourceAssetID:    &assetID,
						SourcePageNumber: 1,
						Dimensions:       &vdm.Dimensions{Width: 595.28, Height: 841.89},
						Rotation:         0,
						IsBlank:          false,
					},
					{
						PageID:           p2ID,
						SourceAssetID:    &assetID,
						SourcePageNumber: 2,
						Dimensions:       &vdm.Dimensions{Width: 595.28, Height: 841.89},
						Rotation:         0,
						IsBlank:          false,
					},
				},
			},
		})

		req := httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions", bytes.NewReader(createBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Test-Guest-ID", guestID)
		resp, _ := app.Test(req, 5000)
		var initResp struct {
			Session struct {
				ID string `json:"id"`
			} `json:"session"`
			ActiveVersion struct {
				ID string `json:"id"`
			} `json:"active_version"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&initResp)
		return guestID, initResp.Session.ID, initResp.ActiveVersion.ID, p1ID
	}

	g1, s1, v1, p1 := createDoc(1)
	g2, s2, v2, p2 := createDoc(2)

	// Fire concurrent requests targeting both documents simultaneously
	var wg sync.WaitGroup
	success1 := int32(0)
	success2 := int32(0)

	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			url := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.0", s1, v1, p1)
			r := httptest.NewRequest(http.MethodGet, url, nil)
			r.Header.Set("X-Test-Guest-ID", g1)
			res, err := app.Test(r, 10000)
			if err == nil && res.StatusCode == http.StatusOK {
				atomic.AddInt32(&success1, 1)
			}
		}()

		go func() {
			defer wg.Done()
			url := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.0", s2, v2, p2)
			r := httptest.NewRequest(http.MethodGet, url, nil)
			r.Header.Set("X-Test-Guest-ID", g2)
			res, err := app.Test(r, 10000)
			if err == nil && res.StatusCode == http.StatusOK {
				atomic.AddInt32(&success2, 1)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(10), success1, "All document 1 requests must succeed")
	assert.Equal(t, int32(10), success2, "All document 2 requests must succeed")
}

func TestStress_WorkerSaturationAndQueueFull429Propagation(t *testing.T) {
	// Setup a mock worker that returns HTTP 429 RENDER_QUEUE_FULL
	saturatedWorker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"code":"RENDER_QUEUE_FULL","message":"Render capacity saturated."}`))
	}))
	defer saturatedWorker.Close()

	t.Setenv("PDFNEST_WORKER_URL", saturatedWorker.URL)
	t.Setenv("WORKER_SHARED_SECRET", "test-secret")

	app, _, _, renderer, _ := setupTestApp(t)
	guestID := "guest_sat_" + uuid.New().String()

	pdfBytes := createDeterministicTestPDF(t)
	pdfKey := fmt.Sprintf("sat_%s.pdf", uuid.New().String())
	storageDir := storage.GetLocalStorageDir()
	_ = os.MkdirAll(storageDir, 0755)
	localPdfPath := filepath.Join(storageDir, pdfKey)
	_ = os.WriteFile(localPdfPath, pdfBytes, 0644)
	defer os.Remove(localPdfPath)

	assetID := "ast_" + uuid.New().String()
	p1ID := uuid.New().String()

	createBody, _ := json.Marshal(CreateSessionRequest{
		FileName:         "sat.pdf",
		FileSize:         int64(len(pdfBytes)),
		InitialPageCount: 1,
		SourceAssetID:    assetID,
		SourceR2Key:      pdfKey,
		InitialVDM: vdm.DocumentModel{
			PageCount: 1,
			Pages: []vdm.PageDescriptor{
				{
					PageID:           p1ID,
					SourceAssetID:    &assetID,
					SourcePageNumber: 1,
					Dimensions:       &vdm.Dimensions{Width: 595.28, Height: 841.89},
					Rotation:         0,
					IsBlank:          false,
				},
			},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ := app.Test(req, 5000)
	var initResp struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
		ActiveVersion struct {
			ID string `json:"id"`
		} `json:"active_version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&initResp)
	sessionID := initResp.Session.ID
	v0ID := initResp.ActiveVersion.ID

	// Request tile when worker is saturated -> must return HTTP 429 Too Many Requests
	tileURL := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.0", sessionID, v0ID, p1ID)
	r := httptest.NewRequest(http.MethodGet, tileURL, nil)
	r.Header.Set("X-Test-Guest-ID", guestID)
	res, err := app.Test(r, 5000)
	require.NoError(t, err)

	assert.Equal(t, http.StatusTooManyRequests, res.StatusCode, "Saturated worker 429 must propagate to client as 429")
	assert.Equal(t, "2", res.Header.Get("Retry-After"), "Retry-After header must be set")

	metrics := renderer.GetMetrics()
	assert.True(t, metrics.WorkerRejections > 0, "WorkerRejections metric must be tracked")
}

func TestStress_CorruptPDFHandlingAndErrorMetrics(t *testing.T) {
	app, _, _, renderer, _ := setupTestApp(t)
	guestID := "guest_corrupt_" + uuid.New().String()

	// Write corrupt bytes
	pdfKey := fmt.Sprintf("corrupt_%s.pdf", uuid.New().String())
	storageDir := storage.GetLocalStorageDir()
	_ = os.MkdirAll(storageDir, 0755)
	localPdfPath := filepath.Join(storageDir, pdfKey)
	_ = os.WriteFile(localPdfPath, []byte("NOT_A_VALID_PDF_HEADER_CORRUPT"), 0644)
	defer os.Remove(localPdfPath)

	assetID := "ast_" + uuid.New().String()
	p1ID := uuid.New().String()

	createBody, _ := json.Marshal(CreateSessionRequest{
		FileName:         "corrupt.pdf",
		FileSize:         100,
		InitialPageCount: 1,
		SourceAssetID:    assetID,
		SourceR2Key:      pdfKey,
		InitialVDM: vdm.DocumentModel{
			PageCount: 1,
			Pages: []vdm.PageDescriptor{
				{
					PageID:           p1ID,
					SourceAssetID:    &assetID,
					SourcePageNumber: 1,
					Dimensions:       &vdm.Dimensions{Width: 595.28, Height: 841.89},
					Rotation:         0,
					IsBlank:          false,
				},
			},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ := app.Test(req, 5000)
	var initResp struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
		ActiveVersion struct {
			ID string `json:"id"`
		} `json:"active_version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&initResp)
	sessionID := initResp.Session.ID
	v0ID := initResp.ActiveVersion.ID

	// Mock renderer that returns error on corrupt PDF
	renderer.SetWorkerRenderer(func(ctx context.Context, pdfPath string, pageNumber int, dpi float64) (image.Image, error) {
		return nil, fmt.Errorf("fitz: cannot open broken document")
	})

	tileURL := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.0", sessionID, v0ID, p1ID)
	r := httptest.NewRequest(http.MethodGet, tileURL, nil)
	r.Header.Set("X-Test-Guest-ID", guestID)
	res, err := app.Test(r, 5000)
	require.NoError(t, err)

	assert.Equal(t, http.StatusInternalServerError, res.StatusCode, "Corrupt PDF render must return 500")
	metrics := renderer.GetMetrics()
	assert.True(t, metrics.RenderErrors > 0, "RenderErrors metric must be incremented on corruption failure")
}

func TestStress_ScaleProgression_1To8(t *testing.T) {
	secret := "stress-scales-" + uuid.New().String()
	t.Setenv("WORKER_SHARED_SECRET", secret)
	workerServer := setupMockPyMuPDFWorkerServer(t, secret)
	defer workerServer.Close()
	t.Setenv("PDFNEST_WORKER_URL", workerServer.URL)

	app, _, _, _, _ := setupTestApp(t)
	guestID := "guest_scales_" + uuid.New().String()

	pdfBytes := createDeterministicTestPDF(t)
	pdfKey := fmt.Sprintf("scales_%s.pdf", uuid.New().String())
	storageDir := storage.GetLocalStorageDir()
	_ = os.MkdirAll(storageDir, 0755)
	localPdfPath := filepath.Join(storageDir, pdfKey)
	_ = os.WriteFile(localPdfPath, pdfBytes, 0644)
	defer os.Remove(localPdfPath)

	assetID := "ast_" + uuid.New().String()
	p1ID := uuid.New().String()

	createBody, _ := json.Marshal(CreateSessionRequest{
		FileName:         "scales.pdf",
		FileSize:         int64(len(pdfBytes)),
		InitialPageCount: 1,
		SourceAssetID:    assetID,
		SourceR2Key:      pdfKey,
		InitialVDM: vdm.DocumentModel{
			PageCount: 1,
			Pages: []vdm.PageDescriptor{
				{
					PageID:           p1ID,
					SourceAssetID:    &assetID,
					SourcePageNumber: 1,
					Dimensions:       &vdm.Dimensions{Width: 595.28, Height: 841.89},
					Rotation:         0,
					IsBlank:          false,
				},
			},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ := app.Test(req, 5000)
	var initResp struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
		ActiveVersion struct {
			ID string `json:"id"`
		} `json:"active_version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&initResp)
	sessionID := initResp.Session.ID
	v0ID := initResp.ActiveVersion.ID

	// Test scale progression: 1.0, 2.0, 4.0, 8.0 with a 200x200 tile
	scales := []float64{1.0, 2.0, 4.0, 8.0}
	for _, sc := range scales {
		tileURL := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=%.1f&tile_x=50&tile_y=50&tile_w=200&tile_h=200", sessionID, v0ID, p1ID, sc)
		r := httptest.NewRequest(http.MethodGet, tileURL, nil)
		r.Header.Set("X-Test-Guest-ID", guestID)
		res, err := app.Test(r, 30000)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, res.StatusCode, "Scale %.1f must render successfully", sc)

		b, _ := io.ReadAll(res.Body)
		img, err := jpeg.Decode(bytes.NewReader(b))
		require.NoError(t, err)
		assert.Equal(t, 200, img.Bounds().Dx())
		assert.Equal(t, 200, img.Bounds().Dy())
	}
}

func TestStress_MetricsEndpointObservability(t *testing.T) {
	app, _, _, _, _ := setupTestApp(t)
	guestID := "guest_metrics_" + uuid.New().String()

	req := httptest.NewRequest(http.MethodGet, "/api/studio/v1/preview/metrics", nil)
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, err := app.Test(req, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var metrics TileMetrics
	err = json.NewDecoder(resp.Body).Decode(&metrics)
	require.NoError(t, err)

	assert.GreaterOrEqual(t, metrics.TotalRequests, uint64(0))
	assert.GreaterOrEqual(t, metrics.CacheHits, uint64(0))
	assert.GreaterOrEqual(t, metrics.CacheMisses, uint64(0))
	assert.GreaterOrEqual(t, metrics.UnderlyingRenders, uint64(0))
}

func TestStress_HugePageClipSafetyAndMaxTileDimension(t *testing.T) {
	secret := "stress-huge-" + uuid.New().String()
	t.Setenv("WORKER_SHARED_SECRET", secret)

	// Launch worker server backed by PyMuPDF supporting clip
	workerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(10 * 1024 * 1024)
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "missing file", http.StatusBadRequest)
			return
		}
		defer file.Close()
		pdfBytes, _ := io.ReadAll(file)
		pageNum, _ := strconv.Atoi(r.FormValue("page"))
		if pageNum < 1 {
			pageNum = 1
		}
		dpi, _ := strconv.ParseFloat(r.FormValue("dpi"), 64)
		if dpi <= 0 {
			dpi = 72.0
		}

		jpegBytes, err := renderPdfWithPyMuPDF(pdfBytes, pageNum, dpi)
		if err != nil {
			http.Error(w, fmt.Sprintf("render error: %v", err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(jpegBytes)
	}))
	defer workerServer.Close()
	t.Setenv("PDFNEST_WORKER_URL", workerServer.URL)

	app, _, _, _, _ := setupTestApp(t)
	guestID := "guest_huge_" + uuid.New().String()

	// 48 x 36 inch page fixture (3456 x 2592 pt)
	cmd := exec.Command("./.venv/bin/python", "-c", `
import pymupdf as fitz
doc = fitz.open()
page = doc.new_page(width=3456, height=2592)
page.draw_rect(fitz.Rect(100, 100, 200, 200), color=(0, 1, 0), fill=(0, 1, 0))
print(doc.tobytes().hex())
`)
	cmd.Dir = "/home/gimesha/My_Projects/platen/pdfnest-worker"
	out, err := cmd.Output()
	require.NoError(t, err)
	hugePdfHex := string(bytes.TrimSpace(out))
	hugePdfBytes, err := hex.DecodeString(hugePdfHex)
	require.NoError(t, err)

	pdfKey := fmt.Sprintf("huge_%s.pdf", uuid.New().String())
	storageDir := storage.GetLocalStorageDir()
	_ = os.MkdirAll(storageDir, 0755)
	localPdfPath := filepath.Join(storageDir, pdfKey)
	_ = os.WriteFile(localPdfPath, hugePdfBytes, 0644)
	defer os.Remove(localPdfPath)

	assetID := "ast_huge_" + uuid.New().String()
	p1ID := uuid.New().String()

	createBody, _ := json.Marshal(CreateSessionRequest{
		FileName:         "huge_blueprint.pdf",
		FileSize:         int64(len(hugePdfBytes)),
		InitialPageCount: 1,
		SourceAssetID:    assetID,
		SourceR2Key:      pdfKey,
		InitialVDM: vdm.DocumentModel{
			PageCount: 1,
			Pages: []vdm.PageDescriptor{
				{
					PageID:           p1ID,
					SourceAssetID:    &assetID,
					SourcePageNumber: 1,
					Dimensions:       &vdm.Dimensions{Width: 3456.0, Height: 2592.0},
					Rotation:         0,
					IsBlank:          false,
				},
			},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ := app.Test(req, 5000)
	var initResp struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
		ActiveVersion struct {
			ID string `json:"id"`
		} `json:"active_version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&initResp)
	sessionID := initResp.Session.ID
	v0ID := initResp.ActiveVersion.ID

	// 1. Valid sub-tile request on huge page at Scale 1.0 (tile 256x256 covering marker)
	tileURL := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.0&tile_x=100&tile_y=100&tile_w=256&tile_h=256", sessionID, v0ID, p1ID)
	r := httptest.NewRequest(http.MethodGet, tileURL, nil)
	r.Header.Set("X-Test-Guest-ID", guestID)
	res, err := app.Test(r, 10000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, res.StatusCode)

	b, _ := io.ReadAll(res.Body)
	img, err := jpeg.Decode(bytes.NewReader(b))
	require.NoError(t, err)
	assert.Equal(t, 256, img.Bounds().Dx())
	assert.Equal(t, 256, img.Bounds().Dy())

	// 2. Oversized tile dimension (> 4096px) must be rejected with 400
	oversizedURL := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.0&tile_x=0&tile_y=0&tile_w=5000&tile_h=5000", sessionID, v0ID, p1ID)
	rOver := httptest.NewRequest(http.MethodGet, oversizedURL, nil)
	rOver.Header.Set("X-Test-Guest-ID", guestID)
	resOver, err := app.Test(rOver, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, resOver.StatusCode, "Tile > 4096px must be rejected with HTTP 400")

	// 3. Out-of-bounds coordinate must be rejected with 400
	oobURL := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.0&tile_x=50000&tile_y=0&tile_w=256&tile_h=256", sessionID, v0ID, p1ID)
	rOob := httptest.NewRequest(http.MethodGet, oobURL, nil)
	rOob.Header.Set("X-Test-Guest-ID", guestID)
	resOob, err := app.Test(rOob, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, resOob.StatusCode, "Out of bounds tile coordinates must be rejected with HTTP 400")
}
