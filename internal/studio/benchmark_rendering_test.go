package studio

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/studio/vdm"
)

func createSolidTestJpeg(w, h int, r, g, b uint8) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: r, G: g, B: b, A: 255}}, image.Point{}, draw.Src)
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85})
	return buf.Bytes()
}

// TestBenchmark_ModeB_WarmCacheVsColdCache benchmarks cold cache vs warm cache latency
func TestBenchmark_ModeB_WarmCacheVsColdCache(t *testing.T) {
	secret := "bench-warm-" + uuid.New().String()
	t.Setenv("WORKER_SHARED_SECRET", secret)

	var workerCalls uint64
	workerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddUint64(&workerCalls, 1)
		jpegBytes := createSolidTestJpeg(256, 256, 100, 150, 200)
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(jpegBytes)
	}))
	defer workerServer.Close()
	t.Setenv("PDFNEST_WORKER_URL", workerServer.URL)

	app, _, _, _, _ := setupTestApp(t)
	guestID := "guest_warm_" + uuid.New().String()

	pdfBytes := []byte("%PDF-1.4 dummy pdf bytes")
	pdfKey := fmt.Sprintf("warm_%s.pdf", uuid.New().String())
	storageDir := storage.GetLocalStorageDir()
	_ = os.MkdirAll(storageDir, 0755)
	localPdfPath := filepath.Join(storageDir, pdfKey)
	_ = os.WriteFile(localPdfPath, pdfBytes, 0644)
	defer os.Remove(localPdfPath)

	assetID := "ast_warm_" + uuid.New().String()
	p1ID := uuid.New().String()

	createBody, _ := json.Marshal(CreateSessionRequest{
		FileName:         "warm_bench.pdf",
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
					Dimensions:       &vdm.Dimensions{Width: 595.0, Height: 842.0},
					Rotation:         0,
					IsBlank:          false,
				},
			},
		},
	})

	reqInit := httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions", bytes.NewReader(createBody))
	reqInit.Header.Set("Content-Type", "application/json")
	reqInit.Header.Set("X-Test-Guest-ID", guestID)
	respInit, _ := app.Test(reqInit, 5000)
	var initResp struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
		ActiveVersion struct {
			ID string `json:"id"`
		} `json:"active_version"`
	}
	_ = json.NewDecoder(respInit.Body).Decode(&initResp)
	sessionID := initResp.Session.ID
	v0ID := initResp.ActiveVersion.ID

	tileURL := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.0&tile_x=0&tile_y=0&tile_w=256&tile_h=256", sessionID, v0ID, p1ID)

	// 1. Cold request (first execution)
	tCold0 := time.Now()
	reqCold := httptest.NewRequest(http.MethodGet, tileURL, nil)
	reqCold.Header.Set("X-Test-Guest-ID", guestID)
	respCold, err := app.Test(reqCold, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, respCold.StatusCode)
	coldDuration := time.Since(tCold0)

	assert.Equal(t, uint64(1), atomic.LoadUint64(&workerCalls))

	// 2. Warm requests (100 sequential hits)
	var warmDurations []time.Duration
	for i := 0; i < 100; i++ {
		tWarm0 := time.Now()
		reqWarm := httptest.NewRequest(http.MethodGet, tileURL, nil)
		reqWarm.Header.Set("X-Test-Guest-ID", guestID)
		respWarm, err := app.Test(reqWarm, 1000)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, respWarm.StatusCode)
		warmDurations = append(warmDurations, time.Since(tWarm0))
		_ = respWarm.Body.Close()
	}

	// Worker should NOT have been called again (still 1 call)
	assert.Equal(t, uint64(1), atomic.LoadUint64(&workerCalls), "Warm cache hits must not invoke worker")

	var totalWarm time.Duration
	for _, d := range warmDurations {
		totalWarm += d
	}
	avgWarm := totalWarm / time.Duration(len(warmDurations))
	t.Logf("Cold Request: %v | Warm Average (100 hits): %v (Speedup: %.1fx)", coldDuration, avgWarm, float64(coldDuration)/float64(avgWarm))
}

