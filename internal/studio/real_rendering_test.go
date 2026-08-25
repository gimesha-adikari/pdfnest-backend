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
	"image/color"
	"image/draw"
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

// renderPdfWithPyMuPDF uses the Python environment's PyMuPDF package to rasterize a PDF page.
func renderPdfWithPyMuPDF(pdfBytes []byte, pageNum int, dpi float64) ([]byte, error) {
	tmpPdf, err := os.CreateTemp("", "pymupdf-in-*.pdf")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmpPdf.Name())

	if _, err := tmpPdf.Write(pdfBytes); err != nil {
		_ = tmpPdf.Close()
		return nil, err
	}
	_ = tmpPdf.Close()

	tmpJpg, err := os.CreateTemp("", "pymupdf-out-*.jpg")
	if err != nil {
		return nil, err
	}
	tmpJpgPath := tmpJpg.Name()
	_ = tmpJpg.Close()
	defer os.Remove(tmpJpgPath)

	pythonBin := "/home/gimesha/My_Projects/platen/pdfnest-worker/.venv/bin/python"
	if _, statErr := os.Stat(pythonBin); statErr != nil {
		pythonBin = "python3"
	}

	pyScript := `
import pymupdf as fitz
import sys
doc = fitz.open(sys.argv[1])
page_num = int(sys.argv[2])
dpi = float(sys.argv[3])
page = doc[page_num - 1]
zoom = float(dpi) / 72.0
matrix = fitz.Matrix(zoom, zoom)
pix = page.get_pixmap(matrix=matrix)
pix.save(sys.argv[4])
`
	cmd := exec.Command(pythonBin, "-c", pyScript, tmpPdf.Name(), strconv.Itoa(pageNum), fmt.Sprintf("%.2f", dpi), tmpJpgPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("pymupdf execution failed: %w (output: %s)", err, string(output))
	}

	return os.ReadFile(tmpJpgPath)
}

