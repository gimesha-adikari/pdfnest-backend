package structure

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"pdfnest-backend/config"
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

	form, err := c.MultipartForm()
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "INVALID_MULTIPART_FORM",
			Message: "Invalid multipart form transmission.",
		})
	}

	files := form.File["files"]
	if len(files) < 2 {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "INSUFFICIENT_FILES",
			Message: "At least two PDF files are required to execute a merge operation.",
		})
	}

	tempDir := os.TempDir()
	var inputPaths []string

	defer func() {
		for _, path := range inputPaths {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				log.Printf("[CLEANUP WARNING] Merge: Failed to delete temporary input file at %s: %v", path, err)
			}
		}
	}()

	for _, fileHeader := range files {
		inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+filepath.Base(fileHeader.Filename))
		if err := c.SaveFile(fileHeader, inputPath); err != nil {
			log.Printf("[SERVER ERROR] Merge: Failed to save multipart file to path %s: %v", inputPath, err)
			return c.Status(fiber.StatusInternalServerError).JSON(APIError{
				Code:    "DISK_WRITE_FAILURE",
				Message: "Failed to initialize staging area for file compilation.",
			})
		}
		inputPaths = append(inputPaths, inputPath)
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

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing source PDF document file parameter.",
		})
	}

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+filepath.Base(fileHeader.Filename))

	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		log.Printf("[SERVER ERROR] Split: Failed to save uploaded file %s: %v", inputPath, err)
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "DISK_WRITE_FAILURE",
			Message: "Failed to allocate scratch file parameters.",
		})
	}
	defer func() {
		if err := os.Remove(inputPath); err != nil && !os.IsNotExist(err) {
			log.Printf("[CLEANUP WARNING] Split: Failed to delete input file at %s: %v", inputPath, err)
		}
	}()

	pageSelection := strings.Split(pagesRaw, ",")
	for i, v := range pageSelection {
		pageSelection[i] = strings.TrimSpace(v)
	}

	outputPath, err := ctrl.service.SplitPDF(inputPath, pageSelection)
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

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing target PDF context file vector.",
		})
	}

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+filepath.Base(fileHeader.Filename))

	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		log.Printf("[SERVER ERROR] Rotate: Failed to save input file %s: %v", inputPath, err)
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "DISK_WRITE_FAILURE",
			Message: "Failed to commit operational asset to temporary block disk maps.",
		})
	}
	defer func() {
		if err := os.Remove(inputPath); err != nil && !os.IsNotExist(err) {
			log.Printf("[CLEANUP WARNING] Rotate: Failed to delete input file at %s: %v", inputPath, err)
		}
	}()

	outputPath, err := ctrl.service.RotatePDF(inputPath, rotations)
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

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing target PDF payload context asset.",
		})
	}

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+filepath.Base(fileHeader.Filename))

	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		log.Printf("[SERVER ERROR] DeletePages: Failed to save input file %s: %v", inputPath, err)
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "DISK_WRITE_FAILURE",
			Message: "Failed to isolate document file structure allocation paths.",
		})
	}
	defer func() {
		if err := os.Remove(inputPath); err != nil && !os.IsNotExist(err) {
			log.Printf("[CLEANUP WARNING] DeletePages: Failed to delete input file at %s: %v", inputPath, err)
		}
	}()

	pagesToDelete := strings.Split(pagesRaw, ",")
	for i, v := range pagesToDelete {
		pagesToDelete[i] = strings.TrimSpace(v)
	}

	outputPath, err := ctrl.service.DeletePDFPages(inputPath, pagesToDelete)
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

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Required tracking document stream missing.",
		})
	}

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+filepath.Base(fileHeader.Filename))

	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		log.Printf("[SERVER ERROR] ReorderPages: Failed to save input file %s: %v", inputPath, err)
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "DISK_WRITE_FAILURE",
			Message: "Could not write target asset structure down standard disk registers.",
		})
	}
	defer func() {
		if err := os.Remove(inputPath); err != nil && !os.IsNotExist(err) {
			log.Printf("[CLEANUP WARNING] ReorderPages: Failed to delete input file at %s: %v", inputPath, err)
		}
	}()

	sequence := strings.Split(sequenceRaw, ",")
	for i, v := range sequence {
		sequence[i] = strings.TrimSpace(v)
	}

	outputPath, err := ctrl.service.ReorderPDFPages(inputPath, sequence)
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

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing baseline target PDF configuration document.",
		})
	}

	text := c.FormValue("text")
	description := c.FormValue("description")

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+filepath.Base(fileHeader.Filename))
	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		log.Printf("[SERVER ERROR] Watermark: Failed to save source file %s: %v", inputPath, err)
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "DISK_WRITE_FAILURE",
			Message: "Failed to isolate document frame maps.",
		})
	}
	defer func() {
		if err := os.Remove(inputPath); err != nil && !os.IsNotExist(err) {
			log.Printf("[CLEANUP WARNING] Watermark: Failed to delete input file at %s: %v", inputPath, err)
		}
	}()

	var imagePath string
	imgHeader, err := c.FormFile("watermarkImage")
	if err == nil && imgHeader != nil {
		imagePath = filepath.Join(tempDir, uuid.New().String()+"-"+filepath.Base(imgHeader.Filename))
		if err := c.SaveFile(imgHeader, imagePath); err != nil {
			log.Printf("[SERVER ERROR] Watermark: Failed to save graphic asset %s: %v", imagePath, err)
			return c.Status(fiber.StatusInternalServerError).JSON(APIError{
				Code:    "GRAPHIC_WRITE_FAILURE",
				Message: "Failed to process attached structural watermark graphic overlay component.",
			})
		}
		defer func() {
			if err := os.Remove(imagePath); err != nil && !os.IsNotExist(err) {
				log.Printf("[CLEANUP WARNING] Watermark: Failed to delete watermark image asset at %s: %v", imagePath, err)
			}
		}()
	}

	outputPath, err := ctrl.service.WatermarkPDF(inputPath, text, imagePath, description)
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

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing core target workspace file reference.",
		})
	}

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+filepath.Base(fileHeader.Filename))
	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		log.Printf("[SERVER ERROR] AddPageNumbers: Failed to save uploaded input file %s: %v", inputPath, err)
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "DISK_WRITE_FAILURE",
			Message: "Failed to parse underlying document file handle arrays.",
		})
	}
	defer func() {
		if err := os.Remove(inputPath); err != nil && !os.IsNotExist(err) {
			log.Printf("[CLEANUP WARNING] AddPageNumbers: Failed to delete input file at %s: %v", inputPath, err)
		}
	}()

	outputPath, err := ctrl.service.AddPageNumbersPDF(inputPath, description)
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

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Core target configuration file container artifact is missing.",
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

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+filepath.Base(fileHeader.Filename))
	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		log.Printf("[SERVER ERROR] UpdateMetadata: Failed to save uploaded file %s: %v", inputPath, err)
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "DISK_WRITE_FAILURE",
			Message: "Failed to capture properties frame reference maps.",
		})
	}
	defer func() {
		if err := os.Remove(inputPath); err != nil && !os.IsNotExist(err) {
			log.Printf("[CLEANUP WARNING] UpdateMetadata: Failed to delete input file at %s: %v", inputPath, err)
		}
	}()

	outputPath, err := ctrl.service.UpdateMetadataPDF(inputPath, metadata, password)
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

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing target PDF document structure.",
		})
	}

	password := c.FormValue("password")

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+filepath.Base(fileHeader.Filename))
	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		log.Printf("[SERVER ERROR] FetchMetadata: Failed to save file: %v", err)
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "DISK_WRITE_FAILURE",
			Message: "Failed to isolate document meta headers.",
		})
	}
	defer func() {
		if err := os.Remove(inputPath); err != nil && !os.IsNotExist(err) {
			log.Printf("[CLEANUP WARNING] FetchMetadata: Failed to delete input file: %v", err)
		}
	}()

	properties, err := ctrl.service.GetMetadataPDF(inputPath, password)
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

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Missing target PDF document file parameter.",
		})
	}

	tempDir := os.TempDir()
	sessionID := uuid.New().String()
	inputPath := filepath.Join(tempDir, sessionID+"-corrupt-"+filepath.Base(fileHeader.Filename))
	outputPath := filepath.Join(tempDir, sessionID+"-repaired-"+filepath.Base(fileHeader.Filename))

	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to allocate local scratch workspace.",
		})
	}

	defer func() {
		os.Remove(inputPath)
		os.Remove(outputPath)
	}()

	if err := RepairPdf(inputPath, outputPath); err != nil {
		return c.Status(fiber.StatusUnprocessableEntity).JSON(fiber.Map{
			"error": "File is too severely corrupted to repair dynamically.",
		})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment("repaired_" + filepath.Base(fileHeader.Filename))

	return c.SendFile(outputPath)
}

