// file: internal/billing/tools.go
package billing

import (
	"encoding/json"
	"fmt"
	"math"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
)

type Estimator func(c *fiber.Ctx) (pages int, images int, err error)

type Tool struct {
	Name        string
	Aliases     []string
	Billable    bool
	BaseUnits   int
	PageFactor  float64
	ImageFactor float64
	Estimate    Estimator
}

type ocrBoxPage struct {
	Page int `json:"page"`
}

func normalizeKey(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "_")
	return s
}

func (t Tool) Units(pages, images int) int {
	base := t.BaseUnits
	if base <= 0 {
		base = 1
	}

	units := float64(base) + float64(pages)*t.PageFactor + float64(images)*t.ImageFactor
	n := int(math.Ceil(units))
	if n < 1 {
		return 1
	}
	return n
}

func (t Tool) WithEstimator(est Estimator) Tool {
	t.Estimate = est
	return t
}

func EstimateNone() Estimator {
	return func(c *fiber.Ctx) (int, int, error) {
		return 0, 0, nil
	}
}

func EstimateUploadedPDF(field string) Estimator {
	return func(c *fiber.Ctx) (int, int, error) {
		return CountUploadedPDFPages(c, field), 0, nil
	}
}

func EstimateUploadedPDFFields(fields ...string) Estimator {
	return func(c *fiber.Ctx) (int, int, error) {
		return CountUploadedPDFPagesFromFields(c, fields...), 0, nil
	}
}

func EstimateUploadedImages(field string) Estimator {
	return func(c *fiber.Ctx) (int, int, error) {
		return 0, CountUploadedImages(c, field), nil
	}
}

func EstimateUploadedImagesFields(fields ...string) Estimator {
	return func(c *fiber.Ctx) (int, int, error) {
		return 0, CountUploadedImagesFromFields(c, fields...), nil
	}
}

func EstimateUploadedDocuments(fields ...string) Estimator {
	return func(c *fiber.Ctx) (int, int, error) {
		return CountUploadedFilesFromFields(c, fields...), 0, nil
	}
}

func EstimateSelectedPages(field string) Estimator {
	return func(c *fiber.Ctx) (int, int, error) {
		return CountSelectedPages(c.FormValue(field)), 0, nil
	}
}

func EstimateSourceTrackerFromBody(field string) Estimator {
	return func(c *fiber.Ctx) (int, int, error) {
		var payload map[string]any
		if err := json.Unmarshal(c.Body(), &payload); err != nil {
			return 0, 0, err
		}

		raw, _ := payload[field].(string)
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return 0, 0, nil
		}

		if _, err := os.Stat(raw); err != nil {
			return 0, 0, nil
		}

		pages, err := api.PageCountFile(raw)
		if err != nil {
			return 0, 0, err
		}
		return pages, 0, nil
	}
}

func FreeTool(name string, aliases ...string) Tool {
	return Tool{
		Name:      name,
		Aliases:   aliases,
		Billable:  false,
		BaseUnits: 0,
		Estimate:  EstimateNone(),
	}
}

