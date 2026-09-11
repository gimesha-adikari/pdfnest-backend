package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/structure"
	"pdfnest-backend/internal/studio/models"
	"pdfnest-backend/internal/studio/vdm"
)

type finalizerTestProcessor struct {
	metadata map[string]string
	failCrop bool
}

func (p *finalizerTestProcessor) CropPDF(inputPath, cropBoxDesc string, selectedPages []string) (string, error) {
	if p.failCrop {
		return "", fmt.Errorf("forced crop failure")
	}
	box, err := model.ParseBox(cropBoxDesc, types.POINTS)
	if err != nil {
		return "", err
	}
	output := filepath.Join(os.TempDir(), "studio-finalizer-test-crop-"+uuid.NewString()+".pdf")
	return output, api.CropFile(inputPath, output, selectedPages, box, model.NewDefaultConfiguration())
}

func (p *finalizerTestProcessor) RotatePDF(inputPath string, rotations map[string]int) (string, error) {
	current := inputPath
	for pages, degrees := range rotations {
		output := filepath.Join(os.TempDir(), "studio-finalizer-test-rotate-"+uuid.NewString()+".pdf")
		if err := api.RotateFile(current, output, degrees, []string{pages}, model.NewDefaultConfiguration()); err != nil {
			return "", err
		}
		if current != inputPath {
			_ = os.Remove(current)
		}
		current = output
	}
	return current, nil
}

