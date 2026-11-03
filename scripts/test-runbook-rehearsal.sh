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
#   4. Separate key custody: missing BACKUP_PASSPHRASE_FILE fails fast
#   5. Synthetic-key bypass unavailable: ALLOW_SYNTHETIC_TEST_KEY is ignored
#   6. Infrastructure identity collision detection refuses execution
#   7. Real privilege rejection: role with production write privileges is rejected
#      before creating any rehearsal database or backup
#   8. Malicious inherited privilege override cannot bypass enforcement or execute arbitrary SQL
#   9. Privilege query error rejection: query errors fail closed via test-harness injection
#  10. Malformed privilege result rejection: unknown states fail closed via test-harness injection
#  11. Valid nonprivileged cases: nonexistent role and read-only role evaluate to 'f'
#  12. Database name collision: preexisting database is NEVER dropped on failure
#  13. Rehearsal failure preserves encrypted recovery asset and key material
#  14. Full positive rehearsal execution with authenticated rehearsal credentials
#  15. Credentials safety: passwords never appear in command arguments, logs, or persistent files
#  16. Asset preservation on success: approved backup and key are NEVER purged
#  17. Independent separate-process decryption proof using retained recovery key
#  18. Fail-closed teardown: dropdb blocked by an active session forces a nonzero
#      result, containment warning, PGPASSFILE removal and asset preservation
#  19. Fail-closed teardown: dropdb falsely reporting success is caught by the
#      executable's independent absence verification
#  20. Successful teardown confirms database absence and exits successfully
#  21. Production credential safety: authenticated connection with canary password (URI format)
#  22. Production wrong-password rejection & credential-file cleanup
#  23. Production credential boundary across failed rehearsal teardown (exit 21 + containment)
#  24. Production credential safety with keyword/value DSN format
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
  # Terminate any harness-owned teardown connection holder before stopping clusters
  if [ -f "$test_root/teardown_holder.pid" ]; then
    kill "$(cat "$test_root/teardown_holder.pid")" 2>/dev/null || true
  fi
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

# Require password authentication specifically for platen_auth_prod_operator
echo "local all platen_auth_prod_operator md5" | cat - "$test_root/prod_data/pg_hba.conf" > "$test_root/prod_data/pg_hba.conf.tmp"
mv "$test_root/prod_data/pg_hba.conf.tmp" "$test_root/prod_data/pg_hba.conf"

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

-- Roles for privilege boundary qualification on production:
-- 1. Role with write/create permissions on public schema
CREATE ROLE prod_write_operator LOGIN;
GRANT CREATE ON SCHEMA public TO prod_write_operator;

-- 2. Role with only read permissions (nonprivileged)
CREATE ROLE prod_read_only LOGIN;
GRANT USAGE ON SCHEMA public TO prod_read_only;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO prod_read_only;

-- 3. Authenticated production operator with distinct canary password
CREATE ROLE platen_auth_prod_operator LOGIN PASSWORD 'CanaryProdSecret_88412!' SUPERUSER;
EOF

CANARY_PROD_PASSWORD="CanaryProdSecret_88412!"
PROD_AUTH_URL="postgresql://platen_auth_prod_operator:${CANARY_PROD_PASSWORD}@/pdfnest_production?host=$test_root&port=55470&sslmode=disable"

echo "==> Initializing separate disposable rehearsal PostgreSQL instance (port 55471)..."
"$bindir/initdb" -D "$test_root/rehearsal_data" -A trust -U platen_rehearsal_operator --encoding=UTF8 >"$test_root/rehearsal_init.log" 2>&1

# Require password authentication specifically for platen_auth_rehearsal_operator
echo "local all platen_auth_rehearsal_operator md5" | cat - "$test_root/rehearsal_data/pg_hba.conf" > "$test_root/rehearsal_data/pg_hba.conf.tmp"
mv "$test_root/rehearsal_data/pg_hba.conf.tmp" "$test_root/rehearsal_data/pg_hba.conf"

"$bindir/pg_ctl" -D "$test_root/rehearsal_data" -l "$test_root/rehearsal.log" -o "-h '' -k $test_root -p 55471 -F" -w start >/dev/null

REHEARSAL_MAINT_DSN="host=$test_root port=55471 user=platen_rehearsal_operator dbname=postgres sslmode=disable"

# Provision roles on rehearsal cluster for identity qualification:
psql -d "$REHEARSAL_MAINT_DSN" -v ON_ERROR_STOP=1 << 'EOF' >/dev/null
CREATE ROLE prod_write_operator LOGIN CREATEDB;
CREATE ROLE prod_read_only LOGIN;
-- Authenticated role with password for credential safety verification
CREATE ROLE platen_auth_rehearsal_operator LOGIN PASSWORD 'SecretRehearsalPass_12345!' CREATEDB;
EOF

# Pre-provision independent recovery key file (Separate Custody)
KEYS_DIR="$test_root/keys"
mkdir -p "$KEYS_DIR"
chmod 700 "$KEYS_DIR"
PROD_RECOVERY_KEY="$KEYS_DIR/pre_provisioned_recovery.key"
head -c 32 /dev/urandom | base64 > "$PROD_RECOVERY_KEY"
chmod 600 "$PROD_RECOVERY_KEY"

# Setup harness-controlled psql failure injection & argument assertion wrapper
harness_bin="$test_root/harness_bin"
mkdir -p "$harness_bin"
real_psql="$bindir/psql"
cat > "$harness_bin/psql" << 'EOF'
#!/usr/bin/env bash
if [ -n "${TEST_ASSERT_NO_SECRETS:-}" ]; then
  for arg in "$@"; do
    if [[ "$arg" == *"CanaryProdSecret"* ]] || [[ "$arg" == *"SecretRehearsalPass"* ]] || [[ "$arg" == *"password="* ]]; then
      echo "FATAL HARNESS ERROR: Secret leaked in psql argument: $arg" >&2
      exit 99
    fi
  done
