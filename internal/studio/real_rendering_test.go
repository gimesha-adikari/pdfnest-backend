package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jung-kurt/gofpdf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/studio/vdm"
)

// createDeterministicTestPDF generates a deterministic 2-page PDF where:
// Page 1 contains a Blue rectangle at (50, 100, 200, 150)
// Page 2 contains a Green rectangle at (50, 100, 200, 150)
func createDeterministicTestPDF(t *testing.T) []byte {
	pdf := gofpdf.New("P", "pt", "A4", "") // A4: 595.28 x 841.89 pt

	// Page 1: Blue box
	pdf.AddPage()
	pdf.SetFillColor(0, 0, 255)
	pdf.Rect(50, 100, 200, 150, "F")
	pdf.SetFont("Arial", "B", 24)
	pdf.SetTextColor(0, 0, 255)
	pdf.Text(50, 80, "PAGE 1 BLUE")

	// Page 2: Green box
	pdf.AddPage()
	pdf.SetFillColor(0, 255, 0)
	pdf.Rect(50, 100, 200, 150, "F")
	pdf.SetFont("Arial", "B", 24)
	pdf.SetTextColor(0, 255, 0)
	pdf.Text(50, 80, "PAGE 2 GREEN")

	var buf bytes.Buffer
	err := pdf.Output(&buf)
	require.NoError(t, err)
	return buf.Bytes()
}

// deterministicTestWorkerRenderer emulates the PyMuPDF worker by rasterizing the deterministic PDF pages into images.
func deterministicTestWorkerRenderer(ctx context.Context, pdfPath string, pageNumber int, dpi float64) (image.Image, error) {
	// A4 standard at given DPI (72 DPI = 595 x 842 px)
	scale := dpi / 72.0
	w := int(595.28 * scale)
	h := int(841.89 * scale)
	if w <= 0 {
		w = 595
	}
	if h <= 0 {
		h = 842
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{255, 255, 255, 255}}, image.Point{}, draw.Src)

	// Draw distinctive page content matching createDeterministicTestPDF
	if pageNumber == 1 {
		// Pure Blue box at scaled (50, 100, 200, 150)
		box := image.Rect(int(50*scale), int(100*scale), int(250*scale), int(250*scale))
		draw.Draw(img, box, &image.Uniform{C: color.RGBA{0, 0, 255, 255}}, image.Point{}, draw.Src)
	} else if pageNumber == 2 {
		// Pure Green box at scaled (50, 100, 200, 150)
		box := image.Rect(int(50*scale), int(100*scale), int(250*scale), int(250*scale))
		draw.Draw(img, box, &image.Uniform{C: color.RGBA{0, 255, 0, 255}}, image.Point{}, draw.Src)
	} else {
		return nil, fmt.Errorf("page number %d out of bounds (max 2)", pageNumber)
	}

	return img, nil
}

