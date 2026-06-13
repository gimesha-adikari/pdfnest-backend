package conversion

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

func (s *ConversionService) OfficeToPdf(inputPath string) (string, error) {
	tempDir := os.TempDir()
	sessionID := uuid.New().String()
	workDir := filepath.Join(tempDir, "office-conv-"+sessionID)

	if err := os.MkdirAll(workDir, 0755); err != nil {
		return "", fmt.Errorf("failed to build office sandbox directory: %w", err)
	}
	defer os.RemoveAll(workDir) // cleanup our work folder sandbox

	cmd := exec.Command("libreoffice",
		"-env:UserInstallation=file://"+filepath.ToSlash(filepath.Join(workDir, "profile")),
		"--headless",
		"--convert-to", "pdf:writer_pdf_Export",
		"--outdir", workDir,
		inputPath,
	)

	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("libreoffice conversion engine failed: %v, trace: %s", err, string(output))
	}

	baseName := filepath.Base(inputPath)
	ext := filepath.Ext(baseName)
	pdfName := strings.TrimSuffix(baseName, ext) + ".pdf"
	generatedPdfPath := filepath.Join(workDir, pdfName)

	finalPdfPath := filepath.Join(tempDir, "office-compiled-"+sessionID+".pdf")

	if err := moveFile(generatedPdfPath, finalPdfPath); err != nil {
		return "", fmt.Errorf("failed to lock conversion stream into staging workspace: %w", err)
	}

	return finalPdfPath, nil
}

func moveFile(src, dst string) error {
	inputFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer inputFile.Close()

	outputFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer outputFile.Close()

	_, err = io.Copy(outputFile, inputFile)
	if err != nil {
		return err
	}

	return os.Remove(src)
}
