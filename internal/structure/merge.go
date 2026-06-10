package structure

import (
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func (s *structureService) MergePDFs(inputPaths []string) (string, error) {
	tempDir := os.TempDir()
	outputFile := "merged-" + uuid.New().String() + ".pdf"
	outputPath := filepath.Join(tempDir, outputFile)

	config := model.NewDefaultConfiguration()

	err := api.MergeCreateFile(inputPaths, outputPath, false, config)
	if err != nil {
		return "", err
	}

	return outputPath, nil
}
