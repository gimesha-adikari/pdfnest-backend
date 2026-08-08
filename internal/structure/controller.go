package structure

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"pdfnest-backend/config"
	"pdfnest-backend/internal/uploads"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type Controller struct {
	service Service
}

func NewController(s Service) *Controller {
	return &Controller{service: s}
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (ctrl *Controller) Merge(c *fiber.Ctx) error {
	ctx := uploads.FromCtx(c)
	var inputPaths []string

	if ctx != nil {
		for _, field := range []string{"files", "file", "pdfs", "documents", "inputs"} {
			for _, f := range ctx.All(field) {
				if f != nil && f.Path != "" {
					inputPaths = append(inputPaths, f.Path)
				}
			}
		}
	}

	if len(inputPaths) < 2 {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "INSUFFICIENT_FILES",
			Message: "At least two PDF files are required to execute a merge operation.",
		})
	}

	for _, path := range inputPaths {
		if _, err := uploads.CheckPDFPageLimit(path, "MAX_PAGES_GENERAL", 1000); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(APIError{
				Code:    "PAGE_LIMIT_EXCEEDED",
				Message: err.Error(),
			})
		}
	}

	outputPath, err := ctrl.service.MergePDFs(inputPaths)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "COMPILATION_ENGINE_FAILED",
			Message: "Merge execution pipeline failure: " + err.Error(),
		})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment("merged_document.pdf")
	err = c.SendFile(outputPath)

	if cleanupErr := os.Remove(outputPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
		log.Printf("[CLEANUP WARNING] Merge: Failed to delete output PDF at %s: %v", outputPath, cleanupErr)
	}

	return err
}

func (ctrl *Controller) Split(c *fiber.Ctx) error {
	pagesRaw := c.FormValue("pages")
	if pagesRaw == "" {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_PAGE_SELECTION",
			Message: "Page selection parameters are required for extraction.",
		})
	}

	upload, err := uploads.MustPDFFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing source PDF document file parameter.",
		})
	}

	if _, err := uploads.CheckPDFPageLimit(upload.Path, "MAX_PAGES_GENERAL", 1000); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "PAGE_LIMIT_EXCEEDED",
			Message: err.Error(),
		})
	}

	pageSelection := strings.Split(pagesRaw, ",")
	for i, v := range pageSelection {
		pageSelection[i] = strings.TrimSpace(v)
	}

	outputPath, err := ctrl.service.SplitPDF(upload.Path, pageSelection)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "EXTRACTION_ENGINE_FAILED",
			Message: "Extraction routine failure or invalid page index syntax: " + err.Error(),
		})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment("split_document.pdf")
	err = c.SendFile(outputPath)

	if cleanupErr := os.Remove(outputPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
		log.Printf("[CLEANUP WARNING] Split: Failed to delete output split file at %s: %v", outputPath, cleanupErr)
	}

	return err
}

func (ctrl *Controller) Rotate(c *fiber.Ctx) error {
	rotationsRaw := c.FormValue("rotations")
	if rotationsRaw == "" {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_ROTATION_CONFIG",
			Message: "Rotation metric mapping layout configuration is required.",
		})
	}

	var rotations map[string]int
	if err := json.Unmarshal([]byte(rotationsRaw), &rotations); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MALFORMED_JSON_MATRIX",
			Message: "Invalid rotation matrix data layout structure mapping.",
		})
	}

	upload, err := uploads.MustPDFFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing target PDF context file vector.",
		})
	}

	if _, err := uploads.CheckPDFPageLimit(upload.Path, "MAX_PAGES_GENERAL", 1000); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "PAGE_LIMIT_EXCEEDED",
			Message: err.Error(),
		})
	}

	outputPath, err := ctrl.service.RotatePDF(upload.Path, rotations)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "ROTATION_ENGINE_FAILED",
			Message: "Rotation transformation engine crash outcome: " + err.Error(),
		})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment("rotated_document.pdf")
	err = c.SendFile(outputPath)

	if cleanupErr := os.Remove(outputPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
		log.Printf("[CLEANUP WARNING] Rotate: Failed to delete output file at %s: %v", outputPath, cleanupErr)
	}

	return err
}

