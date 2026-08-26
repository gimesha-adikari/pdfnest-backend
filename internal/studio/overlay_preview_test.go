package studio

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStudioTilePreview_RendersDeferredOverlayAcrossCropRotationUndoRedo(t *testing.T) {
	service, repo, _, ident, session, version, initial := finalizerFixture(t)
	renderer := NewTileRenderer(repo)
	renderer.SetWorkerRenderer(func(_ context.Context, _ string, _ int, _ float64) (image.Image, error) {
		base := image.NewRGBA(image.Rect(0, 0, 596, 842))
		draw.Draw(base, base.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
		return base, nil
	})
	coordinator := NewOperationCoordinator(repo)

	cropped, err := coordinator.Execute(context.Background(), session.ID, ident, commandRequest(t, version.ID, "preview-crop-"+newTestUUID(), CommandCropPage, CropPageParameters{
		PageIDs: []string{initial.Pages[0].PageID}, CropBox: []float64{40, 40, 550, 800},
	}))
	require.NoError(t, err)
	rotated, err := coordinator.Execute(context.Background(), session.ID, ident, commandRequest(t, cropped.Version.ID, "preview-rotate-"+newTestUUID(), CommandRotatePage, RotatePageParameters{
		PageIDs: []string{initial.Pages[0].PageID}, DeltaDegrees: 90,
	}))
	require.NoError(t, err)
	overlayResult, err := coordinator.Execute(context.Background(), session.ID, ident, commandRequest(t, rotated.Version.ID, "preview-overlay-"+newTestUUID(), CommandAddTextOverlay, AddTextOverlayParameters{
		PageID: initial.Pages[0].PageID, Text: "Preview Overlay", X: 72, Y: 500, FontSize: 18,
	}))
	require.NoError(t, err)

	request := TileRequest{SessionID: session.ID, VersionID: overlayResult.Version.ID, PageID: initial.Pages[0].PageID, Scale: 1, TileW: 760, TileH: 510}
	withOverlay, err := renderer.RenderTile(context.Background(), request, ident)
	require.NoError(t, err)
	assert.Greater(t, jpegInkPixels(t, withOverlay), 10)

	_, err = service.Undo(context.Background(), session.ID, ident)
	require.NoError(t, err)
	withoutOverlay, err := renderer.RenderTile(context.Background(), TileRequest{SessionID: session.ID, VersionID: rotated.Version.ID, PageID: initial.Pages[0].PageID, Scale: 1, TileW: 760, TileH: 510}, ident)
	require.NoError(t, err)
	assert.Equal(t, 0, jpegInkPixels(t, withoutOverlay))

	_, err = service.Redo(context.Background(), session.ID, ident)
	require.NoError(t, err)
	redone, err := renderer.RenderTile(context.Background(), request, ident)
	require.NoError(t, err)
	assert.Greater(t, jpegInkPixels(t, redone), 10)

	_, _, active, err := service.GetSession(context.Background(), session.ID, ident)
	require.NoError(t, err)
	assert.Equal(t, overlayResult.Version.ID, active.ID)
}

func jpegInkPixels(t *testing.T, data []byte) int {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(data))
	require.NoError(t, err)
	count := 0
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r < 62000 || g < 62000 || b < 62000 {
				count++
			}
		}
	}
	return count
}

func newTestUUID() string {
	return uuid.NewString()
}
