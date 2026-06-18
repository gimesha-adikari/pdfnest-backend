package optimize

import (
	"fmt"
	"os/exec"
)

func ConvertToGrayscale(inputPath, outputPath string) error {
	cmd := exec.Command("gs",
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