func (ctrl *Controller) DeletePages(c *fiber.Ctx) error {
	pagesRaw := c.FormValue("pages")
	if pagesRaw == "" {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_PAGE_METADATA",
			Message: "Target indexes chosen for removal operations must be populated.",
		})
	}

	upload, err := uploads.MustPDFFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing target PDF payload context asset.",
		})
	}

	if _, err := uploads.CheckPDFPageLimit(upload.Path, "MAX_PAGES_GENERAL", 1000); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "PAGE_LIMIT_EXCEEDED",
			Message: err.Error(),
		})
	}

	pagesToDelete := strings.Split(pagesRaw, ",")
	for i, v := range pagesToDelete {
		pagesToDelete[i] = strings.TrimSpace(v)
	}

	outputPath, err := ctrl.service.DeletePDFPages(upload.Path, pagesToDelete)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "DELETION_ENGINE_FAILED",
			Message: "Deletion pipeline failure or page index boundary violation: " + err.Error(),
		})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment("modified_document.pdf")
	err = c.SendFile(outputPath)

	if cleanupErr := os.Remove(outputPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
		log.Printf("[CLEANUP WARNING] DeletePages: Failed to delete output file at %s: %v", outputPath, cleanupErr)
	}

	return err
}

func (ctrl *Controller) ReorderPages(c *fiber.Ctx) error {
	sequenceRaw := c.FormValue("sequence")
	if sequenceRaw == "" {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_SEQUENCE_MAP",
			Message: "Structural sequencing mapping configuration targets cannot be blank.",
		})
	}

	upload, err := uploads.MustPDFFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Required tracking document stream missing.",
		})
	}

	if _, err := uploads.CheckPDFPageLimit(upload.Path, "MAX_PAGES_GENERAL", 1000); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "PAGE_LIMIT_EXCEEDED",
			Message: err.Error(),
		})
	}

	sequence := strings.Split(sequenceRaw, ",")
	for i, v := range sequence {
		sequence[i] = strings.TrimSpace(v)
	}

	outputPath, err := ctrl.service.ReorderPDFPages(upload.Path, sequence)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "REORDER_ENGINE_FAILED",
			Message: "Reordering sequence transaction failed: " + err.Error(),
		})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment("reordered_document.pdf")
	err = c.SendFile(outputPath)

	if cleanupErr := os.Remove(outputPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
		log.Printf("[CLEANUP WARNING] ReorderPages: Failed to delete output file at %s: %v", outputPath, cleanupErr)
	}

	return err
}

func (ctrl *Controller) Watermark(c *fiber.Ctx) error {
	upload, err := uploads.MustPDFFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing baseline target PDF configuration document.",
		})
	}

	if _, err := uploads.CheckPDFPageLimit(upload.Path, "MAX_PAGES_GENERAL", 1000); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "PAGE_LIMIT_EXCEEDED",
			Message: err.Error(),
		})
	}

	text := c.FormValue("text")
	description := c.FormValue("description")

	var imagePath string
	imgFile, err := uploads.MustFile(c, "watermarkImage")
	if err == nil && imgFile != nil && imgFile.Path != "" {
		imagePath = imgFile.Path
	}

	outputPath, err := ctrl.service.WatermarkPDF(upload.Path, text, imagePath, description)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "WATERMARK_ENGINE_FAILED",
			Message: "Watermark creation pipeline run error: " + err.Error(),
		})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment("watermarked_document.pdf")
	err = c.SendFile(outputPath)

	if cleanupErr := os.Remove(outputPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
		log.Printf("[CLEANUP WARNING] Watermark: Failed to delete output file at %s: %v", outputPath, cleanupErr)
	}

	return err
}

func (ctrl *Controller) AddPageNumbers(c *fiber.Ctx) error {
	description := c.FormValue("description")
	if description == "" {
		description = "font:Helvetica, pos:bc, scale:12 abs"
	}

	upload, err := uploads.MustPDFFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing core target workspace file reference.",
		})
	}

	if _, err := uploads.CheckPDFPageLimit(upload.Path, "MAX_PAGES_GENERAL", 1000); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "PAGE_LIMIT_EXCEEDED",
			Message: err.Error(),
		})
	}

	outputPath, err := ctrl.service.AddPageNumbersPDF(upload.Path, description)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "PAGINATION_ENGINE_FAILED",
			Message: "Page numbering layer insertion failure: " + err.Error(),
		})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment("paginated_document.pdf")
	err = c.SendFile(outputPath)

	if cleanupErr := os.Remove(outputPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
		log.Printf("[CLEANUP WARNING] AddPageNumbers: Failed to delete output file at %s: %v", outputPath, cleanupErr)
	}

	return err
}

