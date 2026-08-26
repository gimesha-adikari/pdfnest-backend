package studio

import (
	"bytes"
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"

	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/studio/models"
	"pdfnest-backend/internal/studio/vdm"
	"pdfnest-backend/internal/worker"
)

var (
	ErrPageNotFound      = errors.New("studio: page descriptor not found in virtual document model")
	ErrInvalidTileCoords = errors.New("studio: invalid tile coordinates or dimensions")
	ErrInvalidTileScale  = errors.New("studio: invalid tile scale factor")
	ErrTileTooLarge      = errors.New("studio: tile dimension exceeds maximum allowed size (4096px)")
	ErrVersionMismatch   = errors.New("studio: version does not belong to session document")
	ErrRenderFailed      = errors.New("studio: real pdf page rendering failed")
	ErrWorkerBusy        = errors.New("studio: render worker capacity saturated")
	ErrRenderTimeout     = errors.New("studio: render processing timed out")
)

const (
	MaxTileDimension    = 4096
	DefaultMaxEntries   = 1000
	DefaultMaxBytes     = 100 * 1024 * 1024 // 100 MB
	DefaultPageWidthPt  = 595.28            // ISO A4 standard (595.28 x 841.89 pt)
	DefaultPageHeightPt = 841.89
)

// TileMetrics tracks render performance, capacity, and cache observability.
type TileMetrics struct {
	TotalRequests         uint64  `json:"total_requests"`
	CacheHits             uint64  `json:"cache_hits"`
	CacheMisses           uint64  `json:"cache_misses"`
	RenderErrors          uint64  `json:"render_errors"`
	UnderlyingRenders     uint64  `json:"underlying_renders"`
	SingleflightCoalesced uint64  `json:"singleflight_coalesced"`
	WorkerTimeouts        uint64  `json:"worker_timeouts"`
	WorkerRejections      uint64  `json:"worker_rejections"`
	CachedBytes           int64   `json:"cached_bytes"`
	CachedEntries         int     `json:"cached_entries"`
	TotalDurationMs       uint64  `json:"total_duration_ms"`
	LastDurationMs        uint64  `json:"last_duration_ms"`
	AvgDurationMs         float64 `json:"avg_duration_ms"`
}

type cacheElement struct {
	key        string
	data       []byte
	size       int64
	createdAt  time.Time
	accessedAt time.Time
}

// TileCache provides a true O(1) LRU doubly-linked list cache with dual limits (entry count + byte size).
type TileCache struct {
	mu           sync.Mutex
	maxEntries   int
	maxBytes     int64
	currentBytes int64
	entries      map[string]*list.Element
	lruList      *list.List
	metrics      TileMetrics
}

// NewTileCache creates a true O(1) LRU tile cache.
func NewTileCache(maxEntries int, maxBytes int64) *TileCache {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	return &TileCache{
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
		entries:    make(map[string]*list.Element),
		lruList:    list.New(),
	}
}

func (c *TileCache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	atomic.AddUint64(&c.metrics.TotalRequests, 1)

	elem, found := c.entries[key]
	if !found {
		atomic.AddUint64(&c.metrics.CacheMisses, 1)
		return nil, false
	}

	c.lruList.MoveToFront(elem)
	entry := elem.Value.(*cacheElement)
	entry.accessedAt = time.Now()

	atomic.AddUint64(&c.metrics.CacheHits, 1)
	return entry.data, true
}

func (c *TileCache) Put(key string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	dataSize := int64(len(data))

	// If entry already exists, update it
	if elem, exists := c.entries[key]; exists {
		entry := elem.Value.(*cacheElement)
		c.currentBytes -= entry.size
		entry.data = data
		entry.size = dataSize
		entry.accessedAt = time.Now()
		c.currentBytes += dataSize
		c.lruList.MoveToFront(elem)
		c.evictExcessLocked()
		return
	}

	// Evict to make room for new entry
	for (c.currentBytes+dataSize > c.maxBytes || c.lruList.Len() >= c.maxEntries) && c.lruList.Len() > 0 {
		oldest := c.lruList.Back()
		if oldest != nil {
			c.removeElementLocked(oldest)
		}
	}

	entry := &cacheElement{
		key:        key,
		data:       data,
		size:       dataSize,
		createdAt:  time.Now(),
		accessedAt: time.Now(),
	}
	elem := c.lruList.PushFront(entry)
	c.entries[key] = elem
	c.currentBytes += dataSize
}

func (c *TileCache) evictExcessLocked() {
	for (c.currentBytes > c.maxBytes || c.lruList.Len() > c.maxEntries) && c.lruList.Len() > 0 {
		oldest := c.lruList.Back()
		if oldest != nil {
			c.removeElementLocked(oldest)
		}
	}
}

