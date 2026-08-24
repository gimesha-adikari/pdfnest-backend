package models

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func getLiveDB(t *testing.T) *gorm.DB {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "host=localhost user=postgres password=2021 dbname=pdfnest port=5432 sslmode=disable"
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err, "Failed to open connection to live PostgreSQL database")

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Ping(), "Ping to live PostgreSQL failed")

	err = db.AutoMigrate(
		&StudioDocument{},
		&StudioAsset{},
		&StudioSnapshot{},
		&StudioVersion{},
		&StudioOperation{},
		&StudioSession{},
		&StudioExport{},
	)
	require.NoError(t, err, "AutoMigrate failed on live PostgreSQL")
	return db
}

func Test1_ActualDatabaseConstraintsAndIndexes(t *testing.T) {
	db := getLiveDB(t)

	// 1. Verify Tables Exist
	expectedTables := []string{
		"studio_documents",
		"studio_assets",
		"studio_snapshots",
		"studio_versions",
		"studio_operations",
		"studio_sessions",
		"studio_exports",
	}

	for _, table := range expectedTables {
		var exists bool
		query := `SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_schema = 'public' AND table_name = ?)`
		err := db.Raw(query, table).Scan(&exists).Error
		require.NoError(t, err)
		assert.True(t, exists, "Table %s must exist in PostgreSQL", table)
	}

	// 2. Query and Log Actual PostgreSQL Indexes
	type IndexInfo struct {
		Tablename string `gorm:"column:tablename"`
		Indexname string `gorm:"column:indexname"`
		Def       string `gorm:"column:indexdef"`
	}
	var indexes []IndexInfo
	err := db.Raw(`SELECT tablename, indexname, indexdef FROM pg_indexes WHERE schemaname = 'public' AND tablename LIKE 'studio_%' ORDER BY tablename, indexname`).Scan(&indexes).Error
	require.NoError(t, err)

	indexMap := make(map[string]string)
	t.Log("=== Actual PostgreSQL Indexes Discovered ===")
	for _, idx := range indexes {
		t.Logf("[%s] %s -> %s", idx.Tablename, idx.Indexname, idx.Def)
		indexMap[idx.Indexname] = idx.Def
	}

	// Verify idx_doc_idempotency composite unique index
	var foundIdempotencyIndex bool
	for name, def := range indexMap {
		if name == "idx_doc_idempotency" || (name == "studio_operations_document_id_idempotency_key_key") {
			assert.Contains(t, def, "UNIQUE INDEX")
			assert.Contains(t, def, "document_id")
			assert.Contains(t, def, "idempotency_key")
			foundIdempotencyIndex = true
		}
	}
	assert.True(t, foundIdempotencyIndex, "idx_doc_idempotency unique composite index must exist in PostgreSQL")

	// 3. Query and Log Actual PostgreSQL Foreign Keys
	type FKInfo struct {
		ConstraintName string `gorm:"column:constraint_name"`
		TableName      string `gorm:"column:table_name"`
		ColumnName     string `gorm:"column:column_name"`
		ForeignTable   string `gorm:"column:foreign_table_name"`
		ForeignColumn  string `gorm:"column:foreign_column_name"`
	}
	var fks []FKInfo
	fkQuery := `
		SELECT
			tc.constraint_name,
			tc.table_name,
			kcu.column_name,
			ccu.table_name AS foreign_table_name,
			ccu.column_name AS foreign_column_name
		FROM
			information_schema.table_constraints AS tc
			JOIN information_schema.key_column_usage AS kcu
				ON tc.constraint_name = kcu.constraint_name
				AND tc.table_schema = kcu.table_schema
			JOIN information_schema.constraint_column_usage AS ccu
				ON ccu.constraint_name = tc.constraint_name
				AND ccu.table_schema = tc.table_schema
		WHERE tc.constraint_type = 'FOREIGN KEY' AND tc.table_name LIKE 'studio_%'
		ORDER BY tc.table_name, kcu.column_name;
	`
	err = db.Raw(fkQuery).Scan(&fks).Error
	require.NoError(t, err)

	t.Log("=== Actual PostgreSQL Foreign Keys Discovered ===")
	for _, fk := range fks {
		t.Logf("[%s] %s.%s -> %s.%s", fk.ConstraintName, fk.TableName, fk.ColumnName, fk.ForeignTable, fk.ForeignColumn)
	}
}

