package uploads

import (
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"os"
	"path/filepath"
	"pdfnest-backend/internal/temp"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

func saveHeader(c *fiber.Ctx, fh *multipart.FileHeader) (string, error) {
	// Determine safe destination path inside the dedicated temp directory.
	dir := temp.GetDir()
	base := filepath.Base(fh.Filename)
	filename := fmt.Sprintf("pdfnest-upload-%s-%s", uuid.New().String(), base)
	destPath := filepath.Join(dir, filename)

	src, err := fh.Open()
	if err != nil {
		return "", err
	}
	defer src.Close()

	dst, err := os.Create(destPath)
	if err != nil {
		return "", err
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return "", err
	}
	if err := dst.Sync(); err != nil {
		return "", err
	}
	return destPath, nil
}

func Prepare() fiber.Handler {
	return func(c *fiber.Ctx) error {
		contentType := strings.ToLower(c.Get(fiber.HeaderContentType))

		if !strings.Contains(contentType, fiber.MIMEMultipartForm) {
			c.Locals(LocalKey, NewContext())
			return c.Next()
		}

		form, err := c.MultipartForm()
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"code":    "INVALID_MULTIPART_FORM",
				"message": "Invalid multipart form transmission.",
			})
		}

		ctx := NewContext()
		savedPaths := make([]string, 0, 4)

		cleanup := func() {
			for i := len(savedPaths) - 1; i >= 0; i-- {
				if rmErr := os.Remove(savedPaths[i]); rmErr != nil && !os.IsNotExist(rmErr) {
					log.Printf("[UPLOAD CLEANUP WARNING] Failed to delete %s: %v", savedPaths[i], rmErr)
				}
			}
		}

		for field, headers := range form.File {
			for _, fh := range headers {
				path, saveErr := saveHeader(c, fh)
				if saveErr != nil {
					cleanup()
					return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
						"code":    "UPLOAD_STAGE_FAILURE",
						"message": "Failed to stage uploaded file into workspace.",
					})
				}

				ctx.Add(field, fh, path)
				savedPaths = append(savedPaths, path)
			}
		}

		c.Locals(LocalKey, ctx)
		defer cleanup()

		return c.Next()
	}
}