func (c *TileCache) removeElementLocked(elem *list.Element) {
	c.lruList.Remove(elem)
	entry := elem.Value.(*cacheElement)
	c.currentBytes -= entry.size
	delete(c.entries, entry.key)
}

func (c *TileCache) GetMetrics() TileMetrics {
	c.mu.Lock()
	defer c.mu.Unlock()

	totalCompleted := atomic.LoadUint64(&c.metrics.UnderlyingRenders)
	totalDur := atomic.LoadUint64(&c.metrics.TotalDurationMs)
	var avgDur float64
	if totalCompleted > 0 {
		avgDur = float64(totalDur) / float64(totalCompleted)
	}

	return TileMetrics{
		TotalRequests:         atomic.LoadUint64(&c.metrics.TotalRequests),
		CacheHits:             atomic.LoadUint64(&c.metrics.CacheHits),
		CacheMisses:           atomic.LoadUint64(&c.metrics.CacheMisses),
		RenderErrors:          atomic.LoadUint64(&c.metrics.RenderErrors),
		UnderlyingRenders:     totalCompleted,
		SingleflightCoalesced: atomic.LoadUint64(&c.metrics.SingleflightCoalesced),
		WorkerTimeouts:        atomic.LoadUint64(&c.metrics.WorkerTimeouts),
		WorkerRejections:      atomic.LoadUint64(&c.metrics.WorkerRejections),
		CachedBytes:           c.currentBytes,
		CachedEntries:         c.lruList.Len(),
		TotalDurationMs:       totalDur,
		LastDurationMs:        atomic.LoadUint64(&c.metrics.LastDurationMs),
		AvgDurationMs:         avgDur,
	}
}

// RenderWorkerFunc defines the signature for rendering a PDF page into an image.
type RenderWorkerFunc func(ctx context.Context, pdfPath string, pageNumber int, dpi float64) (image.Image, error)

// TileRequest specifies parameters for rendering a tile or page viewport.
type TileRequest struct {
	SessionID uuid.UUID
	VersionID uuid.UUID
	PageID    string
	Scale     float64
	TileX     int
	TileY     int
	TileW     int
	TileH     int
}

// TileRenderer defines the interface for Studio V2 tile preview rendering.
type TileRenderer interface {
	RenderTile(ctx context.Context, req TileRequest, ident identity.Identity) ([]byte, error)
	GetMetrics() TileMetrics
	SetWorkerRenderer(fn RenderWorkerFunc)
}

type studioTileRenderer struct {
	repo       Repository
	cache      *TileCache
	flightGrp  singleflight.Group
	workerFunc RenderWorkerFunc
}

// NewTileRenderer instantiates a Studio V2 tile renderer.
func NewTileRenderer(repo Repository) TileRenderer {
	r := &studioTileRenderer{
		repo:  repo,
		cache: NewTileCache(DefaultMaxEntries, DefaultMaxBytes),
	}
	r.workerFunc = r.defaultRenderWithWorker
	return r
}

func (r *studioTileRenderer) SetWorkerRenderer(fn RenderWorkerFunc) {
	if fn != nil {
		r.workerFunc = fn
	}
}

func (r *studioTileRenderer) GetMetrics() TileMetrics {
	return r.cache.GetMetrics()
}

func validateSession(sess *models.StudioSession, ident identity.Identity) error {
	now := time.Now().UTC()
	if now.After(sess.ExpiresAt) {
		return ErrSessionExpired
	}

	if ident.IsUser() {
		userUUID, err := uuid.Parse(ident.ID)
		if err != nil || sess.UserID == nil || *sess.UserID != userUUID {
			return ErrUnauthorized
		}
		return nil
	}

	// Guest identity check
	guestHash := HashGuestToken(ident.ID)
	if sess.GuestTokenHash != guestHash {
		return ErrUnauthorized
	}
	return nil
}

