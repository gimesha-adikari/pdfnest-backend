package middleware

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"pdfnest-backend/config"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type billingRequest struct {
	ToolName string
	Pages    int
	Images   int
	Charge   bool
}

type billingReservation struct {
	Units     int
	CreditUse int
	ToolName  string
	WasCredit bool
}

type tierLimit struct {
	Units3H    int
	UnitsDay   int
	UnitsMonth int
}

func EnforceLimits() fiber.Handler {
	return func(c *fiber.Ctx) error {
		userID, _ := c.Locals("user_id").(string)
		if strings.TrimSpace(userID) == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "Unauthorized user",
			})
		}

		req := inferBillingRequest(c)
		if !req.Charge || req.ToolName == "" {
			return c.Next()
		}

		units := estimateUnits(req.ToolName, req.Pages, req.Images)
		if units <= 0 {
			units = 1
		}

		reservation, err := reserveQuota(userID, req.ToolName, units)
		if err != nil {
			log.Printf("[BILLING] reserve failed user=%s tool=%s path=%s err=%v", userID, req.ToolName, c.Path(), err)
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error":                    err.Error(),
				"tool":                     req.ToolName,
				"units":                    units,
				"custom_credits_remaining": currentCreditsForError(userID),
			})
		}

		c.Locals("consumed_via_credit", reservation.WasCredit)
		c.Locals("billing_units", reservation.Units)
		c.Locals("billing_tool", reservation.ToolName)

		err = c.Next()
		if err != nil {
			if rbErr := releaseQuota(userID, reservation); rbErr != nil {
				log.Printf("[BILLING] refund failed user=%s tool=%s err=%v", userID, reservation.ToolName, rbErr)
			}
			return err
		}

		if c.Response().StatusCode() >= 400 {
			if rbErr := releaseQuota(userID, reservation); rbErr != nil {
				log.Printf("[BILLING] refund failed user=%s tool=%s err=%v", userID, reservation.ToolName, rbErr)
			}
		}

		return nil
	}
}

func inferBillingRequest(c *fiber.Ctx) billingRequest {
	path := strings.ToLower(c.Path())

	switch {
	case strings.Contains(path, "/ocr/extract-text"):
		return billingRequest{
			ToolName: "extract_text_from_pdf",
			Pages:    countUploadedPDFPages(c, "file"),
			Charge:   true,
		}
	case strings.Contains(path, "/ocr/to-text-pdf"):
		return billingRequest{
			ToolName: "image_to_text_pdf",
			Images:   countMultipartImages(c, "images"),
			Charge:   true,
		}
	case strings.Contains(path, "/edit/extract"):
		return billingRequest{
			ToolName: "pdf_edit_extract",
			Pages:    countUploadedPDFPages(c, "file"),
			Charge:   true,
		}
	case strings.Contains(path, "/edit/compile"):
		return billingRequest{
			ToolName: "pdf_edit_compile",
			Pages:    countPagesFromEditCompileBody(c.Body()),
			Charge:   true,
		}
	case strings.Contains(path, "compress"):
		return billingRequest{
			ToolName: "compress",
			Pages:    countUploadedPDFPages(c, "file"),
			Charge:   true,
		}
	case strings.Contains(path, "delete"):
		return billingRequest{
			ToolName: "delete",
			Pages:    countSelectedPages(c.FormValue("pages")),
			Charge:   true,
		}
	case strings.Contains(path, "reorder"):
		return billingRequest{
			ToolName: "reorder",
			Pages:    countUploadedPDFPages(c, "file"),
			Charge:   true,
		}
	case strings.Contains(path, "watermark"):
		return billingRequest{
			ToolName: "watermark",
			Pages:    countUploadedPDFPages(c, "file"),
			Charge:   true,
		}
	case strings.Contains(path, "add-page-numbers"):
		return billingRequest{
			ToolName: "add_page_numbers",
			Pages:    countUploadedPDFPages(c, "file"),
			Charge:   true,
		}
	case strings.Contains(path, "highlight"):
		return billingRequest{
			ToolName: "highlight",
			Pages:    countUploadedPDFPages(c, "file"),
			Charge:   true,
		}
	case strings.Contains(path, "markdown") && strings.Contains(path, "pdf"):
		return billingRequest{
			ToolName: "markdown_to_pdf",
			Charge:   true,
		}
	case strings.Contains(path, "code") && strings.Contains(path, "pdf"):
		return billingRequest{
			ToolName: "code_to_pdf",
			Charge:   true,
		}
	default:
		return billingRequest{Charge: false}
	}
}

