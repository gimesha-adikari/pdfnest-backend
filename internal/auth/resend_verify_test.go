package auth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"pdfnest-backend/config"
)

// Scenario A: unknown email returns generic 200 and does not create phantom user
func TestResendVerification_AUTH004_A_UnknownEmailReturnsGenericSuccess(t *testing.T) {
	app, _, _, _ := setupEmailVerificationMatrixTest(t)

	unknownEmail := "unknown-" + uuid.NewString() + "@example.invalid"
	req := jsonRequest(http.MethodPost, "/resend-verification", `{"email":"`+unknownEmail+`"}`)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, true, body["success"])
	require.Equal(t, "Verification email sent", body["message"])

	// Ensure 0 phantom users created in DB
	var count int64
	require.NoError(t, config.DB.Model(&config.User{}).Where("email = ?", unknownEmail).Count(&count).Error)
	require.Equal(t, int64(0), count)
}

// Scenario B: pending registered email returns generic 200, replaces token, and refreshes expiration
func TestResendVerification_AUTH004_B_PendingUserRefreshesToken(t *testing.T) {
	app, _, user, oldToken := setupEmailVerificationMatrixTest(t)

	oldHash := hashVerificationToken(oldToken)
	req := jsonRequest(http.MethodPost, "/resend-verification", `{"email":"`+user.Email+`"}`)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, true, body["success"])
	require.Equal(t, "Verification email sent", body["message"])

	var updated config.User
	require.NoError(t, config.DB.First(&updated, "id = ?", user.ID).Error)
	require.False(t, updated.EmailVerified)
	require.NotEmpty(t, updated.EmailVerifyTokenHash)
	require.NotEqual(t, oldHash, updated.EmailVerifyTokenHash)
	require.True(t, updated.EmailVerifyExpiresAt.After(time.Now()))
}

// Scenario C: already-verified registered email returns generic 200, verified remains true, state unchanged
func TestResendVerification_AUTH004_C_AlreadyVerifiedReturnsGenericSuccess(t *testing.T) {
	app, _, user, validToken := setupEmailVerificationMatrixTest(t)

	// Verify user first
	reqVerify := httptest.NewRequest(http.MethodGet, "/verify-email?token="+validToken, nil)
	respVerify, err := app.Test(reqVerify)
	require.NoError(t, err)
	respVerify.Body.Close()
	require.Equal(t, http.StatusOK, respVerify.StatusCode)

	var before config.User
	require.NoError(t, config.DB.First(&before, "id = ?", user.ID).Error)
	require.True(t, before.EmailVerified)

	// Request resend for already-verified email
	req := jsonRequest(http.MethodPost, "/resend-verification", `{"email":"`+user.Email+`"}`)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, true, body["success"])
	require.Equal(t, "Verification email sent", body["message"])

	// Verified status remains true and token/state is NOT altered by resend
	var after config.User
	require.NoError(t, config.DB.First(&after, "id = ?", user.ID).Error)
	require.True(t, after.EmailVerified)
	require.Equal(t, before.EmailVerifyTokenHash, after.EmailVerifyTokenHash)
	require.Equal(t, before.EmailVerifyExpiresAt.Unix(), after.EmailVerifyExpiresAt.Unix())
}

