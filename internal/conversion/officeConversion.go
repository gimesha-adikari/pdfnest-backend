package conversion

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

func ProcessOfficeConversion(format, inputPath, outputPath string) error {
	scriptPath := "./scripts/office_converter.py"

	pythonExecutable := "./venv/bin/python"

	cmd := exec.Command(pythonExecutable, scriptPath, format, inputPath, outputPath)

	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("python script failed: %s | %s", err.Error(), stderr.String())
	}

	if !strings.Contains(out.String(), "SUCCESS") {
		return errors.New("conversion process did not report success: " + out.String())
	}

	return nil
}
