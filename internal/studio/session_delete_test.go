package studio

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/studio/models"
	"pdfnest-backend/internal/studio/vdm"
)

func TestService_DeleteSessionRequiresOwnerAndRemovesWorkspace(t *testing.T) {
	svc, repo := getTestServiceAndRepository(t)
	ctx := context.Background()
	owner := identity.Identity{ID: uuid.NewString(), Type: identity.TypeUser}
	foreign := identity.Identity{ID: uuid.NewString(), Type: identity.TypeUser}
	assetID := "delete-source-" + uuid.NewString()
	model := vdm.DocumentModel{
		DocumentID: uuid.NewString(),
		PageCount:  1,
		Pages:      []vdm.PageDescriptor{{PageID: "delete-page-" + uuid.NewString(), SourcePageNumber: 1, SourceAssetID: &assetID, Dimensions: &vdm.Dimensions{Width: 612, Height: 792}}},
	}
	_, session, _, err := svc.CreateDocument(ctx, owner, "discard-me.pdf", 12, 1, assetID, "studio/test/discard-me.pdf", model)
	require.NoError(t, err)

	assert.ErrorIs(t, svc.DeleteSession(ctx, session.ID, foreign), ErrUnauthorized)
	assert.ErrorIs(t, svc.DeleteSession(ctx, session.ID, identity.Identity{ID: "guest", Type: identity.TypeGuest}), ErrUnauthorized)
	require.NoError(t, svc.DeleteSession(ctx, session.ID, owner))
	_, err = repo.GetSession(ctx, session.ID)
	assert.ErrorIs(t, err, ErrSessionNotFound)
	_, err = repo.GetDocument(ctx, session.DocumentID)
	assert.ErrorIs(t, err, ErrDocumentNotFound)

	assert.ErrorIs(t, svc.DeleteSession(ctx, session.ID, owner), ErrSessionNotFound, "repeated deletion follows the existing not-found convention")
}

func TestRepository_DeleteSessionWorkspaceRemovesAssociatedRows(t *testing.T) {
	svc, repo := getTestServiceAndRepository(t)
	ctx := context.Background()
	owner := identity.Identity{ID: uuid.NewString(), Type: identity.TypeUser}
	assetID := "delete-associated-" + uuid.NewString()
	model := vdm.DocumentModel{DocumentID: uuid.NewString(), PageCount: 1, Pages: []vdm.PageDescriptor{{PageID: "associated-page-" + uuid.NewString(), SourcePageNumber: 1, SourceAssetID: &assetID, Dimensions: &vdm.Dimensions{Width: 612, Height: 792}}}}
	doc, session, version, err := svc.CreateDocument(ctx, owner, "associated.pdf", 10, 1, assetID, "studio/test/associated.pdf", model)
	require.NoError(t, err)
	require.NoError(t, repo.CreateExport(ctx, &models.StudioExport{ID: uuid.New(), DocumentID: doc.ID, VersionID: version.ID, ExportFormat: "pdf", R2Key: "studio/test/export.pdf", ByteSize: 1, ExpiresAt: version.CreatedAt.Add(24 * time.Hour), CreatedAt: version.CreatedAt}))

	require.NoError(t, svc.DeleteSession(ctx, session.ID, owner))
	var count int64
	// The document cascade is the authoritative assertion for all document-owned
	// rows; the explicit export assertion protects the ephemeral resource path.
	db := repo.(*gormRepository).db
	require.NoError(t, db.Model(&models.StudioExport{}).Where("document_id = ?", doc.ID).Count(&count).Error)
	assert.Zero(t, count)
}
