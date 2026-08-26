package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
	"gorm.io/gorm"

	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/structure"
	"pdfnest-backend/internal/studio/models"
	"pdfnest-backend/internal/studio/vdm"
)

const studioExportRetention = 24 * time.Hour

// finalizerPDFProcessor is deliberately narrow. Page assembly happens from
// the VDM with pdfcpu, while crop, rotation, and metadata reuse the existing
// internal processing leaves instead of calling this backend's HTTP routes.
type finalizerPDFProcessor interface {
	CropPDF(inputPath string, cropBoxDesc string, selectedPages []string) (string, error)
	RotatePDF(inputPath string, rotations map[string]int) (string, error)
	AddTextToPDF(inputPath string, elements []structure.TextElement) (string, error)
	SignPDF(inputPath string, signaturePath string, outputPath string, stampsJSON string) error
	WatermarkPDFOnPages(inputPath string, text string, imagePath string, description string, selectedPages []string) (string, error)
	AddPageNumbersPDF(inputPath string, description string) (string, error)
	UpdateMetadataPDF(inputPath string, metadata map[string]string, password string) (string, error)
	GetMetadataPDF(inputPath string, password string) (map[string]string, error)
}

// FinalizationResult contains only public export metadata. The resolved
// storage path stays inside the finalizer and is never returned to clients.
type FinalizationResult struct {
	Export   *models.StudioExport
	FileName string
}

// MaterializedVersion is the reusable durable PDF representation of one
// immutable Studio version. Cleanup only removes a temporary download; the
// snapshot asset itself remains owned by Studio storage.
type MaterializedVersion struct {
	SessionExpiresAt time.Time
	Session          *models.StudioSession
	Document         *models.StudioDocument
	Version          *models.StudioVersion
	Asset            *models.StudioAsset
	Model            *vdm.DocumentModel
	Path             string
	Cleanup          func()
}

// ExportDownload is an owned, temporary local handle used only by the HTTP
// download controller. Cleanup removes remote-storage downloads after use.
type ExportDownload struct {
	Export   *models.StudioExport
	FileName string
	Path     string
	Cleanup  func()
}

// StudioFinalizer materializes the currently active authoritative VDM into a
// single persisted PDF. It intentionally never replays the operation history.
type StudioFinalizer interface {
	StudioVersionMaterializer
	Finalize(ctx context.Context, sessionID uuid.UUID, ident identity.Identity) (*FinalizationResult, error)
	ResolveDownload(ctx context.Context, sessionID, exportID uuid.UUID, ident identity.Identity) (*ExportDownload, error)
}

// StudioVersionMaterializer is the lower-level boundary shared by export and
// synchronous materializing tools. It never creates a user-visible export.
type StudioVersionMaterializer interface {
	MaterializeVersion(ctx context.Context, sessionID uuid.UUID, ident identity.Identity) (*MaterializedVersion, error)
}

// StudioVersionMaterializerByID is used by detached asynchronous branches.
// It materializes a persisted version without changing the session's active
// version or creating an export.
type StudioVersionMaterializerByID interface {
	StudioVersionMaterializer
	MaterializeVersionByID(ctx context.Context, sessionID, versionID uuid.UUID, ident identity.Identity) (*MaterializedVersion, error)
}

type studioFinalizer struct {
	repo      Repository
	processor finalizerPDFProcessor
	now       func() time.Time
}

func NewFinalizer(repo Repository, processor finalizerPDFProcessor) StudioFinalizer {
	return &studioFinalizer{repo: repo, processor: processor, now: func() time.Time { return time.Now().UTC() }}
}

func (f *studioFinalizer) Finalize(ctx context.Context, sessionID uuid.UUID, ident identity.Identity) (*FinalizationResult, error) {
	materialized, err := f.MaterializeVersion(ctx, sessionID, ident)
	if err != nil {
		return nil, err
	}
	defer materialized.Cleanup()

	export, err := f.recordExport(ctx, sessionID, ident, materialized.Version.ID, materialized.Document.ID, materialized.Asset.R2Key, materialized.Asset.ByteSize, materialized.SessionExpiresAt)
	if err != nil {
		return nil, err
	}
	return &FinalizationResult{Export: export, FileName: studioExportFilename(materialized.Document.OriginalFileName)}, nil
}

