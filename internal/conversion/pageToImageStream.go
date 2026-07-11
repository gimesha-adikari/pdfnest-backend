package conversion

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"io"
	"mime/multipart"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

func (s *ConversionService) ConvertPageToImageStream(fileHeader *multipart.FileHeader, pageNum int, scale float64) ([]byte, error) {
	src, err := fileHeader.Open()
	if err != nil {
		return nil, fmt.Errorf("failed to read uploaded file payload stream: %v", err)
	}
	defer src.Close()

	tempDir := os.TempDir()

	ext := strings.ToLower(filepath.Ext(fileHeader.Filename))
	if ext == "" {
		ext = ".pdf"
	}

	tempFileName := fmt.Sprintf("pdfnest-preview-%s%s", uuid.New().String(), ext)
	tempFilePath := filepath.Join(tempDir, tempFileName)

	dst, err := os.Create(tempFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to allocate disk memory space for temporary vector context: %v", err)
	}
	defer os.Remove(tempFilePath)

	if _, err = io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return nil, fmt.Errorf("disk write failure on payload compilation pass: %v", err)
	}
	_ = dst.Close()

	targetPdfPath := tempFilePath

	if ext != ".pdf" {
		compiledPdfPath, err := s.OfficeToPdf(tempFilePath)
		if err != nil {
			return nil, fmt.Errorf("failed to compile office document for preview generation: %v", err)
		}
		targetPdfPath = compiledPdfPath
		defer os.Remove(targetPdfPath)
	}

	renderScript := filepath.Join(".", "scripts", "pdf_render_page.py")

	request := map[string]any{
		"documentPath": targetPdfPath,
		"page":         pageNum,
		"dpi":          72.0 * scale,
	}

	reqBytes, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to encode render request: %v", err)
	}

	pythonExec := filepath.Join(".", "venv", "bin", "python")
	cmd := exec.Command(pythonExec, renderScript)
	cmd.Stdin = bytes.NewReader(reqBytes)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("page render failed: %v; output: %s", err, strings.TrimSpace(string(output)))
	}

	var resp struct {
		Success   bool   `json:"success"`
		Error     string `json:"error"`
		ImagePath string `json:"imagePath"`
	}

	if err := json.Unmarshal(output, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse page render output: %v; raw: %s", err, strings.TrimSpace(string(output)))
	}

	if !resp.Success {
		if resp.Error == "" {
			resp.Error = "unknown render error"
		}
		return nil, fmt.Errorf("page render failed: %s", resp.Error)
	}

	imgBytes, err := os.ReadFile(resp.ImagePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read rendered image file: %v", err)
	}
	defer os.Remove(resp.ImagePath)

	_, err = jpeg.Decode(bytes.NewReader(imgBytes))
	if err != nil {
		return nil, fmt.Errorf("rendered image is not a valid jpeg: %v", err)
	}

	return imgBytes, nil
}
