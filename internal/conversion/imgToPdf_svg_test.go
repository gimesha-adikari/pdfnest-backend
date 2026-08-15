package conversion

import (
	"os"
	"path/filepath"
	"testing"
)

func TestImagesToPDF_SVG_Support(t *testing.T) {
	service := &ConversionService{}
	tempDir := t.TempDir()

	// 1. Basic Shape SVG
	svg1 := `<svg width="200" height="100" viewBox="0 0 200 100" xmlns="http://www.w3.org/2000/svg">
		<rect width="200" height="100" fill="#2563eb" />
	</svg>`
	path1 := filepath.Join(tempDir, "shape.svg")
	if err := os.WriteFile(path1, []byte(svg1), 0600); err != nil {
		t.Fatalf("Failed writing svg1: %v", err)
	}

	// 2. Path & Gradient SVG
	svg2 := `<svg width="300" height="300" viewBox="0 0 300 300" xmlns="http://www.w3.org/2000/svg">
		<defs>
			<linearGradient id="g" x1="0%" y1="0%" x2="100%" y2="100%">
				<stop offset="0%" style="stop-color:#ff0000;stop-opacity:1" />
				<stop offset="100%" style="stop-color:#0000ff;stop-opacity:1" />
			</linearGradient>
		</defs>
		<circle cx="150" cy="150" r="100" fill="url(#g)" />
		<path d="M 100 100 L 200 100 L 150 200 Z" fill="#ffffff" />
	</svg>`
	path2 := filepath.Join(tempDir, "path_grad.svg")
	if err := os.WriteFile(path2, []byte(svg2), 0600); err != nil {
		t.Fatalf("Failed writing svg2: %v", err)
	}

	// 3. Transforms and Text SVG
	svg3 := `<svg width="250" height="250" viewBox="0 0 250 250" xmlns="http://www.w3.org/2000/svg">
		<g transform="translate(50, 50) rotate(15)">
			<rect width="100" height="100" fill="#10b981" />
			<text x="10" y="50" font-size="14" fill="#ffffff">Platen</text>
		</g>
	</svg>`
	path3 := filepath.Join(tempDir, "transform.svg")
	if err := os.WriteFile(path3, []byte(svg3), 0600); err != nil {
		t.Fatalf("Failed writing svg3: %v", err)
	}

	t.Run("Standard ImagesToPDF with multiple SVGs", func(t *testing.T) {
		outPdf, err := service.ImagesToPDF([]string{path1, path2, path3})
		if err != nil {
			t.Fatalf("ImagesToPDF failed on SVG batch: %v", err)
		}
		defer os.Remove(outPdf)

		fi, err := os.Stat(outPdf)
		if err != nil || fi.Size() == 0 {
			t.Fatalf("Generated PDF file is empty or non-existent")
		}
	})

	t.Run("CustomImagesToPDF with SVG items", func(t *testing.T) {
		layout := []CanvasLayoutItem{
			{
				ID:          "item-1",
				FileIndex:   0,
				PageIndex:   0,
				X:           20,
				Y:           30,
				Width:       150,
				Height:      80,
				BorderWidth: 1,
				BorderColor: "#2563eb",
				ZIndex:      1,
			},
			{
				ID:          "item-2",
				FileIndex:   1,
				PageIndex:   0,
				X:           100,
				Y:           120,
				Width:       120,
				Height:      120,
				BorderWidth: 0,
				ZIndex:      2,
			},
			{
				ID:          "item-3",
				FileIndex:   2,
				PageIndex:   1,
				X:           50,
				Y:           50,
				Width:       180,
				Height:      180,
				BorderWidth: 2,
				BorderColor: "#10b981",
				ZIndex:      1,
			},
		}

		outPdf, err := service.CustomImagesToPDF([]string{path1, path2, path3}, layout)
		if err != nil {
			t.Fatalf("CustomImagesToPDF failed with SVG assets: %v", err)
		}
		defer os.Remove(outPdf)

		fi, err := os.Stat(outPdf)
		if err != nil || fi.Size() == 0 {
			t.Fatalf("Custom PDF is empty or missing")
		}
	})

	t.Run("Security: Reject XXE attempt", func(t *testing.T) {
		xxeSvg := `<?xml version="1.0"?>
		<!DOCTYPE svg [
		  <!ENTITY xxe SYSTEM "file:///etc/passwd">
		]>
		<svg width="100" height="100" xmlns="http://www.w3.org/2000/svg">
			<text x="10" y="20">&xxe;</text>
		</svg>`
		xxePath := filepath.Join(tempDir, "xxe.svg")
		_ = os.WriteFile(xxePath, []byte(xxeSvg), 0600)

		_, err := service.ImagesToPDF([]string{xxePath})
		if err == nil {
			t.Fatalf("Expected security error rejecting XXE SVG, but got nil")
		}
	})

	t.Run("Malformed SVG handling", func(t *testing.T) {
		malformedSvg := `<svg width="100" height="100"><unclosed_tag fill="red"></svg>`
		malformedPath := filepath.Join(tempDir, "malformed.svg")
		_ = os.WriteFile(malformedPath, []byte(malformedSvg), 0600)

		_, err := service.ImagesToPDF([]string{malformedPath})
		if err == nil {
			t.Fatalf("Expected error for malformed SVG, but got nil")
		}
	})
}
