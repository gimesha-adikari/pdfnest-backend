package structure

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

const pdfMetadataScript = "scripts/pdf_metadata.py"

func runMetadataScript(mode, inputPath, outputPath, password string, metadata map[string]string) ([]byte, error) {
	args := []string{pdfMetadataScript, mode, inputPath}

	if mode == "write" {
		args = append(args, outputPath)

		payload := map[string]string{
			"title":    strings.TrimSpace(metadata["Title"]),
			"author":   strings.TrimSpace(metadata["Author"]),
			"subject":  strings.TrimSpace(metadata["Subject"]),
			"keywords": strings.TrimSpace(metadata["Keywords"]),
		}

		metaJSON, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}

		args = append(args, "--metadata-json", string(metaJSON))
	}

	if password != "" {
		args = append(args, "--password", password)
	}

	venvPython := filepath.Join("venv", "bin", "python3")
	cmd := exec.Command(venvPython, args...)
	cmd.Env = os.Environ()

	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}

	return out, nil
}

func (s *structureService) UpdateMetadataPDF(inputPath string, metadata map[string]string, password string) (string, error) {
	tempDir := os.TempDir()
	outputFile := "metadata-" + uuid.New().String() + ".pdf"
	outputPath := filepath.Join(tempDir, outputFile)

	if _, err := runMetadataScript("write", inputPath, outputPath, password, metadata); err != nil {
		return "", fmt.Errorf("python metadata writer failed: %w", err)
	}

	return outputPath, nil
}

func (s *structureService) GetMetadataPDF(inputPath string, password string) (map[string]string, error) {
	out, err := runMetadataScript("read", inputPath, "", password, nil)
	if err != nil {
		return nil, fmt.Errorf("python metadata reader failed: %w", err)
	}

	result := map[string]string{
		"title":    "",
		"author":   "",
		"subject":  "",
		"keywords": "",
	}

	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("invalid metadata response: %w", err)
	}

	return result, nil
}
