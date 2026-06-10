package structure

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func (s *structureService) AddPageNumbersPDF(inputPath string, description string) (string, error) {
	tempDir := os.TempDir()
	outputFile := "numbered-" + uuid.New().String() + ".pdf"
	outputPath := filepath.Join(tempDir, outputFile)

	config := model.NewDefaultConfiguration()

	if description == "" {
		description = "font:Helvetica, pos:bc, points:12, rot:0"
	}

	if strings.Contains(description, "pos:bl") {
		description += ", offset: 20 20"
	} else if strings.Contains(description, "pos:bc") {
		description += ", offset: 0 20"
	} else if strings.Contains(description, "pos:br") {
		description += ", offset: -20 20"
	} else if strings.Contains(description, "pos:tl") {
		description += ", offset: 20 -20"
	} else if strings.Contains(description, "pos:tc") {
		description += ", offset: 0 -20"
	} else if strings.Contains(description, "pos:tr") {
		description += ", offset: -20 -20"
	}

	pageTextMacro := "%p"

	wm, err := api.TextWatermark(pageTextMacro, description, true, false, types.POINTS)
	if err != nil {
		return "", err
	}

	inFile, err := os.Open(inputPath)
	if err != nil {
		return "", err
	}
	defer func(inFile *os.File) {
		err := inFile.Close()
		if err != nil {
			_ = err
		}
	}(inFile)

	outFile, err := os.Create(outputPath)
	if err != nil {
		return "", err
	}
	defer func(outFile *os.File) {
		err := outFile.Close()
		if err != nil {
			_ = err
		}
	}(outFile)

	err = api.AddWatermarks(inFile, outFile, nil, wm, config)
	if err != nil {
		return "", err
	}

	return outputPath, nil
}
