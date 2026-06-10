package structure

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func (s *structureService) ReorderPDFPages(inputPath string, sequence []string) (string, error) {
	tempDir := os.TempDir()
	pageDir, err := os.MkdirTemp(tempDir, "reorder-")
	if err != nil {
		return "", err
	}
	defer func(path string) {
		err := os.RemoveAll(path)
		if err != nil {
			_ = err
		}
	}(pageDir)

	config := model.NewDefaultConfiguration()

	err = api.ExtractPagesFile(inputPath, pageDir, sequence, config)
	if err != nil {
		return "", err
	}

	var orderedFiles []string

	for _, pageIdx := range sequence {
		pattern := filepath.Join(pageDir, "*"+pageIdx+".pdf")
		matches, err := filepath.Glob(pattern)

		if err == nil && len(matches) > 0 {
			orderedFiles = append(orderedFiles, matches[0])
		}
	}

	if len(orderedFiles) == 0 {
		return "", fmt.Errorf("extraction failed: no pages matched the sequence pattern in %s", pageDir)
	}

	outputPath := filepath.Join(tempDir, "reordered-"+uuid.New().String()+".pdf")

	err = api.MergeCreateFile(orderedFiles, outputPath, false, config)
	if err != nil {
		return "", err
	}

	return outputPath, nil
}
