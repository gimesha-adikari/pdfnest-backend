package auth

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/mail"
	"os"
	"pdfnest-backend/config"
	"pdfnest-backend/internal/authn"
	"pdfnest-backend/internal/mailer"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errPolicyConsentRequired = errors.New("policy consent required")
var errPasswordResetTokenInvalid = errors.New("invalid or expired password reset token")

const (
	passwordResetTTL      = 30 * time.Minute
	passwordResetCooldown = 60 * time.Second
)

type Controller struct {
	service Service
}

type GoogleAuthRequest struct {
	IDToken        string `json:"id_token"`
	PolicyAccepted bool   `json:"policy_accepted"`
}

type AuthRequest struct {
	Email          string `json:"email"`
	Password       string `json:"password"`
	PolicyAccepted bool   `json:"policy_accepted"`
}

type VerifyEmailRequest struct {
	Token string `json:"token"`
}

type ResendVerificationRequest struct {
	Email string `json:"email"`
}

type PasswordResetRequest struct {
	Email string `json:"email"`
}

type PasswordResetConfirmRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

type VerificationEmailData struct {
	VerifyURL  string
	ContactURL string
	Expiry     string
}

type PasswordResetEmailData struct {
	ResetURL   string
	ContactURL string
	Expiry     string
}

//go:embed templates/verification_email.html templates/password_reset_email.html
var verificationEmailTemplates embed.FS

func isLocal() bool {
	local := os.Getenv("LOCAL")
	return local == "true"
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
	frontendURL := strings.TrimRight(os.Getenv("FRONTEND_URL"), "/")
	if frontendURL == "" {
		frontendURL = "http://localhost:3000"
	}

	verifyURL := fmt.Sprintf("%s/verify-email?token=%s", frontendURL, rawToken)
	contactURL := fmt.Sprintf("%s/contact", frontendURL)

	log.Printf("[AUTH EMAIL] preparing verification email for %s", toEmail)
	log.Printf("[AUTH EMAIL] verify URL: %s", verifyURL)

	htmlBody, err := buildVerificationEmailHTML(verifyURL, contactURL)
	if err != nil {
		log.Printf("[AUTH EMAIL] template render failed: %v", err)
		return err
	}

	textBody := fmt.Sprintf(
		`Verify your email

Thanks for creating your Platen PDF account.

Click the link below to verify your email:

%s

Need help? Contact us:
%s

This verification link expires in 30 minutes.

If you didn't create this account, you can safely ignore this email.`,
		verifyURL,
		contactURL,
	)

	err = mailer.Send(mailer.Email{
		To:      []string{toEmail},
		Subject: "Verify your Platen PDF email",
		Text:    textBody,
		Html:    htmlBody,
	})
	if err != nil {
		log.Printf("[AUTH EMAIL] send failed for %s: %v", toEmail, err)
		return err
	}

	log.Printf("[AUTH EMAIL] verification email sent to %s", toEmail)
	return nil
}

func buildVerificationEmailHTML(verifyURL, contactURL string) (string, error) {
	log.Println("[AUTH EMAIL] rendering verification template from embedded assets")

	tmpl, err := template.ParseFS(verificationEmailTemplates, "templates/verification_email.html")
	if err != nil {
		return "", err
	}

	var body bytes.Buffer
	if err := tmpl.Execute(&body, VerificationEmailData{
		VerifyURL:  verifyURL,
		ContactURL: contactURL,
		Expiry:     "30 minutes",
	}); err != nil {
		return "", err
	}

	return body.String(), nil
}

func sendPasswordResetEmail(toEmail, rawToken string) error {
	frontendURL := strings.TrimRight(os.Getenv("FRONTEND_URL"), "/")
	if frontendURL == "" {
		frontendURL = "http://localhost:3000"
	}

	resetURL := fmt.Sprintf("%s/reset-password?token=%s", frontendURL, rawToken)
	contactURL := fmt.Sprintf("%s/contact", frontendURL)

	htmlBody, err := buildPasswordResetEmailHTML(resetURL, contactURL)
	if err != nil {
		return err
	}

	textBody := fmt.Sprintf(
		`Reset your Platen PDF password

We received a request to reset the password for your account.

Use the link below to choose a new password:

%s

This password reset link expires in 30 minutes and can be used only once.

If you did not request this change, you can safely ignore this email.

Need help? Contact us:
%s`,
		resetURL,
		contactURL,
	)

	return mailer.Send(mailer.Email{
		To:      []string{toEmail},
		Subject: "Reset your Platen PDF password",
		Text:    textBody,
		Html:    htmlBody,
	})
}

