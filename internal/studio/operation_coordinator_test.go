package studio

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/studio/vdm"
)

type coordinatorFixture struct {
	service     Service
	coordinator OperationCoordinator
	identity    identity.Identity
	sessionID   uuid.UUID
	versionID   uuid.UUID
	pageIDs     []string
	initialVDM  vdm.DocumentModel
}

func newCoordinatorFixture(t *testing.T) coordinatorFixture {
	t.Helper()
	service, repo := getTestServiceAndRepository(t)
	ident := identity.Identity{ID: "command_guest_" + uuid.NewString(), Type: identity.TypeGuest}
	assetID := "ast_command_" + uuid.NewString()
	pageIDs := []string{"page_" + uuid.NewString(), "page_" + uuid.NewString(), "page_" + uuid.NewString()}
	model := vdm.DocumentModel{
		DocumentID: "vdm_command_" + uuid.NewString(),
		PageCount:  3,
		Metadata:   map[string]string{"Title": "coordinator fixture", "custom": "preserve me"},
		Pages: []vdm.PageDescriptor{
			{
				PageID: pageIDs[0], SourceAssetID: &assetID, SourcePageNumber: 1,
				Dimensions: &vdm.Dimensions{Width: 600, Height: 800}, Rotation: 0,
				CropBox:  []float64{1, 2, 590, 790},
				Overlays: []vdm.Overlay{{ID: "overlay-1", Type: "text", Text: "keep", Rect: []float64{10, 20, 30, 40}}},
			},
			{
				PageID: pageIDs[1], SourceAssetID: &assetID, SourcePageNumber: 2,
				Dimensions: &vdm.Dimensions{Width: 610, Height: 810}, Rotation: 90, Overlays: []vdm.Overlay{},
			},
			{
				PageID: pageIDs[2], SourceAssetID: &assetID, SourcePageNumber: 3,
				Dimensions: &vdm.Dimensions{Width: 620, Height: 820}, Rotation: 180, Overlays: []vdm.Overlay{},
			},
		},
	}
	_, session, version, err := service.CreateDocument(
		context.Background(), ident, "commands.pdf", 4096, 3, assetID,
		"studio/sources/"+uuid.NewString()+".pdf", model,
	)
	require.NoError(t, err)
	return coordinatorFixture{
		service: service, coordinator: NewOperationCoordinator(repo), identity: ident,
		sessionID: session.ID, versionID: version.ID, pageIDs: pageIDs, initialVDM: model,
	}
}

func commandRequest(t *testing.T, base uuid.UUID, key string, operation CommandName, parameters interface{}) ExecuteCommandRequest {
	t.Helper()
	raw, err := json.Marshal(parameters)
	require.NoError(t, err)
	return ExecuteCommandRequest{BaseVersionID: base, IdempotencyKey: key, Operation: operation, Parameters: raw}
}

func resultVDM(t *testing.T, result *ApplyOperationResult) *vdm.DocumentModel {
	t.Helper()
	model, err := vdm.FromJSON(result.Version.VirtualModel)
	require.NoError(t, err)
	return model
}

func TestOperationCoordinator_RotateDerivesVersionAndPreservesHistory(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	ctx := context.Background()
	request := commandRequest(t, fixture.versionID, "rotate_"+uuid.NewString(), CommandRotatePage, RotatePageParameters{
		PageIDs: []string{fixture.pageIDs[0], fixture.pageIDs[2]}, DeltaDegrees: -90,
	})

	result, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, request)
	require.NoError(t, err)
	assert.False(t, result.IsIdempotentReplay)
	assert.False(t, result.Version.IsMaterialized)
	assert.Equal(t, 1, result.Version.VersionNumber)
	assert.Equal(t, &fixture.versionID, result.Version.ParentVersionID)
	assert.Equal(t, string(CommandRotatePage), result.Operation.OperationName)
	assert.Equal(t, result.Version.ID, result.Operation.VersionID)

	derived := resultVDM(t, result)
	assert.Equal(t, fixture.pageIDs, []string{derived.Pages[0].PageID, derived.Pages[1].PageID, derived.Pages[2].PageID})
	assert.Equal(t, 270, derived.Pages[0].Rotation)
	assert.Equal(t, 90, derived.Pages[1].Rotation)
	assert.Equal(t, 90, derived.Pages[2].Rotation)
	assert.Equal(t, fixture.initialVDM.Pages[0].CropBox, derived.Pages[0].CropBox)
	assert.Equal(t, fixture.initialVDM.Pages[0].Overlays, derived.Pages[0].Overlays)
	assert.Equal(t, result.Version.ID.String(), derived.VersionID)

	replayed, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, request)
	require.NoError(t, err)
	assert.True(t, replayed.IsIdempotentReplay)
	assert.Equal(t, result.Version.ID, replayed.Version.ID)

	mismatch := commandRequest(t, fixture.versionID, request.IdempotencyKey, CommandRotatePage, RotatePageParameters{
		PageIDs: []string{fixture.pageIDs[0]}, DeltaDegrees: 90,
	})
	_, err = fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, mismatch)
	assert.ErrorIs(t, err, ErrIdempotencyConflict)

	versions, operations, err := fixture.service.GetVersionHistory(ctx, fixture.sessionID, fixture.identity)
	require.NoError(t, err)
	assert.Len(t, versions, 2)
	assert.Len(t, operations, 1)

	undone, err := fixture.service.Undo(ctx, fixture.sessionID, fixture.identity)
	require.NoError(t, err)
	assert.Equal(t, fixture.versionID, undone.ID)
	redone, err := fixture.service.Redo(ctx, fixture.sessionID, fixture.identity)
	require.NoError(t, err)
	assert.Equal(t, result.Version.ID, redone.ID)
	assert.JSONEq(t, string(result.Version.VirtualModel), string(redone.VirtualModel))
}