func (f *studioFinalizer) MaterializeVersion(ctx context.Context, sessionID uuid.UUID, ident identity.Identity) (*MaterializedVersion, error) {
	if f.processor == nil {
		return nil, fmt.Errorf("%w: PDF processor is not configured", ErrFinalizationFailed)
	}

	sess, doc, version, documentModel, err := f.loadActiveState(ctx, sessionID, ident)
	if err != nil {
		return nil, err
	}

	asset, err := f.ensureSnapshot(ctx, sessionID, ident, sess, doc, version, documentModel)
	if err != nil {
		return nil, err
	}
	path, cleanup, err := storage.ResolveObject(ctx, asset.R2Key, "pdfnest-studio-materialized", ".pdf")
	if err != nil {
		return nil, fmt.Errorf("%w: resolve snapshot input: %v", ErrFinalizationFailed, err)
	}
	return &MaterializedVersion{
		SessionExpiresAt: sess.ExpiresAt,
		Session:          sess,
		Document:         doc,
		Version:          version,
		Asset:            asset,
		Model:            documentModel,
		Path:             path,
		Cleanup:          cleanup,
	}, nil
}

func (f *studioFinalizer) MaterializeVersionByID(ctx context.Context, sessionID, versionID uuid.UUID, ident identity.Identity) (*MaterializedVersion, error) {
	if f.processor == nil {
		return nil, fmt.Errorf("%w: PDF processor is not configured", ErrFinalizationFailed)
	}
	sess, err := f.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err = validateSessionAccess(sess, ident); err != nil {
		return nil, err
	}
	version, err := f.repo.GetVersion(ctx, versionID)
	if err != nil {
		return nil, err
	}
	if version.DocumentID != sess.DocumentID {
		return nil, ErrInvalidBranchTarget
	}
	doc, err := f.repo.GetDocument(ctx, sess.DocumentID)
	if err != nil {
		return nil, err
	}
	model, err := vdm.FromJSON(version.VirtualModel)
	if err != nil {
		return nil, fmt.Errorf("%w: version VDM: %v", ErrFinalizationFailed, err)
	}
	path, cleanup, err := f.materialize(ctx, doc.ID, model)
	if err != nil {
		return nil, err
	}
	return &MaterializedVersion{SessionExpiresAt: sess.ExpiresAt, Session: sess, Document: doc, Version: version, Model: model, Path: path, Cleanup: cleanup}, nil
}

func (f *studioFinalizer) ResolveDownload(ctx context.Context, sessionID, exportID uuid.UUID, ident identity.Identity) (*ExportDownload, error) {
	sess, err := f.repo.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := validateSessionAccess(sess, ident); err != nil {
		return nil, err
	}

	export, err := f.repo.GetExport(ctx, exportID)
	if err != nil {
		return nil, err
	}
	if export.DocumentID != sess.DocumentID {
		return nil, ErrUnauthorized
	}
	if !f.now().Before(export.ExpiresAt) {
		return nil, ErrExportExpired
	}

	doc, err := f.repo.GetDocument(ctx, sess.DocumentID)
	if err != nil {
		return nil, err
	}
	path, cleanup, err := storage.ResolveObject(ctx, export.R2Key, "pdfnest-studio-export", ".pdf")
	if err != nil {
		return nil, fmt.Errorf("%w: resolve persisted export: %v", ErrFinalizationFailed, err)
	}
	return &ExportDownload{Export: export, FileName: studioExportFilename(doc.OriginalFileName), Path: path, Cleanup: cleanup}, nil
}

func (f *studioFinalizer) loadActiveState(ctx context.Context, sessionID uuid.UUID, ident identity.Identity) (*models.StudioSession, *models.StudioDocument, *models.StudioVersion, *vdm.DocumentModel, error) {
	var sess *models.StudioSession
	var doc *models.StudioDocument
	var version *models.StudioVersion
	var modelState *vdm.DocumentModel

	err := f.repo.WithTransaction(ctx, func(txRepo Repository, _ *gorm.DB) error {
		locked, err := txRepo.LockSession(ctx, sessionID)
		if err != nil {
			return err
		}
		if err := validateSessionAccess(locked, ident); err != nil {
			return err
		}
		loadedDoc, err := txRepo.GetDocument(ctx, locked.DocumentID)
		if err != nil {
			return err
		}
		loadedVersion, err := txRepo.GetVersion(ctx, locked.ActiveVersionID)
		if err != nil {
			return err
		}
		parsed, err := vdm.FromJSON(loadedVersion.VirtualModel)
		if err != nil {
			return fmt.Errorf("%w: active VDM: %v", ErrFinalizationFailed, err)
		}
		if parsed.DocumentID == "" || loadedVersion.DocumentID != loadedDoc.ID {
			return fmt.Errorf("%w: active version does not match session document", ErrFinalizationFailed)
		}
		sess, doc, version, modelState = locked, loadedDoc, loadedVersion, parsed
		return nil
	})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return sess, doc, version, modelState, nil
}

