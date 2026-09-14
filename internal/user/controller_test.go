package user

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/auth"
	"pdfnest-backend/internal/middleware"
)

func TestExportDataIsAuthenticatedAndScopedToCurrentAccount(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL DATABASE_URL")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&config.User{},
		&config.Subscription{},
		&config.UserSetting{},
		&config.UsageLog{},
		&config.Transaction{},
	))
	previousDB := config.DB
	config.DB = db
	t.Cleanup(func() {
		config.DB = previousDB
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	t.Setenv("JWT_SECRET", "export-controller-test-secret")

	makeUser := func(email string) config.User {
		user := config.User{ID: uuid.NewString(), Email: email, Role: "user", Status: "active", EmailVerified: true}
		require.NoError(t, db.Create(&user).Error)
		t.Cleanup(func() { _ = db.Delete(&config.User{}, "id = ?", user.ID).Error })
		return user
	}
	owner := makeUser("owner-" + uuid.NewString() + "@example.invalid")
	other := makeUser("other-" + uuid.NewString() + "@example.invalid")

	service := NewController()
	app := fiber.New()
	app.Get("/export", middleware.Protect(), service.ExportData)

	anonymous, err := app.Test(httptest.NewRequest(http.MethodGet, "/export", nil))
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, anonymous.StatusCode)
	anonymous.Body.Close()

	issue := func(user config.User) *http.Cookie {
		token, err := auth.NewService().GenerateToken(user.ID, user.Role, user.SessionVersion)
		require.NoError(t, err)
		return &http.Cookie{Name: "auth_token", Value: token}
	}

	exportRequest := httptest.NewRequest(http.MethodGet, "/export", nil)
	exportRequest.AddCookie(issue(owner))
	exportResponse, err := app.Test(exportRequest)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, exportResponse.StatusCode)
	require.Contains(t, exportResponse.Header.Get("Content-Disposition"), owner.ID)
	body, err := io.ReadAll(exportResponse.Body)
	require.NoError(t, err)
	require.NoError(t, exportResponse.Body.Close())
	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))
	userPayload, ok := payload["user"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, owner.ID, userPayload["id"])
	require.Equal(t, owner.Email, userPayload["email"])
	require.NotEqual(t, other.ID, userPayload["id"])
	require.NotContains(t, string(body), other.Email)
}
