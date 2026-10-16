package config

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type migrationTrace struct {
	mu         sync.Mutex
	statements []string
	hook       func(string)
}

func (l *migrationTrace) LogMode(logger.LogLevel) logger.Interface      { return l }
func (l *migrationTrace) Info(context.Context, string, ...interface{})  {}
func (l *migrationTrace) Warn(context.Context, string, ...interface{})  {}
func (l *migrationTrace) Error(context.Context, string, ...interface{}) {}
func (l *migrationTrace) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	s, _ := fc()
	l.mu.Lock()
	l.statements = append(l.statements, s)
	l.mu.Unlock()
	if l.hook != nil {
		l.hook(s)
	}
}
func (l *migrationTrace) reset() { l.mu.Lock(); defer l.mu.Unlock(); l.statements = nil }
func (l *migrationTrace) ddl() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, s := range l.statements {
		u := strings.ToUpper(strings.TrimSpace(s))
		if strings.HasPrefix(u, "CREATE ") || strings.HasPrefix(u, "ALTER ") || strings.HasPrefix(u, "DROP ") {
			out = append(out, s)
		}
	}
	return out
}

func migrationDatabase(t *testing.T) (*gorm.DB, *sql.DB, string, *migrationTrace) {
	t.Helper()
	raw := os.Getenv("PLATEN_MIGRATION_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("PLATEN_MIGRATION_TEST_DATABASE_URL not set")
	}
	u, e := url.Parse(raw)
	require.NoError(t, e)
	require.Equal(t, "postgres", u.Scheme)
	require.Contains(t, []string{"127.0.0.1", "localhost", "::1"}, u.Hostname(), "loopback fixture required")
	require.True(t, strings.HasPrefix(strings.TrimPrefix(u.Path, "/"), "test_"), "test_ database required")
	require.NotEmpty(t, u.Port())
	for key := range u.Query() {
		require.Contains(t, []string{"sslmode", "application_name", "connect_timeout", "options"}, key, "fixture URL must not override host/database or read credential files")
	}
	root, e := sql.Open("pgx", raw)
	require.NoError(t, e)
	defer root.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var name, address string
	require.NoError(t, root.QueryRowContext(ctx, "SELECT current_database(),host(inet_server_addr())").Scan(&name, &address))
	require.Equal(t, strings.TrimPrefix(u.Path, "/"), name)
	require.Contains(t, []string{"127.0.0.1", "::1"}, address)
	var b [8]byte
	_, e = rand.Read(b[:])
	require.NoError(t, e)
	name = fmt.Sprintf("test_m1s222_%x", b)
	_, e = root.ExecContext(ctx, `CREATE DATABASE "`+name+`"`)
	require.NoError(t, e)
	u.Path = "/" + name
	q := u.Query()
	q.Set("application_name", name)
	q.Set("options", "-c statement_timeout=5000 -c lock_timeout=2000")
	u.RawQuery = q.Encode()
	l := &migrationTrace{}
	db, e := gorm.Open(postgres.Open(u.String()), &gorm.Config{Logger: l})
	require.NoError(t, e)
	s, e := db.DB()
	require.NoError(t, e)
	s.SetMaxOpenConns(3)
	s.SetMaxIdleConns(3)
	t.Cleanup(func() {
		require.NoError(t, s.Close())
		r, e := sql.Open("pgx", raw)
		require.NoError(t, e)
		defer r.Close()
		c, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		// All task-owned pools have closed before this cleanup. Do not use
		// FORCE: signaling a background/other-role backend requires privileges
		// unrelated to migration ownership. Normal DROP retains its own bounded
		// backend interlock and must still fail if owned clients remain.
		_, e = r.ExecContext(c, `DROP DATABASE "`+name+`"`)
		require.NoError(t, e)
	})
	return db, s, u.String(), l
}
func historicalSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	b, e := os.ReadFile("testdata/historical_schema.sql")
	require.NoError(t, e)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return tx.Exec(string(b)).Error }))
}

func TestManagedSchemaRejectsHistoricalDrift(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{
		{"column", `ALTER TABLE public.users DROP COLUMN password_hash`},
		{"nullability", `ALTER TABLE public.users ALTER COLUMN session_version DROP NOT NULL`},
		{"unique_index", `DROP INDEX public.idx_users_email`},
		{"foreign_key", `ALTER TABLE public.subscriptions DROP CONSTRAINT fk_subscriptions_user`},
		{"sequence", `DROP SEQUENCE public.contact_ticket_sequence`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _, _, _ := migrationDatabase(t)
			historicalSchema(t, db)
			require.NoError(t, db.Exec(tc.sql).Error)
			require.Error(t, ValidateManagedSchema(db), "managed startup must reject structural drift")
		})
	}
}

func independentMigrationObserver(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	s, e := sql.Open("pgx", dsn)
	require.NoError(t, e)
	s.SetMaxOpenConns(2)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}

const migrationSnapshotSQL = `SELECT md5(string_agg(item,E'\n' ORDER BY item)) FROM (
 SELECT 'column:'||c.relname||':'||a.attname||':'||pg_catalog.format_type(a.atttypid,a.atttypmod)||':'||a.attnotnull||':'||coalesce(pg_catalog.pg_get_expr(d.adbin,d.adrelid),'') AS item
 FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid=a.attrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum WHERE n.nspname='public' AND a.attnum>0 AND NOT a.attisdropped
 UNION ALL SELECT 'constraint:'||c.conname||':'||pg_catalog.pg_get_constraintdef(c.oid)||':'||c.convalidated FROM pg_catalog.pg_constraint c JOIN pg_catalog.pg_namespace n ON n.oid=c.connamespace WHERE n.nspname='public'
 UNION ALL SELECT 'index:'||indexname||':'||indexdef FROM pg_catalog.pg_indexes WHERE schemaname='public'
 UNION ALL SELECT 'sequence:'||sequencename||':'||start_value||':'||increment_by||':'||cycle FROM pg_catalog.pg_sequences WHERE schemaname='public')s`