fi
if [ -n "${TEST_INJECT_PSQL_FAILURE:-}" ]; then
  # Detect if this invocation is the privilege check (inspects parameters)
  if [[ "$*" == *"-v ruser="* ]]; then
    if [ "$TEST_INJECT_PSQL_FAILURE" = "query_error" ]; then
      echo "psql: error: connection reset by peer during privilege introspection" >&2
      exit 2
    elif [ "$TEST_INJECT_PSQL_FAILURE" = "malformed" ]; then
      echo "malformed_unknown_state"
      exit 0
    fi
  fi
fi
real_bin="$(cat "$0.real_bin")"
exec "$real_bin" "$@"
EOF
echo "$real_psql" > "$harness_bin/psql.real_bin"
chmod +x "$harness_bin/psql"

# Setup harness-controlled pg_dump argument assertion wrapper
cat > "$harness_bin/pg_dump" << 'EOF'
#!/usr/bin/env bash
if [ -n "${TEST_ASSERT_NO_SECRETS:-}" ]; then
  for arg in "$@"; do
    if [[ "$arg" == *"CanaryProdSecret"* ]] || [[ "$arg" == *"SecretRehearsalPass"* ]] || [[ "$arg" == *"password="* ]]; then
      echo "FATAL HARNESS ERROR: Secret leaked in pg_dump argument: $arg" >&2
      exit 99
    fi
  done
fi
real_bin="$(cat "$0.real_bin")"
exec "$real_bin" "$@"
EOF
echo "$bindir/pg_dump" > "$harness_bin/pg_dump.real_bin"
chmod +x "$harness_bin/pg_dump"

# Setup harness-controlled dropdb teardown fault-injection wrapper.
# Passthrough unless TEST_INJECT_DROPDB is set; only placed on PATH by teardown tests.
#   hold_connection: opens a real session on the target database and waits until
#                    PostgreSQL reports it active, then runs the real dropdb (which
#                    genuinely fails because the database is being accessed).
#   false_success:   reports success without removing anything, proving that the
#                    canonical executable independently verifies database absence.
cat > "$harness_bin/dropdb" << 'EOF'
#!/usr/bin/env bash
real_bin="$(cat "$0.real_bin")"
real_psql="$(cat "$(dirname "$0")/psql.real_bin")"
target_db="${*: -1}"
case "${TEST_INJECT_DROPDB:-}" in
  hold_connection)
    "$real_psql" -X -h "$TEST_HOLDER_HOST" -p "$TEST_HOLDER_PORT" -U "$TEST_HOLDER_USER" -d "$target_db" \
      -c "SELECT pg_sleep(600);" < /dev/null > "$TEST_HOLDER_LOG" 2>&1 &
    echo "$!" > "$TEST_HOLDER_PID_FILE"
    for _ in $(seq 1 100); do
      held=$("$real_psql" -X -h "$TEST_HOLDER_HOST" -p "$TEST_HOLDER_PORT" -U "$TEST_HOLDER_USER" -d postgres -At \
        -c "SELECT count(*) FROM pg_stat_activity WHERE datname = '$target_db' AND query LIKE 'SELECT pg_sleep(600)%';" 2>/dev/null)
      [ "$held" = "1" ] && break
      sleep 0.1
    done
    ;;
  false_success)
    exit 0
    ;;
esac
exec "$real_bin" "$@"
EOF
echo "$bindir/dropdb" > "$harness_bin/dropdb.real_bin"
chmod +x "$harness_bin/dropdb"

# Independent (harness-side) catalog check on the rehearsal cluster
rehearsal_db_count() {
  "$bindir/psql" -X -h "$test_root" -p 55471 -U platen_rehearsal_operator -d postgres -At \
    -v target_db="$1" << 'EOF'
SELECT count(*) FROM pg_database WHERE datname = :'target_db';
EOF
}

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
echo "==> [Negative Test 4] Separate Key Custody (missing BACKUP_PASSPHRASE_FILE fails fast)..."
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
if [ "$rc" -ne 13 ]; then
  echo "FAILED: Expected exit code 13 on missing BACKUP_PASSPHRASE_FILE, got $rc" >&2; exit 1
fi
echo "$output" | grep -q "BACKUP_PASSPHRASE_FILE must be set to a pre-provisioned, independently protected recovery key file" || {
  echo "FAILED: Output did not contain expected key custody error. Got: $output" >&2; exit 1
}
echo "PASSED: Missing BACKUP_PASSPHRASE_FILE rejected with code 13."

