package admin

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/middleware"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func openAdmin001LocalDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("ADMIN001_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated local PostgreSQL via ADMIN001_TEST_DATABASE_URL")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&config.User{}, &config.Subscription{}, &config.AdminSubscriptionMutation{}))
	return db
}

func admin001Target(t *testing.T, db *gorm.DB, initialCredits int) string {
	t.Helper()
	userID := uuid.NewString()
	now := time.Now().UTC()
	user := config.User{
		ID:            userID,
		Email:         userID + "@admin001.invalid",
		Role:          "user",
		Status:        "active",
		EmailVerified: true,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	require.NoError(t, db.Create(&user).Error)
	require.NoError(t, db.Create(&config.Subscription{
		ID:               uuid.NewString(),
		UserID:           userID,
		Status:           "active",
		Tier:             "free",
		CustomCredits:    initialCredits,
		CurrentPeriodEnd: now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}).Error)
	t.Cleanup(func() {
		_ = db.Delete(&config.AdminSubscriptionMutation{}, "user_id = ?", userID).Error
		_ = db.Delete(&config.Subscription{}, "user_id = ?", userID).Error
		_ = db.Delete(&config.User{}, "id = ?", userID).Error
	})
	return userID
}

func admin001App(t *testing.T, db *gorm.DB, actorID string) *fiber.App {
	t.Helper()
	previousDB := config.DB
	config.DB = db
	t.Cleanup(func() { config.DB = previousDB })

	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("user_id", actorID)
		return c.Next()
	})
	app.Patch("/admin/users/:id/tier", NewController().UpdateUserTier)
	return app
}

func admin001Request(t *testing.T, app *fiber.App, userID, key string, credits int) (int, map[string]interface{}) {
	t.Helper()
	body, err := json.Marshal(map[string]interface{}{
		"tier":           "free",
		"status":         "active",
		"custom_credits": credits,
		"days_to_plus":   0,
	})
	require.NoError(t, err)
	req := httptest.NewRequest("PATCH", "/admin/users/"+userID+"/tier", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := app.Test(req, 5000)
	require.NoError(t, err)
	defer resp.Body.Close()
	var payload map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	return resp.StatusCode, payload
}

func admin001Credits(t *testing.T, db *gorm.DB, userID string) int {
	t.Helper()
	var sub config.Subscription
	require.NoError(t, db.Where("user_id = ?", userID).First(&sub).Error)
	return sub.CustomCredits
}

func admin001MutationCount(t *testing.T, db *gorm.DB, userID string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&config.AdminSubscriptionMutation{}).Where("user_id = ?", userID).Count(&count).Error)
	return count
}

func TestAdminTierSingleGrantAndStableRetry(t *testing.T) {
	db := openAdmin001LocalDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	userID := admin001Target(t, db, 0)
	app := admin001App(t, db, uuid.NewString())

	status, first := admin001Request(t, app, userID, "admin001-single", 50)
	require.Equal(t, fiber.StatusOK, status)
	require.Equal(t, "success", first["status"])
	require.Equal(t, float64(50), first["custom_credits"])
	require.Equal(t, false, first["idempotent_replay"])

	status, replay := admin001Request(t, app, userID, "admin001-single", 50)
	require.Equal(t, fiber.StatusOK, status)
	require.Equal(t, float64(50), replay["custom_credits"])
	require.Equal(t, true, replay["idempotent_replay"])
	require.Equal(t, 50, admin001Credits(t, db, userID))
	require.Equal(t, int64(1), admin001MutationCount(t, db, userID))
}

func TestAdminTierConcurrentDuplicateAppliesOnce(t *testing.T) {
	db := openAdmin001LocalDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	userID := admin001Target(t, db, 0)
	app := admin001App(t, db, uuid.NewString())
	const concurrentRequests = 10
	statuses := make(chan int, concurrentRequests)
	var wg sync.WaitGroup
	wg.Add(concurrentRequests)
	for range concurrentRequests {
		go func() {
			defer wg.Done()
			status, _ := admin001Request(t, app, userID, "admin001-concurrent", 25)
			statuses <- status
		}()
	}
	wg.Wait()
	close(statuses)
	for status := range statuses {
		require.Equal(t, fiber.StatusOK, status)
	}

	require.Equal(t, 25, admin001Credits(t, db, userID))
	require.Equal(t, int64(1), admin001MutationCount(t, db, userID))
}

func TestAdminTierDistinctOperationsRemainAdditive(t *testing.T) {
	db := openAdmin001LocalDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	userID := admin001Target(t, db, 0)
	app := admin001App(t, db, uuid.NewString())
	status, _ := admin001Request(t, app, userID, "admin001-distinct-a", 25)
	require.Equal(t, fiber.StatusOK, status)
	status, _ = admin001Request(t, app, userID, "admin001-distinct-b", 25)
	require.Equal(t, fiber.StatusOK, status)

	require.Equal(t, 50, admin001Credits(t, db, userID))
	require.Equal(t, int64(2), admin001MutationCount(t, db, userID))
}

func TestAdminTierRejectsKeyReuseWithDifferentPayload(t *testing.T) {
	db := openAdmin001LocalDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	userID := admin001Target(t, db, 0)
	app := admin001App(t, db, uuid.NewString())
	status, _ := admin001Request(t, app, userID, "admin001-conflict", 25)
	require.Equal(t, fiber.StatusOK, status)
	status, payload := admin001Request(t, app, userID, "admin001-conflict", 50)
	require.Equal(t, fiber.StatusUnprocessableEntity, status)
	require.Equal(t, "IDEMPOTENCY_KEY_REUSE_WITH_DIFFERENT_PAYLOAD", payload["code"])
	require.Equal(t, 25, admin001Credits(t, db, userID))
	require.Equal(t, int64(1), admin001MutationCount(t, db, userID))
}

func TestAdminTierRejectsMissingAndNegativeMutation(t *testing.T) {
	db := openAdmin001LocalDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	userID := admin001Target(t, db, 0)
	app := admin001App(t, db, uuid.NewString())
	status, _ := admin001Request(t, app, userID, "", 25)
	require.Equal(t, fiber.StatusBadRequest, status)
	status, _ = admin001Request(t, app, userID, "admin001-negative", -1)
	require.Equal(t, fiber.StatusBadRequest, status)
	require.Equal(t, 0, admin001Credits(t, db, userID))
	require.Equal(t, int64(0), admin001MutationCount(t, db, userID))
}

func TestAdminTierRollsBackOperationClaimWhenTargetIsMissing(t *testing.T) {
	db := openAdmin001LocalDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	app := admin001App(t, db, uuid.NewString())
	missingUserID := uuid.NewString()
	status, _ := admin001Request(t, app, missingUserID, "admin001-rollback", 30)
	require.Equal(t, fiber.StatusNotFound, status)

	userID := admin001Target(t, db, 0)
	status, _ = admin001Request(t, app, userID, "admin001-rollback", 30)
	require.Equal(t, fiber.StatusOK, status)
	require.Equal(t, 30, admin001Credits(t, db, userID))
	require.Equal(t, int64(1), admin001MutationCount(t, db, userID))
}

func TestAdminTierRouteRejectsNonAdminRole(t *testing.T) {
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("role", "user")
		return c.Next()
	})
	app.Patch("/admin/users/:id/tier", middleware.RequireAdmin(), func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusOK)
	})

	req := httptest.NewRequest("PATCH", "/admin/users/"+uuid.NewString()+"/tier", bytes.NewBufferString("{}"))
	resp, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, fiber.StatusForbidden, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
}
