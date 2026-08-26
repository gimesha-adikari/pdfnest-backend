package studio

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/structure"
)

func TestStudioFinalizer_FlattensFoundationTextOverlayIntoPDF(t *testing.T) {
	service, repo, _, ident, session, version, initial := finalizerFixture(t)
	finalizer := NewFinalizer(repo, structure.NewService())
	coordinator := NewOperationCoordinator(repo)

	rotated, err := coordinator.Execute(context.Background(), session.ID, ident, commandRequest(t, version.ID, "overlay-rotate-"+uuid.NewString(), CommandRotatePage, RotatePageParameters{
		PageIDs: []string{initial.Pages[0].PageID}, DeltaDegrees: 90,
	}))
	require.NoError(t, err)
	cropped, err := coordinator.Execute(context.Background(), session.ID, ident, commandRequest(t, rotated.Version.ID, "overlay-crop-"+uuid.NewString(), CommandCropPage, CropPageParameters{
		PageIDs: []string{initial.Pages[0].PageID}, CropBox: []float64{40, 40, 550, 800},
	}))
	require.NoError(t, err)
	overlayResult, err := coordinator.Execute(context.Background(), session.ID, ident, commandRequest(t, cropped.Version.ID, "overlay-add-"+uuid.NewString(), CommandAddTextOverlay, AddTextOverlayParameters{
		PageID: initial.Pages[0].PageID, Text: "Foundation Overlay", X: 72, Y: 500, FontSize: 18,
	}))
	require.NoError(t, err)

	materialized, err := finalizer.MaterializeVersion(context.Background(), session.ID, ident)
	require.NoError(t, err)
	defer materialized.Cleanup()

	text := extractPDFText(t, materialized.Path)
	assert.Contains(t, text, "Foundation Overlay")
	assert.Equal(t, overlayResult.Version.ID, materialized.Version.ID)
	exportResult, err := finalizer.Finalize(context.Background(), session.ID, ident)
	require.NoError(t, err)
	download, err := finalizer.ResolveDownload(context.Background(), session.ID, exportResult.Export.ID, ident)
	require.NoError(t, err)
	exportedText := extractPDFText(t, download.Path)
	download.Cleanup()
	assert.Contains(t, exportedText, "Foundation Overlay")
	_, _, active, err := service.GetSession(context.Background(), session.ID, ident)
	require.NoError(t, err)
	assert.Equal(t, overlayResult.Version.ID, active.ID)
}

func TestStudioMaterializationCoordinator_CompressPreservesFlattenedOverlayAndRedo(t *testing.T) {
	service, repo, _, ident, session, version, initial := finalizerFixture(t)
	finalizer := NewFinalizer(repo, structure.NewService())
	commandCoordinator := NewOperationCoordinator(repo)
	overlayResult, err := commandCoordinator.Execute(context.Background(), session.ID, ident, commandRequest(t, version.ID, "overlay-materialize-"+uuid.NewString(), CommandAddTextOverlay, AddTextOverlayParameters{
		PageID: initial.Pages[0].PageID, Text: "Compress Overlay", X: 72, Y: 500, FontSize: 18,
	}))
	require.NoError(t, err)

	compressCalls := 0
	processors := MaterializationProcessors{
		Compress: func(_ context.Context, inputPath string, _ ...string) (string, error) {
			compressCalls++
			output := filepath.Join(os.TempDir(), "studio-overlay-compress-"+uuid.NewString()+".pdf")
			data, readErr := os.ReadFile(inputPath)
			if readErr != nil {
				return "", readErr
			}
			return output, os.WriteFile(output, data, 0600)
		},
	}
	materializer := NewMaterializationCoordinator(repo, finalizer, processors)
	result, err := materializer.Execute(context.Background(), session.ID, ident, materializationRequest(t, overlayResult.Version.ID, "compress-overlay-"+uuid.NewString(), MaterializeCompress, CompressParameters{Level: "medium"}))
	require.NoError(t, err)
	assert.True(t, result.Version.IsMaterialized)
	assert.Equal(t, 1, compressCalls)

	materialized, err := finalizer.MaterializeVersion(context.Background(), session.ID, ident)
	require.NoError(t, err)
	text := extractPDFText(t, materialized.Path)
	materialized.Cleanup()
	assert.Contains(t, text, "Compress Overlay")

	_, err = service.Undo(context.Background(), session.ID, ident)
	require.NoError(t, err)
	_, _, undone, err := service.GetSession(context.Background(), session.ID, ident)
	require.NoError(t, err)
	assert.Equal(t, overlayResult.Version.ID, undone.ID)
	_, err = service.Redo(context.Background(), session.ID, ident)
	require.NoError(t, err)
	_, _, redone, err := service.GetSession(context.Background(), session.ID, ident)
	require.NoError(t, err)
	assert.Equal(t, result.Version.ID, redone.ID)
	assert.Equal(t, 1, compressCalls, "redo reuses the immutable materialized child")
}

