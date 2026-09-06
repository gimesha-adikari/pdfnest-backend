package studio

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// EditorLayout mirrors the worker's public extract contract. Trackers are
// opaque editor metadata, never storage locations; this worker currently does
// not require either tracker to compile a layout.
type EditorLayout struct {
	SchemaVersion  string         `json:"schema_version,omitempty"`
	OCRV2          bool           `json:"ocr_v2,omitempty"`
	Success        bool           `json:"success"`
	Pages          []EditorPage   `json:"pages"`
	Source         map[string]any `json:"source,omitempty"`
	SourceTracker  string         `json:"source_tracker,omitempty"`
	UprightTracker string         `json:"upright_tracker,omitempty"`
	LanguageMode   string         `json:"language_mode,omitempty"`
	Languages      []string       `json:"languages,omitempty"`
}
type EditorPage struct {
	PageNum           int             `json:"page_num"`
	Width             float64         `json:"width"`
	Height            float64         `json:"height"`
	Elements          []EditorElement `json:"elements"`
	Kind              string          `json:"kind"`
	HasSelectableText bool            `json:"has_selectable_text"`
	WordCount         int             `json:"word_count"`
	TextBlockCount    int             `json:"text_block_count"`
	ImageBlockCount   int             `json:"image_block_count"`
	IsOCR             *bool           `json:"is_ocr,omitempty"`
	Source            string          `json:"source,omitempty"`
	Provenance        []string        `json:"provenance,omitempty"`
	ReadingOrder      []string        `json:"reading_order,omitempty"`
	Capabilities      []string        `json:"capabilities,omitempty"`
}
type EditorElement struct {
	ID              string               `json:"id"`
	Text            string               `json:"text"`
	Original        string               `json:"original_text,omitempty"`
	TargetSubstring string               `json:"target_substring,omitempty"`
	SelectionStart  *int                 `json:"selection_start,omitempty"`
	SelectionEnd    *int                 `json:"selection_end,omitempty"`
	X               float64              `json:"x"`
	Y               float64              `json:"y"`
	Width           float64              `json:"width"`
	Height          float64              `json:"height"`
	Size            float64              `json:"size"`
	Font            string               `json:"font"`
	BGColor         string               `json:"bg_color,omitempty"`
	TextColor       string               `json:"text_color,omitempty"`
	TransparentBG   bool                 `json:"transparent_bg,omitempty"`
	OCRV2           bool                 `json:"ocr_v2,omitempty"`
	Source          string               `json:"source,omitempty"`
	Provenance      []string             `json:"provenance,omitempty"`
	WordIDs         []string             `json:"word_ids,omitempty"`
	WordGeometry    []EditorWordGeometry `json:"word_geometry,omitempty"`
	ReadingOrder    []string             `json:"reading_order,omitempty"`
	Confidence      *float64             `json:"confidence,omitempty"`
	Style           *EditorElementStyle  `json:"style,omitempty"`
}

type EditorElementStyle struct {
	FontFamily    string   `json:"fontFamily,omitempty"`
	FontSize      *float64 `json:"fontSize,omitempty"`
	Bold          *bool    `json:"bold,omitempty"`
	Italic        *bool    `json:"italic,omitempty"`
	Underline     *bool    `json:"underline,omitempty"`
	Strikethrough *bool    `json:"strikethrough,omitempty"`
	Color         string   `json:"color,omitempty"`
	Background    string   `json:"background,omitempty"`
}

