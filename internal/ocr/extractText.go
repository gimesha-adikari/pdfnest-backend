package ocr

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/google/uuid"
)

func (s *ocrService) ExtractTextFromPDF(inputPath string) (string, error) {
	tempDir := os.TempDir()
	sessionID := uuid.New().String()

	workDir := filepath.Join(tempDir, "ocr-workspace-"+sessionID)
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create sandbox workspace tracking scope: %w", err)
	}
	defer os.RemoveAll(workDir)

	outputTextPath := filepath.Join(tempDir, "extracted-text-"+sessionID+".txt")

	gsCmd := exec.Command("gs",
		"-dNOPAUSE",
		"-dBATCH",
		"-dSAFER",
		"-sDEVICE=pnggray",
		"-r300",
		"-sOutputFile="+filepath.Join(workDir, "page-%03d.png"),
		inputPath,
	)
	if output, err := gsCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ghostscript raster engine failed: %v, trace: %s", err, string(output))
	}

	dirEntries, err := os.ReadDir(workDir)
	if err != nil {
		return "", fmt.Errorf("failed checking working frame index: %w", err)
	}

	var fileNames []string
	for _, entry := range dirEntries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".png" {
			fileNames = append(fileNames, entry.Name())
		}
	}
	sort.Strings(fileNames)

	txtFile, err := os.Create(outputTextPath)
	if err != nil {
		return "", fmt.Errorf("failed creating output plain text sink: %w", err)
	}

	var fileClosed bool
	defer func() {
		if !fileClosed {
			txtFile.Close()
		}
	}()

	for i, name := range fileNames {
		imgPath := filepath.Join(workDir, name)

		tessCmd := exec.Command("tesseract",
			imgPath,
			"stdout",
			"--oem", "1",
			"--psm", "1",
		)
		var outBuffer bytes.Buffer
		var errBuffer bytes.Buffer
		tessCmd.Stdout = &outBuffer
		tessCmd.Stderr = &errBuffer

		if err := tessCmd.Run(); err != nil {
			continue
		}

		pageHeader := fmt.Sprintf("--- START OF PAGE %d ---\n", i+1)
		if _, err := txtFile.WriteString(pageHeader); err != nil {
			return "", fmt.Errorf("failed writing page structure header: %w", err)
		}

		if _, err := txtFile.Write(outBuffer.Bytes()); err != nil {
			return "", fmt.Errorf("failed committing text streams down file systems: %w", err)
		}

		_, _ = txtFile.WriteString("\n--- END OF PAGE ---\n\n")
	}

	if err := txtFile.Close(); err != nil {
		return "", fmt.Errorf("failed finalizing file modifications: %w", err)
	}
	fileClosed = true

	return outputTextPath, nil
}
