package auth

import (
	"context"
	"os"
	"time"

	"github.com/gofiber/fiber/v2/log"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/api/idtoken"
)

type Service interface {
	HashPassword(password string) (string, error)
	VerifyPassword(hashed, password string) error
	GenerateToken(userID, role string) (string, error)
	VerifyGoogleToken(ctx context.Context, idToken string) (map[string]interface{}, error)
}

type authService struct{}

func NewService() Service {
	return &authService{}
}

func (s *authService) HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	return string(bytes), err
}

func (s *authService) VerifyPassword(hashed, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hashed), []byte(password))
}

func (s *authService) GenerateToken(userID, role string) (string, error) {
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		log.Fatal("CRITICAL SECURITY ERROR: JWT_SECRET environment variable is missing. Halting startup.")
	}

	claims := jwt.MapClaims{
		"user_id": userID,
		"role":    role,
		"exp":     time.Now().Add(24 * time.Hour).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

func (s *authService) VerifyGoogleToken(ctx context.Context, idToken string) (map[string]interface{}, error) {
	clientID := os.Getenv("GOOGLE_CLIENT_ID")
	if clientID == "" {
		log.Fatal("CRITICAL SECURITY ERROR: GOOGLE_CLIENT_ID environment variable is missing. Halting startup.")
	}

	payload, err := idtoken.Validate(ctx, idToken, clientID)
	if err != nil {
		return nil, err
	}

	return payload.Claims, nil
}
