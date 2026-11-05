package config

import (
	"context"
	_ "embed"
	"fmt"
	"gorm.io/gorm"
	"time"
)

const AccountCleanupVersion = "20261105_account_cleanup_v1"

// AccountStorageCleanup survives soft deletion without a user FK. UserID is a
// server-authenticated UUID, never a caller-selected storage prefix.
type AccountStorageCleanup struct {
	UserID         string    `gorm:"type:uuid;primaryKey"`
	NextAttemptAt  time.Time `gorm:"not null;index"`
	LeaseExpiresAt *time.Time
	LeaseToken     string `gorm:"type:uuid;default:null"`
	Attempts       int    `gorm:"not null;default:0"`
	LastError      string `gorm:"type:varchar(128)"`
	ScanPurpose    int    `gorm:"not null;default:0"`
	ScanCursor     string `gorm:"type:text;not null;default:''"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

//go:embed migrations/20261105_account_cleanup.up.sql
var accountCleanupMigrationSQL string

//go:embed migrations/20261105_account_cleanup.down.sql
var accountCleanupRollbackSQL string

// ValidateAccountCleanupSchema is read-only. Missing or incompatible schema
// disables account deletion; startup must never silently create this ledger.
func ValidateAccountCleanupSchema(db *gorm.DB) error {
	if db == nil || db.Dialector.Name() != "postgres" {
		return fmt.Errorf("account cleanup requires PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var valid bool
	err := db.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM public.backend_durability_schema WHERE version=?) AND EXISTS(SELECT 1 FROM pg_catalog.pg_index WHERE indrelid='public.account_storage_cleanups'::regclass AND indisprimary AND indisvalid AND indnkeyatts=1 AND pg_catalog.pg_get_indexdef(indexrelid) LIKE '%(user_id)')`, AccountCleanupVersion).Scan(&valid).Error
	if err != nil || !valid {
		return fmt.Errorf("account cleanup schema unavailable")
	}
	expected := map[string]struct {
		typ      string
		required bool
	}{
		"user_id": {"uuid", true}, "next_attempt_at": {"timestamp with time zone", true},
		"lease_expires_at": {"timestamp with time zone", false}, "lease_token": {"uuid", false},
		"attempts": {"bigint", true}, "last_error": {"character varying(128)", false},
		"scan_purpose": {"bigint", true}, "scan_cursor": {"text", true},
		"created_at": {"timestamp with time zone", false}, "updated_at": {"timestamp with time zone", false},
	}
	var columns []struct {
		Name, Type string
		NotNull    bool
	}
	if err := db.WithContext(ctx).Raw(`SELECT a.attname AS name,pg_catalog.format_type(a.atttypid,a.atttypmod) AS type,a.attnotnull AS not_null
      FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid=a.attrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
      WHERE n.nspname='public' AND c.relname='account_storage_cleanups' AND c.relkind='r' AND a.attnum>0 AND NOT a.attisdropped`).Scan(&columns).Error; err != nil {
		return err
	}
	for _, column := range columns {
		if value, ok := expected[column.Name]; ok {
			if value.typ != column.Type || value.required != column.NotNull {
				return fmt.Errorf("account cleanup schema differs at %s", column.Name)
			}
			delete(expected, column.Name)
		}
	}
	if len(expected) != 0 {
		return fmt.Errorf("account cleanup schema incomplete")
	}
	var confirmation bool
	if err := db.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_attribute WHERE attrelid='public.studio_storage_object_retirements'::regclass AND attname='last_delete_confirmed_at' AND atttypid='timestamptz'::regtype AND NOT attisdropped)`).Scan(&confirmation).Error; err != nil || !confirmation {
		return fmt.Errorf("account cleanup deletion confirmation schema missing")
	}
	return nil
}
