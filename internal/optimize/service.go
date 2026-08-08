package optimize

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"pdfnest-backend/internal/process"
	"time"

	"github.com/google/uuid"
)

type Service interface {
	OptimizePDF(ctx context.Context, inputPath string) (string, error)
}

type optimizeService struct{}

func NewService() Service {
	return &optimizeService{}
}

func (s *optimizeService) OptimizePDF(ctx context.Context, inputPath string) (string, error) {
	tempDir := os.TempDir()
	outputFile := "compressed-" + uuid.New().String() + ".pdf"
	outputPath := filepath.Join(tempDir, outputFile)

	if ctx == nil {
		ctx = context.Background()
	}

	runner := process.Runner{GracePeriod: 500 * time.Millisecond}
	output, err := runner.Run(
		ctx,
		10*time.Minute,
		"gs",
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

	if err != nil {
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
