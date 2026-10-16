package config

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	analyzerModels "pdfnest-backend/internal/analyzer/models"
	"pdfnest-backend/internal/models"
	studioModels "pdfnest-backend/internal/studio/models"
)

const managedSchemaVersion = "20260918_01_admin_subscription_mutation"
const priorManagedSchemaVersion = "20260914_01"

func schemaModels() []interface{} {
	return []interface{}{
		&User{}, &Subscription{}, &AdminSubscriptionMutation{}, &Transaction{}, &UsageLog{}, &OCRLanguageUsage{}, &WebhookLog{}, &BillingReservation{}, &UserSetting{},
		&ContactCategory{}, &ContactTicket{}, &models.HomePageContent{}, &models.SubscribePageContent{}, &models.DynamicToolItem{},
		models.AboutPageContent{}, &analyzerModels.AnalyzerSession{}, &studioModels.StudioDocument{}, &studioModels.StudioAsset{},
		&studioModels.StudioSnapshot{}, &studioModels.StudioVersion{}, &studioModels.StudioOperation{}, &studioModels.StudioJob{},
		&studioModels.StudioEditorState{}, &studioModels.StudioSession{}, &studioModels.StudioExport{}, &studioModels.StudioStorageCleanupTask{},
	}
}

// These finite safety caps are configurable; they are not production timing
// qualification. A caller's earlier context deadline always takes precedence.
type migrationLimits struct{ Lock, Statement, Overall time.Duration }

func defaultMigrationLimits() migrationLimits {
	return migrationLimits{5 * time.Second, 30 * time.Second, 2 * time.Minute}
}

func (l migrationLimits) validate() error {
	if l.Lock < time.Millisecond || l.Statement < time.Millisecond || l.Overall < l.Statement || l.Overall < l.Lock {
		return fmt.Errorf("migration deadlines require positive millisecond lock/statement limits within the overall deadline")
	}
	return nil
}
func limitsFromEnvironment() (migrationLimits, error) {
	l := defaultMigrationLimits()
	for _, v := range []struct {
		name  string
		value *time.Duration
	}{
		{"PLATEN_MIGRATION_LOCK_TIMEOUT", &l.Lock}, {"PLATEN_MIGRATION_STATEMENT_TIMEOUT", &l.Statement}, {"PLATEN_MIGRATION_TIMEOUT", &l.Overall},
	} {
		if raw := strings.TrimSpace(os.Getenv(v.name)); raw != "" {
			d, e := time.ParseDuration(raw)
			if e != nil {
				return l, fmt.Errorf("invalid duration in %s", v.name)
			}
			*v.value = d
		}
	}
	return l, l.validate()
}
func migrationContext(db *gorm.DB, l migrationLimits) (context.Context, context.CancelFunc, error) {
	if db == nil || db.Statement == nil || db.Dialector == nil || db.Dialector.Name() != "postgres" || db.DryRun {
		return nil, nil, fmt.Errorf("migration requires a live PostgreSQL pool")
	}
	if _, nested := db.Statement.ConnPool.(gorm.TxCommitter); nested {
		return nil, nil, fmt.Errorf("migration owns its transaction; an existing transaction is not supported")
	}
	if e := l.validate(); e != nil {
		return nil, nil, e
	}
	ctx, cancel := context.WithTimeout(db.Statement.Context, l.Overall)
	return ctx, cancel, nil
}
func setMigrationLimits(tx *gorm.DB, l migrationLimits) error {
	for _, v := range []struct {
		name string
		d    time.Duration
	}{{"lock_timeout", l.Lock}, {"statement_timeout", l.Statement}} {
		// Round up sub-millisecond remainders; never turn a bound into PostgreSQL's zero/unlimited setting.
		ms := v.d.Milliseconds()
		if v.d%time.Millisecond != 0 {
			ms++
		}
		if e := tx.Exec("SELECT pg_catalog.set_config(?, ?, true)", v.name, strconv.FormatInt(ms, 10)).Error; e != nil {
			return fmt.Errorf("set migration deadline: %w", e)
		}
	}
	return nil
}

