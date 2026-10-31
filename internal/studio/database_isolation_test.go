package studio

import (
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Each fixture gets a private PostgreSQL schema. Background reconciliation is
// intentionally global in production; unrelated test jobs must not become its
// inputs, and immutable key tombstones must never be recycled between tests.
func openStudioTestDatabase(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	bootstrap, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err, "Failed to connect to isolated PostgreSQL fixture")
	pool, err := bootstrap.DB()
	require.NoError(t, err)
	schema := "studio_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, bootstrap.Exec("CREATE SCHEMA "+schema).Error)
	require.NoError(t, pool.Close())
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := url.Parse(dsn)
		require.NoError(t, err)
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		dsn = parsed.String()
	} else {
		dsn += " search_path=" + schema
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Exec("DROP SCHEMA "+schema+" CASCADE").Error)
		require.NoError(t, sqlDB.Close())
	})
	return db
}