func (r *studioTileRenderer) RenderTile(
	ctx context.Context,
	req TileRequest,
	ident identity.Identity,
) ([]byte, error) {
	if req.Scale < 0.1 || req.Scale > 8.0 {
		atomic.AddUint64(&r.cache.metrics.RenderErrors, 1)
		return nil, fmt.Errorf("%w: scale must be between 0.1 and 8.0 (got %.2f)", ErrInvalidTileScale, req.Scale)
	}

	// 1. Authorize session ownership
	sess, err := r.repo.GetSession(ctx, req.SessionID)
	if err != nil {
		atomic.AddUint64(&r.cache.metrics.RenderErrors, 1)
		return nil, err
	}
	if sess == nil {
		atomic.AddUint64(&r.cache.metrics.RenderErrors, 1)
		return nil, ErrSessionNotFound
	}
	if err := validateSession(sess, ident); err != nil {
		atomic.AddUint64(&r.cache.metrics.RenderErrors, 1)
		return nil, err
	}

	// 2. Load Immutable Version Node
	ver, err := r.repo.GetVersion(ctx, req.VersionID)
	if err != nil {
		atomic.AddUint64(&r.cache.metrics.RenderErrors, 1)
		return nil, err
	}
	if ver == nil {
		atomic.AddUint64(&r.cache.metrics.RenderErrors, 1)
		return nil, ErrVersionNotFound
	}
	if ver.DocumentID != sess.DocumentID {
		atomic.AddUint64(&r.cache.metrics.RenderErrors, 1)
		return nil, ErrVersionMismatch
	}

	// 3. Parse VDM and find target page descriptor
	var docModel vdm.DocumentModel
	if err := json.Unmarshal(ver.VirtualModel, &docModel); err != nil {
		atomic.AddUint64(&r.cache.metrics.RenderErrors, 1)
		return nil, fmt.Errorf("failed to unmarshal virtual document model: %w", err)
	}

	var targetPage *vdm.PageDescriptor
	var pageIndex int
	for idx, p := range docModel.Pages {
		if p.PageID == req.PageID {
			targetPage = &docModel.Pages[idx]
			pageIndex = idx + 1
			break
		}
	}
	if targetPage == nil {
		atomic.AddUint64(&r.cache.metrics.RenderErrors, 1)
		return nil, ErrPageNotFound
	}

	// 4. Calculate authoritative dimensions in points and scaled pixels
	widthPt := DefaultPageWidthPt
	heightPt := DefaultPageHeightPt
	if targetPage.Dimensions != nil && targetPage.Dimensions.Width > 0 && targetPage.Dimensions.Height > 0 {
		widthPt = targetPage.Dimensions.Width
		heightPt = targetPage.Dimensions.Height
	}
	if len(targetPage.CropBox) > 0 {
		if err := validateCropBox(targetPage, targetPage.CropBox); err != nil {
			atomic.AddUint64(&r.cache.metrics.RenderErrors, 1)
			return nil, fmt.Errorf("%w: %v", ErrRenderFailed, err)
		}
		widthPt = targetPage.CropBox[2] - targetPage.CropBox[0]
		heightPt = targetPage.CropBox[3] - targetPage.CropBox[1]
	}

	// Effective dimensions accounting for rotation (90/270 deg swaps width/height)
	effWidthPt := widthPt
	effHeightPt := heightPt
	if targetPage.Rotation == 90 || targetPage.Rotation == 270 {
		effWidthPt = heightPt
		effHeightPt = widthPt
	}

	pageWidthPx := int(effWidthPt * req.Scale)
	pageHeightPx := int(effHeightPt * req.Scale)
	if pageWidthPx <= 0 {
		pageWidthPx = 1
	}
	if pageHeightPx <= 0 {
		pageHeightPx = 1
	}

	tileX := req.TileX
	tileY := req.TileY
	tileW := req.TileW
	tileH := req.TileH

	// Safe Coordinate and Boundary Validation
	if tileX < 0 || tileY < 0 {
		atomic.AddUint64(&r.cache.metrics.RenderErrors, 1)
		return nil, fmt.Errorf("%w: negative tile coordinates (x=%d, y=%d)", ErrInvalidTileCoords, tileX, tileY)
	}
	if tileW < 0 || tileH < 0 {
		atomic.AddUint64(&r.cache.metrics.RenderErrors, 1)
		return nil, fmt.Errorf("%w: negative tile dimensions (w=%d, h=%d)", ErrInvalidTileCoords, tileW, tileH)
	}
	if tileW > MaxTileDimension || tileH > MaxTileDimension {
		atomic.AddUint64(&r.cache.metrics.RenderErrors, 1)
		return nil, fmt.Errorf("%w: requested %dx%d exceeds %d limit", ErrTileTooLarge, tileW, tileH, MaxTileDimension)
	}

	if tileW == 0 || tileW > pageWidthPx {
		tileW = pageWidthPx - tileX
	}
	if tileH == 0 || tileH > pageHeightPx {
		tileH = pageHeightPx - tileY
	}

	if tileX >= pageWidthPx || tileY >= pageHeightPx || tileX+tileW > pageWidthPx || tileY+tileH > pageHeightPx {
		atomic.AddUint64(&r.cache.metrics.RenderErrors, 1)
		return nil, fmt.Errorf("%w: tile boundary [x=%d, y=%d, w=%d, h=%d] exceeds page dimensions (%dx%d)",
			ErrInvalidTileCoords, tileX, tileY, tileW, tileH, pageWidthPx, pageHeightPx)
	}

	// 5. Version-Safe Cache Key Check
	cacheKey := fmt.Sprintf(
		"tile:%s:%s:s%.2f:x%d:y%d:w%d:h%d:r%d",
		req.VersionID.String(),
		req.PageID,
		req.Scale,
		tileX,
		tileY,
		tileW,
		tileH,
		targetPage.Rotation,
	)

	if cached, hit := r.cache.Get(cacheKey); hit {
		return cached, nil
	}

	// 6. Singleflight Coalesced Rendering
	val, err, shared := r.flightGrp.Do(cacheKey, func() (interface{}, error) {
		// Double-check cache inside singleflight
		if cached, hit := r.cache.Get(cacheKey); hit {
			return cached, nil
		}

		atomic.AddUint64(&r.cache.metrics.UnderlyingRenders, 1)

		startRender := time.Now()
		renderedBytes, renderErr := r.rasterizePageTileReal(ctx, targetPage, pageIndex, docModel.PageCount, sess.DocumentID.String(), req.Scale, tileX, tileY, tileW, tileH, docModel.PageNumbering)
		elapsedMs := uint64(time.Since(startRender).Milliseconds())
		atomic.AddUint64(&r.cache.metrics.TotalDurationMs, elapsedMs)
		atomic.StoreUint64(&r.cache.metrics.LastDurationMs, elapsedMs)

		if renderErr != nil {
			atomic.AddUint64(&r.cache.metrics.RenderErrors, 1)
			return nil, renderErr
		}

		r.cache.Put(cacheKey, renderedBytes)
		return renderedBytes, nil
	})

	if shared {
		atomic.AddUint64(&r.cache.metrics.SingleflightCoalesced, 1)
	}

	if err != nil {
		return nil, err
	}

	return val.([]byte), nil
}

