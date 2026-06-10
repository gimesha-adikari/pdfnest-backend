package conversion

import (
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/jung-kurt/gofpdf"
)

func (s *imagesService) ImagesToPDF(imagePaths []string) (string, error) {
	tempDir := os.TempDir()
	outputFile := "images-compiled-" + uuid.New().String() + ".pdf"
	outputPath := filepath.Join(tempDir, outputFile)

	pdf := gofpdf.New("P", "mm", "A4", "")

	pageWidth, _ := pdf.GetPageSize()
	margin := 10.0
	targetWidth := pageWidth - (margin * 2)

	for _, imgPath := range imagePaths {
		pdf.AddPage()

		pdf.ImageOptions(imgPath, margin, margin, targetWidth, 0, false, gofpdf.ImageOptions{}, 0, "")

		if pdf.Err() {
			pdf.ClearError()
		}
	}

	err := pdf.OutputFileAndClose(outputPath)
	if err != nil {
		return "", err
	}

	return outputPath, nil
}