var (
	// OCR
	ExtractTextPDF = Tool{
		Name:       "extract_text_from_pdf",
		Aliases:    []string{"ocr_extract_text", "ocr"},
		Billable:   true,
		BaseUnits:  5,
		PageFactor: 0.50,
		Estimate:   EstimateUploadedPDFFields("file", "files", "pdf", "pdfs", "documents"),
	}
	ImageToTextPDF = Tool{
		Name:        "image_to_text_pdf",
		Aliases:     []string{"ocr_image_to_text", "image_ocr"},
		Billable:    true,
		BaseUnits:   6,
		ImageFactor: 0.80,
		Estimate:    EstimateUploadedImagesFields("images", "files", "file"),
	}

	// Conversion
	ConvertImagesToPDF = Tool{
		Name:        "to_pdf",
		Aliases:     []string{"convert_images_to_pdf"},
		Billable:    true,
		BaseUnits:   2,
		ImageFactor: 0.20,
		Estimate:    EstimateUploadedImagesFields("images", "files", "file", "uploads"),
	}
	ConvertCustomImagesToPDF = Tool{
		Name:        "custom_to_pdf",
		Aliases:     []string{"convert_custom_images_to_pdf"},
		Billable:    true,
		BaseUnits:   2,
		ImageFactor: 0.20,
		Estimate:    EstimateUploadedImagesFields("images", "files", "file", "uploads"),
	}
	RasterizePDFUniversal = Tool{
		Name:       "pdf_to_images",
		Aliases:    []string{"pdf_rasterize", "pdf_to_png", "pdf_to_jpg"},
		Billable:   true,
		BaseUnits:  2,
		PageFactor: 0.10,
		Estimate:   EstimateUploadedPDFFields("file", "files", "pdf", "pdfs", "documents"),
	}
	ConvertOfficeToPDFWord = Tool{
		Name:       "word_to_pdf",
		Aliases:    []string{"docx_to_pdf", "doc_to_pdf"},
		Billable:   true,
		BaseUnits:  3,
		PageFactor: 0.35,
		Estimate:   EstimateUploadedDocuments("file", "files", "document", "documents", "source"),
	}
	ConvertOfficeToPDFExcel = Tool{
		Name:       "excel_to_pdf",
		Aliases:    []string{"xlsx_to_pdf"},
		Billable:   true,
		BaseUnits:  3,
		PageFactor: 0.35,
		Estimate:   EstimateUploadedDocuments("file", "files", "document", "documents", "source"),
	}
	ConvertOfficeToPDFPowerPoint = Tool{
		Name:       "powerpoint_to_pdf",
		Aliases:    []string{"pptx_to_pdf"},
		Billable:   true,
		BaseUnits:  3,
		PageFactor: 0.35,
		Estimate:   EstimateUploadedDocuments("file", "files", "document", "documents", "source"),
	}
	ConvertURLToPDF = Tool{
		Name:      "url_to_pdf",
		Aliases:   []string{"website_to_pdf", "webpage_to_pdf"},
		Billable:  true,
		BaseUnits: 4,
		Estimate:  EstimateNone(),
	}
	ConvertMarkdownToPDF = Tool{
		Name:      "markdown_to_pdf",
		Aliases:   []string{"markdown_to_pdf_async"},
		Billable:  true,
		BaseUnits: 3,
		Estimate:  EstimateNone(),
	}
	ConvertCodeToPDF = Tool{
		Name:      "code_to_pdf",
		Aliases:   []string{},
		Billable:  true,
		BaseUnits: 3,
		Estimate:  EstimateNone(),
	}
	ConvertPDFToWord = Tool{
		Name:       "pdf_to_word",
		Aliases:    []string{"pdf_to_docx"},
		Billable:   true,
		BaseUnits:  3,
		PageFactor: 0.10,
		Estimate:   EstimateUploadedPDFFields("file", "files", "pdf", "pdfs", "documents"),
	}
	ConvertPDFToExcel = Tool{
		Name:       "pdf_to_excel",
		Aliases:    []string{"pdf_to_xlsx"},
		Billable:   true,
		BaseUnits:  3,
		PageFactor: 0.10,
		Estimate:   EstimateUploadedPDFFields("file", "files", "pdf", "pdfs", "documents"),
	}
	ConvertPDFToPowerPoint = Tool{
		Name:       "pdf_to_powerpoint",
		Aliases:    []string{"pdf_to_pptx"},
		Billable:   true,
		BaseUnits:  3,
		PageFactor: 0.10,
		Estimate:   EstimateUploadedPDFFields("file", "files", "pdf", "pdfs", "documents"),
	}
	HTMLToPDF = Tool{
		Name:      "html_to_pdf",
		Aliases:   []string{"html_to_pdf_async"},
		Billable:  true,
		BaseUnits: 4,
		Estimate:  EstimateNone(),
	}

	// Edit
	EditExtract = Tool{
		Name:       "edit_extract",
		Aliases:    []string{"extract_html", "edit_extract_html"},
		Billable:   true,
		BaseUnits:  3,
		PageFactor: 0.10,
		Estimate:   EstimateUploadedPDFFields("file", "files", "pdf", "pdfs", "documents"),
	}
	EditCompile = Tool{
		Name:       "edit_compile",
		Aliases:    []string{"compile_pdf", "edit_compile_pdf"},
		Billable:   true,
		BaseUnits:  2,
		PageFactor: 0.05,
		Estimate:   EstimateSourceTrackerFromBody("source_tracker"),
	}

	// Optimize
	CompressPDF = Tool{
		Name:       "compress",
		Aliases:    []string{"optimize_compress"},
		Billable:   true,
		BaseUnits:  2,
		PageFactor: 0.20,
		Estimate:   EstimateUploadedPDFFields("file", "files", "pdf", "pdfs", "documents"),
	}
	GrayscalePDF = Tool{
		Name:       "grayscale",
		Aliases:    []string{"optimize_grayscale"},
		Billable:   true,
		BaseUnits:  1,
		PageFactor: 0.10,
		Estimate:   EstimateUploadedPDFFields("file", "files", "pdf", "pdfs", "documents"),
	}

	// Security
	LockPDF = Tool{
		Name:       "lock",
		Aliases:    []string{"security_lock"},
		Billable:   true,
		BaseUnits:  1,
		PageFactor: 0.05,
		Estimate:   EstimateUploadedPDFFields("file", "files", "pdf", "pdfs", "documents"),
	}
	UnlockPDF = Tool{
		Name:       "unlock",
		Aliases:    []string{"security_unlock"},
		Billable:   true,
		BaseUnits:  1,
		PageFactor: 0.05,
		Estimate:   EstimateUploadedPDFFields("file", "files", "pdf", "pdfs", "documents"),
	}
	RedactTextPDF = Tool{
		Name:       "redact_text",
		Aliases:    []string{"security_redact_text"},
		Billable:   true,
		BaseUnits:  4,
		PageFactor: 0.20,
		Estimate:   EstimateUploadedPDFFields("file", "files", "pdf", "pdfs", "documents"),
	}

	// Structure
	AnalyzeStructure = FreeTool("structure_analyze", "analyze")
	PreviewPage      = FreeTool("preview_page", "conversion_preview_page")
	MergePDF         = Tool{
		Name:       "merge",
		Aliases:    []string{"merge_pdf"},
		Billable:   true,
		BaseUnits:  2,
		PageFactor: 0.18,
		Estimate:   EstimateUploadedPDFFields("files", "file", "pdfs", "documents", "inputs"),
	}
	SplitPDF = Tool{
		Name:       "split",
		Aliases:    []string{"split_pdf"},
		Billable:   true,
		BaseUnits:  2,
		PageFactor: 0.12,
		Estimate:   EstimateUploadedPDFFields("file", "files", "pdf", "pdfs", "documents"),
	}
	RotatePDF = Tool{
		Name:       "rotate",
		Aliases:    []string{"rotate_pdf"},
		Billable:   true,
		BaseUnits:  1,
		PageFactor: 0.02,
		Estimate:   EstimateUploadedPDFFields("file", "files", "pdf", "pdfs", "documents"),
	}
	DeletePages = Tool{
		Name:       "delete_pages",
		Aliases:    []string{"delete", "structure_delete_pages"},
		Billable:   true,
		BaseUnits:  1,
		PageFactor: 0.08,
		Estimate:   EstimateSelectedPages("pages"),
	}
	ReorderPages = Tool{
		Name:       "reorder_pages",
		Aliases:    []string{"reorder", "structure_reorder_pages"},
		Billable:   true,
		BaseUnits:  1,
		PageFactor: 0.10,
		Estimate:   EstimateUploadedPDFFields("file", "files", "pdf", "pdfs", "documents"),
	}
	WatermarkPDF = Tool{
		Name:       "watermark",
		Aliases:    []string{"structure_watermark"},
		Billable:   true,
		BaseUnits:  2,
		PageFactor: 0.15,
		Estimate:   EstimateUploadedPDFFields("file", "files", "pdf", "pdfs", "documents"),
	}
	AddPageNumbers = Tool{
		Name:       "add_page_numbers",
		Aliases:    []string{"structure_add_page_numbers"},
		Billable:   true,
		BaseUnits:  2,
		PageFactor: 0.12,
		Estimate:   EstimateUploadedPDFFields("file", "files", "pdf", "pdfs", "documents"),
	}
	UpdateMetadata = Tool{
		Name:       "update_metadata",
		Aliases:    []string{"metadata_update"},
		Billable:   true,
		BaseUnits:  1,
		PageFactor: 0.02,
		Estimate:   EstimateUploadedPDFFields("file", "files", "pdf", "pdfs", "documents"),
	}
	FetchMetadata = FreeTool("fetch_metadata", "metadata_fetch")
	RepairPDF     = Tool{
		Name:       "repair",
		Aliases:    []string{"structure_repair"},
		Billable:   true,
		BaseUnits:  3,
		PageFactor: 0.05,
		Estimate:   EstimateUploadedPDFFields("file", "files", "pdf", "pdfs", "documents"),
	}
	SignPDF = Tool{
		Name:       "sign",
		Aliases:    []string{"structure_sign"},
		Billable:   true,
		BaseUnits:  2,
		PageFactor: 0.05,
		Estimate:   EstimateUploadedPDFFields("file", "files", "pdf", "pdfs", "documents"),
	}
	CropPDF = Tool{
		Name:       "crop",
		Aliases:    []string{"structure_crop"},
		Billable:   true,
		BaseUnits:  2,
		PageFactor: 0.12,
		Estimate:   EstimateUploadedPDFFields("file", "files", "pdf", "pdfs", "documents"),
	}
	DuplicatePDF = Tool{
		Name:       "duplicate",
		Aliases:    []string{"structure_duplicate"},
		Billable:   true,
		BaseUnits:  2,
		PageFactor: 0.08,
		Estimate:   EstimateUploadedPDFFields("file", "files", "pdf", "pdfs", "documents"),
	}
	InsertBlankPDF = Tool{
		Name:       "insert_blank",
		Aliases:    []string{"structure_insert_blank"},
		Billable:   true,
		BaseUnits:  1,
		PageFactor: 0.02,
		Estimate:   EstimateUploadedPDFFields("file", "files", "pdf", "pdfs", "documents"),
	}
	AddTextPDF = Tool{
		Name:       "add_text",
		Aliases:    []string{"structure_add_text"},
		Billable:   true,
		BaseUnits:  2,
		PageFactor: 0.15,
		Estimate:   EstimateUploadedPDFFields("file", "files", "pdf", "pdfs", "documents"),
	}
	HighlightPDF = Tool{
		Name:       "highlight",
		Aliases:    []string{"structure_highlight"},
		Billable:   true,
		BaseUnits:  4,
		PageFactor: 0.20,
		Estimate:   EstimateOCRMarkupPDF("boxes"),
	}

	StrikeoutPDF = Tool{
		Name:       "strikeout",
		Aliases:    []string{"structure_strikeout"},
		Billable:   true,
		BaseUnits:  4,
		PageFactor: 0.20,
		Estimate:   EstimateOCRMarkupPDF("boxes"),
	}

	UnderlinePDF = Tool{
		Name:       "underline",
		Aliases:    []string{"structure_underline"},
		Billable:   true,
		BaseUnits:  4,
		PageFactor: 0.20,
		Estimate:   EstimateOCRMarkupPDF("boxes"),
	}

	// OCR / edit tools already wired separately or used by async workers.
	PDFEditExtract = Tool{
		Name:       "pdf_edit_extract",
		Aliases:    []string{"edit_extract"},
		Billable:   true,
		BaseUnits:  3,
		PageFactor: 0.10,
		Estimate:   EstimateUploadedPDFFields("file", "files", "pdf", "pdfs", "documents"),
	}
	PDFEditCompile = Tool{
		Name:       "pdf_edit_compile",
		Aliases:    []string{"edit_compile"},
		Billable:   true,
		BaseUnits:  2,
		PageFactor: 0.05,
		Estimate:   EstimateSourceTrackerFromBody("source_tracker"),
	}
)

