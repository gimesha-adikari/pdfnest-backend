package studio

import (
	"context"
	"errors"

	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/studio/models"
)

// CreateDocumentFromStoredSource adopts a private snapshot, never the mutable
// presigned destination. The upload URL cannot overwrite the generated source
// key. All initial metadata and page geometry come from the validated snapshot.
func (s *studioService) CreateDocumentFromStoredSource(ctx context.Context, ident identity.Identity, key, name string) (*models.StudioDocument, *models.StudioSession, *models.StudioVersion, error) {
	if !storage.IsOwnedKey(key, ident.ID, "studio_source") {
		return nil, nil, nil, ErrUnauthorized
	}
	path, cleanup, err := storage.StageStudioPDF(ctx, key)
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrStudioPDFTooLarge):
			err = ErrSourceTooLarge
		case errors.Is(err, storage.ErrObjectNotFound):
			err = ErrSourceMissing
		case errors.Is(err, storage.ErrStudioPDFChanged):
			err = ErrSourceChanged
		default:
			err = ErrSourceStorageUnavailable
		}
		return nil, nil, nil, err
	}
	defer cleanup()
	if ctx.Err() != nil {
		return nil, nil, nil, ErrSourceStorageUnavailable
	}
	return s.CreateDocumentFromSourceUpload(ctx, ident, SourceUploadInput{Path: path, OriginalName: name, ContentType: "application/pdf"})
}
