package studio

import (
	"image"
	"image/color"
	"image/draw"
)

// RotateImage rotates an image by 0, 90, 180, or 270 degrees clockwise.
func RotateImage(src image.Image, rotation int) image.Image {
	normRot := ((rotation % 360) + 360) % 360
	if normRot == 0 {
		return src
	}

	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	switch normRot {
	case 90:
		dst := image.NewRGBA(image.Rect(0, 0, h, w))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dst.Set(h-1-y, x, src.At(bounds.Min.X+x, bounds.Min.Y+y))
			}
		}
		return dst
	case 180:
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dst.Set(w-1-x, h-1-y, src.At(bounds.Min.X+x, bounds.Min.Y+y))
			}
		}
		return dst
	case 270:
		dst := image.NewRGBA(image.Rect(0, 0, h, w))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dst.Set(y, w-1-x, src.At(bounds.Min.X+x, bounds.Min.Y+y))
			}
		}
		return dst
	default:
		return src
	}
}

// CropTile extracts a sub-rectangle (tileX, tileY, tileW, tileH) from src.
func CropTile(src image.Image, tileX, tileY, tileW, tileH int) image.Image {
	bounds := src.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()

	if tileX == 0 && tileY == 0 && tileW == srcW && tileH == srcH {
		return src
	}

	dst := image.NewRGBA(image.Rect(0, 0, tileW, tileH))
	// Fill with white default
	draw.Draw(dst, dst.Bounds(), &image.Uniform{C: color.RGBA{255, 255, 255, 255}}, image.Point{}, draw.Src)

	for y := 0; y < tileH; y++ {
		srcY := bounds.Min.Y + tileY + y
		if srcY >= bounds.Max.Y {
			break
		}
		for x := 0; x < tileW; x++ {
			srcX := bounds.Min.X + tileX + x
			if srcX >= bounds.Max.X {
				break
			}
			dst.Set(x, y, src.At(srcX, srcY))
		}
	}
	return dst
}