func (r *studioTileRenderer) rasterizePageTileReal(
	ctx context.Context,
	page *vdm.PageDescriptor,
	pageNumber int,
	totalPages int,
	documentID string,
	scale float64,
	tileX, tileY, tileW, tileH int,
	numbering *vdm.PageNumberingRule,
) ([]byte, error) {
	var img image.Image
	// A. Explicit Blank Page Rendering or mandatory source rasterization.
	if page.IsBlank || page.SourceAssetID == nil || *page.SourceAssetID == "" {
		widthPt, heightPt := DefaultPageWidthPt, DefaultPageHeightPt
		if page.Dimensions != nil && page.Dimensions.Width > 0 && page.Dimensions.Height > 0 {
			widthPt, heightPt = page.Dimensions.Width, page.Dimensions.Height
		}
		blank := image.NewRGBA(image.Rect(0, 0, maxInt(1, int(widthPt*scale)), maxInt(1, int(heightPt*scale))))
		draw.Draw(blank, blank.Bounds(), &image.Uniform{C: color.RGBA{255, 255, 255, 255}}, image.Point{}, draw.Src)
		img = blank
	} else {
		// B. Mandatory Real Source Asset PDF Rendering
		asset, err := r.repo.GetAsset(ctx, *page.SourceAssetID)
		if err != nil || asset == nil {
			return nil, fmt.Errorf("%w: failed to load source asset '%s': %v", ErrRenderFailed, *page.SourceAssetID, err)
		}

		pdfPath, cleanup, resErr := storage.ResolveArchive(ctx, asset.R2Key)
		if resErr != nil || pdfPath == "" {
			return nil, fmt.Errorf("%w: failed to resolve source pdf from storage '%s': %v", ErrRenderFailed, asset.R2Key, resErr)
		}
		defer cleanup()

		dpi := 72.0 * scale
		var workerErr error
		img, workerErr = r.workerFunc(ctx, pdfPath, page.SourcePageNumber, dpi)
		if workerErr != nil || img == nil {
			if errors.Is(workerErr, ErrWorkerBusy) || errors.Is(workerErr, ErrRenderTimeout) {
				return nil, workerErr
			}
			return nil, fmt.Errorf("%w: worker page rasterization failed for page %d: %v", ErrRenderFailed, page.SourcePageNumber, workerErr)
		}
	}

	assets, assetCleanup, err := r.resolveOverlayImages(ctx, documentID, page)
	if err != nil {
		return nil, err
	}
	defer assetCleanup()

	// Crop in unrotated PDF page coordinates, then apply the VDM rotation. This
	// preserves the CropBox convention for every allowed page rotation. Deferred
	// overlays are composed in the same native coordinate space before rotation.
	if len(page.CropBox) > 0 && page.Dimensions != nil {
		img = CropImageToPageBox(img, page.Dimensions.Width, page.Dimensions.Height, page.CropBox)
	}
	img = ComposePageOverlaysWithAssets(img, page, scale, assets)
	img = ComposePageNumbering(img, page, pageNumber, totalPages, numbering, scale)
	rotated := RotateImage(img, page.Rotation)
	// Crop requested tile
	cropped := CropTile(rotated, tileX, tileY, tileW, tileH)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, cropped, &jpeg.Options{Quality: 85}); err != nil {
		return nil, fmt.Errorf("%w: failed to encode rendered tile JPEG: %v", ErrRenderFailed, err)
	}
	return buf.Bytes(), nil
}

