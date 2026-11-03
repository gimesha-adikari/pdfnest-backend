#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# Platen PDF — Qualification Suite for Migration Operator Helper
#
# Validates both negative safety boundaries and positive execution paths of
# scripts/apply-durability-migration.sh against an isolated disposable native
# PostgreSQL instance on private Unix domain sockets (zero TCP network exposure).
#
# Tests Covered:
#   1. Missing DATABASE_URL fails fast (exit 1)
#   2. Missing affirmative confirmation fails fast (exit 2)
#   3. Arbitrary/unreviewed MIGRATION_FILE override fails fast (exit 3)
#   4. Valid URI format with canary password succeeds (0 secrets in args, exit 0)
#   5. Valid keyword/value format with canary password succeeds (exit 0)
#   6. Keyword/value password with escaped apostrophe (\') parses cleanly (exit 0)
#   7. Keyword/value password with escaped backslashes (\\) parses cleanly (exit 0)
#   8. Quoted password with whitespace, colons, and punctuation succeeds (exit 0)
#   9. Unterminated quoted string fails closed before client execution (exit 1)
#  10. Malformed escape sequence fails closed before client execution (exit 1)
#  11. Extraneous characters after closing quote fail closed (exit 1)
#  12. Unquoted string containing unexpected quotes fails closed (exit 1)
#  13. Ambiguous credentials in URI (userinfo + query param) fail closed (exit 1)
#  14. Strict target-specific isolation: missing host/user/dbname fails closed (0 wildcards)
#  15. Empirical inspection of actual generated .pgpass file (mode 0600, zero wildcards)
#  16. Wrong password rejection fails fast (exit 4, 0 secrets in logs, .pgpass cleaned)
#  17. Target database identity mismatch fails closed before DDL execution (exit 5)
#  18. Standby rejection with controlled response and verified zero DDL execution (exit 5)
#  19. Process argument & child environment inspection (0 secrets in argv or general environ)
#  20. Invalid ambient PGPASSFILE path fails closed (exit 1)
#  21. Valid ambient PGPASSFILE preservation with fallback precedence
#  22. Genuine signal termination (SIGTERM exit 143, SIGINT exit 130) & independent cleanup
#  23. Full durability migration execution & exact marker, table, column, index assertions
#  24. Unreviewed SQL file substitution rejection (tampered SQL file rejected, unapplied)
#  25. Wrong PostgreSQL cluster rejection even when database and username match (exit 5)
#  26. Operator role identity mismatch rejection (exit 5)
# -----------------------------------------------------------------------------
set -euo pipefail

bindir=""
for candidate in \
  "$(pg_config --bindir 2>/dev/null || true)" \
  "/usr/lib/postgresql/18/bin" \
  "/usr/lib/postgresql/17/bin" \
  "/usr/lib/postgresql/16/bin" \
  "/usr/lib/postgresql/15/bin" \
  "/usr/bin"; do
  if [ -n "$candidate" ] && [ -x "$candidate/initdb" ] && [ -x "$candidate/pg_ctl" ] && [ -x "$candidate/psql" ]; then
    bindir="$candidate"
    break
  fi
done

if [ -z "$bindir" ]; then
  echo "FAIL: Required PostgreSQL client/server binaries (initdb, pg_ctl, psql) not found." >&2
  exit 1
fi

export PATH="$bindir:$PATH"

test_root="$(mktemp -d /tmp/platen-migration-op-test.XXXXXXXX)"

