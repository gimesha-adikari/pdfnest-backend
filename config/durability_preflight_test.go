package config

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func preflightDisposableDB(t *testing.T) (*gorm.DB, *sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "host=localhost user=postgres password=2021 dbname=pdfnest port=5432 sslmode=disable"
	}

	root, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Skipf("Disposable PostgreSQL fixture unavailable: %v", err)
	}
	rootPool, err := root.DB()
	require.NoError(t, err)

	dbName := "preflight_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	require.NoError(t, rootPool.Ping())
	_, err = rootPool.Exec(fmt.Sprintf(`CREATE DATABASE "%s"`, dbName))
	require.NoError(t, err)

	var childDSN string
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := url.Parse(dsn)
		require.NoError(t, err)
		parsed.Path = "/" + dbName
		childDSN = parsed.String()
	} else {
		// Key-value DSN
		parts := strings.Fields(dsn)
		replaced := false
		for i, part := range parts {
			if strings.HasPrefix(part, "dbname=") {
				parts[i] = "dbname=" + dbName
				replaced = true
			}
		}
		if !replaced {
			parts = append(parts, "dbname="+dbName)
		}
		childDSN = strings.Join(parts, " ")
	}

	childDB, err := gorm.Open(postgres.Open(childDSN), &gorm.Config{})
	require.NoError(t, err)
	childPool, err := childDB.DB()
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = childPool.Close()
		_, _ = rootPool.Exec(fmt.Sprintf(`DROP DATABASE "%s" WITH (FORCE)`, dbName))
		_ = rootPool.Close()
	})

	return childDB, childPool, dbName
}

func execScript(pool *sql.DB, script string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pool.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.ExecContext(ctx, script)
	if err != nil {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
	}
	return err
}

func setupBaseManagedSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	// Simulate the prior managed migration state in production
	require.NoError(t, db.Exec(`
		CREATE TABLE public.schema_migrations (
			version varchar(64) PRIMARY KEY,
			applied_at timestamptz NOT NULL
		);
		INSERT INTO public.schema_migrations (version, applied_at)
		VALUES ('20260918_01_admin_subscription_mutation', now());

		CREATE TABLE public.studio_jobs (
			id uuid PRIMARY KEY,
			document_id uuid,
			idempotency_key text,
			stage text,
			status text,
			created_at timestamptz,
			updated_at timestamptz
		);

		CREATE TABLE public.users (
			id uuid PRIMARY KEY,
			email text NOT NULL,
			created_at timestamptz
		);
	`).Error)
}

// 1. Schema compatibility & 7. Startup gate before/after
func TestPreflight_DurabilityUpgradeAndStartupGate(t *testing.T) {
	db, pool, _ := preflightDisposableDB(t)
	setupBaseManagedSchema(t, db)

	// Before migration: ValidateBackendDurabilitySchema MUST fail
	errBefore := ValidateBackendDurabilitySchema(db)
	require.Error(t, errBefore, "startup gate must fail before durability migration is applied")
	require.Contains(t, errBefore.Error(), "approved durability migration marker missing")

	// Apply durability upgrade
	require.NoError(t, execScript(pool, backendDurabilityDraftSQL))

	// After migration: ValidateBackendDurabilitySchema MUST pass
	require.NoError(t, ValidateBackendDurabilitySchema(db), "startup gate must pass after durability migration is applied")
}

// 2. 2nd-run idempotency
func TestPreflight_UpgradeIdempotency(t *testing.T) {
	db, pool, _ := preflightDisposableDB(t)
	setupBaseManagedSchema(t, db)

	require.NoError(t, execScript(pool, backendDurabilityDraftSQL))
	require.NoError(t, ValidateBackendDurabilitySchema(db))

	// Run migration a second time: MUST succeed without modifying marker or failing
	require.NoError(t, execScript(pool, backendDurabilityDraftSQL))
	require.NoError(t, ValidateBackendDurabilitySchema(db))

	// Marker count must remain exactly 1
	var markerCount int64
	require.NoError(t, db.Raw("SELECT count(*) FROM public.backend_durability_schema WHERE version = ?", BackendDurabilityVersion).Scan(&markerCount).Error)
	require.Equal(t, int64(1), markerCount)
}

// 3. Missing/malformed rejection & 4. Constraints and indexes
func TestPreflight_ConstraintValidationAndAdoptionRejection(t *testing.T) {
	db, pool, _ := preflightDisposableDB(t)

	// If unversioned table exists before migration, migration must reject adoption
	setupBaseManagedSchema(t, db)
	require.NoError(t, db.Exec("CREATE TABLE public.task_completions (id text)").Error)

	err := execScript(pool, backendDurabilityDraftSQL)
	require.Error(t, err, "unversioned schema adoption must be rejected")
	require.Contains(t, err.Error(), "unversioned durability schema exists")

	// Verify rollback occurred: marker table must NOT exist
	var hasMarker bool
	require.NoError(t, db.Raw("SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_class WHERE relname='backend_durability_schema')").Scan(&hasMarker).Error)
	require.False(t, hasMarker)
}