func TestOperationCoordinator_DeleteValidationAndDerivation(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	ctx := context.Background()

	for name, params := range map[string]struct {
		params  DeletePagesParameters
		wantErr error
	}{
		"unknown":   {DeletePagesParameters{PageIDs: []string{"not-a-page"}}, ErrCommandPageNotFound},
		"duplicate": {DeletePagesParameters{PageIDs: []string{fixture.pageIDs[0], fixture.pageIDs[0]}}, ErrDuplicatePageID},
		"all pages": {DeletePagesParameters{PageIDs: append([]string(nil), fixture.pageIDs...)}, ErrCannotDeleteAll},
	} {
		t.Run(name, func(t *testing.T) {
			request := commandRequest(t, fixture.versionID, name+uuid.NewString(), CommandDeletePages, params.params)
			_, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, request)
			assert.ErrorIs(t, err, params.wantErr)
		})
	}

	request := commandRequest(t, fixture.versionID, "delete_"+uuid.NewString(), CommandDeletePages, DeletePagesParameters{PageIDs: []string{fixture.pageIDs[1]}})
	result, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, request)
	require.NoError(t, err)
	derived := resultVDM(t, result)
	assert.Equal(t, 2, derived.PageCount)
	assert.Equal(t, []string{fixture.pageIDs[0], fixture.pageIDs[2]}, []string{derived.Pages[0].PageID, derived.Pages[1].PageID})
	assert.Equal(t, fixture.initialVDM.Pages[0], derived.Pages[0])
	assert.Equal(t, fixture.initialVDM.Pages[2], derived.Pages[1])
}

func TestOperationCoordinator_ReorderRequiresCompleteStablePageIDs(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	ctx := context.Background()

	invalid := []ReorderPagesParameters{
		{PageIDs: fixture.pageIDs[:2]},
		{PageIDs: []string{fixture.pageIDs[0], fixture.pageIDs[1], fixture.pageIDs[1]}},
		{PageIDs: []string{fixture.pageIDs[0], fixture.pageIDs[1], "not-a-page"}},
	}
	for i, params := range invalid {
		_, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity,
			commandRequest(t, fixture.versionID, fmtKey("reorder_invalid", i), CommandReorderPages, params))
		assert.Error(t, err)
	}

	wanted := []string{fixture.pageIDs[2], fixture.pageIDs[0], fixture.pageIDs[1]}
	result, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity,
		commandRequest(t, fixture.versionID, "reorder_"+uuid.NewString(), CommandReorderPages, ReorderPagesParameters{PageIDs: wanted}))
	require.NoError(t, err)
	derived := resultVDM(t, result)
	actual := []string{derived.Pages[0].PageID, derived.Pages[1].PageID, derived.Pages[2].PageID}
	assert.Equal(t, wanted, actual)
	assert.Equal(t, fixture.initialVDM.Pages[2], derived.Pages[0])
	assert.Equal(t, fixture.initialVDM.Pages[0], derived.Pages[1])
	assert.Equal(t, fixture.initialVDM.Pages[1], derived.Pages[2])
}

func TestOperationCoordinator_DuplicateCreatesUniqueLineageAndPreservesDescriptor(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	request := commandRequest(t, fixture.versionID, "duplicate_"+uuid.NewString(), CommandDuplicatePages, DuplicatePagesParameters{
		PageIDs: []string{fixture.pageIDs[0]}, Copies: 2,
	})
	result, err := fixture.coordinator.Execute(context.Background(), fixture.sessionID, fixture.identity, request)
	require.NoError(t, err)
	derived := resultVDM(t, result)
	require.Len(t, derived.Pages, 5)

	original := derived.Pages[0]
	firstCopy := derived.Pages[1]
	secondCopy := derived.Pages[2]
	assert.NotEqual(t, original.PageID, firstCopy.PageID)
	assert.NotEqual(t, firstCopy.PageID, secondCopy.PageID)
	assert.Equal(t, &original.PageID, firstCopy.ParentPageID)
	assert.Equal(t, original.SourceAssetID, firstCopy.SourceAssetID)
	assert.Equal(t, original.SourcePageNumber, firstCopy.SourcePageNumber)
	assert.Equal(t, original.Rotation, firstCopy.Rotation)
	assert.Equal(t, original.CropBox, firstCopy.CropBox)
	assert.Equal(t, original.Overlays, firstCopy.Overlays)
}

