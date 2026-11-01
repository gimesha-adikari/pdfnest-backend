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
> Fail-closed infrastructure checks must verify that rehearsal hosts and identity do not match production before any creation, restore, dry-run, or deletion is attempted.

### Step 2.1: Fail-Safe Backup & Rehearsal Script

The following script implements fail-fast shell execution (`set -euo pipefail`), automated trap cleanup, strict file permissions (`umask 077`, `chmod 600`), infrastructure identity boundary checks, TOC validation, streaming AES-256 encrypted restore rehearsal on a dedicated non-production target, data integrity assertions, and migration dry-run:

```bash
#!/usr/bin/env bash
set -euo pipefail

# -----------------------------------------------------------------------------
# Configuration & Infrastructure Boundary Enforcement
# -----------------------------------------------------------------------------
# DATABASE_URL: Railway production PostgreSQL connection string (read-only snapshot)
# REHEARSAL_*: Credentials for an isolated, disposable non-production PostgreSQL instance
if [ -z "${DATABASE_URL:-}" ]; then
  echo "FATAL: DATABASE_URL must point to the production database." >&2
  exit 1
fi

if [ -z "${REHEARSAL_PGHOST:-}" ] || [ -z "${REHEARSAL_PGUSER:-}" ]; then
  echo "FATAL: REHEARSAL_PGHOST and REHEARSAL_PGUSER must be set for the disposable rehearsal target." >&2
  echo "Rehearsals must NEVER run against the production database or cluster." >&2
  exit 1
fi

REHEARSAL_PGPORT="${REHEARSAL_PGPORT:-5432}"
REHEARSAL_MAINTENANCE_DB="${REHEARSAL_MAINTENANCE_DB:-postgres}"
REHEARSAL_PGPASSWORD="${REHEARSAL_PGPASSWORD:-}"

# Explicit maintenance and dynamic database DSNs (avoiding URI concatenation)
REHEARSAL_MAINTENANCE_DSN="host=$REHEARSAL_PGHOST port=$REHEARSAL_PGPORT user=$REHEARSAL_PGUSER dbname=$REHEARSAL_MAINTENANCE_DB"
if [ -n "$REHEARSAL_PGPASSWORD" ]; then
  REHEARSAL_MAINTENANCE_DSN="$REHEARSAL_MAINTENANCE_DSN password=$REHEARSAL_PGPASSWORD"
fi

# Identity & infrastructure verification
echo "==> Verifying production and rehearsal infrastructure isolation..."
prod_info=$(psql "$DATABASE_URL" -t -A -v ON_ERROR_STOP=1 -c "SELECT coalesce(inet_server_addr()::text, 'socket'), inet_server_port(), current_user, current_database();")
prod_addr=$(echo "$prod_info" | cut -d'|' -f1)
prod_port=$(echo "$prod_info" | cut -d'|' -f2)
prod_user=$(echo "$prod_info" | cut -d'|' -f3)
prod_db=$(echo "$prod_info" | cut -d'|' -f4)

rehearsal_info=$(psql -d "$REHEARSAL_MAINTENANCE_DSN" -t -A -v ON_ERROR_STOP=1 -c "SELECT coalesce(inet_server_addr()::text, 'socket'), inet_server_port(), current_user, current_database();")
rehearsal_addr=$(echo "$rehearsal_info" | cut -d'|' -f1)
rehearsal_port=$(echo "$rehearsal_info" | cut -d'|' -f2)
rehearsal_user=$(echo "$rehearsal_info" | cut -d'|' -f3)

# Fail-closed checks
if [ "$prod_addr" = "$rehearsal_addr" ] && [ "$prod_port" = "$rehearsal_port" ]; then
  echo "FATAL: Rehearsal target ($rehearsal_addr:$rehearsal_port) matches production ($prod_addr:$prod_port)!" >&2
  echo "Rehearsal target must be a completely separate server or instance." >&2
  exit 1
fi

if [ "$prod_user" = "$rehearsal_user" ] && [ "$prod_addr" = "$rehearsal_addr" ]; then
  echo "FATAL: Rehearsal user matches production user on the same host." >&2
  exit 1
fi

echo "Infrastructure isolation confirmed: Production ($prod_addr:$prod_port, db=$prod_db) != Rehearsal ($rehearsal_addr:$rehearsal_port, user=$rehearsal_user)."

# Restrict default file creation permissions for process
umask 077

# Backup directory lifecycle management
BACKUP_DIR_MANAGED=false
if [ -z "${BACKUP_DIR:-}" ]; then
  BACKUP_DIR="$(mktemp -d /tmp/platen-backup-XXXXXX)"
  BACKUP_DIR_MANAGED=true
else
  mkdir -p "$BACKUP_DIR"
fi
chmod 700 "$BACKUP_DIR"

BACKUP_TS="$(date +%Y%m%d_%H%M%S)"
BACKUP_FILE="$BACKUP_DIR/pdfnest_backup_pre_durability_${BACKUP_TS}.dump"
ENCRYPTED_BACKUP="${BACKUP_FILE}.gpg"
REHEARSAL_DB="rehearsal_${BACKUP_TS}_${RANDOM}"
REHEARSAL_DB_DSN="host=$REHEARSAL_PGHOST port=$REHEARSAL_PGPORT user=$REHEARSAL_PGUSER dbname=$REHEARSAL_DB"
if [ -n "$REHEARSAL_PGPASSWORD" ]; then
  REHEARSAL_DB_DSN="$REHEARSAL_DB_DSN password=$REHEARSAL_PGPASSWORD"
fi

# Encryption key / passphrase setup
export GPG_PASSPHRASE="${GPG_PASSPHRASE:-$(head -c 32 /dev/urandom | base64)}"

# -----------------------------------------------------------------------------
# Automated Safe Trap Cleanup
# -----------------------------------------------------------------------------
cleanup() {
  local exit_code=$?
  echo "Executing safe rehearsal cleanup..."
  
  # 1. Clean up disposable rehearsal database (never production)
  if [ -n "${REHEARSAL_DB:-}" ]; then
    dropdb --maintenance-db="$REHEARSAL_MAINTENANCE_DSN" --if-exists "$REHEARSAL_DB" 2>/dev/null || true
  fi

  # 2. Ephemeral artifacts cleanup
  rm -f "$BACKUP_DIR/toc.list" 2>/dev/null || true

  # 3. CRITICAL: Never delete production backup on rehearsal failure!
  # The recovery backup ($ENCRYPTED_BACKUP or $BACKUP_FILE) must be preserved if rehearsal fails
  # or if migration is pending. Only delete if explicit cleanup is requested AND rehearsal succeeded.
  if [ "${CLEANUP_BACKUP_ON_EXIT:-false}" = "true" ] && [ $exit_code -eq 0 ]; then
    rm -f "$BACKUP_FILE" "$ENCRYPTED_BACKUP" 2>/dev/null || true
    if [ "$BACKUP_DIR_MANAGED" = "true" ]; then
      rmdir "$BACKUP_DIR" 2>/dev/null || rm -rf "$BACKUP_DIR"
    fi
  else
    echo "==> Production recovery backup preserved at: ${ENCRYPTED_BACKUP:-$BACKUP_FILE}"
    if [ $exit_code -ne 0 ]; then
      echo "==> REHEARSAL FAILED. Backup asset retained for disaster recovery. Rehearsal DB dropped." >&2
    fi
  fi
  exit "$exit_code"
}
trap cleanup EXIT INT TERM

# -----------------------------------------------------------------------------
# 1. Record Production Baseline Metrics
# -----------------------------------------------------------------------------
echo "==> 1. Recording baseline production row counts..."
baseline_users=$(psql "$DATABASE_URL" -t -A -v ON_ERROR_STOP=1 -c "SELECT count(*) FROM public.users;")
baseline_jobs=$(psql "$DATABASE_URL" -t -A -v ON_ERROR_STOP=1 -c "SELECT count(*) FROM public.studio_jobs;")
baseline_migrations=$(psql "$DATABASE_URL" -t -A -v ON_ERROR_STOP=1 -c "SELECT count(*) FROM public.schema_migrations;")
echo "Production baseline: users=$baseline_users, studio_jobs=$baseline_jobs, schema_migrations=$baseline_migrations"

# -----------------------------------------------------------------------------
# 2. Take Custom-Format Production Snapshot
# -----------------------------------------------------------------------------
echo "==> 2. Taking custom-format production backup..."
touch "$BACKUP_FILE"
chmod 600 "$BACKUP_FILE"

pg_dump "$DATABASE_URL" \
  --format=custom \
  --schema=public \
  --file="$BACKUP_FILE"

test -s "$BACKUP_FILE" || { echo "FATAL: Dump file is empty or missing."; exit 1; }
ls -lh "$BACKUP_FILE"

# -----------------------------------------------------------------------------
# 3. Symmetrically Encrypt Snapshot (AES-256) & Crypto-Shred Plaintext
# -----------------------------------------------------------------------------
echo "==> 3. Encrypting backup with AES-256..."
gpg --batch --yes --symmetric --cipher-algo AES256 \
  --passphrase "$GPG_PASSPHRASE" \
  --output "$ENCRYPTED_BACKUP" "$BACKUP_FILE"
chmod 600 "$ENCRYPTED_BACKUP"

# Securely remove temporary unencrypted dump file
shred -u "$BACKUP_FILE" 2>/dev/null || rm -f "$BACKUP_FILE"
echo "Plaintext dump removed. Encrypted backup secured at: $ENCRYPTED_BACKUP"

# -----------------------------------------------------------------------------
# 4. Verify Archive TOC Listing from Decrypted Stream
# -----------------------------------------------------------------------------
echo "==> 4. Verifying archive TOC framing from encrypted stream..."
gpg --batch --quiet --decrypt --passphrase "$GPG_PASSPHRASE" "$ENCRYPTED_BACKUP" | \
  pg_restore --list > "$BACKUP_DIR/toc.list"

grep -q "TABLE DATA public users" "$BACKUP_DIR/toc.list" || { echo "FATAL: TOC missing users table data."; exit 1; }
grep -q "TABLE DATA public studio_jobs" "$BACKUP_DIR/toc.list" || { echo "FATAL: TOC missing studio_jobs table data."; exit 1; }
echo "Backup archive TOC listing verified."

# -----------------------------------------------------------------------------
# 5. Create Disposable Rehearsal Database on Separate Target
# -----------------------------------------------------------------------------
echo "==> 5. Creating disposable rehearsal database '$REHEARSAL_DB' on non-production target..."
createdb --maintenance-db="$REHEARSAL_MAINTENANCE_DSN" "$REHEARSAL_DB"

# -----------------------------------------------------------------------------
# 6. Perform Real Streaming Rehearsal Restore
# -----------------------------------------------------------------------------
echo "==> 6. Restoring snapshot into disposable rehearsal database from decrypted pipe..."
gpg --batch --quiet --decrypt --passphrase "$GPG_PASSPHRASE" "$ENCRYPTED_BACKUP" | \
  pg_restore --dbname="$REHEARSAL_DB_DSN" \
    --exit-on-error \
    --schema=public \
    --no-owner \
    --no-privileges

# -----------------------------------------------------------------------------
# 7. Assert Restored Data Integrity Against Production Baseline
# -----------------------------------------------------------------------------
echo "==> 7. Verifying restored data integrity against production baseline..."
restored_users=$(psql -d "$REHEARSAL_DB_DSN" -t -A -v ON_ERROR_STOP=1 -c "SELECT count(*) FROM public.users;")
restored_jobs=$(psql -d "$REHEARSAL_DB_DSN" -t -A -v ON_ERROR_STOP=1 -c "SELECT count(*) FROM public.studio_jobs;")
restored_migrations=$(psql -d "$REHEARSAL_DB_DSN" -t -A -v ON_ERROR_STOP=1 -c "SELECT count(*) FROM public.schema_migrations;")

echo "Restored counts: users=$restored_users, studio_jobs=$restored_jobs, schema_migrations=$restored_migrations"

if [ "$restored_users" != "$baseline_users" ] || \
   [ "$restored_jobs" != "$baseline_jobs" ] || \
   [ "$restored_migrations" != "$baseline_migrations" ]; then
  echo "FATAL: Restored row counts do not match production baseline!" >&2
  exit 1
fi
echo "Row counts verified: 100% match with production baseline."

# Verify constraints
psql -d "$REHEARSAL_DB_DSN" -v ON_ERROR_STOP=1 -c "
  SELECT conname, contype, conrelid::regclass 
  FROM pg_constraint 
  WHERE connamespace = 'public'::regnamespace 
    AND conrelid::regclass::text IN ('users', 'studio_jobs', 'subscriptions');
"

# -----------------------------------------------------------------------------
# 8. Rehearsal Migration Dry Run
# -----------------------------------------------------------------------------
echo "==> 8. Testing durability migration against restored rehearsal data..."
psql -d "$REHEARSAL_DB_DSN" -v ON_ERROR_STOP=1 -f config/migrations/20261029_backend_durability.up.sql

# -----------------------------------------------------------------------------
# 9. Programmatically Assert All 12 Durability Index Objects
# -----------------------------------------------------------------------------
echo "==> 9. Programmatically asserting 12 durability index objects on rehearsal database..."
idx_report=$(psql -d "$REHEARSAL_DB_DSN" -t -A -v ON_ERROR_STOP=1 -c "
  SELECT 
      count(*) AS total_indexes,
      count(*) FILTER (WHERE idx.indisvalid = true) AS valid_indexes,
      count(*) FILTER (WHERE idx.indisunique = true) AS unique_indexes
  FROM pg_index idx
  JOIN pg_class t ON t.oid = idx.indrelid
  JOIN pg_class i ON i.oid = idx.indexrelid
  JOIN pg_namespace n ON n.oid = t.relnamespace
  WHERE n.nspname = 'public' 
    AND t.relname IN ('task_completions', 'worker_result_ownerships', 'studio_storage_object_retirements', 'backend_durability_schema');
")

total_idx=$(echo "$idx_report" | cut -d'|' -f1)
valid_idx=$(echo "$idx_report" | cut -d'|' -f2)
unique_idx=$(echo "$idx_report" | cut -d'|' -f3)

echo "Durability indexes: total=$total_idx (expected 12), valid=$valid_idx (expected 12), unique=$unique_idx (expected 6)"

if [ "$total_idx" -ne 12 ] || [ "$valid_idx" -ne 12 ] || [ "$unique_idx" -ne 6 ]; then
  echo "FATAL: Durability index validation failed! Expected exactly 12 total, 12 valid, 6 unique." >&2
  exit 1
fi

echo "==> Rehearsal restore, data integrity verification, and migration dry run PASSED!"
# Safe cleanup trap automatically drops $REHEARSAL_DB and temporary files on exit.
```