// TestBenchmark_ModeC_SingleflightScaling tests singleflight coalescing across concurrency 1, 2, 4, 8, 16, 32, 64
func TestBenchmark_ModeC_SingleflightScaling(t *testing.T) {
	concurrencyLevels := []int{1, 2, 4, 8, 16, 32, 64}

	for _, conc := range concurrencyLevels {
		t.Run(fmt.Sprintf("Concurrency_%d", conc), func(t *testing.T) {
			secret := "bench-secret-" + uuid.New().String()
			t.Setenv("WORKER_SHARED_SECRET", secret)

			var renderExecutions uint64
			workerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddUint64(&renderExecutions, 1)
				time.Sleep(20 * time.Millisecond)

				jpegBytes := createSolidTestJpeg(200, 200, 0, 100, 200)
				w.Header().Set("Content-Type", "image/jpeg")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(jpegBytes)
			}))
			defer workerServer.Close()
			t.Setenv("PDFNEST_WORKER_URL", workerServer.URL)

			app, _, _, _, _ := setupTestApp(t)
			guestID := fmt.Sprintf("guest_conc_%d_%s", conc, uuid.New().String())

			pdfBytes := []byte("%PDF-1.4 dummy pdf bytes")
			pdfKey := fmt.Sprintf("doc_c_%d_%s.pdf", conc, uuid.New().String())
			storageDir := storage.GetLocalStorageDir()
			_ = os.MkdirAll(storageDir, 0755)
			localPdfPath := filepath.Join(storageDir, pdfKey)
			_ = os.WriteFile(localPdfPath, pdfBytes, 0644)
			defer os.Remove(localPdfPath)

			assetID := fmt.Sprintf("ast_c_%d_%s", conc, uuid.New().String())
			p1ID := uuid.New().String()

			createBody, _ := json.Marshal(CreateSessionRequest{
				FileName:         "bench_c.pdf",
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
							Dimensions:       &vdm.Dimensions{Width: 595.0, Height: 842.0},
							Rotation:         0,
							IsBlank:          false,
						},
					},
				},
			})

			reqInit := httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions", bytes.NewReader(createBody))
			reqInit.Header.Set("Content-Type", "application/json")
			reqInit.Header.Set("X-Test-Guest-ID", guestID)
			respInit, _ := app.Test(reqInit, 5000)
			var initResp struct {
				Session struct {
					ID string `json:"id"`
				} `json:"session"`
				ActiveVersion struct {
					ID string `json:"id"`
				} `json:"active_version"`
			}
			_ = json.NewDecoder(respInit.Body).Decode(&initResp)
			sessionID := initResp.Session.ID
			v0ID := initResp.ActiveVersion.ID

			tileURL := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.0&tile_x=0&tile_y=0&tile_w=200&tile_h=200", sessionID, v0ID, p1ID)

			startBarrier := make(chan struct{})
			var wg sync.WaitGroup
			statusCodes := make([]int, conc)
			durations := make([]time.Duration, conc)

			for i := 0; i < conc; i++ {
				wg.Add(1)
				idx := i
				go func() {
					defer wg.Done()
					<-startBarrier
					t0 := time.Now()
					req := httptest.NewRequest(http.MethodGet, tileURL, nil)
					req.Header.Set("X-Test-Guest-ID", guestID)
					resp, err := app.Test(req, 10000)
					if err == nil {
						statusCodes[idx] = resp.StatusCode
						_ = resp.Body.Close()
					}
					durations[idx] = time.Since(t0)
				}()
			}

			// Release all concurrent requests simultaneously
			close(startBarrier)
			wg.Wait()

			for i, code := range statusCodes {
				assert.Equal(t, http.StatusOK, code, "Request %d must return HTTP 200", i)
			}

			// Singleflight proof: On cold cache with simultaneous execution, renders == 1
			underlying := atomic.LoadUint64(&renderExecutions)
			assert.Equal(t, uint64(1), underlying, "Singleflight must coalesce %d concurrent identical requests into exactly 1 render", conc)
		})
	}
}

// TestBenchmark_ModeE_MultiDocumentConcurrency tests concurrent rendering across independent documents
func TestBenchmark_ModeE_MultiDocumentConcurrency(t *testing.T) {
	secret := "bench-multi-" + uuid.New().String()
	t.Setenv("WORKER_SHARED_SECRET", secret)

	workerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
		jpegBytes := createSolidTestJpeg(150, 150, 50, 150, 250)
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(jpegBytes)
	}))
	defer workerServer.Close()
	t.Setenv("PDFNEST_WORKER_URL", workerServer.URL)

	app, _, _, _, _ := setupTestApp(t)

	numDocs := 8
	var tileURLs []string
	var guestIDs []string

	for d := 0; d < numDocs; d++ {
		guestID := fmt.Sprintf("guest_multidoc_%d_%s", d, uuid.New().String())
		guestIDs = append(guestIDs, guestID)

		pdfBytes := []byte("%PDF-1.4 dummy pdf bytes")
		pdfKey := fmt.Sprintf("doc_multi_%d_%s.pdf", d, uuid.New().String())
		storageDir := storage.GetLocalStorageDir()
		_ = os.MkdirAll(storageDir, 0755)
		localPdfPath := filepath.Join(storageDir, pdfKey)
		_ = os.WriteFile(localPdfPath, pdfBytes, 0644)
		defer os.Remove(localPdfPath)

		assetID := fmt.Sprintf("ast_multi_%d_%s", d, uuid.New().String())
		p1ID := uuid.New().String()

		createBody, _ := json.Marshal(CreateSessionRequest{
			FileName:         fmt.Sprintf("multi_doc_%d.pdf", d),
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
						Dimensions:       &vdm.Dimensions{Width: 595.0, Height: 842.0},
						Rotation:         0,
						IsBlank:          false,
					},
				},
			},
		})

		reqInit := httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions", bytes.NewReader(createBody))
		reqInit.Header.Set("Content-Type", "application/json")
		reqInit.Header.Set("X-Test-Guest-ID", guestID)
		respInit, _ := app.Test(reqInit, 5000)
		var initResp struct {
			Session struct {
				ID string `json:"id"`
			} `json:"session"`
			ActiveVersion struct {
				ID string `json:"id"`
			} `json:"active_version"`
		}
		_ = json.NewDecoder(respInit.Body).Decode(&initResp)
		sessionID := initResp.Session.ID
		v0ID := initResp.ActiveVersion.ID

		tileURLs = append(tileURLs, fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.0&tile_x=0&tile_y=0&tile_w=150&tile_h=150", sessionID, v0ID, p1ID))
	}

	var wg sync.WaitGroup
	var successful uint64

	for i := 0; i < numDocs*4; i++ {
		wg.Add(1)
		docIdx := i % numDocs
		go func(url, gID string) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, url, nil)
			req.Header.Set("X-Test-Guest-ID", gID)
			resp, err := app.Test(req, 10000)
			if err == nil && resp.StatusCode == http.StatusOK {
				atomic.AddUint64(&successful, 1)
			}
			if resp != nil {
				_ = resp.Body.Close()
			}
		}(tileURLs[docIdx], guestIDs[docIdx])
	}

	wg.Wait()
	assert.Equal(t, uint64(numDocs*4), atomic.LoadUint64(&successful))
}
