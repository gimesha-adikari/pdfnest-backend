package optimize

import (
	"context"
	"fmt"
	"pdfnest-backend/internal/process"
	"time"
)

func ConvertToGrayscale(ctx context.Context, inputPath, outputPath string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	runner := process.Runner{GracePeriod: 500 * time.Millisecond}
	output, err := runner.Run(
		ctx,
		10*time.Minute,
		"gs",
		"-sDEVICE=pdfwrite",
		"-sColorConversionStrategy=Gray",
		"-dProcessColorModel=/DeviceGray",
		"-dCompatibilityLevel=1.4",
		"-dNOPAUSE",
		"-dBATCH",
		"-sOutputFile="+outputPath,
		inputPath,
	)

	if err != nil {
		return fmt.Errorf("ghostscript failed: %v, trace: %s", err, string(output))
	}
	return nil
}
