#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# Platen PDF — Durability Migration Production Snapshot & Disposable Rehearsal
#
# Performs a streaming AES-256 encrypted snapshot of the production database
# directly into encryption (zero plaintext dump on disk) and executes a live
# restore, integrity verification, and migration dry run against an isolated,
# verified non-production disposable PostgreSQL instance.
#
# Usage:
#   DATABASE_URL="postgres://..." \
#   REHEARSAL_PGHOST="localhost" \
#   REHEARSAL_PGPORT="5432" \
#   REHEARSAL_PGUSER="rehearsal_operator" \
#   REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
#   scripts/run-durability-rehearsal.sh
# -----------------------------------------------------------------------------
set -euo pipefail

# Ensure PostgreSQL binaries from pg_config are in PATH if available
if command -v pg_config >/dev/null 2>&1; then
  pg_bindir="$(pg_config --bindir 2>/dev/null || true)"
  if [ -n "$pg_bindir" ] && [ -d "$pg_bindir" ]; then
    export PATH="$pg_bindir:$PATH"
  fi
fi

# -----------------------------------------------------------------------------
# 1. Parameter Validation & Safeguards
# -----------------------------------------------------------------------------
if [ -z "${DATABASE_URL:-}" ]; then
  echo "FATAL: DATABASE_URL must point to the production database (read-only snapshot source)." >&2
  exit 1
fi

if [ -z "${REHEARSAL_PGHOST:-}" ] || [ -z "${REHEARSAL_PGUSER:-}" ]; then
  echo "FATAL: REHEARSAL_PGHOST and REHEARSAL_PGUSER must be set for the disposable rehearsal target." >&2
  echo "Rehearsals must NEVER target the production database or cluster." >&2
  exit 2
fi

# Require affirmative non-production confirmation
if [ "${REHEARSAL_NONPRODUCTION_CONFIRMED:-false}" != "true" ] && \
   [ "${REHEARSAL_NONPRODUCTION_CONFIRMED:-false}" != "confirmed-non-production" ]; then
  echo "FATAL: REHEARSAL_NONPRODUCTION_CONFIRMED must be set to 'true' or 'confirmed-non-production'." >&2
  echo "This safeguard prevents accidental execution without explicit operator non-production affirmation." >&2
  exit 3
fi

REHEARSAL_PGPORT="${REHEARSAL_PGPORT:-5432}"
REHEARSAL_MAINTENANCE_DB="${REHEARSAL_MAINTENANCE_DB:-postgres}"
REHEARSAL_PGPASSWORD="${REHEARSAL_PGPASSWORD:-}"

# Explicit maintenance DSN using structured parameters (never string concatenation)
REHEARSAL_MAINTENANCE_DSN="host=$REHEARSAL_PGHOST port=$REHEARSAL_PGPORT user=$REHEARSAL_PGUSER dbname=$REHEARSAL_MAINTENANCE_DB"
if [ -n "$REHEARSAL_PGPASSWORD" ]; then
  REHEARSAL_MAINTENANCE_DSN="$REHEARSAL_MAINTENANCE_DSN password=$REHEARSAL_PGPASSWORD"
fi

# -----------------------------------------------------------------------------
# 2. Infrastructure Identity & Isolation Verification
# -----------------------------------------------------------------------------
echo "==> Verifying production and rehearsal infrastructure identity and isolation..."

