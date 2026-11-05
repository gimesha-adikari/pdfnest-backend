package config

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"time"

	"gorm.io/gorm"
)

const ArtifactRecoveryVersion = "20261105_artifact_recovery_v1"

// The operator ledger is independently versioned and is never applied during
// startup. These bytes are used only by the explicit migration tool and tests.
//
//go:embed migrations/20261105_artifact_recovery.up.sql
var artifactRecoveryMigrationSQL string

//go:embed migrations/20261105_artifact_recovery.down.sql
var artifactRecoveryRollbackSQL string

// ValidateArtifactRecoverySchema is a read-only, bounded gate for the admin
// recovery endpoints. Missing schema leaves the endpoints unavailable without
// taking ordinary Backend health or readiness down.
func ValidateArtifactRecoverySchema(db *gorm.DB) error {
	if db == nil || db.Statement == nil || db.Dialector == nil || db.Dialector.Name() != "postgres" {
		return fmt.Errorf("artifact recovery requires PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(db.Statement.Context, 5*time.Second)
	defer cancel()
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var markerCount int64
		if err := tx.Raw("SELECT count(*) FROM public.backend_durability_schema WHERE version = ?", ArtifactRecoveryVersion).Scan(&markerCount).Error; err != nil || markerCount != 1 {
			return fmt.Errorf("artifact recovery migration marker missing")
		}
		expected := map[string]struct {
			typ     string
			notNull bool
		}{
			"id": {"uuid", true}, "operation_key": {"character varying(128)", true},
			"request_fingerprint": {"character(64)", true}, "task_id": {"character varying(128)", true},
			"admin_user_id": {"uuid", true}, "action": {"character varying(16)", true},
			"state": {"character varying(48)", true}, "reason": {"character varying(500)", true},
			"owner_identity": {"text", true}, "original_result_key": {"text", true},
			"artifact_sha256": {"character varying(64)", true}, "artifact_bytes": {"bigint", true},
			"original_expires_at": {"timestamp with time zone", true}, "source_key": {"text", false},
			"recovery_key": {"text", false}, "last_error_code": {"character varying(64)", false},
			"attempts": {"bigint", true}, "lease_expires_at": {"timestamp with time zone", false},
			"created_at": {"timestamp with time zone", false}, "updated_at": {"timestamp with time zone", false},
		}
		var columns []struct {
			Name    string
			Type    string
			NotNull bool
		}
		if err := tx.Raw(`SELECT a.attname AS name,
 pg_catalog.format_type(a.atttypid,a.atttypmod) AS type,a.attnotnull AS not_null
 FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid=a.attrelid
 JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname='public' AND c.relname='artifact_recovery_actions' AND c.relkind='r'
 AND a.attnum>0 AND NOT a.attisdropped`).Scan(&columns).Error; err != nil {
			return err
		}
		for _, column := range columns {
			if value, ok := expected[column.Name]; ok {
				if value.typ != column.Type || value.notNull != column.NotNull {
					return fmt.Errorf("artifact recovery schema differs at column %s", column.Name)
				}
				delete(expected, column.Name)
			}
		}
		if len(expected) != 0 {
			return fmt.Errorf("artifact recovery schema is incomplete")
		}
		for _, query := range []string{
			`SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_index WHERE indrelid='public.artifact_recovery_actions'::pg_catalog.regclass AND indisprimary AND indisvalid AND indnkeyatts=1 AND pg_catalog.pg_get_indexdef(indexrelid) LIKE '%(id)')`,
			`SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_index WHERE indrelid='public.artifact_recovery_actions'::pg_catalog.regclass AND indisunique AND indisvalid AND indnkeyatts=1 AND pg_catalog.pg_get_indexdef(indexrelid) LIKE '%(operation_key)')`,
			`SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_index WHERE indrelid='public.artifact_recovery_actions'::pg_catalog.regclass AND indisvalid AND indnkeyatts=1 AND pg_catalog.pg_get_indexdef(indexrelid) LIKE '%(task_id)')`,
		} {
			var valid bool
			if err := tx.Raw(query).Scan(&valid).Error; err != nil || !valid {
				return fmt.Errorf("artifact recovery ledger index missing")
			}
		}
		return nil
	}, &sql.TxOptions{ReadOnly: true})
}
