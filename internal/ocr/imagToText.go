package ocr

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/jung-kurt/gofpdf"
)

func (s *ocrService) ImageToTextPDF(imagePaths []string) (string, error) {
	tempDir := os.TempDir()
	outputFile := "ocr-compiled-" + uuid.New().String() + ".pdf"
	outputPath := filepath.Join(tempDir, outputFile)

	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetFont("Helvetica", "", 12)

	margin := 10.0
	pageWidth, _ := pdf.GetPageSize()
	writeWidth := pageWidth - (margin * 2)

	for _, imgPath := range imagePaths {
		pdf.AddPage()

		cmd := exec.Command("tesseract", imgPath, "stdout")
		var outBuffer bytes.Buffer
		cmd.Stdout = &outBuffer

		if err := cmd.Run(); err != nil {
			pdf.ImageOptions(imgPath, margin, margin, writeWidth, 0, false, gofpdf.ImageOptions{}, 0, "")
			continue
		}

		extractedText := outBuffer.String()

		if len(strings.TrimSpace(extractedText)) > 0 {
			lines := strings.Split(extractedText, "\n")
			for _, line := range lines {
				pdf.MultiCell(writeWidth, 6, line, "", "L", false)
			}
		} else {
			pdf.ImageOptions(imgPath, margin, margin, writeWidth, 0, false, gofpdf.ImageOptions{}, 0, "")
		}
	}

	err := pdf.OutputFileAndClose(outputPath)
	if err != nil {
		return "", err
	}

	return outputPath, nil
}