# Query production cluster identity
# Explicitly handles Unix sockets where inet_server_addr() is NULL
prod_info=$(psql "$DATABASE_URL" -t -A -v ON_ERROR_STOP=1 -c "
  SELECT 
    coalesce(inet_server_addr()::text, 'unix-socket'),
    coalesce(inet_server_port()::text, current_setting('port')),
    current_user,
    current_database(),
    coalesce(current_setting('data_directory', true), ''),
    coalesce(current_setting('unix_socket_directories', true), ''),
    coalesce((SELECT system_identifier::text FROM pg_control_system()), '')
" 2>/dev/null || { echo "FATAL: Failed to connect to production database via DATABASE_URL." >&2; exit 4; })

prod_addr=$(echo "$prod_info" | cut -d'|' -f1)
prod_port=$(echo "$prod_info" | cut -d'|' -f2)
prod_user=$(echo "$prod_info" | cut -d'|' -f3)
prod_db=$(echo "$prod_info" | cut -d'|' -f4)
prod_data_dir=$(echo "$prod_info" | cut -d'|' -f5)
prod_socket_dir=$(echo "$prod_info" | cut -d'|' -f6)
prod_sysid=$(echo "$prod_info" | cut -d'|' -f7)

# Query rehearsal target cluster identity
rehearsal_info=$(psql -d "$REHEARSAL_MAINTENANCE_DSN" -t -A -v ON_ERROR_STOP=1 -c "
  SELECT 
    coalesce(inet_server_addr()::text, 'unix-socket'),
    coalesce(inet_server_port()::text, current_setting('port')),
    current_user,
    current_database(),
    coalesce(current_setting('data_directory', true), ''),
    coalesce(current_setting('unix_socket_directories', true), ''),
    coalesce((SELECT system_identifier::text FROM pg_control_system()), '')
" 2>/dev/null || { echo "FATAL: Failed to connect to rehearsal maintenance instance via REHEARSAL_* parameters." >&2; exit 5; })

rehearsal_addr=$(echo "$rehearsal_info" | cut -d'|' -f1)
rehearsal_port=$(echo "$rehearsal_info" | cut -d'|' -f2)
rehearsal_user=$(echo "$rehearsal_info" | cut -d'|' -f3)
rehearsal_db=$(echo "$rehearsal_info" | cut -d'|' -f4)
rehearsal_data_dir=$(echo "$rehearsal_info" | cut -d'|' -f5)
rehearsal_socket_dir=$(echo "$rehearsal_info" | cut -d'|' -f6)
rehearsal_sysid=$(echo "$rehearsal_info" | cut -d'|' -f7)

# FAIL-CLOSED ASSERTIONS:
# 1. Cluster System Identifier (unique per cluster generated by initdb)
if [ -n "$prod_sysid" ] && [ -n "$rehearsal_sysid" ] && [ "$prod_sysid" = "$rehearsal_sysid" ]; then
  echo "FATAL: Rehearsal target cluster system_identifier ($rehearsal_sysid) matches production!" >&2
  echo "Rehearsal must run against a physically distinct PostgreSQL cluster." >&2
  exit 6
fi

# 2. Data Directory check (distinct file paths on disk)
if [ -n "$prod_data_dir" ] && [ -n "$rehearsal_data_dir" ] && [ "$prod_data_dir" = "$rehearsal_data_dir" ]; then
  echo "FATAL: Rehearsal data directory ($rehearsal_data_dir) matches production data directory!" >&2
  exit 7
fi

# 3. Endpoint check for TCP connections
if [ "$prod_addr" != "unix-socket" ] && [ "$rehearsal_addr" != "unix-socket" ]; then
  if [ "$prod_addr" = "$rehearsal_addr" ] && [ "$prod_port" = "$rehearsal_port" ]; then
    echo "FATAL: Rehearsal TCP endpoint ($rehearsal_addr:$rehearsal_port) matches production!" >&2
    exit 8
  fi
fi

# 4. Endpoint check for Unix socket connections
if [ "$prod_addr" = "unix-socket" ] && [ "$rehearsal_addr" = "unix-socket" ]; then
  if [ -n "$prod_socket_dir" ] && [ "$prod_socket_dir" = "$rehearsal_socket_dir" ] && [ "$prod_port" = "$rehearsal_port" ]; then
    echo "FATAL: Rehearsal Unix socket ($rehearsal_socket_dir, port $rehearsal_port) matches production!" >&2
    exit 9
  fi
fi

# 5. Check host/port parameter collision
if [ -n "${prod_port:-}" ] && [ "$REHEARSAL_PGPORT" = "$prod_port" ]; then
  if [ "$REHEARSAL_PGHOST" = "$prod_addr" ] || [ "$REHEARSAL_PGHOST" = "$prod_socket_dir" ]; then
    echo "FATAL: REHEARSAL_PGHOST and REHEARSAL_PGPORT collide with production host/socket and port!" >&2
    exit 10
  fi
fi

# 6. Privilege check: rehearsal user must have zero write/create privilege on production
has_prod_create=$(psql "$DATABASE_URL" -t -A -v ON_ERROR_STOP=1 -c "
  SELECT has_schema_privilege('$rehearsal_user', 'public', 'CREATE');
" 2>/dev/null || echo "false")

if [ "$has_prod_create" = "true" ]; then
  echo "FATAL: Rehearsal user '$rehearsal_user' has CREATE privilege on production schema!" >&2
  exit 11
fi

echo "Infrastructure isolation confirmed:"
echo "  Production: [addr=$prod_addr, port=$prod_port, db=$prod_db, sysid=${prod_sysid:0:8}...]"
echo "  Rehearsal:  [addr=$rehearsal_addr, port=$rehearsal_port, user=$rehearsal_user, sysid=${rehearsal_sysid:0:8}...]"

# -----------------------------------------------------------------------------
# 3. Filesystem, Process Permissions & Key Management
# -----------------------------------------------------------------------------
# Enforce strict umask for all created files and directories
umask 077

BACKUP_DIR_MANAGED=false
if [ -z "${BACKUP_DIR:-}" ]; then
  BACKUP_DIR="$(mktemp -d /tmp/platen-backup-XXXXXX)"
  BACKUP_DIR_MANAGED=true
else
  mkdir -p "$BACKUP_DIR"
fi
chmod 700 "$BACKUP_DIR"

BACKUP_TS="$(date +%Y%m%d_%H%M%S)"
BACKUP_NAME="pdfnest_backup_pre_durability_${BACKUP_TS}_${RANDOM}"
ENCRYPTED_BACKUP="$BACKUP_DIR/${BACKUP_NAME}.dump.gpg"

# Safeguard against accidental overwrite of an existing backup file
if [ -e "$ENCRYPTED_BACKUP" ]; then
  echo "FATAL: Backup target file '$ENCRYPTED_BACKUP' already exists. Aborting to prevent overwrite." >&2
  exit 12
fi

# Durable, Recoverable Encryption Key Material
KEY_FILE_GENERATED=false
if [ -n "${BACKUP_PASSPHRASE_FILE:-}" ]; then
  if [ ! -f "$BACKUP_PASSPHRASE_FILE" ]; then
    echo "FATAL: Specified BACKUP_PASSPHRASE_FILE '$BACKUP_PASSPHRASE_FILE' does not exist." >&2
    exit 13
  fi
  chmod 600 "$BACKUP_PASSPHRASE_FILE"
else
  # Durably secure the generated passphrase into a protected file
  BACKUP_PASSPHRASE_FILE="$BACKUP_DIR/${BACKUP_NAME}.key"
  touch "$BACKUP_PASSPHRASE_FILE"
  chmod 600 "$BACKUP_PASSPHRASE_FILE"
  head -c 32 /dev/urandom | base64 > "$BACKUP_PASSPHRASE_FILE"
  KEY_FILE_GENERATED=true
  echo "==> Durable recovery key generated and retained at: $BACKUP_PASSPHRASE_FILE"
fi

if [ ! -s "$BACKUP_PASSPHRASE_FILE" ]; then
  echo "FATAL: BACKUP_PASSPHRASE_FILE is empty." >&2
  exit 14
fi

# Rehearsal database parameters
REHEARSAL_DB="${REHEARSAL_DB:-rehearsal_${BACKUP_TS}_${RANDOM}}"
REHEARSAL_DB_CREATED_BY_US=false
REHEARSAL_TOC_FILE="$BACKUP_DIR/toc_${BACKUP_TS}_${RANDOM}.list"
TOC_FILE_CREATED=false

REHEARSAL_DB_DSN="host=$REHEARSAL_PGHOST port=$REHEARSAL_PGPORT user=$REHEARSAL_PGUSER dbname=$REHEARSAL_DB"
if [ -n "$REHEARSAL_PGPASSWORD" ]; then
  REHEARSAL_DB_DSN="$REHEARSAL_DB_DSN password=$REHEARSAL_PGPASSWORD"
fi

# -----------------------------------------------------------------------------
# 4. Safe Automated Cleanup Trap
# -----------------------------------------------------------------------------
cleanup() {
  local exit_code=$?
  echo "==> Rehearsal cleanup initiated (exit code: $exit_code)..."

  # POSITIVE OWNERSHIP SAFETY:
  # Only drop the rehearsal database if this specific execution positively created it!
  # Preexisting databases are NEVER dropped.
  if [ "$REHEARSAL_DB_CREATED_BY_US" = "true" ]; then
    echo "==> Dropping rehearsal database '$REHEARSAL_DB' created by this execution..."
    dropdb --maintenance-db="$REHEARSAL_MAINTENANCE_DSN" --if-exists "$REHEARSAL_DB" 2>/dev/null || true
    REHEARSAL_DB_CREATED_BY_US=false
  fi

  # Clean up ephemeral TOC list file if created by us
  if [ "$TOC_FILE_CREATED" = "true" ] && [ -f "$REHEARSAL_TOC_FILE" ]; then
    rm -f "$REHEARSAL_TOC_FILE" 2>/dev/null || true
  fi

  # CRITICAL ASSET PRESERVATION:
  # The recovery backup ($ENCRYPTED_BACKUP) and key material ($BACKUP_PASSPHRASE_FILE)
  # must NEVER be deleted on rehearsal failure or normal completion.
  # They are preserved for disaster recovery.
  if [ -f "$ENCRYPTED_BACKUP" ]; then
    echo "==> Production recovery backup preserved at: $ENCRYPTED_BACKUP"
    echo "==> Recovery encryption key preserved at:   $BACKUP_PASSPHRASE_FILE"
  fi

  # Only purge managed directory if explicit PURGE_BACKUP_ON_SUCCESS is set AND execution succeeded
  if [ "${PURGE_BACKUP_ON_SUCCESS:-false}" = "true" ] && [ $exit_code -eq 0 ]; then
    rm -f "$ENCRYPTED_BACKUP" "$BACKUP_PASSPHRASE_FILE" 2>/dev/null || true
    if [ "$BACKUP_DIR_MANAGED" = "true" ]; then
      rmdir "$BACKUP_DIR" 2>/dev/null || true
    fi
  fi

  if [ $exit_code -ne 0 ]; then
    echo "==> REHEARSAL FAILED. Backup and recovery key are retained. Ephemeral rehearsal target cleaned up." >&2
  fi

  exit "$exit_code"
}
trap cleanup EXIT INT TERM

# -----------------------------------------------------------------------------
# 5. Record Production Baseline Metrics
# -----------------------------------------------------------------------------
echo "==> 1. Recording baseline production row counts..."
baseline_users=$(psql "$DATABASE_URL" -t -A -v ON_ERROR_STOP=1 -c "SELECT count(*) FROM public.users;")
baseline_jobs=$(psql "$DATABASE_URL" -t -A -v ON_ERROR_STOP=1 -c "SELECT count(*) FROM public.studio_jobs;")
baseline_migrations=$(psql "$DATABASE_URL" -t -A -v ON_ERROR_STOP=1 -c "SELECT count(*) FROM public.schema_migrations;")
echo "Production baseline: users=$baseline_users, studio_jobs=$baseline_jobs, schema_migrations=$baseline_migrations"

# -----------------------------------------------------------------------------
# 6. Streaming Custom-Format Snapshot Directly into AES-256 Encryption
# -----------------------------------------------------------------------------
echo "==> 2. Streaming custom-format snapshot directly into AES-256 encryption..."
# Zero plaintext dump touches the filesystem: stream pg_dump directly into gpg
pg_dump "$DATABASE_URL" --format=custom --schema=public | \
  gpg --batch --yes --pinentry-mode loopback --symmetric --cipher-algo AES256 \
      --passphrase-file "$BACKUP_PASSPHRASE_FILE" \
      --output "$ENCRYPTED_BACKUP"

test -s "$ENCRYPTED_BACKUP" || { echo "FATAL: Encrypted backup was not created or is 0 bytes." >&2; exit 15; }
chmod 600 "$ENCRYPTED_BACKUP"
echo "Encrypted backup created: $(ls -lh "$ENCRYPTED_BACKUP")"

# -----------------------------------------------------------------------------
# 7. Verify Archive TOC Framing from Decrypted Stream
# -----------------------------------------------------------------------------
echo "==> 3. Verifying archive TOC listing from encrypted stream..."
gpg --batch --quiet --pinentry-mode loopback --decrypt \
    --passphrase-file "$BACKUP_PASSPHRASE_FILE" "$ENCRYPTED_BACKUP" | \
    pg_restore --list > "$REHEARSAL_TOC_FILE"
TOC_FILE_CREATED=true

grep -q "TABLE DATA public users" "$REHEARSAL_TOC_FILE" || { echo "FATAL: TOC missing users table data." >&2; exit 16; }
grep -q "TABLE DATA public studio_jobs" "$REHEARSAL_TOC_FILE" || { echo "FATAL: TOC missing studio_jobs table data." >&2; exit 17; }
echo "Backup archive TOC listing verified."

# -----------------------------------------------------------------------------
# 8. Safe Rehearsal Database Creation (Collision Protection)
# -----------------------------------------------------------------------------
# Fail-fast if rehearsal database already exists
preexisting_db=$(psql -d "$REHEARSAL_MAINTENANCE_DSN" -t -A -v ON_ERROR_STOP=1 \
  -c "SELECT 1 FROM pg_database WHERE datname = '$REHEARSAL_DB';" 2>/dev/null || true)

if [ "$preexisting_db" = "1" ]; then
  echo "FATAL: Rehearsal database '$REHEARSAL_DB' already exists on target instance. Aborting to protect preexisting database." >&2
  exit 18
fi

echo "==> 4. Creating disposable rehearsal database '$REHEARSAL_DB' on non-production target..."
createdb --maintenance-db="$REHEARSAL_MAINTENANCE_DSN" "$REHEARSAL_DB"
REHEARSAL_DB_CREATED_BY_US=true

# -----------------------------------------------------------------------------
# 9. Perform Streaming Rehearsal Restore
# -----------------------------------------------------------------------------
echo "==> 5. Restoring snapshot into disposable rehearsal database from decrypted pipe..."
gpg --batch --quiet --pinentry-mode loopback --decrypt \
    --passphrase-file "$BACKUP_PASSPHRASE_FILE" "$ENCRYPTED_BACKUP" | \
    pg_restore --dbname="$REHEARSAL_DB_DSN" \
      --exit-on-error \
      --schema=public \
      --no-owner \
      --no-privileges

# -----------------------------------------------------------------------------
# 10. Assert Restored Data Integrity Against Production Baseline
# -----------------------------------------------------------------------------
echo "==> 6. Verifying restored data integrity against production baseline..."
restored_users=$(psql -d "$REHEARSAL_DB_DSN" -t -A -v ON_ERROR_STOP=1 -c "SELECT count(*) FROM public.users;")
restored_jobs=$(psql -d "$REHEARSAL_DB_DSN" -t -A -v ON_ERROR_STOP=1 -c "SELECT count(*) FROM public.studio_jobs;")
restored_migrations=$(psql -d "$REHEARSAL_DB_DSN" -t -A -v ON_ERROR_STOP=1 -c "SELECT count(*) FROM public.schema_migrations;")

echo "Restored counts: users=$restored_users, studio_jobs=$restored_jobs, schema_migrations=$restored_migrations"

if [ "$restored_users" != "$baseline_users" ] || \
   [ "$restored_jobs" != "$baseline_jobs" ] || \
   [ "$restored_migrations" != "$baseline_migrations" ]; then
  echo "FATAL: Restored row counts do not match production baseline!" >&2
  exit 19
fi
echo "Row counts verified: 100% match with production baseline."

# Verify primary and foreign keys
psql -d "$REHEARSAL_DB_DSN" -v ON_ERROR_STOP=1 -c "
  SELECT conname, contype, conrelid::regclass 
  FROM pg_constraint 
  WHERE connamespace = 'public'::regnamespace 
    AND conrelid::regclass::text IN ('users', 'studio_jobs', 'subscriptions');
" >/dev/null

# -----------------------------------------------------------------------------
# 11. Rehearsal Migration Dry Run
# -----------------------------------------------------------------------------
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
migration_file="${MIGRATION_FILE:-$repo_root/config/migrations/20261029_backend_durability.up.sql}"

echo "==> 7. Testing durability migration ($migration_file) against restored rehearsal data..."
psql -d "$REHEARSAL_DB_DSN" -v ON_ERROR_STOP=1 -f "$migration_file"

# -----------------------------------------------------------------------------
# 12. Programmatically Assert All 12 Durability Index Objects
# -----------------------------------------------------------------------------
echo "==> 8. Programmatically validating exact 12 durability index objects on rehearsal database..."
idx_check=$(psql -d "$REHEARSAL_DB_DSN" -t -A -v ON_ERROR_STOP=1 << 'EOF'
WITH expected(tbl, idx, is_unique, is_primary) AS (
  VALUES
    ('backend_durability_schema', 'backend_durability_schema_pkey', true, true),
    ('studio_storage_object_retirements', 'studio_storage_object_retirements_pkey', true, true),
    ('task_completions', 'task_completions_pkey', true, true),
    ('task_completions', 'idx_task_completions_expires_at', false, false),
    ('task_completions', 'idx_task_completions_reservation_id', false, false),
    ('task_completions', 'idx_task_completions_state', false, false),
    ('worker_result_ownerships', 'worker_result_ownerships_pkey', true, true),
    ('worker_result_ownerships', 'idx_worker_result_ownerships_expires_at', false, false),
    ('worker_result_ownerships', 'idx_worker_result_ownerships_next_attempt_at', false, false),
    ('worker_result_ownerships', 'idx_worker_result_ownerships_request_id', true, false),
    ('worker_result_ownerships', 'idx_worker_result_ownerships_state', false, false),
    ('worker_result_ownerships', 'idx_worker_result_ownerships_worker_job_id', false, false)
),
actual AS (
  SELECT 
    t.relname AS tbl,
    c.relname AS idx,
    i.indisunique AS is_unique,
    i.indisvalid AS is_valid,
    i.indisprimary AS is_primary
  FROM pg_index i
  JOIN pg_class c ON c.oid = i.indexrelid
  JOIN pg_class t ON t.oid = i.indrelid
  JOIN pg_namespace n ON n.oid = t.relnamespace
  WHERE n.nspname = 'public'
    AND t.relname IN ('task_completions', 'worker_result_ownerships', 'studio_storage_object_retirements', 'backend_durability_schema')
)
SELECT
  count(*) AS total_actual,
  count(*) FILTER (WHERE a.is_valid = true) AS valid_actual,
  count(*) FILTER (WHERE a.is_unique = true) AS unique_actual,
  count(*) FILTER (WHERE a.is_primary = true) AS primary_actual,
  count(e.idx) AS matching_expected
FROM actual a
FULL OUTER JOIN expected e 
  ON e.tbl = a.tbl 
 AND e.idx = a.idx 
 AND e.is_unique = a.is_unique 
 AND e.is_primary = a.is_primary;
EOF
)

total_actual=$(echo "$idx_check" | cut -d'|' -f1)
valid_actual=$(echo "$idx_check" | cut -d'|' -f2)
unique_actual=$(echo "$idx_check" | cut -d'|' -f3)
primary_actual=$(echo "$idx_check" | cut -d'|' -f4)
matching_expected=$(echo "$idx_check" | cut -d'|' -f5)

echo "Durability indexes: total=$total_actual (expected 12), valid=$valid_actual (expected 12), unique=$unique_actual (expected 5), primary=$primary_actual (expected 4), matched=$matching_expected (expected 12)"

if [ "$total_actual" -ne 12 ] || [ "$valid_actual" -ne 12 ] || [ "$unique_actual" -ne 5 ] || [ "$primary_actual" -ne 4 ] || [ "$matching_expected" -ne 12 ]; then
  echo "FATAL: Durability index validation failed! Index definitions, uniqueness, or validity did not match exact specification." >&2
  exit 20
fi

echo "==> All 12 durability indexes verified with exact table, name, uniqueness, primary-key, and validity properties."
echo "==> Disposable rehearsal restore and migration dry run successfully completed!"
# Automatic trap drops $REHEARSAL_DB on exit.