func (f *studioFinalizer) materialize(ctx context.Context, documentID uuid.UUID, modelState *vdm.DocumentModel) (string, func(), error) {
	if modelState == nil || modelState.PageCount == 0 || len(modelState.Pages) == 0 {
		return "", nil, fmt.Errorf("%w: VDM has no pages", ErrFinalizationFailed)
	}

	workDir, err := os.MkdirTemp("", "pdfnest-studio-finalize-*")
	if err != nil {
		return "", nil, fmt.Errorf("%w: create scratch directory: %v", ErrFinalizationFailed, err)
	}
	owned := map[string]struct{}{}
	cleanup := func() {
		for path := range owned {
			_ = os.Remove(path)
		}
		_ = os.RemoveAll(workDir)
	}
	fail := func(err error) (string, func(), error) {
		cleanup()
		return "", nil, err
	}

	sources, sourceCleanup, err := f.resolveSourceAssets(ctx, documentID, modelState)
	if err != nil {
		return fail(err)
	}
	defer sourceCleanup()

	anchor := firstSourcePage(modelState.Pages)
	if anchor == nil {
		return fail(fmt.Errorf("%w: blank-only VDM has no source page anchor", ErrFinalizationFailed))
	}
	pageCache := map[string]string{}
	extract := func(page *vdm.PageDescriptor) (string, error) {
		if page == nil || page.SourceAssetID == nil || page.SourcePageNumber < 1 {
			return "", fmt.Errorf("%w: invalid source page descriptor", ErrFinalizationFailed)
		}
		cacheKey := *page.SourceAssetID + ":" + strconv.Itoa(page.SourcePageNumber)
		if path, ok := pageCache[cacheKey]; ok {
			return path, nil
		}
		sourcePath, ok := sources[*page.SourceAssetID]
		if !ok {
			return "", fmt.Errorf("%w: source asset is not available", ErrFinalizationFailed)
		}
		dir, err := os.MkdirTemp(workDir, "source-page-")
		if err != nil {
			return "", err
		}
		if err := api.ExtractPagesFile(sourcePath, dir, []string{strconv.Itoa(page.SourcePageNumber)}, model.NewDefaultConfiguration()); err != nil {
			return "", fmt.Errorf("%w: extract source page %d: %v", ErrFinalizationFailed, page.SourcePageNumber, err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return "", err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".pdf") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			pageCache[cacheKey] = path
			return path, nil
		}
		return "", fmt.Errorf("%w: extracted source page was not created", ErrFinalizationFailed)
	}

	anchorPath, err := extract(anchor)
	if err != nil {
		return fail(err)
	}
	pageFiles := make([]string, 0, len(modelState.Pages))
	for index := range modelState.Pages {
		page := &modelState.Pages[index]
		if page.IsBlank {
			blankPath, err := createBlankPage(anchorPath, page.Dimensions, workDir, index)
			if err != nil {
				return fail(fmt.Errorf("%w: create blank page: %v", ErrFinalizationFailed, err))
			}
			pageFiles = append(pageFiles, blankPath)
			continue
		}
		pagePath, err := extract(page)
		if err != nil {
			return fail(err)
		}
		pageFiles = append(pageFiles, pagePath)
	}

	assembledPath := filepath.Join(workDir, "assembled.pdf")
	if err := api.MergeCreateFile(pageFiles, assembledPath, false, model.NewDefaultConfiguration()); err != nil {
		return fail(fmt.Errorf("%w: assemble VDM page sequence: %v", ErrFinalizationFailed, err))
	}
	owned[assembledPath] = struct{}{}
	currentPath := assembledPath
	// CropBox is applied in the unrotated page space before deferred overlays,
	// matching the preview compositor's native-coordinate pipeline.
	for index, page := range modelState.Pages {
		if len(page.CropBox) != 4 {
			continue
		}
		pageNumber := strconv.Itoa(index + 1)
		nextPath, err := f.processor.CropPDF(currentPath, cropBoxDescription(page.CropBox), []string{pageNumber})
		if err != nil {
			return fail(fmt.Errorf("%w: crop page %d: %v", ErrFinalizationFailed, index+1, err))
		}
		owned[nextPath] = struct{}{}
		if currentPath != nextPath {
			_ = os.Remove(currentPath)
			delete(owned, currentPath)
		}
		currentPath = nextPath
	}
	watermarkPaths, watermarkCleanup, err := f.resolveWatermarkAssets(ctx, documentID, modelState)
	if err != nil {
		return fail(err)
	}
	defer watermarkCleanup()
	currentPath, err = f.flattenDeferredOverlays(currentPath, modelState, watermarkPaths, owned)
	if err != nil {
		return fail(err)
	}
	if modelState.PageNumbering != nil && modelState.PageNumbering.Enabled {
		nextPath, numberingErr := f.processor.AddPageNumbersPDF(currentPath, pageNumberingDescription(*modelState.PageNumbering))
		if numberingErr != nil {
			return fail(fmt.Errorf("%w: flatten page numbering: %v", ErrFinalizationFailed, numberingErr))
		}
		owned[nextPath] = struct{}{}
		if currentPath != nextPath {
			_ = os.Remove(currentPath)
			delete(owned, currentPath)
		}
		currentPath = nextPath
	}
	for index, page := range modelState.Pages {
		if page.Rotation != 0 {
			pageNumber := strconv.Itoa(index + 1)
			nextPath, err := f.processor.RotatePDF(currentPath, map[string]int{pageNumber: page.Rotation})
			if err != nil {
				return fail(fmt.Errorf("%w: rotate page %d: %v", ErrFinalizationFailed, index+1, err))
			}
			owned[nextPath] = struct{}{}
			if currentPath != nextPath {
				_ = os.Remove(currentPath)
				delete(owned, currentPath)
			}
			currentPath = nextPath
		}
	}

	if len(modelState.Metadata) > 0 {
		nextPath, err := f.processor.UpdateMetadataPDF(currentPath, canonicalMetadata(modelState.Metadata), "")
		if err != nil {
			return fail(fmt.Errorf("%w: write metadata: %v", ErrFinalizationFailed, err))
		}
		owned[nextPath] = struct{}{}
		if currentPath != nextPath {
			_ = os.Remove(currentPath)
			delete(owned, currentPath)
		}
		currentPath = nextPath
		if err := validateFinalMetadata(f.processor, currentPath, modelState.Metadata); err != nil {
			return fail(err)
		}
	}

	return currentPath, cleanup, nil
}