func buildPasswordResetEmailHTML(resetURL, contactURL string) (string, error) {
	tmpl, err := template.ParseFS(verificationEmailTemplates, "templates/password_reset_email.html")
	if err != nil {
		return "", err
	}

	var body bytes.Buffer
	if err := tmpl.Execute(&body, PasswordResetEmailData{
		ResetURL:   resetURL,
		ContactURL: contactURL,
		Expiry:     "30 minutes",
	}); err != nil {
		return "", err
	}

	return body.String(), nil
}

func passwordResetResponse(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"success": true,
		"message": "If an account exists for that email, a password reset link will be sent shortly.",
	})
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

	if !req.PolicyAccepted {
		return c.Status(400).JSON(fiber.Map{
			"error": "Policy consent is required before creating an account",
		})
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
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Missing verification token"})
	}

	if config.DB == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "Authentication is temporarily unavailable"})
	}

	tokenHash := hashVerificationToken(token)

	err := config.DB.WithContext(c.UserContext()).Transaction(func(tx *gorm.DB) error {
		var txUser config.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("email_verify_token_hash = ?", tokenHash).First(&txUser).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New("invalid_or_expired_token")
			}
			return err
		}

		// Expiry validation occurs BEFORE idempotent EmailVerified success so that
		// a retained token hash does not become permanently accepted after its valid lifetime.
		if txUser.EmailVerifyExpiresAt.IsZero() || time.Now().After(txUser.EmailVerifyExpiresAt) {
			return errors.New("token_expired")
		}

		// Idempotency: If this exact token already verified this account within its valid lifetime,
		// return success immediately. Duplicate requests (e.g. React StrictMode, user double-click,
		// browser prefetch, or email scanner prefetch) within the token window must not fail.
		if txUser.EmailVerified {
			return nil
		}

		txUser.EmailVerified = true
		if txUser.Status == "pending" {
			txUser.Status = "active"
		}
		// Notice: txUser.EmailVerifyTokenHash is preserved so that subsequent duplicate or replay requests
		// with this exact valid token are recognized as already verified and handled idempotently.

		if err := tx.Save(&txUser).Error; err != nil {
			return err
		}

		return ensureFreeSubscription(tx, txUser.ID)
	})

	if err != nil {
		if err.Error() == "invalid_or_expired_token" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid or expired token"})
		}
		if err.Error() == "token_expired" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Verification token expired"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed verifying email"})
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

	if config.DB == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "Authentication is temporarily unavailable"})
	}

	var user config.User
	if err := config.DB.Where("email = ?", req.Email).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.JSON(fiber.Map{"success": true, "message": "Verification email sent"})
		}
		return c.Status(500).JSON(fiber.Map{"error": "Failed checking user"})
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

// RequestPasswordReset always returns the same public response for valid
// email-shaped input, regardless of whether an account exists.  A short
// per-account cooldown and one active bounded token keep resend abuse limited
// without exposing account state to the caller.
func (ctrl *Controller) RequestPasswordReset(c *fiber.Ctx) error {
	var req PasswordResetRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request payload"})
	}

	req.Email = normalizeEmail(req.Email)
	if req.Email == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Email is required"})
	}
	if _, err := mail.ParseAddress(req.Email); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid email address"})
	}
	if config.DB == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "Authentication is temporarily unavailable"})
	}

	var user config.User
	var rawToken string
	now := time.Now()
	err := config.DB.WithContext(c.UserContext()).Transaction(func(tx *gorm.DB) error {
		// Lock the account row while checking and replacing the active token so
		// concurrent requests cannot bypass the resend cooldown and send two
		// reset emails for the same account.
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("email = ?", req.Email).First(&user).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}

		if user.PasswordResetTokenHash != "" && now.Before(user.PasswordResetExpiresAt) &&
			!user.PasswordResetRequestedAt.IsZero() && now.Sub(user.PasswordResetRequestedAt) < passwordResetCooldown {
			return nil
		}

		var tokenHash string
		rawToken, tokenHash, err = generateVerificationToken()
		if err != nil {
			return err
		}

		return tx.Model(&config.User{}).Where("id = ?", user.ID).Updates(map[string]any{
			"password_reset_token_hash":   tokenHash,
			"password_reset_expires_at":   now.Add(passwordResetTTL),
			"password_reset_requested_at": now,
		}).Error
	})
	if err != nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "Authentication is temporarily unavailable"})
	}
	if rawToken == "" {
		return passwordResetResponse(c)
	}

	// Keep the public response generic even if the configured provider is
	// unavailable. The token remains bounded and can be resent after cooldown.
	if err := sendPasswordResetEmail(user.Email, rawToken); err != nil {
		log.Printf("[AUTH EMAIL] password reset delivery failed for account %s: %v", user.ID, err)
	}

	return passwordResetResponse(c)
}

