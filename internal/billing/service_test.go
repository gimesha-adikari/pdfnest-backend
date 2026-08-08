package billing

import (
	"os"
	"sync"
	"testing"
	"time"

	"pdfnest-backend/config"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func setupTestDB(t *testing.T) *gorm.DB {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "host=localhost user=postgres password=2021 dbname=pdfnest port=5432 sslmode=disable"
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Skip("Postgres database connection unavailable, skipping test")
	}

	_ = db.AutoMigrate(&config.User{}, &config.Subscription{}, &config.BillingReservation{}, &config.UsageLog{})
	config.DB = db
	return db
}

func createTestUserAndSub(db *gorm.DB) (string, string) {
	userID := uuid.New().String()
	user := config.User{
		ID:        userID,
		Email:     userID + "@example.com",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	_ = db.Create(&user).Error

	subID := uuid.New().String()
	sub := config.Subscription{
		ID:                   subID,
		UserID:               userID,
		PaddleCustomerID:     "cus_" + subID,
		PaddleSubscriptionID: "sub_" + subID,
		Tier:                 "free",
		Status:               "active",
		CreatedAt:            time.Now(),
		UpdatedAt:            time.Now(),
	}
	_ = db.Create(&sub).Error

	return userID, subID
}

func TestBilling_IdempotentRelease(t *testing.T) {
	db := setupTestDB(t)
	userID, _ := createTestUserAndSub(db)

	res := config.BillingReservation{
		ID:        uuid.New().String(),
		UserID:    userID,
		ToolName:  "test_tool",
		Units:     1,
		Status:    "reserved",
		ExpiresAt: time.Now().Add(1 * time.Hour),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	db.Create(&res)

	// First release
	err := Default.Release(res.ID)
	if err != nil {
		t.Fatalf("First release failed: %v", err)
	}

	var check1 config.BillingReservation
	db.First(&check1, "id = ?", res.ID)
	if check1.Status != "released" {
		t.Errorf("Expected status 'released', got '%s'", check1.Status)
	}

	// Second release (idempotent no-op)
	err = Default.Release(res.ID)
	if err != nil {
		t.Fatalf("Second release failed: %v", err)
	}

	var check2 config.BillingReservation
	db.First(&check2, "id = ?", res.ID)
	if check2.Status != "released" {
		t.Errorf("Expected status 'released' to remain unchanged, got '%s'", check2.Status)
	}
}

func TestBilling_CommitThenReleaseNoOp(t *testing.T) {
	db := setupTestDB(t)
	userID, _ := createTestUserAndSub(db)

	res := config.BillingReservation{
		ID:          uuid.New().String(),
		UserID:      userID,
		ToolName:    "test_tool",
		Units:       1,
		PlanUnits:   1,
		CreditUnits: 0,
		Status:      "reserved",
		ExpiresAt:   time.Now().Add(1 * time.Hour),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	db.Create(&res)

	// Commit reservation
	err := Default.Commit(res.ID)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	var committedRes config.BillingReservation
	db.First(&committedRes, "id = ?", res.ID)
	if committedRes.Status != "committed" {
		t.Fatalf("Expected status 'committed', got '%s'", committedRes.Status)
	}

	// Attempt release after commit (must be no-op)
	err = Default.Release(res.ID)
	if err != nil {
		t.Fatalf("Release after commit failed: %v", err)
	}

	var checkRes config.BillingReservation
	db.First(&checkRes, "id = ?", res.ID)
	if checkRes.Status != "committed" {
		t.Errorf("Expected status to remain 'committed', got '%s'", checkRes.Status)
	}
}

func TestBilling_ReleaseThenCommitNoOp(t *testing.T) {
	db := setupTestDB(t)
	userID, _ := createTestUserAndSub(db)

	res := config.BillingReservation{
		ID:        uuid.New().String(),
		UserID:    userID,
		ToolName:  "test_tool",
		Units:     1,
		Status:    "reserved",
		ExpiresAt: time.Now().Add(1 * time.Hour),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	db.Create(&res)

	// Release reservation
	err := Default.Release(res.ID)
	if err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	// Attempt commit after release (must be no-op)
	err = Default.Commit(res.ID)
	if err != nil {
		t.Fatalf("Commit after release failed: %v", err)
	}

	var checkRes config.BillingReservation
	db.First(&checkRes, "id = ?", res.ID)
	if checkRes.Status != "released" {
		t.Errorf("Expected status to remain 'released', got '%s'", checkRes.Status)
	}
}

func TestBilling_ConcurrentCommitVsRelease(t *testing.T) {
	db := setupTestDB(t)
	userID, _ := createTestUserAndSub(db)

	res := config.BillingReservation{
		ID:          uuid.New().String(),
		UserID:      userID,
		ToolName:    "test_tool",
		Units:       1,
		PlanUnits:   1,
		CreditUnits: 0,
		Status:      "reserved",
		ExpiresAt:   time.Now().Add(1 * time.Hour),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	db.Create(&res)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = Default.Commit(res.ID)
		}()
		go func() {
			defer wg.Done()
			_ = Default.Release(res.ID)
		}()
	}
	wg.Wait()

	var finalRes config.BillingReservation
	db.First(&finalRes, "id = ?", res.ID)
	if finalRes.Status != "committed" && finalRes.Status != "released" {
		t.Errorf("Expected status to be terminal ('committed' or 'released'), got '%s'", finalRes.Status)
	}
}