func TestOperationCoordinator_InsertBlankDerivesAdjacentDimensions(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	ctx := context.Background()

	_, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity,
		commandRequest(t, fixture.versionID, "blank_bad_"+uuid.NewString(), CommandInsertBlankPages, InsertBlankPagesParameters{Position: 4, Count: 1}))
	assert.ErrorIs(t, err, ErrInvalidCommand)

	result, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity,
		commandRequest(t, fixture.versionID, "blank_"+uuid.NewString(), CommandInsertBlankPages, InsertBlankPagesParameters{Position: 1, Count: 2}))
	require.NoError(t, err)
	derived := resultVDM(t, result)
	require.Len(t, derived.Pages, 5)
	for _, page := range derived.Pages[1:3] {
		assert.True(t, page.IsBlank)
		assert.Nil(t, page.SourceAssetID)
		assert.Zero(t, page.SourcePageNumber)
		assert.Equal(t, &vdm.Dimensions{Width: 610, Height: 810}, page.Dimensions)
		assert.NotEmpty(t, page.PageID)
	}
	assert.NotEqual(t, derived.Pages[1].PageID, derived.Pages[2].PageID)
}

func TestOperationCoordinator_CropDerivesAuthoritativeBoxAndSupportsUndoRedo(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	ctx := context.Background()
	box := []float64{50, 60, 500, 700}
	request := commandRequest(t, fixture.versionID, "crop_"+uuid.NewString(), CommandCropPage, CropPageParameters{
		PageIDs: []string{fixture.pageIDs[0], fixture.pageIDs[1]}, CropBox: box,
	})

	result, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, request)
	require.NoError(t, err)
	derived := resultVDM(t, result)
	assert.Equal(t, box, derived.Pages[0].CropBox)
	assert.Equal(t, box, derived.Pages[1].CropBox)
	assert.Equal(t, fixture.initialVDM.Pages[0].SourceAssetID, derived.Pages[0].SourceAssetID)
	assert.Equal(t, fixture.initialVDM.Pages[0].Rotation, derived.Pages[0].Rotation)
	assert.Equal(t, fixture.initialVDM.Pages[0].Overlays, derived.Pages[0].Overlays)
	assert.Empty(t, derived.Pages[2].CropBox)

	replayed, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, request)
	require.NoError(t, err)
	assert.True(t, replayed.IsIdempotentReplay)
	assert.Equal(t, result.Version.ID, replayed.Version.ID)

	_, err = fixture.service.Undo(ctx, fixture.sessionID, fixture.identity)
	require.NoError(t, err)
	redone, err := fixture.service.Redo(ctx, fixture.sessionID, fixture.identity)
	require.NoError(t, err)
	assert.Equal(t, result.Version.ID, redone.ID)
	assert.JSONEq(t, string(result.Version.VirtualModel), string(redone.VirtualModel))
}

func TestOperationCoordinator_CropRejectsInvalidGeometry(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	ctx := context.Background()
	invalid := []struct {
		name string
		box  []float64
		want error
	}{
		{name: "wrong length", box: []float64{0, 0, 10}, want: ErrInvalidCommand},
		{name: "inverted", box: []float64{100, 0, 50, 100}, want: ErrInvalidCropBox},
		{name: "outside page", box: []float64{0, 0, 601, 800}, want: ErrInvalidCropBox},
		{name: "zero width", box: []float64{10, 0, 10, 100}, want: ErrInvalidCropBox},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			_, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity,
				commandRequest(t, fixture.versionID, "crop_invalid_"+uuid.NewString(), CommandCropPage, CropPageParameters{
					PageIDs: []string{fixture.pageIDs[0]}, CropBox: test.box,
				}))
			assert.ErrorIs(t, err, test.want)
		})
	}

	_, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, ExecuteCommandRequest{
		BaseVersionID: fixture.versionID, IdempotencyKey: uuid.NewString(), Operation: CommandCropPage,
		Parameters: json.RawMessage(`{"page_ids":["` + fixture.pageIDs[0] + `"],"crop_box":[0,0,NaN,100]}`),
	})
	assert.ErrorIs(t, err, ErrInvalidCommand)
}

