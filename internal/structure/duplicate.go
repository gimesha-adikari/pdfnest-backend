package structure

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func (s *structureService) DuplicatePDFPages(inputPath string, pageSelection string, copies int) (string, error) {
	tempDir := os.TempDir()
	outputFile := "duplicated-" + uuid.New().String() + ".pdf"
	outputPath := filepath.Join(tempDir, outputFile)

	config := model.NewDefaultConfiguration()

	total, err := api.PageCountFile(inputPath)
	if err != nil {
		return "", fmt.Errorf("failed to examine pdf page count: %w", err)
	}
	if total <= 0 {
		return "", fmt.Errorf("document is empty")
	}

	selectedMap := make(map[int]bool)
	parts := strings.Split(pageSelection, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		if strings.Contains(part, "-") {
			ranges := strings.Split(part, "-")
			if len(ranges) == 2 {
				start, err1 := strconv.Atoi(strings.TrimSpace(ranges[0]))
				end, err2 := strconv.Atoi(strings.TrimSpace(ranges[1]))
				if err1 == nil && err2 == nil && start <= end {
					for p := start; p <= end; p++ {
						selectedMap[p] = true
					}
				}
			}
		} else {
			p, err := strconv.Atoi(part)
			if err == nil {
				selectedMap[p] = true
			}
		}
	}

	var outputSequence []string

	for i := 1; i <= total; i++ {
		outputSequence = append(outputSequence, strconv.Itoa(i))

		if selectedMap[i] {
			for c := 0; c < copies; c++ {
				outputSequence = append(outputSequence, strconv.Itoa(i))
			}
		}
	}

	err = api.CollectFile(inputPath, outputPath, outputSequence, config)
	if err != nil {
		return "", fmt.Errorf("duplication layout building matrix failed: %w", err)
	}

	return outputPath, nil
}
