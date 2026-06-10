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
		return "", err
	}
	defer func(path string) {
		err := os.RemoveAll(path)
		if err != nil {
			return
		}
	}(workDir) // Clean up images automatically when finished

	outputTextPath := filepath.Join(tempDir, "extracted-text-"+sessionID+".txt")

	gsCmd := exec.Command("gs",
		"-dNOPAUSE",
		"-dBATCH",
		"-sDEVICE=png16m",
		"-r150",
		"-sOutputFile="+filepath.Join(workDir, "page-%03d.png"),
		inputPath,
	)
	if output, err := gsCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ghostscript rendering failed: %v, output: %s", err, string(output))
	}

	dirEntries, err := os.ReadDir(workDir)
	if err != nil {
		return "", err
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
		return "", err
	}
	defer func(txtFile *os.File) {
		err := txtFile.Close()
		if err != nil {
			return
		}
	}(txtFile)

	for i, name := range fileNames {
		imgPath := filepath.Join(workDir, name)

		tessCmd := exec.Command("tesseract", imgPath, "stdout")
		var outBuffer bytes.Buffer
		tessCmd.Stdout = &outBuffer

		if err := tessCmd.Run(); err != nil {
			return "", fmt.Errorf("tesseract failed on page %d: %v", i+1, err)
		}

		pageHeader := fmt.Sprintf("\n--- PAGE %d ---\n\n", i+1)
		if _, err := txtFile.WriteString(pageHeader); err != nil {
			return "", err
		}

		if _, err := txtFile.Write(outBuffer.Bytes()); err != nil {
			return "", err
		}
	}

	return outputTextPath, nil
}
