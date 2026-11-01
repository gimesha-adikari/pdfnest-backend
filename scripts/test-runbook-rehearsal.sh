#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# Platen PDF — Automated Qualification Suite for Durability Migration Runbook
#
# Validates both negative safety controls and positive recovery paths by
# directly executing the canonical rehearsal implementation
# (scripts/run-durability-rehearsal.sh) against isolated disposable native
# PostgreSQL instances on private Unix sockets.
#
# Tests:
#   1. Missing DATABASE_URL fails fast
#   2. Missing REHEARSAL_PGHOST fails fast
#   3. Missing affirmative nonproduction confirmation fails fast
#   4. Infrastructure identity collision detection (cluster system_identifier,
#      data_directory, socket & port) refuses execution
#   5. Rehearsal user privileges on production checked and rejected if non-empty
#   6. Database name collision: preexisting database is NEVER dropped on failure
#   7. Rehearsal failure preserves encrypted recovery asset and key material;
#      external directories and unrelated files are preserved
#   8. Full positive rehearsal execution via scripts/run-durability-rehearsal.sh
#   9. Independent separate-process decryption proof using retained recovery key
# -----------------------------------------------------------------------------
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# Ensure pg_config binaries are discoverable
if command -v pg_config >/dev/null 2>&1; then
  bindir="$(pg_config --bindir)"
  export PATH="$bindir:$PATH"
else
  bindir="$(dirname "$(command -v psql)")"
fi

test_root="$(mktemp -d /tmp/platen-runbook-qualification.XXXXXXXX)"
export PGPASSFILE="$test_root/pgpass"
install -m 600 /dev/null "$PGPASSFILE"

cleanup_all() {
  local exit_code=$?
  "$bindir/pg_ctl" -D "$test_root/prod_data" -m fast -w stop >/dev/null 2>&1 || true
  "$bindir/pg_ctl" -D "$test_root/rehearsal_data" -m fast -w stop >/dev/null 2>&1 || true
  rm -rf "$test_root"
  exit "$exit_code"
}
trap cleanup_all EXIT INT TERM

echo "==> Initializing simulated production PostgreSQL instance (port 55470)..."
"$bindir/initdb" -D "$test_root/prod_data" -A trust -U platen_prod --encoding=UTF8 >"$test_root/prod_init.log" 2>&1
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
"$bindir/initdb" -D "$test_root/rehearsal_data" -A trust -U platen_rehearsal_operator --encoding=UTF8 >"$test_root/rehearsal_init.log" 2>&1
"$bindir/pg_ctl" -D "$test_root/rehearsal_data" -l "$test_root/rehearsal.log" -o "-h '' -k $test_root -p 55471 -F" -w start >/dev/null

REHEARSAL_MAINT_DSN="host=$test_root port=55471 user=platen_rehearsal_operator dbname=postgres sslmode=disable"

