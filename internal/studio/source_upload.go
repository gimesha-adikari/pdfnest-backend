package studio

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"

	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/studio/models"
	"pdfnest-backend/internal/studio/vdm"
	"pdfnest-backend/internal/uploads"
)

const (
	studioUploadMaxBytesDefault = int64(200 * 1024 * 1024)
	studioUploadPageLimit       = 1000
)

// SourceUploadInput is the staged source file accepted by the Studio document
// initializer. The browser supplies a PDF; all document metadata and VDM state
// are derived here rather than trusted from the client.
type SourceUploadInput struct {
	Path         string
	OriginalName string
	ContentType  string
}

// CreateDocumentFromSourceUpload validates, stores, inspects, and registers a
// real PDF as the initial Studio document, session, and Version 0.
func (s *studioService) CreateDocumentFromSourceUpload(
	ctx context.Context,
	ident identity.Identity,
	input SourceUploadInput,
) (*models.StudioDocument, *models.StudioSession, *models.StudioVersion, error) {
	if input.Path == "" {
		return nil, nil, nil, ErrInvalidSourcePDF
	}

	info, err := os.Stat(input.Path)
	if err != nil || info.IsDir() || info.Size() <= 0 {
		return nil, nil, nil, ErrInvalidSourcePDF
	}
	if info.Size() > studioUploadMaxBytes() {
		return nil, nil, nil, ErrSourceTooLarge
	}
	if err := uploads.ValidatePDFHeader(input.Path); err != nil {
		return nil, nil, nil, fmt.Errorf("%w: %v", ErrInvalidSourcePDF, err)
	}

	pageCount, err := uploads.CheckPDFPageLimit(input.Path, "MAX_PAGES_GENERAL", studioUploadPageLimit)
	if err != nil {
		if errors.Is(err, uploads.ErrPageLimitExceeded) {
			return nil, nil, nil, fmt.Errorf("%w: %v", ErrSourcePageLimit, err)
		}
		return nil, nil, nil, sourceInspectionError(err)
	}
	pageDims, err := api.PageDimsFile(input.Path)
	if err != nil || len(pageDims) != pageCount {
		return nil, nil, nil, sourceInspectionError(err)
	}

	assetID := "studio-source-" + uuid.NewString()
	key := storage.BuildKey("studio/sources", ".pdf")
	if err := persistStudioSource(ctx, input.Path, key, input.ContentType); err != nil {
		return nil, nil, nil, fmt.Errorf("persist Studio source: %w", err)
	}

	initialVDM := vdm.DocumentModel{
		DocumentID: uuid.NewString(),
		PageCount:  pageCount,
		Pages:      make([]vdm.PageDescriptor, 0, pageCount),
	}
	for index, dim := range pageDims {
		initialVDM.Pages = append(initialVDM.Pages, vdm.PageDescriptor{
			PageID:           uuid.NewString(),
			SourceAssetID:    &assetID,
			SourcePageNumber: index + 1,
			Dimensions:       &vdm.Dimensions{Width: dim.Width, Height: dim.Height},
			Rotation:         0,
			Overlays:         []vdm.Overlay{},
		})
	}

	fileName := filepath.Base(input.OriginalName)
	if fileName == "." || fileName == "" {
		fileName = "document.pdf"
	}
	doc, sess, ver, err := s.CreateDocument(ctx, ident, fileName, info.Size(), pageCount, assetID, key, initialVDM)
	if err != nil {
		cleanupStudioSource(ctx, key)
		return nil, nil, nil, err
	}
	return doc, sess, ver, nil
}

func studioUploadMaxBytes() int64 {
	return int64(uploads.GetEnvInt("MAX_STUDIO_UPLOAD_BYTES", int(studioUploadMaxBytesDefault)))
}

func sourceInspectionError(err error) error {
	if err != nil && (containsEncryptedPDFError(err.Error())) {
		return fmt.Errorf("%w: %v", ErrEncryptedSourcePDF, err)
	}
	if err == nil {
		return ErrInvalidSourcePDF
	}
	return fmt.Errorf("%w: %v", ErrInvalidSourcePDF, err)
}

func containsEncryptedPDFError(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, "encrypted") || strings.Contains(lower, "password") || strings.Contains(lower, "authentication")
}

func persistStudioSource(ctx context.Context, sourcePath, key, contentType string) error {
	if store, err := storage.Default(); err == nil && store != nil {
		return store.UploadFile(sourcePath, key, contentType)
	}

	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	_, _, err = storage.SaveLocalStream(ctx, key, source)
	return err
}

func cleanupStudioSource(ctx context.Context, key string) {
	if store, err := storage.Default(); err == nil && store != nil {
		_ = store.DeleteObject(ctx, key)
		return
	}
	_ = storage.DeleteLocalObject(key)
}