func TestStudioFinalizer_FlattensPageNumbersAndMaterializationClearsDeferredRule(t *testing.T) {
	service, repo, _, ident, session, version, initial := finalizerFixture(t)
	finalizer := NewFinalizer(repo, structure.NewService())
	coordinator := NewOperationCoordinator(repo)
	numbered, err := coordinator.Execute(context.Background(), session.ID, ident, commandRequest(t, version.ID, "page-numbering-finality-"+uuid.NewString(), CommandUpdatePageNumbering, UpdatePageNumberingParameters{
		Enabled: true, Position: "bc", FontSize: 12, FontFamily: "Helvetica",
	}))
	require.NoError(t, err)

	materialized, err := finalizer.MaterializeVersion(context.Background(), session.ID, ident)
	require.NoError(t, err)
	text := extractPDFText(t, materialized.Path)
	assert.True(t, strings.Contains(text, "1") || strings.Contains(text, "2"), "materialized PDF contains page-number text")
	assert.Equal(t, numbered.Version.ID, materialized.Version.ID)
	materialized.Cleanup()

	processors := MaterializationProcessors{
		Compress: func(_ context.Context, inputPath string, _ ...string) (string, error) {
			output := filepath.Join(os.TempDir(), "studio-page-number-compress-"+uuid.NewString()+".pdf")
			data, readErr := os.ReadFile(inputPath)
			if readErr != nil {
				return "", readErr
			}
			return output, os.WriteFile(output, data, 0600)
		},
	}
	materializer := NewMaterializationCoordinator(repo, finalizer, processors)
	result, err := materializer.Execute(context.Background(), session.ID, ident, materializationRequest(t, numbered.Version.ID, "page-number-compress-"+uuid.NewString(), MaterializeCompress, CompressParameters{Level: "medium"}))
	require.NoError(t, err)
	assert.Nil(t, result.VDM.PageNumbering, "materialized VDM does not retain a deferred numbering rule")

	compressedPath, compressedCleanup, err := storage.ResolveObject(context.Background(), result.Asset.R2Key, "studio-page-number-compressed", ".pdf")
	require.NoError(t, err)
	compressedBytes, err := os.ReadFile(compressedPath)
	require.NoError(t, err)
	compressedCleanup()

	export, err := finalizer.Finalize(context.Background(), session.ID, ident)
	require.NoError(t, err)
	download, err := finalizer.ResolveDownload(context.Background(), session.ID, export.Export.ID, ident)
	require.NoError(t, err)
	exportedBytes, err := os.ReadFile(download.Path)
	require.NoError(t, err)
	download.Cleanup()
	assert.Equal(t, compressedBytes, exportedBytes, "export reuses the materialized snapshot without applying page numbers twice")

	_, _, active, err := service.GetSession(context.Background(), session.ID, ident)
	require.NoError(t, err)
	assert.Equal(t, result.Version.ID, active.ID)
	assert.Equal(t, initial.PageCount, result.VDM.PageCount)
}

func extractPDFText(t *testing.T, path string) string {
	t.Helper()
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext is required for semantic overlay finality verification")
	}
	output, err := exec.Command("pdftotext", path, "-").Output()
	require.NoError(t, err)
	return string(output)
}
