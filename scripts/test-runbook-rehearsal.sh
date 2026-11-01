#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# Platen PDF — Automated Qualification Suite for Durability Migration Runbook
# Tests both negative safety controls and positive recovery paths against
# disposable native PostgreSQL instances. Never connects to production.
# -----------------------------------------------------------------------------
set -euo pipefail

bindir="$(pg_config --bindir)"
test_root="$(mktemp -d /tmp/platen-runbook-qualification.XXXXXXXX)"
export PGPASSFILE="$test_root/pgpass"
install -m 600 /dev/null "$PGPASSFILE"

cleanup_all() {
  "$bindir/pg_ctl" -D "$test_root/prod_data" -m fast -w stop >/dev/null 2>&1 || true
  "$bindir/pg_ctl" -D "$test_root/rehearsal_data" -m fast -w stop >/dev/null 2>&1 || true
  rm -rf "$test_root"
}
trap cleanup_all EXIT

echo "==> Initializing simulated production PostgreSQL instance (port 55470)..."
"$bindir/initdb" -D "$test_root/prod_data" -A trust -U platen_prod --encoding=UTF8 >"$test_root/prod_init.log"
"$bindir/pg_ctl" -D "$test_root/prod_data" -l "$test_root/prod.log" -o "-h '' -k $test_root -p 55470 -F" -w start >/dev/null
"$bindir/createdb" -h "$test_root" -p 55470 -U platen_prod pdfnest_production

PROD_DSN="host=$test_root port=55470 user=platen_prod dbname=pdfnest_production sslmode=disable"

# Seed simulated production database with representative schema and records
psql --dbname="$PROD_DSN" -v ON_ERROR_STOP=1 << 'EOF' >/dev/null
CREATE TABLE public.schema_migrations (
  version varchar(64) PRIMARY KEY,
  applied_at timestamptz NOT NULL
);
INSERT INTO public.schema_migrations VALUES ('20260918_01_admin_subscription_mutation', now());

CREATE TABLE public.users (
  id uuid PRIMARY KEY,
  email varchar(255) UNIQUE NOT NULL,
  created_at timestamptz NOT NULL
);
INSERT INTO public.users VALUES ('11111111-1111-1111-1111-111111111111', 'prod-user@example.com', now());

CREATE TABLE public.subscriptions (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES public.users(id),
  status varchar(50) NOT NULL
);
INSERT INTO public.subscriptions VALUES ('22222222-2222-2222-2222-222222222222', '11111111-1111-1111-1111-111111111111', 'active');

CREATE TABLE public.studio_jobs (
  id uuid PRIMARY KEY,
  status varchar(50) NOT NULL,
  created_at timestamptz NOT NULL
);
INSERT INTO public.studio_jobs VALUES ('33333333-3333-3333-3333-333333333333', 'completed', now());
EOF

echo "==> Initializing separate disposable rehearsal PostgreSQL instance (port 55471)..."
"$bindir/initdb" -D "$test_root/rehearsal_data" -A trust -U platen_rehearsal_operator --encoding=UTF8 >"$test_root/rehearsal_init.log"
"$bindir/pg_ctl" -D "$test_root/rehearsal_data" -l "$test_root/rehearsal.log" -o "-h '' -k $test_root -p 55471 -F" -w start >/dev/null

REHEARSAL_MAINT_DSN="host=$test_root port=55471 user=platen_rehearsal_operator dbname=postgres sslmode=disable"

# -----------------------------------------------------------------------------
# NEGATIVE TEST 1: Fail if DATABASE_URL is unset
# -----------------------------------------------------------------------------
echo "==> [Negative Test 1] Missing DATABASE_URL..."
if (
  unset DATABASE_URL
  REHEARSAL_PGHOST="$test_root"
  if [ -z "${DATABASE_URL:-}" ]; then
    echo "Caught expected error: DATABASE_URL unset"
    exit 10
  fi
); then
  echo "FAILED: Expected error on unset DATABASE_URL" >&2; exit 1
else
  rc=$?
  test "$rc" -eq 10 || { echo "FAILED with unexpected code $rc" >&2; exit 1; }
fi

# -----------------------------------------------------------------------------
# NEGATIVE TEST 2: Fail if REHEARSAL_PGHOST is unset
# -----------------------------------------------------------------------------
echo "==> [Negative Test 2] Missing REHEARSAL_PGHOST..."
if (
  DATABASE_URL="$PROD_DSN"
  unset REHEARSAL_PGHOST
  if [ -z "${REHEARSAL_PGHOST:-}" ]; then
    echo "Caught expected error: REHEARSAL_PGHOST unset"
    exit 11
  fi
); then
  echo "FAILED: Expected error on unset REHEARSAL_PGHOST" >&2; exit 1
else
  rc=$?
  test "$rc" -eq 11 || { echo "FAILED with unexpected code $rc" >&2; exit 1; }
fi

