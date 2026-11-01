# Platen PDF — Backend Durability Schema Migration Release Runbook

> **Target Environment:** Railway Production PostgreSQL (`pdfnest`)  
> **Schema Migration:** `config/migrations/20261029_backend_durability.up.sql`  
> **Rollback Script:** `config/migrations/20261029_backend_durability.down.sql`  
> **Authoritative Schema Gate:** `config/durability_schema.go` (`ValidateBackendDurabilitySchema`)  
> **Target Schema Marker:** `public.backend_durability_schema` (`version = '20261029_backend_durability_v1'`)  
> **Operational Status:** DRAFT PREFLIGHT — Non-production verified. To be executed only under explicit owner authorization.

---

## 1. Architecture & Background

### Context & Separation of Concerns
1. **Separation from Application Startup:** The Go Backend's GORM `AutoMigrate()` and the historical ledger table `public.schema_migrations` (currently at `20260918_01_admin_subscription_mutation`) intentionally **do not** apply the backend durability migration.
2. **Startup Gate Requirement:** The new Backend binary (PR #47 / `master`) calls `ValidateBackendDurabilitySchema(ctx, db)` at application initialization. If the durability marker or any required column/index is absent or malformed, the binary **fails closed** and terminates immediately to prevent inconsistent or uncommitted task states.
3. **Dual Compatibility:** The schema changes in `20261029_backend_durability.up.sql` are strictly additive and nullable:
   - Existing tables (`users`, `orders`, `transactions`, `email_verifications`, `password_resets`, `api_usage_records`) are completely untouched.
   - `public.studio_jobs` gains an optional nullable `pending_object_keys jsonb` column.
   - Three new standalone tables are created: `task_completions`, `worker_result_ownerships`, and `studio_storage_object_retirements`.
   - The restored production Backend binary (`40dfe66886e7c7c5bc3f522e4ca5fcd55e669a02`) runs completely unhindered against the upgraded database. Queries on `studio_jobs` and existing tables continue without error because all new columns are nullable and historical queries do not select the new durability tables.
4. **Deployment Order Requirement:** Worker PR #13 must be deployed **before** Backend PR #47. Worker PR #13 defaults `dedup_scope="global"` for legacy callers (maintaining full backward-compatibility with the currently running production Backend), while Backend PR #47 actively transmits `dedup_scope=<hmac>` which an older Worker would ignore or fail.

---

## 2. Pre-Requisites & Guardrails

> [!IMPORTANT]
> **Production Restrictions & Safeguards:**
> - Never execute migration commands with unbounded statement timeouts. `up.sql` explicitly sets `lock_timeout = '5s'` and `statement_timeout = '30s'`.
> - The migration acquires a session-level transactional advisory lock: `pg_advisory_xact_lock(hashtext('pdfnest:backend-durability-v1'))` to prevent concurrent migration attempts.
> - **Fail-Closed Unversioned Table Guard:** If any of `task_completions`, `worker_result_ownerships`, `studio_storage_object_retirements`, or `studio_jobs.pending_object_keys` already exist without `backend_durability_schema`, the migration raises an exception and rolls back immediately.

---

## 3. Phase 1: Read-Only Preflight Verification

Before initiating any database modification, run the following read-only SQL queries to verify pre-conditions:

### Step 1.1: Verify Connection & User Privileges
```sql
-- Verify current connected database, user, and server version
SELECT current_database(), current_user, version();

-- Confirm current user has CREATE and USAGE on schema public
SELECT has_schema_privilege(current_user, 'public', 'CREATE') AS can_create,
       has_schema_privilege(current_user, 'public', 'USAGE') AS can_usage;
```
*Expected: `can_create = true`, `can_usage = true`.*

### Step 1.2: Check for Lock Contention or Long-Running Transactions
```sql
-- Check for active queries or blocking locks on studio_jobs
SELECT pid, state, age(clock_timestamp(), query_start) AS duration, query 
FROM pg_stat_activity 
WHERE query NOT LIKE '%pg_stat_activity%' 
  AND state != 'idle'
ORDER BY query_start ASC;
```
*Expected: No blocking locks or transactions older than 10 seconds.*

### Step 1.3: Confirm Absence of Unversioned Conflicting Objects
```sql
-- Verify whether the durability tables or columns currently exist
SELECT 
    to_regclass('public.backend_durability_schema') AS marker_table,
    to_regclass('public.task_completions') AS task_completions,
    to_regclass('public.worker_result_ownerships') AS worker_result_ownerships,
    to_regclass('public.studio_storage_object_retirements') AS studio_storage_object_retirements,
    EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_schema = 'public' 
          AND table_name = 'studio_jobs' 
          AND column_name = 'pending_object_keys'
    ) AS pending_object_keys_col;
```
*Expected before initial migration: All columns return NULL / false (or if already applied, marker_table is non-null).*

### Step 1.4: Confirm Historical Migration Ledger Integrity
```sql
-- Verify the last applied historical migration
SELECT version, applied_at FROM public.schema_migrations ORDER BY version DESC LIMIT 3;
```
*Expected: Latest version is `20260918_01_admin_subscription_mutation` with a valid timestamp.*

---

## 4. Phase 2: Production Snapshot, Access-Controlled Backup & Disposable Restore Rehearsal Prerequisite

> [!CAUTION]
> **CRITICAL REHEARSAL ISOLATION REQUIREMENT — NEVER TARGET PRODUCTION:**
> Rehearsal restore commands (`createdb`, `pg_restore`, `dropdb`, verification queries, dry-run DDL) must **NEVER** target the production database server or cluster.
> Running rehearsal or test DDL against production risks table locking, naming collisions, or catastrophic data loss.
> Rehearsal **strictly requires** a separately provisioned, disposable non-production PostgreSQL instance (e.g. dedicated local container or isolated private Unix-socket fixture) with dedicated isolated credentials (`REHEARSAL_PGHOST`, `REHEARSAL_PGPORT`, `REHEARSAL_PGUSER`, etc.).
> Fail-closed infrastructure checks must verify that rehearsal hosts and cluster identities do not match production before any creation, restore, dry-run, or deletion is attempted.

### Step 2.1: Canonical Executable Rehearsal Implementation

The rehearsal procedure is maintained as an executable, automated script at [`scripts/run-durability-rehearsal.sh`](file:///home/gimesha/My_Projects/platen/pdfnest-backend/scripts/run-durability-rehearsal.sh).
Both manual pre-migration rehearsals and automated CI qualification invoke this exact implementation.

To execute the rehearsal:
```bash
DATABASE_URL="$DATABASE_URL" \
REHEARSAL_PGHOST="<disposable-rehearsal-host-or-socket>" \
REHEARSAL_PGPORT="5432" \
REHEARSAL_PGUSER="<rehearsal-operator-user>" \
REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
./scripts/run-durability-rehearsal.sh
```

Below is the verified, authoritative implementation of `scripts/run-durability-rehearsal.sh`:

```bash
#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# Platen PDF — Durability Migration Production Snapshot & Disposable Rehearsal
#
# Performs a streaming AES-256 encrypted snapshot of the production database
# directly into encryption (zero plaintext dump on disk) and executes a live
# restore, integrity verification, and migration dry run against an isolated,
# verified non-production disposable PostgreSQL instance.
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

# Query production cluster identity (explicitly handling Unix sockets where inet_server_addr is NULL)
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
  if [ "$REHEARSAL_DB_CREATED_BY_US" = "true" ]; then
    echo "==> Dropping rehearsal database '$REHEARSAL_DB' created by this execution..."
    dropdb --maintenance-db="$REHEARSAL_MAINTENANCE_DSN" --if-exists "$REHEARSAL_DB" 2>/dev/null || true
    REHEARSAL_DB_CREATED_BY_US=false
  fi

  if [ "$TOC_FILE_CREATED" = "true" ] && [ -f "$REHEARSAL_TOC_FILE" ]; then
    rm -f "$REHEARSAL_TOC_FILE" 2>/dev/null || true
  fi

  # CRITICAL ASSET PRESERVATION:
  # The recovery backup and key material must NEVER be deleted on rehearsal failure or normal completion.
  if [ -f "$ENCRYPTED_BACKUP" ]; then
    echo "==> Production recovery backup preserved at: $ENCRYPTED_BACKUP"
    echo "==> Recovery encryption key preserved at:   $BACKUP_PASSPHRASE_FILE"
  fi

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
```

### Step 2.2: Backup Access Control, Encryption, and Disposal Standards
1. **Access Control & File Mode:** The backup archive contains sensitive production records (user identities, studio job parameters, file keys). Process permissions enforce `umask 077` and `chmod 600`. File ownership belongs strictly to the migration operator role with zero permissions granted to other users or group members.
2. **Durable Key Material & Zero Command-Line Secrets:** Never pass encryption keys as command-line arguments (which are visible in process tables and audit logs). The procedure accepts `BACKUP_PASSPHRASE_FILE` with permissions `0600`. If unset, it automatically generates a high-entropy key into a dedicated `.key` file with `0600` permissions and durably retains it alongside the backup. Decryption is verified in a separate, isolated process using only this retained key material.
3. **Elimination of Plaintext Dumps:** Plaintext database dumps must never touch physical or virtual disk storage. The snapshot is piped directly from `pg_dump -Fc` into `gpg --batch --pinentry-mode loopback --symmetric --cipher-algo AES256 --passphrase-file ...`. Rehearsal restore streams decrypted bytes directly from `gpg --decrypt` pipe into `pg_restore --exit-on-error`.
4. **Preservation of Recovery Assets:** A rehearsal failure must **never** delete the encrypted recovery backup or its key file. The backup file is the ultimate rollback asset; only ephemeral rehearsal database objects are dropped on exit.
5. **Modern Flash/Cloud Cryptographic Erasure (Crypto-Shredding):**
   - *Physical Limitations of Overwrite on SSDs/Cloud Volumes:* Modern solid-state drives (SSDs, NVMe) and virtualized cloud block devices (e.g. AWS EBS, GCP Persistent Disk, Railway volume mounts) utilize flash translation layers (FTL), wear-leveling algorithms, over-provisioning pools, and copy-on-write sparse remapping. Standard file overwriting tools (`shred`, `rm -P`) overwrite only the currently mapped logical blocks; residual physical flash cells or underlying storage snapshots retain historical data blocks until low-level garbage collection occurs.
   - *Crypto-Shredding as the Authoritative Standard:* Immediate symmetric encryption with AES-256 provides absolute confidentiality. When the snapshot reaches the end of its agreed retention window (typically 24–48 hours post-migration), **cryptographic erasure** is performed by destroying the encryption key/passphrase file (`BACKUP_PASSPHRASE_FILE`) and shredding key files. Once the key is destroyed, ciphertext remaining on flash blocks or block-volume snapshots is rendered mathematically impossible to decrypt.
   - *Disposal Execution:* Once Phase 4 post-condition verification is complete and the release window concludes:
     ```bash
     shred -u "$BACKUP_PASSPHRASE_FILE"
     shred -u "$ENCRYPTED_BACKUP"
     ```

### Step 2.3: Pre-Migration Sign-Off & Approval Checklist

Before proceeding to Phase 3 (Production Execution), confirm and check off every requirement:

| Prerequisite | Verification Method | Status | Sign-Off |
| :--- | :--- | :--- | :--- |
| **Phase 1 Preflight Checks** | Read-only SQL queries in Step 1.1–1.4 executed; no locks > 10s, no unversioned tables, schema ledger verified | Required | [ ] |
| **Infrastructure Isolation Confirmed** | `system_identifier`, `data_directory`, and `current_user` verified; rehearsal cluster distinct from production | Required | [ ] |
| **Streaming Encrypted Snapshot** | `pg_dump -Fc` streamed directly into AES-256 encryption (`.dump.gpg`); 0 bytes plaintext on disk | Required | [ ] |
| **Durable Key Material Secured** | Recovery passphrase durably stored in `.key` file with `0600` permissions; no CLI argument exposure | Required | [ ] |
| **TOC Integrity Confirmed** | `pg_restore --list` from decrypted stream confirms required table structures | Required | [ ] |
| **Collision-Safe DB Creation** | `createdb` executed only after verifying database does not preexist; preexisting DBs never dropped | Required | [ ] |
| **Streaming Rehearsal Restore Passed** | Decrypted stream restored to disposable DB with `--exit-on-error`; 0 errors | Required | [ ] |
| **Row Count Integrity Matched** | Restored row counts (`users`, `studio_jobs`, `schema_migrations`) match production 100% | Required | [ ] |
| **Migration Dry-Run Passed** | `20261029_backend_durability.up.sql` applied cleanly to restored snapshot data | Required | [ ] |
| **12 Indexes Programmatically Validated** | Exactly 12 indexes verified: 5 unique, 4 primary key, 7 secondary, all 12 valid (`is_valid = true`) | Required | [ ] |
| **Separate-Process Decryption Proven** | Encrypted backup decrypted in a separate, isolated process using only retained key file | Required | [ ] |
| **Rehearsal Target Cleaned** | Disposable rehearsal DB dropped cleanly via automated trap; production backup & key preserved | Required | [ ] |
| **Worker PR #13 Deployed** | Worker deployed first; running healthy on Railway (`pdfnest-worker`, `alert-benevolence`) | Required | [ ] |
| **Rollback Binary Qualified** | Preflight test `TestPreflight_HistoricalBinaryRuntimeCompatibility` passing in CI | Required | [ ] |
| **Explicit Owner Authorization** | Written owner/lead approval to execute production DDL | Required | [ ] |

---

## 5. Phase 3: Transactional Migration Execution

Execute the verified idempotent script `config/migrations/20261029_backend_durability.up.sql` inside a single transaction:

```bash
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f config/migrations/20261029_backend_durability.up.sql
```

*Raw SQL equivalent (for execution in an admin SQL console):*
```sql
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';
SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtext('pdfnest:backend-durability-v1'));

DO $$ DECLARE approved boolean; BEGIN
 IF to_regclass('public.backend_durability_schema') IS NULL THEN
  IF to_regclass('public.task_completions') IS NOT NULL
   OR to_regclass('public.worker_result_ownerships') IS NOT NULL
   OR to_regclass('public.studio_storage_object_retirements') IS NOT NULL
   OR EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='studio_jobs' AND column_name='pending_object_keys')
  THEN RAISE EXCEPTION 'unversioned durability schema exists; inspect before adoption'; END IF;
 ELSE
  EXECUTE 'SELECT EXISTS(SELECT 1 FROM public.backend_durability_schema WHERE version=''20261029_backend_durability_v1'')' INTO approved;
  IF NOT approved THEN RAISE EXCEPTION 'unrecognized durability schema marker'; END IF;
 END IF;
END $$;

CREATE TABLE IF NOT EXISTS public.task_completions (
 id varchar(128) PRIMARY KEY,
 owner_identity text NOT NULL,
 download_token text NOT NULL,
 reservation_id text,
 reservation_kind text,
 state text NOT NULL,
 result_key text,
 result_url text,
 error text,
 artifact_sha256 text,
 artifact_bytes bigint,
 expires_at timestamptz,
 created_at timestamptz,
 updated_at timestamptz
);
CREATE INDEX IF NOT EXISTS idx_task_completions_reservation_id ON public.task_completions(reservation_id);
CREATE INDEX IF NOT EXISTS idx_task_completions_state ON public.task_completions(state);
CREATE INDEX IF NOT EXISTS idx_task_completions_expires_at ON public.task_completions(expires_at);

CREATE TABLE IF NOT EXISTS public.worker_result_ownerships (
 id varchar(36) PRIMARY KEY,
 request_id text NOT NULL,
 request_hash text NOT NULL,
 worker_job_id text,
 owner_identity text,
 submit_path text NOT NULL,
 profile text,
 source_keys text NOT NULL,
 state text NOT NULL,
 status_json text,
 result_key text,
 metadata_key text,
 expires_at timestamptz,
 next_attempt_at timestamptz,
 attempts bigint NOT NULL DEFAULT 0,
 last_error text,
 created_at timestamptz,
 updated_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_worker_result_ownerships_request_id ON public.worker_result_ownerships(request_id);
CREATE INDEX IF NOT EXISTS idx_worker_result_ownerships_worker_job_id ON public.worker_result_ownerships(worker_job_id);
CREATE INDEX IF NOT EXISTS idx_worker_result_ownerships_state ON public.worker_result_ownerships(state);
CREATE INDEX IF NOT EXISTS idx_worker_result_ownerships_expires_at ON public.worker_result_ownerships(expires_at);
CREATE INDEX IF NOT EXISTS idx_worker_result_ownerships_next_attempt_at ON public.worker_result_ownerships(next_attempt_at);

ALTER TABLE public.studio_jobs ADD COLUMN IF NOT EXISTS pending_object_keys jsonb;

CREATE TABLE IF NOT EXISTS public.studio_storage_object_retirements (
 object_key varchar(512) PRIMARY KEY,
 created_at timestamptz
);

CREATE TABLE IF NOT EXISTS public.backend_durability_schema (version varchar(64) PRIMARY KEY, applied_at timestamptz NOT NULL);
INSERT INTO public.backend_durability_schema(version,applied_at) VALUES ('20261029_backend_durability_v1',now()) ON CONFLICT DO NOTHING;

COMMIT;
```

---

## 6. Phase 4: Read-Only Post-Condition Verification

Verify that all schema requirements mandated by `ValidateBackendDurabilitySchema` are satisfied:

### Step 4.1: Confirm Schema Marker
```sql
SELECT version, applied_at FROM public.backend_durability_schema;
```
*Expected: 1 row with `version = '20261029_backend_durability_v1'`.*

### Step 4.2: Confirm Tables and Column Definitions
```sql
SELECT table_name, column_name, data_type, is_nullable
FROM information_schema.columns
WHERE table_schema = 'public' 
  AND (
    (table_name = 'studio_jobs' AND column_name = 'pending_object_keys') OR
    (table_name = 'task_completions' AND column_name IN ('id', 'owner_identity', 'state', 'result_key')) OR
    (table_name = 'worker_result_ownerships' AND column_name IN ('id', 'request_id', 'state')) OR
    (table_name = 'studio_storage_object_retirements' AND column_name = 'object_key')
  )
ORDER BY table_name, column_name;
```
*Expected: All 4 tables return the specified columns with matching types (`jsonb` for `pending_object_keys`, `varchar`/`text` for identifiers).*

### Step 4.3: Confirm Exact Constraints and Indexes (Uniqueness, Primary Key & Validity)
```sql
-- Verify specific primary keys, index names, uniqueness, primary-key status, and validity
SELECT 
    t.relname AS tablename,
    i.relname AS indexname,
    idx.indisunique AS is_unique,
    idx.indisprimary AS is_primary,
    idx.indisvalid AS is_valid
FROM pg_index idx
JOIN pg_class t ON t.oid = idx.indrelid
JOIN pg_class i ON i.oid = idx.indexrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = 'public' 
  AND t.relname IN ('task_completions', 'worker_result_ownerships', 'studio_storage_object_retirements', 'backend_durability_schema')
ORDER BY t.relname, i.relname;
```
*Expected: Exactly 12 index objects across the 4 durability tables (all with `is_valid = true`, exactly 5 unique, exactly 4 primary keys):*
- `backend_durability_schema`:
  - `backend_durability_schema_pkey` (UNIQUE=true, PRIMARY=true, VALID=true)
- `studio_storage_object_retirements`:
  - `studio_storage_object_retirements_pkey` (UNIQUE=true, PRIMARY=true, VALID=true)
- `task_completions`:
  - `task_completions_pkey` (UNIQUE=true, PRIMARY=true, VALID=true)
  - `idx_task_completions_expires_at` (UNIQUE=false, PRIMARY=false, VALID=true)
  - `idx_task_completions_reservation_id` (UNIQUE=false, PRIMARY=false, VALID=true)
  - `idx_task_completions_state` (UNIQUE=false, PRIMARY=false, VALID=true)
- `worker_result_ownerships`:
  - `worker_result_ownerships_pkey` (UNIQUE=true, PRIMARY=true, VALID=true)
  - `idx_worker_result_ownerships_request_id` (UNIQUE=true, PRIMARY=false, VALID=true)
  - `idx_worker_result_ownerships_expires_at` (UNIQUE=false, PRIMARY=false, VALID=true)
  - `idx_worker_result_ownerships_next_attempt_at` (UNIQUE=false, PRIMARY=false, VALID=true)
  - `idx_worker_result_ownerships_state` (UNIQUE=false, PRIMARY=false, VALID=true)
  - `idx_worker_result_ownerships_worker_job_id` (UNIQUE=false, PRIMARY=false, VALID=true)

> [!NOTE]
> Automated verification of all 12 indexes, their uniqueness, primary key properties, and validity is enforced in preflight test `TestPreflight_ExplicitIndexAndConstraintValidation` and in `scripts/run-durability-rehearsal.sh`.

> [!NOTE]
> Idempotency of the upgrade script has been fully preflight-verified in disposable fixtures (`TestPreflight_UpgradeIdempotency`). Do not re-run DDL in production unnecessarily.

---

## 7. Phase 5: Ordered Service Deployment & Health Verification

Once the database migration is confirmed:

1. **Deploy Worker First:** Deploy the paired Worker candidate (PR #13) on Railway services `pdfnest-worker` and `alert-benevolence`.
   - Worker PR #13 defaults `dedup_scope="global"` for legacy callers, ensuring uninterrupted operation with existing Backend traffic.
2. **Deploy Backend Second:** Deploy the combined Backend candidate (PR #47).
3. **Verify Startup Gate:** Check application container logs to ensure `ValidateBackendDurabilitySchema` passes without warnings or panics.
4. **Verify Health Endpoints:**
   ```bash
   curl -s -i https://<backend-host>/healthz
   # Expected: HTTP/1.1 200 OK
   curl -s -i https://<backend-host>/api/health
   # Expected: HTTP/1.1 200 OK with database: "healthy"
   ```

---

## 8. Rollback Procedures & Safeguards

### Procedure A: Binary Rollback with Retained Records (Default Recommended Response)
If the new Backend binary exhibits runtime issues, **do not revert the database schema**. Revert the Railway deployment to the known-good Backend image (`40dfe66886e7c7c5bc3f522e4ca5fcd55e669a02`).
- **SQL-Level Compatibility:** As validated in `TestPreflight_OldBinaryCompatibilityWithUpgradedSchema`, existing queries and transactions continue to function without error because all schema modifications are additive and nullable.
- **Actual Runtime Binary Compatibility:** As demonstrated in `TestPreflight_HistoricalBinaryRuntimeCompatibility`, the historical Backend binary at `40dfe66886e7c7c5bc3f522e4ca5fcd55e669a02` was compiled from source and executed live against the upgraded database schema on disposable PostgreSQL. The binary successfully booted, connected to the migrated database, and responded `HTTP 200 OK` on `/api/health` and `/api/ready` (noting that the root `/healthz` route was introduced in subsequent backend revisions).
- This preserves all task completion tokens and worker ownership records written while the new binary was running, avoiding data loss or broken client download URLs.

### Procedure B: Schema Rollback (`down.sql`) Safeguards
If schema rollback is strictly required, execute `config/migrations/20261029_backend_durability.down.sql`.

> [!CAUTION]
> **Data Loss Protection Trigger:**
> `down.sql` contains an active guard:
> ```sql
> IF EXISTS(SELECT 1 FROM public.task_completions) 
>   OR EXISTS(SELECT 1 FROM public.worker_result_ownerships)
>   OR EXISTS(SELECT 1 FROM public.studio_storage_object_retirements)
>   OR EXISTS(SELECT 1 FROM public.studio_jobs WHERE pending_object_keys IS NOT NULL AND pending_object_keys <> '[]'::jsonb)
> THEN RAISE EXCEPTION 'durability rollback refused: retained ownership exists'; END IF;
> ```
> If any task records or pending object keys were written during runtime, the rollback script **actively refuses** to drop tables. Any retained data must be audited or manually archived before dropping durability structures.