func (f *studioFinalizer) flattenDeferredOverlays(currentPath string, modelState *vdm.DocumentModel, watermarkPaths map[string]string, owned map[string]struct{}) (string, error) {
	for pageIndex, page := range modelState.Pages {
		if len(page.Overlays) == 0 {
			continue
		}
		if page.Dimensions == nil || page.Dimensions.Width <= 0 || page.Dimensions.Height <= 0 {
			return "", fmt.Errorf("%w: overlay page %d has invalid dimensions", ErrFinalizationFailed, pageIndex+1)
		}
		for _, overlay := range page.Overlays {
			pageNumber := strconv.Itoa(pageIndex + 1)
			var nextPath string
			var err error
			switch overlay.Type {
			case string(vdm.OverlayTypeText):
				if len(overlay.Rect) < 2 || strings.TrimSpace(overlay.Text) == "" || overlay.FontSize <= 0 {
					return "", fmt.Errorf("%w: invalid text overlay %q", ErrFinalizationFailed, overlay.ID)
				}
				fontSize := int(math.Round(overlay.FontSize))
				if fontSize < 1 {
					fontSize = 1
				}
				topLeftY := page.Dimensions.Height - overlay.Rect[1] - overlay.FontSize
				textColor := overlay.Color
				if textColor == "" {
					textColor = "#000000"
				}
				nextPath, err = f.processor.AddTextToPDF(currentPath, []structure.TextElement{{
					ID: overlay.ID, Text: overlay.Text, X: overlay.Rect[0], Y: topLeftY,
					Page: pageIndex + 1, FontSize: fontSize, Color: textColor,
				}})
			case string(vdm.OverlayTypeWatermark):
				imagePath := ""
				if overlay.AssetID != "" {
					imagePath = watermarkPaths[overlay.AssetID]
					if imagePath == "" {
						return "", fmt.Errorf("%w: watermark asset %q was not resolved", ErrFinalizationFailed, overlay.AssetID)
					}
				}
				if imagePath == "" && strings.TrimSpace(overlay.Text) == "" {
					return "", fmt.Errorf("%w: invalid watermark overlay %q", ErrFinalizationFailed, overlay.ID)
				}
				nextPath, err = f.processor.WatermarkPDFOnPages(currentPath, overlay.Text, imagePath, watermarkDescription(overlay), []string{pageNumber})
			case string(vdm.OverlayTypeSignature):
				signaturePath := watermarkPaths[overlay.AssetID]
				if signaturePath == "" || len(overlay.Rect) != 4 {
					return "", fmt.Errorf("%w: signature asset or rectangle is missing for overlay %q", ErrFinalizationFailed, overlay.ID)
				}
				stampY := page.Dimensions.Height - overlay.Rect[1] - overlay.Rect[3]
				stamps, marshalErr := json.Marshal([]map[string]any{{
					"page": pageIndex + 1,
					"x":    overlay.Rect[0], "y": stampY,
					"width": overlay.Rect[2], "height": overlay.Rect[3],
				}})
				if marshalErr != nil {
					return "", fmt.Errorf("%w: encode signature stamp %q: %v", ErrFinalizationFailed, overlay.ID, marshalErr)
				}
				nextPath = filepath.Join(filepath.Dir(currentPath), "signature-"+uuid.NewString()+".pdf")
				err = f.processor.SignPDF(currentPath, signaturePath, nextPath, string(stamps))
			default:
				return "", fmt.Errorf("%w: overlay type %q is not implemented by the finalizer", ErrFinalizationFailed, overlay.Type)
			}
			if err != nil {
				return "", fmt.Errorf("%w: flatten overlay %q: %v", ErrFinalizationFailed, overlay.ID, err)
			}
			owned[nextPath] = struct{}{}
			if currentPath != nextPath {
				_ = os.Remove(currentPath)
				delete(owned, currentPath)
			}
			currentPath = nextPath
		}
	}
	return currentPath, nil
}

