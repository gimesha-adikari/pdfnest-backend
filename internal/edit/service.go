// file: internal/edit/service.go
package edit

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

type Service interface {
	ExtractLayout(pdfPath string) ([]byte, error)
	CompileLayout(originalPdf string, payload []byte) (string, error)
}

type service struct{}

func NewService() Service {
	return &service{}
}

func (s *service) ExtractLayout(pdfPath string) ([]byte, error) {
	// Call layout mapping script
	output, err := runPythonScript("scripts/pdf_to_layout.py", pdfPath)
	if err != nil {
		return nil, err
	}
	return output, nil
}

func (s *service) CompileLayout(originalPdf string, payload []byte) (string, error) {
	// Write modified frontend text json to a temporary data track
	tempJsonPath := filepath.Join(os.TempDir(), uuid.New().String()+".json")
	if err := os.WriteFile(tempJsonPath, payload, 0644); err != nil {
		return "", err
	}
	defer os.Remove(tempJsonPath)

	outPdfName := fmt.Sprintf("precision_edited_%s.pdf", uuid.New().String())
	outPdfPath := filepath.Join(os.TempDir(), outPdfName)

	// Call patching script: (originalPdf, outputPdf, modificationsJson)
	_, err := runPythonScript("scripts/patch_pdf_layout.py", originalPdf, outPdfPath, tempJsonPath)
	if err != nil {
		return "", err
	}

	return outPdfPath, nil
}
