package studio

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"strconv"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"pdfnest-backend/internal/studio/vdm"
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

// CropImageToPageBox crops an image rendered in the page's unrotated native
// coordinate system. CropBox uses a PDF lower-left origin, while raster image
// coordinates use a top-left origin; the y-axis conversion happens here.
func CropImageToPageBox(src image.Image, pageWidthPt, pageHeightPt float64, cropBox []float64) image.Image {
	if src == nil || len(cropBox) != 4 || pageWidthPt <= 0 || pageHeightPt <= 0 {
		return src
	}

	bounds := src.Bounds()
	scaleX := float64(bounds.Dx()) / pageWidthPt
	scaleY := float64(bounds.Dy()) / pageHeightPt
	left := int(math.Round(cropBox[0] * scaleX))
	top := int(math.Round((pageHeightPt - cropBox[3]) * scaleY))
	right := int(math.Round(cropBox[2] * scaleX))
	bottom := int(math.Round((pageHeightPt - cropBox[1]) * scaleY))

	left = maxInt(0, minInt(left, bounds.Dx()-1))
	top = maxInt(0, minInt(top, bounds.Dy()-1))
	right = maxInt(left+1, minInt(right, bounds.Dx()))
	bottom = maxInt(top+1, minInt(bottom, bounds.Dy()))
	if right <= left || bottom <= top {
		return src
	}
	return CropTile(src, left, top, right-left, bottom-top)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
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

// ComposePageOverlays draws deferred overlays without external assets.
func ComposePageOverlays(src image.Image, page *vdm.PageDescriptor, scale float64) image.Image {
	return ComposePageOverlaysWithAssets(src, page, scale, nil)
}

// ComposePageOverlaysWithAssets draws deferred overlays in their VDM array
// order. Asset images are resolved by the preview boundary, never supplied by
// the browser as paths or bytes.
func ComposePageOverlaysWithAssets(src image.Image, page *vdm.PageDescriptor, scale float64, assets map[string]image.Image) image.Image {
	if src == nil || page == nil || len(page.Overlays) == 0 || scale <= 0 {
		return src
	}

	pageWidth, pageHeight := float64(src.Bounds().Dx())/scale, float64(src.Bounds().Dy())/scale
	if page.Dimensions != nil && page.Dimensions.Width > 0 && page.Dimensions.Height > 0 {
		pageWidth, pageHeight = page.Dimensions.Width, page.Dimensions.Height
	}
	cropBox := page.CropBox
	if len(cropBox) != 4 {
		cropBox = []float64{0, 0, pageWidth, pageHeight}
	}

	dst := image.NewRGBA(image.Rect(0, 0, src.Bounds().Dx(), src.Bounds().Dy()))
	draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)
	for _, overlay := range page.Overlays {
		if overlay.Type != string(vdm.OverlayTypeText) && overlay.Type != string(vdm.OverlayTypeWatermark) && overlay.Type != string(vdm.OverlayTypeSignature) {
			continue
		}
		if len(overlay.Rect) < 2 {
			continue
		}
		fontSize := overlay.FontSize
		if fontSize <= 0 {
			fontSize = 12
		}
		widthPt, heightPt := 0.0, 0.0
		if len(overlay.Rect) >= 4 {
			widthPt, heightPt = overlay.Rect[2], overlay.Rect[3]
		}
		if (overlay.Type == string(vdm.OverlayTypeWatermark) || overlay.Type == string(vdm.OverlayTypeSignature)) && overlay.AssetID != "" {
			widthPt, heightPt = math.Max(widthPt, fontSize*1.5), math.Max(heightPt, fontSize*1.5)
		}
		if widthPt <= 0 {
			widthPt = math.Max(1, float64(len([]rune(overlay.Text)))*fontSize*0.24)
		}
		if heightPt <= 0 {
			heightPt = math.Max(1, fontSize)
		}
		layerW, layerH := maxInt(1, int(math.Round(widthPt*scale))), maxInt(1, int(math.Round(heightPt*scale)))
		layer := image.NewRGBA(image.Rect(0, 0, layerW, layerH))
		if overlay.AssetID != "" {
			if asset := assets[overlay.AssetID]; asset != nil {
				fitImage(layer, asset)
			}
		} else if overlay.Text != "" {
			drawScaledBasicTextWithColor(layer, 0, maxInt(0, layerH-int(math.Round(fontSize*0.4*scale))), overlay.Text, fontSize*0.4*scale, parseOverlayColor(overlay.Color))
		}
		if overlay.Opacity > 0 && overlay.Opacity < 1 {
			applyImageOpacity(layer, overlay.Opacity)
		}
		rotated := rotateImageArbitrary(layer, float64(overlay.Rotation))
		centerX := (overlay.Rect[0] - cropBox[0] + widthPt/2) * scale
		centerY := (cropBox[3] - overlay.Rect[1] - heightPt/2) * scale
		draw.Draw(dst, image.Rect(int(math.Round(centerX))-rotated.Bounds().Dx()/2, int(math.Round(centerY))-rotated.Bounds().Dy()/2, int(math.Round(centerX))-rotated.Bounds().Dx()/2+rotated.Bounds().Dx(), int(math.Round(centerY))-rotated.Bounds().Dy()/2+rotated.Bounds().Dy()), rotated, rotated.Bounds().Min, draw.Over)
	}
	return dst
}

