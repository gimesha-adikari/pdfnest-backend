// file: /home/gimesha/My_Projects/go/pdfnest-backend/internal/conversion/pdfToImg.go
package conversion

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"
)

type ImageOutputFormat struct {
	Device      string
	FileExt     string
	DisplayName string
}

func resolveImageOutputFormat(input string) ImageOutputFormat {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "jpg", "jpeg", "":
		return ImageOutputFormat{
			Device:      "jpeg",
			FileExt:     "jpg",
			DisplayName: "JPEG",
		}
	case "png":
		return ImageOutputFormat{
			Device:      "png16m",
			FileExt:     "png",
			DisplayName: "PNG",
		}
	case "pnggray", "gray", "grayscale":
		return ImageOutputFormat{
			Device:      "pnggray",
			FileExt:     "png",
			DisplayName: "Grayscale PNG",
		}
	case "pngmono", "mono", "bw", "blackwhite", "black-white":
		return ImageOutputFormat{
			Device:      "pngmono",
			FileExt:     "png",
			DisplayName: "Monochrome PNG",
		}
	default:
		// Safe fallback
		return ImageOutputFormat{
			Device:      "jpeg",
			FileExt:     "jpg",
			DisplayName: "JPEG",
		}
	}
}

func (s *ConversionService) PdfToImagesBackend(inputPath string, imageType string) (string, error) {
	tempDir := os.TempDir()
	sessionID := uuid.New().String()

	format := resolveImageOutputFormat(imageType)

	workDir := filepath.Join(tempDir, "pdf-raster-"+sessionID)
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return "", fmt.Errorf("failed to build internal sandbox workspace directory: %w", err)
	}

	defer os.RemoveAll(workDir)

	outputZipPath := filepath.Join(tempDir, "extracted-"+sessionID+".zip")

	outputPattern := filepath.Join(workDir, fmt.Sprintf("page-%%03d.%s", format.FileExt))

	cmd := exec.Command("gs",
		"-dNOPAUSE",
		"-dBATCH",
		"-dSAFER",
		"-sDEVICE="+format.Device,
		"-r200",
		"-sOutputFile="+outputPattern,
		inputPath,
	)

	// Better JPEG quality without affecting PNG modes.
	if format.Device == "jpeg" {
		cmd.Args = append(cmd.Args, "-dJPEGQ=95")
	}

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
			_ = zipFile.Close()
		}
	}()

	archive := zip.NewWriter(zipFile)

	for _, name := range fileNames {
		filePath := filepath.Join(workDir, name)

		if err := appendFileToZip(archive, filePath, name); err != nil {
			_ = archive.Close()
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
