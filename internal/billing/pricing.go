package billing

import (
	"math"
	"strings"
)

type ToolProfile struct {
	Name        string
	BaseUnits   int
	PageFactor  float64
	ImageFactor float64
}

var toolProfiles = map[string]ToolProfile{
	"compress":              {Name: "compress", BaseUnits: 2, PageFactor: 0.20},
	"delete":                {Name: "delete", BaseUnits: 1, PageFactor: 0.08},
	"reorder":               {Name: "reorder", BaseUnits: 1, PageFactor: 0.10},
	"watermark":             {Name: "watermark", BaseUnits: 2, PageFactor: 0.15},
	"add_page_numbers":      {Name: "add_page_numbers", BaseUnits: 2, PageFactor: 0.12},
	"highlight":             {Name: "highlight", BaseUnits: 4, PageFactor: 0.20},
	"extract_text_from_pdf": {Name: "extract_text_from_pdf", BaseUnits: 5, PageFactor: 0.50},
	"image_to_text_pdf":     {Name: "image_to_text_pdf", BaseUnits: 6, ImageFactor: 0.80},
	"pdf_edit_extract":      {Name: "pdf_edit_extract", BaseUnits: 3, PageFactor: 0.10},
	"pdf_edit_compile":      {Name: "pdf_edit_compile", BaseUnits: 3, PageFactor: 0.10},
	"markdown_to_pdf":       {Name: "markdown_to_pdf", BaseUnits: 3},
	"code_to_pdf":           {Name: "code_to_pdf", BaseUnits: 3},
}

var tierLimits = map[string]TierLimits{
	"free": {Units3H: 8, UnitsDay: 20, UnitsMonth: 80},
	"plus": {Units3H: 20, UnitsDay: 60, UnitsMonth: 250},
	"pro":  {Units3H: 80, UnitsDay: 250, UnitsMonth: 1000},
}

func NormalizeToolName(tool string) string {
	tool = strings.TrimSpace(strings.ToLower(tool))
	switch tool {
	case "rotate_pdf", "rotate":
		return "rotate"
	case "delete_pdf", "delete":
		return "delete"
	case "reorder_pages", "reorder":
		return "reorder"
	case "add_page_numbers", "pages_number", "duplicate":
		return "add_page_numbers"
	case "ocr", "extract_text", "extract_text_from_pdf":
		return "extract_text_from_pdf"
	case "image_ocr", "image_to_text_pdf":
		return "image_to_text_pdf"
	}
	return tool
}

func GetTierLimits(tier string) TierLimits {
	if lim, ok := tierLimits[strings.ToLower(strings.TrimSpace(tier))]; ok {
		return lim
	}
	return tierLimits["free"]
}

func EstimateUnits(tool string, pages, images int) ToolEstimate {
	tool = NormalizeToolName(tool)
	profile, ok := toolProfiles[tool]
	if !ok {
		profile = ToolProfile{Name: tool, BaseUnits: 1, PageFactor: 0.10, ImageFactor: 0.0}
	}

	if pages < 0 {
		pages = 0
	}
	if images < 0 {
		images = 0
	}

	units := float64(profile.BaseUnits) + float64(pages)*profile.PageFactor + float64(images)*profile.ImageFactor
	total := int(math.Ceil(units))
	if total < 1 {
		total = 1
	}

	return ToolEstimate{
		ToolName:    tool,
		BaseUnits:   profile.BaseUnits,
		Pages:       pages,
		Images:      images,
		PageFactor:  profile.PageFactor,
		ImageFactor: profile.ImageFactor,
		Units:       total,
	}
}

func InferToolName(path string) string {
	p := strings.ToLower(strings.TrimSpace(path))

	switch {
	case strings.Contains(p, "/ocr/extract-text-async"):
		return "extract_text_from_pdf"
	case strings.Contains(p, "/ocr/extract-text"):
		return "extract_text_from_pdf"
	case strings.Contains(p, "/ocr/to-text-pdf-async"):
		return "image_to_text_pdf"
	case strings.Contains(p, "/ocr/to-text-pdf"):
		return "image_to_text_pdf"

	case strings.Contains(p, "/edit/extract"):
		return "pdf_edit_extract"
	case strings.Contains(p, "/edit/compile"):
		return "pdf_edit_compile"

	case strings.Contains(p, "markdown") && strings.Contains(p, "pdf"):
		return "markdown_to_pdf"
	case strings.Contains(p, "code") && strings.Contains(p, "pdf"):
		return "code_to_pdf"

	case strings.Contains(p, "compress"):
		return "compress"
	case strings.Contains(p, "delete"):
		return "delete"
	case strings.Contains(p, "reorder"):
		return "reorder"
	case strings.Contains(p, "watermark"):
		return "watermark"
	case strings.Contains(p, "page-number"):
		return "add_page_numbers"
	case strings.Contains(p, "highlight"):
		return "highlight"
	}

	return ""
}
