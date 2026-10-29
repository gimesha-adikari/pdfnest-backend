package config

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"time"

	"gorm.io/gorm"
)

const BackendDurabilityVersion = "20261029_backend_durability_v1"

// Draft migration bytes are embedded for disposable-fixture qualification.
// Neither application startup nor the existing migrate command executes them.
//
//go:embed migrations/20261029_backend_durability.up.sql
var backendDurabilityDraftSQL string

//go:embed migrations/20261029_backend_durability.down.sql
var backendDurabilityRollbackSQL string

// ValidateBackendDurabilitySchema is a read-only deployment gate. Historical
// schema checks remain independent: extra nullable columns/new tables do not
// authorize a production migration or a ledger adoption.
func ValidateBackendDurabilitySchema(db *gorm.DB) error {
	if db == nil || db.Statement == nil || db.Dialector == nil || db.Dialector.Name() != "postgres" {
		return fmt.Errorf("durability requires PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(db.Statement.Context, 5*time.Second)
	defer cancel()
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Raw("SELECT count(*) FROM public.backend_durability_schema WHERE version = ?", BackendDurabilityVersion).Scan(&count).Error; err != nil || count != 1 {
			return fmt.Errorf("approved durability migration marker missing")
		}
		required := map[string]map[string]string{
			"task_completions": {
				"id": "character varying(128)", "owner_identity": "text", "download_token": "text", "reservation_id": "text", "reservation_kind": "text", "state": "text", "result_key": "text", "result_url": "text", "error": "text", "artifact_sha256": "text", "artifact_bytes": "bigint", "expires_at": "timestamp with time zone", "created_at": "timestamp with time zone", "updated_at": "timestamp with time zone",
			},
			"worker_result_ownerships": {
				"id": "character varying(36)", "request_id": "text", "request_hash": "text", "worker_job_id": "text", "owner_identity": "text", "submit_path": "text", "profile": "text", "source_keys": "text", "state": "text", "status_json": "text", "result_key": "text", "metadata_key": "text", "expires_at": "timestamp with time zone", "next_attempt_at": "timestamp with time zone", "attempts": "bigint", "last_error": "text", "created_at": "timestamp with time zone", "updated_at": "timestamp with time zone",
			},
			"studio_jobs":                       {"pending_object_keys": "jsonb"},
			"studio_storage_object_retirements": {"object_key": "character varying(512)", "created_at": "timestamp with time zone"},
		}
		var columns []struct {
			Table, Name, Type string
			NotNull           bool
		}
		if err := tx.Raw(`SELECT c.relname AS "table", a.attname AS name, pg_catalog.format_type(a.atttypid,a.atttypmod) AS type, a.attnotnull AS not_null
 FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid=a.attrelid
 JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname='public' AND c.relkind='r' AND a.attnum>0 AND NOT a.attisdropped`).Scan(&columns).Error; err != nil {
			return err
		}
		notNull := map[string]bool{
			"task_completions.id": true, "task_completions.owner_identity": true, "task_completions.download_token": true, "task_completions.state": true,
			"worker_result_ownerships.id": true, "worker_result_ownerships.request_id": true, "worker_result_ownerships.request_hash": true, "worker_result_ownerships.submit_path": true, "worker_result_ownerships.source_keys": true, "worker_result_ownerships.state": true, "worker_result_ownerships.attempts": true,
			"studio_storage_object_retirements.object_key": true,
		}
		for _, column := range columns {
			if notNull[column.Table+"."+column.Name] && !column.NotNull {
				return fmt.Errorf("durability nullable authority column: %s.%s", column.Table, column.Name)
			}
			if fields := required[column.Table]; fields != nil {
				if expected, ok := fields[column.Name]; ok {
					if expected != column.Type {
						return fmt.Errorf("durability schema differs: %s.%s", column.Table, column.Name)
					}
					delete(fields, column.Name)
				}
			}
		}
		for table, fields := range required {
			if len(fields) != 0 {
				return fmt.Errorf("durability columns missing in %s", table)
			}
		}
		// Idempotency and ownership require these constraints, not just columns.
		for _, query := range []string{
			`SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_index WHERE indrelid='public.studio_storage_object_retirements'::regclass AND indisprimary AND indisvalid AND indnkeyatts=1 AND indexprs IS NULL AND pg_catalog.pg_get_indexdef(indexrelid) LIKE '%(object_key)')`,
			`SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_index WHERE indrelid='public.task_completions'::regclass AND indisprimary AND indisvalid AND indnkeyatts=1 AND indexprs IS NULL AND pg_catalog.pg_get_indexdef(indexrelid) LIKE '%(id)')`,
			`SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_index WHERE indrelid='public.worker_result_ownerships'::regclass AND indisprimary AND indisvalid AND indnkeyatts=1 AND indexprs IS NULL AND pg_catalog.pg_get_indexdef(indexrelid) LIKE '%(id)')`,
			`SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_index WHERE indrelid='public.worker_result_ownerships'::regclass AND indisunique AND indisvalid AND indnkeyatts=1 AND indexprs IS NULL AND indpred IS NULL AND pg_catalog.pg_get_indexdef(indexrelid) LIKE '%(request_id)')`,
		} {
			var valid bool
			if err := tx.Raw(query).Scan(&valid).Error; err != nil || !valid {
				return fmt.Errorf("durability ownership constraint missing")
			}
		}
		return nil
	}, &sql.TxOptions{ReadOnly: true})
}
