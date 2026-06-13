// file: internal/edit/service.go
package edit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

type Service interface {
	ExtractHTML(pdfPath string) (string, error)
	CompilePDF(html string) (string, error)
}

type service struct{}

func NewService() Service {
	return &service{}
}

func (s *service) ExtractHTML(pdfPath string) (string, error) {
	output, err := runPythonScript(
		"scripts/pdf_to_html.py",
		pdfPath,
	)

	if err != nil {
		return "", err
	}

	var result struct {
		Success bool   `json:"success"`
		HTML    string `json:"html"`
		Error   string `json:"error"`
	}

	if err := json.Unmarshal(output, &result); err != nil {
		return "", err
	}

	if !result.Success {
		return "", fmt.Errorf(result.Error)
	}

	return result.HTML, nil
}

func (s *service) CompilePDF(html string) (string, error) {
	tempHtmlPath := filepath.Join(
		os.TempDir(),
		uuid.New().String()+".html",
	)

	if err := os.WriteFile(tempHtmlPath, []byte(html), 0644); err != nil {
		return "", err
	}

	outPdfPath := filepath.Join(
		os.TempDir(),
		fmt.Sprintf("edited_%s.pdf", uuid.New().String()),
	)

	_, err := runPythonScript(
		"scripts/html_to_pdf.py",
		tempHtmlPath,
		outPdfPath,
	)

	os.Remove(tempHtmlPath)

	if err != nil {
		return "", err
	}

	return outPdfPath, nil
}