### Step 2.2: Backup Access Control, Encryption, and Disposal Standards
1. **Access Control:** The backup archive contains sensitive production records (user identities, studio job parameters, file keys). Process permissions must enforce `umask 077` and `chmod 600`. File ownership must belong strictly to the migration operator role with zero permissions granted to other users or group members.
2. **Mandatory Encryption at Rest (AES-256):** Every snapshot must be encrypted immediately upon creation using AES-256 symmetric cipher (`gpg --symmetric --cipher-algo AES256`). Plaintext snapshots must never be transmitted across networks or stored at rest on shared file systems.
3. **Restricted Handling:** Never upload database dumps to unmonitored storage buckets, team communication channels, or shared volumes.
4. **Disposal Lifecycle & Modern Flash/Cloud Cryptographic Erasure (Crypto-Shredding):**
   - *Physical Limitations of Overwrite on SSDs/Cloud Volumes:* Modern solid-state drives (SSDs, NVMe) and virtualized cloud block devices (e.g. AWS EBS, GCP Persistent Disk, Railway volume mounts) utilize flash translation layers (FTL), wear-leveling algorithms, over-provisioning pools, and copy-on-write sparse remapping. Consequently, conventional file overwriting tools (`shred`, `rm -P`) overwrite only the currently mapped logical blocks; residual physical flash cells or underlying storage snapshots continue to retain historical data blocks until low-level garbage collection occurs.
   - *Crypto-Shredding as the Authoritative Standard:* Pre-dump or immediate symmetric encryption with AES-256 provides absolute confidentiality. When the snapshot reaches the end of its agreed retention window (typically 24–48 hours post-migration), **cryptographic erasure** is performed by destroying the encryption key/passphrase (`GPG_PASSPHRASE`) and shredding key files. Once the key is destroyed, ciphertext remaining on flash blocks or block-volume snapshots is rendered mathematically impossible to decrypt.
   - *Disposal Execution:* Once Phase 4 post-condition verification is complete and the release window concludes:
     ```bash
     # Destroy the ciphertext file and purge memory/passphrase variables
     shred -u "$ENCRYPTED_BACKUP"
     unset GPG_PASSPHRASE
     ```

