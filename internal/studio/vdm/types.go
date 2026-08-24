package vdm

import (
	"encoding/json"
	"errors"
	"fmt"
)

// DocumentModel represents the canonical Virtual Document Model (VDM) for a Studio version.
type DocumentModel struct {
	DocumentID    string             `json:"document_id"`
	VersionID     string             `json:"version_id,omitempty"`
	PageCount     int                `json:"page_count"`
	Pages         []PageDescriptor   `json:"pages"`
	PageNumbering *PageNumberingRule `json:"page_numbering,omitempty"`
	Metadata      map[string]string  `json:"metadata,omitempty"`
}

// Dimensions defines page width and height in native PostScript points (1/72 inch).
type Dimensions struct {
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// PageDescriptor describes a single page's virtual transformation and overlay composition.
type PageDescriptor struct {
	PageID           string      `json:"page_id"`
	SourceAssetID    *string     `json:"source_asset_id"` // null for blank synthetic pages
	SourcePageNumber int         `json:"source_page_number"`
	ParentPageID     *string     `json:"parent_page_id,omitempty"`
	IsBlank          bool        `json:"is_blank"`
	Dimensions       *Dimensions `json:"dimensions,omitempty"`
	Rotation         int         `json:"rotation"`           // 0, 90, 180, 270
	CropBox          []float64   `json:"crop_box,omitempty"` // [llx, lly, urx, ury] in native points
	Overlays         []Overlay   `json:"overlays"`
}

// Overlay describes an annotation, stamp, watermark, or signature placed on a page.
type Overlay struct {
	ID         string    `json:"id"`
	Type       string    `json:"type"` // watermark, signature, text, highlight, underline, strikeout
	Text       string    `json:"text,omitempty"`
	Font       string    `json:"font,omitempty"`
	FontSize   float64   `json:"font_size,omitempty"`
	Opacity    float64   `json:"opacity,omitempty"`
	Rotation   int       `json:"rotation,omitempty"`
	AssetID    string    `json:"asset_id,omitempty"`
	AssetR2Key string    `json:"asset_r2_key,omitempty"` // populated dynamically during worker preview
	Rect       []float64 `json:"rect,omitempty"`         // [x, y, w, h] in native points
	Quads      []float64 `json:"quads,omitempty"`        // PyMuPDF annotation quads
}

// PageNumberingRule defines dynamic page numbering overlay settings.
type PageNumberingRule struct {
	Enabled        bool     `json:"enabled"`
	Format         string   `json:"format"`   // e.g. "Page {page} of {total}"
	Position       string   `json:"position"` // bottom-center, bottom-right, top-right, etc.
	FontSize       float64  `json:"font_size"`
	FontFamily     string   `json:"font_family"`
	StartAt        int      `json:"start_at"`
	OmittedPageIDs []string `json:"omitted_page_ids,omitempty"`
}

// Validate ensures all structural invariants of the VDM hold true.
func (dm *DocumentModel) Validate() error {
	if dm.DocumentID == "" {
		return errors.New("vdm: document_id is required")
	}
	if len(dm.Pages) != dm.PageCount {
		return fmt.Errorf("vdm: page_count (%d) does not match len(pages) (%d)", dm.PageCount, len(dm.Pages))
	}

	seenPages := make(map[string]bool)
	for i, page := range dm.Pages {
		if page.PageID == "" {
			return fmt.Errorf("vdm: page[%d] is missing page_id", i)
		}
		if seenPages[page.PageID] {
			return fmt.Errorf("vdm: duplicate page_id '%s' at page[%d]", page.PageID, i)
		}
		seenPages[page.PageID] = true

		if page.Rotation%90 != 0 || page.Rotation < 0 || page.Rotation >= 360 {
			return fmt.Errorf("vdm: page[%d] has invalid rotation %d (must be 0, 90, 180, 270)", i, page.Rotation)
		}

		if len(page.CropBox) != 0 && len(page.CropBox) != 4 {
			return fmt.Errorf("vdm: page[%d] crop_box must have 4 elements [llx, lly, urx, ury]", i)
		}

		if !page.IsBlank && (page.SourceAssetID == nil || *page.SourceAssetID == "") {
			return fmt.Errorf("vdm: non-blank page[%d] must have a source_asset_id", i)
		}
	}
	return nil
}

// ToJSON serializes the VDM to byte slice.
func (dm *DocumentModel) ToJSON() ([]byte, error) {
	if err := dm.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(dm)
}

// FromJSON deserializes bytes into a validated DocumentModel.
func FromJSON(data []byte) (*DocumentModel, error) {
	var dm DocumentModel
	if err := json.Unmarshal(data, &dm); err != nil {
		return nil, fmt.Errorf("vdm: failed to unmarshal json: %w", err)
	}
	if err := dm.Validate(); err != nil {
		return nil, err
	}
	return &dm, nil
}
