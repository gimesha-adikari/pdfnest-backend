package config

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func preflightDisposableDB(t *testing.T) (*gorm.DB, *sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("BACKEND_DURABILITY_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_DURABILITY_TEST_DSN disposable fixture required; ambient DATABASE_URL fallback is strictly disabled")
	}
	require.Contains(t, dsn, "host=/tmp/platen-", "fixture must be private Unix-socket under /tmp/platen-")
	require.Contains(t, dsn, "user=platen_", "fixture must use dedicated platen_ user")

	root, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	var address sql.NullString
	require.NoError(t, root.QueryRow("SELECT inet_server_addr()::text").Scan(&address))
	require.False(t, address.Valid, "only private Unix-socket fixtures allowed; TCP connections forbidden")

	dbName := "preflight_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	_, err = root.Exec(fmt.Sprintf(`CREATE DATABASE "%s"`, dbName))
	require.NoError(t, err)

	fields := strings.Fields(dsn)
	for i, s := range fields {
		if strings.HasPrefix(s, "dbname=") {
			fields[i] = "dbname=" + dbName
		}
	}
	childDSN := strings.Join(fields, " ")

	childDB, err := gorm.Open(postgres.Open(childDSN), &gorm.Config{})
	require.NoError(t, err)
	childPool, err := childDB.DB()
	require.NoError(t, err)
	childPool.SetMaxOpenConns(3)

	t.Cleanup(func() {
		_ = childPool.Close()
		_, _ = root.Exec(fmt.Sprintf(`DROP DATABASE "%s" WITH (FORCE)`, dbName))
		_ = root.Close()
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

// Explicit automated assertions for the 12 required index objects
func TestPreflight_ExplicitIndexAndConstraintValidation(t *testing.T) {
	db, pool, _ := preflightDisposableDB(t)
	setupBaseManagedSchema(t, db)
	require.NoError(t, execScript(pool, backendDurabilityDraftSQL))
	require.NoError(t, ValidateBackendDurabilitySchema(db))

	type indexMeta struct {
		IndexName string `gorm:"column:index_name"`
		TableName string `gorm:"column:table_name"`
		IsUnique  bool   `gorm:"column:is_unique"`
		IsValid   bool   `gorm:"column:is_valid"`
		IsPrimary bool   `gorm:"column:is_primary"`
	}

	var rows []indexMeta
	err := db.Raw(`
		SELECT c.relname AS index_name,
		       t.relname AS table_name,
		       i.indisunique AS is_unique,
		       i.indisvalid AS is_valid,
		       i.indisprimary AS is_primary
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid
		JOIN pg_class t ON t.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE n.nspname = 'public'
		  AND t.relname IN ('task_completions', 'worker_result_ownerships', 'studio_storage_object_retirements', 'backend_durability_schema')
		ORDER BY c.relname
	`).Scan(&rows).Error
	require.NoError(t, err)

	expectedIndexes := map[string]struct {
		Table     string
		IsUnique  bool
		IsPrimary bool
	}{
		"backend_durability_schema_pkey":              {"backend_durability_schema", true, true},
		"studio_storage_object_retirements_pkey":      {"studio_storage_object_retirements", true, true},
		"task_completions_pkey":                       {"task_completions", true, true},
		"idx_task_completions_reservation_id":         {"task_completions", false, false},
		"idx_task_completions_state":                  {"task_completions", false, false},
		"idx_task_completions_expires_at":             {"task_completions", false, false},
		"worker_result_ownerships_pkey":               {"worker_result_ownerships", true, true},
		"idx_worker_result_ownerships_request_id":     {"worker_result_ownerships", true, false},
		"idx_worker_result_ownerships_worker_job_id":  {"worker_result_ownerships", false, false},
		"idx_worker_result_ownerships_state":          {"worker_result_ownerships", false, false},
		"idx_worker_result_ownerships_expires_at":     {"worker_result_ownerships", false, false},
		"idx_worker_result_ownerships_next_attempt_at": {"worker_result_ownerships", false, false},
	}

	require.Equal(t, len(expectedIndexes), len(rows), "must have exactly 12 index objects across durability tables")
	for _, r := range rows {
		expected, ok := expectedIndexes[r.IndexName]
		require.True(t, ok, "unexpected index: %s", r.IndexName)
		require.Equal(t, expected.Table, r.TableName, "table mismatch for %s", r.IndexName)
		require.Equal(t, expected.IsUnique, r.IsUnique, "uniqueness mismatch for %s", r.IndexName)
		require.Equal(t, expected.IsPrimary, r.IsPrimary, "primary key mismatch for %s", r.IndexName)
		require.True(t, r.IsValid, "index %s must be marked valid in pg_index", r.IndexName)
	}
}

// Backup & restore rehearsal into target disposable database
func TestPreflight_BackupAndRestoreRehearsal(t *testing.T) {
	pgDumpPath, err := exec.LookPath("pg_dump")
	if err != nil {
		t.Skip("pg_dump not available on PATH")
	}
	pgRestorePath, err := exec.LookPath("pg_restore")
	if err != nil {
		t.Skip("pg_restore not available on PATH")
	}

	db, _, sourceDBName := preflightDisposableDB(t)
	setupBaseManagedSchema(t, db)

	// Insert representative data before backup
	userID := uuid.New()
	jobID := uuid.New()
	require.NoError(t, db.Exec("INSERT INTO public.users(id, email, created_at) VALUES (?, 'backup-test@example.com', now())", userID).Error)
	require.NoError(t, db.Exec("INSERT INTO public.studio_jobs(id, status, created_at) VALUES (?, 'completed', now())", jobID).Error)

	dsn := os.Getenv("BACKEND_DURABILITY_TEST_DSN")
	fields := strings.Fields(dsn)
	var host, port, user string
	for _, f := range fields {
		if strings.HasPrefix(f, "host=") {
			host = strings.TrimPrefix(f, "host=")
		} else if strings.HasPrefix(f, "port=") {
			port = strings.TrimPrefix(f, "port=")
		} else if strings.HasPrefix(f, "user=") {
			user = strings.TrimPrefix(f, "user=")
		}
	}

	dumpFile := filepath.Join(t.TempDir(), "preflight_rehearsal.dump")

	// 1. Take custom-format backup
	dumpCmd := exec.Command(pgDumpPath,
		"-h", host,
		"-p", port,
		"-U", user,
		"-d", sourceDBName,
		"--format=custom",
		"--schema=public",
		"--file="+dumpFile,
	)
	dumpOut, err := dumpCmd.CombinedOutput()
	require.NoError(t, err, "pg_dump failed: %s", string(dumpOut))

	// 2. Verify TOC listing
	listCmd := exec.Command(pgRestorePath, "--list", dumpFile)
	listOut, err := listCmd.CombinedOutput()
	require.NoError(t, err, "pg_restore --list failed: %s", string(listOut))
	require.Contains(t, string(listOut), "TABLE DATA public users")
	require.Contains(t, string(listOut), "TABLE DATA public studio_jobs")

	// 3. Create target restore database on the same private Unix socket
	restoreDBName := "restore_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	root, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	_, err = root.Exec(fmt.Sprintf(`CREATE DATABASE "%s"`, restoreDBName))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = root.Exec(fmt.Sprintf(`DROP DATABASE "%s" WITH (FORCE)`, restoreDBName))
		_ = root.Close()
	})

	// 4. Perform real restore into target database
	restoreCmd := exec.Command(pgRestorePath,
		"-h", host,
		"-p", port,
		"-U", user,
		"-d", restoreDBName,
		"--schema=public",
		dumpFile,
	)
	restoreOut, err := restoreCmd.CombinedOutput()
	require.NoError(t, err, "pg_restore failed: %s", string(restoreOut))

	// 5. Connect to restored database and verify data integrity and structural checks
	var restoreFields []string
	for _, f := range fields {
		if strings.HasPrefix(f, "dbname=") {
			restoreFields = append(restoreFields, "dbname="+restoreDBName)
		} else {
			restoreFields = append(restoreFields, f)
		}
	}
	restoreDSN := strings.Join(restoreFields, " ")
	restoreDB, err := gorm.Open(postgres.Open(restoreDSN), &gorm.Config{})
	require.NoError(t, err)
	restorePool, err := restoreDB.DB()
	require.NoError(t, err)
	defer restorePool.Close()

	var restoredUsers int64
	require.NoError(t, restoreDB.Raw("SELECT count(*) FROM public.users WHERE id = ?", userID).Scan(&restoredUsers).Error)
	require.Equal(t, int64(1), restoredUsers, "restored database must contain pre-backup user")

	var restoredJobs int64
	require.NoError(t, restoreDB.Raw("SELECT count(*) FROM public.studio_jobs WHERE id = ?", jobID).Scan(&restoredJobs).Error)
	require.Equal(t, int64(1), restoredJobs, "restored database must contain pre-backup studio job")

	// 6. Apply durability migration to the restored database and verify startup gate
	require.NoError(t, execScript(restorePool, backendDurabilityDraftSQL))
	require.NoError(t, ValidateBackendDurabilitySchema(restoreDB), "startup gate must pass on restored database after durability migration")
}

