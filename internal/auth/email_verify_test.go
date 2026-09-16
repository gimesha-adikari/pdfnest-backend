package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/config"
)

func setupEmailVerificationMatrixTest(t *testing.T) (*fiber.App, *Controller, *config.User, string) {
	t.Helper()
	db, app, ctrl, _ := setupAuthControllerTest(t)

	require.NoError(t, db.AutoMigrate(&config.User{}, &config.Subscription{}))

	app.Get("/verify-email", ctrl.VerifyEmail)
	app.Post("/verify-email", ctrl.VerifyEmail)
	app.Post("/resend-verification", ctrl.ResendVerification)

	rawToken, tokenHash, err := generateVerificationToken()
	require.NoError(t, err)

	unverifiedUser := &config.User{
		ID:                   uuid.NewString(),
		Email:                "unverified-" + uuid.NewString() + "@example.invalid",
		Role:                 "user",
		Status:               "pending",
		EmailVerified:        false,
		EmailVerifyTokenHash: tokenHash,
		EmailVerifyExpiresAt: time.Now().Add(30 * time.Minute),
	}
	require.NoError(t, db.Create(unverifiedUser).Error)
	t.Cleanup(func() {
		_ = db.Delete(&config.Subscription{}, "user_id = ?", unverifiedUser.ID).Error
		_ = db.Delete(&config.User{}, "id = ?", unverifiedUser.ID).Error
	})

	return app, ctrl, unverifiedUser, rawToken
}

// Test A: first valid click -> success
func TestEmailVerify_A_FirstValidClick(t *testing.T) {
	app, _, user, rawToken := setupEmailVerificationMatrixTest(t)

	req := httptest.NewRequest(http.MethodGet, "/verify-email?token="+rawToken, nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, true, body["success"])
	require.Equal(t, "Email verified successfully", body["message"])

	var persisted config.User
	require.NoError(t, config.DB.First(&persisted, "id = ?", user.ID).Error)
	require.True(t, persisted.EmailVerified)
	require.Equal(t, "active", persisted.Status)
}

// Test B: immediate second click -> safe deterministic response
func TestEmailVerify_B_ImmediateSecondClick(t *testing.T) {
	app, _, user, rawToken := setupEmailVerificationMatrixTest(t)

	// First click
	req1 := httptest.NewRequest(http.MethodGet, "/verify-email?token="+rawToken, nil)
	resp1, err := app.Test(req1)
	require.NoError(t, err)
	resp1.Body.Close()
	require.Equal(t, http.StatusOK, resp1.StatusCode)

	// Immediate second click (duplicate request)
	req2 := httptest.NewRequest(http.MethodGet, "/verify-email?token="+rawToken, nil)
	resp2, err := app.Test(req2)
	require.NoError(t, err)
	defer resp2.Body.Close()

	require.Equal(t, http.StatusOK, resp2.StatusCode)
	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&body))
	require.Equal(t, true, body["success"])
	require.Equal(t, "Email verified successfully", body["message"])

	var persisted config.User
	require.NoError(t, config.DB.First(&persisted, "id = ?", user.ID).Error)
	require.True(t, persisted.EmailVerified)
}

// Test C: concurrent same-token clicks -> one logical verification outcome
func TestEmailVerify_C_ConcurrentSameTokenClicks(t *testing.T) {
	app, _, user, rawToken := setupEmailVerificationMatrixTest(t)

	const concurrency = 5
	var wg sync.WaitGroup
	statuses := make([]int, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/verify-email?token="+rawToken, nil)
			resp, err := app.Test(req)
			if err == nil {
				statuses[idx] = resp.StatusCode
				resp.Body.Close()
			}
		}(i)
	}
	wg.Wait()

	for _, code := range statuses {
		require.Equal(t, http.StatusOK, code, "every concurrent request with valid token must succeed")
	}

	var persisted config.User
	require.NoError(t, config.DB.First(&persisted, "id = ?", user.ID).Error)
	require.True(t, persisted.EmailVerified)
	require.Equal(t, "active", persisted.Status)

	var subCount int64
	require.NoError(t, config.DB.Model(&config.Subscription{}).Where("user_id = ?", user.ID).Count(&subCount).Error)
	require.Equal(t, int64(1), subCount, "concurrent requests must ensure exactly 1 free subscription is created")
}