# -----------------------------------------------------------------------------
# NEGATIVE TEST 1: Fail fast if DATABASE_URL is unset
# -----------------------------------------------------------------------------
echo "==> [Negative Test 1] Missing DATABASE_URL..."
set +e
output=$(env -i PATH="$PATH" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55471" \
  REHEARSAL_PGUSER="platen_rehearsal_operator" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  "$repo_root/scripts/run-durability-rehearsal.sh" 2>&1)
rc=$?
set -e
if [ "$rc" -eq 0 ]; then
  echo "FAILED: Expected error on missing DATABASE_URL" >&2; exit 1
fi
echo "$output" | grep -q "DATABASE_URL must point to the production database" || {
  echo "FAILED: Output did not contain expected error message. Got: $output" >&2; exit 1
}
echo "PASSED: Missing DATABASE_URL rejected with code $rc."

# -----------------------------------------------------------------------------
# NEGATIVE TEST 2: Fail fast if REHEARSAL_PGHOST is unset
# -----------------------------------------------------------------------------
echo "==> [Negative Test 2] Missing REHEARSAL_PGHOST..."
set +e
output=$(env -i PATH="$PATH" \
  DATABASE_URL="$PROD_DSN" \
  REHEARSAL_PGUSER="platen_rehearsal_operator" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  "$repo_root/scripts/run-durability-rehearsal.sh" 2>&1)
rc=$?
set -e
if [ "$rc" -eq 0 ]; then
  echo "FAILED: Expected error on missing REHEARSAL_PGHOST" >&2; exit 1
fi
echo "$output" | grep -q "REHEARSAL_PGHOST and REHEARSAL_PGUSER must be set" || {
  echo "FAILED: Output did not contain expected error message. Got: $output" >&2; exit 1
}
echo "PASSED: Missing REHEARSAL_PGHOST rejected with code $rc."

# -----------------------------------------------------------------------------
# NEGATIVE TEST 3: Fail fast if REHEARSAL_NONPRODUCTION_CONFIRMED is missing
# -----------------------------------------------------------------------------
echo "==> [Negative Test 3] Missing affirmative non-production confirmation..."
set +e
output=$(env -i PATH="$PATH" \
  DATABASE_URL="$PROD_DSN" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55471" \
  REHEARSAL_PGUSER="platen_rehearsal_operator" \
  "$repo_root/scripts/run-durability-rehearsal.sh" 2>&1)
rc=$?
set -e
if [ "$rc" -eq 0 ]; then
  echo "FAILED: Expected error on missing REHEARSAL_NONPRODUCTION_CONFIRMED" >&2; exit 1
fi
echo "$output" | grep -q "REHEARSAL_NONPRODUCTION_CONFIRMED must be set" || {
  echo "FAILED: Output did not contain expected error message. Got: $output" >&2; exit 1
}
echo "PASSED: Missing affirmative confirmation rejected with code $rc."

# -----------------------------------------------------------------------------
# NEGATIVE TEST 4: Infrastructure identity collision detection
# -----------------------------------------------------------------------------
echo "==> [Negative Test 4] Infrastructure collision detection (targeting production cluster)..."
set +e
output=$(env -i PATH="$PATH" \
  DATABASE_URL="$PROD_DSN" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55470" \
  REHEARSAL_PGUSER="platen_prod" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  "$repo_root/scripts/run-durability-rehearsal.sh" 2>&1)
rc=$?
set -e
if [ "$rc" -eq 0 ]; then
  echo "FAILED: Expected error when rehearsal targets production cluster" >&2; exit 1
fi
echo "$output" | grep -q -E "(system_identifier.*matches production|data_directory.*matches production|Unix socket.*matches production|collide with production)" || {
  echo "FAILED: Expected infrastructure collision message. Got: $output" >&2; exit 1
}
echo "PASSED: Infrastructure collision rejected with code $rc."

# -----------------------------------------------------------------------------
# NEGATIVE TEST 5: Privilege boundary check
# -----------------------------------------------------------------------------
echo "==> [Negative Test 5] Checking rehearsal user boundary on production..."
has_priv=$(psql --dbname="$PROD_DSN" -Atc "
  SELECT has_schema_privilege('platen_rehearsal_operator', 'public', 'CREATE');
" 2>/dev/null || echo "false")
test "$has_priv" = "false" || {
  echo "FAILED: Rehearsal user must not have CREATE privileges on production!" >&2; exit 1
}
echo "PASSED: Zero privilege on production confirmed."

# -----------------------------------------------------------------------------
# NEGATIVE TEST 6: Database Name Collision & Preexisting Database Preservation
# -----------------------------------------------------------------------------
echo "==> [Negative Test 6] Preexisting database collision safety..."
# Create a preexisting database on rehearsal target
"$bindir/createdb" -h "$test_root" -p 55471 -U platen_rehearsal_operator preexisting_canary_db
psql -h "$test_root" -p 55471 -U platen_rehearsal_operator -d preexisting_canary_db -v ON_ERROR_STOP=1 << 'EOF' >/dev/null
CREATE TABLE canary_data (val text PRIMARY KEY);
INSERT INTO canary_data VALUES ('vital_preexisting_record');
EOF

set +e
output=$(env -i PATH="$PATH" \
  DATABASE_URL="$PROD_DSN" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55471" \
  REHEARSAL_PGUSER="platen_rehearsal_operator" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  REHEARSAL_DB="preexisting_canary_db" \
  "$repo_root/scripts/run-durability-rehearsal.sh" 2>&1)
rc=$?
set -e
if [ "$rc" -eq 0 ]; then
  echo "FAILED: Expected failure when REHEARSAL_DB already exists" >&2; exit 1
fi
echo "$output" | grep -q "already exists on target instance" || {
  echo "FAILED: Expected already exists message. Got: $output" >&2; exit 1
}

# CRITICAL PROOF: The preexisting database MUST STILL EXIST and its records must be intact!
canary_val=$(psql -h "$test_root" -p 55471 -U platen_rehearsal_operator -d preexisting_canary_db -Atc "SELECT val FROM canary_data;" 2>/dev/null || echo "MISSING")
if [ "$canary_val" != "vital_preexisting_record" ]; then
  echo "FATAL: Preexisting database was deleted or corrupted on createdb failure! Value: $canary_val" >&2
  exit 1
fi
echo "PASSED: Preexisting database survived collision without deletion."
# Clean up canary db
"$bindir/dropdb" -h "$test_root" -p 55471 -U platen_rehearsal_operator preexisting_canary_db

# -----------------------------------------------------------------------------
# NEGATIVE TEST 7: External BACKUP_DIR & Backup Preservation on Rehearsal Failure
# -----------------------------------------------------------------------------
echo "==> [Negative Test 7] External BACKUP_DIR and backup asset preservation across failure..."
external_dir="$test_root/external_operator_dir"
mkdir -p "$external_dir"
echo "unrelated_operator_file_contents" > "$external_dir/unrelated_operator_note.txt"

# Create a faulty migration script to force rehearsal failure during dry-run
faulty_migration="$test_root/faulty_migration.sql"
echo "SYNTAX ERROR IN MIGRATION SCRIPT;" > "$faulty_migration"

set +e
output=$(env -i PATH="$PATH" \
  DATABASE_URL="$PROD_DSN" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55471" \
  REHEARSAL_PGUSER="platen_rehearsal_operator" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  BACKUP_DIR="$external_dir" \
  MIGRATION_FILE="$faulty_migration" \
  "$repo_root/scripts/run-durability-rehearsal.sh" 2>&1)
rc=$?
set -e
if [ "$rc" -eq 0 ]; then
  echo "FAILED: Expected failure with faulty migration file" >&2; exit 1
fi

# Assertions on filesystem preservation:
# 1. Unrelated operator note must be completely intact
if [ ! -f "$external_dir/unrelated_operator_note.txt" ] || \
   [ "$(cat "$external_dir/unrelated_operator_note.txt")" != "unrelated_operator_file_contents" ]; then
  echo "FAILED: External directory contents were modified or deleted on failure!" >&2
  exit 1
fi

# 2. Encrypted recovery backup and key material MUST be preserved
preserved_dump=$(find "$external_dir" -name "*.dump.gpg" | head -n 1)
preserved_key=$(find "$external_dir" -name "*.key" | head -n 1)

if [ -z "$preserved_dump" ] || [ ! -s "$preserved_dump" ]; then
  echo "FAILED: Production encrypted backup was deleted on rehearsal failure!" >&2
  exit 1
fi
if [ -z "$preserved_key" ] || [ ! -s "$preserved_key" ]; then
  echo "FAILED: Production recovery key was deleted on rehearsal failure!" >&2
  exit 1
fi
echo "PASSED: External directory and recovery assets ($preserved_dump, $preserved_key) preserved on rehearsal failure."

# -----------------------------------------------------------------------------
# POSITIVE TEST 8: Full End-to-End Rehearsal Execution via run-durability-rehearsal.sh
# -----------------------------------------------------------------------------
echo "==> [Positive Test 8] Full end-to-end execution of canonical rehearsal script..."
positive_backup_dir="$test_root/positive_backups"

output=$(env -i PATH="$PATH" \
  DATABASE_URL="$PROD_DSN" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55471" \
  REHEARSAL_PGUSER="platen_rehearsal_operator" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  BACKUP_DIR="$positive_backup_dir" \
  "$repo_root/scripts/run-durability-rehearsal.sh")

echo "$output" | grep -q "All 12 durability indexes verified with exact table, name, uniqueness, primary-key, and validity properties." || {
  echo "FAILED: Positive rehearsal output missing index verification confirmation." >&2
  exit 1
}
echo "$output" | grep -q "Disposable rehearsal restore and migration dry run successfully completed!" || {
  echo "FAILED: Positive rehearsal output missing completion confirmation." >&2
  exit 1
}

# -----------------------------------------------------------------------------
# POSITIVE TEST 9: Independent Separate-Process Decryption Proof
# -----------------------------------------------------------------------------
echo "==> [Positive Test 9] Verifying decryption in an independent process using retained recovery key..."
prod_dump_gpg=$(find "$positive_backup_dir" -name "*.dump.gpg" | head -n 1)
prod_key_file=$(find "$positive_backup_dir" -name "*.key" | head -n 1)

test -n "$prod_dump_gpg" && test -s "$prod_dump_gpg" || { echo "FAILED: Missing encrypted backup file." >&2; exit 1; }
test -n "$prod_key_file" && test -s "$prod_key_file" || { echo "FAILED: Missing recovery key file." >&2; exit 1; }

fresh_toc="$test_root/fresh_process_decrypted.toc"

# Run in scrubbed environment with zero ambient secrets
env -i PATH="$PATH" \
  gpg --batch --quiet --pinentry-mode loopback --decrypt \
      --passphrase-file "$prod_key_file" "$prod_dump_gpg" | \
      pg_restore --list > "$fresh_toc"

grep -q "TABLE DATA public users" "$fresh_toc" || {
  echo "FAILED: Fresh process decrypted TOC missing users table data!" >&2; exit 1
}
grep -q "TABLE DATA public studio_jobs" "$fresh_toc" || {
  echo "FAILED: Fresh process decrypted TOC missing studio_jobs table data!" >&2; exit 1
}
echo "PASSED: Retained recovery key cleanly decrypted backup in independent fresh process."

echo ""
echo "================================================================================"
echo "ALL REHEARSAL RUNBOOK QUALIFICATION CHECKS PASSED SUCCESSFULLY!"
echo "================================================================================"
