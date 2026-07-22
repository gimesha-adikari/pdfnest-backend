package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/mail"
	"os"
	"strings"
	"time"

	"pdfnest-backend/config"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/resend/resend-go/v2"
	"gorm.io/gorm"
)

type Controller struct {
	service Service
}

type GoogleAuthRequest struct {
	IDToken string `json:"id_token"`
}

type AuthRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type VerifyEmailRequest struct {
	Token string `json:"token"`
}

type ResendVerificationRequest struct {
	Email string `json:"email"`
}

func isLocal() bool {
	local := os.Getenv("LOCAL")
	if local == "true" {
		return true
	}
	return false
}
func NewController(s Service) *Controller {
	return &Controller{service: s}
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func generateVerificationToken() (raw string, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}

	raw = hex.EncodeToString(b)
	sum := sha256.Sum256([]byte(raw))
	hash = hex.EncodeToString(sum[:])
	return raw, hash, nil
}

func hashVerificationToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func ensureFreeSubscription(tx *gorm.DB, userID string) error {
	var count int64
	if err := tx.Model(&config.Subscription{}).Where("user_id = ?", userID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	freeSub := config.Subscription{
		ID:                   uuid.New().String(),
		UserID:               userID,
		PaddleCustomerID:     "free_cust_" + userID,
		PaddleSubscriptionID: "free_sub_" + userID,
		Status:               "active",
		Tier:                 "free",
		CurrentPeriodEnd:     time.Now().AddDate(10, 0, 0),
	}

	return tx.Create(&freeSub).Error
}

func sendVerificationEmail(toEmail, rawToken string) error {
	apiKey := os.Getenv("RESEND_API_KEY")
	fromEmail := os.Getenv("FROM_EMAIL")

	if apiKey == "" {
		return fmt.Errorf("RESEND_API_KEY is not configured")
	}

	if fromEmail == "" {
		return fmt.Errorf("FROM_EMAIL is not configured")
	}

	frontendURL := strings.TrimRight(os.Getenv("FRONTEND_URL"), "/")
	if frontendURL == "" {
		frontendURL = "http://localhost:3000"
	}

	verifyURL := fmt.Sprintf("%s/verify-email?token=%s", frontendURL, rawToken)

	client := resend.NewClient(apiKey)

	params := &resend.SendEmailRequest{
		From:    fromEmail,
		To:      []string{toEmail},
		Subject: "Verify your Platen PDF email",

		Text: fmt.Sprintf(
			`Verify your email

Thanks for creating your Platen PDF account.

Click the link below to verify your email:

%s

This verification link expires in 30 minutes.

If you didn't create this account, you can safely ignore this email.`,
			verifyURL,
		),

		Html: fmt.Sprintf(`
<!DOCTYPE html>
<html>
<body style="font-family:Arial,sans-serif;background:#f5f5f5;padding:40px;">
<div style="max-width:600px;margin:auto;background:white;padding:40px;border-radius:12px;">

<h2>Verify your email</h2>

<p>Thanks for creating your Platen PDF account.</p>

<p>Please click the button below to verify your email address.</p>

<p style="margin:30px 0;">
<a href="%s"
style="
background:#4f46e5;
color:white;
padding:14px 28px;
border-radius:8px;
text-decoration:none;
font-weight:bold;
display:inline-block;">
Verify Email
</a>
</p>

<p>If the button doesn't work, use this link:</p>

<p>
<a href="%s">%s</a>
</p>

<hr>

<p style="color:#666">
This verification link expires in 30 minutes.
</p>

<p style="color:#666">
If you didn't create this account, you can safely ignore this email.
</p>

</div>
</body>
</html>
`, verifyURL, verifyURL, verifyURL),
	}

	email, err := client.Emails.Send(params)
	if err != nil {
		log.Printf("Resend error: %v", err)
		return err
	}

	log.Printf("Email sent: %+v", email)

	return nil
}

func (ctrl *Controller) Register(c *fiber.Ctx) error {
	var req AuthRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid request payload"})
	}

	req.Email = normalizeEmail(req.Email)
	if _, err := mail.ParseAddress(req.Email); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid email address"})
	}

	if len(req.Password) < 8 {
		return c.Status(400).JSON(fiber.Map{"error": "Password must be at least 8 characters"})
	}

	var existing config.User
	if err := config.DB.Where("email = ?", req.Email).First(&existing).Error; err == nil {
		return c.Status(400).JSON(fiber.Map{"error": "Email already registered"})
	}

	hashedPassword, err := ctrl.service.HashPassword(req.Password)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed hashing password"})
	}

	rawToken, tokenHash, err := generateVerificationToken()
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed generating verification token"})
	}

	user := config.User{
		ID:                   uuid.New().String(),
		Email:                req.Email,
		PasswordHash:         hashedPassword,
		Role:                 "user",
		Status:               "pending",
		EmailVerified:        false,
		EmailVerifyTokenHash: tokenHash,
		EmailVerifyExpiresAt: time.Now().Add(30 * time.Minute),
	}

	if err := config.DB.Create(&user).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed creating account"})
	}

	if err := sendVerificationEmail(user.Email, rawToken); err != nil {
		log.Printf("failed to send verification email to %s: %v", user.Email, err)
		return c.Status(201).JSON(fiber.Map{
			"message":                 "Account created, but the verification email could not be sent. Use resend verification.",
			"verification_email_sent": false,
			"userId":                  user.ID,
		})
	}

	return c.Status(201).JSON(fiber.Map{
		"message":                 "Account created. Please verify your email.",
		"verification_email_sent": true,
		"userId":                  user.ID,
	})
}