func TestOperationCoordinator_UpdateMetadataDerivesStateAndSupportsReloadUndoRedo(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	ctx := context.Background()
	request := commandRequest(t, fixture.versionID, "metadata_"+uuid.NewString(), CommandUpdateMetadata, UpdateMetadataParameters{
		Title: "  Edited title  ", Author: "Edited author", Subject: "Edited subject", Keywords: "one, two",
	})

	result, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, request)
	require.NoError(t, err)
	assert.Equal(t, string(CommandUpdateMetadata), result.Operation.OperationName)
	assert.False(t, result.Version.IsMaterialized)
	derived := resultVDM(t, result)
	assert.Equal(t, map[string]string{
		"Title": "Edited title", "Author": "Edited author", "Subject": "Edited subject", "Keywords": "one, two", "custom": "preserve me",
	}, derived.Metadata)
	assert.Equal(t, fixture.initialVDM.Pages, derived.Pages)

	_, reloadedDoc, reloadedVersion, err := fixture.service.GetSession(ctx, fixture.sessionID, fixture.identity)
	require.NoError(t, err)
	assert.NotNil(t, reloadedDoc)
	reloadedVDM, err := vdm.FromJSON(reloadedVersion.VirtualModel)
	require.NoError(t, err)
	assert.Equal(t, derived.Metadata, reloadedVDM.Metadata)

	replayed, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, request)
	require.NoError(t, err)
	assert.True(t, replayed.IsIdempotentReplay)
	assert.Equal(t, result.Version.ID, replayed.Version.ID)

	_, err = fixture.service.Undo(ctx, fixture.sessionID, fixture.identity)
	require.NoError(t, err)
	_, _, undoneVersion, err := fixture.service.GetSession(ctx, fixture.sessionID, fixture.identity)
	require.NoError(t, err)
	undoneVDM, err := vdm.FromJSON(undoneVersion.VirtualModel)
	require.NoError(t, err)
	assert.Equal(t, fixture.initialVDM.Metadata, undoneVDM.Metadata)

	_, err = fixture.service.Redo(ctx, fixture.sessionID, fixture.identity)
	require.NoError(t, err)
	_, _, redoneVersion, err := fixture.service.GetSession(ctx, fixture.sessionID, fixture.identity)
	require.NoError(t, err)
	redoneVDM, err := vdm.FromJSON(redoneVersion.VirtualModel)
	require.NoError(t, err)
	assert.Equal(t, derived.Metadata, redoneVDM.Metadata)
}

func TestOperationCoordinator_AddTextOverlayDerivesServerIdentityAndSupportsDAG(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	ctx := context.Background()
	params := AddTextOverlayParameters{PageID: fixture.pageIDs[0], Text: "Foundation Overlay", X: 72, Y: 500, FontSize: 18}
	request := commandRequest(t, fixture.versionID, "overlay-"+uuid.NewString(), CommandAddTextOverlay, params)

	result, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, request)
	require.NoError(t, err)
	derived := resultVDM(t, result)
	require.Len(t, derived.Pages[0].Overlays, 2)
	overlay := derived.Pages[0].Overlays[1]
	assert.NotEmpty(t, overlay.ID)
	assert.Equal(t, string(vdm.OverlayTypeText), overlay.Type)
	assert.Equal(t, params.Text, overlay.Text)
	assert.Equal(t, "#000000", overlay.Color)
	assert.Equal(t, params.FontSize, overlay.FontSize)
	assert.Equal(t, []float64{params.X, params.Y, 0, 0}, overlay.Rect)
	assert.Equal(t, fixture.initialVDM.Pages[0].CropBox, derived.Pages[0].CropBox)
	assert.Equal(t, fixture.initialVDM.Metadata, derived.Metadata)

	replayed, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, request)
	require.NoError(t, err)
	assert.True(t, replayed.IsIdempotentReplay)
	assert.Equal(t, result.Version.ID, replayed.Version.ID)
	assert.JSONEq(t, string(result.Version.VirtualModel), string(replayed.Version.VirtualModel))

	_, err = fixture.service.Undo(ctx, fixture.sessionID, fixture.identity)
	require.NoError(t, err)
	_, _, undone, err := fixture.service.GetSession(ctx, fixture.sessionID, fixture.identity)
	require.NoError(t, err)
	undoneModel := resultVDM(t, &ApplyOperationResult{Version: undone})
	assert.Len(t, undoneModel.Pages[0].Overlays, 1)

	_, err = fixture.service.Redo(ctx, fixture.sessionID, fixture.identity)
	require.NoError(t, err)
	_, _, redone, err := fixture.service.GetSession(ctx, fixture.sessionID, fixture.identity)
	require.NoError(t, err)
	redoneModel := resultVDM(t, &ApplyOperationResult{Version: redone})
	assert.Equal(t, overlay.ID, redoneModel.Pages[0].Overlays[1].ID)

	reordered, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, commandRequest(t, redone.ID, "overlay-reorder-"+uuid.NewString(), CommandReorderPages, ReorderPagesParameters{PageIDs: []string{fixture.pageIDs[2], fixture.pageIDs[0], fixture.pageIDs[1]}}))
	require.NoError(t, err)
	reorderedModel := resultVDM(t, reordered)
	assert.Equal(t, overlay.ID, reorderedModel.Pages[1].Overlays[1].ID)

	duplicated, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, commandRequest(t, reordered.Version.ID, "overlay-duplicate-"+uuid.NewString(), CommandDuplicatePages, DuplicatePagesParameters{PageIDs: []string{fixture.pageIDs[0]}, Copies: 1}))
	require.NoError(t, err)
	duplicatedModel := resultVDM(t, duplicated)
	assert.Equal(t, duplicatedModel.Pages[1].Overlays, duplicatedModel.Pages[2].Overlays)

	deleted, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, commandRequest(t, duplicated.Version.ID, "overlay-delete-"+uuid.NewString(), CommandDeletePages, DeletePagesParameters{PageIDs: []string{fixture.pageIDs[0]}}))
	require.NoError(t, err)
	deletedModel := resultVDM(t, deleted)
	assert.NotContains(t, []string{deletedModel.Pages[0].PageID, deletedModel.Pages[1].PageID, deletedModel.Pages[2].PageID}, fixture.pageIDs[0])
	overlayPages := 0
	for _, page := range deletedModel.Pages {
		if len(page.Overlays) > 0 {
			overlayPages++
		}
	}
	assert.Equal(t, 1, overlayPages, "the duplicated page retains its copied descriptor while the deleted page's descriptor disappears")
}

