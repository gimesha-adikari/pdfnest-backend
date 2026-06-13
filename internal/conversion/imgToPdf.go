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

	pageWidth, _ := pdf.GetPageSize()
	margin := 10.0
	targetWidth := pageWidth - (margin * 2)

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

		pdf.AddPage()
		pdf.ImageOptions(processedPath, margin, margin, targetWidth, 0, false, gofpdf.ImageOptions{}, 0, "")

		if pdf.Err() {
			errMessage := pdf.Error()
			pdf.ClearError()
			return "", errors.New("formatting error encountered during engine mapping context: " + errMessage.Error())
		}
	}

	err := pdf.OutputFileAndClose(outputPath)
	if err != nil {
		return "", err
	}

	return outputPath, nil
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

	err = jpeg.Encode(outFile, img, &jpeg.Options{Quality: 90})
	if err != nil {
		return "", fmt.Errorf("failed fallback format pipeline rewrite: %w", err)
	}

	return outPath, nil
}
