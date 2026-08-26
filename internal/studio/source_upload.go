package studio

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"

	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/studio/models"
	"pdfnest-backend/internal/studio/vdm"
	"pdfnest-backend/internal/uploads"
)

const (
	studioUploadMaxBytesDefault      = int64(200 * 1024 * 1024)
	studioUploadPageLimit            = 1000
	studioWatermarkImageMaxBytes     = int64(20 * 1024 * 1024)
	studioWatermarkImageMaxDimension = 10000
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
	fileSize, pageCount, err := validateStudioPDFUpload(input.Path)
	if err != nil {
		return nil, nil, nil, err
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
	doc, sess, ver, err := s.CreateDocument(ctx, ident, fileName, fileSize, pageCount, assetID, key, initialVDM)
	if err != nil {
		cleanupStudioSource(ctx, key)
		return nil, nil, nil, err
	}
	return doc, sess, ver, nil
}

// validateStudioPDFUpload is shared by initial-document and secondary-asset
// ingestion. It preserves Batch 0's header/page-count checks and Studio's
// size, page-limit, and encrypted/unreadable error mapping.
func validateStudioPDFUpload(path string) (int64, int, error) {
	if path == "" {
		return 0, 0, ErrInvalidSourcePDF
	}

	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() <= 0 {
		return 0, 0, ErrInvalidSourcePDF
	}
	if info.Size() > studioUploadMaxBytes() {
		return 0, 0, ErrSourceTooLarge
	}
	if err := uploads.ValidatePDFHeader(path); err != nil {
		return 0, 0, fmt.Errorf("%w: %v", ErrInvalidSourcePDF, err)
	}

	pageCount, err := uploads.CheckPDFPageLimit(path, "MAX_PAGES_GENERAL", studioUploadPageLimit)
	if err != nil {
		if errors.Is(err, uploads.ErrPageLimitExceeded) {
			return 0, 0, fmt.Errorf("%w: %v", ErrSourcePageLimit, err)
		}
		return 0, 0, sourceInspectionError(err)
	}
	if err := rejectEncryptedStudioPDF(path); err != nil {
		return 0, 0, err
	}
	// PageCountFile can inspect some encrypted PDFs without a password. Match
	// initial Studio upload behavior by requiring a readable page geometry pass
	// before registering either a source document or a secondary asset.
	if _, err := api.PageDimsFile(path); err != nil {
		return 0, 0, sourceInspectionError(err)
	}
	return info.Size(), pageCount, nil
}

func rejectEncryptedStudioPDF(path string) error {
	encrypted, err := pdfHasEncryptionMarker(path)
	if err != nil {
		return sourceInspectionError(err)
	}
	if encrypted {
		return ErrEncryptedSourcePDF
	}

	file, err := os.Open(path)
	if err != nil {
		return sourceInspectionError(err)
	}
	defer file.Close()

	info, err := api.PDFInfo(file, filepath.Base(path), nil, false, model.NewDefaultConfiguration())
	if err != nil {
		return sourceInspectionError(err)
	}
	if info != nil && info.Encrypted {
		return ErrEncryptedSourcePDF
	}
	return nil
}

func pdfHasEncryptionMarker(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()

	const marker = "/Encrypt"
	reader := bufio.NewReader(file)
	buffer := make([]byte, 32*1024)
	carry := make([]byte, 0, len(marker)-1)
	for {
		read, readErr := reader.Read(buffer)
		chunk := append(carry, buffer[:read]...)
		if bytes.Contains(chunk, []byte(marker)) {
			return true, nil
		}
		if len(chunk) >= len(marker)-1 {
			carry = append(carry[:0], chunk[len(chunk)-(len(marker)-1):]...)
		} else {
			carry = append(carry[:0], chunk...)
		}
		if readErr != nil {
			if readErr == io.EOF {
				return false, nil
			}
			return false, readErr
		}
	}
}

func studioUploadMaxBytes() int64 {
	return int64(uploads.GetEnvInt("MAX_STUDIO_UPLOAD_BYTES", int(studioUploadMaxBytesDefault)))
}

func validateStudioWatermarkImage(path string) (int64, string, error) {
	if path == "" {
		return 0, "", ErrInvalidOverlay
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() <= 0 || info.Size() > studioWatermarkImageMaxBytes {
		return 0, "", ErrInvalidOverlay
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, "", ErrInvalidOverlay
	}
	defer file.Close()
	config, format, err := image.DecodeConfig(file)
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > studioWatermarkImageMaxDimension || config.Height > studioWatermarkImageMaxDimension {
		return 0, "", ErrInvalidOverlay
	}
	switch format {
	case "png":
		return info.Size(), "image/png", nil
	case "jpeg":
		return info.Size(), "image/jpeg", nil
	default:
		return 0, "", ErrInvalidOverlay
	}
}

func imageStorageSuffix(mimeType string) string {
	if mimeType == "image/png" {
		return ".png"
	}
	return ".jpg"
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