var Registry = map[string]Tool{}

func registerTool(t Tool) {
	Registry[normalizeKey(t.Name)] = t
	for _, alias := range t.Aliases {
		Registry[normalizeKey(alias)] = t
	}
}

func init() {
	for _, t := range []Tool{
		ExtractTextPDF,
		ImageToTextPDF,

		ConvertImagesToPDF,
		ConvertCustomImagesToPDF,
		RasterizePDFUniversal,
		ConvertOfficeToPDFWord,
		ConvertOfficeToPDFExcel,
		ConvertOfficeToPDFPowerPoint,
		ConvertURLToPDF,
		ConvertMarkdownToPDF,
		ConvertCodeToPDF,
		ConvertPDFToWord,
		ConvertPDFToExcel,
		ConvertPDFToPowerPoint,
		HTMLToPDF,

		EditExtract,
		EditCompile,

		CompressPDF,
		GrayscalePDF,

		LockPDF,
		UnlockPDF,
		RedactTextPDF,

		AnalyzeStructure,
		PreviewPage,
		MergePDF,
		SplitPDF,
		RotatePDF,
		DeletePages,
		ReorderPages,
		WatermarkPDF,
		AddPageNumbers,
		UpdateMetadata,
		FetchMetadata,
		RepairPDF,
		SignPDF,
		CropPDF,
		DuplicatePDF,
		InsertBlankPDF,
		AddTextPDF,
		HighlightPDF,
		StrikeoutPDF,
		UnderlinePDF,

		PDFEditExtract,
		PDFEditCompile,
	} {
		registerTool(t)
	}
}