func (p *finalizerTestProcessor) AddTextToPDF(inputPath string, _ []structure.TextElement) (string, error) {
	output := filepath.Join(os.TempDir(), "studio-finalizer-test-text-"+uuid.NewString()+".pdf")
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

func (p *finalizerTestProcessor) SignPDF(inputPath string, signaturePath string, outputPath string, stampsJSON string) error {
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

func (p *finalizerTestProcessor) WatermarkPDFOnPages(inputPath string, _ string, _ string, _ string, _ []string) (string, error) {
	output := filepath.Join(os.TempDir(), "studio-finalizer-test-watermark-"+uuid.NewString()+".pdf")
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
	return output, out.Close()
}

func (p *finalizerTestProcessor) AddPageNumbersPDF(inputPath string, _ string) (string, error) {
	output := filepath.Join(os.TempDir(), "studio-finalizer-test-page-numbers-"+uuid.NewString()+".pdf")
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
	return output, out.Close()
}

func (p *finalizerTestProcessor) UpdateMetadataPDF(inputPath string, metadata map[string]string, _ string) (string, error) {
	output := filepath.Join(os.TempDir(), "studio-finalizer-test-metadata-"+uuid.NewString()+".pdf")
	in, err := os.Open(inputPath)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.Create(output)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	p.metadata = map[string]string{"title": metadata["Title"], "author": metadata["Author"], "subject": metadata["Subject"], "keywords": metadata["Keywords"]}
	return output, nil
}

func (p *finalizerTestProcessor) GetMetadataPDF(_ string, _ string) (map[string]string, error) {
	return p.metadata, nil
}

func finalizerFixture(t *testing.T) (Service, Repository, StudioFinalizer, identity.Identity, *models.StudioSession, *models.StudioVersion, *vdm.DocumentModel) {
	t.Helper()
	t.Setenv("LOCAL_STORAGE_DIR", t.TempDir())
	service, repo := getTestServiceAndRepository(t)
	ctx := context.Background()
	ident := identity.Identity{ID: "finalizer_guest_" + uuid.NewString(), Type: identity.TypeGuest}
	assetID := "studio-finalizer-source-" + uuid.NewString()
	key := "studio/sources/" + uuid.NewString() + ".pdf"
	fixturePath := filepath.Join("..", "..", "..", "benchmarks", "rendering", "corpus", "standard_a4_10p.pdf")
	fixture, err := os.Open(fixturePath)
	require.NoError(t, err)
	defer fixture.Close()
	_, _, err = storage.SaveLocalStream(ctx, key, fixture)
	require.NoError(t, err)

	dimensions, err := api.PageDimsFile(fixturePath)
	require.NoError(t, err)
	pages := make([]vdm.PageDescriptor, 4)
	for index := range pages {
		pages[index] = vdm.PageDescriptor{
			PageID:           "page-" + strconv.Itoa(index+1) + "-" + uuid.NewString(),
			SourceAssetID:    &assetID,
			SourcePageNumber: index + 1,
			Dimensions:       &vdm.Dimensions{Width: dimensions[index].Width, Height: dimensions[index].Height},
			Overlays:         []vdm.Overlay{},
		}
	}
	initial := &vdm.DocumentModel{DocumentID: "finalizer-doc-" + uuid.NewString(), PageCount: len(pages), Pages: pages}
	_, session, version, err := service.CreateDocument(ctx, ident, "finalizer source.pdf", 1, len(pages), assetID, key, *initial)
	require.NoError(t, err)
	processor := &finalizerTestProcessor{}
	return service, repo, NewFinalizer(repo, processor), ident, session, version, initial
}

func TestStudioFinalizerMaterializesSourcePersistedInLocalModeWithR2Environment(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("STORAGE_MODE", "local")
	localRoot := t.TempDir()
	t.Setenv("LOCAL_STORAGE_DIR", localRoot)
	t.Setenv("R2_BUCKET", "stale-development-bucket")
	t.Setenv("R2_ACCESS_KEY", "stale-access-key")
	t.Setenv("R2_SECRET_KEY", "stale-secret-key")
	t.Setenv("R2_ENDPOINT", "127.0.0.1:1")

	service, repo := getTestServiceAndRepository(t)
	ctx := context.Background()
	ident := identity.Identity{ID: "finalizer-local-selector-" + uuid.NewString(), Type: identity.TypeGuest}
	assetID := "studio-finalizer-local-selector-" + uuid.NewString()
	sourceKey := storage.BuildKey("studio/sources", ".pdf")
	fixturePath := filepath.Join("..", "..", "..", "benchmarks", "rendering", "corpus", "standard_a4_10p.pdf")
	require.NoError(t, persistStudioSource(ctx, fixturePath, sourceKey, "application/pdf"))

	dimensions, err := api.PageDimsFile(fixturePath)
	require.NoError(t, err)
	initial := vdm.DocumentModel{
		DocumentID: "finalizer-local-selector-doc-" + uuid.NewString(),
		PageCount:  1,
		Pages: []vdm.PageDescriptor{{
			PageID:           "page-local-selector-" + uuid.NewString(),
			SourceAssetID:    &assetID,
			SourcePageNumber: 1,
			Dimensions:       &vdm.Dimensions{Width: dimensions[0].Width, Height: dimensions[0].Height},
			Overlays:         []vdm.Overlay{},
		}},
	}
	_, session, _, err := service.CreateDocument(ctx, ident, "local-selector.pdf", 1, 1, assetID, sourceKey, initial)
	require.NoError(t, err)

	materialized, err := NewFinalizer(repo, &finalizerTestProcessor{}).MaterializeVersion(ctx, session.ID, ident)
	require.NoError(t, err)
	defer materialized.Cleanup()
	require.True(t, strings.HasPrefix(materialized.Path, localRoot+string(os.PathSeparator)), "snapshot must resolve from the active local root")
	require.True(t, storage.ObjectExists(ctx, sourceKey))
	require.True(t, storage.ObjectExists(ctx, materialized.Asset.R2Key))
}

func TestStudioFinalizerMaterializeVersionByIDReusesVerifiedSnapshot(t *testing.T) {
	_, repo, finalizer, ident, session, version, _ := finalizerFixture(t)
	ctx := context.Background()

	// Editor extraction materializes the active immutable version first. A
	// subsequent Editor compile resolves that exact version by ID, so it must use
	// the same verified snapshot rather than rebuild every source page.
	warmed, err := finalizer.MaterializeVersion(ctx, session.ID, ident)
	require.NoError(t, err)
	warmed.Cleanup()

	cachedVersion, err := repo.GetVersion(ctx, version.ID)
	require.NoError(t, err)
	require.NotNil(t, cachedVersion.SnapshotID)
	snapshot, err := repo.GetSnapshot(ctx, *cachedVersion.SnapshotID)
	require.NoError(t, err)

	byID, err := finalizer.(StudioVersionMaterializerByID).MaterializeVersionByID(ctx, session.ID, version.ID, ident)
	require.NoError(t, err)
	defer byID.Cleanup()
	require.NotNil(t, byID.Asset)
	assert.Equal(t, snapshot.AssetID, byID.Asset.ID)
	assert.True(t, strings.HasPrefix(byID.Path, storage.GetLocalStorageDir()+string(os.PathSeparator)), "version-by-ID must resolve the verified snapshot instead of rebuilding the VDM")
}

func registerFinalizerTestSnapshot(t *testing.T, service Service, docID, versionID uuid.UUID, assetID, key string, pageCount int, object []byte) *models.StudioSnapshot {
	t.Helper()
	ctx := context.Background()
	if object != nil {
		_, _, err := storage.SaveLocalStream(ctx, key, bytes.NewReader(object))
		require.NoError(t, err)
	}
	snapshot, err := service.RegisterSnapshot(ctx, docID, versionID, assetID, key, int64(len(object)), pageCount)
	require.NoError(t, err)
	return snapshot
}

func materializeVersionByIDForTest(t *testing.T, finalizer StudioFinalizer, sessionID, versionID uuid.UUID, ident identity.Identity) *MaterializedVersion {
	t.Helper()
	byID, err := finalizer.(StudioVersionMaterializerByID).MaterializeVersionByID(context.Background(), sessionID, versionID, ident)
	require.NoError(t, err)
	return byID
}

func TestStudioFinalizerMaterializeVersionByIDFallsBackForInvalidSnapshots(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "..", "benchmarks", "rendering", "corpus", "standard_a4_10p.pdf")
	validObject, err := os.ReadFile(fixturePath)
	require.NoError(t, err)

	t.Run("no snapshot", func(t *testing.T) {
		_, _, finalizer, ident, session, version, _ := finalizerFixture(t)
		byID := materializeVersionByIDForTest(t, finalizer, session.ID, version.ID, ident)
		defer byID.Cleanup()
		assert.Nil(t, byID.Asset)
		assert.FileExists(t, byID.Path)
	})

	t.Run("missing object", func(t *testing.T) {
		service, repo, finalizer, ident, session, version, initial := finalizerFixture(t)
		registerFinalizerTestSnapshot(t, service, session.DocumentID, version.ID, "missing-snapshot-"+uuid.NewString(), "studio/snapshots/missing-"+uuid.NewString()+".pdf", initial.PageCount, validObject)
		cachedVersion, err := repo.GetVersion(context.Background(), version.ID)
		require.NoError(t, err)
		snapshot, err := repo.GetSnapshot(context.Background(), *cachedVersion.SnapshotID)
		require.NoError(t, err)
		asset, err := repo.GetAsset(context.Background(), snapshot.AssetID)
		require.NoError(t, err)
		require.NoError(t, storage.DeleteLocalObject(asset.R2Key))
		byID := materializeVersionByIDForTest(t, finalizer, session.ID, version.ID, ident)
		defer byID.Cleanup()
		assert.Nil(t, byID.Asset)
		assert.FileExists(t, byID.Path)
	})

	t.Run("foreign document asset", func(t *testing.T) {
		service, repo, finalizer, ident, session, version, initial := finalizerFixture(t)
		foreignVDM := *initial
		foreignVDM.DocumentID = "foreign-finalizer-doc-" + uuid.NewString()
		foreignDoc, _, _, err := service.CreateDocument(context.Background(), ident, "foreign.pdf", int64(len(validObject)), initial.PageCount, "foreign-source-"+uuid.NewString(), "studio/sources/foreign-"+uuid.NewString()+".pdf", foreignVDM)
		require.NoError(t, err)
		registerFinalizerTestSnapshot(t, service, foreignDoc.ID, version.ID, "foreign-snapshot-"+uuid.NewString(), "studio/snapshots/foreign-"+uuid.NewString()+".pdf", initial.PageCount, validObject)
		byID := materializeVersionByIDForTest(t, finalizer, session.ID, version.ID, ident)
		defer byID.Cleanup()
		assert.Nil(t, byID.Asset)
		assert.FileExists(t, byID.Path)
	})

	t.Run("wrong page count", func(t *testing.T) {
		service, repo, finalizer, ident, session, version, initial := finalizerFixture(t)
		registerFinalizerTestSnapshot(t, service, session.DocumentID, version.ID, "wrong-pages-"+uuid.NewString(), "studio/snapshots/wrong-pages-"+uuid.NewString()+".pdf", initial.PageCount+1, validObject)
		byID := materializeVersionByIDForTest(t, finalizer, session.ID, version.ID, ident)
		defer byID.Cleanup()
		assert.Nil(t, byID.Asset)
		assert.FileExists(t, byID.Path)
	})

	t.Run("mismatched snapshot version", func(t *testing.T) {
		service, repo, finalizer, ident, session, version, initial := finalizerFixture(t)
		child := executeFinalizerCommand(t, NewOperationCoordinator(repo), session.ID, ident, version.ID, CommandRotatePage, RotatePageParameters{PageIDs: []string{initial.Pages[0].PageID}, DeltaDegrees: 90})
		childSnapshot := registerFinalizerTestSnapshot(t, service, session.DocumentID, child.Version.ID, "child-snapshot-"+uuid.NewString(), "studio/snapshots/child-"+uuid.NewString()+".pdf", initial.PageCount, validObject)
		gormRepo, ok := repo.(*gormRepository)
		require.True(t, ok)
		require.NoError(t, gormRepo.db.Model(&models.StudioVersion{}).Where("id = ?", version.ID).Update("snapshot_id", childSnapshot.ID).Error)
		byID := materializeVersionByIDForTest(t, finalizer, session.ID, version.ID, ident)
		defer byID.Cleanup()
		assert.Nil(t, byID.Asset)
		assert.FileExists(t, byID.Path)
	})

	t.Run("non-file storage object", func(t *testing.T) {
		service, repo, finalizer, ident, session, version, initial := finalizerFixture(t)
		key := "studio/snapshots/non-file-" + uuid.NewString() + ".pdf"
		require.NoError(t, os.MkdirAll(filepath.Join(storage.GetLocalStorageDir(), filepath.FromSlash(key)), 0o755))
		registerFinalizerTestSnapshot(t, service, session.DocumentID, version.ID, "non-file-snapshot-"+uuid.NewString(), key, initial.PageCount, nil)
		byID := materializeVersionByIDForTest(t, finalizer, session.ID, version.ID, ident)
		defer byID.Cleanup()
		assert.Nil(t, byID.Asset)
		assert.FileExists(t, byID.Path)
	})
}

func executeFinalizerCommand(t *testing.T, coordinator OperationCoordinator, sessionID uuid.UUID, ident identity.Identity, baseVersionID uuid.UUID, operation CommandName, parameters interface{}) *ApplyOperationResult {
	t.Helper()
	raw, err := json.Marshal(parameters)
	require.NoError(t, err)
	result, err := coordinator.Execute(context.Background(), sessionID, ident, ExecuteCommandRequest{BaseVersionID: baseVersionID, IdempotencyKey: "finalizer-" + string(operation) + "-" + uuid.NewString(), Operation: operation, Parameters: raw})
	require.NoError(t, err)
	return result
}

func TestStudioFinalizer_MaterializesAuthoritativeVDMAndReusesSnapshot(t *testing.T) {
	service, repo, finalizer, ident, session, version, initial := finalizerFixture(t)
	coordinator := NewOperationCoordinator(repo)
	ctx := context.Background()

	result := executeFinalizerCommand(t, coordinator, session.ID, ident, version.ID, CommandReorderPages, ReorderPagesParameters{PageIDs: []string{initial.Pages[1].PageID, initial.Pages[0].PageID, initial.Pages[2].PageID, initial.Pages[3].PageID}})
	result = executeFinalizerCommand(t, coordinator, session.ID, ident, result.Version.ID, CommandRotatePage, RotatePageParameters{PageIDs: []string{initial.Pages[1].PageID}, DeltaDegrees: 90})
	result = executeFinalizerCommand(t, coordinator, session.ID, ident, result.Version.ID, CommandCropPage, CropPageParameters{PageIDs: []string{initial.Pages[0].PageID}, CropBox: []float64{100, 100, 450, 700}})
	result = executeFinalizerCommand(t, coordinator, session.ID, ident, result.Version.ID, CommandDeletePages, DeletePagesParameters{PageIDs: []string{initial.Pages[3].PageID}})
	result = executeFinalizerCommand(t, coordinator, session.ID, ident, result.Version.ID, CommandDuplicatePages, DuplicatePagesParameters{PageIDs: []string{initial.Pages[2].PageID}, Copies: 1})
	result = executeFinalizerCommand(t, coordinator, session.ID, ident, result.Version.ID, CommandInsertBlankPages, InsertBlankPagesParameters{Position: 3, Count: 1})
	result = executeFinalizerCommand(t, coordinator, session.ID, ident, result.Version.ID, CommandUpdateMetadata, UpdateMetadataParameters{Title: "Finalizer title", Author: "Studio test", Subject: "VDM materialization", Keywords: "finalizer,studio"})

	exportResult, err := finalizer.Finalize(ctx, session.ID, ident)
	require.NoError(t, err)
	assert.Equal(t, result.Version.ID, exportResult.Export.VersionID)
	assert.Equal(t, "finalizer source-studio.pdf", exportResult.FileName)
	assert.True(t, storage.ObjectExists(ctx, exportResult.Export.R2Key))

	path, cleanup, err := storage.ResolveObject(ctx, exportResult.Export.R2Key, "studio-finalizer-test", ".pdf")
	require.NoError(t, err)
	defer cleanup()
	assert.NoError(t, api.ValidateFile(path, model.NewDefaultConfiguration()))
	count, err := api.PageCountFile(path)
	require.NoError(t, err)
	assert.Equal(t, 5, count)
	dimensions, err := api.PageDimsFile(path)
	require.NoError(t, err)
	assert.Greater(t, dimensions[0].Width, dimensions[0].Height, "the reordered source page 2 was rotated")
	assert.Equal(t, initial.Pages[0].Dimensions.Width, dimensions[1].Width, "crop preserves the native MediaBox while adding the CropBox")
	assert.Equal(t, initial.Pages[0].Dimensions.Height, dimensions[1].Height)
	finalFile, err := os.Open(path)
	require.NoError(t, err)
	boundaries, err := api.Boxes(finalFile, []string{"2"}, model.NewDefaultConfiguration())
	_ = finalFile.Close()
	require.NoError(t, err)
	// pdfcpu reports effective boundaries for the complete document even when
	// a page selection was used to query it; descriptor index 1 is the cropped
	// second final page in this fixture.
	require.Len(t, boundaries, 5)
	cropBox := boundaries[1].CropBox()
	require.NotNil(t, cropBox)
	assert.InDelta(t, 100, cropBox.LL.X, 0.01)
	assert.InDelta(t, 100, cropBox.LL.Y, 0.01)
	assert.InDelta(t, 450, cropBox.UR.X, 0.01)
	assert.InDelta(t, 700, cropBox.UR.Y, 0.01)
	assert.Equal(t, initial.Pages[2].Dimensions.Width, dimensions[3].Width, "blank page uses authoritative dimensions")
	assert.Equal(t, initial.Pages[2].Dimensions.Height, dimensions[3].Height)

	sessionState, _, activeVersion, err := service.GetSession(ctx, session.ID, ident)
	require.NoError(t, err)
	assert.Equal(t, result.Version.ID, sessionState.ActiveVersionID)
	assert.Equal(t, result.Version.ID, activeVersion.ID)
	require.NotNil(t, activeVersion.SnapshotID)

	retryResult, err := finalizer.Finalize(ctx, session.ID, ident)
	require.NoError(t, err)
	assert.NotEqual(t, exportResult.Export.ID, retryResult.Export.ID)
	assert.Equal(t, exportResult.Export.R2Key, retryResult.Export.R2Key, "retry reuses the immutable version snapshot")
}

func TestStudioFinalizer_UsesActiveUndoRedoStateAndEnforcesOwnership(t *testing.T) {
	_, repo, finalizer, ident, session, version, initial := finalizerFixture(t)
	coordinator := NewOperationCoordinator(repo)
	ctx := context.Background()

	rotated := executeFinalizerCommand(t, coordinator, session.ID, ident, version.ID, CommandRotatePage, RotatePageParameters{PageIDs: []string{initial.Pages[0].PageID}, DeltaDegrees: 90})
	service := NewService(repo)
	undone, err := service.Undo(ctx, session.ID, ident)
	require.NoError(t, err)
	assert.Equal(t, version.ID, undone.ID)
	undoExport, err := finalizer.Finalize(ctx, session.ID, ident)
	require.NoError(t, err)
	assert.Equal(t, version.ID, undoExport.Export.VersionID)

	redone, err := service.Redo(ctx, session.ID, ident)
	require.NoError(t, err)
	assert.Equal(t, rotated.Version.ID, redone.ID)
	redoExport, err := finalizer.Finalize(ctx, session.ID, ident)
	require.NoError(t, err)
	assert.Equal(t, rotated.Version.ID, redoExport.Export.VersionID)

	wrongIdentity := identity.Identity{ID: "wrong-" + uuid.NewString(), Type: identity.TypeGuest}
	_, err = finalizer.Finalize(ctx, session.ID, wrongIdentity)
	assert.ErrorIs(t, err, ErrUnauthorized)
	_, err = finalizer.ResolveDownload(ctx, session.ID, redoExport.Export.ID, wrongIdentity)
	assert.ErrorIs(t, err, ErrUnauthorized)
	download, err := finalizer.ResolveDownload(ctx, session.ID, redoExport.Export.ID, ident)
	require.NoError(t, err)
	defer download.Cleanup()
	assert.True(t, strings.HasSuffix(download.FileName, "-studio.pdf"))
}

func TestStudioFinalizer_FailureLeavesActiveVersionAndNoExportObject(t *testing.T) {
	_, repo, _, ident, session, version, initial := finalizerFixture(t)
	coordinator := NewOperationCoordinator(repo)
	rotated := executeFinalizerCommand(t, coordinator, session.ID, ident, version.ID, CommandCropPage, CropPageParameters{PageIDs: []string{initial.Pages[0].PageID}, CropBox: []float64{100, 100, 450, 700}})
	finalizer := NewFinalizer(repo, &finalizerTestProcessor{failCrop: true})

	_, err := finalizer.Finalize(context.Background(), session.ID, ident)
	assert.ErrorIs(t, err, ErrFinalizationFailed)
	service := NewService(repo)
	current, _, active, getErr := service.GetSession(context.Background(), session.ID, ident)
	require.NoError(t, getErr)
	assert.Equal(t, rotated.Version.ID, current.ActiveVersionID)
	assert.Equal(t, rotated.Version.ID, active.ID)
	assert.Nil(t, active.SnapshotID, "a failed materialization must not register a snapshot")
}
