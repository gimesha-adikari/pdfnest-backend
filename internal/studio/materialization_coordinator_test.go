package studio

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/studio/models"
	"pdfnest-backend/internal/studio/vdm"
)

type materializationTestProcessors struct {
	calls []string
}

func (p *materializationTestProcessors) copy(inputPath, name string) (string, error) {
	output := filepath.Join(os.TempDir(), "studio-materialization-test-"+name+"-"+uuid.NewString()+".pdf")
	in, err := os.Open(inputPath)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.Create(output)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	return output, nil
}

func (p *materializationTestProcessors) processorSet() MaterializationProcessors {
	return MaterializationProcessors{
		Compress: func(_ context.Context, inputPath string, _ ...string) (string, error) {
			p.calls = append(p.calls, "compress")
			return p.copy(inputPath, "compress")
		},
		Grayscale: func(_ context.Context, inputPath, outputPath string) error {
			p.calls = append(p.calls, "grayscale")
			return copyPDFForMaterializationTest(inputPath, outputPath)
		},
		Repair: func(inputPath, outputPath string) error {
			p.calls = append(p.calls, "repair")
			return copyPDFForMaterializationTest(inputPath, outputPath)
		},
		Redact: func(inputPath, outputDir string, _ []string, _ string) (string, error) {
			p.calls = append(p.calls, "redact")
			output := filepath.Join(outputDir, "redacted.pdf")
			return filepath.Base(output), copyPDFForMaterializationTest(inputPath, output)
		},
		Split: func(inputPath string, pages []string) (string, error) {
			p.calls = append(p.calls, "split:"+pages[0])
			output := filepath.Join(os.TempDir(), "studio-materialization-test-split-"+uuid.NewString()+".pdf")
			return output, api.TrimFile(inputPath, output, pages, model.NewDefaultConfiguration())
		},
		Merge: func(inputPaths []string) (string, error) {
			p.calls = append(p.calls, "merge")
			output := filepath.Join(os.TempDir(), "studio-materialization-test-merge-"+uuid.NewString()+".pdf")
			return output, api.MergeCreateFile(inputPaths, output, false, model.NewDefaultConfiguration())
		},
	}
}

