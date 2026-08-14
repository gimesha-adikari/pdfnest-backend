package middleware

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
)

func makeToken(secret string, claims jwt.MapClaims) string {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, _ := token.SignedString([]byte(secret))
	return signed
}

func newTestApp() *fiber.App {
	os.Setenv("JWT_SECRET", "test-secret")
	app := fiber.New()
	app.Use(Protect())
	app.Get("/", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"user_id": c.Locals("user_id"),
			"role":    c.Locals("role"),
		})
	})
	return app
}

func TestProtect_NoToken(t *testing.T) {
	app := newTestApp()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestProtect_ValidToken(t *testing.T) {
	app := newTestApp()
	tok := makeToken("test-secret", jwt.MapClaims{
		"user_id": "user-abc",
		"role":    "user",
		"exp":     time.Now().Add(time.Hour).Unix(),
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: tok})
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestProtect_NonStringUserID(t *testing.T) {
	app := newTestApp()
	// user_id is an integer — should return 401 not panic
	tok := makeToken("test-secret", jwt.MapClaims{
		"user_id": float64(12345),
		"role":    "user",
		"exp":     time.Now().Add(time.Hour).Unix(),
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: tok})
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestProtect_MissingUserID(t *testing.T) {
	app := newTestApp()
	tok := makeToken("test-secret", jwt.MapClaims{
		"role": "user",
		"exp":  time.Now().Add(time.Hour).Unix(),
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: tok})
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestProtect_EmptyUserID(t *testing.T) {
	app := newTestApp()
	tok := makeToken("test-secret", jwt.MapClaims{
		"user_id": "",
		"role":    "user",
		"exp":     time.Now().Add(time.Hour).Unix(),
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: tok})
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestProtect_MissingRole(t *testing.T) {
	app := newTestApp()
	tok := makeToken("test-secret", jwt.MapClaims{
		"user_id": "user-abc",
		"exp":     time.Now().Add(time.Hour).Unix(),
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: tok})
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestProtect_InvalidSignature(t *testing.T) {
	app := newTestApp()
	tok := makeToken("wrong-secret", jwt.MapClaims{
		"user_id": "user-abc",
		"role":    "user",
		"exp":     time.Now().Add(time.Hour).Unix(),
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: tok})
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestProtect_ExpiredToken(t *testing.T) {
	app := newTestApp()
	tok := makeToken("test-secret", jwt.MapClaims{
		"user_id": "user-abc",
		"role":    "user",
		"exp":     time.Now().Add(-time.Hour).Unix(),
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: tok})
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}