func TestOperationCoordinator_UpdateTextOverlayPreservesIDAndRejectsWrongTarget(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	ctx := context.Background()
	added, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, commandRequest(t, fixture.versionID, "text-update-add-"+uuid.NewString(), CommandAddTextOverlay, AddTextOverlayParameters{
		PageID: fixture.pageIDs[0], Text: "Before", X: 72, Y: 500, FontSize: 18, Color: "#000000",
	}))
	require.NoError(t, err)
	addedModel := resultVDM(t, added)
	overlayID := addedModel.Pages[0].Overlays[1].ID

	updated, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, commandRequest(t, added.Version.ID, "text-update-"+uuid.NewString(), CommandUpdateTextOverlay, UpdateTextOverlayParameters{
		PageID: fixture.pageIDs[0], OverlayID: overlayID, Text: "After", X: 100, Y: 450, FontSize: 24, Color: "#1e3a8a",
	}))
	require.NoError(t, err)
	updatedModel := resultVDM(t, updated)
	assert.Equal(t, overlayID, updatedModel.Pages[0].Overlays[1].ID)
	assert.Equal(t, "After", updatedModel.Pages[0].Overlays[1].Text)
	assert.Equal(t, "#1e3a8a", updatedModel.Pages[0].Overlays[1].Color)
	assert.Equal(t, []float64{100, 450, 0, 0}, updatedModel.Pages[0].Overlays[1].Rect)
	assert.Equal(t, addedModel.Pages[0].Overlays[0], updatedModel.Pages[0].Overlays[0])

	_, err = fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, commandRequest(t, updated.Version.ID, "text-update-unknown-"+uuid.NewString(), CommandUpdateTextOverlay, UpdateTextOverlayParameters{
		PageID: fixture.pageIDs[0], OverlayID: "missing-overlay", Text: "Nope", X: 10, Y: 10, FontSize: 12, Color: "#000000",
	}))
	assert.ErrorIs(t, err, ErrInvalidOverlay)
}

func TestOperationCoordinator_AddTextOverlayRejectsInvalidAndUnauthorizedInput(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	ctx := context.Background()
	tests := []struct {
		name   string
		params json.RawMessage
		want   error
	}{
		{name: "unknown page", params: json.RawMessage(`{"page_id":"missing","text":"x","x":10,"y":10,"font_size":12}`), want: ErrCommandPageNotFound},
		{name: "negative coordinate", params: json.RawMessage(`{"page_id":"` + fixture.pageIDs[0] + `","text":"x","x":-1,"y":10,"font_size":12}`), want: ErrInvalidOverlay},
		{name: "oversized font", params: json.RawMessage(`{"page_id":"` + fixture.pageIDs[0] + `","text":"x","x":10,"y":10,"font_size":257}`), want: ErrInvalidOverlay},
		{name: "unknown field", params: json.RawMessage(`{"page_id":"` + fixture.pageIDs[0] + `","text":"x","x":10,"y":10,"font_size":12,"vdm":{}}`), want: ErrInvalidCommand},
		{name: "nul text", params: json.RawMessage(`{"page_id":"` + fixture.pageIDs[0] + `","text":"\u0000","x":10,"y":10,"font_size":12}`), want: ErrInvalidOverlay},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, ExecuteCommandRequest{BaseVersionID: fixture.versionID, IdempotencyKey: "overlay-invalid-" + uuid.NewString(), Operation: CommandAddTextOverlay, Parameters: test.params})
			assert.ErrorIs(t, err, test.want)
		})
	}

	wrongOwner := identity.Identity{ID: "wrong-overlay-owner-" + uuid.NewString(), Type: identity.TypeGuest}
	_, err := fixture.coordinator.Execute(ctx, fixture.sessionID, wrongOwner, commandRequest(t, fixture.versionID, "overlay-wrong-owner-"+uuid.NewString(), CommandAddTextOverlay, AddTextOverlayParameters{PageID: fixture.pageIDs[0], Text: "x", X: 10, Y: 10, FontSize: 12}))
	assert.ErrorIs(t, err, ErrUnauthorized)
}

