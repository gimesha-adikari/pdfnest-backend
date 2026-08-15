package conversion

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jung-kurt/gofpdf"
	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
	_ "golang.org/x/image/webp"
)

const (
	maxSVGRasterDimension = 4096
	defaultSVGDimension   = 800
)

// RasterizeSVG converts an SVG byte slice into a valid PNG image byte slice in memory.
// Enforces security boundaries: rejects external entities (XXE) and caps pathological dimensions.
func RasterizeSVG(svgBytes []byte) ([]byte, int, int, error) {
	if len(svgBytes) == 0 {
		return nil, 0, 0, errors.New("empty SVG payload provided")
	}

	svgStr := string(svgBytes)
	lowerSvg := strings.ToLower(svgStr)
	if strings.Contains(lowerSvg, "<!entity") || (strings.Contains(lowerSvg, "<!doctype") && strings.Contains(lowerSvg, "system")) {
		return nil, 0, 0, errors.New("external entity references (XXE) are strictly forbidden in SVG input")
	}

	icon, err := oksvg.ReadIconStream(bytes.NewReader(svgBytes))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to parse SVG vector stream: %w", err)
	}

	width := int(math.Round(float64(icon.ViewBox.W)))
	height := int(math.Round(float64(icon.ViewBox.H)))

	if width <= 0 || height <= 0 {
		width = defaultSVGDimension
		height = defaultSVGDimension
	}

	// Enforce safe memory raster limits
	if width > maxSVGRasterDimension || height > maxSVGRasterDimension {
		aspect := float64(width) / float64(height)
		if width > height {
			width = maxSVGRasterDimension
			height = int(math.Round(float64(maxSVGRasterDimension) / aspect))
		} else {
			height = maxSVGRasterDimension
			width = int(math.Round(float64(maxSVGRasterDimension) * aspect))
		}
	}
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}

	icon.SetTarget(0, 0, float64(width), float64(height))
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	dasher := rasterx.NewDasher(width, height, rasterx.NewScannerGV(width, height, img, img.Bounds()))
	icon.Draw(dasher, 1.0)

	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		return nil, 0, 0, fmt.Errorf("failed to encode rasterized SVG into PNG: %w", err)
	}

	return pngBuf.Bytes(), width, height, nil
}

func isSVG(path string) bool {
	if strings.HasSuffix(strings.ToLower(path), ".svg") {
		return true
	}

	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil || n == 0 {
		return false
	}

	content := strings.ToLower(string(buf[:n]))
	return strings.Contains(content, "<svg") || strings.Contains(content, "<?xml") && strings.Contains(content, "svg")
}

func convertSVGToPNG(srcPath, tempDir string) (string, error) {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return "", fmt.Errorf("failed reading source SVG: %w", err)
	}

	pngBytes, _, _, err := RasterizeSVG(data)
	if err != nil {
		return "", err
	}

	outPath := filepath.Join(tempDir, "svg-rasterized-"+uuid.New().String()+".png")
	if err := os.WriteFile(outPath, pngBytes, 0600); err != nil {
		return "", fmt.Errorf("failed writing rasterized SVG PNG: %w", err)
	}

	return outPath, nil
}

func standardizeImagePath(srcPath, tempDir string) (string, bool, error) {
	lowerPath := strings.ToLower(srcPath)

	if strings.HasSuffix(lowerPath, ".webp") {
		standardized, err := convertToCompatibleJPEG(srcPath, tempDir)
		if err != nil {
			return "", false, fmt.Errorf("failed modern image adaptation step: %w", err)
		}
		return standardized, true, nil
	}

	if isSVG(srcPath) {
		standardized, err := convertSVGToPNG(srcPath, tempDir)
		if err != nil {
			return "", false, fmt.Errorf("failed SVG vector adaptation step: %w", err)
		}
		return standardized, true, nil
	}

	return srcPath, false, nil
}