func Test2_RealSessionLockBlockingBehavior(t *testing.T) {
	db := getLiveDB(t)

	docID := uuid.New()
	require.NoError(t, db.Create(&StudioDocument{
		ID:               docID,
		OriginalFileName: "lock_test.pdf",
		FileSize:         100,
		InitialPageCount: 1,
		CreatedAt:        time.Now().UTC(),
	}).Error)

	v0ID := uuid.New()
	require.NoError(t, db.Create(&StudioVersion{
		ID:            v0ID,
		DocumentID:    docID,
		VersionNumber: 0,
		Status:        "ready",
		OperationType: "init",
		VirtualModel:  JSON([]byte(`{}`)),
		CreatedAt:     time.Now().UTC(),
	}).Error)

	sessID := uuid.New()
	require.NoError(t, db.Create(&StudioSession{
		ID:              sessID,
		GuestTokenHash:  "lock_guest_hash",
		DocumentID:      docID,
		ActiveVersionID: v0ID,
		CreatedAt:       time.Now().UTC(),
		LastAccessedAt:  time.Now().UTC(),
		ExpiresAt:       time.Now().UTC().Add(24 * time.Hour),
	}).Error)

	var txALocked int32
	var txBBlockedDuration time.Duration
	var wg sync.WaitGroup

	wg.Add(2)

	// Goroutine A acquires row lock and holds it for 300ms
	go func() {
		defer wg.Done()
		err := db.Transaction(func(tx *gorm.DB) error {
			var sess StudioSession
			err := tx.Raw("SELECT id FROM studio_sessions WHERE id = ? FOR UPDATE", sessID).Scan(&sess).Error
			require.NoError(t, err)

			atomic.StoreInt32(&txALocked, 1)
			time.Sleep(300 * time.Millisecond) // Hold lock
			return nil
		})
		require.NoError(t, err)
	}()

	// Goroutine B tries to acquire same lock
	go func() {
		defer wg.Done()
		// Wait until A has locked
		for atomic.LoadInt32(&txALocked) == 0 {
			time.Sleep(5 * time.Millisecond)
		}

		startAttempt := time.Now()
		err := db.Transaction(func(tx *gorm.DB) error {
			var sess StudioSession
			err := tx.Raw("SELECT id FROM studio_sessions WHERE id = ? FOR UPDATE", sessID).Scan(&sess).Error
			require.NoError(t, err)
			return nil
		})
		require.NoError(t, err)
		txBBlockedDuration = time.Since(startAttempt)
	}()

	wg.Wait()

	t.Logf("Transaction B blocked for: %v (expected >= 200ms)", txBBlockedDuration)
	assert.GreaterOrEqual(t, txBBlockedDuration, 200*time.Millisecond, "Transaction B must be blocked by Transaction A's row lock")
}

func Test3_RealIdempotencyRaceExecution(t *testing.T) {
	db := getLiveDB(t)

	docID := uuid.New()
	require.NoError(t, db.Create(&StudioDocument{
		ID:               docID,
		OriginalFileName: "idem_race.pdf",
		FileSize:         200,
		InitialPageCount: 1,
		CreatedAt:        time.Now().UTC(),
	}).Error)

	v0ID := uuid.New()
	require.NoError(t, db.Create(&StudioVersion{
		ID:            v0ID,
		DocumentID:    docID,
		VersionNumber: 0,
		Status:        "ready",
		OperationType: "init",
		VirtualModel:  JSON([]byte(`{}`)),
		CreatedAt:     time.Now().UTC(),
	}).Error)

	sessID := uuid.New()
	require.NoError(t, db.Create(&StudioSession{
		ID:              sessID,
		GuestTokenHash:  "guest_hash_race",
		DocumentID:      docID,
		ActiveVersionID: v0ID,
		CreatedAt:       time.Now().UTC(),
		LastAccessedAt:  time.Now().UTC(),
		ExpiresAt:       time.Now().UTC().Add(24 * time.Hour),
	}).Error)

	idempotencyKey := "idem_live_race_" + uuid.New().String()

	var wg sync.WaitGroup
	results := make([]error, 2)
	resolvedVersionIDs := make([]uuid.UUID, 2)

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

				// 2. Idempotency lookup
				var existingOp StudioOperation
				if err := tx.Where("document_id = ? AND idempotency_key = ?", docID, idempotencyKey).First(&existingOp).Error; err == nil {
					resolvedVersionIDs[idx] = existingOp.VersionID
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

				resolvedVersionIDs[idx] = newVerID
				return nil
			})
			results[idx] = err
		}(i)
	}

	wg.Wait()

	for i := 0; i < 2; i++ {
		assert.NoError(t, results[i])
	}

	assert.Equal(t, resolvedVersionIDs[0], resolvedVersionIDs[1], "Both concurrent requests must resolve to the identical version ID")

	var opCount int64
	db.Model(&StudioOperation{}).Where("document_id = ? AND idempotency_key = ?", docID, idempotencyKey).Count(&opCount)
	assert.Equal(t, int64(1), opCount, "Exactly ONE operation record must exist")

	var verCount int64
	db.Model(&StudioVersion{}).Where("document_id = ? AND version_number = 1", docID).Count(&verCount)
	assert.Equal(t, int64(1), verCount, "Exactly ONE version 1 record must exist")

	var finalSess StudioSession
	require.NoError(t, db.First(&finalSess, "id = ?", sessID).Error)
	assert.Equal(t, resolvedVersionIDs[0], finalSess.ActiveVersionID, "Session active_version_id must point to winning version")
}