func TestOperationCoordinator_AddWatermarkDerivesAllPageOverlaysAndDeletesOnlyTargets(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	ctx := context.Background()
	result, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, commandRequest(t, fixture.versionID, "watermark-"+uuid.NewString(), CommandAddWatermark, AddWatermarkParameters{
		PageIDs: fixture.pageIDs, Kind: "text", Text: "CONFIDENTIAL", Font: "Times-Roman", FontSize: 48, Rotation: 45, Opacity: 0.3, Position: "cc",
	}))
	require.NoError(t, err)
	model := resultVDM(t, result)
	watermarkIDs := make([]DeleteOverlayTarget, 0, len(model.Pages))
	for index, page := range model.Pages {
		expectedOverlayCount := 1
		if index == 0 {
			expectedOverlayCount = 2
		}
		require.Len(t, page.Overlays, expectedOverlayCount)
		overlay := page.Overlays[len(page.Overlays)-1]
		assert.Equal(t, string(vdm.OverlayTypeWatermark), overlay.Type)
		assert.Equal(t, "cc", overlay.Position)
		assert.Equal(t, 0.3, overlay.Opacity)
		assert.Equal(t, []float64{page.Dimensions.Width/2 - overlay.Rect[2]/2, page.Dimensions.Height/2 - overlay.Rect[3]/2, overlay.Rect[2], overlay.Rect[3]}, overlay.Rect)
		assert.NotEmpty(t, overlay.ID)
		watermarkIDs = append(watermarkIDs, DeleteOverlayTarget{PageID: fixture.pageIDs[index], OverlayID: overlay.ID})
	}
	deleted, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, commandRequest(t, result.Version.ID, "delete-watermark-"+uuid.NewString(), CommandDeleteOverlay, DeleteOverlayParameters{Targets: watermarkIDs}))
	require.NoError(t, err)
	deletedModel := resultVDM(t, deleted)
	assert.Len(t, deletedModel.Pages[0].Overlays, 1)
	assert.Equal(t, "overlay-1", deletedModel.Pages[0].Overlays[0].ID)
	assert.Empty(t, deletedModel.Pages[1].Overlays)
}

func TestOperationCoordinator_UpdateMetadataRejectsUnknownInvalidStaleAndUnauthorized(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	ctx := context.Background()

	_, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, ExecuteCommandRequest{
		BaseVersionID: fixture.versionID, IdempotencyKey: uuid.NewString(), Operation: CommandUpdateMetadata,
		Parameters: json.RawMessage(`{"title":"ok","unknown":"reject"}`),
	})
	assert.ErrorIs(t, err, ErrInvalidCommand)

	_, err = fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, ExecuteCommandRequest{
		BaseVersionID: fixture.versionID, IdempotencyKey: uuid.NewString(), Operation: CommandUpdateMetadata,
		Parameters: json.RawMessage(`{"title":"bad\u0000value","author":"a","subject":"s","keywords":"k"}`),
	})
	assert.ErrorIs(t, err, ErrInvalidMetadata)

	first, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity,
		commandRequest(t, fixture.versionID, "metadata_stale_"+uuid.NewString(), CommandUpdateMetadata, UpdateMetadataParameters{Title: "first"}))
	require.NoError(t, err)
	_, err = fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity,
		commandRequest(t, fixture.versionID, "metadata_stale_"+uuid.NewString(), CommandUpdateMetadata, UpdateMetadataParameters{Title: "stale"}))
	assert.ErrorIs(t, err, ErrInvalidBaseVersion)
	assert.NotNil(t, first)

	wrongIdentity := identity.Identity{ID: "wrong_" + uuid.NewString(), Type: identity.TypeGuest}
	_, err = fixture.coordinator.Execute(ctx, fixture.sessionID, wrongIdentity,
		commandRequest(t, first.Version.ID, "metadata_unauthorized_"+uuid.NewString(), CommandUpdateMetadata, UpdateMetadataParameters{Title: "nope"}))
	assert.ErrorIs(t, err, ErrUnauthorized)
}

