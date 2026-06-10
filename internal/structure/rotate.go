package structure

import (
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func (s *structureService) RotatePDF(inputPath string, rotations map[string]int) (string, error) {
	config := model.NewDefaultConfiguration()
	currentInput := inputPath

	for pagesStr, degrees := range rotations {
		tempDir := os.TempDir()
		outputFile := "rotate-" + uuid.New().String() + ".pdf"
		outputPath := filepath.Join(tempDir, outputFile)

		pageSlice := []string{pagesStr}
		err := api.RotateFile(currentInput, outputPath, degrees, pageSlice, config)
		if err != nil {
			if currentInput != inputPath {
				err := os.Remove(currentInput)
				if err != nil {
					return "", err
				}
			}
			return "", err
		}

		if currentInput != inputPath {
			err := os.Remove(currentInput)
			if err != nil {
				return "", err
			}
		}
		currentInput = outputPath
	}

	return currentInput, nil
}
