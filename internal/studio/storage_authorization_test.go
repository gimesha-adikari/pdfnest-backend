package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/studio/vdm"
	"testing"
)

func TestCreateSessionRejectsForeignStoredSource(t *testing.T) {
	app, _, _, _, _ := setupTestApp(t)
	body, _ := json.Marshal(CreateSessionRequest{FileName: "foreign.pdf", SourceAssetID: uuid.NewString(), SourceR2Key: storage.NewOwnedKey("victim", "studio_source", ".pdf")})
	req := httptest.NewRequest("POST", "/api/studio/v1/sessions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Guest-ID", "attacker")
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 403, resp.StatusCode)
}
func TestApplyOperationRejectsForeignAssetWithoutAdvancingVersion(t *testing.T) {
	_, _, svc, _, _ := setupTestApp(t)
	ctx := context.Background()
	victim := identity.Identity{ID: uuid.NewString(), Type: identity.TypeGuest}
	attacker := identity.Identity{ID: uuid.NewString(), Type: identity.TypeGuest}
	foreignAsset := uuid.NewString()
	model := vdm.DocumentModel{DocumentID: uuid.NewString(), PageCount: 1, Pages: []vdm.PageDescriptor{{PageID: uuid.NewString(), SourceAssetID: &foreignAsset, SourcePageNumber: 1}}}
	_, _, _, err := svc.CreateDocument(ctx, victim, "victim.pdf", 20, 1, foreignAsset, "studio/source/victim.pdf", model)
	require.NoError(t, err)
	blank := vdm.DocumentModel{DocumentID: uuid.NewString(), PageCount: 1, Pages: []vdm.PageDescriptor{{PageID: uuid.NewString(), IsBlank: true, SourcePageNumber: 1}}}
	_, session, version, err := svc.CreateDocument(ctx, attacker, "blank.pdf", 0, 1, "", "", blank)
	require.NoError(t, err)
	_, err = svc.ApplyOperation(ctx, session.ID, attacker, ApplyOperationRequest{BaseVersionID: version.ID, IdempotencyKey: uuid.NewString(), OperationName: "replace", NewVirtualModel: model})
	require.ErrorIs(t, err, ErrUnauthorized)
	current, _, _, err := svc.GetSession(ctx, session.ID, attacker)
	require.NoError(t, err)
	require.Equal(t, version.ID, current.ActiveVersionID)
}