func reserveQuota(userID, toolName string, units int) (*billingReservation, error) {
	now := time.Now()
	var res *billingReservation

	err := config.DB.Transaction(func(tx *gorm.DB) error {
		var sub config.Subscription
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ?", userID).
			First(&sub).Error; err != nil {
			return fmt.Errorf("account subscription missing")
		}

		syncWindows(&sub, now)

		limits := limitsForTier(sub.Tier)

		// Hard caps for burst control.
		if sub.UsedUnits3h+units > limits.Units3H {
			return fmt.Errorf("3-hour limit exceeded")
		}
		if sub.UsedUnitsDaily+units > limits.UnitsDay {
			return fmt.Errorf("daily limit exceeded")
		}

		// Monthly allowance can be extended by credits, but credits still obey the same burst windows.
		totalMonthlyAllowance := limits.UnitsMonth + sub.CustomCredits
		if sub.UsedUnitsMonthly+units > totalMonthlyAllowance {
			return fmt.Errorf("monthly limit exceeded")
		}

		creditUse := 0
		if sub.UsedUnitsMonthly+units > limits.UnitsMonth {
			creditUse = sub.UsedUnitsMonthly + units - limits.UnitsMonth
			if creditUse > sub.CustomCredits {
				return fmt.Errorf("credits exhausted")
			}
		}

		sub.UsedUnits3h += units
		sub.UsedUnitsDaily += units
		sub.UsedUnitsMonthly += units
		sub.CustomCredits -= creditUse
		sub.UpdatedAt = now

		if err := tx.Save(&sub).Error; err != nil {
			return err
		}

		res = &billingReservation{
			Units:     units,
			CreditUse: creditUse,
			ToolName:  toolName,
			WasCredit: creditUse > 0,
		}
		return nil
	})

	return res, err
}

func releaseQuota(userID string, r *billingReservation) error {
	if r == nil {
		return nil
	}

	now := time.Now()
	return config.DB.Transaction(func(tx *gorm.DB) error {
		var sub config.Subscription
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ?", userID).
			First(&sub).Error; err != nil {
			return err
		}

		syncWindows(&sub, now)

		sub.UsedUnits3h -= r.Units
		sub.UsedUnitsDaily -= r.Units
		sub.UsedUnitsMonthly -= r.Units
		if sub.UsedUnits3h < 0 {
			sub.UsedUnits3h = 0
		}
		if sub.UsedUnitsDaily < 0 {
			sub.UsedUnitsDaily = 0
		}
		if sub.UsedUnitsMonthly < 0 {
			sub.UsedUnitsMonthly = 0
		}
		sub.CustomCredits += r.CreditUse
		sub.UpdatedAt = now

		return tx.Save(&sub).Error
	})
}

func syncWindows(sub *config.Subscription, now time.Time) {
	if sub.Tier == "" {
		sub.Tier = "free"
	}

	if (sub.Tier == "pro" || sub.Tier == "plus") && !sub.CurrentPeriodEnd.IsZero() && now.After(sub.CurrentPeriodEnd) {
		sub.Tier = "free"
		sub.Status = "expired"
	}

	if sub.Window3HResetAt.IsZero() || !now.Before(sub.Window3HResetAt) {
		sub.UsedUnits3h = 0
		sub.Window3HResetAt = now.Truncate(3 * time.Hour).Add(3 * time.Hour)
	}

	if sub.WindowDailyResetAt.IsZero() || !now.Before(sub.WindowDailyResetAt) {
		sub.UsedUnitsDaily = 0
		sub.WindowDailyResetAt = nextMidnight(now)
	}

	if sub.WindowMonthlyResetAt.IsZero() || !now.Before(sub.WindowMonthlyResetAt) {
		sub.UsedUnitsMonthly = 0
		sub.WindowMonthlyResetAt = nextMonthStart(now)
	}
}

