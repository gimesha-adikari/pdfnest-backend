package auth

import (
	"pdfnest-backend/config"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type Controller struct {
	service Service
}
type GoogleAuthRequest struct {
	IDToken string `json:"id_token"`
}

func NewController(s Service) *Controller {
	return &Controller{service: s}
}

type AuthRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (ctrl *Controller) Register(c *fiber.Ctx) error {
	var req AuthRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid request parameters payload"})
	}

	hashedPassword, err := ctrl.service.HashPassword(req.Password)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed hashing text credential safety vectors"})
	}

	user := config.User{
		ID:           uuid.New().String(),
		Email:        req.Email,
		PasswordHash: hashedPassword,
		Role:         "user",
		Status:       "active",
	}

	if err := config.DB.Create(&user).Error; err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "User email context already exists inside registers"})
	}

	freeSub := config.Subscription{
		ID:               uuid.New().String(),
		UserID:           user.ID,
		Status:           "active",
		Tier:             "free", // Fixed from PlanTier
		CurrentPeriodEnd: time.Now().AddDate(10, 0, 0),
	}
	config.DB.Create(&freeSub)

	return c.Status(201).JSON(fiber.Map{"message": "Account created successfully", "userId": user.ID})
}

func (ctrl *Controller) Login(c *fiber.Ctx) error {
	var req AuthRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid request payload layout parameters"})
	}

	var user config.User
	if err := config.DB.Where("email = ?", req.Email).First(&user).Error; err != nil {
		return c.Status(401).JSON(fiber.Map{"error": "Invalid credential pair parameters configuration rules"})
	}

	if user.Status == "banned" {
		return c.Status(403).JSON(fiber.Map{"error": "This profile account access level is currently suspended"})
	}

	if err := ctrl.service.VerifyPassword(user.PasswordHash, req.Password); err != nil {
		return c.Status(401).JSON(fiber.Map{"error": "Invalid credentials match pattern context configurations"})
	}

	token, err := ctrl.service.GenerateToken(user.ID, user.Role)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Token signature key validation generation error failed"})
	}

	c.Cookie(&fiber.Cookie{
		Name:     "auth_token",
		Value:    token,
		Expires:  time.Now().Add(24 * time.Hour),
		HTTPOnly: true,
		Secure:   true,
		SameSite: "Lax",
	})

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

	email := claims["email"].(string)
	googleID := claims["sub"].(string)

	var user config.User
	err = config.DB.Where("google_id = ? OR email = ?", googleID, email).First(&user).Error

	if err != nil {
		user = config.User{
			ID:       uuid.New().String(),
			Email:    email,
			GoogleID: googleID,
			Role:     "user",
			Status:   "active",
		}

		if err := config.DB.Create(&user).Error; err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "Failed creating account via Google context"})
		}

		freeSub := config.Subscription{
			ID:               uuid.New().String(),
			UserID:           user.ID,
			Status:           "active",
			Tier:             "free", // Fixed from PlanTier
			CurrentPeriodEnd: time.Now().AddDate(10, 0, 0),
		}
		config.DB.Create(&freeSub)
	} else {
		if user.GoogleID == "" {
			user.GoogleID = googleID
			config.DB.Save(&user)
		}
	}

	if user.Status == "banned" {
		return c.Status(403).JSON(fiber.Map{"error": "This profile account access level is currently suspended"})
	}

	token, err := ctrl.service.GenerateToken(user.ID, user.Role)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Token signature key validation generation error failed"})
	}

	c.Cookie(&fiber.Cookie{
		Name:     "auth_token",
		Value:    token,
		Expires:  time.Now().Add(24 * time.Hour),
		HTTPOnly: true,
		Secure:   true,
		SameSite: "Lax",
	})

	return c.JSON(fiber.Map{"success": true, "role": user.Role})
}