func copyPDFForMaterializationTest(inputPath, outputPath string) error {
	in, err := os.Open(inputPath)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func materializationRequest(t *testing.T, base uuid.UUID, key string, operation MaterializationName, params interface{}) MaterializationRequest {
	t.Helper()
	raw, err := json.Marshal(params)
	require.NoError(t, err)
	return MaterializationRequest{BaseVersionID: base, IdempotencyKey: key, Operation: operation, Parameters: raw}
}

func newMaterializationCoordinatorFixture(t *testing.T) (Service, Repository, StudioFinalizer, identity.Identity, *MaterializationProcessors, *models.StudioSession, *models.StudioVersion, *vdm.DocumentModel) {
	service, repo, _, ident, session, version, initial := finalizerFixture(t)
	processor := &finalizerTestProcessor{}
	finalizer := NewFinalizer(repo, processor)
	testProcessors := &materializationTestProcessors{}
	processors := testProcessors.processorSet()
	return service, repo, finalizer, ident, &processors, session, version, initial
}

func TestStudioMaterializationCoordinator_CompressesCurrentVDMAndReusesSnapshot(t *testing.T) {
	service, repo, finalizer, ident, _, session, version, initial := newMaterializationCoordinatorFixture(t)
	coordinator := NewOperationCoordinator(repo)
	rotated := executeFinalizerCommand(t, coordinator, session.ID, ident, version.ID, CommandRotatePage, RotatePageParameters{PageIDs: []string{initial.Pages[0].PageID}, DeltaDegrees: 90})

	processors := (&materializationTestProcessors{}).processorSet()
	materializer := NewMaterializationCoordinator(repo, finalizer, processors)
	request := materializationRequest(t, rotated.Version.ID, "compress-"+uuid.NewString(), MaterializeCompress, CompressParameters{Level: "medium"})
	result, err := materializer.Execute(context.Background(), session.ID, ident, request)
	require.NoError(t, err)
	assert.False(t, result.IsIdempotentReplay)
	assert.Equal(t, MaterializeCompress, MaterializationName(result.Operation.OperationName))
	assert.True(t, result.Version.IsMaterialized)
	require.NotNil(t, result.Version.SnapshotID)
	assert.Equal(t, result.Asset.ID, *result.VDM.Pages[0].SourceAssetID)
	assert.Equal(t, result.Asset.ID, *result.VDM.Pages[len(result.VDM.Pages)-1].SourceAssetID)
	assert.Greater(t, result.VDM.Pages[0].Dimensions.Width, result.VDM.Pages[0].Dimensions.Height, "the source VDM rotation is represented in the materialized input before compression")

	current, _, active, err := service.GetSession(context.Background(), session.ID, ident)
	require.NoError(t, err)
	assert.Equal(t, result.Version.ID, current.ActiveVersionID)
	assert.Equal(t, result.Version.ID, active.ID)

	replay, err := materializer.Execute(context.Background(), session.ID, ident, request)
	require.NoError(t, err)
	assert.True(t, replay.IsIdempotentReplay)
	assert.Equal(t, result.Version.ID, replay.Version.ID)
	assert.Equal(t, result.Asset.ID, replay.Asset.ID)

	_, err = service.Undo(context.Background(), session.ID, ident)
	require.NoError(t, err)
	undoExport, err := finalizer.Finalize(context.Background(), session.ID, ident)
	require.NoError(t, err)
	assert.NotEqual(t, result.Version.ID, undoExport.Export.VersionID)
	_, err = service.Redo(context.Background(), session.ID, ident)
	require.NoError(t, err)
	redoExport, err := finalizer.Finalize(context.Background(), session.ID, ident)
	require.NoError(t, err)
	assert.Equal(t, result.Version.ID, redoExport.Export.VersionID)
}

func TestStudioMaterializationCoordinator_DerivesSplitVDMAndRejectsStaleResult(t *testing.T) {
	service, repo, finalizer, ident, _, session, version, initial := newMaterializationCoordinatorFixture(t)
	processors := (&materializationTestProcessors{}).processorSet()
	materializer := NewMaterializationCoordinator(repo, finalizer, processors)
	request := materializationRequest(t, version.ID, "split-"+uuid.NewString(), MaterializeSplit, SplitParameters{PageIDs: []string{initial.Pages[0].PageID, initial.Pages[2].PageID}})
	result, err := materializer.Execute(context.Background(), session.ID, ident, request)
	require.NoError(t, err)
	assert.Equal(t, 2, result.VDM.PageCount)
	assert.Equal(t, result.Asset.ID, *result.VDM.Pages[0].SourceAssetID)
	assert.Equal(t, result.Asset.ID, *result.VDM.Pages[1].SourceAssetID)
	assert.Equal(t, result.VDM.Pages[0].SourcePageNumber, 1)
	assert.Equal(t, result.VDM.Pages[1].SourcePageNumber, 2)

	_, err = service.Undo(context.Background(), session.ID, ident)
	require.NoError(t, err)
	staleRequest := request
	staleRequest.IdempotencyKey = "stale-" + uuid.NewString()
	staleRequest.BaseVersionID = result.Version.ID
	_, err = materializer.Execute(context.Background(), session.ID, ident, staleRequest)
	assert.ErrorIs(t, err, ErrInvalidBaseVersion, "the original request cannot overwrite a newer active branch after undo")
}

func TestStudioMaterializationCoordinator_CoversRepresentativeProcessorDispatchAndOwnership(t *testing.T) {
	for _, operation := range []MaterializationName{MaterializeCompress, MaterializeGrayscale, MaterializeRepair, MaterializeRedact} {
		t.Run(string(operation), func(t *testing.T) {
			_, repo, finalizer, ident, _, session, version, _ := newMaterializationCoordinatorFixture(t)
			processors := (&materializationTestProcessors{}).processorSet()
			materializer := NewMaterializationCoordinator(repo, finalizer, processors)
			var params interface{} = struct{}{}
			if operation == MaterializeCompress {
				params = CompressParameters{Level: "low"}
			}
			if operation == MaterializeRedact {
				params = RedactParameters{Keywords: []string{"secret"}}
			}
			result, err := materializer.Execute(context.Background(), session.ID, ident, materializationRequest(t, version.ID, string(operation)+"-"+uuid.NewString(), operation, params))
			require.NoError(t, err)
			assert.Equal(t, string(operation), result.Operation.OperationName)
			assert.True(t, result.Version.IsMaterialized)

			wrong := identity.Identity{ID: "other-" + uuid.NewString(), Type: identity.TypeGuest}
			_, err = materializer.Execute(context.Background(), session.ID, wrong, materializationRequest(t, result.Version.ID, "unauthorized-"+uuid.NewString(), operation, params))
			assert.ErrorIs(t, err, ErrUnauthorized)
		})
	}
}

func TestStudioMaterializationCoordinator_RejectsMergeAssetOwnershipAndUnknownIDs(t *testing.T) {
	service, repo, finalizer, ident, _, session, version, _ := newMaterializationCoordinatorFixture(t)
	processors := (&materializationTestProcessors{}).processorSet()
	materializer := NewMaterializationCoordinator(repo, finalizer, processors)

	otherIdent := identity.Identity{ID: "other-merge-owner-" + uuid.NewString(), Type: identity.TypeGuest}
	otherAssetID := "other-merge-asset-" + uuid.NewString()
	otherPageID := "other-page-" + uuid.NewString()
	otherVDM := vdm.DocumentModel{
		DocumentID: "other-doc-" + uuid.NewString(),
		PageCount:  1,
		Pages: []vdm.PageDescriptor{{
			PageID: otherPageID, SourceAssetID: &otherAssetID, SourcePageNumber: 1,
			Dimensions: &vdm.Dimensions{Width: 612, Height: 792}, Overlays: []vdm.Overlay{},
		}},
	}
	_, _, _, err := service.CreateDocument(context.Background(), otherIdent, "other.pdf", 1, 1, otherAssetID, "studio/sources/shared.pdf", otherVDM)
	require.NoError(t, err)

	request := materializationRequest(t, version.ID, "merge-cross-doc-"+uuid.NewString(), MaterializeMerge, MergeParameters{SourceAssetIDs: []string{otherAssetID}})
	_, err = materializer.Execute(context.Background(), session.ID, ident, request)
	assert.ErrorIs(t, err, ErrUnauthorized)

	wrongOwnerRequest := materializationRequest(t, version.ID, "merge-wrong-owner-"+uuid.NewString(), MaterializeMerge, MergeParameters{SourceAssetIDs: []string{otherAssetID}})
	_, err = materializer.Execute(context.Background(), session.ID, otherIdent, wrongOwnerRequest)
	assert.ErrorIs(t, err, ErrUnauthorized)

	unknownRequest := materializationRequest(t, version.ID, "merge-unknown-"+uuid.NewString(), MaterializeMerge, MergeParameters{SourceAssetIDs: []string{"guessed-asset-id"}})
	_, err = materializer.Execute(context.Background(), session.ID, ident, unknownRequest)
	assert.ErrorIs(t, err, ErrAssetNotFound)
}

func TestStudioMaterializationCoordinator_RejectsInvalidProcessorOutputAndCleansObject(t *testing.T) {
	_, repo, finalizer, ident, _, session, version, _ := newMaterializationCoordinatorFixture(t)
	processors := MaterializationProcessors{
		Compress: func(context.Context, string, ...string) (string, error) {
			path := filepath.Join(os.TempDir(), "studio-invalid-materialization-"+uuid.NewString()+".pdf")
			require.NoError(t, os.WriteFile(path, []byte("not a pdf"), 0600))
			return path, nil
		},
	}
	materializer := NewMaterializationCoordinator(repo, finalizer, processors)
	_, err := materializer.Execute(context.Background(), session.ID, ident, materializationRequest(t, version.ID, "invalid-"+uuid.NewString(), MaterializeCompress, CompressParameters{Level: "medium"}))
	assert.ErrorIs(t, err, ErrInvalidMaterializedOutput)
}

func TestStudioMaterializationCoordinator_RejectsActiveVersionChangeAfterProcessing(t *testing.T) {
	service, repo, finalizer, ident, _, session, version, initial := newMaterializationCoordinatorFixture(t)
	commandCoordinator := NewOperationCoordinator(repo)
	processors := MaterializationProcessors{
		Compress: func(_ context.Context, inputPath string, _ ...string) (string, error) {
			// Simulate another tab winning the session while the expensive leaf is
			// running. The coordinator must refuse to register this stale output.
			executeFinalizerCommand(t, commandCoordinator, session.ID, ident, version.ID, CommandRotatePage, RotatePageParameters{PageIDs: []string{initial.Pages[0].PageID}, DeltaDegrees: 90})
			output := filepath.Join(os.TempDir(), "studio-stale-materialization-"+uuid.NewString()+".pdf")
			return output, copyPDFForMaterializationTest(inputPath, output)
		},
	}
	materializer := NewMaterializationCoordinator(repo, finalizer, processors)
	_, err := materializer.Execute(context.Background(), session.ID, ident, materializationRequest(t, version.ID, "stale-"+uuid.NewString(), MaterializeCompress, CompressParameters{Level: "medium"}))
	assert.ErrorIs(t, err, ErrConflict)
	_, operations, historyErr := service.GetVersionHistory(context.Background(), session.ID, ident)
	require.NoError(t, historyErr)
	assert.Len(t, operations, 1, "the stale materialization must not register a second operation/version")
}
