// Package authn verifies session credentials against current account state.
package authn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"pdfnest-backend/config"
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
	sessionVersion, err := sessionVersionClaim(claims)
	if err != nil {
		return nil, ErrInvalid
	}
	if config.DB == nil {
		return nil, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var user config.User
	err = config.DB.WithContext(ctx).Select("id", "role", "status", "email_verified", "session_version").First(&user, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrAccountDisabled
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	if user.Status != "active" || !user.EmailVerified {
		return nil, ErrAccountDisabled
	}
	if user.SessionVersion != sessionVersion {
		return nil, ErrInvalid
	}
	return &user, nil
}

// sessionVersionClaim keeps tokens issued before the session-version rollout
// valid at version zero while rejecting malformed or fractional values. JWT
// JSON numbers are decoded as float64 by jwt.MapClaims.
func sessionVersionClaim(claims jwt.MapClaims) (int64, error) {
	raw, ok := claims["session_version"]
	if !ok {
		return 0, nil
	}

	var version int64
	switch value := raw.(type) {
	case float64:
		if value < 0 || value > float64(math.MaxInt64) || math.Trunc(value) != value {
			return 0, fmt.Errorf("invalid session version")
		}
		version = int64(value)
	case float32:
		if value < 0 || math.Trunc(float64(value)) != float64(value) {
			return 0, fmt.Errorf("invalid session version")
		}
		version = int64(value)
	case int:
		version = int64(value)
	case int64:
		version = value
	case int32:
		version = int64(value)
	case json.Number:
		parsed, err := strconv.ParseInt(string(value), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid session version: %w", err)
		}
		version = parsed
	default:
		return 0, fmt.Errorf("invalid session version type")
	}

	if version < 0 {
		return 0, fmt.Errorf("invalid session version")
	}
	return version, nil
}
