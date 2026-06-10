package security

import (
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func (s *securityService) EncryptPDF(inputPath string, password string) (string, error) {
	tempDir := os.TempDir()
	outputFile := "locked-" + uuid.New().String() + ".pdf"
	outputPath := filepath.Join(tempDir, outputFile)

	config := model.NewAESConfiguration(password, password, 128)

	err := api.EncryptFile(inputPath, outputPath, config)
	if err != nil {
		return "", err
	}

	return outputPath, nil
}