func migrationSnapshot(t *testing.T, s *sql.DB) string {
	t.Helper()
	var v string
	require.NoError(t, s.QueryRow(migrationSnapshotSQL).Scan(&v))
	return v
}
func assertNoMigrationLocks(t *testing.T, s *sql.DB) {
	t.Helper()
	var n int
	var detail string
	require.NoError(t, s.QueryRow(`SELECT count(*),coalesce(string_agg(l.pid::text||':'||l.granted::text||':'||coalesce(a.state,''),','),'') FROM pg_catalog.pg_locks l LEFT JOIN pg_catalog.pg_stat_activity a ON a.pid=l.pid WHERE l.locktype='advisory' AND l.objid=pg_catalog.hashtext('pdfnest:managed-schema')::oid AND l.database=(SELECT oid FROM pg_catalog.pg_database WHERE datname=current_database())`).Scan(&n, &detail))
	require.Zero(t, n, "migration locks at return: %s", detail)
}

func testMigrationLimits() migrationLimits {
	return migrationLimits{Lock: time.Second, Statement: 3 * time.Second, Overall: 10 * time.Second}
}

func TestManagedMigrationHistoricalNoOp(t *testing.T) {
	db, _, dsn, l := migrationDatabase(t)
	historicalSchema(t, db)
	obs := independentMigrationObserver(t, dsn)
	require.NoError(t, db.Exec(`CREATE FUNCTION public.reject_ledger_writes() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'unexpected ledger write'; END $$; CREATE TRIGGER reject_ledger_writes BEFORE INSERT OR UPDATE OR DELETE ON public.schema_migrations FOR EACH STATEMENT EXECUTE FUNCTION public.reject_ledger_writes()`).Error)
	before := migrationSnapshot(t, obs)
	var at time.Time
	require.NoError(t, obs.QueryRow("SELECT applied_at FROM public.schema_migrations").Scan(&at))
	l.reset()
	tx, e := obs.Begin()
	require.NoError(t, e)
	defer tx.Rollback()
	_, e = tx.Exec("LOCK TABLE public.admin_subscription_mutations IN ACCESS SHARE MODE")
	require.NoError(t, e)
	require.NoError(t, runManagedSchemaMigrations(db, false, testMigrationLimits()))
	require.NoError(t, ValidateManagedSchema(db))
	require.Empty(t, l.ddl())
	require.Equal(t, before, migrationSnapshot(t, obs))
	var after time.Time
	require.NoError(t, obs.QueryRow("SELECT applied_at FROM public.schema_migrations").Scan(&after))
	require.True(t, at.Equal(after))
	assertNoMigrationLocks(t, obs)
	require.NoError(t, tx.Rollback())
}
func TestManagedMigrationInitialization(t *testing.T) {
	db, _, dsn, l := migrationDatabase(t)
	obs := independentMigrationObserver(t, dsn)
	require.Error(t, runManagedSchemaMigrations(db, false, testMigrationLimits()))
	require.Empty(t, l.ddl())
	l.reset()
	require.NoError(t, runManagedSchemaMigrations(db, true, testMigrationLimits()))
	require.NotEmpty(t, l.ddl())
	var tables, versions int
	require.NoError(t, obs.QueryRow("SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE'").Scan(&tables))
	require.Equal(t, 27, tables)
	require.NoError(t, obs.QueryRow("SELECT count(*) FROM public.schema_migrations WHERE version='20260918_01_admin_subscription_mutation'").Scan(&versions))
	require.Equal(t, 1, versions)
	require.NoError(t, ValidateManagedSchema(db))
	assertNoMigrationLocks(t, obs)
	l.reset()
	require.NoError(t, runManagedSchemaMigrations(db, true, testMigrationLimits()))
	require.Empty(t, l.ddl(), "initialization permission must not bypass the applied-version gate")
}