func TestOperationCoordinator_PageNumberingUsesDocumentRuleAndSupportsDisableUndoRedo(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	ctx := context.Background()
	request := commandRequest(t, fixture.versionID, "page-numbering-"+uuid.NewString(), CommandUpdatePageNumbering, UpdatePageNumberingParameters{
		Enabled: true, Position: "bc", FontSize: 12, FontFamily: "Helvetica",
	})
	result, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, request)
	require.NoError(t, err)
	model := resultVDM(t, result)
	require.NotNil(t, model.PageNumbering)
	assert.True(t, model.PageNumbering.Enabled)
	assert.Equal(t, "%p", model.PageNumbering.Format)
	assert.Equal(t, "bc", model.PageNumbering.Position)
	assert.Equal(t, 12.0, model.PageNumbering.FontSize)
	assert.Equal(t, "Helvetica", model.PageNumbering.FontFamily)
	assert.Equal(t, 1, model.PageNumbering.StartAt)
	assert.Equal(t, fixture.initialVDM.Pages, model.Pages, "page numbering remains document-level state")

	reordered, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, commandRequest(t, result.Version.ID, "page-numbering-reorder-"+uuid.NewString(), CommandReorderPages, ReorderPagesParameters{
		PageIDs: []string{fixture.pageIDs[2], fixture.pageIDs[0], fixture.pageIDs[1]},
	}))
	require.NoError(t, err)
	reorderedModel := resultVDM(t, reordered)
	assert.Equal(t, model.PageNumbering, reorderedModel.PageNumbering)
	assert.Equal(t, fixture.pageIDs[2], reorderedModel.Pages[0].PageID)

	disabled, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, commandRequest(t, reordered.Version.ID, "page-numbering-disable-"+uuid.NewString(), CommandUpdatePageNumbering, UpdatePageNumberingParameters{Enabled: false}))
	require.NoError(t, err)
	disabledModel := resultVDM(t, disabled)
	require.NotNil(t, disabledModel.PageNumbering)
	assert.False(t, disabledModel.PageNumbering.Enabled)
	assert.Equal(t, reorderedModel.Pages, disabledModel.Pages)

	_, err = fixture.service.Undo(ctx, fixture.sessionID, fixture.identity)
	require.NoError(t, err)
	_, _, undoneVersion, err := fixture.service.GetSession(ctx, fixture.sessionID, fixture.identity)
	require.NoError(t, err)
	undoneModel, err := vdm.FromJSON(undoneVersion.VirtualModel)
	require.NoError(t, err)
	assert.True(t, undoneModel.PageNumbering.Enabled)

	_, err = fixture.service.Redo(ctx, fixture.sessionID, fixture.identity)
	require.NoError(t, err)
	_, _, redoneVersion, err := fixture.service.GetSession(ctx, fixture.sessionID, fixture.identity)
	require.NoError(t, err)
	redoneModel, err := vdm.FromJSON(redoneVersion.VirtualModel)
	require.NoError(t, err)
	assert.False(t, redoneModel.PageNumbering.Enabled)
}

func TestOperationCoordinator_PageNumberingFollowsDeleteDuplicateAndBlankSequence(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	ctx := context.Background()
	enabled, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, commandRequest(t, fixture.versionID, "page-numbering-sequence-"+uuid.NewString(), CommandUpdatePageNumbering, UpdatePageNumberingParameters{
		Enabled: true, Position: "bc", FontSize: 12, FontFamily: "Helvetica",
	}))
	require.NoError(t, err)

	deleted, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, commandRequest(t, enabled.Version.ID, "page-numbering-delete-"+uuid.NewString(), CommandDeletePages, DeletePagesParameters{PageIDs: []string{fixture.pageIDs[1]}}))
	require.NoError(t, err)
	deletedModel := resultVDM(t, deleted)
	require.Len(t, deletedModel.Pages, 2)
	assert.Equal(t, []string{"1", "2"}, []string{pageNumberLabel(deletedModel.PageNumbering, 0), pageNumberLabel(deletedModel.PageNumbering, 1)})

	duplicated, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, commandRequest(t, deleted.Version.ID, "page-numbering-duplicate-"+uuid.NewString(), CommandDuplicatePages, DuplicatePagesParameters{PageIDs: []string{fixture.pageIDs[0]}, Copies: 1}))
	require.NoError(t, err)
	duplicatedModel := resultVDM(t, duplicated)
	require.Len(t, duplicatedModel.Pages, 3)
	assert.Equal(t, []string{"1", "2", "3"}, []string{pageNumberLabel(duplicatedModel.PageNumbering, 0), pageNumberLabel(duplicatedModel.PageNumbering, 1), pageNumberLabel(duplicatedModel.PageNumbering, 2)})

	blank, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, commandRequest(t, duplicated.Version.ID, "page-numbering-blank-"+uuid.NewString(), CommandInsertBlankPages, InsertBlankPagesParameters{Position: 1, Count: 1}))
	require.NoError(t, err)
	blankModel := resultVDM(t, blank)
	require.Len(t, blankModel.Pages, 4)
	assert.True(t, blankModel.Pages[1].IsBlank)
	assert.Equal(t, []string{"1", "2", "3", "4"}, []string{pageNumberLabel(blankModel.PageNumbering, 0), pageNumberLabel(blankModel.PageNumbering, 1), pageNumberLabel(blankModel.PageNumbering, 2), pageNumberLabel(blankModel.PageNumbering, 3)})
}

