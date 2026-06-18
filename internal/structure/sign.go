package structure

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// SignatureStamp defines the exact coordinates for a single signature drop
type SignatureStamp struct {
	Page int     `json:"page"`
	X    float64 `json:"x"` // Points from the left edge
	Y    float64 `json:"y"` // Points from the BOTTOM edge (PDF Standard)
}

// SignPdfMulti applies one or more signatures to precise coordinates on specific pages
func SignPdfMulti(inputPdfPath, signatureImgPath, outputPath, stampsJson string) error {
	var stamps []SignatureStamp
	if err := json.Unmarshal([]byte(stampsJson), &stamps); err != nil {
		return fmt.Errorf("failed to parse signature coordinates: %w", err)
	}

	if len(stamps) == 0 {
		return fmt.Errorf("no signature coordinates provided")
	}

	// We apply signatures iteratively. If a user signs the document 3 times,
	// we process it in 3 passes to ensure pdfcpu safely overlays them.
	currentInput := inputPdfPath

	for i, stamp := range stamps {
		currentOutput := outputPath

		// If this isn't the last signature, write to a temporary intermediate file
		if i < len(stamps)-1 {
			currentOutput = fmt.Sprintf("%s.temp%d", inputPdfPath, i)
		}

		// anchor to Bottom-Left (bl), then offset by exact X/Y points.
		// scale: 0.25 reduces the image size so it looks like a natural signature.
		desc := fmt.Sprintf("pos:bl, offset:%.2f %.2f, scale:0.25, rot:0, opacity:1.0", stamp.X, stamp.Y)

		wm, err := api.ImageWatermark(signatureImgPath, desc, true, false, types.POINTS)
		if err != nil {
			return fmt.Errorf("failed to initialize signature stamp: %w", err)
		}

		// Apply to the specific page
		pages := []string{fmt.Sprintf("%d", stamp.Page)}
		if err := api.AddWatermarksFile(currentInput, currentOutput, pages, wm, model.NewDefaultConfiguration()); err != nil {
			return fmt.Errorf("failed to apply signature to page %d: %w", stamp.Page, err)
		}

		// Clean up the intermediate temp file from the previous loop
		if i > 0 {
			os.Remove(currentInput)
		}
		// Set the output of this loop as the input for the next loop
		currentInput = currentOutput
	}

	return nil
}