// RunSchemaMigrations preserves unmanaged development's model synchronization.
// All synchronization and version recording now commit/roll back together, with
// transaction-owned exclusion. Managed release commands use the version gate.
func RunSchemaMigrations(db *gorm.DB) error {
	l := defaultMigrationLimits()
	ctx, cancel, e := migrationContext(db, l)
	if e != nil {
		return e
	}
	defer cancel()
	return db.WithContext(ctx).Connection(func(conn *gorm.DB) error {
		return conn.Transaction(func(tx *gorm.DB) error {
			if e := setMigrationLimits(tx, l); e != nil {
				return e
			}
			if e := lockMigration(tx); e != nil {
				return e
			}
			return applySchema(tx)
		})
	})
}
func lockMigration(tx *gorm.DB) error {
	if e := tx.Exec("SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtext('pdfnest:managed-schema'))").Error; e != nil {
		return fmt.Errorf("acquire transaction schema lock: %w", e)
	}
	return nil
}
func applySchema(tx *gorm.DB) error {
	if e := tx.Exec("SELECT pg_catalog.set_config('search_path', 'public, pg_catalog, pg_temp', true)").Error; e != nil {
		return e
	}
	if e := tx.Exec(`CREATE TABLE IF NOT EXISTS public.schema_migrations (version varchar(64) PRIMARY KEY, applied_at timestamptz NOT NULL)`).Error; e != nil {
		return fmt.Errorf("create migration ledger: %w", e)
	}
	if e := tx.AutoMigrate(schemaModels()...); e != nil {
		return fmt.Errorf("auto-migrate application schema: %w", e)
	}
	if e := tx.Exec("CREATE SEQUENCE IF NOT EXISTS public.contact_ticket_sequence START 1").Error; e != nil {
		return fmt.Errorf("create contact sequence: %w", e)
	}
	if e := tx.Exec("INSERT INTO public.schema_migrations(version,applied_at) VALUES (?,?) ON CONFLICT (version) DO NOTHING", managedSchemaVersion, time.Now().UTC()).Error; e != nil {
		return fmt.Errorf("record schema version: %w", e)
	}
	return nil
}

type managedSchemaState int

const (
	schemaApplied managedSchemaState = iota
	schemaEmpty
)

func inspectManagedSchema(tx *gorm.DB) (managedSchemaState, error) {
	var ledger bool
	if e := tx.Raw(`SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname='schema_migrations' AND c.relkind='r')`).Scan(&ledger).Error; e != nil {
		return schemaEmpty, fmt.Errorf("inspect migration ledger: %w", e)
	}
	if ledger {
		var versions []string
		if e := tx.Raw("SELECT version FROM public.schema_migrations ORDER BY version").Scan(&versions).Error; e != nil {
			return schemaEmpty, fmt.Errorf("read migration ledger: %w", e)
		}
		if !recognizedManagedSchemaVersions(versions) {
			return schemaEmpty, fmt.Errorf("schema incompatible: missing or unrecognized migration version; require an approved migration/adoption plan")
		}
		return schemaApplied, nil
	}
	var populated bool
	if e := tx.Raw(`SELECT
 EXISTS(SELECT 1 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname NOT LIKE 'pg\_%' AND n.nspname<>'information_schema') OR
 EXISTS(SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname NOT LIKE 'pg\_%' AND nspname NOT IN ('information_schema','public')) OR
 EXISTS(SELECT 1 FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname NOT LIKE 'pg\_%' AND n.nspname<>'information_schema') OR
 EXISTS(SELECT 1 FROM pg_catalog.pg_type t JOIN pg_catalog.pg_namespace n ON n.oid=t.typnamespace WHERE n.nspname NOT LIKE 'pg\_%' AND n.nspname<>'information_schema') OR
 EXISTS(SELECT 1 FROM pg_catalog.pg_depend d JOIN pg_catalog.pg_namespace n ON d.refclassid='pg_catalog.pg_namespace'::pg_catalog.regclass AND d.refobjid=n.oid WHERE n.nspname NOT LIKE 'pg\_%' AND n.nspname<>'information_schema') OR
 EXISTS(SELECT 1 FROM pg_catalog.pg_extension WHERE extname<>'plpgsql')`).Scan(&populated).Error; e != nil {
		return schemaEmpty, fmt.Errorf("inspect initialization eligibility: %w", e)
	}
	if populated {
		return schemaEmpty, fmt.Errorf("schema is unversioned and not empty; require an approved adoption plan")
	}
	return schemaEmpty, nil
}