// Test D: expired token -> rejected
func TestEmailVerify_D_ExpiredTokenRejected(t *testing.T) {
	app, _, user, rawToken := setupEmailVerificationMatrixTest(t)

	// Set token expiration in the past
	require.NoError(t, config.DB.Model(&config.User{}).Where("id = ?", user.ID).Update("email_verify_expires_at", time.Now().Add(-1*time.Minute)).Error)

	req := httptest.NewRequest(http.MethodGet, "/verify-email?token="+rawToken, nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, "Verification token expired", body["error"])

	var persisted config.User
	require.NoError(t, config.DB.First(&persisted, "id = ?", user.ID).Error)
	require.False(t, persisted.EmailVerified, "expired token must not verify user")
}

// Test E: malformed token -> rejected
func TestEmailVerify_E_MalformedTokenRejected(t *testing.T) {
	app, _, _, _ := setupEmailVerificationMatrixTest(t)

	// Empty token
	req1 := httptest.NewRequest(http.MethodGet, "/verify-email?token=", nil)
	resp1, err := app.Test(req1)
	require.NoError(t, err)
	defer resp1.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp1.StatusCode)

	// Random nonexistent token
	req2 := httptest.NewRequest(http.MethodGet, "/verify-email?token=completely-bogus-token-string", nil)
	resp2, err := app.Test(req2)
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp2.StatusCode)
	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&body))
	require.Equal(t, "Invalid or expired token", body["error"])
}

// Test F: token for another user -> rejected for victim
func TestEmailVerify_F_TokenForAnotherUser(t *testing.T) {
	app, _, user1, rawToken1 := setupEmailVerificationMatrixTest(t)

	_, tokenHash2, err := generateVerificationToken()
	require.NoError(t, err)
	user2 := &config.User{
		ID:                   uuid.NewString(),
		Email:                "unverified-2-" + uuid.NewString() + "@example.invalid",
		Role:                 "user",
		Status:               "pending",
		EmailVerified:        false,
		EmailVerifyTokenHash: tokenHash2,
		EmailVerifyExpiresAt: time.Now().Add(30 * time.Minute),
	}
	require.NoError(t, config.DB.Create(user2).Error)
	t.Cleanup(func() {
		_ = config.DB.Delete(&config.Subscription{}, "user_id = ?", user2.ID).Error
		_ = config.DB.Delete(&config.User{}, "id = ?", user2.ID).Error
	})

	// Use Token 1
	req := httptest.NewRequest(http.MethodGet, "/verify-email?token="+rawToken1, nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify user1 is verified, user2 is NOT verified
	var p1, p2 config.User
	require.NoError(t, config.DB.First(&p1, "id = ?", user1.ID).Error)
	require.NoError(t, config.DB.First(&p2, "id = ?", user2.ID).Error)
	require.True(t, p1.EmailVerified)
	require.False(t, p2.EmailVerified, "user 2 must not be verified by user 1's token")
}

// Test G: resend creates intended token behavior
func TestEmailVerify_G_ResendCreatesIntendedToken(t *testing.T) {
	app, _, user, _ := setupEmailVerificationMatrixTest(t)

	resendReq := jsonRequest(http.MethodPost, "/resend-verification", `{"email":"`+user.Email+`"}`)
	resendResp, err := app.Test(resendReq)
	require.NoError(t, err)
	defer resendResp.Body.Close()
	require.Equal(t, http.StatusOK, resendResp.StatusCode)

	var persisted config.User
	require.NoError(t, config.DB.First(&persisted, "id = ?", user.ID).Error)
	require.NotEmpty(t, persisted.EmailVerifyTokenHash)
	require.True(t, persisted.EmailVerifyExpiresAt.After(time.Now()))
}

// Test H: old token after resend -> explicitly rejected
func TestEmailVerify_H_OldTokenAfterResendRejected(t *testing.T) {
	app, _, user, oldToken := setupEmailVerificationMatrixTest(t)

	// Resend verification
	resendReq := jsonRequest(http.MethodPost, "/resend-verification", `{"email":"`+user.Email+`"}`)
	resendResp, err := app.Test(resendReq)
	require.NoError(t, err)
	resendResp.Body.Close()
	require.Equal(t, http.StatusOK, resendResp.StatusCode)

	// Try using the OLD token
	req := httptest.NewRequest(http.MethodGet, "/verify-email?token="+oldToken, nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, "Invalid or expired token", body["error"])

	var persisted config.User
	require.NoError(t, config.DB.First(&persisted, "id = ?", user.ID).Error)
	require.False(t, persisted.EmailVerified, "old token after resend must not verify user")
}

// Test I: already-verified account + unrelated bogus token -> still rejected
func TestEmailVerify_I_AlreadyVerifiedAccountBogusTokenRejected(t *testing.T) {
	app, _, user, validToken := setupEmailVerificationMatrixTest(t)

	// First verify successfully with valid token
	reqValid := httptest.NewRequest(http.MethodGet, "/verify-email?token="+validToken, nil)
	respValid, err := app.Test(reqValid)
	require.NoError(t, err)
	respValid.Body.Close()
	require.Equal(t, http.StatusOK, respValid.StatusCode)

	var persisted config.User
	require.NoError(t, config.DB.First(&persisted, "id = ?", user.ID).Error)
	require.True(t, persisted.EmailVerified)

	// Now attempt verification with unrelated bogus token
	reqBogus := httptest.NewRequest(http.MethodGet, "/verify-email?token=bogus-arbitrary-unrelated-token-12345", nil)
	respBogus, err := app.Test(reqBogus)
	require.NoError(t, err)
	defer respBogus.Body.Close()

	require.Equal(t, http.StatusBadRequest, respBogus.StatusCode)
	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(respBogus.Body).Decode(&body))
	require.Equal(t, "Invalid or expired token", body["error"])
}