type EditorWordGeometry struct {
	ID     string  `json:"id"`
	Text   string  `json:"text"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

func decodeEditorLayout(raw []byte) (EditorLayout, []byte, error) {
	if len(raw) == 0 || len(raw) > 32<<20 {
		return EditorLayout{}, nil, ErrInvalidJob
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var layout EditorLayout
	if err := dec.Decode(&layout); err != nil {
		return EditorLayout{}, nil, ErrInvalidJob
	}
	if dec.More() {
		return EditorLayout{}, nil, ErrInvalidJob
	}
	if err := validateEditorLayout(layout); err != nil {
		return EditorLayout{}, nil, err
	}
	canonical, err := json.Marshal(layout)
	if err != nil {
		return EditorLayout{}, nil, fmt.Errorf("marshal editor layout: %w", err)
	}
	return layout, canonical, nil
}

func validateEditorLayout(layout EditorLayout) error {
	if len(layout.Pages) == 0 || len(layout.Pages) > 10000 {
		return ErrInvalidJob
	}
	seenPages := make(map[int]bool, len(layout.Pages))
	seenElements := map[string]bool{}
	if layout.LanguageMode != "" && layout.LanguageMode != "AUTO" && layout.LanguageMode != "EXPLICIT" {
		return ErrInvalidJob
	}
	if len(layout.Languages) > 3 {
		return ErrInvalidJob
	}
	for _, code := range layout.Languages {
		if code != "eng" && code != "sin" && code != "tam" {
			return ErrInvalidJob
		}
	}
	for index, page := range layout.Pages {
		if page.PageNum != index+1 || seenPages[page.PageNum] || !finitePositive(page.Width) || !finitePositive(page.Height) {
			return ErrInvalidJob
		}
		seenPages[page.PageNum] = true
		switch page.Kind {
		case "text", "mixed", "scanned", "blank":
		default:
			return ErrInvalidJob
		}
		for _, e := range page.Elements {
			if strings.TrimSpace(e.ID) == "" || len(e.ID) > 256 || seenElements[e.ID] || len(e.Text) > 1<<20 || len(e.TargetSubstring) > 1<<20 || len(e.Font) > 256 || !finite(e.X) || !finite(e.Y) || !finitePositive(e.Width) || !finitePositive(e.Height) || !finitePositive(e.Size) {
				return ErrInvalidJob
			}
			if (e.SelectionStart == nil) != (e.SelectionEnd == nil) {
				return ErrInvalidJob
			}
			if e.SelectionStart != nil {
				originalRunes := []rune(e.Original)
				if *e.SelectionStart < 0 || *e.SelectionEnd < *e.SelectionStart || *e.SelectionEnd > len(originalRunes) || e.TargetSubstring == "" || string(originalRunes[*e.SelectionStart:*e.SelectionEnd]) != e.TargetSubstring {
					return ErrInvalidJob
				}
			}
			seenElements[e.ID] = true
			if !validEditorStyle(e.Style) {
				return ErrInvalidJob
			}
		}
	}
	if layout.SourceTracker != "" || layout.UprightTracker != "" {
		return ErrInvalidJob
	}
	return nil
}

func validEditorStyle(style *EditorElementStyle) bool {
	if style == nil {
		return true
	}
	switch style.FontFamily {
	case "", "original", "helv", "tiro", "cour":
	default:
		return false
	}
	if style.FontSize != nil && (!finitePositive(*style.FontSize) || *style.FontSize < 6 || *style.FontSize > 72) {
		return false
	}
	for _, color := range []string{style.Color, style.Background} {
		if color != "" && color != "transparent" && (len(color) != 7 || color[0] != '#') {
			return false
		}
		if len(color) == 7 {
			for _, c := range color[1:] {
				if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
					return false
				}
			}
		}
	}
	return true
}

func validateEditedEditorLayout(base EditorLayout, edited EditorLayout) error {
	if err := validateEditorLayout(edited); err != nil {
		return err
	}
	if len(base.Pages) != len(edited.Pages) {
		return ErrInvalidJob
	}
	if base.LanguageMode != edited.LanguageMode || strings.Join(base.Languages, "+") != strings.Join(edited.Languages, "+") {
		return ErrInvalidJob
	}
	for i := range base.Pages {
		if base.Pages[i].PageNum != edited.Pages[i].PageNum || base.Pages[i].Width != edited.Pages[i].Width || base.Pages[i].Height != edited.Pages[i].Height || len(base.Pages[i].Elements) != len(edited.Pages[i].Elements) {
			return ErrInvalidJob
		}
		for j := range base.Pages[i].Elements {
			baseElement := base.Pages[i].Elements[j]
			editedElement := edited.Pages[i].Elements[j]
			if baseElement.ID != editedElement.ID || baseElement.Original != editedElement.Original || baseElement.X != editedElement.X || baseElement.Y != editedElement.Y || baseElement.Width != editedElement.Width || baseElement.Height != editedElement.Height {
				return ErrInvalidJob
			}
		}
	}
	return nil
}
func finite(v float64) bool         { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func finitePositive(v float64) bool { return finite(v) && v > 0 }