func Lookup(name string) (Tool, bool) {
	t, ok := Registry[normalizeKey(name)]
	return t, ok
}

func CountUploadedPDFPagesFromFields(c *fiber.Ctx, fields ...string) int {
	form, err := c.MultipartForm()
	if err != nil || form == nil {
		return 0
	}

	total := 0
	for _, field := range fields {
		for _, fh := range form.File[field] {
			total += countPDFPagesForHeader(c, fh)
		}
	}
	return total
}

func CountUploadedImagesFromFields(c *fiber.Ctx, fields ...string) int {
	form, err := c.MultipartForm()
	if err != nil || form == nil {
		return 0
	}

	total := 0
	for _, field := range fields {
		total += len(form.File[field])
	}
	return total
}

func CountUploadedFilesFromFields(c *fiber.Ctx, fields ...string) int {
	form, err := c.MultipartForm()
	if err != nil || form == nil {
		return 0
	}

	total := 0
	for _, field := range fields {
		total += len(form.File[field])
	}
	return total
}

func countPDFPagesForHeader(c *fiber.Ctx, fh *multipart.FileHeader) int {
	tempDir := os.TempDir()
	tmp := filepath.Join(tempDir, uuid.New().String()+"-"+filepath.Base(fh.Filename))
	if err := c.SaveFile(fh, tmp); err != nil {
		return 0
	}
	defer func() { _ = os.Remove(tmp) }()

	pages, err := api.PageCountFile(tmp)
	if err != nil || pages <= 0 {
		return 0
	}
	return pages
}

