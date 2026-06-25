package structure

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func (s *structureService) InsertBlankPages(inputPath string, insertAt string, targetPage int, count int) (string, error) {
	tempDir := os.TempDir()
	outputFile := "inserted-" + uuid.New().String() + ".pdf"
	outputPath := filepath.Join(tempDir, outputFile)

	config := model.NewDefaultConfiguration()

	total, err := api.PageCountFile(inputPath)
	if err != nil {
		return "", fmt.Errorf("failed to examine pdf page count: %w", err)
	}

	var anchorPage int
	var insertBefore bool

	switch insertAt {
	case "start":
		anchorPage = 1
		insertBefore = true
	case "end":
		anchorPage = total
		insertBefore = false
	case "after":
		if targetPage < 1 || targetPage > total {
			return "", fmt.Errorf("target page index %d is out of bounds (1-%d)", targetPage, total)
		}
		anchorPage = targetPage
		insertBefore = false
	default:
		return "", fmt.Errorf("unknown insertion strategy option: %s", insertAt)
	}

	pageSelection := []string{strconv.Itoa(anchorPage)}

	currentIn := inputPath

	for i := 0; i < count; i++ {
		currentOut := outputPath

		if i < count-1 {
			currentOut = filepath.Join(tempDir, fmt.Sprintf("step-%d-%s", i, uuid.New().String()+".pdf"))
		}

		err = api.InsertPagesFile(currentIn, currentOut, pageSelection, insertBefore, nil, config)
		if err != nil {
			return "", fmt.Errorf("blank page insertion failed at step %d: %w", i+1, err)
		}

		if currentIn != inputPath {
			_ = os.Remove(currentIn)
		}

		currentIn = currentOut
	}

	return outputPath, nil
}
