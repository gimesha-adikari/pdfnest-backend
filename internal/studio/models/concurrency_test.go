package models

import (
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func getTestDB(t *testing.T) *gorm.DB {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "host=localhost user=postgres password=2021 dbname=pdfnest port=5432 sslmode=disable"
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Skip("PostgreSQL database not accessible in current environment: ", err)
		return nil
	}

	sqlDB, err := db.DB()
	if err != nil || sqlDB.Ping() != nil {
		t.Skip("PostgreSQL database ping failed; skipping live DB integration test")
		return nil
	}

	err = db.AutoMigrate(
		&StudioDocument{},
		&StudioAsset{},
		&StudioSnapshot{},
		&StudioVersion{},
		&StudioOperation{},
		&StudioSession{},
		&StudioExport{},
	)
	require.NoError(t, err)
	return db
}

func TestLiveIdempotencyAndSessionLocking(t *testing.T) {
	db := getTestDB(t)
	if db == nil {
		return
	}

	docID := uuid.New()
	doc := StudioDocument{
		ID:               docID,
		OriginalFileName: "test.pdf",
		FileSize:         1000,
		InitialPageCount: 1,
		CreatedAt:        time.Now().UTC(),
	}
	require.NoError(t, db.Create(&doc).Error)

	v0ID := uuid.New()
	v0 := StudioVersion{
		ID:            v0ID,
		DocumentID:    docID,
		VersionNumber: 0,
		Status:        "ready",
		OperationType: "upload",
		VirtualModel:  JSON([]byte(`{"pages":[]}`)),
		CreatedAt:     time.Now().UTC(),
	}
	require.NoError(t, db.Create(&v0).Error)

	sessID := uuid.New()
	sess := StudioSession{
		ID:              sessID,
		GuestTokenHash:  "test_guest_hash",
		DocumentID:      docID,
		ActiveVersionID: v0ID,
		CreatedAt:       time.Now().UTC(),
		LastAccessedAt:  time.Now().UTC(),
		ExpiresAt:       time.Now().UTC().Add(24 * time.Hour),
	}
	require.NoError(t, db.Create(&sess).Error)

	idempotencyKey := "idem_race_" + uuid.New().String()

	var wg sync.WaitGroup
	results := make([]error, 2)
	createdVersionIDs := make([]uuid.UUID, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			err := db.Transaction(func(tx *gorm.DB) error {
				// 1. Session lock
				var activeSess StudioSession
				if err := tx.Raw("SELECT id, active_version_id, document_id FROM studio_sessions WHERE id = ? FOR UPDATE", sessID).Scan(&activeSess).Error; err != nil {
					return err
				}

				// 2. Idempotency check
				var existingOp StudioOperation
				if err := tx.Where("document_id = ? AND idempotency_key = ?", docID, idempotencyKey).First(&existingOp).Error; err == nil {
					// Found existing
					createdVersionIDs[idx] = existingOp.VersionID
					return nil
				}

				// 3. Create version
				newVerID := uuid.New()
				newVer := StudioVersion{
					ID:            newVerID,
					DocumentID:    docID,
					VersionNumber: 1,
					Status:        "ready",
					OperationType: "rotate",
					VirtualModel:  JSON([]byte(`{"pages":[{"page_id":"p1","rotation":90}]}`)),
					CreatedAt:     time.Now().UTC(),
				}
				if err := tx.Create(&newVer).Error; err != nil {
					return err
				}

				// 4. Create operation
				op := StudioOperation{
					ID:             uuid.New(),
					DocumentID:     docID,
					VersionID:      newVerID,
					IdempotencyKey: idempotencyKey,
					OperationName:  "rotate",
					Parameters:     JSON([]byte(`{"angle":90}`)),
					CreatedAt:      time.Now().UTC(),
				}
				if err := tx.Create(&op).Error; err != nil {
					return err
				}

				// 5. Update session
				if err := tx.Model(&StudioSession{}).Where("id = ?", sessID).Update("active_version_id", newVerID).Error; err != nil {
					return err
				}

				createdVersionIDs[idx] = newVerID
				return nil
			})
			results[idx] = err
		}(i)
	}

	wg.Wait()

	// Assertions
	for i := 0; i < 2; i++ {
		assert.NoError(t, results[i])
	}
	// Both goroutines must return the exact same version ID
	assert.Equal(t, createdVersionIDs[0], createdVersionIDs[1], "Concurrent idempotency race must produce identical Version ID")

	// Verify in DB that exactly ONE operation and ONE version 1 exist
	var opCount int64
	db.Model(&StudioOperation{}).Where("document_id = ? AND idempotency_key = ?", docID, idempotencyKey).Count(&opCount)
	assert.Equal(t, int64(1), opCount, "Exactly ONE operation must exist in database")

	var verCount int64
	db.Model(&StudioVersion{}).Where("document_id = ? AND version_number = 1", docID).Count(&verCount)
	assert.Equal(t, int64(1), verCount, "Exactly ONE version 1 must exist in database")
}
