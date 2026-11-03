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
#   4. Separate key custody: missing BACKUP_PASSPHRASE_FILE without synthetic flag fails fast
#   5. Infrastructure identity collision detection refuses execution
#   6. Real privilege rejection: role with production write privileges is rejected
#      before creating any rehearsal database or backup
#   7. Privilege query error rejection: query errors fail closed
#   8. Malformed privilege result rejection: unknown states fail closed
#   9. Valid nonprivileged cases: nonexistent role and read-only role evaluate to 'f'
#  10. Database name collision: preexisting database is NEVER dropped on failure
#  11. Rehearsal failure preserves encrypted recovery asset and key material
#  12. Full positive rehearsal execution via scripts/run-durability-rehearsal.sh
#  13. Asset preservation on success: approved backup and key are NEVER purged
#  14. Independent separate-process decryption proof using retained recovery key
#  15. Synthetic key workflow with explicit test boundaries
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

# -----------------------------------------------------------------------------
# Setup Simulated Production and Rehearsal Clusters
# -----------------------------------------------------------------------------
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

-- Roles for privilege boundary qualification:
-- 1. Role with write/create permissions on public schema
CREATE ROLE prod_write_operator LOGIN;
GRANT CREATE ON SCHEMA public TO prod_write_operator;

-- 2. Role with only read permissions (nonprivileged)
CREATE ROLE prod_read_only LOGIN;
GRANT USAGE ON SCHEMA public TO prod_read_only;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO prod_read_only;
EOF

echo "==> Initializing separate disposable rehearsal PostgreSQL instance (port 55471)..."
"$bindir/initdb" -D "$test_root/rehearsal_data" -A trust -U platen_rehearsal_operator --encoding=UTF8 >"$test_root/rehearsal_init.log" 2>&1
"$bindir/pg_ctl" -D "$test_root/rehearsal_data" -l "$test_root/rehearsal.log" -o "-h '' -k $test_root -p 55471 -F" -w start >/dev/null

REHEARSAL_MAINT_DSN="host=$test_root port=55471 user=platen_rehearsal_operator dbname=postgres sslmode=disable"

# Provision roles on rehearsal cluster for identity qualification:
psql -d "$REHEARSAL_MAINT_DSN" -v ON_ERROR_STOP=1 << 'EOF' >/dev/null
CREATE ROLE prod_write_operator LOGIN CREATEDB;
CREATE ROLE prod_read_only LOGIN;
EOF

