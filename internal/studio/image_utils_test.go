package studio

import (
	"image"
	"image/color"
	"image/draw"
	"testing"

	"github.com/stretchr/testify/assert"

	"pdfnest-backend/internal/studio/vdm"
)

func TestCropImageToPageBoxUsesPDFCoordinatesBeforeAllRotations(t *testing.T) {
	const pageWidth, pageHeight = 600.0, 800.0
	src := image.NewRGBA(image.Rect(0, 0, int(pageWidth), int(pageHeight)))
	// The crop box [100, 200, 500, 700] maps to raster rect
	// x=100..500, y=(800-700)..(800-200), or 100..600.
	src.Set(100, 100, color.RGBA{R: 255, A: 255})
	box := []float64{100, 200, 500, 700}

	cropped := CropImageToPageBox(src, pageWidth, pageHeight, box)
	assert.Equal(t, 400, cropped.Bounds().Dx())
	assert.Equal(t, 500, cropped.Bounds().Dy())
	r, g, b, _ := cropped.At(0, 0).RGBA()
	assert.Greater(t, r, uint32(60000))
	assert.Less(t, g, uint32(1000))
	assert.Less(t, b, uint32(1000))

	for _, rotation := range []int{0, 90, 180, 270} {
		rotated := RotateImage(cropped, rotation)
		if rotation == 90 || rotation == 270 {
			assert.Equal(t, 500, rotated.Bounds().Dx())
			assert.Equal(t, 400, rotated.Bounds().Dy())
		} else {
			assert.Equal(t, 400, rotated.Bounds().Dx())
			assert.Equal(t, 500, rotated.Bounds().Dy())
		}
	}
}

func TestComposePageOverlaysUsesNativeCoordinatesBeforeCropAndRotation(t *testing.T) {
	page := &vdm.PageDescriptor{
		Dimensions: &vdm.Dimensions{Width: 100, Height: 100},
		Overlays: []vdm.Overlay{{
			ID: "overlay-1", Type: string(vdm.OverlayTypeText), Text: "X", FontSize: 13, Color: "#ff0000",
			Rect: []float64{20, 20, 0, 0},
		}},
	}
	src := image.NewRGBA(image.Rect(0, 0, 100, 100))
	draw.Draw(src, src.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)

	composed := ComposePageOverlays(src, page, 1)
	assert.Greater(t, countNonWhitePixels(composed), 0)
	redPixels := 0
	for y := 0; y < composed.Bounds().Dy(); y++ {
		for x := 0; x < composed.Bounds().Dx(); x++ {
			r, g, b, _ := composed.At(x, y).RGBA()
			if r > 50000 && g < 10000 && b < 10000 {
				redPixels++
			}
		}
	}
	assert.Greater(t, redPixels, 0, "V1 text color is reflected in preview")

	page.CropBox = []float64{10, 10, 90, 90}
	cropped := CropImageToPageBox(src, 100, 100, page.CropBox)
	croppedWithOverlay := ComposePageOverlays(cropped, page, 1)
	assert.Greater(t, countNonWhitePixels(croppedWithOverlay), 0, "overlay remains visible after deterministic CropBox conversion")

	for _, rotation := range []int{0, 90, 180, 270} {
		page.Rotation = rotation
		rotated := RotateImage(croppedWithOverlay, rotation)
		assert.Greater(t, countNonWhitePixels(rotated), 0)
		if rotation == 90 || rotation == 270 {
			assert.Equal(t, croppedWithOverlay.Bounds().Dy(), rotated.Bounds().Dx())
			assert.Equal(t, croppedWithOverlay.Bounds().Dx(), rotated.Bounds().Dy())
		} else {
			assert.Equal(t, croppedWithOverlay.Bounds().Dx(), rotated.Bounds().Dx())
			assert.Equal(t, croppedWithOverlay.Bounds().Dy(), rotated.Bounds().Dy())
		}
	}
}

