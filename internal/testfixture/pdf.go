// Package testfixture provides bounded, deterministic synthetic test inputs.
package testfixture

import (
	"bytes"
	"github.com/jung-kurt/gofpdf"
	"time"
)

// PDFBytes returns one ordinary text page without external files or services.
func PDFBytes() ([]byte, error) {
	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetCreationDate(time.Unix(1700000000, 0).UTC())
	pdf.SetModificationDate(time.Unix(1700000000, 0).UTC())
	pdf.AddPage()
	pdf.SetFont("Helvetica", "", 12)
	pdf.Text(20, 30, "Synthetic application lifecycle fixture")
	var out bytes.Buffer
	err := pdf.Output(&out)
	return out.Bytes(), err
}
