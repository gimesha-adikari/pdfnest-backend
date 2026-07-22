package structure

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func (s *structureService) CropPDF(inputPath string, cropBoxDesc string, selectedPages []string) (string, error) {
	tempDir := os.TempDir()
	outputFile := "cropped-" + uuid.New().String() + ".pdf"
	outputPath := filepath.Join(tempDir, outputFile)

	config := model.NewDefaultConfiguration()

	box, err := model.ParseBox(cropBoxDesc, types.POINTS)
	if err != nil {
		return "", fmt.Errorf("failed to parse crop box geometry parameters: %w", err)
	}

	if err := api.CropFile(inputPath, outputPath, selectedPages, box, config); err != nil {
		return "", err
	}

	return outputPath, nil
}