func (f *studioFinalizer) resolveWatermarkAssets(ctx context.Context, documentID uuid.UUID, modelState *vdm.DocumentModel) (map[string]string, func(), error) {
	paths := make(map[string]string)
	cleanups := make([]func(), 0)
	cleanup := func() {
		for _, fn := range cleanups {
			fn()
		}
	}
	for _, page := range modelState.Pages {
		for _, overlay := range page.Overlays {
			if (overlay.Type != string(vdm.OverlayTypeWatermark) && overlay.Type != string(vdm.OverlayTypeSignature)) || overlay.AssetID == "" {
				continue
			}
			if _, ok := paths[overlay.AssetID]; ok {
				continue
			}
			asset, err := f.repo.GetAsset(ctx, overlay.AssetID)
			if err != nil {
				cleanup()
				return nil, func() {}, err
			}
			expectedType := "watermark_image"
			namespace := "pdfnest-studio-watermark"
			if overlay.Type == string(vdm.OverlayTypeSignature) {
				expectedType = "signature_image"
				namespace = "pdfnest-studio-signature"
			}
			if asset.DocumentID != documentID || asset.AssetType != expectedType || (asset.MimeType != "image/png" && asset.MimeType != "image/jpeg") {
				cleanup()
				return nil, func() {}, ErrUnauthorized
			}
			suffix := ".jpg"
			if asset.MimeType == "image/png" {
				suffix = ".png"
			}
			path, pathCleanup, err := storage.ResolveObject(ctx, asset.R2Key, namespace, suffix)
			if err != nil {
				cleanup()
				return nil, func() {}, fmt.Errorf("%w: resolve watermark asset: %v", ErrFinalizationFailed, err)
			}
			paths[overlay.AssetID] = path
			cleanups = append(cleanups, pathCleanup)
		}
	}
	return paths, cleanup, nil
}