### Step 2.3: Pre-Migration Sign-Off & Approval Checklist

Before proceeding to Phase 3 (Production Execution), confirm and check off every requirement:

| Prerequisite | Verification Method | Status | Sign-Off |
| :--- | :--- | :--- | :--- |
| **Phase 1 Preflight Checks** | Read-only SQL queries in Step 1.1–1.4 executed; no locks > 10s, no unversioned tables, schema ledger verified | Required | [ ] |
| **Infrastructure Isolation Confirmed** | `inet_server_addr()` and `current_user` verified; rehearsal host/socket distinct from production | Required | [ ] |
| **Production Snapshot Taken** | `pg_dump -Fc` completed with non-zero size, restricted `0600` permissions | Required | [ ] |
| **AES-256 Encryption & Plaintext Shredding** | Snapshot encrypted with AES-256 (`.dump.gpg`); unencrypted dump securely removed | Required | [ ] |
| **TOC Integrity Confirmed** | `pg_restore --list` from decrypted stream confirms required table structures | Required | [ ] |
| **Streaming Rehearsal Restore Passed** | Decrypted stream restored to disposable DB with `--exit-on-error`; 0 errors | Required | [ ] |
| **Row Count Integrity Matched** | Restored row counts (`users`, `studio_jobs`, `schema_migrations`) match production 100% | Required | [ ] |
| **Migration Dry-Run Passed** | `20261029_backend_durability.up.sql` applied cleanly to restored snapshot data | Required | [ ] |
| **12 Indexes Programmatically Validated** | All 12 durability indexes confirmed valid (`is_valid = true`) and unique in rehearsal database | Required | [ ] |
| **Rehearsal Target Cleaned** | Rehearsal database dropped cleanly via automated trap; production backup preserved | Required | [ ] |
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