# Pre-provision independent recovery key file (Separate Custody)
KEYS_DIR="$test_root/keys"
mkdir -p "$KEYS_DIR"
chmod 700 "$KEYS_DIR"
PROD_RECOVERY_KEY="$KEYS_DIR/pre_provisioned_recovery.key"
head -c 32 /dev/urandom | base64 > "$PROD_RECOVERY_KEY"
chmod 600 "$PROD_RECOVERY_KEY"

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
  BACKUP_PASSPHRASE_FILE="$PROD_RECOVERY_KEY" \
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
  BACKUP_PASSPHRASE_FILE="$PROD_RECOVERY_KEY" \
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
  BACKUP_PASSPHRASE_FILE="$PROD_RECOVERY_KEY" \
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
# NEGATIVE TEST 4: Separate Key Custody - Fail fast if BACKUP_PASSPHRASE_FILE is missing
# -----------------------------------------------------------------------------
echo "==> [Negative Test 4] Separate Key Custody (missing BACKUP_PASSPHRASE_FILE without synthetic flag)..."
set +e
output=$(env -i PATH="$PATH" \
  DATABASE_URL="$PROD_DSN" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55471" \
  REHEARSAL_PGUSER="platen_rehearsal_operator" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  "$repo_root/scripts/run-durability-rehearsal.sh" 2>&1)
rc=$?
set -e
if [ "$rc" -eq 0 ]; then
  echo "FAILED: Expected error on missing BACKUP_PASSPHRASE_FILE" >&2; exit 1
fi
echo "$output" | grep -q "BACKUP_PASSPHRASE_FILE must be set to a pre-provisioned, independently protected recovery key file" || {
  echo "FAILED: Output did not contain expected key custody error. Got: $output" >&2; exit 1
}
echo "PASSED: Missing BACKUP_PASSPHRASE_FILE rejected with code $rc."

# -----------------------------------------------------------------------------
# NEGATIVE TEST 5: Infrastructure identity collision detection
# -----------------------------------------------------------------------------
echo "==> [Negative Test 5] Infrastructure collision detection (targeting production cluster)..."
set +e
output=$(env -i PATH="$PATH" \
  DATABASE_URL="$PROD_DSN" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55470" \
  REHEARSAL_PGUSER="platen_prod" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  BACKUP_PASSPHRASE_FILE="$PROD_RECOVERY_KEY" \
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
# NEGATIVE TEST 6: Real Privilege Rejection (Role with Production Write Permissions)
# -----------------------------------------------------------------------------
echo "==> [Negative Test 6] Real privilege rejection: role with production write permissions..."
canary_priv_dir="$test_root/canary_priv_backup_dir"
mkdir -p "$canary_priv_dir"

set +e
output=$(env -i PATH="$PATH" \
  DATABASE_URL="$PROD_DSN" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55471" \
  REHEARSAL_PGUSER="prod_write_operator" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  BACKUP_PASSPHRASE_FILE="$PROD_RECOVERY_KEY" \
  BACKUP_DIR="$canary_priv_dir" \
  "$repo_root/scripts/run-durability-rehearsal.sh" 2>&1)
rc=$?
set -e
if [ "$rc" -ne 11 ]; then
  echo "FAILED: Expected exit code 11 for privileged rehearsal user, got $rc. Output: $output" >&2
  exit 1
fi
echo "$output" | grep -q "FATAL: Rehearsal user 'prod_write_operator' has write/create privileges on production database or cluster!" || {
  echo "FAILED: Output did not contain expected write privilege fatal message. Got: $output" >&2
  exit 1
}

# 1. Exact query verification: Direct query against production cluster returns 't'
direct_priv_val=$(psql --dbname="$PROD_DSN" -t -A -v ON_ERROR_STOP=1 -v ruser="prod_write_operator" << 'EOF'
SELECT CASE 
  WHEN NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'ruser') THEN 'f'
  WHEN (SELECT rolsuper FROM pg_roles WHERE rolname = :'ruser') THEN 't'
  WHEN has_database_privilege(:'ruser', current_database(), 'CREATE') THEN 't'
  WHEN has_schema_privilege(:'ruser', 'public', 'CREATE') THEN 't'
  ELSE 'f'
END;
EOF
)
if [ "$direct_priv_val" != "t" ]; then
  echo "FAILED: Direct privilege query did not return 't' for prod_write_operator! Got: $direct_priv_val" >&2
  exit 1
fi
echo "Exact privilege query output verified: '$direct_priv_val' (role has CREATE privileges on production)."

# 2. Pre-execution safety proof: NO backup was created
leaked_backups=$(find "$canary_priv_dir" -name "*.dump.gpg" 2>/dev/null || true)
if [ -n "$leaked_backups" ]; then
  echo "FAILED: Backup was created before privilege rejection! Found: $leaked_backups" >&2
  exit 1
fi

# 3. Pre-execution safety proof: NO rehearsal database was created on rehearsal cluster
leaked_dbs=$(psql -d "$REHEARSAL_MAINT_DSN" -t -A -c "SELECT count(*) FROM pg_database WHERE datname LIKE 'rehearsal_%';")
if [ "$leaked_dbs" -ne 0 ]; then
  echo "FAILED: Rehearsal database was created before privilege rejection!" >&2
  exit 1
fi
echo "PASSED: Privileged user rejected with code 11 before creating any backup or rehearsal database."

# -----------------------------------------------------------------------------
# NEGATIVE TEST 7: Privilege Rejection on Query Error (Never Fail Open)
# -----------------------------------------------------------------------------
echo "==> [Negative Test 7] Privilege query error rejection (fail-closed check)..."
set +e
output=$(env -i PATH="$PATH" \
  DATABASE_URL="$PROD_DSN" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55471" \
  REHEARSAL_PGUSER="platen_rehearsal_operator" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  BACKUP_PASSPHRASE_FILE="$PROD_RECOVERY_KEY" \
  PRIVILEGE_QUERY_OVERRIDE="SELECT 1/0;" \
  "$repo_root/scripts/run-durability-rehearsal.sh" 2>&1)
rc=$?
set -e
if [ "$rc" -ne 11 ]; then
  echo "FAILED: Expected exit code 11 on privilege query error, got $rc. Output: $output" >&2
  exit 1
fi
echo "$output" | grep -q "FATAL: Privilege query failed against production database" || {
  echo "FAILED: Output did not contain expected query failure message. Got: $output" >&2
  exit 1
}
echo "$output" | grep -q "Query errors cannot be treated as evidence of safety; aborting rehearsal." || {
  echo "FAILED: Output missing safety abort message. Got: $output" >&2
  exit 1
}
echo "PASSED: Privilege query error cleanly rejected with code 11 (fail-closed verified)."

# -----------------------------------------------------------------------------
# NEGATIVE TEST 8: Privilege Rejection on Malformed Result (Unknown States Rejected)
# -----------------------------------------------------------------------------
echo "==> [Negative Test 8] Privilege check malformed / unknown result rejection..."
set +e
output=$(env -i PATH="$PATH" \
  DATABASE_URL="$PROD_DSN" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55471" \
  REHEARSAL_PGUSER="platen_rehearsal_operator" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  BACKUP_PASSPHRASE_FILE="$PROD_RECOVERY_KEY" \
  PRIVILEGE_QUERY_OVERRIDE="SELECT 'malformed_state';" \
  "$repo_root/scripts/run-durability-rehearsal.sh" 2>&1)
rc=$?
set -e
if [ "$rc" -ne 11 ]; then
  echo "FAILED: Expected exit code 11 on malformed privilege result, got $rc. Output: $output" >&2
  exit 1
fi
echo "$output" | grep -q "FATAL: Privilege check returned unexpected/malformed result: 'malformed_state'" || {
  echo "FAILED: Output did not contain expected malformed result message. Got: $output" >&2
  exit 1
}
echo "PASSED: Malformed privilege state cleanly rejected with code 11."

# -----------------------------------------------------------------------------
# TEST 9: Valid Nonprivileged Cases Direct Verification
# -----------------------------------------------------------------------------
echo "==> [Test 9] Valid nonprivileged cases direct verification..."
# Case 9A: Nonexistent role on production
nonexistent_val=$(psql --dbname="$PROD_DSN" -t -A -v ON_ERROR_STOP=1 -v ruser="platen_rehearsal_operator" << 'EOF'
SELECT CASE 
  WHEN NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'ruser') THEN 'f'
  WHEN (SELECT rolsuper FROM pg_roles WHERE rolname = :'ruser') THEN 't'
  WHEN has_database_privilege(:'ruser', current_database(), 'CREATE') THEN 't'
  WHEN has_schema_privilege(:'ruser', 'public', 'CREATE') THEN 't'
  ELSE 'f'
END;
EOF
)
if [ "$nonexistent_val" != "f" ]; then
  echo "FAILED: Nonexistent role must return 'f'! Got: $nonexistent_val" >&2
  exit 1
fi
echo "Verified Case 9A: Nonexistent role on production returns exact value: '$nonexistent_val'."

# Case 9B: Existing role with read-only privileges on production
readonly_val=$(psql --dbname="$PROD_DSN" -t -A -v ON_ERROR_STOP=1 -v ruser="prod_read_only" << 'EOF'
SELECT CASE 
  WHEN NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'ruser') THEN 'f'
  WHEN (SELECT rolsuper FROM pg_roles WHERE rolname = :'ruser') THEN 't'
  WHEN has_database_privilege(:'ruser', current_database(), 'CREATE') THEN 't'
  WHEN has_schema_privilege(:'ruser', 'public', 'CREATE') THEN 't'
  ELSE 'f'
END;
EOF
)
if [ "$readonly_val" != "f" ]; then
  echo "FAILED: Read-only role must return 'f'! Got: $readonly_val" >&2
  exit 1
fi
echo "Verified Case 9B: Read-only role on production returns exact value: '$readonly_val'."
echo "PASSED: Valid nonprivileged cases confirmed."

# -----------------------------------------------------------------------------
# NEGATIVE TEST 10: Database Name Collision & Preexisting Database Preservation
# -----------------------------------------------------------------------------
echo "==> [Negative Test 10] Preexisting database collision safety..."
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
  BACKUP_PASSPHRASE_FILE="$PROD_RECOVERY_KEY" \
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
"$bindir/dropdb" -h "$test_root" -p 55471 -U platen_rehearsal_operator preexisting_canary_db

# -----------------------------------------------------------------------------
# NEGATIVE TEST 11: External BACKUP_DIR & Backup Preservation on Rehearsal Failure
# -----------------------------------------------------------------------------
echo "==> [Negative Test 11] External BACKUP_DIR and backup asset preservation across failure..."
external_dir="$test_root/external_operator_dir"
mkdir -p "$external_dir"
echo "unrelated_operator_file_contents" > "$external_dir/unrelated_operator_note.txt"

faulty_migration="$test_root/faulty_migration.sql"
echo "SYNTAX ERROR IN MIGRATION SCRIPT;" > "$faulty_migration"

set +e
output=$(env -i PATH="$PATH" \
  DATABASE_URL="$PROD_DSN" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55471" \
  REHEARSAL_PGUSER="platen_rehearsal_operator" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  BACKUP_PASSPHRASE_FILE="$PROD_RECOVERY_KEY" \
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
if [ -z "$preserved_dump" ] || [ ! -s "$preserved_dump" ]; then
  echo "FAILED: Production encrypted backup was deleted on rehearsal failure!" >&2
  exit 1
fi
if [ ! -s "$PROD_RECOVERY_KEY" ]; then
  echo "FAILED: Pre-provisioned recovery key was deleted on rehearsal failure!" >&2
  exit 1
fi
echo "PASSED: External directory and recovery assets ($preserved_dump, $PROD_RECOVERY_KEY) preserved on rehearsal failure."

# -----------------------------------------------------------------------------
# POSITIVE TEST 12: Full End-to-End Rehearsal Execution via run-durability-rehearsal.sh
# -----------------------------------------------------------------------------
echo "==> [Positive Test 12] Full end-to-end execution of canonical rehearsal script..."
positive_backup_dir="$test_root/positive_backups"

output=$(env -i PATH="$PATH" \
  DATABASE_URL="$PROD_DSN" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55471" \
  REHEARSAL_PGUSER="platen_rehearsal_operator" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  BACKUP_PASSPHRASE_FILE="$PROD_RECOVERY_KEY" \
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
# POSITIVE TEST 13: Recovery Asset Retention on Success (No Automatic Purging)
# -----------------------------------------------------------------------------
echo "==> [Positive Test 13] Verifying recovery assets are retained on success (no automated purge)..."
prod_dump_gpg=$(find "$positive_backup_dir" -name "*.dump.gpg" | head -n 1)

if [ -z "$prod_dump_gpg" ] || [ ! -s "$prod_dump_gpg" ]; then
  echo "FAILED: Encrypted backup was purged or missing after successful rehearsal!" >&2
  exit 1
fi
if [ ! -s "$PROD_RECOVERY_KEY" ]; then
  echo "FAILED: Pre-provisioned recovery key was purged or missing after successful rehearsal!" >&2
  exit 1
fi
echo "PASSED: Recovery assets ($prod_dump_gpg and $PROD_RECOVERY_KEY) preserved on successful rehearsal."

# -----------------------------------------------------------------------------
# POSITIVE TEST 14: Independent Separate-Process Decryption Proof
# -----------------------------------------------------------------------------
echo "==> [Positive Test 14] Verifying decryption in an independent process using retained recovery key..."
fresh_toc="$test_root/fresh_process_decrypted.toc"

# Run in scrubbed environment with zero ambient secrets
env -i PATH="$PATH" \
  gpg --batch --quiet --pinentry-mode loopback --decrypt \
      --passphrase-file "$PROD_RECOVERY_KEY" "$prod_dump_gpg" | \
      pg_restore --list > "$fresh_toc"

grep -q "TABLE DATA public users" "$fresh_toc" || {
  echo "FAILED: Fresh process decrypted TOC missing users table data!" >&2; exit 1
}
grep -q "TABLE DATA public studio_jobs" "$fresh_toc" || {
  echo "FAILED: Fresh process decrypted TOC missing studio_jobs table data!" >&2; exit 1
}
echo "PASSED: Retained recovery key cleanly decrypted backup in independent fresh process."

# -----------------------------------------------------------------------------
# POSITIVE TEST 15: Synthetic Test Key Workflow with Explicit Boundaries
# -----------------------------------------------------------------------------
echo "==> [Positive Test 15] Synthetic test key workflow with explicit test boundaries..."
synthetic_backup_dir="$test_root/synthetic_key_backups"

output=$(env -i PATH="$PATH" \
  DATABASE_URL="$PROD_DSN" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55471" \
  REHEARSAL_PGUSER="platen_rehearsal_operator" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  ALLOW_SYNTHETIC_TEST_KEY="true" \
  BACKUP_DIR="$synthetic_backup_dir" \
  "$repo_root/scripts/run-durability-rehearsal.sh" 2>&1)

echo "$output" | grep -q "WARNING: ALLOW_SYNTHETIC_TEST_KEY=true is set. Generating synthetic test key." || {
  echo "FAILED: Output missing synthetic test key warning. Got: $output" >&2
  exit 1
}
echo "$output" | grep -q "WARNING: This synthetic key workflow is strictly restricted to automated CI/test environments and is FORBIDDEN in production." || {
  echo "FAILED: Output missing production prohibition warning. Got: $output" >&2
  exit 1
}
echo "$output" | grep -q "Disposable rehearsal restore and migration dry run successfully completed!" || {
  echo "FAILED: Synthetic key rehearsal did not complete successfully. Got: $output" >&2
  exit 1
}

# Verify synthetic key was generated into synthetic_test_keys subfolder
synth_key=$(find "$synthetic_backup_dir/synthetic_test_keys" -name "*.key" | head -n 1)
if [ -z "$synth_key" ] || [ ! -s "$synth_key" ]; then
  echo "FAILED: Synthetic key was not generated in synthetic_test_keys subfolder!" >&2
  exit 1
fi
echo "PASSED: Synthetic key workflow operates cleanly with explicit boundaries and warnings."

echo ""
echo "================================================================================"
echo "ALL REHEARSAL RUNBOOK QUALIFICATION CHECKS (15/15) PASSED SUCCESSFULLY!"
echo "================================================================================"
