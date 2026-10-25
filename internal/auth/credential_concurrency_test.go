package auth

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"pdfnest-backend/config"
	"pdfnest-backend/internal/admin"
	"pdfnest-backend/internal/middleware"
	account "pdfnest-backend/internal/user"
)

func TestAdminAccountEditsDoNotRestoreStaleCredentials(t *testing.T) {
	for _, kind := range []string{"status", "role"} {
		t.Run(kind, func(t *testing.T) {
			db, app, _, u := setupAuthControllerTest(t)
			changedHash := "synthetic-concurrently-replaced-hash"
			var injected atomic.Bool
			name := "credential-interleaving"
			require.NoError(t, db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
				if tx.Statement.Table == "users" && len(tx.Statement.Selects) == 0 && injected.CompareAndSwap(false, true) {
					tx.AddError(db.Exec("UPDATE users SET password_hash = ?, session_version = 7 WHERE id = ?", changedHash, u.ID).Error)
				}
			}))
			defer db.Callback().Query().Remove(name)
			path := "/admin/" + u.ID
			var reqBody string
			if kind == "status" {
				app.Put("/admin/:id", admin.NewController().ToggleBanUser)
			} else {
				app.Put("/admin/:id", admin.NewController().UpdateUserRole)
				reqBody = `{"role":"admin"}`
			}
			r, err := app.Test(jsonRequest("PUT", path, reqBody), 10000)
			require.NoError(t, err)
			require.Equal(t, 200, r.StatusCode)
			r.Body.Close()
			require.True(t, injected.Load())
			var after config.User
			require.NoError(t, db.First(&after, "id = ?", u.ID).Error)
			require.Equal(t, changedHash, after.PasswordHash)
			require.EqualValues(t, 7, after.SessionVersion)
		})
	}
}

type googleFixtureService struct {
	Service
	email string
}

func (s googleFixtureService) VerifyGoogleToken(context.Context, string) (map[string]interface{}, error) {
	return map[string]interface{}{"email": s.email, "sub": "synthetic-google-subject"}, nil
}
func TestGoogleSignInCannotOverwriteConcurrentPasswordChange(t *testing.T) {
	db, app, _, u := setupAuthControllerTest(t)
	require.NoError(t, db.AutoMigrate(&config.Subscription{}))
	defer db.Delete(&config.Subscription{}, "user_id = ?", u.ID)
	ctrl := NewController(googleFixtureService{Service: NewService(), email: u.Email})
	app.Post("/google", ctrl.GoogleSignIn)
	app.Put("/password", middleware.Protect(), account.NewController().UpdatePassword)
	token, err := NewService().GenerateToken(u.ID, u.Role, 0)
	require.NoError(t, err)
	locked := make(chan struct{})
	release := make(chan struct{})
	var observed atomic.Bool
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("google_read", func(tx *gorm.DB) {
		if _, exists := tx.Statement.Clauses["FOR"]; exists && observed.CompareAndSwap(false, true) {
			close(locked)
			<-release
		}
	}))
	defer db.Callback().Query().Remove("google_read")
	googleDone := make(chan int, 1)
	go func() {
		r, err := app.Test(jsonRequest("POST", "/google", `{"id_token":"synthetic","policy_accepted":true}`), 10000)
		if err != nil {
			googleDone <- 0
			return
		}
		r.Body.Close()
		googleDone <- r.StatusCode
	}()
	select {
	case <-locked:
	case <-time.After(3 * time.Second):
		t.Fatal("Google read did not acquire account lock")
	}
	changed := make(chan int, 1)
	go func() {
		r, err := app.Test(passwordRequest("/password", "old-password-123", "google-race-new-password", &http.Cookie{Name: "auth_token", Value: token}), 10000)
		if err != nil {
			changed <- 0
			return
		}
		r.Body.Close()
		changed <- r.StatusCode
	}()
	select {
	case <-changed:
		t.Fatal("password change bypassed Google account lock")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	require.Equal(t, 200, <-googleDone)
	require.Equal(t, 200, <-changed)
	var after config.User
	require.NoError(t, db.First(&after, "id = ?", u.ID).Error)
	require.EqualValues(t, 1, after.SessionVersion)
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(after.PasswordHash), []byte("google-race-new-password")))
}