### Step 4.3: Confirm Exact Constraints and Indexes (Uniqueness & Validity)
```sql
-- Verify specific primary keys, index names, uniqueness, and validity
SELECT 
    t.relname AS tablename,
    i.relname AS indexname,
    idx.indisunique AS is_unique,
    idx.indisvalid AS is_valid
FROM pg_index idx
JOIN pg_class t ON t.oid = idx.indrelid
JOIN pg_class i ON i.oid = idx.indexrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = 'public' 
  AND t.relname IN ('task_completions', 'worker_result_ownerships', 'studio_storage_object_retirements', 'backend_durability_schema')
ORDER BY t.relname, i.relname;
```
*Expected: The following 12 exact index objects must be returned, all with `is_valid = true`:*
- `backend_durability_schema`:
  - `backend_durability_schema_pkey` (UNIQUE, VALID)
- `studio_storage_object_retirements`:
  - `studio_storage_object_retirements_pkey` (UNIQUE, VALID)
- `task_completions`:
  - `task_completions_pkey` (UNIQUE, VALID)
  - `idx_task_completions_expires_at` (NON-UNIQUE, VALID)
  - `idx_task_completions_reservation_id` (NON-UNIQUE, VALID)
  - `idx_task_completions_state` (NON-UNIQUE, VALID)
- `worker_result_ownerships`:
  - `worker_result_ownerships_pkey` (UNIQUE, VALID)
  - `idx_worker_result_ownerships_expires_at` (NON-UNIQUE, VALID)
  - `idx_worker_result_ownerships_next_attempt_at` (NON-UNIQUE, VALID)
  - `idx_worker_result_ownerships_request_id` (UNIQUE, VALID)
  - `idx_worker_result_ownerships_state` (NON-UNIQUE, VALID)
  - `idx_worker_result_ownerships_worker_job_id` (NON-UNIQUE, VALID)

> [!NOTE]
> Automated verification of all 12 indexes, their uniqueness, and their validity is enforced in preflight test `TestPreflight_ExplicitIndexAndConstraintValidation`.

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
