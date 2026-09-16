package auth

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/config"
)

// TestResendVerification_AUTH004_UnknownEmailReturnsGenericSuccess verifies that
// requesting verification resend for an unknown/unregistered email returns HTTP 200
// with a generic success message, preventing account enumeration (AUTH-004).
func TestResendVerification_AUTH004_UnknownEmailReturnsGenericSuccess(t *testing.T) {
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

	// Ensure no phantom user was created in the database
	var count int64
	require.NoError(t, config.DB.Model(&config.User{}).Where("email = ?", unknownEmail).Count(&count).Error)
	require.Equal(t, int64(0), count)
}

// TestResendVerification_AUTH004_PendingUserReceivesNewToken verifies that
// a registered but unverified user successfully receives a refreshed verification token
// and HTTP 200 response with identical payload structure.
func TestResendVerification_AUTH004_PendingUserReceivesNewToken(t *testing.T) {
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

// TestResendVerification_AUTH004_AlreadyVerifiedReturnsBadRequest verifies that
// an already-verified user receives the standard 400 error.
func TestResendVerification_AUTH004_AlreadyVerifiedReturnsBadRequest(t *testing.T) {
	app, _, user, validToken := setupEmailVerificationMatrixTest(t)

	// Verify the user first
	reqVerify := jsonRequest(http.MethodPost, "/verify-email?token="+validToken, "")
	respVerify, err := app.Test(reqVerify)
	require.NoError(t, err)
	respVerify.Body.Close()
	require.Equal(t, http.StatusOK, respVerify.StatusCode)

	// Request resend for already verified email
	req := jsonRequest(http.MethodPost, "/resend-verification", `{"email":"`+user.Email+`"}`)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, "Email is already verified", body["error"])
}

// TestResendVerification_AUTH004_InvalidEmailFormat verifies input validation.
func TestResendVerification_AUTH004_InvalidEmailFormat(t *testing.T) {
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

// TestResendVerification_AUTH004_MalformedPayload verifies malformed JSON handling.
func TestResendVerification_AUTH004_MalformedPayload(t *testing.T) {
	app, _, _, _ := setupEmailVerificationMatrixTest(t)

	req := jsonRequest(http.MethodPost, "/resend-verification", `{malformed json`)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, "Invalid request payload", body["error"])
}

// TestResendVerification_AUTH004_DatabaseUnavailable verifies service unavailable response.
func TestResendVerification_AUTH004_DatabaseUnavailable(t *testing.T) {
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
