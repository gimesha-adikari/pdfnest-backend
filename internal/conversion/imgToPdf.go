package conversion

import (
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jung-kurt/gofpdf"
	_ "golang.org/x/image/webp"
)

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

		processedPath := imgPath
		lowerPath := strings.ToLower(imgPath)

		if strings.HasSuffix(lowerPath, ".webp") {
			standardizedPath, err := convertToCompatibleJPEG(imgPath, tempDir)
			if err != nil {
				return "", fmt.Errorf("failed modern image adaptation step: %w", err)
			}
			processedPath = standardizedPath
			intermediatePaths = append(intermediatePaths, standardizedPath)
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

	sort.Slice(layout, func(i, j int) bool {
		if layout[i].PageIndex != layout[j].PageIndex {
			return layout[i].PageIndex < layout[j].PageIndex
		}
		return layout[i].ZIndex < layout[j].ZIndex
	})

	tempDir := os.TempDir()
	outputFile := "custom-compiled-" + uuid.New().String() + ".pdf"
	outputPath := filepath.Join(tempDir, outputFile)

	pdf := gofpdf.New("P", "mm", "A4", "")

	const scaleRatio = 210.0 / 350.0
	currentPageIndex := -1

	for _, item := range layout {
		if item.FileIndex >= len(imagePaths) {
			continue
		}

		targetImgPath := imagePaths[item.FileIndex]

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