func EstimateOCRMarkupPDF(boxField string) Estimator {
	return func(c *fiber.Ctx) (int, int, error) {
		// Base PDF pages always count.
		pages := CountUploadedPDFPages(c, "file")

		// If mode is not OCR, there is no OCR surcharge.
		mode := strings.ToLower(strings.TrimSpace(c.FormValue("mode")))
		if mode != "ocr" {
			return pages, 0, nil
		}

		// OCR surcharge depends on how many unique pages contain boxes.
		raw := c.FormValue(boxField)
		if raw == "" {
			return pages, 0, nil
		}

		var boxes []ocrBoxPage
		if err := json.Unmarshal([]byte(raw), &boxes); err != nil {
			return 0, 0, err
		}

		seen := make(map[int]struct{})
		for _, box := range boxes {
			if box.Page > 0 {
				seen[box.Page] = struct{}{}
			}
		}

		// Treat each unique page touched by OCR as extra work.
		return pages + len(seen), 0, nil
	}
}

func atoiSafe(s string) (int, error) {
	var v int
	_, err := fmt.Sscanf(s, "%d", &v)
	return v, err
}

func normalizeMarkupMode(mode string) string {
	return strings.ToLower(strings.TrimSpace(mode))
}

func selectMarkupBilling(
	mode string,
	smart Tool,
	text Tool,
	custom Tool,
	ocr Tool,
	ocrPages int,
) (Tool, int) {
	switch normalizeMarkupMode(mode) {
	case "ocr":
		return ocr, ocrPages
	case "text":
		return text, 0
	case "custom":
		return custom, 0
	default:
		return smart, 0
	}
}

