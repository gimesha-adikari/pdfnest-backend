package security

import (
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func (s *securityService) DecryptPDF(inputPath string, password string) (string, error) {
	tempDir := os.TempDir()
	outputFile := "unlocked-" + uuid.New().String() + ".pdf"
	outputPath := filepath.Join(tempDir, outputFile)

	config := model.NewAESConfiguration(password, password, 128)

	err := api.DecryptFile(inputPath, outputPath, config)
	if err != nil {
		return "", err
	}

	return outputPath, nil
}