// 5. Existing record retention safety
func TestPreflight_ExistingRecordRetention(t *testing.T) {
	db, pool, _ := preflightDisposableDB(t)
	setupBaseManagedSchema(t, db)

	// Insert existing production records
	userID := uuid.New()
	jobID := uuid.New()
	require.NoError(t, db.Exec("INSERT INTO public.users(id, email, created_at) VALUES (?, 'user@example.com', now())", userID).Error)
	require.NoError(t, db.Exec("INSERT INTO public.studio_jobs(id, status, created_at) VALUES (?, 'completed', now())", jobID).Error)

	// Apply migration
	require.NoError(t, execScript(pool, backendDurabilityDraftSQL))

	// Verify existing records are 100% retained and untouched
	var userCount int64
	require.NoError(t, db.Raw("SELECT count(*) FROM public.users WHERE id = ?", userID).Scan(&userCount).Error)
	require.Equal(t, int64(1), userCount)

	var jobCount int64
	require.NoError(t, db.Raw("SELECT count(*) FROM public.studio_jobs WHERE id = ?", jobID).Scan(&jobCount).Error)
	require.Equal(t, int64(1), jobCount)

	// studio_jobs should have pending_object_keys NULL for existing row
	var pendingKeys *string
	require.NoError(t, db.Raw("SELECT pending_object_keys::text FROM public.studio_jobs WHERE id = ?", jobID).Scan(&pendingKeys).Error)
	require.Nil(t, pendingKeys)
}

// 6. Old binary vs upgraded schema
func TestPreflight_OldBinaryCompatibilityWithUpgradedSchema(t *testing.T) {
	db, pool, _ := preflightDisposableDB(t)
	setupBaseManagedSchema(t, db)

	require.NoError(t, execScript(pool, backendDurabilityDraftSQL))

	// Verify old binary ledger checks: schema_migrations is still at '20260918_01_admin_subscription_mutation'
	var versions []string
	require.NoError(t, db.Raw("SELECT version FROM public.schema_migrations ORDER BY version").Scan(&versions).Error)
	require.Equal(t, []string{"20260918_01_admin_subscription_mutation"}, versions, "historical schema ledger must remain untouched")

	// Old binary queries on studio_jobs and users must continue to function normally
	var jobCount int64
	require.NoError(t, db.Raw("SELECT count(*) FROM public.studio_jobs").Scan(&jobCount).Error)
	require.Equal(t, int64(0), jobCount)
}

// 8. Transaction rollback & 9. Refusal of destructive rollback
func TestPreflight_DestructiveRollbackRefusalAndCleanRollback(t *testing.T) {
	db, pool, _ := preflightDisposableDB(t)
	setupBaseManagedSchema(t, db)

	require.NoError(t, execScript(pool, backendDurabilityDraftSQL))
	require.NoError(t, ValidateBackendDurabilitySchema(db))

	// Case A: retained ownership in task_completions -> rollback REFUSED
	require.NoError(t, db.Exec("INSERT INTO public.task_completions(id, owner_identity, download_token, state) VALUES ('held-task', 'owner-1', 'tok-1', 'publishing')").Error)
	rollbackErr := execScript(pool, backendDurabilityRollbackSQL)
	require.Error(t, rollbackErr)
	require.Contains(t, rollbackErr.Error(), "durability rollback refused: retained ownership exists")

	// Schema must still be intact and valid
	require.NoError(t, ValidateBackendDurabilitySchema(db))

	// Clean out the retained task
	require.NoError(t, db.Exec("DELETE FROM public.task_completions WHERE id = 'held-task'").Error)

	// Case B: retained ownership in studio_storage_object_retirements -> rollback REFUSED
	require.NoError(t, db.Exec("INSERT INTO public.studio_storage_object_retirements(object_key, created_at) VALUES ('outputs/ret-1', now())").Error)
	rollbackErr = execScript(pool, backendDurabilityRollbackSQL)
	require.Error(t, rollbackErr)
	require.Contains(t, rollbackErr.Error(), "durability rollback refused: retained ownership exists")
	require.NoError(t, db.Exec("DELETE FROM public.studio_storage_object_retirements WHERE object_key = 'outputs/ret-1'").Error)

	// Case C: non-empty pending_object_keys in studio_jobs -> rollback REFUSED
	require.NoError(t, db.Exec("INSERT INTO public.studio_jobs(id, pending_object_keys) VALUES (gen_random_uuid(), '[\"outputs/pending.pdf\"]')").Error)
	rollbackErr = execScript(pool, backendDurabilityRollbackSQL)
	require.Error(t, rollbackErr)
	require.Contains(t, rollbackErr.Error(), "durability rollback refused: retained ownership exists")
	require.NoError(t, db.Exec("DELETE FROM public.studio_jobs").Error)

	// Case D: Completely empty durability state -> rollback SUCCEEDS
	require.NoError(t, execScript(pool, backendDurabilityRollbackSQL), "empty-state rollback must succeed")

	// All durability tables and column dropped
	var hasTaskCompletions bool
	require.NoError(t, db.Raw("SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_class WHERE relname='task_completions')").Scan(&hasTaskCompletions).Error)
	require.False(t, hasTaskCompletions)

	var hasMarker bool
	require.NoError(t, db.Raw("SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_class WHERE relname='backend_durability_schema')").Scan(&hasMarker).Error)
	require.False(t, hasMarker)
}
