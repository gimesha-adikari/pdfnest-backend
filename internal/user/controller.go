package user

import (
	"encoding/json"
	"fmt"
	"pdfnest-backend/config"
	"time"

	"github.com/gofiber/fiber/v2"
	"golang.org/x/crypto/bcrypt"
)

type Controller struct{}

func NewController() *Controller {
	return &Controller{}
}

type PreferencesRequest struct {
	EmailNotifications bool   `json:"email_notifications"`
	ProductUpdates     bool   `json:"product_updates"`
	BillingEmails      bool   `json:"billing_emails"`
	SecurityAlerts     bool   `json:"security_alerts"`
	Theme              string `json:"theme"`
	Language           string `json:"language"`
}

func getUserID(c *fiber.Ctx) (string, error) {
	v := c.Locals("user_id")
	if v == nil {
		return "", fiber.NewError(fiber.StatusUnauthorized, "Unauthorized")
	}

	userID, ok := v.(string)
	if !ok || userID == "" {
		return "", fiber.NewError(fiber.StatusUnauthorized, "Unauthorized")
	}

	return userID, nil
}

func ensureUserSettings(userID string) (*config.UserSetting, error) {
	var settings config.UserSetting
	err := config.DB.First(&settings, "user_id = ?", userID).Error
	if err == nil {
		return &settings, nil
	}

	settings = config.UserSetting{
		ID:                 config.NewUUID(),
		UserID:             userID,
		EmailNotifications: true,
		ProductUpdates:     true,
		BillingEmails:      true,
		SecurityAlerts:     true,
		Theme:              "system",
		Language:           "en",
		CreatedAt:          time.Now(),
		UpdatedAt:          time.Now(),
	}

	if err := config.DB.Create(&settings).Error; err != nil {
		return nil, err
	}

	return &settings, nil
}

// UpdatePassword allows users to change their account password
func (ctrl *Controller) UpdatePassword(c *fiber.Ctx) error {
	userID, err := getUserID(c)
	if err != nil {
		return err
	}

	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}

	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request payload"})
	}

	if len(req.NewPassword) < 8 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "New password must be at least 8 characters"})
	}

	var user config.User
	if err := config.DB.First(&user, "id = ?", userID).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "User not found"})
	}

	if user.PasswordHash == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "This account uses Google sign-in"})
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.CurrentPassword)); err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Incorrect current password"})
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to process new password"})
	}

	user.PasswordHash = string(hashedPassword)
	user.UpdatedAt = time.Now()

	if err := config.DB.Save(&user).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to update password"})
	}

	return c.JSON(fiber.Map{"status": "success", "message": "Password updated successfully"})
}

// GetPreferences returns user notification/theme/language preferences
func (ctrl *Controller) GetPreferences(c *fiber.Ctx) error {
	userID, err := getUserID(c)
	if err != nil {
		return err
	}

	settings, err := ensureUserSettings(userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to load preferences"})
	}

	return c.JSON(fiber.Map{
		"email_notifications": settings.EmailNotifications,
		"product_updates":     settings.ProductUpdates,
		"billing_emails":      settings.BillingEmails,
		"security_alerts":     settings.SecurityAlerts,
		"theme":               settings.Theme,
		"language":            settings.Language,
	})
}

// UpdatePreferences saves user notification/theme/language preferences
func (ctrl *Controller) UpdatePreferences(c *fiber.Ctx) error {
	userID, err := getUserID(c)
	if err != nil {
		return err
	}

	var req PreferencesRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request payload"})
	}

	if req.Theme == "" {
		req.Theme = "system"
	}
	if req.Language == "" {
		req.Language = "en"
	}

	settings, err := ensureUserSettings(userID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to load preferences"})
	}

	settings.EmailNotifications = req.EmailNotifications
	settings.ProductUpdates = req.ProductUpdates
	settings.BillingEmails = req.BillingEmails
	settings.SecurityAlerts = req.SecurityAlerts
	settings.Theme = req.Theme
	settings.Language = req.Language
	settings.UpdatedAt = time.Now()

	if err := config.DB.Save(settings).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to save preferences"})
	}

	return c.JSON(fiber.Map{
		"status":  "success",
		"message": "Preferences saved successfully",
	})
}

// ExportData returns a downloadable JSON backup of account data
func (ctrl *Controller) ExportData(c *fiber.Ctx) error {
	userID, err := getUserID(c)
	if err != nil {
		return err
	}

	var user config.User
	if err := config.DB.First(&user, "id = ?", userID).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "User not found"})
	}

	var subscription config.Subscription
	_ = config.DB.Where("user_id = ?", userID).First(&subscription).Error

	var settings config.UserSetting
	_ = config.DB.Where("user_id = ?", userID).First(&settings).Error

	var usageLogs []config.UsageLog
	config.DB.Where("user_id = ?", userID).Order("created_at desc").Limit(200).Find(&usageLogs)

	var transactions []config.Transaction
	config.DB.Where("user_id = ?", userID).Order("created_at desc").Limit(200).Find(&transactions)

	export := fiber.Map{
		"exported_at": time.Now().UTC(),
		"user": fiber.Map{
			"id":             user.ID,
			"email":          user.Email,
			"role":           user.Role,
			"status":         user.Status,
			"email_verified": user.EmailVerified,
			"created_at":     user.CreatedAt,
			"updated_at":     user.UpdatedAt,
		},
		"subscription": fiber.Map{
			"tier":               subscription.Tier,
			"status":             subscription.Status,
			"billing_interval":   subscription.BillingInterval,
			"current_period_end": subscription.CurrentPeriodEnd,
			"used_units_3h":      subscription.UsedUnits3h,
			"used_units_daily":   subscription.UsedUnitsDaily,
			"used_units_monthly": subscription.UsedUnitsMonthly,
			"custom_credits":     subscription.CustomCredits,
			"update_url":         subscription.UpdateURL,
			"cancel_url":         subscription.CancelURL,
		},
		"preferences": fiber.Map{
			"email_notifications": settings.EmailNotifications,
			"product_updates":     settings.ProductUpdates,
			"billing_emails":      settings.BillingEmails,
			"security_alerts":     settings.SecurityAlerts,
			"theme":               settings.Theme,
			"language":            settings.Language,
		},
		"usage_logs":   usageLogs,
		"transactions": transactions,
	}

	body, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to export data"})
	}

	filename := fmt.Sprintf("pdfnest-account-data-%s.json", userID)
	c.Set("Content-Type", "application/json")
	c.Attachment(filename)
	return c.Send(body)
}

// DeleteAccount handles self-service account deletion
func (ctrl *Controller) DeleteAccount(c *fiber.Ctx) error {
	userID, err := getUserID(c)
	if err != nil {
		return err
	}

	var user config.User
	if err := config.DB.First(&user, "id = ?", userID).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "User not found"})
	}

	// Optional: cancel subscription in Paddle before deleting user
	// and delete related settings, preferences, logs if you want hard cleanup.

	if err := config.DB.Delete(&user).Error; err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to delete account"})
	}

	return c.JSON(fiber.Map{"status": "success", "message": "Account successfully deleted"})
}
