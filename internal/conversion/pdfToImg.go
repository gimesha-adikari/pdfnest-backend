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

func (s *imagesService) PdfToImagesBackend(inputPath string) (string, error) {
	tempDir := os.TempDir()

	sessionID := uuid.New().String()
	workDir := filepath.Join(tempDir, "pdf-raster-"+sessionID)
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return "", err
	}
	defer func(path string) {
		err := os.RemoveAll(path)
		if err != nil {
			_ = err
		}
	}(workDir)

	outputZipPath := filepath.Join(tempDir, "extracted-"+sessionID+".zip")

	cmd := exec.Command("gs",
		"-dNOPAUSE",
		"-dBATCH",
		"-sDEVICE=jpeg",
		"-r200",
		"-sOutputFile="+filepath.Join(workDir, "page-%03d.jpg"),
		inputPath,
	)

	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ghostscript rendering engine failed: %v, output: %s", err, string(output))
	}

	dirEntries, err := os.ReadDir(workDir)
	if err != nil {
		return "", err
	}

	if len(dirEntries) == 0 {
		return "", fmt.Errorf("could not extract pages from document container (empty or corrupt)")
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
		return "", err
	}
	defer func(zipFile *os.File) {
		err := zipFile.Close()
		if err != nil {
			_ = err
		}
	}(zipFile)

	archive := zip.NewWriter(zipFile)
	defer func(archive *zip.Writer) {
		err := archive.Close()
		if err != nil {
			_ = err
		}
	}(archive)

	for _, name := range fileNames {
		filePath := filepath.Join(workDir, name)
		fileToZip, err := os.Open(filePath)
		if err != nil {
			return "", err
		}

		writer, err := archive.Create(name)
		if err != nil {
			err := fileToZip.Close()
			if err != nil {
				return "", err
			}
			return "", err
		}

		if _, err := io.Copy(writer, fileToZip); err != nil {
			err := fileToZip.Close()
			if err != nil {
				return "", err
			}
			return "", err
		}
		err = fileToZip.Close()
		if err != nil {
			return "", err
		}
	}

	return outputZipPath, nil
}