func watermarkDescription(overlay vdm.Overlay) string {
	font := overlay.Font
	if font == "" {
		font = "Helvetica"
	}
	position := overlay.Position
	if position == "cc" {
		position = "c"
	} else if position == "cl" {
		position = "l"
	} else if position == "cr" {
		position = "r"
	}
	scale := overlay.FontSize / 50
	if overlay.AssetID != "" {
		scale = overlay.FontSize / 600
	}
	return fmt.Sprintf("font:%s, pos:%s, scale:%g, rot:%d, op:%g", font, position, scale, -overlay.Rotation, overlay.Opacity)
}

func pageNumberingDescription(rule vdm.PageNumberingRule) string {
	font := rule.FontFamily
	if font == "" {
		font = "Helvetica"
	}
	position := rule.Position
	if position == "" {
		position = "bc"
	}
	fontSize := rule.FontSize
	if fontSize <= 0 {
		fontSize = 12
	}
	offset := map[string]string{
		"bl": "20 20",
		"bc": "0 20",
		"br": "-20 20",
		"tl": "20 -20",
		"tc": "0 -20",
		"tr": "-20 -20",
	}[position]
	description := fmt.Sprintf("font:%s, scale:%.2f abs, pos:%s, rot:0", font, fontSize/25, position)
	if offset != "" {
		description += ", offset: " + offset
	}
	return description
}

func (f *studioFinalizer) resolveSourceAssets(ctx context.Context, documentID uuid.UUID, modelState *vdm.DocumentModel) (map[string]string, func(), error) {
	paths := make(map[string]string)
	cleanups := make([]func(), 0)
	cleanup := func() {
		for _, fn := range cleanups {
			fn()
		}
	}
	for _, page := range modelState.Pages {
		if page.IsBlank {
			continue
		}
		if page.SourceAssetID == nil || *page.SourceAssetID == "" || page.SourcePageNumber < 1 {
			cleanup()
			return nil, func() {}, fmt.Errorf("%w: non-blank page has no valid source reference", ErrFinalizationFailed)
		}
		if _, ok := paths[*page.SourceAssetID]; ok {
			continue
		}
		asset, err := f.repo.GetAsset(ctx, *page.SourceAssetID)
		if err != nil {
			cleanup()
			return nil, func() {}, err
		}
		if asset.DocumentID != documentID {
			cleanup()
			return nil, func() {}, ErrUnauthorized
		}
		path, pathCleanup, err := storage.ResolveObject(ctx, asset.R2Key, "pdfnest-studio-source", ".pdf")
		if err != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("%w: resolve source asset: %v", ErrFinalizationFailed, err)
		}
		paths[*page.SourceAssetID] = path
		cleanups = append(cleanups, pathCleanup)
	}
	return paths, cleanup, nil
}

func (f *studioFinalizer) ensureSnapshot(ctx context.Context, sessionID uuid.UUID, ident identity.Identity, sess *models.StudioSession, doc *models.StudioDocument, version *models.StudioVersion, documentModel *vdm.DocumentModel) (*models.StudioAsset, error) {
	// A snapshot is immutable and unique per version. Reuse it for both user
	// export and internal materialization retries.
	if version.SnapshotID != nil && *version.SnapshotID != uuid.Nil {
		if snapshot, err := f.repo.GetSnapshot(ctx, *version.SnapshotID); err == nil {
			if asset, assetErr := f.repo.GetAsset(ctx, snapshot.AssetID); assetErr == nil &&
				asset.DocumentID == doc.ID && storage.ObjectExists(ctx, asset.R2Key) {
				return asset, nil
			}
		}
	}

	outputPath, cleanup, err := f.materialize(ctx, doc.ID, documentModel)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	info, err := validateFinalOutput(outputPath, documentModel.PageCount)
	if err != nil {
		return nil, err
	}

	key := storage.BuildKey(filepath.ToSlash(filepath.Join("studio", "snapshots", doc.ID.String())), ".pdf")
	if err := persistStudioPDF(ctx, outputPath, key); err != nil {
		return nil, fmt.Errorf("%w: persist snapshot: %v", ErrFinalizationFailed, err)
	}
	persisted := true
	defer func() {
		if persisted {
			return
		}
		cleanupStudioObject(ctx, key)
	}()

	assetID := "studio-snapshot-" + uuid.NewString()
	asset := &models.StudioAsset{ID: assetID, DocumentID: doc.ID, AssetType: "snapshot", R2Key: key, ByteSize: info.Size(), MimeType: "application/pdf", CreatedAt: f.now()}
	snapshot := &models.StudioSnapshot{ID: uuid.New(), VersionID: version.ID, AssetID: assetID, PageCount: documentModel.PageCount, CreatedAt: f.now()}
	if err := f.recordSnapshot(ctx, sessionID, ident, version.ID, doc.ID, asset, snapshot); err != nil {
		persisted = false
		return nil, err
	}
	return asset, nil
}