func (ctrl *Controller) Sign(c *fiber.Ctx) error {

	pdfHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Missing target PDF document."})
	}

	sigHeader, err := c.FormFile("signature")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Missing signature image data."})
	}

	stampsJson := c.FormValue("stamps", "[]")

	tempDir := os.TempDir()
	sessionID := uuid.New().String()

	pdfInputPath := filepath.Join(tempDir, sessionID+"-doc-"+filepath.Base(pdfHeader.Filename))
	sigInputPath := filepath.Join(tempDir, sessionID+"-sig-"+filepath.Base(sigHeader.Filename))
	outputPath := filepath.Join(tempDir, sessionID+"-signed-"+filepath.Base(pdfHeader.Filename))

	if err := c.SaveFile(pdfHeader, pdfInputPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to save PDF workspace."})
	}
	if err := c.SaveFile(sigHeader, sigInputPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to save signature workspace."})
	}

	defer func() {
		os.Remove(pdfInputPath)
		os.Remove(sigInputPath)
		os.Remove(outputPath)
	}()

	if err := SignPdfMulti(pdfInputPath, sigInputPath, outputPath, stampsJson); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Signature stamping failed: " + err.Error()})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment("signed_" + filepath.Base(pdfHeader.Filename))

	return c.SendFile(outputPath)
}

