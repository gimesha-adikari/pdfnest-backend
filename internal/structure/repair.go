package structure

import (
	"fmt"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func RepairPdf(inputPath, outputPath string) error {
	conf := model.NewDefaultConfiguration()
	if err := api.OptimizeFile(inputPath, outputPath, conf); err != nil {
		return fmt.Errorf("file is too severely corrupted to repair: %w", err)
	}
	return nil
}
