package conversion

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/google/uuid"
)

func (s *ConversionService) PdfToImagesBackend(inputPath string) (string, error) {
	tempDir := os.TempDir()
	sessionID := uuid.New().String()

	workDir := filepath.Join(tempDir, "pdf-raster-"+sessionID)
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return "", fmt.Errorf("failed to build internal sandbox workspace directory: %w", err)
	}

	defer os.RemoveAll(workDir)

	outputZipPath := filepath.Join(tempDir, "extracted-"+sessionID+".zip")

	cmd := exec.Command("gs",
		"-dNOPAUSE",
		"-dBATCH",
		"-dSAFER",
		"-sDEVICE=jpeg",
		"-r200",
		"-sOutputFile="+filepath.Join(workDir, "page-%03d.jpg"),
		inputPath,
	)

	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ghostscript rendering engine failed: %v, trace: %s", err, string(output))
	}

	dirEntries, err := os.ReadDir(workDir)
	if err != nil {
		return "", fmt.Errorf("failed scanning internal sandbox raster directory: %w", err)
	}

	if len(dirEntries) == 0 {
		return "", fmt.Errorf("could not extract pages from document container (empty or corrupt canvas layer)")
	}

	var fileNames []string
	for _, entry := range dirEntries {
		if !entry.IsDir() {
			fileNames = append(fileNames, entry.Name())
		}
	}
	sort.Strings(fileNames)

	zipFile, err := os.Create(outputZipPath)
	if err != nil {
		return "", fmt.Errorf("failed to create baseline platform zip wrapper: %w", err)
	}

	var zipClosed bool
	defer func() {
		if !zipClosed {
			zipFile.Close()
		}
	}()

	archive := zip.NewWriter(zipFile)

	for _, name := range fileNames {
		filePath := filepath.Join(workDir, name)

		if err := appendFileToZip(archive, filePath, name); err != nil {
			archive.Close()
			return "", err
		}
	}

	if err := archive.Close(); err != nil {
		return "", fmt.Errorf("failed finalizing operational target archive wrapper: %w", err)
	}

	zipClosed = true
	if err := zipFile.Close(); err != nil {
		return "", fmt.Errorf("failed locking underlying target file descriptor handles: %w", err)
	}

	return outputZipPath, nil
}

func appendFileToZip(archive *zip.Writer, srcPath, internalName string) error {
	fileToZip, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("failed opening intermediate target frame: %w", err)
	}
	defer fileToZip.Close()

	writer, err := archive.Create(internalName)
	if err != nil {
		return fmt.Errorf("failed initializing inner archive index segment: %w", err)
	}

	if _, err := io.Copy(writer, fileToZip); err != nil {
		return fmt.Errorf("failed copying block segments inside zip compression layout: %w", err)
	}

	return nil
}
