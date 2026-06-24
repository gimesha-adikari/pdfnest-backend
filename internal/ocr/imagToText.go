package ocr

import (
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jung-kurt/gofpdf"
	_ "golang.org/x/image/webp"
)

func (s *ocrService) ImageToTextPDF(imagePaths []string) (string, error) {
	if len(imagePaths) == 0 {
		return "", errors.New("no images provided")
	}

	tempDir := os.TempDir()

	rawPDF := filepath.Join(
		tempDir,
		"ocr-raw-"+uuid.New().String()+".pdf",
	)

	searchablePDF := filepath.Join(
		tempDir,
		"ocr-searchable-"+uuid.New().String()+".pdf",
	)

	pdf := gofpdf.New("P", "mm", "A4", "")

	var tempFiles []string

	defer func() {
		for _, f := range tempFiles {
			_ = os.Remove(f)
		}
	}()

	for _, imgPath := range imagePaths {

		if _, err := os.Stat(imgPath); err != nil {
			return "", fmt.Errorf("image not found: %s", imgPath)
		}

		processedPath := imgPath

		if strings.HasSuffix(strings.ToLower(imgPath), ".webp") {
			converted, err := normalizeImageToJPEG(imgPath, tempDir)
			if err != nil {
				return "", err
			}

			tempFiles = append(tempFiles, converted)
			processedPath = converted
		}

		imgWidth, imgHeight, err := getImageDimensions(processedPath)
		if err != nil {
			return "", err
		}

		pdf.AddPage()

		pageW, pageH := pdf.GetPageSize()

		scale := min(
			pageW/float64(imgWidth),
			pageH/float64(imgHeight),
		)

		renderW := float64(imgWidth) * scale
		renderH := float64(imgHeight) * scale

		x := (pageW - renderW) / 2
		y := (pageH - renderH) / 2

		pdf.ImageOptions(
			processedPath,
			x,
			y,
			renderW,
			renderH,
			false,
			gofpdf.ImageOptions{
				ReadDpi: true,
			},
			0,
			"",
		)
	}

	if err := pdf.OutputFileAndClose(rawPDF); err != nil {
		return "", err
	}

	cmd := exec.Command(
		"ocrmypdf",
		"--skip-text",
		"--rotate-pages",
		"--deskew",
		"--optimize", "3",
		"--jobs", strconv.Itoa(runtime.NumCPU()),
		rawPDF,
		searchablePDF,
	)

	output, err := cmd.CombinedOutput()

	_ = os.Remove(rawPDF)

	if err != nil {
		_ = os.Remove(searchablePDF)

		return "", fmt.Errorf(
			"ocrmypdf failed: %v\n%s",
			err,
			string(output),
		)
	}

	return searchablePDF, nil
}

func getImageDimensions(path string) (int, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()

	cfg, _, err := image.DecodeConfig(file)
	if err != nil {
		return 0, 0, err
	}

	return cfg.Width, cfg.Height, nil
}

func normalizeImageToJPEG(srcPath, tempDir string) (string, error) {
	in, err := os.Open(srcPath)
	if err != nil {
		return "", err
	}
	defer in.Close()

	img, _, err := image.Decode(in)
	if err != nil {
		return "", err
	}

	outPath := filepath.Join(
		tempDir,
		"ocr-webp-"+uuid.New().String()+".jpg",
	)

	out, err := os.Create(outPath)
	if err != nil {
		return "", err
	}
	defer out.Close()

	err = jpeg.Encode(
		out,
		img,
		&jpeg.Options{
			Quality: 95,
		},
	)

	if err != nil {
		return "", err
	}

	return outPath, nil
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