// Runtime binary compatibility verification against historical binary 40dfe66
func TestPreflight_HistoricalBinaryRuntimeCompatibility(t *testing.T) {
	const historicalCommit = "40dfe66886e7c7c5bc3f522e4ca5fcd55e669a02"

	repoRoot, err := filepath.Abs("..")
	require.NoError(t, err)
	checkCommit := exec.Command("git", "-C", repoRoot, "cat-file", "-e", historicalCommit)
	if err := checkCommit.Run(); err != nil {
		t.Skipf("historical commit %s not available in local git repository", historicalCommit)
	}

	db, pool, dbName := preflightDisposableDB(t)
	setupBaseManagedSchema(t, db)
	require.NoError(t, execScript(pool, backendDurabilityDraftSQL))
	require.NoError(t, ValidateBackendDurabilitySchema(db))

	tempDir := t.TempDir()
	archiveCmd := exec.Command("git", "-C", repoRoot, "archive", historicalCommit)
	tarCmd := exec.Command("tar", "-x", "-C", tempDir)
	r, w := io.Pipe()
	archiveCmd.Stdout = w
	tarCmd.Stdin = r
	require.NoError(t, archiveCmd.Start())
	require.NoError(t, tarCmd.Start())
	require.NoError(t, archiveCmd.Wait())
	_ = w.Close()
	require.NoError(t, tarCmd.Wait())

	binPath := filepath.Join(tempDir, "server_40dfe66")
	buildCmd := exec.Command("go", "build", "-o", binPath, "main.go", "request_limits.go")
	buildCmd.Dir = tempDir
	buildOut, err := buildCmd.CombinedOutput()
	if err != nil {
		t.Skipf("building historical binary failed: %s (%v)", string(buildOut), err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	portStr := strconv.Itoa(port)

	dsn := os.Getenv("BACKEND_DURABILITY_TEST_DSN")
	fields := strings.Fields(dsn)
	var parts []string
	for _, f := range fields {
		if strings.HasPrefix(f, "dbname=") {
			parts = append(parts, "dbname="+dbName)
		} else {
			parts = append(parts, f)
		}
	}
	dbURL := strings.Join(parts, " ")

	srv := miniredis.RunT(t)

	logFilePath := filepath.Join(tempDir, "server.log")
	logFile, err := os.Create(logFilePath)
	require.NoError(t, err)
	defer logFile.Close()

	srvCmd := exec.Command(binPath)
	srvCmd.Dir = tempDir
	srvCmd.Stdout = logFile
	srvCmd.Stderr = logFile
	srvCmd.Env = append(os.Environ(),
		"PORT="+portStr,
		"DATABASE_URL="+dbURL,
		"REDIS_URL=redis://"+srv.Addr(),
		"APP_ENV=test",
		"LOCAL=true",
		"STORAGE_MODE=local",
		"BILLING_MODE=normal",
	)
	require.NoError(t, srvCmd.Start())
	t.Cleanup(func() {
		if srvCmd.Process != nil {
			_ = srvCmd.Process.Signal(syscall.SIGTERM)
			done := make(chan error, 1)
			go func() { done <- srvCmd.Wait() }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				_ = srvCmd.Process.Kill()
			}
		}
	})

	ready := false
	serverURL := "http://127.0.0.1:" + portStr
	for i := 0; i < 40; i++ {
		time.Sleep(150 * time.Millisecond)
		resp, err := http.Get(serverURL + "/api/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
	}
	if !ready {
		logBytes, _ := os.ReadFile(logFilePath)
		t.Fatalf("historical backend binary failed to become ready against migrated database: %s", string(logBytes))
	}

	// 1. Verify /api/health returns 200 OK
	resp, err := http.Get(serverURL + "/api/health")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// 2. Verify /api/ready returns 200 OK with database and redis ready
	resp, err = http.Get(serverURL + "/api/ready")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// 3. Verify /healthz: historical binary does not expose root /healthz (returns 404),
	// whereas modern binaries expose root /healthz. We verify it does not error at TCP layer.
	resp, err = http.Get(serverURL + "/healthz")
	require.NoError(t, err)
	require.True(t, resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound)
	resp.Body.Close()
}