func (ctrl *Controller) UpdateMetadata(c *fiber.Ctx) error {
	upload, err := uploads.MustPDFFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Core target configuration file container artifact is missing.",
		})
	}

	if _, err := uploads.CheckPDFPageLimit(upload.Path, "MAX_PAGES_GENERAL", 1000); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "PAGE_LIMIT_EXCEEDED",
			Message: err.Error(),
		})
	}

	password := c.FormValue("password")

	metadata := make(map[string]string)
	if title := c.FormValue("title"); title != "" {
		metadata["Title"] = title
	}
	if author := c.FormValue("author"); author != "" {
		metadata["Author"] = author
	}
	if subject := c.FormValue("subject"); subject != "" {
		metadata["Subject"] = subject
	}
	if keywords := c.FormValue("keywords"); keywords != "" {
		metadata["Keywords"] = keywords
	}

	outputPath, err := ctrl.service.UpdateMetadataPDF(upload.Path, metadata, password)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "METADATA_ENGINE_FAILED",
			Message: "Metadata catalog reconstruction matrix execution error: " + err.Error(),
		})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment("updated_metadata_document.pdf")
	err = c.SendFile(outputPath)

	if cleanupErr := os.Remove(outputPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
		log.Printf("[CLEANUP WARNING] UpdateMetadata: Failed to delete output file at %s: %v", outputPath, cleanupErr)
	}

	return err
}

func (ctrl *Controller) FetchMetadata(c *fiber.Ctx) error {
	upload, err := uploads.MustPDFFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing target PDF document structure.",
		})
	}

	password := c.FormValue("password")

	properties, err := ctrl.service.GetMetadataPDF(upload.Path, password)
	if err != nil {
		log.Printf("[METADATA ERROR] %v", err)
		return c.Status(fiber.StatusUnauthorized).JSON(APIError{
			Code:    "DECRYPTION_METADATA_FAILED",
			Message: err.Error(),
		})
	}

	return c.JSON(properties)
}

func (ctrl *Controller) Repair(c *fiber.Ctx) error {
	upload, err := uploads.MustPDFFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Missing target PDF document file parameter.",
		})
	}

	if _, err := uploads.CheckPDFPageLimit(upload.Path, "MAX_PAGES_GENERAL", 1000); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	tempDir := os.TempDir()
	sessionID := uuid.New().String()
	outputPath := filepath.Join(tempDir, sessionID+"-repaired-"+filepath.Base(upload.Header.Filename))

	defer func() {
		os.Remove(outputPath)
	}()

	if err := RepairPdf(upload.Path, outputPath); err != nil {
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{
			"error": "File is too severely corrupted to repair dynamically.",
		})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment("repaired_" + filepath.Base(upload.Header.Filename))

	return c.SendFile(outputPath)
}

func (ctrl *Controller) Sign(c *fiber.Ctx) error {
	pdfFile, err := uploads.MustPDFFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Missing target PDF document."})
	}

	sigFile, err := uploads.MustFile(c, "signature")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Missing signature image data."})
	}

	if _, err := uploads.CheckPDFPageLimit(pdfFile.Path, "MAX_PAGES_GENERAL", 1000); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	stampsJson := c.FormValue("stamps", "[]")

	tempDir := os.TempDir()
	sessionID := uuid.New().String()
	outputPath := filepath.Join(tempDir, sessionID+"-signed-"+filepath.Base(pdfFile.Header.Filename))

	defer func() {
		os.Remove(outputPath)
	}()

	if err := SignPdfMulti(pdfFile.Path, sigFile.Path, outputPath, stampsJson); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Signature stamping failed: " + err.Error()})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment("signed_" + filepath.Base(pdfFile.Header.Filename))

	return c.SendFile(outputPath)
}

func parseSelectedPages(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (ctrl *Controller) Crop(c *fiber.Ctx) error {
	cropBoxDesc := c.FormValue("box")
	if cropBoxDesc == "" {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_CROP_DIMENSIONS",
			Message: "A target crop box boundary dimension map is required.",
		})
	}

	selectedPages := parseSelectedPages(c.FormValue("pages"))

	upload, err := uploads.MustPDFFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing source PDF context file vector.",
		})
	}

	if _, err := uploads.CheckPDFPageLimit(upload.Path, "MAX_PAGES_GENERAL", 1000); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "PAGE_LIMIT_EXCEEDED",
			Message: err.Error(),
		})
	}

	outputPath, err := ctrl.service.CropPDF(upload.Path, cropBoxDesc, selectedPages)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "CROPPING_ENGINE_FAILED",
			Message: "Crop transaction engine boundary processing failure: " + err.Error(),
		})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment("cropped_document.pdf")

	err = c.SendFile(outputPath)

	if cleanupErr := os.Remove(outputPath); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
		log.Printf("[CLEANUP WARNING] Crop: Failed to delete output split file at %s: %v", outputPath, cleanupErr)
	}

	return err
}