func limitsForTier(tier string) tierLimit {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "pro":
		return tierLimit{Units3H: 80, UnitsDay: 250, UnitsMonth: 1000}
	case "plus":
		return tierLimit{Units3H: 20, UnitsDay: 60, UnitsMonth: 250}
	default:
		return tierLimit{Units3H: 8, UnitsDay: 20, UnitsMonth: 80}
	}
}

func estimateUnits(toolName string, pages, images int) int {
	if pages < 0 {
		pages = 0
	}
	if images < 0 {
		images = 0
	}

	switch toolName {
	case "compress":
		return atLeastOne(math.Ceil(2 + float64(pages)*0.20))
	case "delete":
		return atLeastOne(math.Ceil(1 + float64(pages)*0.08))
	case "reorder":
		return atLeastOne(math.Ceil(1 + float64(pages)*0.10))
	case "watermark":
		return atLeastOne(math.Ceil(2 + float64(pages)*0.15))
	case "add_page_numbers":
		return atLeastOne(math.Ceil(2 + float64(pages)*0.12))
	case "highlight":
		return atLeastOne(math.Ceil(4 + float64(pages)*0.20))
	case "extract_text_from_pdf":
		return atLeastOne(math.Ceil(5 + float64(pages)*0.50))
	case "image_to_text_pdf":
		return atLeastOne(math.Ceil(6 + float64(images)*0.80))
	case "pdf_edit_extract":
		return atLeastOne(math.Ceil(3 + float64(pages)*0.10))
	case "pdf_edit_compile":
		return atLeastOne(math.Ceil(2 + float64(pages)*0.05))
	case "markdown_to_pdf":
		return 3
	case "code_to_pdf":
		return 3
	default:
		return 1
	}
}

func atLeastOne(v float64) int {
	n := int(math.Ceil(v))
	if n < 1 {
		return 1
	}
	return n
}

func nextMidnight(now time.Time) time.Time {
	y, m, d := now.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, now.Location()).AddDate(0, 0, 1)
}

func nextMonthStart(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).AddDate(0, 1, 0)
}

func countUploadedPDFPages(c *fiber.Ctx, field string) int {
	fileHeader, err := c.FormFile(field)
	if err != nil || fileHeader == nil {
		return 0
	}

	tempDir := os.TempDir()
	tempPath := filepath.Join(tempDir, uuid.New().String()+"-"+filepath.Base(fileHeader.Filename))
	if err := c.SaveFile(fileHeader, tempPath); err != nil {
		return 0
	}
	defer func() { _ = os.Remove(tempPath) }()

	pages, err := api.PageCountFile(tempPath)
	if err != nil || pages <= 0 {
		return 0
	}
	return pages
}

func countMultipartImages(c *fiber.Ctx, field string) int {
	form, err := c.MultipartForm()
	if err != nil || form == nil {
		return 0
	}
	return len(form.File[field])
}

func countPagesFromEditCompileBody(body []byte) int {
	var payload struct {
		SourceTracker string `json:"source_tracker"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0
	}
	if strings.TrimSpace(payload.SourceTracker) == "" {
		return 0
	}
	if _, err := os.Stat(payload.SourceTracker); err != nil {
		return 0
	}
	pages, err := api.PageCountFile(payload.SourceTracker)
	if err != nil || pages <= 0 {
		return 0
	}
	return pages
}

func countSelectedPages(selection string) int {
	selection = strings.TrimSpace(selection)
	if selection == "" {
		return 0
	}

	total := 0
	parts := strings.Split(selection, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			r := strings.Split(part, "-")
			if len(r) != 2 {
				continue
			}
			start, err1 := atoiSafe(strings.TrimSpace(r[0]))
			end, err2 := atoiSafe(strings.TrimSpace(r[1]))
			if err1 != nil || err2 != nil || end < start {
				continue
			}
			total += end - start + 1
		} else {
			if _, err := atoiSafe(part); err == nil {
				total++
			}
		}
	}
	return total
}

func atoiSafe(s string) (int, error) {
	var v int
	_, err := fmt.Sscanf(s, "%d", &v)
	return v, err
}

func currentCreditsForError(userID string) int {
	var sub config.Subscription
	if err := config.DB.Where("user_id = ?", userID).First(&sub).Error; err != nil {
		return 0
	}
	return sub.CustomCredits
}
