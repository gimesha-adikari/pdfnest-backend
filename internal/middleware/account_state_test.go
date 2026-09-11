package middleware

import (
	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"net/http"
	"net/http/httptest"
	"os"
	"pdfnest-backend/config"
	"pdfnest-backend/internal/identity"
	"testing"
	"time"
)

func activeAccount(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL DATABASE_URL")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&config.User{}))
	previous := config.DB
	config.DB = db
	t.Cleanup(func() { config.DB = previous; sqlDB, _ := db.DB(); sqlDB.Close() })
	id := uuid.NewString()
	require.NoError(t, db.Create(&config.User{ID: id, Email: id + "@example.invalid", Role: "admin", Status: "active", EmailVerified: true}).Error)
	t.Cleanup(func() { db.Delete(&config.User{}, "id = ?", id) })
	return db, id
}

func TestCurrentAccountStateOverridesOldToken(t *testing.T) {
	db, id := activeAccount(t)
	t.Setenv("JWT_SECRET", "test-secret")
	token := makeToken("test-secret", jwt.MapClaims{"user_id": id, "role": "admin", "exp": time.Now().Add(time.Hour).Unix()})
	app := fiber.New()
	app.Get("/protected", Protect(), RequireAdmin(), func(c *fiber.Ctx) error { return c.SendStatus(200) })
	app.Get("/identity", identity.Resolve(nil), func(c *fiber.Ctx) error { return c.SendStatus(200) })
	request := func(path string) int {
		req := httptest.NewRequest("GET", path, nil)
		req.AddCookie(&http.Cookie{Name: "auth_token", Value: token})
		resp, err := app.Test(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	require.Equal(t, 200, request("/protected"))
	require.Equal(t, 200, request("/identity"))
	require.NoError(t, db.Model(&config.User{}).Where("id = ?", id).Update("role", "user").Error)
	require.Equal(t, 403, request("/protected"), "old admin claim cannot override demotion")
	require.NoError(t, db.Model(&config.User{}).Where("id = ?", id).Update("status", "banned").Error)
	require.Equal(t, 401, request("/protected"))
	require.Equal(t, 401, request("/identity"))
	require.NoError(t, db.Delete(&config.User{}, "id = ?", id).Error)
	require.Equal(t, 401, request("/protected"))
	require.Equal(t, 401, request("/identity"))
}

func TestProtectRejectsMissingExpirationAndMissingConfiguration(t *testing.T) {
	app := newTestApp()
	t.Setenv("JWT_SECRET", "test-secret")
	token := makeToken("test-secret", jwt.MapClaims{"user_id": uuid.NewString(), "role": "user"})
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: token})
	resp, err := app.Test(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, 401, resp.StatusCode)
	t.Setenv("JWT_SECRET", "")
	resp, err = app.Test(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, 503, resp.StatusCode)
}