func (ctrl *Controller) VerifyEmail(c *fiber.Ctx) error {
	token := strings.TrimSpace(c.Query("token"))

	if token == "" {
		var req VerifyEmailRequest
		if err := c.BodyParser(&req); err == nil {
			token = strings.TrimSpace(req.Token)
		}
	}

	if token == "" {
		return c.Status(400).JSON(fiber.Map{"error": "Missing verification token"})
	}

	tokenHash := hashVerificationToken(token)

	var user config.User
	if err := config.DB.Where("email_verify_token_hash = ?", tokenHash).First(&user).Error; err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid or expired token"})
	}

	if time.Now().After(user.EmailVerifyExpiresAt) {
		return c.Status(400).JSON(fiber.Map{"error": "Verification token expired"})
	}

	err := config.DB.Transaction(func(tx *gorm.DB) error {
		var txUser config.User
		if err := tx.First(&txUser, "id = ?", user.ID).Error; err != nil {
			return err
		}

		txUser.EmailVerified = true
		txUser.Status = "active"
		txUser.EmailVerifyTokenHash = ""
		txUser.EmailVerifyExpiresAt = time.Time{}

		if err := tx.Save(&txUser).Error; err != nil {
			return err
		}

		return ensureFreeSubscription(tx, txUser.ID)
	})
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed verifying email"})
	}

	return c.JSON(fiber.Map{"success": true, "message": "Email verified successfully"})
}

func (ctrl *Controller) ResendVerification(c *fiber.Ctx) error {
	var req ResendVerificationRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid request payload"})
	}

	req.Email = normalizeEmail(req.Email)
	if _, err := mail.ParseAddress(req.Email); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid email address"})
	}

	var user config.User
	if err := config.DB.Where("email = ?", req.Email).First(&user).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "User not found"})
	}

	if user.EmailVerified {
		return c.Status(400).JSON(fiber.Map{"error": "Email is already verified"})
	}

	rawToken, tokenHash, err := generateVerificationToken()
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed generating verification token"})
	}

	user.EmailVerifyTokenHash = tokenHash
	user.EmailVerifyExpiresAt = time.Now().Add(30 * time.Minute)

	if err := config.DB.Save(&user).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed updating verification token"})
	}

	if err := sendVerificationEmail(user.Email, rawToken); err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed sending verification email"})
	}

	return c.JSON(fiber.Map{"success": true, "message": "Verification email sent"})
}

