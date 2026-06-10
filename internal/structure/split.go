package structure

import (
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func (s *structureService) SplitPDF(inputPath string, pageSelection []string) (string, error) {
	tempDir := os.TempDir()
	outputFile := "split-" + uuid.New().String() + ".pdf"
	outputPath := filepath.Join(tempDir, outputFile)

	config := model.NewDefaultConfiguration()

	err := api.TrimFile(inputPath, outputPath, pageSelection, config)
	if err != nil {
		return "", err
	}

	return outputPath, nil
}
