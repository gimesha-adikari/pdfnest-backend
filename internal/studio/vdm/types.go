package vdm

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
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

// OverlayType is the closed set of page-local deferred overlay kinds supported
// by the VDM. Product tools can add richer typed parameters later without
// changing the page-local ownership or ordering contract.
type OverlayType string

const (
	OverlayTypeWatermark OverlayType = "watermark"
	OverlayTypeSignature OverlayType = "signature"
	OverlayTypeText      OverlayType = "text"
	OverlayTypeHighlight OverlayType = "highlight"
	OverlayTypeUnderline OverlayType = "underline"
	OverlayTypeStrikeout OverlayType = "strikeout"
)

// Overlay describes a deferred page-local annotation, stamp, watermark, or
// signature. Coordinates use native unrotated PDF page space in PostScript
// points with a lower-left origin. Rect is [x, y, width, height]. Array order
// is the deterministic z-order from back to front.
type Overlay struct {
	ID         string    `json:"id"`
	Type       string    `json:"type"` // watermark, signature, text, highlight, underline, strikeout
	Text       string    `json:"text,omitempty"`
	Font       string    `json:"font,omitempty"`
	Color      string    `json:"color,omitempty"`
	FontSize   float64   `json:"font_size,omitempty"`
	Opacity    float64   `json:"opacity,omitempty"`
	Rotation   int       `json:"rotation,omitempty"`
	Position   string    `json:"position,omitempty"`
	AssetID    string    `json:"asset_id,omitempty"`
	AssetR2Key string    `json:"asset_r2_key,omitempty"` // populated dynamically during worker preview
	Rect       []float64 `json:"rect,omitempty"`         // [x, y, w, h] in native points
	Quads      []float64 `json:"quads,omitempty"`        // PyMuPDF annotation quads
}

func (overlay Overlay) Validate() error {
	if overlay.ID == "" {
		return errors.New("vdm: overlay id is required")
	}
	switch OverlayType(overlay.Type) {
	case OverlayTypeWatermark, OverlayTypeSignature, OverlayTypeText, OverlayTypeHighlight, OverlayTypeUnderline, OverlayTypeStrikeout:
	default:
		return fmt.Errorf("vdm: unknown overlay type %q", overlay.Type)
	}
	if len(overlay.Rect) != 0 && len(overlay.Rect) != 4 {
		return errors.New("vdm: overlay rect must have 4 elements [x, y, width, height]")
	}
	for _, value := range overlay.Rect {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return errors.New("vdm: overlay rect must contain finite non-negative values")
		}
	}
	for _, value := range overlay.Quads {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return errors.New("vdm: overlay quads must contain finite values")
		}
	}
	if overlay.FontSize < 0 || math.IsNaN(overlay.FontSize) || math.IsInf(overlay.FontSize, 0) {
		return errors.New("vdm: overlay font_size must be finite and non-negative")
	}
	if overlay.Opacity < 0 || overlay.Opacity > 1 || math.IsNaN(overlay.Opacity) || math.IsInf(overlay.Opacity, 0) {
		return errors.New("vdm: overlay opacity must be finite and between 0 and 1")
	}
	if !utf8.ValidString(overlay.Text) || strings.IndexByte(overlay.Text, 0) >= 0 {
		return errors.New("vdm: overlay text must be valid UTF-8 without NUL")
	}
	if overlay.Color != "" && !strings.HasPrefix(overlay.Color, "#") {
		return errors.New("vdm: overlay color must be a hex color")
	}
	return nil
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

func (rule PageNumberingRule) Validate() error {
	if !utf8.ValidString(rule.Format) || strings.IndexByte(rule.Format, 0) >= 0 {
		return errors.New("vdm: page numbering format must be valid UTF-8 without NUL")
	}
	if !utf8.ValidString(rule.Position) || strings.IndexByte(rule.Position, 0) >= 0 {
		return errors.New("vdm: page numbering position must be valid UTF-8 without NUL")
	}
	if !utf8.ValidString(rule.FontFamily) || strings.IndexByte(rule.FontFamily, 0) >= 0 {
		return errors.New("vdm: page numbering font family must be valid UTF-8 without NUL")
	}
	if rule.FontSize < 0 || math.IsNaN(rule.FontSize) || math.IsInf(rule.FontSize, 0) {
		return errors.New("vdm: page numbering font_size must be finite and non-negative")
	}
	if rule.StartAt < 0 {
		return errors.New("vdm: page numbering start_at must be non-negative")
	}
	seen := make(map[string]struct{}, len(rule.OmittedPageIDs))
	for _, pageID := range rule.OmittedPageIDs {
		if pageID == "" || !utf8.ValidString(pageID) || strings.IndexByte(pageID, 0) >= 0 {
			return errors.New("vdm: page numbering omitted page IDs must be valid non-empty UTF-8")
		}
		if _, exists := seen[pageID]; exists {
			return fmt.Errorf("vdm: page numbering omitted page ID %q is duplicated", pageID)
		}
		seen[pageID] = struct{}{}
	}
	return nil
}

// Validate ensures all structural invariants of the VDM hold true.
func (dm *DocumentModel) Validate() error {
	if dm.DocumentID == "" {
		return errors.New("vdm: document_id is required")
	}
	if len(dm.Pages) != dm.PageCount {
		return fmt.Errorf("vdm: page_count (%d) does not match len(pages) (%d)", dm.PageCount, len(dm.Pages))
	}
	if dm.PageNumbering != nil {
		if err := dm.PageNumbering.Validate(); err != nil {
			return err
		}
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
		for overlayIndex, overlay := range page.Overlays {
			if err := overlay.Validate(); err != nil {
				return fmt.Errorf("vdm: page[%d] overlay[%d]: %w", i, overlayIndex, err)
			}
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
