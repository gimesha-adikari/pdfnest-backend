package edit

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

const editPipelineScript = "scripts/pdf_edit_pipeline.py"

type Service interface {
	ExtractLayout(pdfPath string) ([]byte, error)
	CompileLayout(originalPdf string, payload []byte) (string, error)
}

type service struct{}

func NewService() Service {
	return &service{}
}

func (s *service) ExtractLayout(pdfPath string) ([]byte, error) {
	output, err := runPythonScript(editPipelineScript, "extract", pdfPath)
	if err != nil {
		return nil, err
	}
	return output, nil
}

func (s *service) CompileLayout(originalPdf string, payload []byte) (string, error) {
	tempJsonPath := filepath.Join(os.TempDir(), uuid.New().String()+".json")
	if err := os.WriteFile(tempJsonPath, payload, 0644); err != nil {
		return "", err
	}
	defer os.Remove(tempJsonPath)

	outPdfPath := filepath.Join(os.TempDir(), fmt.Sprintf("precision_edited_%s.pdf", uuid.New().String()))

	_, err := runPythonScript(editPipelineScript, "compile", originalPdf, outPdfPath, tempJsonPath)
	if err != nil {
		return "", err
	}

	return outPdfPath, nil
}