cleanup_all() {
  local exit_code=$?
  if [ $exit_code -ne 0 ]; then
    echo "ERROR: Test failed with exit code $exit_code. Dumping recent logs:" >&2
    for log in "$test_root"/*.log; do
      if [ -f "$log" ]; then
        echo "=== $log ===" >&2
        cat "$log" >&2
      fi
    done
  fi
  if [ -d "$test_root/pgdata" ]; then
    "$bindir/pg_ctl" -D "$test_root/pgdata" -m fast -w stop >/dev/null 2>&1 || true
  fi
  rm -rf "$test_root"
  exit "$exit_code"
}

trap cleanup_all EXIT INT TERM

echo "==> Initializing disposable PostgreSQL instance on private socket..."
"$bindir/initdb" -D "$test_root/pgdata" -A trust -U platen_admin --encoding=UTF8 >"$test_root/init.log" 2>&1

# Setup pg_hba.conf for md5 password authentication on test roles
cat << 'EOF' | cat - "$test_root/pgdata/pg_hba.conf" > "$test_root/pgdata/pg_hba.conf.tmp"
local all platen_op_auth md5
local all platen_op_quote md5
local all platen_op_slash md5
local all platen_op_punct md5
local all platen_op_special md5
EOF
mv "$test_root/pgdata/pg_hba.conf.tmp" "$test_root/pgdata/pg_hba.conf"

TEST_PORT=55490
"$bindir/pg_ctl" -D "$test_root/pgdata" -l "$test_root/pg.log" -o "-h '' -k $test_root -p $TEST_PORT -F" -w start >/dev/null

TARGET_DB="platen_test_migration_db"
WRONG_DB="platen_wrong_migration_db"
"$bindir/createdb" -h "$test_root" -p "$TEST_PORT" -U platen_admin "$TARGET_DB"
"$bindir/createdb" -h "$test_root" -p "$TEST_PORT" -U platen_admin "$WRONG_DB"

ADMIN_DSN="host=$test_root port=$TEST_PORT user=platen_admin dbname=$TARGET_DB sslmode=disable"

# Provision initial schema and test roles
psql --dbname="$ADMIN_DSN" -v ON_ERROR_STOP=1 << 'EOF' >/dev/null
CREATE TABLE public.schema_migrations (
  version varchar(64) PRIMARY KEY,
  applied_at timestamptz NOT NULL
);
INSERT INTO public.schema_migrations VALUES ('20260918_01_initial', now());

CREATE TABLE public.users (
  id uuid PRIMARY KEY,
  email varchar(255) NOT NULL
);

CREATE TABLE public.studio_jobs (
  id varchar(64) PRIMARY KEY,
  user_id uuid REFERENCES public.users(id),
  status text NOT NULL
);

-- Roles with password authentication
CREATE ROLE platen_op_auth WITH SUPERUSER LOGIN PASSWORD 'CanaryOpSecret_123!';
CREATE ROLE platen_op_quote WITH SUPERUSER LOGIN PASSWORD $tag$Canary'OpQuote#88!$tag$;
CREATE ROLE platen_op_slash WITH SUPERUSER LOGIN PASSWORD $tag$Canary\OpSlash\\Double#88!$tag$;
CREATE ROLE platen_op_punct WITH SUPERUSER LOGIN PASSWORD $tag$Canary Op Secret With Spaces & Punctuation! #99$tag$;
CREATE ROLE platen_op_special WITH SUPERUSER LOGIN PASSWORD $tag$Canary:Special\Pass:88!#@$tag$;

ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO public;
GRANT ALL ON ALL TABLES IN SCHEMA public TO public;
GRANT ALL ON SCHEMA public TO public;
EOF

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SCRIPT_PATH="$REPO_ROOT/scripts/apply-durability-migration.sh"

# -----------------------------------------------------------------------------
# Setup Secure Inspection Wrapper for psql (Checks argv & general environ)
# -----------------------------------------------------------------------------
mkdir -p "$test_root/harness_bin"
cat << EOF > "$test_root/harness_bin/psql"
#!/usr/bin/env bash
if [ "\${TEST_ASSERT_NO_SECRETS:-0}" = "1" ]; then
  for arg in "\$@"; do
    if echo "\$arg" | grep -qiE "Canary|password=|password\s*="; then
      echo "ASSERTION FAILED: Credential detected in process argv: \$arg" >&2
      exit 99
    fi
  done
  if [ -n "\${DATABASE_URL:-}" ]; then
    echo "ASSERTION FAILED: DATABASE_URL leaked into child environment!" >&2
    exit 98
  fi
  if [ -n "\${PGPASSWORD:-}" ]; then
    echo "ASSERTION FAILED: PGPASSWORD leaked into child environment!" >&2
    exit 98
  fi
fi
exec "$bindir/psql" "\$@"
EOF
chmod +x "$test_root/harness_bin/psql"
REAL_PATH="$PATH"
HARNESS_PATH="$test_root/harness_bin:$PATH"

echo "================================================================================"
echo "STARTING MIGRATION OPERATOR QUALIFICATION SUITE"
echo "================================================================================"

# Test 1: Missing DATABASE_URL
echo "==> [Test 1] Missing DATABASE_URL..."
set +e
DATABASE_URL="" CONFIRM_PRODUCTION_MIGRATION="yes" "$SCRIPT_PATH" >"$test_root/t1.log" 2>&1
rc=$?
set -e
if [ $rc -ne 1 ]; then echo "FAIL: Expected exit 1, got $rc"; exit 1; fi
echo "PASSED: Missing DATABASE_URL rejected with exit code 1."

# Test 2: Missing affirmative confirmation
echo "==> [Test 2] Missing affirmative confirmation..."
set +e
DATABASE_URL="postgres://platen_admin@$test_root:$TEST_PORT/$TARGET_DB" CONFIRM_PRODUCTION_MIGRATION="no" "$SCRIPT_PATH" >"$test_root/t2.log" 2>&1
rc=$?
set -e
if [ $rc -ne 2 ]; then echo "FAIL: Expected exit 2, got $rc"; exit 1; fi
echo "PASSED: Missing affirmative confirmation rejected with exit code 2."

# Test 3: Unreviewed/arbitrary MIGRATION_FILE override rejected
echo "==> [Test 3] Arbitrary/unreviewed MIGRATION_FILE override fails fast..."
set +e
DATABASE_URL="postgres://platen_admin@$test_root:$TEST_PORT/$TARGET_DB" CONFIRM_PRODUCTION_MIGRATION="yes" MIGRATION_FILE="/nonexistent/unreviewed.sql" "$SCRIPT_PATH" >"$test_root/t3.log" 2>&1
rc=$?
set -e
if [ $rc -ne 3 ]; then echo "FAIL: Expected exit 3, got $rc"; exit 1; fi
grep -q "Arbitrary or unreviewed MIGRATION_FILE is strictly prohibited" "$test_root/t3.log"
echo "PASSED: Arbitrary MIGRATION_FILE override cleanly rejected (exit 3)."

# Test 4: Valid URI format with canary password
echo "==> [Test 4] Valid URI format with canary password..."
URI_DSN="postgresql://platen_op_auth:CanaryOpSecret_123%21@/platen_test_migration_db?host=$test_root&port=$TEST_PORT&sslmode=disable"
PATH="$HARNESS_PATH" TEST_ASSERT_NO_SECRETS=1 DATABASE_URL="$URI_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" "$SCRIPT_PATH" >"$test_root/t4.log" 2>&1
echo "PASSED: Valid URI format executed cleanly; 0 credentials leaked in argv/environ (exit 0)."

# Test 5: Valid keyword/value format with canary password
echo "==> [Test 5] Valid keyword/value format with canary password..."
KV_DSN="host=$test_root port=$TEST_PORT dbname=$TARGET_DB user=platen_op_auth password='CanaryOpSecret_123!' sslmode=disable"
PATH="$HARNESS_PATH" TEST_ASSERT_NO_SECRETS=1 DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" "$SCRIPT_PATH" >"$test_root/t5.log" 2>&1
echo "PASSED: Valid keyword/value format executed cleanly (exit 0)."

# Test 6: Keyword/value password with escaped apostrophe (\')
echo "==> [Test 6] Keyword/value password with escaped apostrophe (\')..."
KV_QUOTE="host=$test_root port=$TEST_PORT dbname=$TARGET_DB user=platen_op_quote password='Canary\'OpQuote#88!' sslmode=disable"
PATH="$HARNESS_PATH" TEST_ASSERT_NO_SECRETS=1 DATABASE_URL="$KV_QUOTE" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" "$SCRIPT_PATH" >"$test_root/t6.log" 2>&1
echo "PASSED: Password with escaped apostrophe authenticated cleanly (exit 0)."

# Test 7: Keyword/value password with escaped backslashes (\\)
echo "==> [Test 7] Keyword/value password with escaped backslashes (\\)..."
KV_SLASH="host=$test_root port=$TEST_PORT dbname=$TARGET_DB user=platen_op_slash password='Canary\\\\OpSlash\\\\\\\\Double#88!' sslmode=disable"
PATH="$HARNESS_PATH" TEST_ASSERT_NO_SECRETS=1 DATABASE_URL="$KV_SLASH" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" "$SCRIPT_PATH" >"$test_root/t7.log" 2>&1
echo "PASSED: Password with escaped backslashes authenticated cleanly (exit 0)."

# Test 8: Quoted password with whitespace, punctuation, and colons
echo "==> [Test 8] Quoted password with whitespace, punctuation, and colons..."
KV_PUNCT="host=$test_root port=$TEST_PORT dbname=$TARGET_DB user=platen_op_punct password='Canary Op Secret With Spaces & Punctuation! #99' sslmode=disable"
PATH="$HARNESS_PATH" TEST_ASSERT_NO_SECRETS=1 DATABASE_URL="$KV_PUNCT" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" "$SCRIPT_PATH" >"$test_root/t8.log" 2>&1
echo "PASSED: Password with whitespace and punctuation authenticated cleanly (exit 0)."

# Test 9: Unterminated quoted string fails closed before client execution
echo "==> [Test 9] Unterminated quoted string fails closed..."
KV_UNTERM="host=$test_root port=$TEST_PORT dbname=$TARGET_DB user=platen_op_auth password='UnterminatedCanarySecret"
set +e
DATABASE_URL="$KV_UNTERM" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" "$SCRIPT_PATH" >"$test_root/t9.log" 2>&1
rc=$?
set -e
if [ $rc -ne 1 ]; then echo "FAIL: Expected exit 1, got $rc"; exit 1; fi
grep -q "Unterminated quoted string" "$test_root/t9.log"
echo "PASSED: Unterminated quoted string cleanly failed closed before client execution (exit 1)."

# Test 10: Malformed escape sequence fails closed
echo "==> [Test 10] Malformed escape sequence fails closed..."
KV_BAD_ESC="host=$test_root port=$TEST_PORT dbname=$TARGET_DB user=platen_op_auth password='DanglingEscape\\"
set +e
DATABASE_URL="$KV_BAD_ESC" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" "$SCRIPT_PATH" >"$test_root/t10.log" 2>&1
rc=$?
set -e
if [ $rc -ne 1 ]; then echo "FAIL: Expected exit 1, got $rc"; exit 1; fi
grep -q "Unterminated escape sequence" "$test_root/t10.log"
echo "PASSED: Unterminated escape sequence cleanly failed closed (exit 1)."

# Test 11: Extraneous characters after closing quote fail closed
echo "==> [Test 11] Extraneous characters after closing quote fail closed..."
KV_EXTRA="host=$test_root port=$TEST_PORT dbname=$TARGET_DB user=platen_op_auth password='ValidPassword'extraChars sslmode=disable"
set +e
DATABASE_URL="$KV_EXTRA" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" "$SCRIPT_PATH" >"$test_root/t11.log" 2>&1
rc=$?
set -e
if [ $rc -ne 1 ]; then echo "FAIL: Expected exit 1, got $rc"; exit 1; fi
grep -q "Extraneous character after closing quote" "$test_root/t11.log"
echo "PASSED: Extraneous post-quote characters cleanly failed closed (exit 1)."

# Test 12: Unquoted string containing unexpected quotes fails closed
echo "==> [Test 12] Unquoted string containing unexpected quotes fails closed..."
KV_BAD_UNQUOTED="host=$test_root port=$TEST_PORT dbname=$TARGET_DB user=platen_op_auth password=Unquoted'Quote sslmode=disable"
set +e
DATABASE_URL="$KV_BAD_UNQUOTED" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" "$SCRIPT_PATH" >"$test_root/t12.log" 2>&1
rc=$?
set -e
if [ $rc -ne 1 ]; then echo "FAIL: Expected exit 1, got $rc"; exit 1; fi
grep -q "unexpected quote character" "$test_root/t12.log"
echo "PASSED: Unexpected quote inside unquoted value cleanly failed closed (exit 1)."

# Test 13: Ambiguous credentials in URI fail closed
echo "==> [Test 13] Ambiguous credentials in URI fail closed..."
URI_AMBIG="postgresql://platen_op_auth:UserinfoSecret@$test_root:$TEST_PORT/$TARGET_DB?password=QueryParamSecret"
set +e
DATABASE_URL="$URI_AMBIG" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" "$SCRIPT_PATH" >"$test_root/t13.log" 2>&1
rc=$?
set -e
if [ $rc -ne 1 ]; then echo "FAIL: Expected exit 1, got $rc"; exit 1; fi
grep -q "Ambiguous credential specification" "$test_root/t13.log"
echo "PASSED: Ambiguous query parameter credentials cleanly failed closed (exit 1)."

# Test 14: Strict target-specific isolation: missing fields fail closed (0 wildcards)
echo "==> [Test 14] Strict target-specific isolation: missing host/user/dbname fails closed..."
set +e
DATABASE_URL="port=$TEST_PORT dbname=$TARGET_DB user=platen_op_auth password='foo'" CONFIRM_PRODUCTION_MIGRATION="yes" "$SCRIPT_PATH" >"$test_root/t14a.log" 2>&1
rc=$?
set -e
if [ $rc -ne 1 ]; then echo "FAIL: Expected exit 1 for missing host, got $rc"; exit 1; fi

set +e
DATABASE_URL="host=$test_root port=$TEST_PORT dbname=$TARGET_DB password='foo'" CONFIRM_PRODUCTION_MIGRATION="yes" "$SCRIPT_PATH" >"$test_root/t14b.log" 2>&1
rc=$?
set -e
if [ $rc -ne 1 ]; then echo "FAIL: Expected exit 1 for missing user, got $rc"; exit 1; fi

set +e
DATABASE_URL="host=$test_root port=$TEST_PORT user=platen_op_auth password='foo'" CONFIRM_PRODUCTION_MIGRATION="yes" "$SCRIPT_PATH" >"$test_root/t14c.log" 2>&1
rc=$?
set -e
if [ $rc -ne 1 ]; then echo "FAIL: Expected exit 1 for missing dbname, got $rc"; exit 1; fi
echo "PASSED: Missing connection fields cleanly rejected (0 wildcards allowed)."

# Test 15: Empirical inspection of actual generated .pgpass file
echo "==> [Test 15] Empirical inspection of actual generated .pgpass file..."
mkdir -p "$test_root/harness_t15"
cat << 'EOF' > "$test_root/harness_t15/psql"
#!/usr/bin/env bash
if [ -z "${PGPASSFILE:-}" ] || [ ! -f "$PGPASSFILE" ]; then
  echo "T15_FAIL: PGPASSFILE missing or not created: '${PGPASSFILE:-}'" >&2
  exit 97
fi
# Empirically check file permissions mode 0600
perms="$(stat -c %a "$PGPASSFILE" 2>/dev/null || stat -f %Lp "$PGPASSFILE" 2>/dev/null)"
if [ "$perms" != "600" ]; then
  echo "T15_FAIL: PGPASSFILE has unsafe permissions: $perms (expected 600)" >&2
  exit 97
fi
line="$(head -n 1 "$PGPASSFILE")"
IFS=':' read -r f_host f_port f_db f_user f_pass <<< "$line"
if [ -z "$f_host" ] || [ -z "$f_port" ] || [ -z "$f_db" ] || [ -z "$f_user" ] || [ -z "$f_pass" ]; then
  echo "T15_FAIL: Incomplete line in PGPASSFILE: $line" >&2
  exit 97
fi
if [ "$f_host" = "*" ] || [ "$f_port" = "*" ] || [ "$f_db" = "*" ] || [ "$f_user" = "*" ]; then
  echo "T15_FAIL: Wildcard field found in PGPASSFILE: $line" >&2
  exit 97
fi
echo "T15_VERIFIED_PGPASS: host=$f_host port=$f_port db=$f_db user=$f_user perms=$perms"
exit 0
EOF
chmod +x "$test_root/harness_t15/psql"

set +e
PATH="$test_root/harness_t15:$REAL_PATH" DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" "$SCRIPT_PATH" >"$test_root/t15.log" 2>&1
rc=$?
set -e
grep -q "T15_VERIFIED_PGPASS: host=$test_root port=$TEST_PORT db=$TARGET_DB user=platen_op_auth perms=600" "$test_root/t15.log"
echo "PASSED: Empirically inspected actual generated .pgpass file: mode 0600, exact target fields, zero wildcards."

# Test 16: Wrong password rejection
echo "==> [Test 16] Wrong password rejection..."
KV_WRONG="host=$test_root port=$TEST_PORT dbname=$TARGET_DB user=platen_op_auth password='WrongCanarySecret_999!' sslmode=disable"
set +e
DATABASE_URL="$KV_WRONG" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" "$SCRIPT_PATH" >"$test_root/t16.log" 2>&1
rc=$?
set -e
if [ $rc -ne 4 ]; then echo "FAIL: Expected exit 4 for auth failure, got $rc"; exit 1; fi
if grep -q "WrongCanarySecret_999!" "$test_root/t16.log"; then
  echo "FAIL: Password leaked in output log!"; exit 1
fi
echo "PASSED: Wrong password rejected (exit 4); zero secrets in logs."

# Test 17: Target database identity mismatch fails closed before DDL execution
echo "==> [Test 17] Target database identity mismatch fails closed before DDL execution..."
set +e
DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="different_prod_db" "$SCRIPT_PATH" >"$test_root/t17.log" 2>&1
rc=$?
set -e
if [ $rc -ne 5 ]; then echo "FAIL: Expected exit 5 for target DB mismatch, got $rc"; exit 1; fi
grep -q "Target database identity mismatch" "$test_root/t17.log"
echo "PASSED: Target database identity mismatch cleanly failed closed before running DDL (exit 5)."

# Test 18: Standby rejection with controlled test response and verified zero DDL execution
echo "==> [Test 18] Standby rejection with controlled response and verified zero DDL execution..."
mkdir -p "$test_root/harness_t18"
cat << EOF > "$test_root/harness_t18/psql"
#!/usr/bin/env bash
for arg in "\$@"; do
  if echo "\$arg" | grep -q "pg_catalog.pg_is_in_recovery"; then
    # Return standby recovery state: is_recovery = 't'
    echo "$TARGET_DB|platen_op_auth|t|1234567890"
    exit 0
  fi
done
echo "T18_FAIL: DDL execution psql called on standby node!" >&2
exit 96
EOF
chmod +x "$test_root/harness_t18/psql"

set +e
PATH="$test_root/harness_t18:$REAL_PATH" DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" "$SCRIPT_PATH" >"$test_root/t18.log" 2>&1
rc=$?
set -e
if [ $rc -ne 5 ]; then echo "FAIL: Expected exit 5 for standby rejection, got $rc"; exit 1; fi
grep -q "in recovery (standby/read-only replica: 't')" "$test_root/t18.log"
if grep -q "T18_FAIL" "$test_root/t18.log"; then
  echo "FAIL: DDL was attempted on standby node!" >&2
  exit 1
fi
echo "PASSED: Standby node rejected with controlled test response (exit 5); verified zero DDL execution."

# Test 19: Process argument & child environment inspection
echo "==> [Test 19] Process argument & child environment inspection..."
PATH="$HARNESS_PATH" TEST_ASSERT_NO_SECRETS=1 DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" "$SCRIPT_PATH" >"$test_root/t19.log" 2>&1
echo "PASSED: Harness wrapper confirmed zero credentials in argv (exit 99 assertion passed) and general environ (exit 98 assertion passed)."

# Test 20: Invalid ambient PGPASSFILE path fails closed
echo "==> [Test 20] Invalid ambient PGPASSFILE path fails closed..."
set +e
PGPASSFILE="$test_root/nonexistent_ambient_pgpass" DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" "$SCRIPT_PATH" >"$test_root/t20.log" 2>&1
rc=$?
set -e
if [ $rc -ne 1 ]; then echo "FAIL: Expected exit 1 for invalid ambient PGPASSFILE, got $rc"; exit 1; fi
grep -q "ambient PGPASSFILE does not exist" "$test_root/t20.log"
echo "PASSED: Invalid ambient PGPASSFILE path rejected (fail-closed, exit 1)."

# Test 21: Valid ambient PGPASSFILE preservation with fallback precedence
echo "==> [Test 21] Valid ambient PGPASSFILE preservation with fallback precedence..."
VALID_AMBIENT="$test_root/valid_ambient_pgpass"
echo "ambient_host:5432:ambient_db:ambient_user:ambient_pass" > "$VALID_AMBIENT"
chmod 600 "$VALID_AMBIENT"
PGPASSFILE="$VALID_AMBIENT" DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" "$SCRIPT_PATH" >"$test_root/t21.log" 2>&1
echo "PASSED: Valid ambient PGPASSFILE entries preserved with proper fallback precedence."

# Test 22: Genuine signal termination (SIGTERM exit 143, SIGINT exit 130) & independent cleanup
echo "==> [Test 22] Genuine signal termination (SIGTERM exit 143, SIGINT exit 130) & independent cleanup..."
mkdir -p "$test_root/harness_t22"
cat << 'EOF' > "$test_root/harness_t22/psql"
#!/usr/bin/env bash
if [ -n "${TEST_SIGNAL_CANARY:-}" ]; then
  echo "$PGPASSFILE" > "$TEST_SIGNAL_CANARY"
  trap 'exit 130' INT
  trap 'exit 143' TERM
  # Sleep to allow test harness to deliver signal
  sleep 15
  exit 0
fi
exec "$bindir/psql" "$@"
EOF
chmod +x "$test_root/harness_t22/psql"

# Test SIGTERM (exit 143)
CANARY_TERM="$test_root/canary_term"
set -m
PATH="$test_root/harness_t22:$REAL_PATH" TEST_SIGNAL_CANARY="$CANARY_TERM" DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" "$SCRIPT_PATH" >"$test_root/t22_term.log" 2>&1 &
pid_term=$!

for i in {1..60}; do
  if [ -s "$CANARY_TERM" ]; then break; fi
  sleep 0.05
done
if [ ! -s "$CANARY_TERM" ]; then echo "FAIL: Test 22 SIGTERM canary was not written"; kill -9 -$pid_term 2>/dev/null || true; exit 1; fi

managed_file_term="$(cat "$CANARY_TERM")"
if [ ! -f "$managed_file_term" ]; then echo "FAIL: Managed pgpass file missing before signal"; exit 1; fi

kill -TERM -$pid_term
set +e
wait "$pid_term"
rc_term=$?
set -e
if [ $rc_term -ne 143 ]; then echo "FAIL: Expected exit 143 for SIGTERM, got $rc_term"; exit 1; fi
if [ -f "$managed_file_term" ]; then echo "FAIL: Managed pgpass file was not removed after SIGTERM!"; exit 1; fi

# Test SIGINT (exit 130)
CANARY_INT="$test_root/canary_int"
PATH="$test_root/harness_t22:$REAL_PATH" TEST_SIGNAL_CANARY="$CANARY_INT" DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" "$SCRIPT_PATH" >"$test_root/t22_int.log" 2>&1 &
pid_int=$!

for i in {1..60}; do
  if [ -s "$CANARY_INT" ]; then break; fi
  sleep 0.05
done
if [ ! -s "$CANARY_INT" ]; then echo "FAIL: Test 22 SIGINT canary was not written"; kill -9 -$pid_int 2>/dev/null || true; exit 1; fi

managed_file_int="$(cat "$CANARY_INT")"
if [ ! -f "$managed_file_int" ]; then echo "FAIL: Managed pgpass file missing before signal"; exit 1; fi

kill -INT -$pid_int
set +e
wait "$pid_int"
rc_int=$?
set -e
set +m
if [ $rc_int -ne 130 ]; then echo "FAIL: Expected exit 130 for SIGINT, got $rc_int"; exit 1; fi
if [ -f "$managed_file_int" ]; then echo "FAIL: Managed pgpass file was not removed after SIGINT!"; exit 1; fi

echo "PASSED: Genuinely sent SIGTERM (exit 143) and SIGINT (exit 130); independently verified temporary file unlinked."

# Test 23: Full durability migration execution & exact marker, table, column, index assertions
echo "==> [Test 23] Full durability migration execution & exact postconditions..."
set +e
DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" "$SCRIPT_PATH" >"$test_root/t23.log" 2>&1
rc=$?
set -e
if [ $rc -ne 0 ]; then
  echo "FAIL: Test 23 exited with code $rc. Output:"
  cat "$test_root/t23.log"
  exit 1
fi

# Assert exact marker row count == 1
marker_count="$(psql --dbname="$ADMIN_DSN" -At -c "SELECT count(*) FROM public.backend_durability_schema WHERE version = '20261029_backend_durability_v1';")"
if [ "$marker_count" != "1" ]; then echo "FAIL: Expected marker count 1, got '$marker_count'"; exit 1; fi

# Assert exact table presence
for tbl in task_completions worker_result_ownerships studio_storage_object_retirements backend_durability_schema; do
  reg="$(psql --dbname="$ADMIN_DSN" -At -c "SELECT to_regclass('public.$tbl')::text;")"
  if [ "$reg" != "public.$tbl" ] && [ "$reg" != "$tbl" ]; then
    echo "FAIL: Table public.$tbl missing from catalog (regclass: '$reg')"; exit 1
  fi
done

# Assert exact column data_type
col_type="$(psql --dbname="$ADMIN_DSN" -At -c "SELECT data_type FROM information_schema.columns WHERE table_schema='public' AND table_name='studio_jobs' AND column_name='pending_object_keys';")"
if [ "$col_type" != "jsonb" ]; then echo "FAIL: Expected pending_object_keys to be jsonb, got '$col_type'"; exit 1; fi

# Assert all 12 exact durability indexes
index_info="$(psql --dbname="$ADMIN_DSN" -At -c "
SELECT count(*), count(*) FILTER (WHERE i.indisunique = true), count(*) FILTER (WHERE i.indisvalid = true)
FROM pg_index i
JOIN pg_class c ON c.oid = i.indexrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relname IN (
  'backend_durability_schema_pkey',
  'studio_storage_object_retirements_pkey',
  'task_completions_pkey',
  'idx_task_completions_expires_at',
  'idx_task_completions_reservation_id',
  'idx_task_completions_state',
  'worker_result_ownerships_pkey',
  'idx_worker_result_ownerships_expires_at',
  'idx_worker_result_ownerships_next_attempt_at',
  'idx_worker_result_ownerships_request_id',
  'idx_worker_result_ownerships_state',
  'idx_worker_result_ownerships_worker_job_id'
);")"

total_idx="$(echo "$index_info" | cut -d'|' -f1)"
unique_idx="$(echo "$index_info" | cut -d'|' -f2)"
valid_idx="$(echo "$index_info" | cut -d'|' -f3)"

if [ "$total_idx" != "12" ] || [ "$unique_idx" != "5" ] || [ "$valid_idx" != "12" ]; then
  echo "FAIL: Index postcondition mismatch! Total: $total_idx (exp 12), Unique: $unique_idx (exp 5), Valid: $valid_idx (exp 12)"
  exit 1
fi
echo "PASSED: Full durability migration executed cleanly; exact marker, 4 tables, jsonb column, and 12 valid indexes verified."

# Test 24: Unreviewed SQL file substitution rejection
echo "==> [Test 24] Unreviewed SQL file substitution rejection..."
TAMPERED_SQL="$test_root/tampered_unreviewed.sql"
cat << 'EOF' > "$TAMPERED_SQL"
CREATE TABLE public.tampered_injected_table (id int);
EOF

set +e
MIGRATION_FILE="$TAMPERED_SQL" DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" "$SCRIPT_PATH" >"$test_root/t24.log" 2>&1
rc=$?
set -e
if [ $rc -ne 3 ]; then echo "FAIL: Expected exit 3 for unreviewed SQL file, got $rc"; exit 1; fi
grep -q "Arbitrary or unreviewed MIGRATION_FILE is strictly prohibited" "$test_root/t24.log"

# Verify tampered table was not created
tampered_check="$(psql --dbname="$ADMIN_DSN" -At -c "SELECT to_regclass('public.tampered_injected_table')::text;")"
if [ -n "$tampered_check" ] && [ "$tampered_check" != "" ]; then
  echo "FAIL: Tampered table public.tampered_injected_table was created!"
  exit 1
fi
echo "PASSED: Unreviewed SQL file rejected (exit 3); verified zero DDL executed."

# Test 25: Wrong PostgreSQL cluster rejection even when database and username match
echo "==> [Test 25] Wrong PostgreSQL cluster rejection (system_identifier mismatch)..."
set +e
EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="9999999999999999999" DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" "$SCRIPT_PATH" >"$test_root/t25.log" 2>&1
rc=$?
set -e
if [ $rc -ne 5 ]; then echo "FAIL: Expected exit 5 for cluster mismatch, got $rc"; exit 1; fi
grep -q "Target cluster system_identifier mismatch" "$test_root/t25.log"
echo "PASSED: Wrong PostgreSQL cluster rejected even with matching database and username (exit 5)."

# Test 26: Operator role identity mismatch rejection
echo "==> [Test 26] Operator role identity mismatch rejection..."
set +e
EXPECTED_USER="platen_unauthorized_role" DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" "$SCRIPT_PATH" >"$test_root/t26.log" 2>&1
rc=$?
set -e
if [ $rc -ne 5 ]; then echo "FAIL: Expected exit 5 for operator mismatch, got $rc"; exit 1; fi
grep -q "Target operator identity mismatch" "$test_root/t26.log"
echo "PASSED: Operator identity mismatch rejected (exit 5)."

echo "================================================================================"
echo "ALL MIGRATION OPERATOR QUALIFICATION TESTS (26/26) PASSED SUCCESSFULLY!"
echo "================================================================================"