func (r *studioTileRenderer) resolveOverlayImages(ctx context.Context, documentID string, page *vdm.PageDescriptor) (map[string]image.Image, func(), error) {
	assets := make(map[string]image.Image)
	cleanups := make([]func(), 0)
	cleanup := func() {
		for _, fn := range cleanups {
			fn()
		}
	}
	for _, overlay := range page.Overlays {
		if overlay.Type != string(vdm.OverlayTypeWatermark) || overlay.AssetID == "" {
			continue
		}
		asset, err := r.repo.GetAsset(ctx, overlay.AssetID)
		if err != nil {
			cleanup()
			return nil, func() {}, err
		}
		if asset == nil || asset.DocumentID.String() != documentID || asset.AssetType != "watermark_image" || (asset.MimeType != "image/png" && asset.MimeType != "image/jpeg") {
			cleanup()
			return nil, func() {}, ErrUnauthorized
		}
		suffix := ".jpg"
		if asset.MimeType == "image/png" {
			suffix = ".png"
		}
		path, pathCleanup, err := storage.ResolveObject(ctx, asset.R2Key, "pdfnest-studio-watermark-preview", suffix)
		if err != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("%w: resolve watermark image: %v", ErrRenderFailed, err)
		}
		file, err := os.Open(path)
		if err != nil {
			pathCleanup()
			cleanup()
			return nil, func() {}, fmt.Errorf("%w: open watermark image: %v", ErrRenderFailed, err)
		}
		decoded, _, err := image.Decode(file)
		_ = file.Close()
		if err != nil {
			pathCleanup()
			cleanup()
			return nil, func() {}, fmt.Errorf("%w: decode watermark image: %v", ErrRenderFailed, err)
		}
		assets[overlay.AssetID] = decoded
		cleanups = append(cleanups, pathCleanup)
	}
	return assets, cleanup, nil
}

func (r *studioTileRenderer) defaultRenderWithWorker(
	ctx context.Context,
	pdfPath string,
	pageNumber int,
	dpi float64,
) (image.Image, error) {
	file, err := os.Open(pdfPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open local pdf: %w", err)
	}
	defer file.Close()

	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)

	go func() {
		defer file.Close()
		part, err := writer.CreateFormFile("file", filepath.Base(pdfPath))
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		if _, err := io.Copy(part, file); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = writer.WriteField("page", strconv.Itoa(pageNumber))
		_ = writer.WriteField("dpi", fmt.Sprintf("%.2f", dpi))
		_ = writer.Close()
		_ = pw.Close()
	}()

	workerURL := fmt.Sprintf("%s/api/v1/render/page", worker.GetWorkerURL())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, workerURL, pr)
	if err != nil {
		return nil, fmt.Errorf("failed to build worker request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := worker.Client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			atomic.AddUint64(&r.cache.metrics.WorkerTimeouts, 1)
			return nil, fmt.Errorf("%w: worker request timeout: %v", ErrRenderTimeout, err)
		}
		return nil, fmt.Errorf("worker network error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
		body, _ := io.ReadAll(resp.Body)
		atomic.AddUint64(&r.cache.metrics.WorkerRejections, 1)
		return nil, fmt.Errorf("%w: worker capacity saturated (status %d): %s", ErrWorkerBusy, resp.StatusCode, string(body))
	}

	if resp.StatusCode == http.StatusGatewayTimeout {
		body, _ := io.ReadAll(resp.Body)
		atomic.AddUint64(&r.cache.metrics.WorkerTimeouts, 1)
		return nil, fmt.Errorf("%w: worker timed out (504): %s", ErrRenderTimeout, string(body))
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("worker returned status %d: %s", resp.StatusCode, string(body))
	}

	img, err := jpeg.Decode(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to decode worker JPEG response: %w", err)
	}

	return img, nil
}
