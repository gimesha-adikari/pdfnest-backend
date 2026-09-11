package billing

import (
	"bytes"
	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"pdfnest-backend/config"
	"testing"
	"time"
)

func TestMockBillingCannotGrantEntitlements(t *testing.T) {
	db := setupTestDB(t)
	require.NoError(t, db.AutoMigrate(&config.Transaction{}))
	id, subID := createTestUserAndSub(db)
	require.NoError(t, db.Model(&config.User{}).Where("id = ?", id).Updates(map[string]any{"status": "active", "email_verified": true}).Error)
	t.Setenv("JWT_SECRET", "audit-billing-test-secret")
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"user_id": id, "role": "user", "exp": time.Now().Add(time.Hour).Unix()}).SignedString([]byte("audit-billing-test-secret"))
	require.NoError(t, err)
	app := fiber.New()
	RegisterRoutes(app, NewController())
	for _, path := range []string{"/billing/upgrade-mock", "/billing/buy-credits-mock"} {
		req := httptest.NewRequest("POST", path, bytes.NewBufferString(`{"tier":"pro","credits":500}`))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "auth_token", Value: token})
		resp, err := app.Test(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, 404, resp.StatusCode, path)
	}
	var sub config.Subscription
	require.NoError(t, db.First(&sub, "id = ?", subID).Error)
	require.Equal(t, "free", sub.Tier)
	require.Zero(t, sub.CustomCredits)
}