func TestRealRendering_EndToEndWorkerRenderer(t *testing.T) {
	// Set test shared secret
	testSecret := "test-secret-" + uuid.New().String()
	t.Setenv("WORKER_SHARED_SECRET", testSecret)

	// 1. Launch a mock worker HTTP server that verifies HMAC signatures and renders with PyMuPDF
	workerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A. Verify route
		if r.URL.Path != "/api/v1/render/page" {
			http.NotFound(w, r)
			return
		}

		// B. Verify HMAC Signature
		sig := r.Header.Get("X-Worker-Signature")
		ts := r.Header.Get("X-Worker-Timestamp")
		nonce := r.Header.Get("X-Worker-Nonce")
		if sig == "" || ts == "" || nonce == "" {
			http.Error(w, "missing worker authentication", http.StatusUnauthorized)
			return
		}

		stringToSign := fmt.Sprintf("%s\n%s\n%s\n%s", r.Method, r.URL.Path, ts, nonce)
		mac := hmac.New(sha256.New, []byte(testSecret))
		mac.Write([]byte(stringToSign))
		expectedSig := hex.EncodeToString(mac.Sum(nil))

		if sig != expectedSig {
			http.Error(w, "invalid worker signature", http.StatusUnauthorized)
			return
		}

		// C. Parse multipart form
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

		pdfBytes, err := io.ReadAll(file)
		if err != nil {
			http.Error(w, "failed to read file", http.StatusBadRequest)
			return
		}

		pageNum, _ := strconv.Atoi(r.FormValue("page"))
		if pageNum < 1 {
			pageNum = 1
		}
		dpi, _ := strconv.ParseFloat(r.FormValue("dpi"), 64)
		if dpi <= 0 {
			dpi = 72.0
		}

		// D. Genuinely render PDF page using PyMuPDF
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

	// 2. Setup production Studio Backend app
	app, _, _, _, _ := setupTestApp(t)
	guestID := "guest_e2e_test_" + uuid.New().String()

	// 3. Create deterministic 2-page PDF and save to storage
	pdfBytes := createDeterministicTestPDF(t)
	pdfKey := fmt.Sprintf("e2e_test_%s.pdf", uuid.New().String())
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
		FileName:         "e2e_render.pdf",
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
			ID string `json:"id"`
		} `json:"session"`
		ActiveVersion struct {
			ID string `json:"id"`
		} `json:"active_version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&initResp)
	sessionID := initResp.Session.ID
	v0ID := initResp.ActiveVersion.ID

	// 4. Fetch Page 1 Tile through the full production pipeline
	urlP1 := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.0", sessionID, v0ID, p1ID)
	req1 := httptest.NewRequest(http.MethodGet, urlP1, nil)
	req1.Header.Set("X-Test-Guest-ID", guestID)
	resp1, err := app.Test(req1, 5000)
	require.NoError(t, err)
	p1Bytes, err := io.ReadAll(resp1.Body)
	require.NoError(t, err)
	if resp1.StatusCode != http.StatusOK {
		t.Logf("Response status %d: %s", resp1.StatusCode, string(p1Bytes))
	}
	assert.Equal(t, http.StatusOK, resp1.StatusCode)

	// Decode Page 1 JPEG rendered genuinely by PyMuPDF
	img1, err := jpeg.Decode(bytes.NewReader(p1Bytes))
	require.NoError(t, err, "Page 1 JPEG from PyMuPDF must decode cleanly")
	assert.Equal(t, 595, img1.Bounds().Dx())
	assert.Equal(t, 841, img1.Bounds().Dy())

	// Verify Page 1 has solid Blue box at (100, 150)
	_, _, b1, _ := img1.At(100, 150).RGBA()
	assert.True(t, (b1>>8) > 200, "PyMuPDF rendered Page 1 pixel must be Blue")

	// 5. Fetch Page 2 Tile through the full production pipeline
	urlP2 := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.0", sessionID, v0ID, p2ID)
	req2 := httptest.NewRequest(http.MethodGet, urlP2, nil)
	req2.Header.Set("X-Test-Guest-ID", guestID)
	resp2, err := app.Test(req2, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp2.StatusCode)
	p2Bytes, err := io.ReadAll(resp2.Body)
	require.NoError(t, err)

	// Decode Page 2 JPEG rendered genuinely by PyMuPDF
	img2, err := jpeg.Decode(bytes.NewReader(p2Bytes))
	require.NoError(t, err, "Page 2 JPEG from PyMuPDF must decode cleanly")
	assert.Equal(t, 595, img2.Bounds().Dx())
	assert.Equal(t, 841, img2.Bounds().Dy())

	// Verify Page 2 has solid Green box at (100, 150)
	_, g2, b2, _ := img2.At(100, 150).RGBA()
	assert.True(t, (g2>>8) > 200, "PyMuPDF rendered Page 2 pixel must be Green")
	assert.True(t, (b2>>8) < 80, "PyMuPDF rendered Page 2 pixel must not be Blue")

	assert.False(t, bytes.Equal(p1Bytes, p2Bytes))
}

func TestRealRendering_EndToEndWorkerRenderer_SubTilesAndIntegratedRotation(t *testing.T) {
	testSecret := "test-secret-subtile-" + uuid.New().String()
	t.Setenv("WORKER_SHARED_SECRET", testSecret)

	// Launch mock worker HTTP server backed by PyMuPDF
	workerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		mac := hmac.New(sha256.New, []byte(testSecret))
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
	defer workerServer.Close()
	t.Setenv("PDFNEST_WORKER_URL", workerServer.URL)

	app, _, _, _, _ := setupTestApp(t)
	guestID := "guest_subtile_rot_" + uuid.New().String()

	pdfBytes := createDeterministicTestPDF(t)
	pdfKey := fmt.Sprintf("subtile_test_%s.pdf", uuid.New().String())
	storageDir := storage.GetLocalStorageDir()
	_ = os.MkdirAll(storageDir, 0755)
	localPdfPath := filepath.Join(storageDir, pdfKey)
	err := os.WriteFile(localPdfPath, pdfBytes, 0644)
	require.NoError(t, err)
	defer os.Remove(localPdfPath)

	assetID := "ast_subtile_" + uuid.New().String()
	p0DegID := uuid.New().String()
	p90DegID := uuid.New().String()
	p180DegID := uuid.New().String()
	p270DegID := uuid.New().String()

	initialVDM := vdm.DocumentModel{
		PageCount: 4,
		Pages: []vdm.PageDescriptor{
			{
				PageID:           p0DegID,
				SourceAssetID:    &assetID,
				SourcePageNumber: 1, // Page 1 Blue box (50, 100, 200, 150)
				Dimensions:       &vdm.Dimensions{Width: 595.28, Height: 841.89},
				Rotation:         0,
				IsBlank:          false,
			},
			{
				PageID:           p90DegID,
				SourceAssetID:    &assetID,
				SourcePageNumber: 1,
				Dimensions:       &vdm.Dimensions{Width: 595.28, Height: 841.89},
				Rotation:         90,
				IsBlank:          false,
			},
			{
				PageID:           p180DegID,
				SourceAssetID:    &assetID,
				SourcePageNumber: 1,
				Dimensions:       &vdm.Dimensions{Width: 595.28, Height: 841.89},
				Rotation:         180,
				IsBlank:          false,
			},
			{
				PageID:           p270DegID,
				SourceAssetID:    &assetID,
				SourcePageNumber: 1,
				Dimensions:       &vdm.Dimensions{Width: 595.28, Height: 841.89},
				Rotation:         270,
				IsBlank:          false,
			},
		},
	}

	createBody, _ := json.Marshal(CreateSessionRequest{
		FileName:         "subtile_rotation.pdf",
		FileSize:         int64(len(pdfBytes)),
		InitialPageCount: 4,
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
			ID string `json:"id"`
		} `json:"session"`
		ActiveVersion struct {
			ID string `json:"id"`
		} `json:"active_version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&initResp)
	sessionID := initResp.Session.ID
	v0ID := initResp.ActiveVersion.ID

	// 1. Non-full-page sub-tile request on 0° page covering the Blue Box: (tile_x=50, tile_y=100, tile_w=150, tile_h=100)
	urlSub1 := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.0&tile_x=50&tile_y=100&tile_w=150&tile_h=100", sessionID, v0ID, p0DegID)
	reqSub1 := httptest.NewRequest(http.MethodGet, urlSub1, nil)
	reqSub1.Header.Set("X-Test-Guest-ID", guestID)
	respSub1, err := app.Test(reqSub1, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, respSub1.StatusCode)
	sub1Bytes, err := io.ReadAll(respSub1.Body)
	require.NoError(t, err)

	imgSub1, err := jpeg.Decode(bytes.NewReader(sub1Bytes))
	require.NoError(t, err)
	// Assert exact requested dimensions
	assert.Equal(t, 150, imgSub1.Bounds().Dx(), "Sub-tile width must strictly match requested tile_w")
	assert.Equal(t, 100, imgSub1.Bounds().Dy(), "Sub-tile height must strictly match requested tile_h")
	// Assert pixel inside sub-tile is Blue
	_, _, bSub1, _ := imgSub1.At(50, 50).RGBA()
	assert.True(t, (bSub1>>8) > 200, "Sub-tile pixel must correspond to original Blue box")

	// 2. Non-full-page sub-tile request outside the Blue Box: (tile_x=350, tile_y=350, tile_w=100, tile_h=100)
	urlSubWhite := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.0&tile_x=350&tile_y=350&tile_w=100&tile_h=100", sessionID, v0ID, p0DegID)
	reqSubWhite := httptest.NewRequest(http.MethodGet, urlSubWhite, nil)
	reqSubWhite.Header.Set("X-Test-Guest-ID", guestID)
	respSubWhite, err := app.Test(reqSubWhite, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, respSubWhite.StatusCode)
	subWhiteBytes, err := io.ReadAll(respSubWhite.Body)
	require.NoError(t, err)

	imgSubWhite, err := jpeg.Decode(bytes.NewReader(subWhiteBytes))
	require.NoError(t, err)
	assert.Equal(t, 100, imgSubWhite.Bounds().Dx())
	assert.Equal(t, 100, imgSubWhite.Bounds().Dy())
	rW, gW, bW, _ := imgSubWhite.At(50, 50).RGBA()
	assert.True(t, (rW>>8) > 240 && (gW>>8) > 240 && (bW>>8) > 240, "Pixel outside box must be white background")

	// 3. Integrated Rotation 90° Sub-tile
	// (100, 150) -> (841 - 1 - 150, 100) = (690, 100)
	urlRot90 := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.0&tile_x=650&tile_y=80&tile_w=100&tile_h=80", sessionID, v0ID, p90DegID)
	reqRot90 := httptest.NewRequest(http.MethodGet, urlRot90, nil)
	reqRot90.Header.Set("X-Test-Guest-ID", guestID)
	respRot90, err := app.Test(reqRot90, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, respRot90.StatusCode)
	rot90Bytes, err := io.ReadAll(respRot90.Body)
	require.NoError(t, err)

	imgRot90, err := jpeg.Decode(bytes.NewReader(rot90Bytes))
	require.NoError(t, err)
	assert.Equal(t, 100, imgRot90.Bounds().Dx())
	assert.Equal(t, 80, imgRot90.Bounds().Dy())
	// In tile (650, 80), global (690, 100) is at local (40, 20)
	_, _, b90, _ := imgRot90.At(40, 20).RGBA()
	assert.True(t, (b90>>8) > 200, "90° rotated sub-tile pixel at (40, 20) must be Blue")

	// 4. Integrated Rotation 180° Sub-tile
	// (100, 150) -> (595 - 1 - 100, 841 - 1 - 150) = (494, 690)
	urlRot180 := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.0&tile_x=450&tile_y=650&tile_w=100&tile_h=80", sessionID, v0ID, p180DegID)
	reqRot180 := httptest.NewRequest(http.MethodGet, urlRot180, nil)
	reqRot180.Header.Set("X-Test-Guest-ID", guestID)
	respRot180, err := app.Test(reqRot180, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, respRot180.StatusCode)
	rot180Bytes, err := io.ReadAll(respRot180.Body)
	require.NoError(t, err)

	imgRot180, err := jpeg.Decode(bytes.NewReader(rot180Bytes))
	require.NoError(t, err)
	assert.Equal(t, 100, imgRot180.Bounds().Dx())
	assert.Equal(t, 80, imgRot180.Bounds().Dy())
	// In tile (450, 650), global (494, 690) is at local (44, 40)
	_, _, b180, _ := imgRot180.At(44, 40).RGBA()
	assert.True(t, (b180>>8) > 200, "180° rotated sub-tile pixel at (44, 40) must be Blue")

	// 5. Integrated Rotation 270° Sub-tile
	// (100, 150) -> (150, 595 - 1 - 100) = (150, 494)
	urlRot270 := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.0&tile_x=120&tile_y=460&tile_w=80&tile_h=80", sessionID, v0ID, p270DegID)
	reqRot270 := httptest.NewRequest(http.MethodGet, urlRot270, nil)
	reqRot270.Header.Set("X-Test-Guest-ID", guestID)
	respRot270, err := app.Test(reqRot270, 5000)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, respRot270.StatusCode)
	rot270Bytes, err := io.ReadAll(respRot270.Body)
	require.NoError(t, err)

	imgRot270, err := jpeg.Decode(bytes.NewReader(rot270Bytes))
	require.NoError(t, err)
	assert.Equal(t, 80, imgRot270.Bounds().Dx())
	assert.Equal(t, 80, imgRot270.Bounds().Dy())
	// In tile (120, 460), global (150, 494) is at local (30, 34)
	_, _, b270, _ := imgRot270.At(30, 34).RGBA()
	assert.True(t, (b270>>8) > 200, "270° rotated sub-tile pixel at (30, 34) must be Blue")
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
	// Must fail with internal server error, NEVER HTTP 200
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
		time.Sleep(60 * time.Millisecond) // Simulate real worker rendering delay
		img := image.NewRGBA(image.Rect(0, 0, 200, 200))
		draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{100, 150, 200, 255}}, image.Point{}, draw.Src)
		return img, nil
	}

	// Create Studio repo and mock session with the exact renderer passed to controller
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

	// Synchronization barrier to ensure all 15 requests fire simultaneously on a cold tile
	tileURL := fmt.Sprintf("/api/studio/v1/sessions/%s/versions/%s/pages/%s/tile?scale=1.0&tile_x=0&tile_y=0&tile_w=100&tile_h=100", sessionID, v0ID, p1ID)

	var wg sync.WaitGroup
	startBarrier := make(chan struct{})
	statusCodes := make([]int, 15)
	payloads := make([][]byte, 15)

	for i := 0; i < 15; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-startBarrier // Wait for simultaneous release
			r := httptest.NewRequest(http.MethodGet, tileURL, nil)
			r.Header.Set("X-Test-Guest-ID", guestID)
			res, testErr := app.Test(r, 5000)
			if testErr == nil {
				statusCodes[idx] = res.StatusCode
				b, _ := io.ReadAll(res.Body)
				payloads[idx] = b
			}
		}(i)
	}

	// Release all 15 requests at once
	close(startBarrier)
	wg.Wait()

	// 1. All 15 requests must succeed with HTTP 200 and non-empty valid JPEGs
	for i := 0; i < 15; i++ {
		assert.Equal(t, http.StatusOK, statusCodes[i])
		assert.True(t, len(payloads[i]) > 0)
		assert.Equal(t, payloads[0], payloads[i], "All concurrent singleflight requests must return identical payload")
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
