package structure

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

type SignatureStamp struct {
	Page int     `json:"page"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
}

func SignPdfMulti(inputPdfPath, signatureImgPath, outputPath, stampsJson string) error {
	var stamps []SignatureStamp
	if err := json.Unmarshal([]byte(stampsJson), &stamps); err != nil {
		return fmt.Errorf("failed to parse signature coordinates: %w", err)
	}

	if len(stamps) == 0 {
		return fmt.Errorf("no signature coordinates provided")
	}

	currentInput := inputPdfPath

	for i, stamp := range stamps {
		currentOutput := outputPath

		if i < len(stamps)-1 {
			currentOutput = fmt.Sprintf("%s.temp%d", inputPdfPath, i)
		}

		desc := fmt.Sprintf("pos:bl, offset:%.2f %.2f, scale:0.25, rot:0, opacity:1.0", stamp.X, stamp.Y)

		wm, err := api.ImageWatermark(signatureImgPath, desc, true, false, types.POINTS)
		if err != nil {
			return fmt.Errorf("failed to initialize signature stamp: %w", err)
		}

		pages := []string{fmt.Sprintf("%d", stamp.Page)}
		if err := api.AddWatermarksFile(currentInput, currentOutput, pages, wm, model.NewDefaultConfiguration()); err != nil {
			return fmt.Errorf("failed to apply signature to page %d: %w", stamp.Page, err)
		}

		if i > 0 {
			os.Remove(currentInput)
		}
		currentInput = currentOutput
	}

	return nil
}
