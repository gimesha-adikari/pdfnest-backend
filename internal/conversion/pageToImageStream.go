package conversion

import (
	"bytes"
	"fmt"
	"image/jpeg"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"

	"github.com/gen2brain/go-fitz"
	"github.com/google/uuid"
)

func (s *ConversionService) ConvertPageToImageStream(fileHeader *multipart.FileHeader, pageNum int, scale float64) ([]byte, error) {
	src, err := fileHeader.Open()
	if err != nil {
		return nil, fmt.Errorf("failed to read uploaded file payload stream: %v", err)
	}
	defer src.Close()

	tempDir := os.TempDir()
	tempFileName := fmt.Sprintf("pdfnest-preview-%s.pdf", uuid.New().String())
	tempFilePath := filepath.Join(tempDir, tempFileName)

	dst, err := os.Create(tempFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to allocate disk memory space for temporary vector context: %v", err)
	}
	defer func() {
		dst.Close()
		os.Remove(tempFilePath)
	}()

	if _, err = io.Copy(dst, src); err != nil {
		return nil, fmt.Errorf("disk write failure on payload compilation pass: %v", err)
	}
	dst.Close()

	doc, err := fitz.New(tempFilePath)
	if err != nil {
		return nil, fmt.Errorf("raster engine initialization failure on target container tree: %v", err)
	}
	defer doc.Close()

	adjustedPage := pageNum - 1
	if adjustedPage < 0 || adjustedPage >= doc.NumPage() {
		return nil, fmt.Errorf("requested bounds page index out of document limits: requested %d, total pages %d", pageNum, doc.NumPage())
	}

	dpi := 72.0 * scale
	img, err := doc.ImageDPI(adjustedPage, dpi)
	if err != nil {
		return nil, fmt.Errorf("vector translation framework crash on page raster assignment: %v", err)
	}

	var buffer bytes.Buffer
	err = jpeg.Encode(&buffer, img, &jpeg.Options{Quality: 85})
	if err != nil {
		return nil, fmt.Errorf("failed to compress memory matrices data stream to JPEG output: %v", err)
	}

	return buffer.Bytes(), nil
}