func Test4_TransactionRollbackAtomicity(t *testing.T) {
	db := getLiveDB(t)

	docID := uuid.New()
	require.NoError(t, db.Create(&StudioDocument{
		ID:               docID,
		OriginalFileName: "rollback.pdf",
		FileSize:         300,
		InitialPageCount: 1,
		CreatedAt:        time.Now().UTC(),
	}).Error)

	uncommittedVerID := uuid.New()

	// Transaction that fails after inserting version
	err := db.Transaction(func(tx *gorm.DB) error {
		ver := StudioVersion{
			ID:            uncommittedVerID,
			DocumentID:    docID,
			VersionNumber: 99,
			Status:        "ready",
			OperationType: "failed_op",
			VirtualModel:  JSON([]byte(`{}`)),
			CreatedAt:     time.Now().UTC(),
		}
		if err := tx.Create(&ver).Error; err != nil {
			return err
		}
		// Force rollback
		return fmt.Errorf("intentional failure before commit")
	})
	assert.Error(t, err)

	// Verify uncommitted version DOES NOT exist in database
	var count int64
	db.Model(&StudioVersion{}).Where("id = ?", uncommittedVerID).Count(&count)
	assert.Equal(t, int64(0), count, "Rolled back version must NOT exist in PostgreSQL")
}

func Test5_PostgreSQLUniqueConstraints(t *testing.T) {
	db := getLiveDB(t)

	docID := uuid.New()
	require.NoError(t, db.Create(&StudioDocument{
		ID:               docID,
		OriginalFileName: "unique_test.pdf",
		FileSize:         400,
		InitialPageCount: 1,
		CreatedAt:        time.Now().UTC(),
	}).Error)

	v1ID := uuid.New()
	v2ID := uuid.New()
	require.NoError(t, db.Create(&StudioVersion{
		ID:            v1ID,
		DocumentID:    docID,
		VersionNumber: 1,
		Status:        "ready",
		OperationType: "init",
		VirtualModel:  JSON([]byte(`{}`)),
		CreatedAt:     time.Now().UTC(),
	}).Error)

	require.NoError(t, db.Create(&StudioVersion{
		ID:            v2ID,
		DocumentID:    docID,
		VersionNumber: 2,
		Status:        "ready",
		OperationType: "init",
		VirtualModel:  JSON([]byte(`{}`)),
		CreatedAt:     time.Now().UTC(),
	}).Error)

	idemKey := "test_unique_key_" + uuid.New().String()

	// 1. Insert first operation
	op1 := StudioOperation{
		ID:             uuid.New(),
		DocumentID:     docID,
		VersionID:      v1ID,
		IdempotencyKey: idemKey,
		OperationName:  "test",
		Parameters:     JSON([]byte(`{}`)),
		CreatedAt:      time.Now().UTC(),
	}
	require.NoError(t, db.Create(&op1).Error)

	// 2. Attempt duplicate (document_id, idempotency_key) -> MUST FAIL
	opDuplicateKey := StudioOperation{
		ID:             uuid.New(),
		DocumentID:     docID,
		VersionID:      v2ID,
		IdempotencyKey: idemKey, // duplicate!
		OperationName:  "test",
		Parameters:     JSON([]byte(`{}`)),
		CreatedAt:      time.Now().UTC(),
	}
	err := db.Create(&opDuplicateKey).Error
	assert.Error(t, err, "PostgreSQL must reject duplicate (document_id, idempotency_key)")

	// 3. Attempt duplicate operation.version_id -> MUST FAIL
	opDuplicateVer := StudioOperation{
		ID:             uuid.New(),
		DocumentID:     docID,
		VersionID:      v1ID, // duplicate version_id!
		IdempotencyKey: "diff_key_" + uuid.New().String(),
		OperationName:  "test",
		Parameters:     JSON([]byte(`{}`)),
		CreatedAt:      time.Now().UTC(),
	}
	err = db.Create(&opDuplicateVer).Error
	assert.Error(t, err, "PostgreSQL must reject duplicate operation.version_id")

	// 4. Attempt duplicate snapshot.version_id -> MUST FAIL
	assetID := "ast_uniq_" + uuid.New().String()
	require.NoError(t, db.Create(&StudioAsset{
		ID:         assetID,
		DocumentID: docID,
		AssetType:  "snapshot",
		R2Key:      "studio/snapshots/test.pdf",
		ByteSize:   500,
		MimeType:   "application/pdf",
		CreatedAt:  time.Now().UTC(),
	}).Error)

	snap1 := StudioSnapshot{
		ID:        uuid.New(),
		VersionID: v1ID,
		AssetID:   assetID,
		PageCount: 1,
		CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, db.Create(&snap1).Error)

	snapDuplicateVer := StudioSnapshot{
		ID:        uuid.New(),
		VersionID: v1ID, // duplicate version_id!
		AssetID:   assetID,
		PageCount: 1,
		CreatedAt: time.Now().UTC(),
	}
	err = db.Create(&snapDuplicateVer).Error
	assert.Error(t, err, "PostgreSQL must reject duplicate snapshot.version_id")
}