func (ctrl *Controller) ResetPassword(c *fiber.Ctx) error {
	var req PasswordResetConfirmRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid request payload"})
	}

	req.Token = strings.TrimSpace(req.Token)
	if req.Token == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": errPasswordResetTokenInvalid.Error()})
	}
	if len(req.Password) < 8 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Password must be at least 8 characters"})
	}
	if config.DB == nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "Authentication is temporarily unavailable"})
	}

	tokenHash := hashVerificationToken(req.Token)
	err := config.DB.WithContext(c.UserContext()).Transaction(func(tx *gorm.DB) error {
		var user config.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("password_reset_token_hash = ?", tokenHash).First(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errPasswordResetTokenInvalid
			}
			return err
		}
		if user.PasswordResetExpiresAt.IsZero() || time.Now().After(user.PasswordResetExpiresAt) {
			return errPasswordResetTokenInvalid
		}

		hashedPassword, err := ctrl.service.HashPassword(req.Password)
		if err != nil {
			return err
		}
		return tx.Model(&user).Updates(map[string]any{
			"password_hash":               hashedPassword,
			"password_reset_token_hash":   "",
			"password_reset_expires_at":   time.Time{},
			"password_reset_requested_at": time.Time{},
			"session_version":             gorm.Expr("session_version + ?", 1),
			"updated_at":                  time.Now(),
		}).Error
	})

	if errors.Is(err, errPasswordResetTokenInvalid) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": errPasswordResetTokenInvalid.Error()})
	}
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Failed to reset password"})
	}

	return c.JSON(fiber.Map{"success": true, "message": "Password reset successfully. You can now sign in."})
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

	token, err := ctrl.service.GenerateToken(user.ID, user.Role, user.SessionVersion)
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
			if !req.PolicyAccepted {
				return errPolicyConsentRequired
			}

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
		if errors.Is(err, errPolicyConsentRequired) {
			return c.Status(400).JSON(fiber.Map{
				"error": "Policy consent is required before creating an account",
			})
		}
		return c.Status(500).JSON(fiber.Map{"error": "Failed processing Google sign-in"})
	}

	if user.Status == "banned" {
		return c.Status(403).JSON(fiber.Map{"error": "This account is suspended"})
	}

	token, err := ctrl.service.GenerateToken(user.ID, user.Role, user.SessionVersion)
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

func revokeSession(c *fiber.Ctx) error {
	rawToken := strings.TrimSpace(c.Cookies("auth_token"))
	if rawToken == "" {
		return nil
	}

	user, err := authn.Verify(c.UserContext(), rawToken)
	if errors.Is(err, authn.ErrInvalid) || errors.Is(err, authn.ErrAccountDisabled) {
		// Logout remains idempotent for an already expired, revoked, or disabled
		// credential; the cookie is still cleared below.
		return nil
	}
	if err != nil {
		return err
	}

	// The version predicate makes concurrent logout requests safe: the first
	// request advances the version, and later requests observe zero affected
	// rows because the credential has already been revoked.
	if config.DB == nil {
		return authn.ErrUnavailable
	}
	if err := config.DB.WithContext(c.UserContext()).Model(&config.User{}).
		Where("id = ? AND session_version = ?", user.ID, user.SessionVersion).
		UpdateColumn("session_version", gorm.Expr("session_version + ?", 1)).Error; err != nil {
		return authn.ErrUnavailable
	}
	return nil
}

func (ctrl *Controller) Logout(c *fiber.Ctx) error {
	isProduction := os.Getenv("APP_ENV") == "production"
	revokeErr := revokeSession(c)

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
	if revokeErr != nil {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"success": false,
			"error":   "Authentication is temporarily unavailable",
		})
	}

	return c.JSON(fiber.Map{"success": true, "message": "Logged out successfully"})
}