func (f *studioFinalizer) recordSnapshot(ctx context.Context, sessionID uuid.UUID, ident identity.Identity, versionID, documentID uuid.UUID, asset *models.StudioAsset, snapshot *models.StudioSnapshot) error {
	return f.repo.WithTransaction(ctx, func(txRepo Repository, tx *gorm.DB) error {
		locked, err := txRepo.LockSession(ctx, sessionID)
		if err != nil {
			return err
		}
		if err := validateSessionAccess(locked, ident); err != nil {
			return err
		}
		if locked.DocumentID != documentID || locked.ActiveVersionID != versionID {
			return ErrConflict
		}
		if err := txRepo.CreateAsset(ctx, asset); err != nil {
			return err
		}
		if err := txRepo.CreateSnapshot(ctx, snapshot); err != nil {
			return err
		}
		return tx.Model(&models.StudioVersion{}).Where("id = ?", versionID).Updates(map[string]interface{}{"snapshot_id": snapshot.ID, "is_materialized": true}).Error
	})
}

func (f *studioFinalizer) recordExport(ctx context.Context, sessionID uuid.UUID, ident identity.Identity, versionID, documentID uuid.UUID, key string, byteSize int64, sessionExpiresAt time.Time) (*models.StudioExport, error) {
	var export *models.StudioExport
	err := f.repo.WithTransaction(ctx, func(txRepo Repository, _ *gorm.DB) error {
		locked, err := txRepo.LockSession(ctx, sessionID)
		if err != nil {
			return err
		}
		if err := validateSessionAccess(locked, ident); err != nil {
			return err
		}
		if locked.DocumentID != documentID || locked.ActiveVersionID != versionID {
			return ErrConflict
		}
		export = &models.StudioExport{ID: uuid.New(), DocumentID: documentID, VersionID: versionID, ExportFormat: "pdf", R2Key: key, ByteSize: byteSize, ExpiresAt: exportExpiry(f.now(), sessionExpiresAt), CreatedAt: f.now()}
		return txRepo.CreateExport(ctx, export)
	})
	if err != nil {
		return nil, err
	}
	return export, nil
}

func createBlankPage(anchorPath string, dimensions *vdm.Dimensions, workDir string, index int) (string, error) {
	if dimensions == nil || dimensions.Width <= 0 || dimensions.Height <= 0 {
		return "", ErrBlankDimensions
	}
	withSource := filepath.Join(workDir, fmt.Sprintf("blank-%d-source.pdf", index))
	blankPath := filepath.Join(workDir, fmt.Sprintf("blank-%d.pdf", index))
	pageConf := &pdfcpu.PageConfiguration{PageDim: &types.Dim{Width: dimensions.Width, Height: dimensions.Height}, UserDim: true, InpUnit: types.POINTS}
	conf := model.NewDefaultConfiguration()
	if err := api.InsertPagesFile(anchorPath, withSource, []string{"1"}, true, pageConf, conf); err != nil {
		return "", err
	}
	defer os.Remove(withSource)
	if err := api.RemovePagesFile(withSource, blankPath, []string{"2"}, model.NewDefaultConfiguration()); err != nil {
		return "", err
	}
	return blankPath, nil
}

func firstSourcePage(pages []vdm.PageDescriptor) *vdm.PageDescriptor {
	for index := range pages {
		if !pages[index].IsBlank {
			return &pages[index]
		}
	}
	return nil
}

func cropBoxDescription(cropBox []float64) string {
	// pdfcpu treats unbracketed four values as margins. VDM CropBox is an
	// explicit native PDF rectangle, so preserve its [llx lly urx ury]
	// semantics with PDF-array notation.
	return fmt.Sprintf("[%.8f %.8f %.8f %.8f]", cropBox[0], cropBox[1], cropBox[2], cropBox[3])
}

