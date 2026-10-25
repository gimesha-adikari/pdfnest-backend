package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"pdfnest-backend/config"
	"pdfnest-backend/internal/authn"
	"pdfnest-backend/internal/middleware"
	account "pdfnest-backend/internal/user"
)

func passwordRequest(path, current, next string, cookie *http.Cookie) *http.Request {
	req := jsonRequest(http.MethodPut, path, `{"current_password":"`+current+`","new_password":"`+next+`"}`)
	req.AddCookie(cookie)
	return req
}
func TestPasswordChangeRevokesEverySessionAndClearsCookie(t *testing.T) {
	db, app, _, u := setupAuthControllerTest(t)
	app.Put("/password", middleware.Protect(), account.NewController().UpdatePassword)
	service := NewService()
	first, err := service.GenerateToken(u.ID, u.Role, 0)
	require.NoError(t, err)
	second, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id": u.ID, "role": u.Role, "session_version": 0, "exp": time.Now().Add(time.Hour).Unix(), "jti": "synthetic-second-session",
	}).SignedString([]byte("auth-controller-test-secret"))
	require.NoError(t, err)
	require.NotEqual(t, first, second)
	// Independent signed credentials for the same account/version.
	cookie := &http.Cookie{Name: "auth_token", Value: first}
	resetRaw, resetHash, err := generateVerificationToken()
	require.NoError(t, err)
	require.NoError(t, db.Model(&config.User{}).Where("id = ?", u.ID).Updates(map[string]any{"password_reset_token_hash": resetHash, "password_reset_expires_at": time.Now().Add(time.Minute)}).Error)
	resp, err := app.Test(passwordRequest("/password", "old-password-123", "new-password-456", cookie), 5000)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
	body := responseBody(t, resp)
	require.Contains(t, body, `"reauthentication_required":true`)
	require.NotContains(t, body, first)
	require.NotContains(t, body, "new-password-456")
	require.Len(t, resp.Cookies(), 1)
	cleared := resp.Cookies()[0]
	require.Equal(t, "auth_token", cleared.Name)
	require.Empty(t, cleared.Value)
	require.True(t, cleared.HttpOnly)
	require.Equal(t, "/", cleared.Path)
	require.Equal(t, http.SameSiteLaxMode, cleared.SameSite)
	require.True(t, cleared.Expires.Before(time.Now()))
	for _, token := range []string{first, second} {
		req := httptest.NewRequest("GET", "/protected", nil)
		req.AddCookie(&http.Cookie{Name: "auth_token", Value: token})
		rejected, err := app.Test(req, 10000)
		require.NoError(t, err)
		require.Equal(t, 401, rejected.StatusCode)
		rejected.Body.Close()
	}
	var after config.User
	require.NoError(t, db.First(&after, "id = ?", u.ID).Error)
	require.EqualValues(t, 1, after.SessionVersion)
	require.Empty(t, after.PasswordResetTokenHash)
	staleReset, err := app.Test(jsonRequest("POST", "/reset-password", `{"token":"`+resetRaw+`","password":"stale-reset-password"}`), 10000)
	require.NoError(t, err)
	require.Equal(t, 400, staleReset.StatusCode)
	staleReset.Body.Close()
	for _, scenario := range []struct {
		password string
		status   int
	}{{"old-password-123", 401}, {"new-password-456", 200}} {
		login, err := app.Test(jsonRequest("POST", "/login", `{"email":"`+u.Email+`","password":"`+scenario.password+`"}`), 10000)
		require.NoError(t, err)
		require.Equal(t, scenario.status, login.StatusCode)
		if scenario.status == 200 {
			verified, err := authn.Verify(t.Context(), authCookie(t, login).Value)
			require.NoError(t, err)
			require.EqualValues(t, 1, verified.SessionVersion)
		}
		login.Body.Close()
	}
}

func TestPasswordChangeWrongPasswordGoogleAndDatabaseFailure(t *testing.T) {
	for _, scenario := range []string{"wrong", "google", "database", "commit"} {
		t.Run(scenario, func(t *testing.T) {
			db, app, _, u := setupAuthControllerTest(t)
			if scenario == "google" {
				require.NoError(t, db.Model(&config.User{}).Where("id = ?", u.ID).Update("password_hash", "").Error)
				u.PasswordHash = ""
			}
			token, err := NewService().GenerateToken(u.ID, u.Role, 0)
			require.NoError(t, err)
			app.Put("/password", middleware.Protect(), account.NewController().UpdatePassword)
			if scenario == "database" {
				require.NoError(t, db.Callback().Update().Before("gorm:update").Register("synthetic_failure", func(tx *gorm.DB) { tx.AddError(errors.New("synthetic update failure")) }))
				defer db.Callback().Update().Remove("synthetic_failure")
			}
			if scenario == "commit" {
				installAccountCommitFailure(t, db)
			}
			current := "old-password-123"
			expected := 500
			if scenario == "wrong" {
				current = "incorrect"
				expected = 401
			}
			if scenario == "google" {
				expected = 400
			}
			resp, err := app.Test(passwordRequest("/password", current, "new-password-456", &http.Cookie{Name: "auth_token", Value: token}), 5000)
			require.NoError(t, err)
			require.Equal(t, expected, resp.StatusCode)
			require.Empty(t, resp.Cookies())
			resp.Body.Close()
			var after config.User
			require.NoError(t, db.First(&after, "id = ?", u.ID).Error)
			require.Equal(t, u.PasswordHash, after.PasswordHash)
			require.Zero(t, after.SessionVersion)
			_, err = authn.Verify(t.Context(), token)
			require.NoError(t, err)
		})
	}
}

