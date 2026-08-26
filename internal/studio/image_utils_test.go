package studio

import (
	"image"
	"image/color"
	"testing"

	"github.com/stretchr/testify/assert"
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