func (ctrl *Controller) Duplicate(c *fiber.Ctx) error {
	var userID = c.Locals("user_id").(string)

	pageSelection := c.FormValue("pages")
	if pageSelection == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"code":    "MISSING_PAGE_SELECTION",
			"message": "Target page descriptions are required for duplication matrix processing.",
		})
	}

	copiesStr := c.FormValue("copies")
	copies, err := strconv.Atoi(copiesStr)
	if err != nil || copies < 1 {
		copies = 1
	}

	maxPages := 5
	maxCopies := 2

	var sub config.Subscription

	if err := config.DB.
		Where("user_id = ? AND status = ?", userID, "active").
		First(&sub).Error; err == nil {

		if sub.Tier == "pro" {
			maxPages = 50
			maxCopies = 10
		}
	}

	selectedPages := 0

	for _, part := range strings.Split(pageSelection, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		if strings.Contains(part, "-") {
			r := strings.Split(part, "-")
			if len(r) != 2 {
				continue
			}

			start, err1 := strconv.Atoi(strings.TrimSpace(r[0]))
			end, err2 := strconv.Atoi(strings.TrimSpace(r[1]))

			if err1 == nil && err2 == nil && end >= start {
				selectedPages += end - start + 1
			}
		} else {
			if _, err := strconv.Atoi(part); err == nil {
				selectedPages++
			}
		}
	}

	if selectedPages > maxPages {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"code": "PAGE_LIMIT_EXCEEDED",
			"message": fmt.Sprintf(
				"Your subscription allows duplicating a maximum of %d pages at one time.",
				maxPages,
			),
		})
	}

	if copies > maxCopies {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"code": "COPY_LIMIT_EXCEEDED",
			"message": fmt.Sprintf(
				"Your subscription allows a maximum of %d copies per page.",
				maxCopies,
			),
		})
	}

	upload, err := uploads.MustPDFFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"code":    "MISSING_UPLOAD_FILE",
			"message": "Missing input context PDF source vector.",
		})
	}

	if _, err := uploads.CheckPDFPageLimit(upload.Path, "MAX_PAGES_GENERAL", 1000); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"code":    "PAGE_LIMIT_EXCEEDED",
			"message": err.Error(),
		})
	}

	outputPath, err := ctrl.service.DuplicatePDFPages(upload.Path, pageSelection, copies)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "DUPLICATION_ENGINE_FAILED",
			Message: "Page matrix layout rendering transaction failed: " + err.Error(),
		})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment(fmt.Sprintf("%s-duplicated.pdf", strings.TrimSuffix(upload.Header.Filename, filepath.Ext(upload.Header.Filename))))

	sendErr := c.SendFile(outputPath)

	defer func() {
		_ = os.Remove(outputPath)
	}()

	return sendErr
}

func (ctrl *Controller) InsertBlank(c *fiber.Ctx) error {
	insertAt := c.FormValue("insertAt")

	targetPage := 1
	if insertAt == "after" {
		var err error
		targetPage, err = strconv.Atoi(c.FormValue("targetPage"))
		if err != nil || targetPage < 1 {
			targetPage = 1
		}
	}

	count, err := strconv.Atoi(c.FormValue("count"))
	if err != nil || count < 1 {
		count = 1
	}

	if count > 10 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"code":    "BLANK_PAGE_LIMIT_EXCEEDED",
			"message": "A maximum of 10 blank pages can be inserted in a single operation.",
		})
	}

	upload, err := uploads.MustPDFFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"code":    "MISSING_UPLOAD_FILE",
			"message": "Missing input context PDF source vector.",
		})
	}

	if _, err := uploads.CheckPDFPageLimit(upload.Path, "MAX_PAGES_GENERAL", 1000); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"code":    "PAGE_LIMIT_EXCEEDED",
			"message": err.Error(),
		})
	}

	outputPath, err := ctrl.service.InsertBlankPages(upload.Path, insertAt, targetPage, count)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "INSERTION_ENGINE_FAILED",
			Message: "Blank page insert rendering transaction failed: " + err.Error(),
		})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment(fmt.Sprintf("%s-with-blank.pdf", strings.TrimSuffix(upload.Header.Filename, filepath.Ext(upload.Header.Filename))))

	sendErr := c.SendFile(outputPath)

	defer func() {
		_ = os.Remove(outputPath)
	}()

	return sendErr
}