// Scenario D: exact response equivalence across unknown, pending, and verified states
func TestResendVerification_AUTH004_D_ExactResponseEquivalence(t *testing.T) {
	app, _, pendingUser, _ := setupEmailVerificationMatrixTest(t)

	// Create a verified user in the same DB
	verifiedUser := &config.User{
		ID:            uuid.NewString(),
		Email:         "verified-" + uuid.NewString() + "@example.invalid",
		Role:          "user",
		Status:        "active",
		EmailVerified: true,
	}
	require.NoError(t, config.DB.Create(verifiedUser).Error)
	t.Cleanup(func() {
		_ = config.DB.Delete(&config.User{}, "id = ?", verifiedUser.ID).Error
	})

	unknownEmail := "unknown-" + uuid.NewString() + "@example.invalid"

	// 1. Unknown
	reqUnknown := jsonRequest(http.MethodPost, "/resend-verification", `{"email":"`+unknownEmail+`"}`)
	respUnknown, err := app.Test(reqUnknown)
	require.NoError(t, err)
	defer respUnknown.Body.Close()
	bodyUnknownBytes, err := io.ReadAll(respUnknown.Body)
	require.NoError(t, err)

	// 2. Pending
	reqPending := jsonRequest(http.MethodPost, "/resend-verification", `{"email":"`+pendingUser.Email+`"}`)
	respPending, err := app.Test(reqPending)
	require.NoError(t, err)
	defer respPending.Body.Close()
	bodyPendingBytes, err := io.ReadAll(respPending.Body)
	require.NoError(t, err)

	// 3. Verified
	reqVerified := jsonRequest(http.MethodPost, "/resend-verification", `{"email":"`+verifiedUser.Email+`"}`)
	respVerified, err := app.Test(reqVerified)
	require.NoError(t, err)
	defer respVerified.Body.Close()
	bodyVerifiedBytes, err := io.ReadAll(respVerified.Body)
	require.NoError(t, err)

	// Compare HTTP Status Codes
	require.Equal(t, http.StatusOK, respUnknown.StatusCode)
	require.Equal(t, respUnknown.StatusCode, respPending.StatusCode)
	require.Equal(t, respPending.StatusCode, respVerified.StatusCode)

	// Compare Content-Type headers
	require.Equal(t, "application/json", respUnknown.Header.Get("Content-Type"))
	require.Equal(t, respUnknown.Header.Get("Content-Type"), respPending.Header.Get("Content-Type"))
	require.Equal(t, respPending.Header.Get("Content-Type"), respVerified.Header.Get("Content-Type"))

	// Compare decoded JSON bodies
	var jsonUnknown, jsonPending, jsonVerified map[string]interface{}
	require.NoError(t, json.Unmarshal(bodyUnknownBytes, &jsonUnknown))
	require.NoError(t, json.Unmarshal(bodyPendingBytes, &jsonPending))
	require.NoError(t, json.Unmarshal(bodyVerifiedBytes, &jsonVerified))

	require.Equal(t, map[string]interface{}{"success": true, "message": "Verification email sent"}, jsonUnknown)
	require.Equal(t, jsonUnknown, jsonPending)
	require.Equal(t, jsonPending, jsonVerified)
}

// Scenario E: invalid email syntax returns 400
func TestResendVerification_AUTH004_E_InvalidEmailSyntax(t *testing.T) {
	app, _, _, _ := setupEmailVerificationMatrixTest(t)

	cases := []struct {
		name    string
		payload string
	}{
		{"empty email", `{"email":""}`},
		{"malformed email", `{"email":"not-an-email"}`},
		{"missing domain", `{"email":"user@"}`},
		{"spaces only", `{"email":"   "}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := jsonRequest(http.MethodPost, "/resend-verification", tc.payload)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			require.Equal(t, http.StatusBadRequest, resp.StatusCode)
			var body map[string]interface{}
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
			require.Equal(t, "Invalid email address", body["error"])
		})
	}
}

// Scenario F: malformed JSON returns 400
func TestResendVerification_AUTH004_F_MalformedJSON(t *testing.T) {
	app, _, _, _ := setupEmailVerificationMatrixTest(t)

	req := jsonRequest(http.MethodPost, "/resend-verification", `{not-valid-json`)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, "Invalid request payload", body["error"])
}

// Scenario G: DB unavailable (config.DB == nil) returns 503
func TestResendVerification_AUTH004_G_DatabaseUnavailable(t *testing.T) {
	app, _, _, _ := setupEmailVerificationMatrixTest(t)

	origDB := config.DB
	config.DB = nil
	defer func() { config.DB = origDB }()

	req := jsonRequest(http.MethodPost, "/resend-verification", `{"email":"test@example.com"}`)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, "Authentication is temporarily unavailable", body["error"])
}

// Scenario H: unexpected DB query failure returns 5xx
func TestResendVerification_AUTH004_H_UnexpectedDatabaseError(t *testing.T) {
	app, _, _, _ := setupEmailVerificationMatrixTest(t)

	origDB := config.DB
	defer func() { config.DB = origDB }()

	// Create a valid temporary connection and close its underlying connection pool
	dsn := os.Getenv("DATABASE_URL")
	tempDB, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := tempDB.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	config.DB = tempDB

	req := jsonRequest(http.MethodPost, "/resend-verification", `{"email":"user@example.com"}`)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, "Failed checking user", body["error"])
}