func overlayTextElements(modelState *vdm.DocumentModel) ([]structure.TextElement, error) {
	if modelState == nil {
		return nil, fmt.Errorf("%w: missing VDM for overlay finalization", ErrFinalizationFailed)
	}
	elements := make([]structure.TextElement, 0)
	for pageIndex, page := range modelState.Pages {
		if len(page.Overlays) == 0 {
			continue
		}
		if page.Dimensions == nil || page.Dimensions.Width <= 0 || page.Dimensions.Height <= 0 {
			return nil, fmt.Errorf("%w: overlay page %d has invalid dimensions", ErrFinalizationFailed, pageIndex+1)
		}
		for _, overlay := range page.Overlays {
			if overlay.Type != string(vdm.OverlayTypeText) {
				return nil, fmt.Errorf("%w: overlay type %q is not implemented by the foundation finalizer", ErrFinalizationFailed, overlay.Type)
			}
			if len(overlay.Rect) < 2 || strings.TrimSpace(overlay.Text) == "" || overlay.FontSize <= 0 {
				return nil, fmt.Errorf("%w: invalid text overlay %q", ErrFinalizationFailed, overlay.ID)
			}
			fontSize := int(math.Round(overlay.FontSize))
			if fontSize < 1 {
				fontSize = 1
			}
			// structure.AddTextToPDF accepts its legacy top-left Y input. Convert
			// the shared lower-left native coordinate to that boundary here.
			topLeftY := page.Dimensions.Height - overlay.Rect[1] - overlay.FontSize
			elements = append(elements, structure.TextElement{
				ID: overlay.ID, Text: overlay.Text, X: overlay.Rect[0], Y: topLeftY,
				Page: pageIndex + 1, FontSize: fontSize, Color: overlay.Color,
			})
		}
	}
	return elements, nil
}

func canonicalMetadata(metadata map[string]string) map[string]string {
	return map[string]string{
		"Title":    metadata["Title"],
		"Author":   metadata["Author"],
		"Subject":  metadata["Subject"],
		"Keywords": metadata["Keywords"],
	}
}

func validateFinalMetadata(processor finalizerPDFProcessor, outputPath string, expected map[string]string) error {
	actual, err := processor.GetMetadataPDF(outputPath, "")
	if err != nil {
		return fmt.Errorf("%w: read final metadata: %v", ErrInvalidFinalOutput, err)
	}
	for key, expectedValue := range canonicalMetadata(expected) {
		actualValue := actual[strings.ToLower(key)]
		if actualValue != expectedValue {
			return fmt.Errorf("%w: metadata %s did not persist", ErrInvalidFinalOutput, key)
		}
	}
	return nil
}

func validateFinalOutput(path string, expectedPageCount int) (os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() <= 0 {
		return nil, fmt.Errorf("%w: output is missing or empty", ErrInvalidFinalOutput)
	}
	if err := api.ValidateFile(path, model.NewDefaultConfiguration()); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidFinalOutput, err)
	}
	pageCount, err := api.PageCountFile(path)
	if err != nil || pageCount != expectedPageCount {
		return nil, fmt.Errorf("%w: expected %d pages, got %d", ErrInvalidFinalOutput, expectedPageCount, pageCount)
	}
	return info, nil
}

func persistStudioPDF(ctx context.Context, outputPath, key string) error {
	if store, err := storage.Default(); err == nil && store != nil {
		return store.UploadFile(outputPath, key, "application/pdf")
	}
	file, err := os.Open(outputPath)
	if err != nil {
		return err
	}
	defer file.Close()
	_, _, err = storage.SaveLocalStream(ctx, key, file)
	return err
}

func cleanupStudioObject(ctx context.Context, key string) {
	if store, err := storage.Default(); err == nil && store != nil {
		_ = store.DeleteObject(ctx, key)
		return
	}
	_ = storage.DeleteLocalObject(key)
}

func exportExpiry(now, sessionExpiresAt time.Time) time.Time {
	expiresAt := now.Add(studioExportRetention)
	if sessionExpiresAt.Before(expiresAt) {
		return sessionExpiresAt
	}
	return expiresAt
}

func studioExportFilename(originalName string) string {
	base := filepath.Base(strings.TrimSpace(originalName))
	base = strings.TrimSuffix(base, filepath.Ext(base))
	if base == "" || base == "." {
		base = "studio-document"
	}
	base = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == '"' || r < 32 {
			return '-'
		}
		return r
	}, base)
	return base + "-studio.pdf"
}
