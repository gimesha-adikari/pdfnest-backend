package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"pdfnest-backend/config"
	"pdfnest-backend/internal/mailer"
)

func eligibleVerification(t *testing.T, u *config.User) {
	t.Helper()
	require.NoError(t, config.DB.Model(&config.User{}).Where("id = ?", u.ID).Update("email_verify_expires_at", time.Now().Add(emailVerificationTTL-emailVerificationCooldown-time.Second)).Error)
}
func verificationOutbox(t *testing.T) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(os.Getenv("MAILER_OUTBOX_DIR"))
	require.NoError(t, err)
	return entries
}
func resend(t *testing.T, app *fiber.App, email string) string {
	t.Helper()
	resp, err := app.Test(jsonRequest("POST", "/resend-verification", `{"email":"`+email+`"}`), 10000)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
	return responseBody(t, resp)
}
func TestResendCooldownPreservesInitialLinkAndAllowsEligibleRotation(t *testing.T) {
	app, _, u, _ := setupEmailVerificationMatrixTest(t)
	beforeHash, beforeExpiry := u.EmailVerifyTokenHash, u.EmailVerifyExpiresAt
	unknown := resend(t, app, "unknown-cooldown@example.invalid")
	require.Empty(t, verificationOutbox(t))
	first := resend(t, app, u.Email)
	require.Equal(t, unknown, first)
	var after config.User
	require.NoError(t, config.DB.First(&after, "id = ?", u.ID).Error)
	require.Equal(t, beforeHash, after.EmailVerifyTokenHash)
	require.WithinDuration(t, beforeExpiry, after.EmailVerifyExpiresAt, time.Microsecond)
	require.Empty(t, verificationOutbox(t))
	eligibleVerification(t, u)
	require.Equal(t, first, resend(t, app, u.Email))
	require.NoError(t, config.DB.First(&after, "id = ?", u.ID).Error)
	require.NotEqual(t, beforeHash, after.EmailVerifyTokenHash)
	require.WithinDuration(t, time.Now().Add(emailVerificationTTL), after.EmailVerifyExpiresAt, 2*time.Second)
	messages := verificationOutbox(t)
	require.Len(t, messages, 1)
	data, err := os.ReadFile(filepath.Join(os.Getenv("MAILER_OUTBOX_DIR"), messages[0].Name()))
	require.NoError(t, err)
	var email mailer.Email
	require.NoError(t, json.Unmarshal(data, &email))
	match := regexp.MustCompile(`token=([a-f0-9]{64})`).FindStringSubmatch(email.Text)
	require.Len(t, match, 2)
	require.True(t, hashVerificationToken(match[1]) == after.EmailVerifyTokenHash, "delivered token must match committed hash")
	// Another immediate resend cannot invalidate the newly delivered link.
	require.Equal(t, first, resend(t, app, u.Email))
	require.Len(t, verificationOutbox(t), 1)
	verify, err := app.Test(httptest.NewRequest("GET", "/verify-email?token="+match[1], nil))
	require.NoError(t, err)
	require.Equal(t, 200, verify.StatusCode)
	verify.Body.Close()
	require.NoError(t, config.DB.First(&after, "id = ?", u.ID).Error)
	require.True(t, after.EmailVerified)
}

func TestConcurrentResendsReplaceAndDeliverAtMostOnce(t *testing.T) {
	app, _, u, _ := setupEmailVerificationMatrixTest(t)
	eligibleVerification(t, u)
	old := u.EmailVerifyTokenHash
	const count = 6
	var wg sync.WaitGroup
	statuses := make([]int, count)
	for i := range count {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := app.Test(jsonRequest("POST", "/resend-verification", `{"email":"`+u.Email+`"}`), 10000)
			if err == nil {
				statuses[i] = r.StatusCode
				r.Body.Close()
			}
		}(i)
	}
	wg.Wait()
	for _, status := range statuses {
		require.Equal(t, 200, status)
	}
	require.Len(t, verificationOutbox(t), 1)
	var after config.User
	require.NoError(t, config.DB.First(&after, "id = ?", u.ID).Error)
	require.NotEqual(t, old, after.EmailVerifyTokenHash)
}

