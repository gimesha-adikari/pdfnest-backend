// Package authn verifies session credentials against current account state.
package authn

import (
	"context"
	"errors"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"os"
	"pdfnest-backend/config"
	"strings"
	"time"
)

var ErrInvalid = errors.New("invalid session")
var ErrAccountDisabled = errors.New("account is unavailable")
var ErrUnavailable = errors.New("authentication service is unavailable")

func Verify(ctx context.Context, raw string) (*config.User, error) {
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		return nil, ErrUnavailable
	}
	token, err := jwt.Parse(raw, func(*jwt.Token) (any, error) { return []byte(secret), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
	if err != nil || !token.Valid {
		return nil, ErrInvalid
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, ErrInvalid
	}
	id, ok := claims["user_id"].(string)
	if !ok {
		return nil, ErrInvalid
	}
	role, ok := claims["role"].(string)
	if !ok || strings.TrimSpace(role) == "" {
		return nil, ErrInvalid
	}
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrInvalid
	}
	if config.DB == nil {
		return nil, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var user config.User
	err = config.DB.WithContext(ctx).Select("id", "role", "status", "email_verified").First(&user, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrAccountDisabled
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	if user.Status != "active" || !user.EmailVerified {
		return nil, ErrAccountDisabled
	}
	return &user, nil
}