func TestOperationCoordinator_PageNumberingRejectsUnsupportedSettings(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	ctx := context.Background()
	tests := []struct {
		name   string
		params json.RawMessage
	}{
		{name: "unknown field", params: json.RawMessage(`{"enabled":true,"position":"bc","font_size":12,"font_family":"Helvetica","format":"Page {page}"}`)},
		{name: "unsupported position", params: json.RawMessage(`{"enabled":true,"position":"cc","font_size":12,"font_family":"Helvetica"}`)},
		{name: "unsupported font", params: json.RawMessage(`{"enabled":true,"position":"bc","font_size":12,"font_family":"Arial"}`)},
		{name: "invalid size", params: json.RawMessage(`{"enabled":true,"position":"bc","font_size":73,"font_family":"Helvetica"}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, ExecuteCommandRequest{
				BaseVersionID: fixture.versionID, IdempotencyKey: "page-numbering-invalid-" + uuid.NewString(), Operation: CommandUpdatePageNumbering, Parameters: test.params,
			})
			assert.Error(t, err)
		})
	}
}

func TestOperationCoordinator_RejectsUnknownMalformedStaleAndUnauthorized(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	ctx := context.Background()

	_, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, ExecuteCommandRequest{
		BaseVersionID: fixture.versionID, IdempotencyKey: uuid.NewString(), Operation: CommandName("replace_entire_vdm"), Parameters: json.RawMessage(`{}`),
	})
	assert.ErrorIs(t, err, ErrUnknownCommand)

	_, err = fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, ExecuteCommandRequest{
		BaseVersionID: fixture.versionID, IdempotencyKey: uuid.NewString(), Operation: CommandRotatePage,
		Parameters: json.RawMessage(`{"page_ids":["` + fixture.pageIDs[0] + `"],"delta_degrees":90,"new_virtual_model":{}}`),
	})
	assert.ErrorIs(t, err, ErrInvalidCommand)

	_, err = fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity,
		commandRequest(t, fixture.versionID, uuid.NewString(), CommandRotatePage, RotatePageParameters{PageIDs: []string{fixture.pageIDs[0]}, DeltaDegrees: 45}))
	assert.ErrorIs(t, err, ErrInvalidCommand)

	_, err = fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity,
		commandRequest(t, fixture.versionID, uuid.NewString(), CommandDuplicatePages, DuplicatePagesParameters{PageIDs: []string{fixture.pageIDs[0]}, Copies: 11}))
	assert.ErrorIs(t, err, ErrInvalidCommand)

	_, err = fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity,
		commandRequest(t, fixture.versionID, uuid.NewString(), CommandInsertBlankPages, InsertBlankPagesParameters{Position: 0, Count: 0}))
	assert.ErrorIs(t, err, ErrInvalidCommand)

	wrongIdentity := identity.Identity{ID: "wrong_" + uuid.NewString(), Type: identity.TypeGuest}
	_, err = fixture.coordinator.Execute(ctx, fixture.sessionID, wrongIdentity,
		commandRequest(t, fixture.versionID, uuid.NewString(), CommandRotatePage, RotatePageParameters{PageIDs: []string{fixture.pageIDs[0]}, DeltaDegrees: 90}))
	assert.ErrorIs(t, err, ErrUnauthorized)

	first, err := fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity,
		commandRequest(t, fixture.versionID, uuid.NewString(), CommandRotatePage, RotatePageParameters{PageIDs: []string{fixture.pageIDs[0]}, DeltaDegrees: 90}))
	require.NoError(t, err)
	assert.NotNil(t, first)
	_, err = fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity,
		commandRequest(t, fixture.versionID, uuid.NewString(), CommandRotatePage, RotatePageParameters{PageIDs: []string{fixture.pageIDs[1]}, DeltaDegrees: 90}))
	assert.ErrorIs(t, err, ErrInvalidBaseVersion)
}

func TestOperationCoordinator_ConcurrentCommandsSerializeOnBaseVersion(t *testing.T) {
	fixture := newCoordinatorFixture(t)
	ctx := context.Background()
	requests := []ExecuteCommandRequest{
		commandRequest(t, fixture.versionID, uuid.NewString(), CommandRotatePage, RotatePageParameters{PageIDs: []string{fixture.pageIDs[0]}, DeltaDegrees: 90}),
		commandRequest(t, fixture.versionID, uuid.NewString(), CommandRotatePage, RotatePageParameters{PageIDs: []string{fixture.pageIDs[1]}, DeltaDegrees: 90}),
	}

	errs := make([]error, len(requests))
	var wait sync.WaitGroup
	for i := range requests {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, errs[index] = fixture.coordinator.Execute(ctx, fixture.sessionID, fixture.identity, requests[index])
		}(i)
	}
	wait.Wait()

	successes, stale := 0, 0
	for _, err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrInvalidBaseVersion) {
			stale++
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, stale)
}

func fmtKey(prefix string, index int) string {
	return prefix + "_" + string(rune('a'+index)) + "_" + uuid.NewString()
}