// ComposePageNumbering draws the transient document-level page number after
// CropBox composition and before page rotation. The number is derived from
// the current VDM order, never from source_page_number or page identity.
func ComposePageNumbering(src image.Image, page *vdm.PageDescriptor, pageIndex, totalPages int, rule *vdm.PageNumberingRule, scale float64) image.Image {
	if src == nil || page == nil || rule == nil || !rule.Enabled || scale <= 0 || pageIndex < 0 || totalPages <= 0 {
		return src
	}
	position := normalizePageNumberPosition(rule.Position)
	if position == "" {
		return src
	}
	fontSize := rule.FontSize
	if fontSize <= 0 {
		fontSize = 12
	}
	label := pageNumberLabel(rule, pageIndex)
	layer := renderBasicText(label, fontSize*scale)
	if layer == nil {
		return src
	}
	margin := maxInt(1, int(math.Round(20*scale)))
	width, height := src.Bounds().Dx(), src.Bounds().Dy()
	x, y := margin, margin
	if position[1] == 'c' {
		x = (width - layer.Bounds().Dx()) / 2
	} else if position[1] == 'r' {
		x = width - margin - layer.Bounds().Dx()
	}
	if position[0] == 't' {
		y = margin
	} else {
		y = height - margin - layer.Bounds().Dy()
	}
	x = maxInt(0, minInt(x, maxInt(0, width-layer.Bounds().Dx())))
	y = maxInt(0, minInt(y, maxInt(0, height-layer.Bounds().Dy())))
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)
	draw.Draw(dst, image.Rect(x, y, x+layer.Bounds().Dx(), y+layer.Bounds().Dy()), layer, layer.Bounds().Min, draw.Over)
	return dst
}

func pageNumberLabel(rule *vdm.PageNumberingRule, pageIndex int) string {
	startAt := 1
	if rule != nil && rule.StartAt > 0 {
		startAt = rule.StartAt
	}
	return strconv.Itoa(startAt + pageIndex)
}

func normalizePageNumberPosition(position string) string {
	switch position {
	case "bl", "bc", "br", "tl", "tc", "tr":
		return position
	case "bottom-left":
		return "bl"
	case "bottom-center":
		return "bc"
	case "bottom-right":
		return "br"
	case "top-left":
		return "tl"
	case "top-center":
		return "tc"
	case "top-right":
		return "tr"
	default:
		return ""
	}
}

func fitImage(dst draw.Image, src image.Image) {
	if dst == nil || src == nil {
		return
	}
	sb := src.Bounds()
	scale := math.Min(float64(dst.Bounds().Dx())/float64(sb.Dx()), float64(dst.Bounds().Dy())/float64(sb.Dy()))
	w, h := maxInt(1, int(math.Round(float64(sb.Dx())*scale))), maxInt(1, int(math.Round(float64(sb.Dy())*scale)))
	x, y := (dst.Bounds().Dx()-w)/2, (dst.Bounds().Dy()-h)/2
	xdraw.CatmullRom.Scale(dst, image.Rect(x, y, x+w, y+h), src, sb, xdraw.Over, nil)
}

func applyImageOpacity(img *image.RGBA, opacity float64) {
	if img == nil {
		return
	}
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			c := img.RGBAAt(x, y)
			c.A = uint8(math.Round(float64(c.A) * opacity))
			img.SetRGBA(x, y, c)
		}
	}
}

func rotateImageArbitrary(src image.Image, degrees float64) image.Image {
	if src == nil || math.Abs(math.Mod(degrees, 360)) < 0.001 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	radians := degrees * math.Pi / 180
	cosine, sine := math.Cos(radians), math.Sin(radians)
	outW := maxInt(1, int(math.Ceil(math.Abs(float64(w)*cosine)+math.Abs(float64(h)*sine))))
	outH := maxInt(1, int(math.Ceil(math.Abs(float64(w)*sine)+math.Abs(float64(h)*cosine))))
	dst := image.NewRGBA(image.Rect(0, 0, outW, outH))
	srcCX, srcCY := float64(w-1)/2, float64(h-1)/2
	dstCX, dstCY := float64(outW-1)/2, float64(outH-1)/2
	for y := 0; y < outH; y++ {
		for x := 0; x < outW; x++ {
			dx, dy := float64(x)-dstCX, float64(y)-dstCY
			sx := cosine*dx + sine*dy + srcCX
			sy := -sine*dx + cosine*dy + srcCY
			ix, iy := int(math.Round(sx)), int(math.Round(sy))
			if ix >= 0 && ix < w && iy >= 0 && iy < h {
				dst.Set(x, y, src.At(b.Min.X+ix, b.Min.Y+iy))
			}
		}
	}
	return dst
}