func TestVerificationWinningRowLockPreventsResendMutation(t *testing.T) {
	app, _, u, raw := setupEmailVerificationMatrixTest(t)
	eligibleVerification(t, u)
	tx := config.DB.Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	require.NoError(t, tx.Exec("SELECT 1 FROM users WHERE id = ? FOR UPDATE", u.ID).Error)
	done := make(chan int, 1)
	go func() {
		r, err := app.Test(jsonRequest("POST", "/resend-verification", `{"email":"`+u.Email+`"}`), 10000)
		if err != nil {
			done <- 0
			return
		}
		done <- r.StatusCode
		r.Body.Close()
	}()
	// Complete the same locked state transition that VerifyEmail performs;
	// the waiting resend must re-read verified=true after acquiring its lock.
	require.NoError(t, tx.Model(&config.User{}).Where("id = ?", u.ID).Updates(map[string]any{"email_verified": true, "status": "active"}).Error)
	require.NoError(t, tx.Commit().Error)
	require.Equal(t, 200, <-done)
	require.Empty(t, verificationOutbox(t))
	var after config.User
	require.NoError(t, config.DB.First(&after, "id = ?", u.ID).Error)
	require.True(t, after.EmailVerified)
	require.Equal(t, u.EmailVerifyTokenHash, after.EmailVerifyTokenHash)
	r, err := app.Test(httptest.NewRequest("GET", "/verify-email?token="+raw, nil))
	require.NoError(t, err)
	require.Equal(t, 200, r.StatusCode)
	r.Body.Close()
}

func TestResendFailuresDoNotLeakOrDeliverUncommittedToken(t *testing.T) {
	for _, kind := range []string{"delivery", "update", "commit"} {
		t.Run(kind, func(t *testing.T) {
			app, _, u, _ := setupEmailVerificationMatrixTest(t)
			eligibleVerification(t, u)
			if kind == "delivery" {
				outbox := filepath.Join(t.TempDir(), "blocked")
				require.NoError(t, os.WriteFile(outbox, []byte("synthetic"), 0600))
				t.Setenv("MAILER_OUTBOX_DIR", outbox)
				known := resend(t, app, u.Email)
				unknown := resend(t, app, "unknown@example.invalid")
				require.Equal(t, known, unknown)
				return
			}
			if kind == "update" {
				require.NoError(t, config.DB.Callback().Update().Before("gorm:update").Register("resend_failure", func(tx *gorm.DB) { tx.AddError(errors.New("synthetic update failure")) }))
				defer config.DB.Callback().Update().Remove("resend_failure")
			} else {
				installAccountCommitFailure(t, config.DB)
			}
			r, err := app.Test(jsonRequest("POST", "/resend-verification", `{"email":"`+u.Email+`"}`), 10000)
			require.NoError(t, err)
			require.Equal(t, 500, r.StatusCode)
			r.Body.Close()
			require.Empty(t, verificationOutbox(t))
			var after config.User
			require.NoError(t, config.DB.First(&after, "id = ?", u.ID).Error)
			require.Equal(t, u.EmailVerifyTokenHash, after.EmailVerifyTokenHash)
		})
	}
}

func TestRegistrationStartsVerificationCooldown(t *testing.T) {
	db, app, ctrl, _ := setupAuthControllerTest(t)
	app.Post("/register", ctrl.Register)
	app.Post("/resend-verification", ctrl.ResendVerification)
	const email = "registration-cooldown@example.invalid"
	r, err := app.Test(jsonRequest("POST", "/register", `{"email":"`+email+`","password":"synthetic-password","policy_accepted":true}`), 10000)
	require.NoError(t, err)
	require.Equal(t, 201, r.StatusCode)
	r.Body.Close()
	var before config.User
	require.NoError(t, db.First(&before, "email = ?", email).Error)
	defer db.Delete(&config.User{}, "id = ?", before.ID)
	require.Len(t, verificationOutbox(t), 1)
	resend(t, app, email)
	var after config.User
	require.NoError(t, db.First(&after, "id = ?", before.ID).Error)
	require.Equal(t, before.EmailVerifyTokenHash, after.EmailVerifyTokenHash)
	require.Len(t, verificationOutbox(t), 1)
}

func TestResendPeerAdmissionIsBoundedAndIgnoresForwardingHeaders(t *testing.T) {
	g := newVerificationAdmission()
	now := time.Now()
	for range resendPeerRequests {
		require.True(t, g.allow("peer", now))
	}
	require.False(t, g.allow("peer", now))
	require.True(t, g.allow("peer", now.Add(time.Minute)))
	for i := 0; i < resendPeerCapacity; i++ {
		g.allow(time.Unix(int64(i), 0).String(), now)
	}
	require.LessOrEqual(t, len(g.peers), resendPeerCapacity)
	require.False(t, g.allow("new-peer", now))
	require.True(t, g.allow("new-peer", now.Add(2*time.Minute)))
	app, ctrl, u, _ := setupEmailVerificationMatrixTest(t)
	eligibleVerification(t, u)
	for i := range resendPeerRequests + 2 {
		req := jsonRequest("POST", "/resend-verification", `{"email":"`+u.Email+`"}`)
		req.Header.Set("X-Forwarded-For", time.Unix(int64(i), 0).Format("150405"))
		r, err := app.Test(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, r.StatusCode)
		r.Body.Close()
	}
	require.Len(t, verificationOutbox(t), 1)
	require.Len(t, ctrl.resendAdmission.peers, 1, "spoofed forwarding headers cannot allocate different peer budgets")
	for _, window := range ctrl.resendAdmission.peers {
		require.Equal(t, resendPeerRequests, window.count)
	}
}