func (ctrl *Controller) Login(c *fiber.Ctx) error {
	var req AuthRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid request payload"})
	}

	req.Email = normalizeEmail(req.Email)

	var user config.User
	if err := config.DB.Where("email = ?", req.Email).First(&user).Error; err != nil {
		return c.Status(401).JSON(fiber.Map{"error": "Invalid credentials"})
	}

	if user.Status == "banned" {
		return c.Status(403).JSON(fiber.Map{"error": "This account is suspended"})
	}

	if !user.EmailVerified {
		return c.Status(403).JSON(fiber.Map{"error": "Please verify your email first"})
	}

	if err := ctrl.service.VerifyPassword(user.PasswordHash, req.Password); err != nil {
		return c.Status(401).JSON(fiber.Map{"error": "Invalid credentials"})
	}

	token, err := ctrl.service.GenerateToken(user.ID, user.Role)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed generating token"})
	}

	isProduction := os.Getenv("APP_ENV") == "production"

	cookie := &fiber.Cookie{
		Name:     "auth_token",
		Value:    token,
		Path:     "/",
		Expires:  time.Now().Add(24 * time.Hour),
		HTTPOnly: true,
		Secure:   isProduction,
	}

	if isProduction {
		cookie.SameSite = "None"
	} else {
		cookie.SameSite = "Lax"
	}

	c.Cookie(cookie)

	return c.JSON(fiber.Map{"success": true, "role": user.Role})
}

func (ctrl *Controller) GoogleSignIn(c *fiber.Ctx) error {
	var req GoogleAuthRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid payload"})
	}

	claims, err := ctrl.service.VerifyGoogleToken(c.Context(), req.IDToken)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"error": "Invalid Google authentication token"})
	}

	email := normalizeEmail(claims["email"].(string))
	googleID := claims["sub"].(string)

	var user config.User
	err = config.DB.Transaction(func(tx *gorm.DB) error {
		findErr := tx.Where("google_id = ? OR email = ?", googleID, email).First(&user).Error
		if findErr != nil {
			user = config.User{
				ID:            uuid.New().String(),
				Email:         email,
				GoogleID:      &googleID,
				Role:          "user",
				Status:        "active",
				EmailVerified: true,
			}

			if err := tx.Create(&user).Error; err != nil {
				return err
			}

			return ensureFreeSubscription(tx, user.ID)
		}

		if user.GoogleID == nil || *user.GoogleID == "" {
			user.GoogleID = &googleID
		}
		user.EmailVerified = true
		if user.Status != "banned" {
			user.Status = "active"
		}

		if err := tx.Save(&user).Error; err != nil {
			return err
		}

		return ensureFreeSubscription(tx, user.ID)
	})
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed processing Google sign-in"})
	}

	if user.Status == "banned" {
		return c.Status(403).JSON(fiber.Map{"error": "This account is suspended"})
	}

	token, err := ctrl.service.GenerateToken(user.ID, user.Role)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed generating token"})
	}

	isProduction := os.Getenv("APP_ENV") == "production"

	cookie := &fiber.Cookie{
		Name:     "auth_token",
		Value:    token,
		Path:     "/",
		Expires:  time.Now().Add(24 * time.Hour),
		HTTPOnly: true,
		Secure:   isProduction,
	}

	if isProduction {
		cookie.SameSite = "None"
	} else {
		cookie.SameSite = "Lax"
	}

	c.Cookie(cookie)

	return c.JSON(fiber.Map{"success": true, "role": user.Role})
}

func (ctrl *Controller) Logout(c *fiber.Ctx) error {

	isProduction := os.Getenv("APP_ENV") == "production"

	cookie := &fiber.Cookie{
		Name:     "auth_token",
		Value:    "",
		Path:     "/",
		Expires:  time.Now().Add(-24 * time.Hour),
		HTTPOnly: true,
		Secure:   isProduction,
	}

	if isProduction {
		cookie.SameSite = "None"
	} else {
		cookie.SameSite = "Lax"
	}

	c.Cookie(cookie)

	return c.JSON(fiber.Map{"success": true, "message": "Logged out successfully"})
}