// Test J: resend on already-verified account -> rejected
func TestEmailVerify_J_ResendOnAlreadyVerifiedRejected(t *testing.T) {
	app, _, user, validToken := setupEmailVerificationMatrixTest(t)

	// First verify successfully
	reqValid := httptest.NewRequest(http.MethodGet, "/verify-email?token="+validToken, nil)
	respValid, err := app.Test(reqValid)
	require.NoError(t, err)
	respValid.Body.Close()
	require.Equal(t, http.StatusOK, respValid.StatusCode)

	// Resend verification on verified account
	resendReq := jsonRequest(http.MethodPost, "/resend-verification", `{"email":"`+user.Email+`"}`)
	resendResp, err := app.Test(resendReq)
	require.NoError(t, err)
	defer resendResp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resendResp.StatusCode)
	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(resendResp.Body).Decode(&body))
	require.Equal(t, "Email is already verified", body["error"])
}

// Test K: same successfully-used token AFTER expiry -> rejected with 400 "Verification token expired"
func TestEmailVerify_K_SameTokenAfterExpiryRejected(t *testing.T) {
	app, _, user, validToken := setupEmailVerificationMatrixTest(t)

	// 1. Create pending user with valid token and verify successfully
	reqValid := httptest.NewRequest(http.MethodGet, "/verify-email?token="+validToken, nil)
	respValid, err := app.Test(reqValid)
	require.NoError(t, err)
	respValid.Body.Close()
	require.Equal(t, http.StatusOK, respValid.StatusCode)

	var persisted config.User
	require.NoError(t, config.DB.First(&persisted, "id = ?", user.ID).Error)
	require.True(t, persisted.EmailVerified)

	// 2. Move EmailVerifyExpiresAt into the past
	require.NoError(t, config.DB.Model(&config.User{}).Where("id = ?", user.ID).Update("email_verify_expires_at", time.Now().Add(-5*time.Minute)).Error)

	// 3. Call /verify-email again using THE SAME TOKEN
	reqExpired := httptest.NewRequest(http.MethodGet, "/verify-email?token="+validToken, nil)
	respExpired, err := app.Test(reqExpired)
	require.NoError(t, err)
	defer respExpired.Body.Close()

	// 4. Assert bounded-lifetime behavior: rejected with 400 "Verification token expired"
	require.Equal(t, http.StatusBadRequest, respExpired.StatusCode)
	var body map[string]interface{}
	require.NoError(t, json.NewDecoder(respExpired.Body).Decode(&body))
	require.Equal(t, "Verification token expired", body["error"])
}