func TestConcurrentPasswordChangesHaveOneWinner(t *testing.T) {
	db, app, _, u := setupAuthControllerTest(t)
	token, err := NewService().GenerateToken(u.ID, u.Role, 0)
	require.NoError(t, err)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	app.Put("/password", middleware.Protect(), func(c *fiber.Ctx) error {
		entered <- struct{}{}
		<-release
		return account.NewController().UpdatePassword(c)
	})
	type result struct {
		status   int
		password string
	}
	results := make(chan result, 2)
	for _, next := range []string{"winner-password-a", "winner-password-b"} {
		go func(next string) {
			r, err := app.Test(passwordRequest("/password", "old-password-123", next, &http.Cookie{Name: "auth_token", Value: token}), 10000)
			if err != nil {
				results <- result{0, next}
				return
			}
			r.Body.Close()
			results <- result{r.StatusCode, next}
		}(next)
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("request did not authenticate")
		}
	}
	close(release)
	a, b := <-results, <-results
	require.ElementsMatch(t, []int{200, 401}, []int{a.status, b.status})
	winner := a
	if b.status == 200 {
		winner = b
	}
	var after config.User
	require.NoError(t, db.First(&after, "id = ?", u.ID).Error)
	require.EqualValues(t, 1, after.SessionVersion)
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(after.PasswordHash), []byte(winner.password)))
}

func TestPasswordChangeCannotOverwriteConcurrentResetOrLogout(t *testing.T) {
	for _, kind := range []string{"reset", "logout"} {
		t.Run(kind, func(t *testing.T) {
			db, app, _, u := setupAuthControllerTest(t)
			token, err := NewService().GenerateToken(u.ID, u.Role, 0)
			require.NoError(t, err)
			cookie := &http.Cookie{Name: "auth_token", Value: token}
			entered := make(chan struct{})
			release := make(chan struct{})
			app.Put("/delayed", middleware.Protect(), func(c *fiber.Ctx) error { close(entered); <-release; return account.NewController().UpdatePassword(c) })
			done := make(chan int, 1)
			go func() {
				r, err := app.Test(passwordRequest("/delayed", "old-password-123", "stale-password-789", cookie), 10000)
				if err != nil {
					done <- 0
					return
				}
				r.Body.Close()
				done <- r.StatusCode
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("request did not authenticate")
			}
			req := httptest.NewRequest("POST", "/logout", nil)
			req.AddCookie(cookie)
			expectedHash := u.PasswordHash
			if kind == "reset" {
				raw, hash, err := generateVerificationToken()
				require.NoError(t, err)
				require.NoError(t, db.Model(&config.User{}).Where("id = ?", u.ID).Updates(map[string]any{"password_reset_token_hash": hash, "password_reset_expires_at": time.Now().Add(time.Minute)}).Error)
				req = jsonRequest("POST", "/reset-password", `{"token":"`+raw+`","password":"reset-password-456"}`)
			}
			resp, err := app.Test(req, 10000)
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode)
			resp.Body.Close()
			close(release)
			require.Equal(t, 401, <-done)
			var after config.User
			require.NoError(t, db.First(&after, "id = ?", u.ID).Error)
			require.EqualValues(t, 1, after.SessionVersion)
			if kind == "logout" {
				require.Equal(t, expectedHash, after.PasswordHash)
			} else {
				require.NoError(t, bcrypt.CompareHashAndPassword([]byte(after.PasswordHash), []byte("reset-password-456")))
			}
			require.False(t, strings.Contains(after.PasswordHash, "stale-password"))
		})
	}
}

// A disposable-database-only deferred failure proves atomic rollback even
// when the SQL UPDATE succeeded and COMMIT subsequently fails.
func installAccountCommitFailure(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`CREATE FUNCTION reject_test_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic commit failure'; END $$`).Error)
	require.NoError(t, db.Exec(`CREATE CONSTRAINT TRIGGER reject_test_commit AFTER UPDATE ON users DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_test_commit()`).Error)
	t.Cleanup(func() { _ = db.Exec(`DROP FUNCTION reject_test_commit() CASCADE`).Error })
}