var (
	HighlightSmartPDF = Tool{
		Name:      "highlight_smart",
		Billable:  true,
		BaseUnits: 4,
		Estimate:  EstimateNone(),
	}
	HighlightTextPDF = Tool{
		Name:      "highlight_text",
		Billable:  true,
		BaseUnits: 3,
		Estimate:  EstimateNone(),
	}
	HighlightCustomPDF = Tool{
		Name:      "highlight_custom",
		Billable:  true,
		BaseUnits: 5,
		Estimate:  EstimateNone(),
	}
	HighlightOCRPDF = Tool{
		Name:       "highlight_ocr",
		Billable:   true,
		BaseUnits:  6,
		PageFactor: 1.0,
		Estimate:   EstimateNone(),
	}

	StrikeoutSmartPDF = Tool{
		Name:      "strikeout_smart",
		Billable:  true,
		BaseUnits: 4,
		Estimate:  EstimateNone(),
	}
	StrikeoutTextPDF = Tool{
		Name:      "strikeout_text",
		Billable:  true,
		BaseUnits: 3,
		Estimate:  EstimateNone(),
	}
	StrikeoutCustomPDF = Tool{
		Name:      "strikeout_custom",
		Billable:  true,
		BaseUnits: 5,
		Estimate:  EstimateNone(),
	}
	StrikeoutOCRPDF = Tool{
		Name:       "strikeout_ocr",
		Billable:   true,
		BaseUnits:  6,
		PageFactor: 1.0,
		Estimate:   EstimateNone(),
	}

	UnderlineSmartPDF = Tool{
		Name:      "underline_smart",
		Billable:  true,
		BaseUnits: 4,
		Estimate:  EstimateNone(),
	}
	UnderlineTextPDF = Tool{
		Name:      "underline_text",
		Billable:  true,
		BaseUnits: 3,
		Estimate:  EstimateNone(),
	}
	UnderlineCustomPDF = Tool{
		Name:      "underline_custom",
		Billable:  true,
		BaseUnits: 5,
		Estimate:  EstimateNone(),
	}
	UnderlineOCRPDF = Tool{
		Name:       "underline_ocr",
		Billable:   true,
		BaseUnits:  6,
		PageFactor: 1.0,
		Estimate:   EstimateNone(),
	}
)

func SelectHighlightBilling(mode string, ocrPages int) (Tool, int) {
	return selectMarkupBilling(
		mode,
		HighlightSmartPDF,
		HighlightTextPDF,
		HighlightCustomPDF,
		HighlightOCRPDF,
		ocrPages,
	)
}

func SelectStrikeoutBilling(mode string, ocrPages int) (Tool, int) {
	return selectMarkupBilling(
		mode,
		StrikeoutSmartPDF,
		StrikeoutTextPDF,
		StrikeoutCustomPDF,
		StrikeoutOCRPDF,
		ocrPages,
	)
}

func SelectUnderlineBilling(mode string, ocrPages int) (Tool, int) {
	return selectMarkupBilling(
		mode,
		UnderlineSmartPDF,
		UnderlineTextPDF,
		UnderlineCustomPDF,
		UnderlineOCRPDF,
		ocrPages,
	)
}