func TestManagedMigrationRejectsAmbiguousState(t *testing.T) {
	for _, tc := range []struct{ name, setup string }{
		{"populated_without_ledger", `CREATE TABLE public.unversioned_data(id integer); INSERT INTO public.unversioned_data VALUES(1)`},
		{"empty_ledger", `CREATE TABLE public.schema_migrations(version varchar(64) PRIMARY KEY,applied_at timestamptz NOT NULL)`},
		{"unknown_version", `CREATE TABLE public.schema_migrations(version varchar(64) PRIMARY KEY,applied_at timestamptz NOT NULL); INSERT INTO public.schema_migrations VALUES('synthetic_unknown_marker',now())`},
		{"custom_schema", `CREATE SCHEMA synthetic_existing`},
		{"user_function", `CREATE FUNCTION public.synthetic_function() RETURNS integer LANGUAGE sql AS 'SELECT 1'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _, _, l := migrationDatabase(t)
			require.NoError(t, db.Exec(tc.setup).Error)
			l.reset()
			err := runManagedSchemaMigrations(db, true, testMigrationLimits())
			require.Error(t, err)
			require.NotContains(t, err.Error(), "synthetic_unknown_marker")
			require.Empty(t, l.ddl())
			require.Error(t, ValidateManagedSchema(db))
		})
	}
	t.Run("extra_version_with_historical_marker", func(t *testing.T) {
		db, _, _, l := migrationDatabase(t)
		historicalSchema(t, db)
		require.NoError(t, db.Exec("INSERT INTO public.schema_migrations VALUES('synthetic_unknown_marker',now())").Error)
		l.reset()
		require.Error(t, runManagedSchemaMigrations(db, false, testMigrationLimits()))
		require.Error(t, ValidateManagedSchema(db))
		require.Empty(t, l.ddl())
	})
}

func TestManagedSchemaDefinitionDrift(t *testing.T) {
	for _, tc := range []struct{ name, change string }{
		{"column_type", `ALTER TABLE public.users ALTER COLUMN password_hash TYPE varchar(20)`},
		{"column_default", `ALTER TABLE public.users ALTER COLUMN session_version SET DEFAULT 1`},
		{"nullable_column_strengthened", `ALTER TABLE public.users ALTER COLUMN password_hash SET NOT NULL`},
		{"index_not_unique", `DROP INDEX public.idx_users_email; CREATE INDEX idx_users_email ON public.users(email)`},
		{"index_wrong_expression", `DROP INDEX public.idx_users_email; CREATE UNIQUE INDEX idx_users_email ON public.users(lower(email))`},
		{"foreign_key_wrong_action", `ALTER TABLE public.subscriptions DROP CONSTRAINT fk_subscriptions_user; ALTER TABLE public.subscriptions ADD CONSTRAINT fk_subscriptions_user FOREIGN KEY(user_id) REFERENCES public.users(id) ON DELETE RESTRICT ON UPDATE CASCADE`},
		{"foreign_key_unvalidated", `ALTER TABLE public.subscriptions DROP CONSTRAINT fk_subscriptions_user; ALTER TABLE public.subscriptions ADD CONSTRAINT fk_subscriptions_user FOREIGN KEY(user_id) REFERENCES public.users(id) ON DELETE CASCADE ON UPDATE CASCADE NOT VALID`},
		{"sequence_increment", `ALTER SEQUENCE public.contact_ticket_sequence INCREMENT BY 2`},
		{"sequence_ownership", `ALTER SEQUENCE public.home_page_contents_id_seq OWNED BY NONE`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _, dsn, l := migrationDatabase(t)
			historicalSchema(t, db)
			require.NoError(t, db.Exec(tc.change).Error)
			obs := independentMigrationObserver(t, dsn)
			before := migrationSnapshot(t, obs)
			l.reset()
			require.Error(t, runManagedSchemaMigrations(db, false, testMigrationLimits()))
			require.Error(t, ValidateManagedSchema(db))
			require.Equal(t, before, migrationSnapshot(t, obs))
			require.Empty(t, l.ddl())
		})
	}
}

func migrationPool(t *testing.T, dsn, label string) (*gorm.DB, *sql.DB, *migrationTrace) {
	t.Helper()
	u, e := url.Parse(dsn)
	require.NoError(t, e)
	q := u.Query()
	q.Set("application_name", label)
	u.RawQuery = q.Encode()
	l := &migrationTrace{}
	g, e := gorm.Open(postgres.Open(u.String()), &gorm.Config{Logger: l})
	require.NoError(t, e)
	s, e := g.DB()
	require.NoError(t, e)
	s.SetMaxOpenConns(1)
	s.SetMaxIdleConns(1)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return g, s, l
}
func migrationResult(t *testing.T, c <-chan error) error {
	t.Helper()
	select {
	case e := <-c:
		return e
	case <-time.After(12 * time.Second):
		t.Fatal("migration did not complete within test bound")
		return nil
	}
}
func migrationBarrier(t *testing.T, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(5 * time.Second):
		t.Fatal("migration barrier not reached")
	}
}
func migrationBlocker(t *testing.T, obs *sql.DB, app string) (int, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var pid, blocker int
		e := obs.QueryRowContext(ctx, "SELECT pid,(pg_catalog.pg_blocking_pids(pid))[1] FROM pg_catalog.pg_stat_activity WHERE application_name=$1 AND cardinality(pg_catalog.pg_blocking_pids(pid))>0", app).Scan(&pid, &blocker)
		if e == nil {
			return pid, blocker
		}
		if e != sql.ErrNoRows {
			require.NoError(t, e)
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("PostgreSQL blocker not observed")
			return 0, 0
		}
	}
}
func migrationOwner(t *testing.T, obs *sql.DB) int {
	t.Helper()
	var pid int
	require.NoError(t, obs.QueryRow(`SELECT pid FROM pg_catalog.pg_locks WHERE locktype='advisory' AND granted AND objid=pg_catalog.hashtext('pdfnest:managed-schema')::oid AND database=(SELECT oid FROM pg_catalog.pg_database WHERE datname=current_database())`).Scan(&pid))
	return pid
}

func TestManagedMigrationIndependentPools(t *testing.T) {
	for _, churn := range []bool{false, true} {
		t.Run(fmt.Sprintf("churn_%t", churn), func(t *testing.T) {
			_, _, dsn, _ := migrationDatabase(t)
			g1, s1, l1 := migrationPool(t, dsn, "m1s222_first")
			g2, _, l2 := migrationPool(t, dsn, "m1s222_second")
			if churn {
				s1.SetMaxIdleConns(0)
			}
			obs := independentMigrationObserver(t, dsn)
			locked := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			unlock := func() { once.Do(func() { close(release) }) }
			defer unlock()
			l1.hook = func(q string) {
				if strings.Contains(q, "pg_advisory_xact_lock") {
					close(locked)
					select {
					case <-release:
					case <-time.After(5 * time.Second):
					}
				}
			}
			done1 := make(chan error, 1)
			done2 := make(chan error, 1)
			go func() { done1 <- runManagedSchemaMigrations(g1, true, testMigrationLimits()) }()
			migrationBarrier(t, locked)
			owner := migrationOwner(t, obs)
			go func() { done2 <- runManagedSchemaMigrations(g2, true, testMigrationLimits()) }()
			pid, blocker := migrationBlocker(t, obs, "m1s222_second")
			require.Equal(t, owner, blocker)
			require.NotEqual(t, owner, pid)
			unlock()
			require.NoError(t, migrationResult(t, done1))
			require.NoError(t, migrationResult(t, done2))
			require.Empty(t, l2.ddl(), "second caller must recheck after first commit")
			require.NoError(t, ValidateManagedSchema(g2))
			assertNoMigrationLocks(t, obs)
			t.Logf("owner=%d blocked_second=%d churn=%t", owner, pid, churn)
		})
	}
}

func TestManagedMigrationAtomicFailure(t *testing.T) {
	for _, mode := range []string{"disconnect_after_ddl", "cancel_after_ledger"} {
		t.Run(mode, func(t *testing.T) {
			db, s, dsn, l := migrationDatabase(t)
			s.SetMaxOpenConns(1)
			s.SetMaxIdleConns(0)
			obs := independentMigrationObserver(t, dsn)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fired := false
			var hookErr error
			var owner int
			l.hook = func(q string) {
				if fired {
					return
				}
				match := mode == "disconnect_after_ddl" && strings.HasPrefix(q, "CREATE SEQUENCE IF NOT EXISTS public.contact_ticket_sequence") || mode == "cancel_after_ledger" && strings.HasPrefix(q, "INSERT INTO public.schema_migrations")
				if !match {
					return
				}
				fired = true
				owner = migrationOwner(t, obs)
				if mode == "cancel_after_ledger" {
					cancel()
					return
				}
				var pid int
				hookErr = obs.QueryRow(`SELECT pid FROM pg_catalog.pg_locks WHERE locktype='advisory' AND granted AND objid=pg_catalog.hashtext('pdfnest:managed-schema')::oid AND database=(SELECT oid FROM pg_catalog.pg_database WHERE datname=current_database())`).Scan(&pid)
				if hookErr == nil {
					_, hookErr = obs.Exec("SELECT pg_catalog.pg_terminate_backend($1)", pid)
				}
			}
			err := runManagedSchemaMigrations(db.WithContext(ctx), true, testMigrationLimits())
			require.Error(t, err)
			require.True(t, fired, "late failure must occur after real schema DDL/ledger work")
			require.NoError(t, hookErr)
			require.Zero(t, s.Stats().InUse)
			awaitMigrationBackendCleanup(t, obs, owner)
			var n int
			require.NoError(t, obs.QueryRow("SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public'").Scan(&n))
			require.Zero(t, n, "all tables, indexes, sequences and ledger must roll back")
			assertNoMigrationLocks(t, obs)
			l.hook = nil
			require.NoError(t, runManagedSchemaMigrations(db, true, testMigrationLimits()))
			require.NoError(t, ValidateManagedSchema(db))
			assertNoMigrationLocks(t, obs)
		})
	}
}

func TestManagedMigrationBoundedWaits(t *testing.T) {
	for _, mode := range []string{"lock_timeout", "statement_timeout", "cancellation"} {
		t.Run(mode, func(t *testing.T) {
			db, pool, dsn, _ := migrationDatabase(t)
			obs := independentMigrationObserver(t, dsn)
			block, e := obs.Begin()
			require.NoError(t, e)
			defer block.Rollback()
			_, e = block.Exec("SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtext('pdfnest:managed-schema'))")
			require.NoError(t, e)
			l := migrationLimits{Lock: 150 * time.Millisecond, Statement: 500 * time.Millisecond, Overall: 2 * time.Second}
			if mode == "statement_timeout" {
				l.Lock = 500 * time.Millisecond
				l.Statement = 150 * time.Millisecond
			}
			if mode == "cancellation" {
				l.Lock = time.Second
				l.Statement = time.Second
			}
			if mode == "overall_deadline" {
				l.Lock = 150 * time.Millisecond
				l.Statement = 150 * time.Millisecond
				l.Overall = 150 * time.Millisecond
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- runManagedSchemaMigrations(db.WithContext(ctx), true, l) }()
			u, e := url.Parse(dsn)
			require.NoError(t, e)
			pid, blocker := migrationBlocker(t, obs, u.Query().Get("application_name"))
			require.NotEqual(t, pid, blocker)
			if mode == "cancellation" {
				cancel()
			}
			err := migrationResult(t, done)
			require.Error(t, err)
			switch mode {
			case "lock_timeout":
				require.Contains(t, err.Error(), "55P03")
			case "statement_timeout":
				require.Contains(t, err.Error(), "57014")
			case "cancellation":
				require.ErrorIs(t, err, context.Canceled)
			case "overall_deadline":
				require.True(t, strings.Contains(err.Error(), "deadline") || strings.Contains(err.Error(), "57014") || strings.Contains(err.Error(), "55P03"))
			}
			require.Zero(t, pool.Stats().InUse, "no canceled transaction may keep a pooled connection lease")
			awaitMigrationBackendCleanup(t, obs, pid)
			require.NoError(t, block.Rollback())
			assertNoMigrationLocks(t, obs)
			var n int
			require.NoError(t, obs.QueryRow("SELECT count(*) FROM information_schema.tables WHERE table_schema='public'").Scan(&n))
			require.Zero(t, n)
			t.Logf("waiter=%d blocker=%d mode=%s", pid, blocker, mode)
		})
	}
}

// Driver cancellation/physical disconnect is asynchronous at the server. Wait
// for a real backend-state barrier before releasing the test's blocking owner;
// never replace cleanup proof with a sleep or an unchecked eventual result.
func awaitMigrationBackendCleanup(t *testing.T, s *sql.DB, pid int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var done bool
		require.NoError(t, s.QueryRowContext(ctx, "SELECT NOT EXISTS(SELECT 1 FROM pg_catalog.pg_stat_activity WHERE pid=$1 AND xact_start IS NOT NULL)", pid).Scan(&done))
		if done {
			return
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal("canceled PostgreSQL backend retained transaction beyond cleanup bound")
			return
		}
	}
}

func TestManagedMigrationOverallPoolDeadline(t *testing.T) {
	db, s, dsn, _ := migrationDatabase(t)
	s.SetMaxOpenConns(1)
	held, e := s.Conn(context.Background())
	require.NoError(t, e)
	defer held.Close()
	done := make(chan error, 1)
	l := migrationLimits{Lock: 50 * time.Millisecond, Statement: 100 * time.Millisecond, Overall: 150 * time.Millisecond}
	go func() { done <- runManagedSchemaMigrations(db, true, l) }()
	require.ErrorIs(t, migrationResult(t, done), context.DeadlineExceeded)
	require.NoError(t, held.Close())
	obs := independentMigrationObserver(t, dsn)
	assertNoMigrationLocks(t, obs)
	var n int
	require.NoError(t, obs.QueryRow("SELECT count(*) FROM information_schema.tables WHERE table_schema='public'").Scan(&n))
	require.Zero(t, n)
}

func TestManagedMigrationSharedSmallPool(t *testing.T) {
	for _, size := range []int{1, 2} {
		t.Run(fmt.Sprintf("connections_%d", size), func(t *testing.T) {
			db, s, dsn, l := migrationDatabase(t)
			s.SetMaxOpenConns(size)
			s.SetMaxIdleConns(0)
			obs := independentMigrationObserver(t, dsn)
			entered := make(chan struct{})
			release := make(chan struct{})
			var gate, ungate sync.Once
			defer ungate.Do(func() { close(release) })
			l.hook = func(q string) {
				if strings.Contains(q, "pg_advisory_xact_lock") {
					gate.Do(func() {
						close(entered)
						select {
						case <-release:
						case <-time.After(5 * time.Second):
						}
					})
				}
			}
			a := make(chan error, 1)
			b := make(chan error, 1)
			go func() { a <- runManagedSchemaMigrations(db, true, testMigrationLimits()) }()
			migrationBarrier(t, entered)
			go func() { b <- runManagedSchemaMigrations(db, true, testMigrationLimits()) }()
			if size == 1 {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				tick := time.NewTicker(5 * time.Millisecond)
				defer tick.Stop()
				for s.Stats().WaitCount == 0 {
					select {
					case <-tick.C:
					case <-ctx.Done():
						t.Fatal("second caller did not wait for the pinned connection")
					}
				}
			} else {
				u, e := url.Parse(dsn)
				require.NoError(t, e)
				_, _ = migrationBlocker(t, obs, u.Query().Get("application_name"))
			}
			ungate.Do(func() { close(release) })
			require.NoError(t, migrationResult(t, a))
			require.NoError(t, migrationResult(t, b))
			require.NoError(t, ValidateManagedSchema(db))
			assertNoMigrationLocks(t, obs)
		})
	}
}

func TestManagedSchemaRejectsInvalidUniqueIndex(t *testing.T) {
	db, _, dsn, l := migrationDatabase(t)
	historicalSchema(t, db)
	require.NoError(t, db.Exec(`DROP INDEX public.idx_users_email; INSERT INTO public.users(id,email) VALUES('00000000-0000-0000-0000-000000000001','invalid-index@example.invalid'),('00000000-0000-0000-0000-000000000002','invalid-index@example.invalid')`).Error)
	require.Error(t, db.Exec(`CREATE UNIQUE INDEX CONCURRENTLY idx_users_email ON public.users(email)`).Error)
	obs := independentMigrationObserver(t, dsn)
	var valid bool
	require.NoError(t, obs.QueryRow("SELECT indisvalid FROM pg_catalog.pg_index WHERE indexrelid='public.idx_users_email'::regclass").Scan(&valid))
	require.False(t, valid)
	assertIndexPolicyRejected(t, db, obs, l)
}

func TestUnmanagedMigrationRemainsAutomaticAndAtomic(t *testing.T) {
	db, _, dsn, _ := migrationDatabase(t)
	obs := independentMigrationObserver(t, dsn)
	require.NoError(t, RunSchemaMigrations(db))
	require.NoError(t, db.Exec("ALTER TABLE public.users DROP COLUMN password_hash").Error)
	require.NoError(t, RunSchemaMigrations(db))
	var count int
	require.NoError(t, obs.QueryRow("SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='users' AND column_name='password_hash'").Scan(&count))
	require.Equal(t, 1, count)
	assertNoMigrationLocks(t, obs)
	require.Error(t, db.Transaction(func(tx *gorm.DB) error { return RunSchemaMigrations(tx) }), "runner must not silently use nested savepoints")
}

func TestMigrationDeadlineConfiguration(t *testing.T) {
	for _, tc := range []struct{ name, value string }{{"PLATEN_MIGRATION_LOCK_TIMEOUT", "0"}, {"PLATEN_MIGRATION_STATEMENT_TIMEOUT", "invalid-synthetic-value"}, {"PLATEN_MIGRATION_TIMEOUT", "-1s"}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.name, tc.value)
			_, err := limitsFromEnvironment()
			require.Error(t, err)
			require.NotContains(t, err.Error(), tc.value, "diagnostics must not echo arbitrary environment values")
		})
	}
	t.Run("submillisecond", func(t *testing.T) {
		t.Setenv("PLATEN_MIGRATION_LOCK_TIMEOUT", "1ns")
		_, err := limitsFromEnvironment()
		require.Error(t, err)
	})
}

func TestManagedSchemaRejectsWriteRestrictions(t *testing.T) {
	for _, tc := range []struct{ name, change string }{
		{"extra_required_column", `ALTER TABLE public.users ADD COLUMN required_external_value integer NOT NULL`},
		{"domain_column", `CREATE DOMAIN public.required_annotation AS text NOT NULL; ALTER TABLE public.users ADD COLUMN annotation public.required_annotation`},
		{"rewrite_rule", `CREATE RULE suppress_user_insert AS ON INSERT TO public.users DO INSTEAD NOTHING`},
		{"expression_index", `CREATE INDEX incompatible_expression ON public.users ((1/(session_version-session_version)))`},
		{"predicate_index", `CREATE INDEX incompatible_predicate ON public.users(id) WHERE 1/(session_version-session_version)=1`},
		{"inbound_fk", `CREATE TABLE public.external_owner(user_id uuid REFERENCES public.users(id) ON DELETE RESTRICT)`},
		{"external_schema_fk", `CREATE SCHEMA extra; CREATE TABLE extra.external_owner(user_id uuid REFERENCES public.users(id) ON DELETE RESTRICT)`},
		{"extra_default_column", `ALTER TABLE public.users ADD COLUMN external_default integer DEFAULT 1`},
		{"extra_unique_index", `CREATE UNIQUE INDEX incompatible_unique ON public.users(session_version)`},
		{"extra_check", `ALTER TABLE public.users ADD CONSTRAINT incompatible_check CHECK(false)`},
		{"extra_fk", `ALTER TABLE public.users ADD CONSTRAINT incompatible_fk FOREIGN KEY(id) REFERENCES public.studio_documents(id) NOT VALID`},
		{"row_security", `ALTER TABLE public.users ENABLE ROW LEVEL SECURITY`},
		{"user_trigger", `CREATE FUNCTION public.suppress_user_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NULL; END $$; CREATE TRIGGER suppress_user_insert BEFORE INSERT ON public.users FOR EACH ROW EXECUTE FUNCTION public.suppress_user_insert()`},
		{"identity", `ALTER TABLE public.admin_subscription_mutations ALTER COLUMN requested_credits ADD GENERATED ALWAYS AS IDENTITY`},
		{"unlogged_table", `ALTER TABLE public.analyzer_sessions SET UNLOGGED`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _, _, l := migrationDatabase(t)
			historicalSchema(t, db)
			require.NoError(t, db.Exec(tc.change).Error)
			l.reset()
			require.Error(t, ValidateManagedSchema(db))
			require.Error(t, runManagedSchemaMigrations(db, false, testMigrationLimits()))
			require.Empty(t, l.ddl())
		})
	}
}

func TestManagedSchemaAllowsHarmlessAdditions(t *testing.T) {
	db, _, _, l := migrationDatabase(t)
	historicalSchema(t, db)
	require.NoError(t, db.Exec(`ALTER TABLE public.users ADD COLUMN optional_annotation text; CREATE INDEX additional_nonunique ON public.users(password_hash); CREATE TABLE public.unrelated_synthetic(id integer)`).Error)
	l.reset()
	require.NoError(t, runManagedSchemaMigrations(db, false, testMigrationLimits()))
	require.Empty(t, l.ddl())
	require.NoError(t, db.Create(&User{ID: "00000000-0000-0000-0000-000000000001", Email: "historical-one@example.invalid"}).Error)
	require.NoError(t, db.Create(&User{ID: "00000000-0000-0000-0000-000000000002", Email: "historical-two@example.invalid"}).Error)
}

func TestManagedSchemaRejectsDisabledFKEnforcement(t *testing.T) {
	db, _, dsn, l := migrationDatabase(t)
	historicalSchema(t, db)
	raw := os.Getenv("PLATEN_MIGRATION_TEST_ADMIN_DATABASE_URL")
	if raw == "" {
		t.Skip("admin loopback fixture URL required only to disable internal FK triggers")
	}
	adminURL, e := url.Parse(raw)
	require.NoError(t, e)
	target, e := url.Parse(dsn)
	require.NoError(t, e)
	require.Equal(t, "postgres", adminURL.Scheme)
	require.Equal(t, target.Host, adminURL.Host, "admin fixture must use the same verified loopback server")
	require.True(t, strings.HasPrefix(adminURL.Path, "/test_"))
	for k := range adminURL.Query() {
		require.Contains(t, []string{"sslmode", "connect_timeout"}, k)
	}
	adminURL.Path = target.Path
	admin, e := sql.Open("pgx", adminURL.String())
	require.NoError(t, e)
	defer admin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, e = admin.ExecContext(ctx, "ALTER TABLE public.subscriptions DISABLE TRIGGER ALL")
	require.NoError(t, e)
	l.reset()
	require.Error(t, ValidateManagedSchema(db))
	require.Error(t, runManagedSchemaMigrations(db, false, testMigrationLimits()))
	require.Empty(t, l.ddl())
}

func TestManagedMigrationRejectsUnversionedCollation(t *testing.T) {
	db, _, _, l := migrationDatabase(t)
	require.NoError(t, db.Exec(`CREATE COLLATION public.synthetic_existing FROM pg_catalog."C"`).Error)
	l.reset()
	require.Error(t, runManagedSchemaMigrations(db, true, testMigrationLimits()))
	require.Empty(t, l.ddl())
}

// Inspect the actual extra index rows as well as the historical catalog and
// ledger. A failed validation must not repair or write any of these objects.
func assertIndexPolicyRejected(t *testing.T, db *gorm.DB, obs *sql.DB, l *migrationTrace) {
	t.Helper()
	snapshot := func() string {
		var value string
		require.NoError(t, obs.QueryRow(`SELECT md5(coalesce((SELECT string_agg(to_jsonb(x)::text||to_jsonb(c)::text,E'\n' ORDER BY x.indexrelid) FROM pg_catalog.pg_index x JOIN pg_catalog.pg_class c ON c.oid=x.indexrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public'),'')|| (SELECT jsonb_agg(s ORDER BY version)::text FROM public.schema_migrations s))`).Scan(&value))
		return migrationSnapshot(t, obs) + value
	}
	before := snapshot()
	l.reset()
	if err := ValidateManagedSchema(db); err == nil {
		t.Error("startup accepted unsupported additional index")
	} else {
		require.Contains(t, err.Error(), "schema incompatible:")
	}
	if err := runManagedSchemaMigrations(db, false, testMigrationLimits()); err == nil {
		t.Error("managed migrate accepted unsupported additional index")
	} else {
		require.Contains(t, err.Error(), "schema incompatible:")
	}
	require.Empty(t, l.ddl())
	require.Equal(t, before, snapshot(), "rejection must preserve catalogs and ledger")
	l.mu.Lock()
	statements := append([]string(nil), l.statements...)
	l.mu.Unlock()
	for _, statement := range statements {
		command := strings.ToUpper(strings.TrimSpace(statement))
		require.False(t, strings.HasPrefix(command, "INSERT ") || strings.HasPrefix(command, "UPDATE ") || strings.HasPrefix(command, "DELETE "), "no ledger/application DML during rejection")
	}
	assertNoMigrationLocks(t, obs)
}

func TestManagedSchemaAdditionalIndexPolicy(t *testing.T) {
	for _, tc := range []struct{ name, ddl string }{
		{"missing_required", `DROP INDEX public.idx_users_email`},
		{"altered_required", `DROP INDEX public.idx_users_email; CREATE INDEX idx_users_email ON public.users(email)`},
		{"include_payload", `CREATE INDEX additional_ticket_payload ON public.contact_tickets(id) INCLUDE(message)`},
		{"unsupported_varchar_width", `CREATE INDEX additional_wide_varchar ON public.studio_assets(r2_key)`},
		{"unbounded_text_key", `CREATE INDEX additional_message ON public.contact_tickets(message)`},
		{"different_collation", `CREATE INDEX additional_collation ON public.users(password_hash COLLATE "C")`},
		{"descending", `CREATE INDEX additional_desc ON public.users(session_version DESC)`},
		{"nulls_first", `CREATE INDEX additional_nulls ON public.users(session_version NULLS FIRST)`},
		{"nondefault_opclass", `CREATE INDEX additional_pattern ON public.users(password_hash varchar_pattern_ops)`},
		{"expression", `CREATE INDEX additional_expression ON public.users((session_version+1))`},
		{"predicate", `CREATE INDEX additional_partial ON public.users(session_version) WHERE session_version>0`},
		{"fillfactor", `CREATE INDEX additional_storage ON public.users(session_version) WITH(fillfactor=70)`},
		{"deduplication", `CREATE INDEX additional_dedup ON public.users(session_version) WITH(deduplicate_items=off)`},
		{"multiple_keys", `CREATE INDEX additional_multiple ON public.users(session_version,id)`},
		{"nonhistorical_key", `ALTER TABLE public.users ADD COLUMN extra_key bigint; CREATE INDEX additional_extra ON public.users(extra_key)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _, dsn, l := migrationDatabase(t)
			historicalSchema(t, db)
			require.NoError(t, db.Exec(tc.ddl).Error)
			assertIndexPolicyRejected(t, db, independentMigrationObserver(t, dsn), l)
		})
	}
}

func TestManagedSchemaAdditionalIndexPositive(t *testing.T) {
	for _, tc := range []struct{ name, ddl string }{
		{"uuid", `CREATE INDEX additional_bounded ON public.users(id)`},
		{"bigint", `CREATE INDEX additional_bounded ON public.users(session_version)`},
		{"bounded_varchar", `CREATE INDEX additional_bounded ON public.users(password_hash)`},
		{"explicit_defaults", `CREATE INDEX additional_bounded ON public.users(password_hash COLLATE "default" text_ops ASC NULLS LAST)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _, dsn, l := migrationDatabase(t)
			historicalSchema(t, db)
			require.NoError(t, db.Exec(tc.ddl).Error)
			obs := independentMigrationObserver(t, dsn)
			before := migrationSnapshot(t, obs)
			l.reset()
			require.NoError(t, ValidateManagedSchema(db))
			require.NoError(t, runManagedSchemaMigrations(db, false, testMigrationLimits()))
			require.Empty(t, l.ddl())
			require.Equal(t, before, migrationSnapshot(t, obs))
			// 255 four-byte characters exercise the accepted varchar's maximum bytes.
			require.NoError(t, db.Create(&User{ID: "00000000-0000-0000-0000-000000000003", Email: "bounded@example.invalid", PasswordHash: strings.Repeat("🙂", 255)}).Error)
		})
	}
}

func TestManagedSchemaAdditionalIndexOversizeCharacterization(t *testing.T) {
	for _, shape := range []string{"key", "include"} {
		t.Run(shape, func(t *testing.T) {
			db, _, dsn, l := migrationDatabase(t)
			historicalSchema(t, db)
			obs := independentMigrationObserver(t, dsn)
			insert := `INSERT INTO public.contact_tickets(id,ticket_number,email,category,subject,message) SELECT '00000000-0000-0000-0000-000000000004','synthetic-ticket','synthetic@example.invalid','synthetic','synthetic',string_agg(md5(g::text),'') FROM generate_series(1,1024) g`
			require.NoError(t, db.Exec(insert).Error, "historically valid unbounded text must store without extra index")
			require.NoError(t, db.Exec("DELETE FROM public.contact_tickets").Error)
			ddl := `CREATE INDEX additional_wide ON public.contact_tickets(message)`
			if shape == "include" {
				ddl = `CREATE INDEX additional_wide ON public.contact_tickets(id) INCLUDE(message)`
			}
			require.NoError(t, db.Exec(ddl).Error)
			err := db.Exec(insert).Error
			require.Error(t, err)
			require.Contains(t, err.Error(), "54000", "actual PostgreSQL oversized index row, not a model validation")
			var rows int
			require.NoError(t, obs.QueryRow("SELECT count(*) FROM public.contact_tickets").Scan(&rows))
			require.Zero(t, rows)
			t.Logf("%s: synthetic 32768-byte message succeeds without extra index, fails with SQLSTATE 54000", shape)
			assertIndexPolicyRejected(t, db, obs, l)
		})
	}
}

func TestManagedSchemaAdditionalIndexIncomplete(t *testing.T) {
	for _, phase := range []string{"not_ready", "ready_not_valid"} {
		t.Run(phase, func(t *testing.T) {
			db, _, dsn, l := migrationDatabase(t)
			historicalSchema(t, db)
			obs := independentMigrationObserver(t, dsn)
			block, e := obs.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
			require.NoError(t, e)
			defer block.Rollback()
			var blocker int
			require.NoError(t, block.QueryRow("SELECT pg_backend_pid()").Scan(&blocker))
			if phase == "not_ready" {
				_, e = block.Exec("UPDATE public.users SET session_version=session_version WHERE false")
			} else {
				_, e = block.Exec("SELECT * FROM public.users")
			}
			require.NoError(t, e)
			build, _, _ := migrationPool(t, dsn, "m1s222r_build")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- build.WithContext(ctx).Exec("CREATE INDEX CONCURRENTLY additional_incomplete ON public.users(session_version)").Error
			}()
			pid, owner := migrationBlocker(t, obs, "m1s222r_build")
			require.Equal(t, blocker, owner)
			var valid, ready, live bool
			require.NoError(t, obs.QueryRow("SELECT indisvalid,indisready,indislive FROM pg_index WHERE indexrelid='public.additional_incomplete'::regclass").Scan(&valid, &ready, &live))
			require.False(t, valid)
			require.Equal(t, phase == "ready_not_valid", ready)
			require.True(t, live)
			cancel()
			require.Error(t, migrationResult(t, done))
			awaitMigrationBackendCleanup(t, obs, pid)
			require.NoError(t, block.Rollback())
			require.NoError(t, obs.QueryRow("SELECT indisvalid,indisready,indislive FROM pg_index WHERE indexrelid='public.additional_incomplete'::regclass").Scan(&valid, &ready, &live))
			t.Logf("%s: actual canceled concurrent index valid=%t ready=%t live=%t build=%d blocker=%d", phase, valid, ready, live, pid, owner)
			assertIndexPolicyRejected(t, db, obs, l)
		})
	}
}

func TestManagedSchemaAdditionalIndexNotLive(t *testing.T) {
	db, _, dsn, l := migrationDatabase(t)
	historicalSchema(t, db)
	require.NoError(t, db.Exec("CREATE INDEX additional_dead ON public.users(session_version)").Error)
	obs := independentMigrationObserver(t, dsn)
	first, e := obs.Begin()
	require.NoError(t, e)
	defer first.Rollback()
	_, e = first.Exec("SELECT * FROM public.users")
	require.NoError(t, e)
	var firstPID int
	require.NoError(t, first.QueryRow("SELECT pg_backend_pid()").Scan(&firstPID))
	drop, _, _ := migrationPool(t, dsn, "m1s222r_drop")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- drop.WithContext(ctx).Exec("DROP INDEX CONCURRENTLY public.additional_dead").Error }()
	pid, blocker := migrationBlocker(t, obs, "m1s222r_drop")
	require.Equal(t, firstPID, blocker)
	// Open a reader after invalidation's first wait has already selected lockers.
	// It holds the second wait after DROP commits ready=false/live=false.
	second, e := obs.Begin()
	require.NoError(t, e)
	defer second.Rollback()
	_, e = second.Exec("SELECT * FROM public.users")
	require.NoError(t, e)
	var secondPID int
	require.NoError(t, second.QueryRow("SELECT pg_backend_pid()").Scan(&secondPID))
	require.NoError(t, first.Rollback())
	wait, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var dead bool
		require.NoError(t, obs.QueryRowContext(wait, `SELECT NOT x.indislive AND $1=ANY(pg_blocking_pids($2)) FROM pg_index x WHERE x.indexrelid='public.additional_dead'::regclass`, secondPID, pid).Scan(&dead))
		if dead {
			break
		}
		select {
		case <-tick.C:
		case <-wait.Done():
			t.Fatal("concurrent DROP did not reach dead-index barrier")
		}
	}
	cancel()
	require.Error(t, migrationResult(t, done))
	awaitMigrationBackendCleanup(t, obs, pid)
	require.NoError(t, second.Rollback())
	var valid, ready, live bool
	require.NoError(t, obs.QueryRow("SELECT indisvalid,indisready,indislive FROM pg_index WHERE indexrelid='public.additional_dead'::regclass").Scan(&valid, &ready, &live))
	require.False(t, valid)
	require.False(t, ready)
	require.False(t, live)
	t.Logf("actual canceled concurrent DROP valid=%t ready=%t live=%t drop=%d first=%d second=%d", valid, ready, live, pid, firstPID, secondPID)
	assertIndexPolicyRejected(t, db, obs, l)
}
