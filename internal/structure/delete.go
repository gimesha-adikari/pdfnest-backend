package structure

import (
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func (s *structureService) DeletePDFPages(inputPath string, pagesToDelete []string) (string, error) {
	tempDir := os.TempDir()
	outputFile := "deleted-" + uuid.New().String() + ".pdf"
	outputPath := filepath.Join(tempDir, outputFile)

	config := model.NewDefaultConfiguration()

	err := api.RemovePagesFile(inputPath, outputPath, pagesToDelete, config)
	if err != nil {
		return "", err
	}

	return outputPath, nil
}
