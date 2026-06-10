package optimize

import (
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

type Service interface {
	OptimizePDF(inputPath string) (string, error)
}

type optimizeService struct{}

func NewService() Service {
	return &optimizeService{}
}

func (s *optimizeService) OptimizePDF(inputPath string) (string, error) {
	tempDir := os.TempDir()
	outputFile := "compressed-" + uuid.New().String() + ".pdf"
	outputPath := filepath.Join(tempDir, outputFile)

	config := model.NewDefaultConfiguration()

	config.Optimize = true

	err := api.OptimizeFile(inputPath, outputPath, config)
	if err != nil {
		return "", err
	}

	return outputPath, nil
}
