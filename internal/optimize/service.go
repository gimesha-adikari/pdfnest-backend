package optimize

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/google/uuid"
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

	cmd := exec.Command("gs",
		"-dNOPAUSE",
		"-dBATCH",
		"-dSAFER",
		"-sDEVICE=pdfwrite",
		"-dCompatibilityLevel=1.4",
		"-dPDFSETTINGS=/ebook",
		"-dColorImageDownsampleType=/Bicubic",
		"-dColorImageResolution=150",
		"-dGrayImageDownsampleType=/Bicubic",
		"-dGrayImageResolution=150",
		"-dMonoImageDownsampleType=/Bicubic",
		"-dMonoImageResolution=150",
		"-sOutputFile="+outputPath,
		inputPath,
	)

	if output, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(outputPath)
		return "", fmt.Errorf("ghostscript compression failure: %v, trace: %s", err, string(output))
	}

	fi, err := os.Stat(outputPath)
	if err != nil || fi.Size() == 0 {
		_ = os.Remove(outputPath)
		return "", fmt.Errorf("compression output file was empty or unreadable")
	}

	return outputPath, nil
}