# -----------------------------------------------------------------------------
# NEGATIVE TEST 5: Synthetic-Key Bypass Unavailable in Production Executable
# -----------------------------------------------------------------------------
echo "==> [Negative Test 5] Verifying synthetic-key bypass is completely unavailable..."
set +e
output=$(env -i PATH="$PATH" \
  DATABASE_URL="$PROD_DSN" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55471" \
  REHEARSAL_PGUSER="platen_rehearsal_operator" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  ALLOW_SYNTHETIC_TEST_KEY="true" \
  "$repo_root/scripts/run-durability-rehearsal.sh" 2>&1)
rc=$?
set -e
if [ "$rc" -ne 13 ]; then
  echo "FAILED: Expected exit code 13 when trying to bypass key custody with ALLOW_SYNTHETIC_TEST_KEY, got $rc" >&2
  exit 1
fi
echo "$output" | grep -q "BACKUP_PASSPHRASE_FILE must be set to a pre-provisioned, independently protected recovery key file" || {
  echo "FAILED: Script did not fail closed on key custody. Got: $output" >&2; exit 1
}
# Confirm no synthetic keys were generated
if [ -d "$test_root/synthetic_test_keys" ]; then
  echo "FAILED: Synthetic keys folder was created!" >&2; exit 1
fi
echo "PASSED: Synthetic-key bypass rejected with code 13; key custody remains strictly enforced."

# -----------------------------------------------------------------------------
# NEGATIVE TEST 6: Infrastructure identity collision detection
# -----------------------------------------------------------------------------
echo "==> [Negative Test 6] Infrastructure collision detection (targeting production cluster)..."
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
# NEGATIVE TEST 7: Real Privilege Rejection (Role with Production Write Permissions)
# -----------------------------------------------------------------------------
echo "==> [Negative Test 7] Real privilege rejection: role with production write permissions..."
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
# NEGATIVE TEST 8: Malicious Inherited Privilege Override Blocked & No Arbitrary SQL
# -----------------------------------------------------------------------------
echo "==> [Negative Test 8] Malicious inherited privilege override cannot bypass enforcement or execute arbitrary SQL..."
set +e
# Attempt 8A: Attempting to bypass enforcement by setting PRIVILEGE_QUERY_OVERRIDE="SELECT 'f';"
output_8a=$(env -i PATH="$PATH" \
  DATABASE_URL="$PROD_DSN" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55471" \
  REHEARSAL_PGUSER="prod_write_operator" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  BACKUP_PASSPHRASE_FILE="$PROD_RECOVERY_KEY" \
  PRIVILEGE_QUERY_OVERRIDE="SELECT 'f';" \
  "$repo_root/scripts/run-durability-rehearsal.sh" 2>&1)
rc_8a=$?
set -e
if [ "$rc_8a" -ne 11 ]; then
  echo "FAILED: PRIVILEGE_QUERY_OVERRIDE bypassed privilege enforcement! Code: $rc_8a. Output: $output_8a" >&2
  exit 1
fi
echo "Verified 8A: Inherited override was ignored; privileged role was strictly rejected with code 11."

set +e
# Attempt 8B: Attempting arbitrary write SQL injection via PRIVILEGE_QUERY_OVERRIDE
output_8b=$(env -i PATH="$PATH" \
  DATABASE_URL="$PROD_DSN" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55471" \
  REHEARSAL_PGUSER="platen_rehearsal_operator" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  BACKUP_PASSPHRASE_FILE="$PROD_RECOVERY_KEY" \
  PRIVILEGE_QUERY_OVERRIDE="CREATE TABLE public.injected_by_override (id int);" \
  "$repo_root/scripts/run-durability-rehearsal.sh" 2>&1)
set -e
# Check that injected table was NOT created on production
table_exists=$(psql --dbname="$PROD_DSN" -t -A -c "SELECT to_regclass('public.injected_by_override')::text;")
if [ -n "$table_exists" ]; then
  echo "FAILED: Arbitrary SQL injection executed on production cluster! Table created: $table_exists" >&2
  exit 1
fi
echo "Verified 8B: Inherited override was not executed; zero arbitrary SQL executed on production."
echo "PASSED: Privilege override completely neutralized."

# -----------------------------------------------------------------------------
# NEGATIVE TEST 9: Privilege Query Error Rejection (Fail-Closed via Harness Injection)
# -----------------------------------------------------------------------------
echo "==> [Negative Test 9] Privilege query error rejection (fail-closed via harness injection)..."
set +e
output=$(env -i PATH="$harness_bin:$PATH" \
  DATABASE_URL="$PROD_DSN" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55471" \
  REHEARSAL_PGUSER="platen_rehearsal_operator" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  BACKUP_PASSPHRASE_FILE="$PROD_RECOVERY_KEY" \
  TEST_INJECT_PSQL_FAILURE="query_error" \
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
# NEGATIVE TEST 10: Privilege Check Malformed Result Rejection (Harness Injection)
# -----------------------------------------------------------------------------
echo "==> [Negative Test 10] Privilege check malformed / unknown result rejection..."
set +e
output=$(env -i PATH="$harness_bin:$PATH" \
  DATABASE_URL="$PROD_DSN" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55471" \
  REHEARSAL_PGUSER="platen_rehearsal_operator" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  BACKUP_PASSPHRASE_FILE="$PROD_RECOVERY_KEY" \
  TEST_INJECT_PSQL_FAILURE="malformed" \
  "$repo_root/scripts/run-durability-rehearsal.sh" 2>&1)
rc=$?
set -e
if [ "$rc" -ne 11 ]; then
  echo "FAILED: Expected exit code 11 on malformed privilege result, got $rc. Output: $output" >&2
  exit 1
fi
echo "$output" | grep -q "FATAL: Privilege check returned unexpected/malformed result: 'malformed_unknown_state'" || {
  echo "FAILED: Output did not contain expected malformed result message. Got: $output" >&2
  exit 1
}
echo "PASSED: Malformed privilege state cleanly rejected with code 11."

# -----------------------------------------------------------------------------
# TEST 11: Valid Nonprivileged Cases Direct Verification
# -----------------------------------------------------------------------------
echo "==> [Test 11] Valid nonprivileged cases direct verification..."
# Case 11A: Nonexistent role on production
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
echo "Verified Case 11A: Nonexistent role on production returns exact value: '$nonexistent_val'."

# Case 11B: Existing role with read-only privileges on production
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
echo "Verified Case 11B: Read-only role on production returns exact value: '$readonly_val'."
echo "PASSED: Valid nonprivileged cases confirmed."

# -----------------------------------------------------------------------------
# NEGATIVE TEST 12: Database Name Collision & Preexisting Database Preservation
# -----------------------------------------------------------------------------
echo "==> [Negative Test 12] Preexisting database collision safety..."
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
# NEGATIVE TEST 13: External BACKUP_DIR & Backup Preservation on Rehearsal Failure
# -----------------------------------------------------------------------------
echo "==> [Negative Test 13] External BACKUP_DIR and backup asset preservation across failure..."
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
# POSITIVE TEST 14: Full Rehearsal Execution with Authenticated Credentials
# -----------------------------------------------------------------------------
echo "==> [Positive Test 14] Full end-to-end rehearsal execution with authenticated credentials..."
positive_backup_dir="$test_root/positive_backups"

output=$(env -i PATH="$PATH" \
  DATABASE_URL="$PROD_DSN" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55471" \
  REHEARSAL_PGUSER="platen_auth_rehearsal_operator" \
  REHEARSAL_PGPASSWORD="SecretRehearsalPass_12345!" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  BACKUP_PASSPHRASE_FILE="$PROD_RECOVERY_KEY" \
  BACKUP_DIR="$positive_backup_dir" \
  "$repo_root/scripts/run-durability-rehearsal.sh" 2>&1)

echo "$output" | grep -q "All 12 durability indexes verified with exact table, name, uniqueness, primary-key, and validity properties." || {
  echo "FAILED: Positive rehearsal output missing index verification confirmation. Got: $output" >&2
  exit 1
}
echo "$output" | grep -q "Disposable rehearsal restore and migration dry run successfully completed!" || {
  echo "FAILED: Positive rehearsal output missing completion confirmation." >&2
  exit 1
}
echo "$output" | grep -q "Teardown verified: rehearsal database '.*' no longer exists on the rehearsal target." || {
  echo "FAILED: Positive rehearsal output missing verified teardown confirmation. Got: $output" >&2
  exit 1
}
echo "PASSED: Rehearsal executed cleanly using password-authenticated role via PGPASSFILE."

# -----------------------------------------------------------------------------
# POSITIVE TEST 15: Credential Safety (Zero Password Leakage in Output or Commands)
# -----------------------------------------------------------------------------
echo "==> [Positive Test 15] Verifying database credentials never appear in command arguments or logs..."
# 1. Assert plain password never leaked in output
if echo "$output" | grep -q "SecretRehearsalPass_12345!"; then
  echo "FAILED: Password string was leaked in script output!" >&2
  exit 1
fi
# 2. Assert password= never leaked in output
if echo "$output" | grep -q "password="; then
  echo "FAILED: DSN password parameter was printed in script output!" >&2
  exit 1
fi
# 3. Assert temporary managed PGPASSFILE was cleaned up on exit
leaked_pgpass=$(ls /tmp/platen-rehearsal-pgpass-* 2>/dev/null || true)
if [ -n "$leaked_pgpass" ]; then
  echo "FAILED: Managed PGPASSFILE was not cleaned up after execution! Found: $leaked_pgpass" >&2
  exit 1
fi
# 4. Assert wrong password fails authentication
set +e
wrong_pw_output=$(env -i PATH="$PATH" \
  DATABASE_URL="$PROD_DSN" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55471" \
  REHEARSAL_PGUSER="platen_auth_rehearsal_operator" \
  REHEARSAL_PGPASSWORD="WrongPassword_Invalid!" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  BACKUP_PASSPHRASE_FILE="$PROD_RECOVERY_KEY" \
  "$repo_root/scripts/run-durability-rehearsal.sh" 2>&1)
wrong_pw_rc=$?
set -e
if [ "$wrong_pw_rc" -eq 0 ]; then
  echo "FAILED: Execution succeeded with wrong password!" >&2; exit 1
fi
echo "$wrong_pw_output" | grep -q "Failed to connect to rehearsal maintenance instance" || {
  echo "FAILED: Wrong password did not fail connection as expected. Got: $wrong_pw_output" >&2; exit 1
}
# 5. Assert managed PGPASSFILE was cleaned up on exit even on failure
leaked_pgpass_after_err=$(ls /tmp/platen-rehearsal-pgpass-* 2>/dev/null || true)
if [ -n "$leaked_pgpass_after_err" ]; then
  echo "FAILED: Managed PGPASSFILE was not cleaned up after failed execution! Found: $leaked_pgpass_after_err" >&2
  exit 1
fi
echo "PASSED: Credentials safety verified (0 secrets leaked, PGPASSFILE cleaned on success and failure, wrong credentials rejected)."

# -----------------------------------------------------------------------------
# POSITIVE TEST 16: Recovery Asset Retention on Success (No Automatic Purging)
# -----------------------------------------------------------------------------
echo "==> [Positive Test 16] Verifying recovery assets are retained on success (no automated purge)..."
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
# POSITIVE TEST 17: Independent Separate-Process Decryption Proof
# -----------------------------------------------------------------------------
echo "==> [Positive Test 17] Verifying decryption in an independent process using retained recovery key..."
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
# Shared fixtures for teardown tests 18-20
# -----------------------------------------------------------------------------
# Unrelated operator database on the rehearsal cluster that must survive every teardown path
"$bindir/createdb" -h "$test_root" -p 55471 -U platen_rehearsal_operator unrelated_operator_canary_db
psql -X -h "$test_root" -p 55471 -U platen_rehearsal_operator -d unrelated_operator_canary_db -v ON_ERROR_STOP=1 \
  -c "CREATE TABLE canary_data (val text PRIMARY KEY); INSERT INTO canary_data VALUES ('unrelated_operator_record');" >/dev/null

assert_unrelated_databases_intact() {
  local canary
  canary=$("$bindir/psql" -X -h "$test_root" -p 55471 -U platen_rehearsal_operator -d unrelated_operator_canary_db -At \
    -c "SELECT val FROM canary_data;" 2>/dev/null || echo "MISSING")
  if [ "$canary" != "unrelated_operator_record" ]; then
    echo "FAILED: Unrelated operator database was deleted or modified during teardown! Value: $canary" >&2
    exit 1
  fi
  if [ "$(rehearsal_db_count postgres)" != "1" ]; then
    echo "FAILED: Maintenance database 'postgres' is missing on the rehearsal cluster!" >&2
    exit 1
  fi
}

assert_no_managed_pgpassfile() {
  local leaked
  leaked=$(ls /tmp/platen-rehearsal-pgpass-* 2>/dev/null || true)
  if [ -n "$leaked" ]; then
    echo "FAILED: Managed PGPASSFILE was not removed after teardown failure! Found: $leaked" >&2
    exit 1
  fi
}

assert_recovery_assets_protected() {
  local backup_dir="$1" dump
  dump=$(find "$backup_dir" -name "*.dump.gpg" | head -n 1)
  if [ -z "$dump" ] || [ ! -s "$dump" ] || [ "$(stat -c %a "$dump")" != "600" ]; then
    echo "FAILED: Encrypted recovery backup missing, empty, or not mode 0600 in $backup_dir!" >&2
    exit 1
  fi
  if [ ! -s "$PROD_RECOVERY_KEY" ] || [ "$(stat -c %a "$PROD_RECOVERY_KEY")" != "600" ]; then
    echo "FAILED: Recovery key missing, empty, or not mode 0600!" >&2
    exit 1
  fi
  env -i PATH="$PATH" gpg --batch --quiet --pinentry-mode loopback --decrypt \
    --passphrase-file "$PROD_RECOVERY_KEY" "$dump" | pg_restore --list | grep -q "TABLE DATA public users" || {
    echo "FAILED: Retained recovery backup in $backup_dir could not be decrypted with retained key!" >&2
    exit 1
  }
}

# -----------------------------------------------------------------------------
# NEGATIVE TEST 18: Teardown blocked by an active session fails closed
# -----------------------------------------------------------------------------
echo "==> [Negative Test 18] Fail-closed teardown: rehearsal database removal blocked by an active session..."
teardown_db="teardown_failure_probe_db"
teardown_backup_dir="$test_root/teardown_failure_backups"
holder_pid_file="$test_root/teardown_holder.pid"

set +e
output=$(env -i PATH="$harness_bin:$PATH" \
  DATABASE_URL="$PROD_DSN" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55471" \
  REHEARSAL_PGUSER="platen_auth_rehearsal_operator" \
  REHEARSAL_PGPASSWORD="SecretRehearsalPass_12345!" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  BACKUP_PASSPHRASE_FILE="$PROD_RECOVERY_KEY" \
  BACKUP_DIR="$teardown_backup_dir" \
  REHEARSAL_DB="$teardown_db" \
  TEST_INJECT_DROPDB="hold_connection" \
  TEST_HOLDER_HOST="$test_root" \
  TEST_HOLDER_PORT="55471" \
  TEST_HOLDER_USER="platen_rehearsal_operator" \
  TEST_HOLDER_PID_FILE="$holder_pid_file" \
  TEST_HOLDER_LOG="$test_root/teardown_holder.log" \
  "$repo_root/scripts/run-durability-rehearsal.sh" 2>&1)
rc=$?
set -e

echo "---- Test 18 evidence: canonical executable teardown output (exit $rc) ----"
echo "$output" | sed -n '/Rehearsal cleanup initiated/,$p'
echo "---- end Test 18 evidence ----"

# 1. Restore and migration checks passed before teardown (failure is purely teardown)
echo "$output" | grep -q "Disposable rehearsal restore and migration dry run successfully completed!" || {
  echo "FAILED: Test 18 precondition: restore/migration checks did not pass. Got: $output" >&2; exit 1
}
# 2. Overall result is nonzero with the dedicated teardown failure status
if [ "$rc" -ne 21 ]; then
  echo "FAILED: Expected exit code 21 on teardown failure, got $rc. Output: $output" >&2; exit 1
fi
# 3. Canonical executable detected the real PostgreSQL failure
echo "$output" | grep -q "ERROR: dropdb failed for rehearsal database '$teardown_db'" || {
  echo "FAILED: dropdb failure was not reported. Got: $output" >&2; exit 1
}
echo "$output" | grep -q "is being accessed by other users" || {
  echo "FAILED: Expected genuine PostgreSQL active-session refusal. Got: $output" >&2; exit 1
}
# 4. Clear containment/remediation warning with remaining database and target identity
for expected in "CONTAINMENT REQUIRED: REHEARSAL DATABASE TEARDOWN FAILED" \
                "Remaining database : $teardown_db" \
                "Rehearsal target   : host=$test_root port=55471 user=platen_auth_rehearsal_operator" \
                "Cluster identity   : system_identifier=" \
                "REHEARSAL FAILED (exit 21)"; do
  echo "$output" | grep -qF "$expected" || {
    echo "FAILED: Containment warning missing '$expected'. Got: $output" >&2; exit 1
  }
done
if echo "$output" | grep -qE "Teardown verified|REHEARSAL SUCCEEDED"; then
  echo "FAILED: Teardown failure was misreported as success! Got: $output" >&2; exit 1
fi
# 5. Harness independently confirms the reported database really remains
if [ "$(rehearsal_db_count "$teardown_db")" != "1" ]; then
  echo "FAILED: Harness could not independently confirm the remaining rehearsal database." >&2; exit 1
fi
# 6. No unrelated database deleted; recovery assets protected; PGPASSFILE removed
assert_unrelated_databases_intact
assert_recovery_assets_protected "$teardown_backup_dir"
assert_no_managed_pgpassfile
if echo "$output" | grep -q "SecretRehearsalPass_12345!"; then
  echo "FAILED: Rehearsal password appeared in captured teardown-failure output!" >&2; exit 1
fi
# 7. Harness containment of the synthetic fixture (harness-only superuser termination;
#    the canonical executable never terminates sessions or uses --force)
"$bindir/psql" -X -h "$test_root" -p 55471 -U platen_rehearsal_operator -d postgres -At \
  -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$teardown_db';" >/dev/null
kill "$(cat "$holder_pid_file")" 2>/dev/null || true
for _ in $(seq 1 50); do
  active=$("$bindir/psql" -X -h "$test_root" -p 55471 -U platen_rehearsal_operator -d postgres -At \
    -c "SELECT count(*) FROM pg_stat_activity WHERE datname = '$teardown_db';")
  [ "$active" = "0" ] && break
  sleep 0.1
done
rm -f "$holder_pid_file"
"$bindir/dropdb" -h "$test_root" -p 55471 -U platen_rehearsal_operator "$teardown_db"
if [ "$(rehearsal_db_count "$teardown_db")" != "0" ]; then
  echo "FAILED: Harness could not contain synthetic teardown-failure database." >&2; exit 1
fi
echo "PASSED: Blocked teardown exited 21 with containment warning; unrelated DBs, recovery assets intact; PGPASSFILE removed; fixture contained."

# -----------------------------------------------------------------------------
# NEGATIVE TEST 19: dropdb falsely reporting success is caught by verification
# -----------------------------------------------------------------------------
echo "==> [Negative Test 19] Fail-closed teardown: independent absence verification catches false dropdb success..."
false_db="teardown_false_success_probe_db"
false_backup_dir="$test_root/teardown_false_success_backups"

set +e
output=$(env -i PATH="$harness_bin:$PATH" \
  DATABASE_URL="$PROD_DSN" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55471" \
  REHEARSAL_PGUSER="platen_auth_rehearsal_operator" \
  REHEARSAL_PGPASSWORD="SecretRehearsalPass_12345!" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  BACKUP_PASSPHRASE_FILE="$PROD_RECOVERY_KEY" \
  BACKUP_DIR="$false_backup_dir" \
  REHEARSAL_DB="$false_db" \
  TEST_INJECT_DROPDB="false_success" \
  "$repo_root/scripts/run-durability-rehearsal.sh" 2>&1)
rc=$?
set -e

if [ "$rc" -ne 21 ]; then
  echo "FAILED: Expected exit code 21 when dropdb falsely reports success, got $rc. Output: $output" >&2; exit 1
fi
if echo "$output" | grep -q "ERROR: dropdb failed"; then
  echo "FAILED: Test 19 precondition: dropdb should have reported success. Got: $output" >&2; exit 1
fi
echo "$output" | grep -qF "Remaining database : $false_db" || {
  echo "FAILED: Independent verification did not report remaining database. Got: $output" >&2; exit 1
}
if echo "$output" | grep -qE "Teardown verified|REHEARSAL SUCCEEDED"; then
  echo "FAILED: False dropdb success was accepted! Got: $output" >&2; exit 1
fi
if [ "$(rehearsal_db_count "$false_db")" != "1" ]; then
  echo "FAILED: Harness could not independently confirm the remaining rehearsal database." >&2; exit 1
fi
assert_unrelated_databases_intact
assert_recovery_assets_protected "$false_backup_dir"
assert_no_managed_pgpassfile
"$bindir/dropdb" -h "$test_root" -p 55471 -U platen_rehearsal_operator "$false_db"
if [ "$(rehearsal_db_count "$false_db")" != "0" ]; then
  echo "FAILED: Harness could not contain synthetic false-success database." >&2; exit 1
fi
echo "PASSED: False dropdb success caught by independent catalog verification (exit 21); fixture contained."

# -----------------------------------------------------------------------------
# POSITIVE TEST 20: Successful teardown confirms absence and exits successfully
# -----------------------------------------------------------------------------
echo "==> [Positive Test 20] Successful teardown confirms database absence and exits 0..."
success_db="teardown_success_probe_db"
success_backup_dir="$test_root/teardown_success_backups"

set +e
output=$(env -i PATH="$harness_bin:$PATH" \
  DATABASE_URL="$PROD_DSN" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55471" \
  REHEARSAL_PGUSER="platen_auth_rehearsal_operator" \
  REHEARSAL_PGPASSWORD="SecretRehearsalPass_12345!" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  BACKUP_PASSPHRASE_FILE="$PROD_RECOVERY_KEY" \
  BACKUP_DIR="$success_backup_dir" \
  REHEARSAL_DB="$success_db" \
  "$repo_root/scripts/run-durability-rehearsal.sh" 2>&1)
rc=$?
set -e

echo "---- Test 20 evidence: canonical executable teardown output (exit $rc) ----"
echo "$output" | sed -n '/Rehearsal cleanup initiated/,$p'
echo "---- end Test 20 evidence ----"

if [ "$rc" -ne 0 ]; then
  echo "FAILED: Expected exit 0 on verified teardown, got $rc. Output: $output" >&2; exit 1
fi
echo "$output" | grep -qF "Teardown verified: rehearsal database '$success_db' no longer exists on the rehearsal target." || {
  echo "FAILED: Missing verified-teardown confirmation. Got: $output" >&2; exit 1
}
echo "$output" | grep -qF "REHEARSAL SUCCEEDED: all checks passed and rehearsal database absence was verified." || {
  echo "FAILED: Missing success summary. Got: $output" >&2; exit 1
}
if echo "$output" | grep -q "CONTAINMENT REQUIRED"; then
  echo "FAILED: Containment warning emitted on successful teardown. Got: $output" >&2; exit 1
fi
if [ "$(rehearsal_db_count "$success_db")" != "0" ]; then
  echo "FAILED: Harness found rehearsal database remaining after reported successful teardown!" >&2; exit 1
fi
assert_unrelated_databases_intact
assert_recovery_assets_protected "$success_backup_dir"
assert_no_managed_pgpassfile
echo "PASSED: Successful teardown verified database absence (independently confirmed) and exited 0."

# -----------------------------------------------------------------------------
# POSITIVE TEST 21: Production Credential Safety with Canary Password (URI Format)
# -----------------------------------------------------------------------------
echo "==> [Positive Test 21] Production credential safety: authenticated connection with canary password (URI)..."
prod_cred_backup_dir="$test_root/prod_cred_backups"

set +e
output=$(env -i PATH="$harness_bin:$PATH" \
  TEST_ASSERT_NO_SECRETS=1 \
  DATABASE_URL="$PROD_AUTH_URL" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55471" \
  REHEARSAL_PGUSER="platen_auth_rehearsal_operator" \
  REHEARSAL_PGPASSWORD="SecretRehearsalPass_12345!" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  BACKUP_PASSPHRASE_FILE="$PROD_RECOVERY_KEY" \
  BACKUP_DIR="$prod_cred_backup_dir" \
  "$repo_root/scripts/run-durability-rehearsal.sh" 2>&1)
rc=$?
set -e

if [ "$rc" -ne 0 ]; then
  echo "FAILED: Expected exit 0 with authenticated production credentials, got $rc. Output: $output" >&2
  exit 1
fi

# 1. Assert production canary password never leaked in output
if echo "$output" | grep -q "$CANARY_PROD_PASSWORD"; then
  echo "FAILED: Production canary password was leaked in script output!" >&2
  exit 1
fi
# 2. Assert rehearsal password never leaked in output
if echo "$output" | grep -q "SecretRehearsalPass_12345!"; then
  echo "FAILED: Rehearsal password was leaked in script output!" >&2
  exit 1
fi
# 3. Assert password= never leaked in output
if echo "$output" | grep -q "password="; then
  echo "FAILED: Password parameter was printed in script output!" >&2
  exit 1
fi
# 4. Assert full rehearsal lifecycle passed
echo "$output" | grep -q "All 12 durability indexes verified with exact table, name, uniqueness, primary-key, and validity properties." || {
  echo "FAILED: Output missing index verification confirmation. Got: $output" >&2
  exit 1
}
echo "$output" | grep -q "REHEARSAL SUCCEEDED: all checks passed and rehearsal database absence was verified." || {
  echo "FAILED: Output missing success confirmation. Got: $output" >&2
  exit 1
}
# 5. Assert managed PGPASSFILE was cleaned up on exit
assert_no_managed_pgpassfile
# 6. Assert recovery assets preserved and cleanly decryptable
assert_recovery_assets_protected "$prod_cred_backup_dir"
echo "PASSED: Authenticated production connection with canary password succeeded; 0 secrets leaked; PGPASSFILE cleaned."

# -----------------------------------------------------------------------------
# NEGATIVE TEST 22: Production Wrong-Password Rejection & Credential Cleanup
# -----------------------------------------------------------------------------
echo "==> [Negative Test 22] Production source-database wrong password rejection & credential cleanup..."
prod_wrong_url="postgresql://platen_auth_prod_operator:WrongCanarySecret_99999!@/pdfnest_production?host=$test_root&port=55470&sslmode=disable"

set +e
output=$(env -i PATH="$harness_bin:$PATH" \
  TEST_ASSERT_NO_SECRETS=1 \
  DATABASE_URL="$prod_wrong_url" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55471" \
  REHEARSAL_PGUSER="platen_auth_rehearsal_operator" \
  REHEARSAL_PGPASSWORD="SecretRehearsalPass_12345!" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  BACKUP_PASSPHRASE_FILE="$PROD_RECOVERY_KEY" \
  "$repo_root/scripts/run-durability-rehearsal.sh" 2>&1)
rc=$?
set -e

if [ "$rc" -eq 0 ]; then
  echo "FAILED: Execution succeeded despite wrong production password!" >&2
  exit 1
fi
if [ "$rc" -ne 4 ]; then
  echo "FAILED: Expected exit code 4 on production connection failure, got $rc. Output: $output" >&2
  exit 1
fi
echo "$output" | grep -q "Failed to connect to production database via DATABASE_URL." || {
  echo "FAILED: Output missing expected production connection failure message. Got: $output" >&2
  exit 1
}
# Assert zero password leakage in error output
if echo "$output" | grep -q -E "$CANARY_PROD_PASSWORD|WrongCanarySecret_99999!|SecretRehearsalPass_12345!"; then
  echo "FAILED: Password leaked in connection failure output! Got: $output" >&2
  exit 1
fi
# Assert managed PGPASSFILE was cleaned up on failure
assert_no_managed_pgpassfile
echo "PASSED: Production wrong-password rejected (exit code 4); 0 secrets leaked; PGPASSFILE cleaned."

# -----------------------------------------------------------------------------
# NEGATIVE TEST 23: Production Credential Boundary Across Failed Rehearsal Teardown
# -----------------------------------------------------------------------------
echo "==> [Negative Test 23] Production credentials across failed rehearsal teardown..."
teardown_prod_db="teardown_prod_cred_probe_db"
teardown_prod_backup_dir="$test_root/teardown_prod_cred_backups"
holder_pid_file="$test_root/teardown_holder.pid"

set +e
output=$(env -i PATH="$harness_bin:$PATH" \
  TEST_ASSERT_NO_SECRETS=1 \
  DATABASE_URL="$PROD_AUTH_URL" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55471" \
  REHEARSAL_PGUSER="platen_auth_rehearsal_operator" \
  REHEARSAL_PGPASSWORD="SecretRehearsalPass_12345!" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  BACKUP_PASSPHRASE_FILE="$PROD_RECOVERY_KEY" \
  BACKUP_DIR="$teardown_prod_backup_dir" \
  REHEARSAL_DB="$teardown_prod_db" \
  TEST_INJECT_DROPDB="hold_connection" \
  TEST_HOLDER_HOST="$test_root" \
  TEST_HOLDER_PORT="55471" \
  TEST_HOLDER_USER="platen_rehearsal_operator" \
  TEST_HOLDER_PID_FILE="$holder_pid_file" \
  TEST_HOLDER_LOG="$test_root/teardown_holder.log" \
  "$repo_root/scripts/run-durability-rehearsal.sh" 2>&1)
rc=$?
set -e

echo "---- Test 23 evidence: teardown failure with authenticated production source (exit $rc) ----"
echo "$output" | sed -n '/Rehearsal cleanup initiated/,$p'
echo "---- end Test 23 evidence ----"

if [ "$rc" -ne 21 ]; then
  echo "FAILED: Expected exit code 21 on teardown failure with authenticated production source, got $rc. Output: $output" >&2
  exit 1
fi
echo "$output" | grep -q "Disposable rehearsal restore and migration dry run successfully completed!" || {
  echo "FAILED: Restore and migration dry run did not succeed before teardown. Got: $output" >&2
  exit 1
}
echo "$output" | grep -q "CONTAINMENT REQUIRED: REHEARSAL DATABASE TEARDOWN FAILED" || {
  echo "FAILED: Output missing containment alert. Got: $output" >&2
  exit 1
}
# Assert zero password leakage in teardown failure output
if echo "$output" | grep -q -E "$CANARY_PROD_PASSWORD|SecretRehearsalPass_12345!"; then
  echo "FAILED: Secret leaked in teardown failure output! Got: $output" >&2
  exit 1
fi
# Assert managed PGPASSFILE cleaned up on exit
assert_no_managed_pgpassfile
# Assert recovery assets preserved
assert_recovery_assets_protected "$teardown_prod_backup_dir"
assert_unrelated_databases_intact

# Clean up synthetic teardown fixture
"$bindir/psql" -X -h "$test_root" -p 55471 -U platen_rehearsal_operator -d postgres -At \
  -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$teardown_prod_db';" >/dev/null
kill "$(cat "$holder_pid_file")" 2>/dev/null || true
for _ in $(seq 1 50); do
  active=$("$bindir/psql" -X -h "$test_root" -p 55471 -U platen_rehearsal_operator -d postgres -At \
    -c "SELECT count(*) FROM pg_stat_activity WHERE datname = '$teardown_prod_db';")
  [ "$active" = "0" ] && break
  sleep 0.1
done
rm -f "$holder_pid_file"
"$bindir/dropdb" -h "$test_root" -p 55471 -U platen_rehearsal_operator "$teardown_prod_db"
if [ "$(rehearsal_db_count "$teardown_prod_db")" != "0" ]; then
  echo "FAILED: Harness could not contain synthetic teardown-failure database." >&2; exit 1
fi
echo "PASSED: Authenticated production credentials preserved boundary across teardown failure (exit 21); PGPASSFILE cleaned; fixture contained."

# -----------------------------------------------------------------------------
# POSITIVE TEST 24: Production Credential Safety with Keyword/Value DSN Format
# -----------------------------------------------------------------------------
echo "==> [Positive Test 24] Production credential safety: keyword/value DSN format with canary password..."
prod_dsn_backup_dir="$test_root/prod_dsn_backups"
prod_auth_dsn="host=$test_root port=55470 user=platen_auth_prod_operator password='$CANARY_PROD_PASSWORD' dbname=pdfnest_production sslmode=disable"

set +e
output=$(env -i PATH="$harness_bin:$PATH" \
  TEST_ASSERT_NO_SECRETS=1 \
  DATABASE_URL="$prod_auth_dsn" \
  REHEARSAL_PGHOST="$test_root" \
  REHEARSAL_PGPORT="55471" \
  REHEARSAL_PGUSER="platen_auth_rehearsal_operator" \
  REHEARSAL_PGPASSWORD="SecretRehearsalPass_12345!" \
  REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
  BACKUP_PASSPHRASE_FILE="$PROD_RECOVERY_KEY" \
  BACKUP_DIR="$prod_dsn_backup_dir" \
  "$repo_root/scripts/run-durability-rehearsal.sh" 2>&1)
rc=$?
set -e

if [ "$rc" -ne 0 ]; then
  echo "FAILED: Expected exit 0 with keyword/value DSN production credentials, got $rc. Output: $output" >&2
  exit 1
fi
if echo "$output" | grep -q "$CANARY_PROD_PASSWORD"; then
  echo "FAILED: Canary password leaked in output with keyword/value DSN!" >&2
  exit 1
fi
assert_no_managed_pgpassfile
assert_recovery_assets_protected "$prod_dsn_backup_dir"
echo "PASSED: Keyword/value production DSN with canary password succeeded; 0 secrets leaked; PGPASSFILE cleaned."

echo ""
echo "================================================================================"
echo "ALL REHEARSAL RUNBOOK QUALIFICATION CHECKS (24/24) PASSED SUCCESSFULLY!"
echo "================================================================================"
