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

	err = api.AddWatermarksFile(inputPath, outputPath, nil, wm, config)
	if err != nil {
		return "", err
	}

	return outputPath, nil
}