/*
// onto the cropped, unrotated page image. The caller rotates the result using
// the page's VDM rotation afterward, so preview coordinates follow the same
// native-page contract as finalization for 0, 90, 180, and 270 degrees.
//
// The preview uses a deterministic embedded bitmap face as a lightweight
// foundation fallback. Final PDF output uses the existing Helvetica text
// processor; the semantic contract is placement, not pixel-identical glyph
// rasterization.

	func ComposePageOverlays(src image.Image, page *vdm.PageDescriptor, scale float64) image.Image {
		if src == nil || page == nil || len(page.Overlays) == 0 || scale <= 0 {
			return src
		}

		pageWidth, pageHeight := float64(src.Bounds().Dx())/scale, float64(src.Bounds().Dy())/scale
		if page.Dimensions != nil && page.Dimensions.Width > 0 && page.Dimensions.Height > 0 {
			pageWidth, pageHeight = page.Dimensions.Width, page.Dimensions.Height
		}
		cropBox := page.CropBox
		if len(cropBox) != 4 {
			cropBox = []float64{0, 0, pageWidth, pageHeight}
		}

		dst := image.NewRGBA(image.Rect(0, 0, src.Bounds().Dx(), src.Bounds().Dy()))
		draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)
		for _, overlay := range page.Overlays {
			if overlay.Type != string(vdm.OverlayTypeText) || len(overlay.Rect) < 2 || overlay.Text == "" {
				continue
			}
			fontSize := overlay.FontSize
			if fontSize <= 0 {
				fontSize = 12
			}
			x := int(math.Round((overlay.Rect[0] - cropBox[0]) * scale))
			y := int(math.Round((cropBox[3] - overlay.Rect[1] - fontSize) * scale))
			drawScaledBasicTextWithColor(dst, x, y, overlay.Text, fontSize*scale, parseOverlayColor(overlay.Color))
		}
		return dst
	}
*/
func drawScaledBasicText(dst draw.Image, x, y int, value string, targetFontSize float64) {
	drawScaledBasicTextWithColor(dst, x, y, value, targetFontSize, color.Black)
}

func drawScaledBasicTextWithColor(dst draw.Image, x, y int, value string, targetFontSize float64, textColor color.Color) {
	if dst == nil || value == "" || targetFontSize <= 0 {
		return
	}
	scaled := renderBasicTextColor(value, targetFontSize, textColor)
	if scaled == nil {
		return
	}
	draw.Draw(dst, image.Rect(x, y, x+scaled.Bounds().Dx(), y+scaled.Bounds().Dy()), scaled, scaled.Bounds().Min, draw.Over)
}

func renderBasicText(value string, targetFontSize float64) *image.RGBA {
	return renderBasicTextColor(value, targetFontSize, color.Black)
}

func renderBasicTextColor(value string, targetFontSize float64, textColor color.Color) *image.RGBA {
	if value == "" || targetFontSize <= 0 {
		return nil
	}
	face := basicfont.Face7x13
	width := font.MeasureString(face, value).Ceil() + 2
	natural := image.NewRGBA(image.Rect(0, 0, width, 15))
	drawer := font.Drawer{Dst: natural, Src: image.NewUniform(textColor), Face: face, Dot: fixed.P(1, 12)}
	drawer.DrawString(value)

	factor := targetFontSize / 13.0
	targetWidth := maxInt(1, int(math.Round(float64(natural.Bounds().Dx())*factor)))
	targetHeight := maxInt(1, int(math.Round(float64(natural.Bounds().Dy())*factor)))
	scaled := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	xdraw.NearestNeighbor.Scale(scaled, scaled.Bounds(), natural, natural.Bounds(), xdraw.Src, nil)
	return scaled
}

func parseOverlayColor(value string) color.Color {
	if len(value) != 7 || value[0] != '#' {
		return color.Black
	}
	parse := func(pair string) (uint8, bool) {
		value, err := strconv.ParseUint(pair, 16, 8)
		return uint8(value), err == nil
	}
	r, rok := parse(value[1:3])
	g, gok := parse(value[3:5])
	b, bok := parse(value[5:7])
	if !rok || !gok || !bok {
		return color.Black
	}
	return color.RGBA{R: r, G: g, B: b, A: 255}
}
