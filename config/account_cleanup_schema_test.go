package config

import (
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
	"time"
)

func TestAccountCleanupMigrationGateAndRollbackPreserveIntent(t *testing.T) {
	db, pool := durabilityMigrationFixture(t)
	createPriorDurabilitySchema(t, db)
	require.NoError(t, runDurabilityScript(pool, backendDurabilityDraftSQL))
	require.Error(t, ValidateAccountCleanupSchema(db))
	require.NoError(t, runDurabilityScript(pool, accountCleanupMigrationSQL))
	require.NoError(t, ValidateAccountCleanupSchema(db))
	require.NoError(t, db.Exec("ALTER TABLE account_storage_cleanups ALTER COLUMN next_attempt_at DROP NOT NULL").Error)
	require.Error(t, ValidateAccountCleanupSchema(db))
	require.NoError(t, db.Exec("ALTER TABLE account_storage_cleanups ALTER COLUMN next_attempt_at SET NOT NULL").Error)
	require.NoError(t, runDurabilityScript(pool, accountCleanupMigrationSQL))
	trace := &migrationTrace{}
	require.NoError(t, ValidateAccountCleanupSchema(db.Session(&gorm.Session{Logger: trace})))
	require.Empty(t, trace.ddl())
	require.NoError(t, db.Create(&AccountStorageCleanup{UserID: uuid.NewString(), NextAttemptAt: time.Now()}).Error)
	require.ErrorContains(t, runDurabilityScript(pool, accountCleanupRollbackSQL), "pending account cleanup")
	require.NoError(t, db.Exec("DELETE FROM account_storage_cleanups").Error)
	require.NoError(t, runDurabilityScript(pool, accountCleanupRollbackSQL))
	require.Error(t, ValidateAccountCleanupSchema(db))
}