func (s *ConversionService) ImagesToPDF(imagePaths []string) (string, error) {
	if len(imagePaths) == 0 {
		return "", errors.New("empty file buffer set provided for PDF conversion pipeline")
	}

	tempDir := os.TempDir()
	outputFile := "images-compiled-" + uuid.New().String() + ".pdf"
	outputPath := filepath.Join(tempDir, outputFile)

	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(0, 0, 0)
	pdf.SetAutoPageBreak(false, 0)

	pageW, pageH := pdf.GetPageSize()

	var intermediatePaths []string
	defer func() {
		for _, path := range intermediatePaths {
			_ = os.Remove(path)
		}
	}()

	for _, imgPath := range imagePaths {
		if _, err := os.Stat(imgPath); os.IsNotExist(err) {
			return "", errors.New("underlying structural file chunk was dropped during allocation sequence")
		}

		processedPath, isIntermediate, err := standardizeImagePath(imgPath, tempDir)
		if err != nil {
			return "", err
		}
		if isIntermediate {
			intermediatePaths = append(intermediatePaths, processedPath)
		}

		imgW, imgH, err := getImageSizeMM(processedPath)
		if err != nil {
			return "", err
		}

		// Scale to fit inside the page while preserving aspect ratio.
		// This ensures one dimension always touches the page edge.
		scale := minFloat(pageW/imgW, pageH/imgH)
		drawW := imgW * scale
		drawH := imgH * scale
		posX := (pageW - drawW) / 2
		posY := (pageH - drawH) / 2

		pdf.AddPage()
		pdf.ImageOptions(
			processedPath,
			posX,
			posY,
			drawW,
			drawH,
			false,
			gofpdf.ImageOptions{},
			0,
			"",
		)

		if pdf.Err() {
			errMessage := pdf.Error()
			pdf.ClearError()
			return "", errors.New("formatting error encountered during engine mapping context: " + errMessage.Error())
		}
	}

	if err := pdf.OutputFileAndClose(outputPath); err != nil {
		return "", err
	}

	return outputPath, nil
}

func getImageSizeMM(path string) (float64, float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to read image dimensions: %w", err)
	}

	if cfg.Width <= 0 || cfg.Height <= 0 {
		return 0, 0, errors.New("invalid image dimensions")
	}

	return float64(cfg.Width), float64(cfg.Height), nil
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func convertToCompatibleJPEG(srcPath, tempDir string) (string, error) {
	file, err := os.Open(srcPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		return "", fmt.Errorf("failed parsing image stream headers: %w", err)
	}

	outPath := filepath.Join(tempDir, "adapted-frame-"+uuid.New().String()+".jpg")
	outFile, err := os.Create(outPath)
	if err != nil {
		return "", err
	}
	defer outFile.Close()

	if err := jpeg.Encode(outFile, img, &jpeg.Options{Quality: 90}); err != nil {
		return "", fmt.Errorf("failed fallback format pipeline rewrite: %w", err)
	}

	return outPath, nil
}

func (s *ConversionService) CustomImagesToPDF(imagePaths []string, layout []CanvasLayoutItem) (string, error) {
	if len(imagePaths) == 0 {
		return "", errors.New("empty source file matrix provided")
	}

	tempDir := os.TempDir()
	outputFile := "custom-compiled-" + uuid.New().String() + ".pdf"
	outputPath := filepath.Join(tempDir, outputFile)

	var intermediatePaths []string
	defer func() {
		for _, path := range intermediatePaths {
			_ = os.Remove(path)
		}
	}()

	standardizedImagePaths := make([]string, len(imagePaths))
	for i, imgPath := range imagePaths {
		processedPath, isIntermediate, err := standardizeImagePath(imgPath, tempDir)
		if err != nil {
			return "", err
		}
		if isIntermediate {
			intermediatePaths = append(intermediatePaths, processedPath)
		}
		standardizedImagePaths[i] = processedPath
	}

	sort.Slice(layout, func(i, j int) bool {
		if layout[i].PageIndex != layout[j].PageIndex {
			return layout[i].PageIndex < layout[j].PageIndex
		}
		return layout[i].ZIndex < layout[j].ZIndex
	})

	pdf := gofpdf.New("P", "mm", "A4", "")

	const scaleRatio = 210.0 / 350.0
	currentPageIndex := -1

	for _, item := range layout {
		if item.FileIndex >= len(standardizedImagePaths) {
			continue
		}

		targetImgPath := standardizedImagePaths[item.FileIndex]

		for currentPageIndex < item.PageIndex {
			pdf.AddPage()
			currentPageIndex++
		}

		if item.BorderWidth > 0 {
			var r, g, b int64
			if len(item.BorderColor) == 7 && item.BorderColor[0] == '#' {
				r, _ = strconv.ParseInt(item.BorderColor[1:3], 16, 64)
				g, _ = strconv.ParseInt(item.BorderColor[3:5], 16, 64)
				b, _ = strconv.ParseInt(item.BorderColor[5:7], 16, 64)
			}
			pdf.SetDrawColor(int(r), int(g), int(b))
			pdf.SetLineWidth(item.BorderWidth * scaleRatio)
			pdf.Rect(item.X*scaleRatio, item.Y*scaleRatio, item.Width*scaleRatio, item.Height*scaleRatio, "D")
		}

		pdf.ImageOptions(
			targetImgPath,
			item.X*scaleRatio,
			item.Y*scaleRatio,
			item.Width*scaleRatio,
			item.Height*scaleRatio,
			false,
			gofpdf.ImageOptions{},
			0,
			"",
		)

		if pdf.Err() {
			return "", fmt.Errorf("vector translation engine crash: %v", pdf.Error())
		}
	}

	if currentPageIndex == -1 {
		pdf.AddPage()
	}

	err := pdf.OutputFileAndClose(outputPath)
	if err != nil {
		return "", err
	}

	return outputPath, nil
}