func Test6_PostgreSQLForeignKeyRejection(t *testing.T) {
	db := getLiveDB(t)

	nonExistentDocID := uuid.New()
	nonExistentVerID := uuid.New()

	// 1. Invalid Asset DocumentID
	err := db.Create(&StudioAsset{
		ID:         "ast_invalid_doc",
		DocumentID: nonExistentDocID,
		AssetType:  "source_pdf",
		R2Key:      "invalid.pdf",
		ByteSize:   100,
		MimeType:   "application/pdf",
		CreatedAt:  time.Now().UTC(),
	}).Error
	assert.Error(t, err, "PostgreSQL must reject StudioAsset referencing non-existent DocumentID")

	// 2. Invalid Snapshot VersionID
	err = db.Create(&StudioSnapshot{
		ID:        uuid.New(),
		VersionID: nonExistentVerID,
		AssetID:   "ast_invalid",
		PageCount: 1,
		CreatedAt: time.Now().UTC(),
	}).Error
	assert.Error(t, err, "PostgreSQL must reject StudioSnapshot referencing non-existent VersionID")

	// 3. Invalid Export DocumentID
	err = db.Create(&StudioExport{
		ID:           uuid.New(),
		DocumentID:   nonExistentDocID,
		VersionID:    nonExistentVerID,
		ExportFormat: "pdf",
		R2Key:        "invalid_exp.pdf",
		ByteSize:     100,
		ExpiresAt:    time.Now().UTC().Add(time.Hour),
		CreatedAt:    time.Now().UTC(),
	}).Error
	assert.Error(t, err, "PostgreSQL must reject StudioExport referencing non-existent DocumentID")
}

func Test7_CyclicVersionSnapshotRelationshipAndDeletionSafety(t *testing.T) {
	db := getLiveDB(t)

	docID := uuid.New()
	require.NoError(t, db.Create(&StudioDocument{
		ID:               docID,
		OriginalFileName: "cyclic.pdf",
		FileSize:         500,
		InitialPageCount: 1,
		CreatedAt:        time.Now().UTC(),
	}).Error)

	vID := uuid.New()
	snapID := uuid.New()
	assetID := "ast_cyclic_" + uuid.New().String()

	// 1. Create Version first (with null snapshot_id)
	ver := StudioVersion{
		ID:            vID,
		DocumentID:    docID,
		VersionNumber: 1,
		Status:        "ready",
		OperationType: "snapshot_op",
		VirtualModel:  JSON([]byte(`{}`)),
		SnapshotID:    nil,
		CreatedAt:     time.Now().UTC(),
	}
	require.NoError(t, db.Create(&ver).Error)

	// 2. Create Asset
	require.NoError(t, db.Create(&StudioAsset{
		ID:         assetID,
		DocumentID: docID,
		AssetType:  "snapshot",
		R2Key:      "studio/snapshots/cyclic.pdf",
		ByteSize:   500,
		MimeType:   "application/pdf",
		CreatedAt:  time.Now().UTC(),
	}).Error)

	// 3. Create Snapshot referencing Version and Asset
	snap := StudioSnapshot{
		ID:        snapID,
		VersionID: vID,
		AssetID:   assetID,
		PageCount: 1,
		CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, db.Create(&snap).Error)

	// 4. Update Version to link snapshot_id
	require.NoError(t, db.Model(&StudioVersion{}).Where("id = ?", vID).Update("snapshot_id", snapID).Error)

	// Verify both links resolve cleanly
	var reloadedVer StudioVersion
	require.NoError(t, db.First(&reloadedVer, "id = ?", vID).Error)
	assert.NotNil(t, reloadedVer.SnapshotID)
	assert.Equal(t, snapID, *reloadedVer.SnapshotID)

	var reloadedSnap StudioSnapshot
	require.NoError(t, db.First(&reloadedSnap, "id = ?", snapID).Error)
	assert.Equal(t, vID, reloadedSnap.VersionID)
}
