package ocr

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/jung-kurt/gofpdf"
	_ "golang.org/x/image/webp"
)

func (s *ocrService) ImageToTextPDF(imagePaths []string) (string, error) {
	if len(imagePaths) == 0 {
		return "", errors.New("empty dataset sequence provided for OCR pipeline handling")
	}

	tempDir := os.TempDir()
	outputFile := "ocr-compiled-" + uuid.New().String() + ".pdf"
	outputPath := filepath.Join(tempDir, outputFile)

	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetFont("Helvetica", "", 12)

	margin := 10.0
	pageWidth, _ := pdf.GetPageSize()
	writeWidth := pageWidth - (margin * 2)

	var intermediatePaths []string
	defer func() {
		for _, path := range intermediatePaths {
			_ = os.Remove(path)
		}
	}()

	for _, imgPath := range imagePaths {
		if _, err := os.Stat(imgPath); os.IsNotExist(err) {
			return "", errors.New("underlying image segment missing during text conversion pipeline")
		}

		processedPath := imgPath
		lowerPath := strings.ToLower(imgPath)

		if strings.HasSuffix(lowerPath, ".webp") {
			standardizedPath, err := normalizeImageToJPEG(imgPath, tempDir)
			if err != nil {
				return "", fmt.Errorf("failed normalizing frame array context: %w", err)
			}
			processedPath = standardizedPath
			intermediatePaths = append(intermediatePaths, standardizedPath)
		}

		pdf.AddPage()

		cmd := exec.Command("tesseract",
			processedPath,
			"stdout",
			"--oem", "1",
			"--psm", "1",
		)
		var outBuffer bytes.Buffer
		cmd.Stdout = &outBuffer

		if err := cmd.Run(); err != nil {
			pdf.ImageOptions(processedPath, margin, margin, writeWidth, 0, false, gofpdf.ImageOptions{}, 0, "")
			continue
		}

		extractedText := outBuffer.String()

		if len(strings.TrimSpace(extractedText)) > 0 {
			lines := strings.Split(extractedText, "\n")
			for _, line := range lines {
				pdf.MultiCell(writeWidth, 6, line, "", "L", false)
			}
		} else {
			pdf.ImageOptions(processedPath, margin, margin, writeWidth, 0, false, gofpdf.ImageOptions{}, 0, "")
		}

		if pdf.Err() {
			errMessage := pdf.Error()
			pdf.ClearError()
			return "", fmt.Errorf("pdf generation fault during textual injection context: %w", errMessage)
		}
	}

	err := pdf.OutputFileAndClose(outputPath)
	if err != nil {
		return "", err
	}

	return outputPath, nil
}

func normalizeImageToJPEG(srcPath, tempDir string) (string, error) {
	file, err := os.Open(srcPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		return "", err
	}

	outPath := filepath.Join(tempDir, "ocr-adapted-"+uuid.New().String()+".jpg")
	outFile, err := os.Create(outPath)
	if err != nil {
		return "", err
	}
	defer outFile.Close()

	err = jpeg.Encode(outFile, img, &jpeg.Options{Quality: 98})
	if err != nil {
		return "", err
	}

	return outPath, nil
}
