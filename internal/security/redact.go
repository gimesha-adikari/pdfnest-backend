package security

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// RedactPageText executes true secure binary redaction by delegating coordinate
// extraction, keyword matching, and pixel box scrubbing to the local PyMuPDF virtual environment.
func (s *securityService) RedactPageText(inputPath string, outputPath string, keywords []string, boxesStr string) (string, error) {
	outFileName := fmt.Sprintf("redacted_%s.pdf", uuid.New().String())
	finalOutPath := filepath.Join(outputPath, outFileName)

	// Join keywords with our custom multi-word safe character delimiter
	keywordsStr := strings.Join(keywords, "|||")

	// Fallback empty array literal safeguard to prevent empty string command arguments breaking python sys indexes
	if boxesStr == "" {
		boxesStr = "[]"
	}

	// Reference local isolated python execution binary assets natively
	pythonExecutable := filepath.Join(".", "venv", "bin", "python")
	scriptPath := filepath.Join("scripts", "redact.py")

	// Execute execution routine passing: (inputFile, outputFile, keywordsString, canvasBoxesJSONString)
	cmd := exec.Command(pythonExecutable, scriptPath, inputPath, finalOutPath, keywordsStr, boxesStr)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("secure redaction engine failed: %v | Details: %s", err, string(output))
	}

	// Verify target out file block exists
	if _, err := os.Stat(finalOutPath); os.IsNotExist(err) {
		return "", errors.New("redaction script completed but output file is missing")
	}

	return outFileName, nil
}