// recognize only the exact ledger histories written by the historical runner;
// unknown or duplicate records must fail closed even when the current marker is present.
func recognizedManagedSchemaVersions(versions []string) bool {
	switch len(versions) {
	case 1:
		return versions[0] == managedSchemaVersion
	case 2:
		return (versions[0] == priorManagedSchemaVersion && versions[1] == managedSchemaVersion) ||
			(versions[0] == managedSchemaVersion && versions[1] == priorManagedSchemaVersion)
	default:
		return false
	}
}

func checkManagedReadOnly(db *gorm.DB, l migrationLimits) (managedSchemaState, error) {
	state := schemaEmpty
	err := db.Transaction(func(tx *gorm.DB) error {
		if e := setMigrationLimits(tx, l); e != nil {
			return e
		}
		var e error
		state, e = inspectManagedSchema(tx)
		if e != nil {
			return e
		}
		if state == schemaApplied {
			return validateHistoricalContract(tx)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return state, err
}

var errConcurrentInitialization = errors.New("schema initialization completed by another caller")

// Initialization is opt-in and only for an empty database. An applied version
// always takes a PostgreSQL READ ONLY compatibility path with no advisory/schema
// write locks or ledger writes. Unknown/unversioned populated states fail closed.
func runManagedSchemaMigrations(db *gorm.DB, allowInitialize bool, l migrationLimits) error {
	ctx, cancel, e := migrationContext(db, l)
	if e != nil {
		return e
	}
	defer cancel()
	db = db.WithContext(ctx)
	// Reserve the connection lease for this logical operation; all transaction
	// statements and locks use it. After cancellation, PostgreSQL may briefly
	// still be processing the driver's disconnect, not a reusable pooled session.
	return db.Connection(func(conn *gorm.DB) error {
		state, e := checkManagedReadOnly(conn, l)
		if e != nil || state == schemaApplied {
			return e
		}
		if !allowInitialize {
			return fmt.Errorf("schema is empty; initialization requires explicit PLATEN_ALLOW_SCHEMA_INITIALIZATION=true")
		}
		e = conn.Transaction(func(tx *gorm.DB) error {
			if e := setMigrationLimits(tx, l); e != nil {
				return e
			}
			if e := lockMigration(tx); e != nil {
				return e
			}
			// READ COMMITTED intentionally observes the winning commit after a lock wait.
			state, e := inspectManagedSchema(tx)
			if e != nil {
				return e
			}
			if state == schemaApplied {
				return errConcurrentInitialization
			}
			if e := applySchema(tx); e != nil {
				return e
			}
			return validateHistoricalContract(tx)
		}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if errors.Is(e, errConcurrentInitialization) {
			_, e = checkManagedReadOnly(conn, l)
		}
		return e
	})
}

// ValidateManagedSchema is also the managed application startup gate. It cannot
// create/repair schema, record a version or initialize an empty database.
func ValidateManagedSchema(db *gorm.DB) error {
	l, e := limitsFromEnvironment()
	if e != nil {
		return e
	}
	ctx, cancel, e := migrationContext(db, l)
	if e != nil {
		return e
	}
	defer cancel()
	return db.WithContext(ctx).Connection(func(conn *gorm.DB) error {
		state, e := checkManagedReadOnly(conn, l)
		if e != nil {
			return e
		}
		if state != schemaApplied {
			return fmt.Errorf("managed schema is empty; run an explicitly approved initialization")
		}
		return nil
	})
}
func RunManagedMigrations() error {
	if !IsManagedEnvironment() {
		return fmt.Errorf("the migrate command requires APP_ENV=canary, staging, or production")
	}
	if e := ValidateRuntimeConfig(); e != nil {
		return e
	}
	l, e := limitsFromEnvironment()
	if e != nil {
		return e
	}
	allow := false
	switch strings.TrimSpace(os.Getenv("PLATEN_ALLOW_SCHEMA_INITIALIZATION")) {
	case "", "false":
	case "true":
		allow = true
	default:
		return fmt.Errorf("PLATEN_ALLOW_SCHEMA_INITIALIZATION must be true or false")
	}
	db, e := gorm.Open(postgres.Open(os.Getenv("DATABASE_URL")), &gorm.Config{DisableAutomaticPing: true})
	if e != nil {
		return fmt.Errorf("open migration database: %w", e)
	}
	s, e := db.DB()
	if e != nil {
		return fmt.Errorf("get migration database handle: %w", e)
	}
	defer s.Close()
	return runManagedSchemaMigrations(db, allow, l)
}
