package structure

import (
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func (s *structureService) UpdateMetadataPDF(inputPath string, metadata map[string]string) (string, error) {
	tempDir := os.TempDir()
	outputFile := "metadata-" + uuid.New().String() + ".pdf"
	outputPath := filepath.Join(tempDir, outputFile)

	config := model.NewDefaultConfiguration()

	err := api.AddPropertiesFile(inputPath, outputPath, metadata, config)
	if err != nil {
		return "", err
	}

	return outputPath, nil
}