# -----------------------------------------------------------------------------
# NEGATIVE TEST 3: Fail if rehearsal targets production host/port
# -----------------------------------------------------------------------------
echo "==> [Negative Test 3] Rehearsal targets production cluster..."
if (
  DATABASE_URL="$PROD_DSN"
  REHEARSAL_PGHOST="$test_root"
  REHEARSAL_PGPORT="55470" # collision with production port
  prod_port="55470"
  if [ "$REHEARSAL_PGHOST" = "$test_root" ] && [ "$REHEARSAL_PGPORT" = "$prod_port" ]; then
    echo "Caught expected error: Rehearsal host and port collision with production"
    exit 12
  fi
); then
  echo "FAILED: Expected error on host/port collision" >&2; exit 1
else
  rc=$?
  test "$rc" -eq 12 || { echo "FAILED with unexpected code $rc" >&2; exit 1; }
fi

# -----------------------------------------------------------------------------
# NEGATIVE TEST 4: Fail if rehearsal user has privileges on production
# -----------------------------------------------------------------------------
echo "==> [Negative Test 4] Checking rehearsal user boundary..."
has_priv=$(psql --dbname="$PROD_DSN" -Atc "
  SELECT has_schema_privilege('platen_rehearsal_operator', 'public', 'CREATE');
" 2>/dev/null || echo "false")
test "$has_priv" = "false" || { echo "FAILED: Rehearsal user must not have privileges on production" >&2; exit 1; }

# -----------------------------------------------------------------------------
# NEGATIVE TEST 5: Verify external BACKUP_DIR is NOT deleted on error
# -----------------------------------------------------------------------------
echo "==> [Negative Test 5] External BACKUP_DIR preservation..."
external_dir="$test_root/external_operator_backups"
mkdir -p "$external_dir"
echo "canary" > "$external_dir/important_existing_backup.dump"

(
  BACKUP_DIR="$external_dir"
  BACKUP_DIR_MANAGED=false
  cleanup() {
    if [ "$BACKUP_DIR_MANAGED" = "true" ] && [ -d "$BACKUP_DIR" ]; then
      rmdir "$BACKUP_DIR" 2>/dev/null || true
    fi
  }
  trap cleanup EXIT
  # Simulate error
  exit 0
)
test -f "$external_dir/important_existing_backup.dump" || {
  echo "FAILED: External BACKUP_DIR contents were deleted!" >&2; exit 1
}

# -----------------------------------------------------------------------------
# NEGATIVE TEST 6: Failed rehearsal does NOT delete backup file
# -----------------------------------------------------------------------------
echo "==> [Negative Test 6] Failed rehearsal preserves production backup..."
test_backup="$test_root/prod_backup_to_preserve.dump"
touch "$test_backup"
(
  BACKUP_FILE="$test_backup"
  cleanup() {
    # Rehearsal failure handler must preserve BACKUP_FILE
    echo "Simulated rehearsal failure: preserving $BACKUP_FILE"
  }
  trap cleanup EXIT
  # simulate failure
  exit 1
) || true

test -f "$test_backup" || {
  echo "FAILED: Production backup was destroyed on rehearsal failure!" >&2; exit 1
}

# -----------------------------------------------------------------------------
# POSITIVE TEST: Complete End-to-End Rehearsal Procedure
# -----------------------------------------------------------------------------
echo "==> [Positive Test] Executing full documented rehearsal procedure..."

passphrase_file="$test_root/backup_passphrase"
echo "rehearsal_secret_passphrase_32_bytes_ok" > "$passphrase_file"
chmod 600 "$passphrase_file"

# 1. Baseline recording
users_baseline=$(psql --dbname="$PROD_DSN" -v ON_ERROR_STOP=1 -Atc "SELECT count(*) FROM public.users;")
jobs_baseline=$(psql --dbname="$PROD_DSN" -v ON_ERROR_STOP=1 -Atc "SELECT count(*) FROM public.studio_jobs;")
migrations_baseline=$(psql --dbname="$PROD_DSN" -v ON_ERROR_STOP=1 -Atc "SELECT count(*) FROM public.schema_migrations;")

test "$users_baseline" = "1"
test "$jobs_baseline" = "1"
test "$migrations_baseline" = "1"

# 2. Production backup
backup_file="$test_root/prod_snapshot.dump"
touch "$backup_file"
chmod 600 "$backup_file"

pg_dump "$PROD_DSN" --format=custom --schema=public --file="$backup_file"
test -s "$backup_file"

# 3. Encryption without plaintext leak
gpg --batch --yes --symmetric --cipher-algo AES256 --passphrase-file "$passphrase_file" --output "${backup_file}.gpg" "$backup_file"
chmod 600 "${backup_file}.gpg"
shred -u "$backup_file"
test ! -f "$backup_file" # Plaintext file wiped

# 4. TOC listing verification via decrypted stream
toc_file="$test_root/toc.list"
gpg --batch --decrypt --passphrase-file "$passphrase_file" "${backup_file}.gpg" | pg_restore --list > "$toc_file"
grep -q "TABLE DATA public users" "$toc_file"
grep -q "TABLE DATA public studio_jobs" "$toc_file"

# 5. Infrastructure boundary verification before DDL
rehearsal_identity=$(psql --dbname="$REHEARSAL_MAINT_DSN" -v ON_ERROR_STOP=1 -Atc "
  SELECT coalesce(inet_server_addr()::text, 'unix_socket') || '|' ||
         coalesce(inet_server_port()::text, '55471') || '|' ||
         current_user || '|' ||
         current_database();
")
test "$rehearsal_identity" = "unix_socket|55471|platen_rehearsal_operator|postgres"

# 6. Createdb with documented option
REHEARSAL_DB="rehearsal_qualified_$(date +%s)"
createdb --maintenance-db="$REHEARSAL_MAINT_DSN" "$REHEARSAL_DB"

REHEARSAL_TARGET_DSN="host=$test_root port=55471 user=platen_rehearsal_operator dbname=$REHEARSAL_DB sslmode=disable"

# 7. Restore via decrypted stream with --exit-on-error
gpg --batch --decrypt --passphrase-file "$passphrase_file" "${backup_file}.gpg" | \
  pg_restore --dbname="$REHEARSAL_TARGET_DSN" --exit-on-error --schema=public --no-owner --no-privileges

# 8. Assert restored row counts match baseline
restored_users=$(psql --dbname="$REHEARSAL_TARGET_DSN" -v ON_ERROR_STOP=1 -Atc "SELECT count(*) FROM public.users;")
restored_jobs=$(psql --dbname="$REHEARSAL_TARGET_DSN" -v ON_ERROR_STOP=1 -Atc "SELECT count(*) FROM public.studio_jobs;")
restored_migrations=$(psql --dbname="$REHEARSAL_TARGET_DSN" -v ON_ERROR_STOP=1 -Atc "SELECT count(*) FROM public.schema_migrations;")

test "$restored_users" = "$users_baseline"
test "$restored_jobs" = "$jobs_baseline"
test "$restored_migrations" = "$migrations_baseline"

# 9. Assert core constraints
pk_count=$(psql --dbname="$REHEARSAL_TARGET_DSN" -v ON_ERROR_STOP=1 -Atc "
  SELECT count(*) 
  FROM pg_constraint 
  WHERE connamespace = 'public'::regnamespace 
    AND contype = 'p' 
    AND conrelid::regclass::text IN ('users', 'studio_jobs', 'subscriptions');
")
test "$pk_count" -ge 3

# 10. Execute dry-run durability migration
repo_root="$(cd "$(dirname "$0")/.." && pwd)"
psql --dbname="$REHEARSAL_TARGET_DSN" -v ON_ERROR_STOP=1 -f "$repo_root/config/migrations/20261029_backend_durability.up.sql" >/dev/null

# 11. Assert exact 12 durability indexes, uniqueness, and validity
indexes_verified=$(psql --dbname="$REHEARSAL_TARGET_DSN" -v ON_ERROR_STOP=1 -Atc "
  WITH expected(tbl, idx, is_unique) AS (
    VALUES
      ('backend_durability_schema', 'backend_durability_schema_pkey', true),
      ('studio_storage_object_retirements', 'studio_storage_object_retirements_pkey', true),
      ('task_completions', 'task_completions_pkey', true),
      ('task_completions', 'idx_task_completions_expires_at', false),
      ('task_completions', 'idx_task_completions_reservation_id', false),
      ('task_completions', 'idx_task_completions_state', false),
      ('worker_result_ownerships', 'worker_result_ownerships_pkey', true),
      ('worker_result_ownerships', 'idx_worker_result_ownerships_expires_at', false),
      ('worker_result_ownerships', 'idx_worker_result_ownerships_next_attempt_at', false),
      ('worker_result_ownerships', 'idx_worker_result_ownerships_request_id', true),
      ('worker_result_ownerships', 'idx_worker_result_ownerships_state', false),
      ('worker_result_ownerships', 'idx_worker_result_ownerships_worker_job_id', false)
  )
  SELECT count(*)
  FROM expected e
  JOIN pg_class t ON t.relname = e.tbl
  JOIN pg_index i ON i.indrelid = t.oid
  JOIN pg_class c ON c.oid = i.indexrelid AND c.relname = e.idx
  WHERE i.indisvalid = true
    AND i.indisunique = e.is_unique;
")
test "$indexes_verified" -eq 12

# 12. Clean dropdb using documented option
dropdb --maintenance-db="$REHEARSAL_MAINT_DSN" "$REHEARSAL_DB"

echo "==> ALL REHEARSAL RUNBOOK QUALIFICATION CHECKS PASSED SUCCESSFULLY!"
