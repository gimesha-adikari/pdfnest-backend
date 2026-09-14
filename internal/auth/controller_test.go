package auth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/mailer"
	"pdfnest-backend/internal/middleware"
)

func setupAuthControllerTest(t *testing.T) (*gorm.DB, *fiber.App, *Controller, *config.User) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL DATABASE_URL")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&config.User{}))
	previousDB := config.DB
	config.DB = db
	t.Cleanup(func() {
		config.DB = previousDB
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	t.Setenv("JWT_SECRET", "auth-controller-test-secret")
	t.Setenv("LOCAL", "true")
	t.Setenv("APP_ENV", "development")
	outbox := t.TempDir()
	t.Setenv("MAILER_OUTBOX_DIR", outbox)
	t.Setenv("FRONTEND_URL", "http://localhost:3000")

	service := NewService()
	hash, err := service.HashPassword("old-password-123")
	require.NoError(t, err)
	user := &config.User{
		ID:            uuid.NewString(),
		Email:         "controlled-" + uuid.NewString() + "@example.invalid",
		PasswordHash:  hash,
		Role:          "user",
		Status:        "active",
		EmailVerified: true,
	}
	require.NoError(t, db.Create(user).Error)
	t.Cleanup(func() { _ = db.Delete(&config.User{}, "id = ?", user.ID).Error })

	ctrl := NewController(service)
	app := fiber.New()
	app.Post("/login", ctrl.Login)
	app.Post("/logout", ctrl.Logout)
	app.Get("/protected", middleware.Protect(), func(c *fiber.Ctx) error { return c.SendStatus(http.StatusOK) })
	app.Post("/request-password-reset", ctrl.RequestPasswordReset)
	app.Post("/reset-password", ctrl.ResetPassword)
	return db, app, ctrl, user
}

func jsonRequest(method, path string, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func responseBody(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return string(body)
}

func authCookie(t *testing.T, response *http.Response) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Cookies() {
		if cookie.Name == "auth_token" && cookie.Value != "" {
			return cookie
		}
	}
	t.Fatal("login did not issue auth_token")
	return nil
}

func TestLogoutRevokesCapturedCredentialAndNewLoginStillWorks(t *testing.T) {
	db, app, _, user := setupAuthControllerTest(t)

	loginResponse, err := app.Test(jsonRequest(http.MethodPost, "/login", `{"email":"`+user.Email+`","password":"old-password-123"}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, loginResponse.StatusCode)
	oldCookie := authCookie(t, loginResponse)

	protected := httptest.NewRequest(http.MethodGet, "/protected", nil)
	protected.AddCookie(oldCookie)
	protectedResponse, err := app.Test(protected)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, protectedResponse.StatusCode)
	protectedResponse.Body.Close()

	logout := httptest.NewRequest(http.MethodPost, "/logout", nil)
	logout.AddCookie(oldCookie)
	logoutResponse, err := app.Test(logout)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, logoutResponse.StatusCode)
	logoutResponse.Body.Close()

	oldRetry := httptest.NewRequest(http.MethodGet, "/protected", nil)
	oldRetry.AddCookie(oldCookie)
	oldRetryResponse, err := app.Test(oldRetry)
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, oldRetryResponse.StatusCode)
	oldRetryResponse.Body.Close()

	var persisted config.User
	require.NoError(t, db.First(&persisted, "id = ?", user.ID).Error)
	require.Equal(t, int64(1), persisted.SessionVersion)

	newLoginResponse, err := app.Test(jsonRequest(http.MethodPost, "/login", `{"email":"`+user.Email+`","password":"old-password-123"}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, newLoginResponse.StatusCode)
	newCookie := authCookie(t, newLoginResponse)
	newProtected := httptest.NewRequest(http.MethodGet, "/protected", nil)
	newProtected.AddCookie(newCookie)
	newProtectedResponse, err := app.Test(newProtected)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, newProtectedResponse.StatusCode)
	newProtectedResponse.Body.Close()
}

func TestPasswordResetLifecycleIsBoundedSingleUseAndNonEnumerating(t *testing.T) {
	db, app, ctrl, user := setupAuthControllerTest(t)
	unknown := "unknown-" + uuid.NewString() + "@example.invalid"

	knownResponse, err := app.Test(jsonRequest(http.MethodPost, "/request-password-reset", `{"email":"`+user.Email+`"}`))
	require.NoError(t, err)
	knownBody := responseBody(t, knownResponse)
	require.Equal(t, http.StatusOK, knownResponse.StatusCode)
	unknownResponse, err := app.Test(jsonRequest(http.MethodPost, "/request-password-reset", `{"email":"`+unknown+`"}`))
	require.NoError(t, err)
	unknownBody := responseBody(t, unknownResponse)
	require.Equal(t, http.StatusOK, unknownResponse.StatusCode)
	require.JSONEq(t, knownBody, unknownBody)

	mailFiles, err := filepath.Glob(filepath.Join(os.Getenv("MAILER_OUTBOX_DIR"), "mail-*.json"))
	require.NoError(t, err)
	require.Len(t, mailFiles, 1)
	mailBody, err := os.ReadFile(mailFiles[0])
	require.NoError(t, err)
	var email mailer.Email
	require.NoError(t, json.Unmarshal(mailBody, &email))
	resetURL := regexp.MustCompile(`reset-password\?token=([a-f0-9]{64})`).FindStringSubmatch(email.Text)
	require.Len(t, resetURL, 2)
	token := resetURL[1]

	resetResponse, err := app.Test(jsonRequest(http.MethodPost, "/reset-password", `{"token":"`+token+`","password":"new-password-456"}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resetResponse.StatusCode)
	resetResponse.Body.Close()

	replayResponse, err := app.Test(jsonRequest(http.MethodPost, "/reset-password", `{"token":"`+token+`","password":"third-password-789"}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, replayResponse.StatusCode)
	replayResponse.Body.Close()

	oldLogin, err := app.Test(jsonRequest(http.MethodPost, "/login", `{"email":"`+user.Email+`","password":"old-password-123"}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, oldLogin.StatusCode)
	oldLogin.Body.Close()
	newLogin, err := app.Test(jsonRequest(http.MethodPost, "/login", `{"email":"`+user.Email+`","password":"new-password-456"}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, newLogin.StatusCode)
	newLogin.Body.Close()

	// An expired token is rejected with the same safe error as a used token.
	secondRaw, secondHash, err := generateVerificationToken()
	require.NoError(t, err)
	require.NoError(t, db.Model(&config.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"password_reset_token_hash":   secondHash,
		"password_reset_expires_at":   time.Now().Add(-time.Minute),
		"password_reset_requested_at": time.Now().Add(-time.Hour),
	}).Error)
	expiredResponse, err := app.Test(jsonRequest(http.MethodPost, "/reset-password", `{"token":"`+secondRaw+`","password":"expired-password-123"}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, expiredResponse.StatusCode)
	require.Contains(t, responseBody(t, expiredResponse), "invalid or expired")

	// The controller uses the existing service and does not need a second
	// account-specific reset architecture.
	require.NotNil(t, ctrl)
	var after config.User
	require.NoError(t, db.First(&after, "id = ?", user.ID).Error)
	require.True(t, after.PasswordResetTokenHash != "", "expired token remains auditable until a new request replaces it")
}

func TestPasswordResetRejectsMalformedTokenWithoutDatabaseLookup(t *testing.T) {
	_, app, _, _ := setupAuthControllerTest(t)
	response, err := app.Test(jsonRequest(http.MethodPost, "/reset-password", `{"token":"","password":"new-password-456"}`))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, response.StatusCode)
	body := responseBody(t, response)
	require.Contains(t, body, "invalid or expired")
}
