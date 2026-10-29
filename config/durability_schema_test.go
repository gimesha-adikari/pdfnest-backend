package config

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// An explicit task-owned Unix-socket fixture is mandatory; never use DATABASE_URL.
func durabilityMigrationFixture(t *testing.T) (*gorm.DB, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("BACKEND_DURABILITY_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_DURABILITY_TEST_DSN disposable fixture required")
	}
	require.Contains(t, dsn, "host=/tmp/platen-")
	require.Contains(t, dsn, "user=platen_")
	root, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	var address sql.NullString
	require.NoError(t, root.QueryRow("SELECT inet_server_addr()::text").Scan(&address))
	require.False(t, address.Valid, "only private Unix-socket fixtures allowed")
	name := "test_durability_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = root.Exec(`CREATE DATABASE "` + name + `"`)
	require.NoError(t, err)
	fields := strings.Fields(dsn)
	for i, s := range fields {
		if strings.HasPrefix(s, "dbname=") {
			fields[i] = "dbname=" + name
		}
	}
	db, err := gorm.Open(postgres.Open(strings.Join(fields, " ")), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(3)
	t.Cleanup(func() {
		_ = pool.Close()
		_, _ = root.Exec(`DROP DATABASE "` + name + `" WITH (FORCE)`)
		_ = root.Close()
	})
	return db, pool
}
func runDurabilityScript(pool *sql.DB, script string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
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
func createPriorDurabilitySchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`CREATE TABLE public.studio_jobs(id uuid PRIMARY KEY);
 CREATE TABLE public.schema_migrations(version varchar(64) PRIMARY KEY,applied_at timestamptz NOT NULL);
 INSERT INTO public.schema_migrations VALUES ('20260918_01_admin_subscription_mutation',now());`).Error)
}
func TestBackendDurabilityDraftUpgradeIdempotenceAndReadOnlyValidation(t *testing.T) {
	db, pool := durabilityMigrationFixture(t)
	createPriorDurabilitySchema(t, db)
	require.Error(t, ValidateBackendDurabilitySchema(db))
	require.NoError(t, runDurabilityScript(pool, backendDurabilityDraftSQL))
	require.NoError(t, ValidateBackendDurabilitySchema(db))
	require.NoError(t, runDurabilityScript(pool, backendDurabilityDraftSQL))
	trace := &migrationTrace{}
	require.NoError(t, ValidateBackendDurabilitySchema(db.Session(&gorm.Session{Logger: trace})))
	require.Empty(t, trace.ddl(), "startup gate must never apply DDL")
	var versions []string
	require.NoError(t, db.Raw("SELECT version FROM schema_migrations").Scan(&versions).Error)
	require.Equal(t, []string{"20260918_01_admin_subscription_mutation"}, versions, "historical ledger immutable")
	require.NoError(t, db.Exec("ALTER TABLE task_completions ALTER COLUMN state DROP NOT NULL").Error)
	require.ErrorContains(t, ValidateBackendDurabilitySchema(db), "nullable")
	require.NoError(t, db.Exec("ALTER TABLE task_completions ALTER COLUMN state SET NOT NULL; DROP INDEX idx_worker_result_ownerships_request_id").Error)
	require.ErrorContains(t, ValidateBackendDurabilitySchema(db), "constraint")
}
func TestBackendDurabilityDraftFailureIsAtomicAndRejectsAdoption(t *testing.T) {
	db, pool := durabilityMigrationFixture(t)
	require.Error(t, runDurabilityScript(pool, backendDurabilityDraftSQL), "missing historical table must roll back entire draft")
	require.False(t, db.Migrator().HasTable("task_completions"))
	require.False(t, db.Migrator().HasTable("backend_durability_schema"))
	createPriorDurabilitySchema(t, db)
	require.NoError(t, db.Exec("CREATE TABLE task_completions(id text)").Error)
	require.ErrorContains(t, runDurabilityScript(pool, backendDurabilityDraftSQL), "unversioned")
	require.False(t, db.Migrator().HasTable("backend_durability_schema"))
}
func TestBackendDurabilityDraftRollbackRefusesRetainedOwnership(t *testing.T) {
	db, pool := durabilityMigrationFixture(t)
	createPriorDurabilitySchema(t, db)
	require.NoError(t, runDurabilityScript(pool, backendDurabilityDraftSQL))
	require.NoError(t, runDurabilityScript(pool, backendDurabilityRollbackSQL))
	require.False(t, db.Migrator().HasTable("task_completions"))
	require.NoError(t, runDurabilityScript(pool, backendDurabilityDraftSQL))
	for _, statement := range []string{
		"INSERT INTO task_completions(id,owner_identity,download_token,state) VALUES ('held','owner','token','publishing')",
		"INSERT INTO worker_result_ownerships(id,request_id,request_hash,submit_path,source_keys,state) VALUES ('held','request','hash','/submit','[]','uncertain')",
		"INSERT INTO studio_storage_object_retirements(object_key) VALUES ('outputs/retired')",
		"INSERT INTO studio_jobs(id,pending_object_keys) VALUES (gen_random_uuid(),'[\"outputs/pending\"]')",
	} {
		require.NoError(t, db.Exec(statement).Error)
		require.ErrorContains(t, runDurabilityScript(pool, backendDurabilityRollbackSQL), "retained ownership")
		require.NoError(t, ValidateBackendDurabilitySchema(db))
		require.NoError(t, db.Exec("TRUNCATE task_completions,worker_result_ownerships,studio_storage_object_retirements,studio_jobs").Error)
	}
	require.NoError(t, runDurabilityScript(pool, backendDurabilityRollbackSQL))
}