func (ctrl *Controller) Crop(c *fiber.Ctx) error {

	cropBoxDesc := c.FormValue("box")
	if cropBoxDesc == "" {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_CROP_DIMENSIONS",
			Message: "A target crop box boundary dimension map is required.",
		})
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(APIError{
			Code:    "MISSING_UPLOAD_FILE",
			Message: "Missing source PDF context file vector.",
		})
	}

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+filepath.Base(fileHeader.Filename))

	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		log.Printf("[SERVER ERROR] Crop: Failed to save uploaded input file %s: %v", inputPath, err)
		return c.Status(fiber.StatusInternalServerError).JSON(APIError{
			Code:    "DISK_WRITE_FAILURE",
			Message: "Failed to isolate document file parameters into scratch space.",
		})
	}
	defer func() {
		if err := os.Remove(inputPath); err != nil && !os.IsNotExist(err) {
			log.Printf("[CLEANUP WARNING] Crop: Failed to delete input file at %s: %v", inputPath, err)
		}
	}()

	outputPath, err := ctrl.service.CropPDF(inputPath, cropBoxDesc)
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

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"code":    "MISSING_UPLOAD_FILE",
			"message": "Missing input context PDF source vector.",
		})
	}

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+filepath.Base(fileHeader.Filename))

	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"code":    "DISK_WRITE_FAILURE",
			"message": "Failed to store asset into intermediate local temporary storage bounds.",
		})
	}
	defer func() {
		_ = os.Remove(inputPath)
	}()

	if password := c.FormValue("file_password"); password != "" {
	}

	outputPath, err := ctrl.service.DuplicatePDFPages(inputPath, pageSelection, copies)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"code":    "DUPLICATION_ENGINE_FAILED",
			"message": "Page matrix layout rendering transaction failed: " + err.Error(),
		})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment(fmt.Sprintf("%s-duplicated.pdf", strings.TrimSuffix(fileHeader.Filename, filepath.Ext(fileHeader.Filename))))

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

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"code":    "MISSING_UPLOAD_FILE",
			"message": "Missing input context PDF source vector.",
		})
	}

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+filepath.Base(fileHeader.Filename))

	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"code":    "DISK_WRITE_FAILURE",
			"message": "Failed to store asset into temporary scratch bounds.",
		})
	}
	defer func() {
		_ = os.Remove(inputPath)
	}()

	outputPath, err := ctrl.service.InsertBlankPages(inputPath, insertAt, targetPage, count)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"code":    "INSERTION_ENGINE_FAILED",
			"message": "Blank page insert rendering transaction failed: " + err.Error(),
		})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment(fmt.Sprintf("%s-with-blank.pdf", filepath.Base(fileHeader.Filename)))

	sendErr := c.SendFile(outputPath)

	defer func() {
		_ = os.Remove(outputPath)
	}()

	return sendErr
}