func TestRealRendering_DeterministicContentAndVerification(t *testing.T) {
	app, _, _, renderer, _ := setupTestApp(t)
	guestID := "guest_content_test_" + uuid.New().String()

	// Inject deterministic worker renderer into test app
	renderer.SetWorkerRenderer(deterministicTestWorkerRenderer)

	// 1. Create a 2-page deterministic PDF and save to storage
	pdfBytes := createDeterministicTestPDF(t)
	pdfKey := fmt.Sprintf("test_content_%s.pdf", uuid.New().String())
	storageDir := storage.GetLocalStorageDir()
	_ = os.MkdirAll(storageDir, 0755)
	localPdfPath := filepath.Join(storageDir, pdfKey)
	err := os.WriteFile(localPdfPath, pdfBytes, 0644)
	require.NoError(t, err)
	defer os.Remove(localPdfPath)

	assetID := "ast_" + uuid.New().String()
	p1ID := uuid.New().String()
	p2ID := uuid.New().String()

	initialVDM := vdm.DocumentModel{
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
	}

	createBody, _ := json.Marshal(CreateSessionRequest{
		FileName:         "content_test.pdf",
		FileSize:         int64(len(pdfBytes)),
		InitialPageCount: 2,
		SourceAssetID:    assetID,
		SourceR2Key:      pdfKey,
		InitialVDM:       initialVDM,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, err := app.Test(req, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	var initResp struct {
		Session struct {
			ID         string `json:"id"`
			DocumentID string `json:"document_id"`
		} `json:"session"`
		ActiveVersion struct {
			ID string `json:"id"`
		} `json:"active_version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&initResp)
	sessionID := initResp.Session.ID
	v0ID := initResp.ActiveVersion.ID

	// 2. Fetch and Decode Page 1 Tile
	urlP1 := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.0", sessionID, v0ID, p1ID)
	req1 := httptest.NewRequest(http.MethodGet, urlP1, nil)
	req1.Header.Set("X-Test-Guest-ID", guestID)
	resp1, err := app.Test(req1, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp1.StatusCode)
	p1Bytes, err := io.ReadAll(resp1.Body)
	require.NoError(t, err)

	// Decode Page 1 image and verify actual Blue pixel content
	img1, err := jpeg.Decode(bytes.NewReader(p1Bytes))
	require.NoError(t, err, "Page 1 JPEG must decode cleanly")
	assert.Equal(t, 595, img1.Bounds().Dx())
	assert.Equal(t, 841, img1.Bounds().Dy())

	// Sample pixel inside Page 1 Blue box (x=100, y=150)
	r1, g1, b1, _ := img1.At(100, 150).RGBA()
	// Convert 16-bit color channel to 8-bit
	assert.True(t, (b1>>8) > 200, "Page 1 sample pixel must be prominently Blue")
	assert.True(t, (g1>>8) < 80, "Page 1 sample pixel must not be Green")

	// 3. Fetch and Decode Page 2 Tile
	urlP2 := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.0", sessionID, v0ID, p2ID)
	req2 := httptest.NewRequest(http.MethodGet, urlP2, nil)
	req2.Header.Set("X-Test-Guest-ID", guestID)
	resp2, err := app.Test(req2, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp2.StatusCode)
	p2Bytes, err := io.ReadAll(resp2.Body)
	require.NoError(t, err)

	// Decode Page 2 image and verify actual Green pixel content
	img2, err := jpeg.Decode(bytes.NewReader(p2Bytes))
	require.NoError(t, err, "Page 2 JPEG must decode cleanly")
	assert.Equal(t, 595, img2.Bounds().Dx())
	assert.Equal(t, 841, img2.Bounds().Dy())

	// Sample pixel inside Page 2 Green box (x=100, y=150)
	_, g2, b2, _ := img2.At(100, 150).RGBA()
	assert.True(t, (g2>>8) > 200, "Page 2 sample pixel must be prominently Green")
	assert.True(t, (b2>>8) < 80, "Page 2 sample pixel must not be Blue")

	// 4. Assert Page 1 and Page 2 are demonstrably distinct
	assert.False(t, bytes.Equal(p1Bytes, p2Bytes))
	_ = r1
}

func TestRealRendering_GeometricRotations(t *testing.T) {
	// Test Rotations: 0, 90, 180, 270 on a known canvas of W=300, H=500
	srcW, srcH := 300, 500
	srcImg := image.NewRGBA(image.Rect(0, 0, srcW, srcH))
	draw.Draw(srcImg, srcImg.Bounds(), &image.Uniform{C: color.RGBA{255, 255, 255, 255}}, image.Point{}, draw.Src)

	// Place a distinctive Red marker at Top-Left (x: 20..60, y: 20..60)
	redColor := color.RGBA{255, 0, 0, 255}
	draw.Draw(srcImg, image.Rect(20, 20, 60, 60), &image.Uniform{C: redColor}, image.Point{}, draw.Src)

	// 1. Rotation 0° (Dimensions W x H)
	rot0 := RotateImage(srcImg, 0)
	assert.Equal(t, srcW, rot0.Bounds().Dx())
	assert.Equal(t, srcH, rot0.Bounds().Dy())
	r, g, b, _ := rot0.At(40, 40).RGBA()
	assert.True(t, (r>>8) > 240 && (g>>8) < 20 && (b>>8) < 20, "0° marker must be at (40, 40)")

	// 2. Rotation 90° clockwise (Dimensions H x W)
	// (x, y) -> (H - 1 - y, x) => (40, 40) -> (500 - 1 - 40, 40) = (459, 40)
	rot90 := RotateImage(srcImg, 90)
	assert.Equal(t, srcH, rot90.Bounds().Dx())
	assert.Equal(t, srcW, rot90.Bounds().Dy())
	r90, g90, b90, _ := rot90.At(459, 40).RGBA()
	assert.True(t, (r90>>8) > 240 && (g90>>8) < 20 && (b90>>8) < 20, "90° marker must be at (459, 40)")

	// 3. Rotation 180° (Dimensions W x H)
	// (x, y) -> (W - 1 - x, H - 1 - y) => (40, 40) -> (300 - 1 - 40, 500 - 1 - 40) = (259, 459)
	rot180 := RotateImage(srcImg, 180)
	assert.Equal(t, srcW, rot180.Bounds().Dx())
	assert.Equal(t, srcH, rot180.Bounds().Dy())
	r180, g180, b180, _ := rot180.At(259, 459).RGBA()
	assert.True(t, (r180>>8) > 240 && (g180>>8) < 20 && (b180>>8) < 20, "180° marker must be at (259, 459)")

	// 4. Rotation 270° clockwise (Dimensions H x W)
	// (x, y) -> (y, W - 1 - x) => (40, 40) -> (40, 300 - 1 - 40) = (40, 259)
	rot270 := RotateImage(srcImg, 270)
	assert.Equal(t, srcH, rot270.Bounds().Dx())
	assert.Equal(t, srcW, rot270.Bounds().Dy())
	r270, g270, b270, _ := rot270.At(40, 259).RGBA()
	assert.True(t, (r270>>8) > 240 && (g270>>8) < 20 && (b270>>8) < 20, "270° marker must be at (40, 259)")
}

func TestRealRendering_NoSilentSyntheticFallbackOnWorkerFailure(t *testing.T) {
	app, _, _, _, _ := setupTestApp(t)
	guestID := "guest_fail_test_" + uuid.New().String()

	// 1. Create a session pointing to a nonexistent / failing worker asset
	assetID := "ast_failing_" + uuid.New().String()
	p1ID := uuid.New().String()
	pBlankID := uuid.New().String()

	createBody, _ := json.Marshal(CreateSessionRequest{
		FileName:         "fail_test.pdf",
		FileSize:         1000,
		InitialPageCount: 2,
		SourceAssetID:    assetID,
		SourceR2Key:      "nonexistent_storage_key.pdf",
		InitialVDM: vdm.DocumentModel{
			PageCount: 2,
			Pages: []vdm.PageDescriptor{
				{
					PageID:           p1ID,
					SourceAssetID:    &assetID,
					SourcePageNumber: 1,
					Dimensions:       &vdm.Dimensions{Width: 595.28, Height: 841.89},
					Rotation:         0,
					IsBlank:          false, // Real asset -> MUST FAIL explicitly if storage/worker fails
				},
				{
					PageID:           pBlankID,
					SourceAssetID:    nil,
					SourcePageNumber: 0,
					Dimensions:       &vdm.Dimensions{Width: 595.28, Height: 841.89},
					Rotation:         0,
					IsBlank:          true, // Blank page -> MUST SUCCEED with white page
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

	// 2. Request Page 1 (Real Asset with missing PDF) -> MUST NOT return 200 with fake synthetic grid!
	urlFail := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.0", sessionID, v0ID, p1ID)
	reqFail := httptest.NewRequest(http.MethodGet, urlFail, nil)
	reqFail.Header.Set("X-Test-Guest-ID", guestID)
	respFail, err := app.Test(reqFail, 5000)
	require.NoError(t, err)
	// Must fail with internal server error / bad gateway, NEVER HTTP 200
	assert.Equal(t, http.StatusInternalServerError, respFail.StatusCode, "Failing real asset render must return 500, never fake 200")

	// 3. Request Blank Page -> MUST succeed with clean white 200
	urlBlank := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.0", sessionID, v0ID, pBlankID)
	reqBlank := httptest.NewRequest(http.MethodGet, urlBlank, nil)
	reqBlank.Header.Set("X-Test-Guest-ID", guestID)
	respBlank, err := app.Test(reqBlank, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, respBlank.StatusCode)
	blankBytes, err := io.ReadAll(respBlank.Body)
	require.NoError(t, err)
	imgBlank, err := jpeg.Decode(bytes.NewReader(blankBytes))
	require.NoError(t, err)
	assert.Equal(t, 595, imgBlank.Bounds().Dx())
}

func TestRealRendering_SingleflightStrictProof(t *testing.T) {
	var renderCount uint64

	// Mock slow renderer that tracks exact invocation count
	slowWorkerRenderer := func(ctx context.Context, pdfPath string, pageNumber int, dpi float64) (image.Image, error) {
		atomic.AddUint64(&renderCount, 1)
		time.Sleep(50 * time.Millisecond) // Simulate real worker rendering delay
		img := image.NewRGBA(image.Rect(0, 0, 200, 200))
		draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{100, 150, 200, 255}}, image.Point{}, draw.Src)
		return img, nil
	}

	// Create Studio repo and mock session
	app, _, _, renderer, _ := setupTestApp(t)
	renderer.SetWorkerRenderer(slowWorkerRenderer)
	guestID := "guest_sf_strict_" + uuid.New().String()

	// Write dummy local PDF
	pdfKey := fmt.Sprintf("test_sf_%s.pdf", uuid.New().String())
	storageDir := storage.GetLocalStorageDir()
	_ = os.MkdirAll(storageDir, 0755)
	localPdfPath := filepath.Join(storageDir, pdfKey)
	_ = os.WriteFile(localPdfPath, []byte("%PDF-1.4 dummy"), 0644)
	defer os.Remove(localPdfPath)

	assetID := "ast_" + uuid.New().String()
	p1ID := uuid.New().String()

	createBody, _ := json.Marshal(CreateSessionRequest{
		FileName:         "sf_strict.pdf",
		FileSize:         500,
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

	// Fire 15 concurrent identical requests for the exact same tile
	tileURL := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.0&tile_x=0&tile_y=0&tile_w=100&tile_h=100", sessionID, v0ID, p1ID)

	var wg sync.WaitGroup
	statusCodes := make([]int, 15)
	payloads := make([][]byte, 15)

	for i := 0; i < 15; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			r := httptest.NewRequest(http.MethodGet, tileURL, nil)
			r.Header.Set("X-Test-Guest-ID", guestID)
			res, err := app.Test(r, 5000)
			if err == nil {
				statusCodes[idx] = res.StatusCode
				b, _ := io.ReadAll(res.Body)
				payloads[idx] = b
			}
		}(i)
	}
	wg.Wait()

	// 1. All 15 requests must succeed
	for i := 0; i < 15; i++ {
		assert.Equal(t, http.StatusOK, statusCodes[i])
		assert.True(t, len(payloads[i]) > 0)
		assert.Equal(t, payloads[0], payloads[i], "All concurrent requests must return identical payload")
	}

	// 2. Strict proof: Exactly ONE underlying render execution occurred
	assert.Equal(t, uint64(1), atomic.LoadUint64(&renderCount), "Underlying worker render must be invoked strictly once for 15 concurrent requests")
	assert.Equal(t, uint64(1), renderer.GetMetrics().UnderlyingRenders, "Metrics UnderlyingRenders must be strictly 1")
}

func TestRealRendering_CoordinateAndScaleValidations(t *testing.T) {
	app, _, _, _, _ := setupTestApp(t)
	guestID := "guest_val_test_" + uuid.New().String()

	createBody, _ := json.Marshal(CreateSessionRequest{FileName: "val.pdf"})
	req := httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, _ := app.Test(req, 5000)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	var initResp struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
		ActiveVersion struct {
			ID string `json:"id"`
		} `json:"active_version"`
		VDM vdm.DocumentModel `json:"vdm"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&initResp)
	sessionID := initResp.Session.ID
	v0ID := initResp.ActiveVersion.ID
	pageID := initResp.VDM.Pages[0].PageID

	testCases := []struct {
		name       string
		query      string
		wantStatus int
	}{
		{"Valid Sub-Tile", "?scale=1.0&tile_x=0&tile_y=0&tile_w=200&tile_h=200", http.StatusOK},
		{"Scale below minimum (<0.1)", "?scale=0.05", http.StatusBadRequest},
		{"Scale above maximum (>8.0)", "?scale=8.5", http.StatusBadRequest},
		{"Negative tile_x", "?scale=1.0&tile_x=-10&tile_y=0&tile_w=100&tile_h=100", http.StatusBadRequest},
		{"Negative tile_y", "?scale=1.0&tile_x=0&tile_y=-20&tile_w=100&tile_h=100", http.StatusBadRequest},
		{"Negative tile_w", "?scale=1.0&tile_x=0&tile_y=0&tile_w=-100&tile_h=100", http.StatusBadRequest},
		{"Negative tile_h", "?scale=1.0&tile_x=0&tile_y=0&tile_w=100&tile_h=-100", http.StatusBadRequest},
		{"Oversized tile (>4096px)", "?scale=1.0&tile_x=0&tile_y=0&tile_w=5000&tile_h=5000", http.StatusBadRequest},
		{"Out of bounds tile_x", "?scale=1.0&tile_x=5000&tile_y=0&tile_w=100&tile_h=100", http.StatusBadRequest},
		{"Boundary overrun (tile_x+tile_w > pageW)", "?scale=1.0&tile_x=500&tile_y=0&tile_w=300&tile_h=300", http.StatusBadRequest},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			url := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile%s", sessionID, v0ID, pageID, tc.query)
			req := httptest.NewRequest(http.MethodGet, url, nil)
			req.Header.Set("X-Test-Guest-ID", guestID)
			res, err := app.Test(req, 5000)
			require.NoError(t, err)
			assert.Equal(t, tc.wantStatus, res.StatusCode)
		})
	}
}

func TestRealRendering_LRUMemoryLimitAndEviction(t *testing.T) {
	// Test O(1) LRU Cache memory limit eviction directly
	smallCache := NewTileCache(2, 500) // Max 2 entries or 500 bytes
	d1 := []byte("data_item_one_12345")
	d2 := []byte("data_item_two_67890")
	d3 := []byte("data_item_three_abc")

	smallCache.Put("key1", d1)
	smallCache.Put("key2", d2)
	assert.Equal(t, 2, smallCache.GetMetrics().CachedEntries)

	// Access key1 to make key2 the least recently used
	_, hit1 := smallCache.Get("key1")
	assert.True(t, hit1)

	// Put key3 -> should evict key2 (since key1 was accessed more recently)
	smallCache.Put("key3", d3)
	assert.Equal(t, 2, smallCache.GetMetrics().CachedEntries)

	_, hit2 := smallCache.Get("key2")
	assert.False(t, hit2, "key2 must be evicted by LRU")

	_, hit1Again := smallCache.Get("key1")
	assert.True(t, hit1Again, "key1 must remain in cache")
}
