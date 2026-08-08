package optimize

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

func ConvertToGrayscale(inputPath, outputPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "gs",
		"-sDEVICE=pdfwrite",
		"-sColorConversionStrategy=Gray",
		"-dProcessColorModel=/DeviceGray",
		"-dCompatibilityLevel=1.4",
		"-dNOPAUSE",
		"-dBATCH",
		"-sOutputFile="+outputPath,
		inputPath,
	)

	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ghostscript failed: %v, trace: %s", err, string(output))
	}
	return nil
}