func TestComposePageOverlaysRendersWatermarkImageWithOpacityAndRotation(t *testing.T) {
	page := &vdm.PageDescriptor{
		Dimensions: &vdm.Dimensions{Width: 120, Height: 120},
		Overlays: []vdm.Overlay{{
			ID: "watermark-1", Type: string(vdm.OverlayTypeWatermark), AssetID: "asset-1",
			FontSize: 48, Opacity: 0.3, Rotation: 45, Position: "cc", Rect: []float64{42, 42, 36, 36},
		}},
	}
	src := image.NewRGBA(image.Rect(0, 0, 120, 120))
	draw.Draw(src, src.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	asset := image.NewRGBA(image.Rect(0, 0, 2, 2))
	draw.Draw(asset, asset.Bounds(), &image.Uniform{C: color.RGBA{R: 255, A: 255}}, image.Point{}, draw.Src)
	composed := ComposePageOverlaysWithAssets(src, page, 1, map[string]image.Image{"asset-1": asset})
	assert.Greater(t, countNonWhitePixels(composed), 0)
}

func TestComposePageOverlaysRendersSignatureAssetInNativeRect(t *testing.T) {
	page := &vdm.PageDescriptor{
		Dimensions: &vdm.Dimensions{Width: 200, Height: 200},
		Overlays: []vdm.Overlay{{
			ID: "signature-1", Type: string(vdm.OverlayTypeSignature), AssetID: "sig-asset",
			Rect: []float64{40, 60, 80, 40},
		}},
	}
	src := image.NewRGBA(image.Rect(0, 0, 200, 200))
	draw.Draw(src, src.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	asset := image.NewRGBA(image.Rect(0, 0, 4, 2))
	draw.Draw(asset, asset.Bounds(), &image.Uniform{C: color.RGBA{B: 255, A: 255}}, image.Point{}, draw.Src)
	composed := ComposePageOverlaysWithAssets(src, page, 1, map[string]image.Image{"sig-asset": asset})
	assert.Greater(t, countNonWhitePixels(composed), 0)
	bluePixels := 0
	for y := 0; y < composed.Bounds().Dy(); y++ {
		for x := 0; x < composed.Bounds().Dx(); x++ {
			r, g, b, _ := composed.At(x, y).RGBA()
			if b > 50000 && r < 10000 && g < 10000 {
				bluePixels++
			}
		}
	}
	assert.Greater(t, bluePixels, 0)
}

func TestComposePageNumberingFollowsCurrentOrderAndCropRotation(t *testing.T) {
	page := &vdm.PageDescriptor{
		Dimensions: &vdm.Dimensions{Width: 600, Height: 800},
		CropBox:    []float64{50, 60, 550, 740},
	}
	src := image.NewRGBA(image.Rect(0, 0, 500, 680))
	draw.Draw(src, src.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	rule := &vdm.PageNumberingRule{Enabled: true, Format: "%p", Position: "bc", FontSize: 12, FontFamily: "Helvetica", StartAt: 1}
	assert.Equal(t, "1", pageNumberLabel(rule, 0))
	assert.Equal(t, "2", pageNumberLabel(rule, 1))
	assert.Equal(t, "3", pageNumberLabel(rule, 2))

	first := ComposePageNumbering(src, page, 0, 3, rule, 1)
	third := ComposePageNumbering(src, page, 2, 3, rule, 1)
	different := false
	for y := 0; y < first.Bounds().Dy() && !different; y++ {
		for x := 0; x < first.Bounds().Dx(); x++ {
			if first.At(x, y) != third.At(x, y) {
				different = true
				break
			}
		}
	}
	assert.True(t, different, "current VDM position changes the generated label")
	assert.Greater(t, countNonWhitePixels(first), 0)
	assert.Greater(t, countNonWhitePixels(third), 0)

	for _, position := range []string{"bl", "bc", "br", "tl", "tc", "tr"} {
		rule.Position = position
		composed := ComposePageNumbering(src, page, 1, 3, rule, 1)
		assert.Greater(t, countNonWhitePixels(composed), 0, "position %s renders", position)
		for _, rotation := range []int{0, 90, 180, 270} {
			page.Rotation = rotation
			rotated := RotateImage(composed, rotation)
			assert.Greater(t, countNonWhitePixels(rotated), 0, "position %s rotation %d retains number", position, rotation)
		}
	}
}

func countNonWhitePixels(src image.Image) int {
	count := 0
	for y := src.Bounds().Min.Y; y < src.Bounds().Max.Y; y++ {
		for x := src.Bounds().Min.X; x < src.Bounds().Max.X; x++ {
			r, g, b, _ := src.At(x, y).RGBA()
			if r < 62000 || g < 62000 || b < 62000 {
				count++
			}
		}
	}
	return count
}
