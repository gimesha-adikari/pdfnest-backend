package structure

import (
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func (s *structureService) WatermarkPDF(inputPath string, text string, imagePath string, description string) (string, error) {
	return s.WatermarkPDFOnPages(inputPath, text, imagePath, description, nil)
}

// WatermarkPDFOnPages is the reusable pdfcpu leaf used by Studio V2. A nil
// page selection retains the legacy global behavior; an explicit selection
// keeps page-local VDM overlays page-local during finalization.
func (s *structureService) WatermarkPDFOnPages(inputPath string, text string, imagePath string, description string, selectedPages []string) (string, error) {
	tempDir := os.TempDir()
	outputFile := "watermarked-" + uuid.New().String() + ".pdf"
	outputPath := filepath.Join(tempDir, outputFile)
	config := model.NewDefaultConfiguration()

	var wm *model.Watermark
	var err error

	if imagePath != "" {
		wm, err = api.ImageWatermark(imagePath, description, true, false, types.POINTS)
	} else {
		wm, err = api.TextWatermark(text, description, true, false, types.POINTS)
	}

	if err != nil {
		return "", err
	}

	wm.ScaleAbs = true

	err = api.AddWatermarksFile(inputPath, outputPath, selectedPages, wm, config)
	if err != nil {
		return "", err
	}

	return outputPath, nil
}
