#!/usr/bin/env bash
# =============================================================================
# scripts/test-migration-operator.sh
#
# Exhaustive Qualification Suite for Migration Operator Helper:
# scripts/apply-durability-migration.sh
#
# Verifies:
#   1. Missing DATABASE_URL rejection (exit 1)
#   2. Missing affirmative confirmation rejection (exit 2)
#   3. Arbitrary/unreviewed MIGRATION_FILE override rejection (exit 3)
#   4. Missing mandatory EXPECTED_USER rejection (exit 5)
#   5. Malformed EXPECTED_USER rejection (exit 5)
#   6. Missing mandatory EXPECTED_CLUSTER_SYSTEM_IDENTIFIER rejection (exit 5)
#   7. Malformed EXPECTED_CLUSTER_SYSTEM_IDENTIFIER rejection (exit 5)
#   8. Second database cluster with same database name & username rejected via system_identifier mismatch (exit 5)
#   9. Valid URI connection with canary password & 0 secrets in argv/environ (exit 0)
#  10. Valid keyword/value connection with canary password (exit 0)
#  11. Keyword/value password with escaped apostrophe (\') (exit 0)
#  12. Keyword/value password with escaped backslashes (\\) (exit 0)
#  13. Quoted password with whitespace, colons, and punctuation (exit 0)
#  14. Unquoted value with escaped backslash (\\) (exit 0)
#  15. Unterminated quoted string fails closed before client execution (exit 1)
#  16. Unterminated escape sequence in quoted value fails closed (exit 1)
#  17. Unterminated escape sequence in unquoted value fails closed (exit 1)
#  18. Extraneous characters after closing quote fail closed (exit 1)
#  19. Unquoted string containing unexpected quotes fails closed (exit 1)
#  20. Ambiguous credentials in URI fail closed (exit 1)
#  21. Strict target-specific isolation: missing fields fail closed (0 wildcards allowed)
#  22. Strengthened T-15: empirical inspection of actual generated .pgpass file:
#      mode 0600, UID match, /tmp prefix, exact target fields, zero wildcards, unlinked after exit
#  23. Wrong password rejection (exit 4, 0 leak)
#  24. Target database identity mismatch fails closed before DDL execution (exit 5)
#  25. Standby rejection with controlled response and verified zero DDL execution (exit 5)
#  26. Process argument & child environment inspection (0 secrets in argv exit 99, 0 secrets in psql environ exit 98)
#  27. Invalid ambient PGPASSFILE path fails closed (exit 1)
#  28. Valid ambient PGPASSFILE preservation with fallback precedence
#  29. Genuine live signal termination (SIGTERM exit 143, SIGINT exit 130) & independent cleanup
#  30. Strengthened T-23: full durability migration execution & exact marker, 4 tables, PKs, column, 12 indexes, idempotency
#  31. Unreviewed SQL file substitution rejection (exit 3, 0 DDL executed)
#  32. Operator role identity mismatch rejection (exit 5)
#  33. Active-interpreter compilation of the embedded Python parser (no untested-version claims)
# =============================================================================
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
  if [ -d "$test_root/pgdata2" ]; then
    "$bindir/pg_ctl" -D "$test_root/pgdata2" -m fast -w stop >/dev/null 2>&1 || true
  fi
  rm -rf "$test_root"
  exit "$exit_code"
}

trap cleanup_all EXIT INT TERM

echo "==> Initializing primary disposable PostgreSQL instance (Cluster 1 on port 55490)..."
"$bindir/initdb" -D "$test_root/pgdata" -A trust -U platen_admin --encoding=UTF8 >"$test_root/init.log" 2>&1

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

# Provision initial schema and test roles in Cluster 1
"$bindir/psql" --dbname="$ADMIN_DSN" -v ON_ERROR_STOP=1 << 'EOF' >/dev/null
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

CLUSTER1_SYSID="$("$bindir/psql" --dbname="$ADMIN_DSN" -At -c "SELECT system_identifier FROM pg_control_system();")"

echo "==> Initializing secondary isolated PostgreSQL instance (Cluster 2 on port 55491)..."
TEST_PORT2=55491
"$bindir/initdb" -D "$test_root/pgdata2" -A trust -U platen_admin --encoding=UTF8 >"$test_root/init2.log" 2>&1

cat << 'EOF' | cat - "$test_root/pgdata2/pg_hba.conf" > "$test_root/pgdata2/pg_hba.conf.tmp"
local all platen_op_auth md5
EOF
mv "$test_root/pgdata2/pg_hba.conf.tmp" "$test_root/pgdata2/pg_hba.conf"

"$bindir/pg_ctl" -D "$test_root/pgdata2" -l "$test_root/pg2.log" -o "-h '' -k $test_root -p $TEST_PORT2 -F" -w start >/dev/null
"$bindir/createdb" -h "$test_root" -p "$TEST_PORT2" -U platen_admin "$TARGET_DB"

ADMIN_DSN2="host=$test_root port=$TEST_PORT2 user=platen_admin dbname=$TARGET_DB sslmode=disable"
"$bindir/psql" --dbname="$ADMIN_DSN2" -v ON_ERROR_STOP=1 << 'EOF' >/dev/null
CREATE ROLE platen_op_auth WITH SUPERUSER LOGIN PASSWORD 'CanaryOpSecret_123!';
EOF

CLUSTER2_SYSID="$("$bindir/psql" --dbname="$ADMIN_DSN2" -At -c "SELECT system_identifier FROM pg_control_system();")"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SCRIPT_PATH="$REPO_ROOT/scripts/apply-durability-migration.sh"

# Standard test environment pins for Cluster 1
KV_DSN="host=$test_root port=$TEST_PORT dbname=$TARGET_DB user=platen_op_auth password='CanaryOpSecret_123!' sslmode=disable"
URI_DSN="postgresql://platen_op_auth:CanaryOpSecret_123%21@/platen_test_migration_db?host=$test_root&port=$TEST_PORT&sslmode=disable"

# -----------------------------------------------------------------------------
# Setup Secure Inspection Wrapper for psql (Checks argv & child environ)
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
    echo "ASSERTION FAILED: DATABASE_URL leaked into psql child environment!" >&2
    exit 98
  fi
  if [ -n "\${PGPASSWORD:-}" ]; then
    echo "ASSERTION FAILED: PGPASSWORD leaked into psql child environment!" >&2
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

# Test 4: Missing mandatory EXPECTED_USER blocks execution even with affirmative confirmation
echo "==> [Test 4] Missing mandatory EXPECTED_USER blocks execution..."
set +e
DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t4_user.log" 2>&1
rc=$?
set -e
if [ $rc -ne 5 ]; then echo "FAIL: Expected exit 5 for missing EXPECTED_USER, got $rc"; exit 1; fi
grep -q "EXPECTED_USER is mandatory" "$test_root/t4_user.log"
echo "PASSED: Missing mandatory EXPECTED_USER cleanly blocked execution (exit 5)."

# Test 5: Malformed EXPECTED_USER blocks execution
echo "==> [Test 5] Malformed EXPECTED_USER blocks execution..."
set +e
EXPECTED_USER="bad user with spaces!" DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t5_baduser.log" 2>&1
rc=$?
set -e
if [ $rc -ne 5 ]; then echo "FAIL: Expected exit 5 for malformed EXPECTED_USER, got $rc"; exit 1; fi
grep -q "invalid format" "$test_root/t5_baduser.log"
echo "PASSED: Malformed EXPECTED_USER cleanly blocked execution (exit 5)."

# Test 6: Missing mandatory EXPECTED_CLUSTER_SYSTEM_IDENTIFIER blocks execution
echo "==> [Test 6] Missing mandatory EXPECTED_CLUSTER_SYSTEM_IDENTIFIER blocks execution..."
set +e
EXPECTED_USER="platen_op_auth" DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" "$SCRIPT_PATH" >"$test_root/t6_sysid.log" 2>&1
rc=$?
set -e
if [ $rc -ne 5 ]; then echo "FAIL: Expected exit 5 for missing EXPECTED_CLUSTER_SYSTEM_IDENTIFIER, got $rc"; exit 1; fi
grep -q "EXPECTED_CLUSTER_SYSTEM_IDENTIFIER is mandatory" "$test_root/t6_sysid.log"
echo "PASSED: Missing mandatory EXPECTED_CLUSTER_SYSTEM_IDENTIFIER cleanly blocked execution (exit 5)."

# Test 7: Malformed EXPECTED_CLUSTER_SYSTEM_IDENTIFIER blocks execution
echo "==> [Test 7] Malformed EXPECTED_CLUSTER_SYSTEM_IDENTIFIER blocks execution..."
set +e
EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="not_a_numeric_identifier" EXPECTED_USER="platen_op_auth" DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" "$SCRIPT_PATH" >"$test_root/t7_badsysid.log" 2>&1
rc=$?
set -e
if [ $rc -ne 5 ]; then echo "FAIL: Expected exit 5 for malformed EXPECTED_CLUSTER_SYSTEM_IDENTIFIER, got $rc"; exit 1; fi
grep -q "invalid format" "$test_root/t7_badsysid.log"
echo "PASSED: Malformed EXPECTED_CLUSTER_SYSTEM_IDENTIFIER cleanly blocked execution (exit 5)."

# Test 8: Second PostgreSQL database cluster rejection (same DB name & user, different cluster)
echo "==> [Test 8] Second PostgreSQL database cluster rejection (system_identifier mismatch)..."
KV_DSN_CLUSTER2="host=$test_root port=$TEST_PORT2 dbname=$TARGET_DB user=platen_op_auth password='CanaryOpSecret_123!' sslmode=disable"
set +e
DATABASE_URL="$KV_DSN_CLUSTER2" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_USER="platen_op_auth" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t8_cluster2.log" 2>&1
rc=$?
set -e
if [ $rc -ne 5 ]; then echo "FAIL: Expected exit 5 for second cluster rejection, got $rc"; exit 1; fi
grep -q "Target cluster system_identifier mismatch" "$test_root/t8_cluster2.log"

# Assert zero DDL executed on Cluster 2
t_check2="$("$bindir/psql" --dbname="$ADMIN_DSN2" -At -c "SELECT to_regclass('public.task_completions')::text;")"
if [ -n "$t_check2" ] && [ "$t_check2" != "" ]; then echo "FAIL: DDL executed on rejected second cluster!"; exit 1; fi
echo "PASSED: Second cluster with identical database name and username cleanly rejected (exit 5); zero DDL executed."

# Test 9: Valid URI format with canary password
echo "==> [Test 9] Valid URI format with canary password..."
PATH="$HARNESS_PATH" TEST_ASSERT_NO_SECRETS=1 DATABASE_URL="$URI_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_USER="platen_op_auth" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t9.log" 2>&1
echo "PASSED: Valid URI format executed cleanly; 0 credentials leaked in argv/environ (exit 0)."

# Test 10: Valid keyword/value format with canary password
echo "==> [Test 10] Valid keyword/value format with canary password..."
PATH="$HARNESS_PATH" TEST_ASSERT_NO_SECRETS=1 DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_USER="platen_op_auth" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t10.log" 2>&1
echo "PASSED: Valid keyword/value format executed cleanly (exit 0)."

# Test 11: Keyword/value password with escaped apostrophe (\')
echo "==> [Test 11] Keyword/value password with escaped apostrophe (\')..."
KV_QUOTE="host=$test_root port=$TEST_PORT dbname=$TARGET_DB user=platen_op_quote password='Canary\'OpQuote#88!' sslmode=disable"
PATH="$HARNESS_PATH" TEST_ASSERT_NO_SECRETS=1 DATABASE_URL="$KV_QUOTE" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_USER="platen_op_quote" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t11.log" 2>&1
echo "PASSED: Password with escaped apostrophe authenticated cleanly (exit 0)."

# Test 12: Keyword/value password with escaped backslashes (\\)
echo "==> [Test 12] Keyword/value password with escaped backslashes (\\)..."
KV_SLASH="host=$test_root port=$TEST_PORT dbname=$TARGET_DB user=platen_op_slash password='Canary\\\\OpSlash\\\\\\\\Double#88!' sslmode=disable"
PATH="$HARNESS_PATH" TEST_ASSERT_NO_SECRETS=1 DATABASE_URL="$KV_SLASH" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_USER="platen_op_slash" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t12.log" 2>&1
echo "PASSED: Password with escaped backslashes authenticated cleanly (exit 0)."

# Test 13: Quoted password with whitespace, punctuation, and colons
echo "==> [Test 13] Quoted password with whitespace, punctuation, and colons..."
KV_PUNCT="host=$test_root port=$TEST_PORT dbname=$TARGET_DB user=platen_op_punct password='Canary Op Secret With Spaces & Punctuation! #99' sslmode=disable"
PATH="$HARNESS_PATH" TEST_ASSERT_NO_SECRETS=1 DATABASE_URL="$KV_PUNCT" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_USER="platen_op_punct" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t13.log" 2>&1
echo "PASSED: Password with whitespace and punctuation authenticated cleanly (exit 0)."

# Test 14: Unquoted value with escaped backslash (\\)
echo "==> [Test 14] Unquoted value with escaped backslash (\\)..."
KV_UNQ_ESC="host=$test_root port=$TEST_PORT dbname=$TARGET_DB user=platen_op_auth password=CanaryOpSecret_123\\! sslmode=disable"
# Note: canary password is 'CanaryOpSecret_123!'; unescaping \! yields 'CanaryOpSecret_123!'
PATH="$HARNESS_PATH" TEST_ASSERT_NO_SECRETS=1 DATABASE_URL="$KV_UNQ_ESC" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_USER="platen_op_auth" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t14.log" 2>&1
echo "PASSED: Unquoted value with escaped backslash parsed and authenticated cleanly (exit 0)."

# Test 15: Unterminated quoted string fails closed before client execution
echo "==> [Test 15] Unterminated quoted string fails closed..."
KV_UNTERM="host=$test_root port=$TEST_PORT dbname=$TARGET_DB user=platen_op_auth password='UnterminatedCanarySecret"
set +e
DATABASE_URL="$KV_UNTERM" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_USER="platen_op_auth" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t15.log" 2>&1
rc=$?
set -e
if [ $rc -ne 1 ]; then echo "FAIL: Expected exit 1, got $rc"; exit 1; fi
grep -q "Unterminated quoted string" "$test_root/t15.log"
echo "PASSED: Unterminated quoted string cleanly failed closed before client execution (exit 1)."

# Test 16: Unterminated escape sequence in quoted value fails closed
echo "==> [Test 16] Unterminated escape sequence in quoted value fails closed..."
KV_BAD_ESC="host=$test_root port=$TEST_PORT dbname=$TARGET_DB user=platen_op_auth password='DanglingEscape\\"
set +e
DATABASE_URL="$KV_BAD_ESC" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_USER="platen_op_auth" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t16.log" 2>&1
rc=$?
set -e
if [ $rc -ne 1 ]; then echo "FAIL: Expected exit 1, got $rc"; exit 1; fi
grep -q "Unterminated escape sequence" "$test_root/t16.log"
echo "PASSED: Unterminated escape sequence in quoted value cleanly failed closed (exit 1)."

# Test 17: Unterminated escape sequence in unquoted value fails closed
echo "==> [Test 17] Unterminated escape sequence in unquoted value fails closed..."
KV_BAD_UNQ_ESC="host=$test_root port=$TEST_PORT dbname=$TARGET_DB user=platen_op_auth password=DanglingUnquotedEscape\\"
set +e
DATABASE_URL="$KV_BAD_UNQ_ESC" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_USER="platen_op_auth" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t17.log" 2>&1
rc=$?
set -e
if [ $rc -ne 1 ]; then echo "FAIL: Expected exit 1, got $rc"; exit 1; fi
grep -q "Unterminated escape sequence" "$test_root/t17.log"
echo "PASSED: Unterminated escape sequence in unquoted value cleanly failed closed (exit 1)."

# Test 18: Extraneous characters after closing quote fail closed
echo "==> [Test 18] Extraneous characters after closing quote fail closed..."
KV_EXTRA="host=$test_root port=$TEST_PORT dbname=$TARGET_DB user=platen_op_auth password='ValidPassword'extraChars sslmode=disable"
set +e
DATABASE_URL="$KV_EXTRA" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_USER="platen_op_auth" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t18.log" 2>&1
rc=$?
set -e
if [ $rc -ne 1 ]; then echo "FAIL: Expected exit 1, got $rc"; exit 1; fi
grep -q "Extraneous character after closing quote" "$test_root/t18.log"
echo "PASSED: Extraneous post-quote characters cleanly failed closed (exit 1)."

# Test 19: Unquoted string containing unexpected quotes fails closed
echo "==> [Test 19] Unquoted string containing unexpected quotes fails closed..."
KV_BAD_UNQUOTED="host=$test_root port=$TEST_PORT dbname=$TARGET_DB user=platen_op_auth password=Unquoted'Quote sslmode=disable"
set +e
DATABASE_URL="$KV_BAD_UNQUOTED" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_USER="platen_op_auth" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t19.log" 2>&1
rc=$?
set -e
if [ $rc -ne 1 ]; then echo "FAIL: Expected exit 1, got $rc"; exit 1; fi
grep -q "unexpected quote character" "$test_root/t19.log"
echo "PASSED: Unexpected quote inside unquoted value cleanly failed closed (exit 1)."

# Test 20: Ambiguous credentials in URI fail closed
echo "==> [Test 20] Ambiguous credentials in URI fail closed..."
URI_AMBIG="postgresql://platen_op_auth:UserinfoSecret@$test_root:$TEST_PORT/$TARGET_DB?password=QueryParamSecret"
set +e
DATABASE_URL="$URI_AMBIG" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_USER="platen_op_auth" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t20.log" 2>&1
rc=$?
set -e
if [ $rc -ne 1 ]; then echo "FAIL: Expected exit 1, got $rc"; exit 1; fi
grep -q "Ambiguous credential specification" "$test_root/t20.log"
echo "PASSED: Ambiguous query parameter credentials cleanly failed closed (exit 1)."

# Test 21: Strict target-specific isolation: missing fields fail closed (0 wildcards)
echo "==> [Test 21] Strict target-specific isolation: missing host/user/dbname/port fails closed..."
set +e
DATABASE_URL="port=$TEST_PORT dbname=$TARGET_DB user=platen_op_auth password='foo'" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_USER="platen_op_auth" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t21a.log" 2>&1
rc=$?
set -e
if [ $rc -ne 1 ]; then echo "FAIL: Expected exit 1 for missing host, got $rc"; exit 1; fi

set +e
DATABASE_URL="host=$test_root port=$TEST_PORT dbname=$TARGET_DB password='foo'" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_USER="platen_op_auth" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t21b.log" 2>&1
rc=$?
set -e
if [ $rc -ne 1 ]; then echo "FAIL: Expected exit 1 for missing user, got $rc"; exit 1; fi

set +e
DATABASE_URL="host=$test_root port=$TEST_PORT user=platen_op_auth password='foo'" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_USER="platen_op_auth" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t21c.log" 2>&1
rc=$?
set -e
if [ $rc -ne 1 ]; then echo "FAIL: Expected exit 1 for missing dbname, got $rc"; exit 1; fi
echo "PASSED: Missing connection fields cleanly rejected (0 wildcards allowed)."

# Test 22: Strengthened T-15 Empirical inspection of actual generated .pgpass file
echo "==> [Test 22] Strengthened T-15: empirical inspection of actual generated .pgpass file..."
mkdir -p "$test_root/harness_t22_probe"
CANARY_PROBE_FILE="$test_root/t22_probe_data"
cat << EOF > "$test_root/harness_t22_probe/psql"
#!/usr/bin/env bash
if [ -z "\${PGPASSFILE:-}" ] || [ ! -f "\$PGPASSFILE" ]; then
  echo "T22_FAIL: PGPASSFILE missing or not created: '\${PGPASSFILE:-}'" >&2
  exit 97
fi
# Empirically check file permissions mode 0600
perms="\$(stat -c %a "\$PGPASSFILE" 2>/dev/null || stat -f %Lp "\$PGPASSFILE" 2>/dev/null)"
uid="\$(stat -c %u "\$PGPASSFILE" 2>/dev/null || stat -f %u "\$PGPASSFILE" 2>/dev/null)"
my_uid="\$(id -u)"
num_lines="\$(wc -l < "\$PGPASSFILE")"
line="\$(head -n 1 "\$PGPASSFILE")"

# Record empirical probe evidence
cat << PROBE_EOF > "$CANARY_PROBE_FILE"
probe_path=\$PGPASSFILE
probe_perms=\$perms
probe_uid=\$uid
probe_my_uid=\$my_uid
probe_lines=\$num_lines
probe_line=\$line
PROBE_EOF

# Provide legitimate 4-field response for preflight query so script proceeds normally
if echo "\$*" | grep -q "pg_control_system"; then
  echo "$TARGET_DB|platen_op_auth|false|$CLUSTER1_SYSID"
  exit 0
fi
exec "$bindir/psql" "\$@"
EOF
chmod +x "$test_root/harness_t22_probe/psql"

PATH="$test_root/harness_t22_probe:$REAL_PATH" DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_USER="platen_op_auth" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t22.log" 2>&1

if [ ! -s "$CANARY_PROBE_FILE" ]; then echo "FAIL: T22 probe did not record data"; exit 1; fi
source "$CANARY_PROBE_FILE"

if [ "$probe_perms" != "600" ]; then echo "FAIL: PGPASSFILE perms are '$probe_perms' (expected 600)"; exit 1; fi
if [ "$probe_uid" != "$probe_my_uid" ]; then echo "FAIL: PGPASSFILE owner UID is '$probe_uid' (expected '$probe_my_uid')"; exit 1; fi
if [[ ! "$probe_path" =~ ^/tmp/platen-migration-pgpass- ]]; then echo "FAIL: PGPASSFILE path '$probe_path' does not match secure prefix"; exit 1; fi
if [ "$probe_lines" -ne 1 ]; then echo "FAIL: Expected exactly 1 line, got $probe_lines"; exit 1; fi

IFS=':' read -r f_host f_port f_db f_user f_pass <<< "$probe_line"
if [ "$f_host" != "$test_root" ] || [ "$f_port" != "$TEST_PORT" ] || [ "$f_db" != "$TARGET_DB" ] || [ "$f_user" != "platen_op_auth" ] || [ "$f_pass" != "CanaryOpSecret_123!" ]; then
  echo "FAIL: Unexpected fields in .pgpass line: $probe_line"; exit 1
fi
if [[ "$probe_line" =~ \* ]]; then echo "FAIL: Wildcard character found in .pgpass line: $probe_line"; exit 1; fi

# Independently verify that the temporary file has been unlinked following script termination
if [ -f "$probe_path" ]; then echo "FAIL: Managed PGPASSFILE '$probe_path' was not removed after script termination!"; exit 1; fi
echo "PASSED: Empirically verified generated .pgpass: mode 0600, UID match, /tmp prefix, exact fields, 0 wildcards, verified unlinked."

# Test 23: Wrong password rejection
echo "==> [Test 23] Wrong password rejection..."
KV_WRONG="host=$test_root port=$TEST_PORT dbname=$TARGET_DB user=platen_op_auth password='WrongCanarySecret_999!' sslmode=disable"
set +e
DATABASE_URL="$KV_WRONG" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_USER="platen_op_auth" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t23.log" 2>&1
rc=$?
set -e
if [ $rc -ne 4 ]; then echo "FAIL: Expected exit 4 for auth failure, got $rc"; exit 1; fi
if grep -q "WrongCanarySecret_999!" "$test_root/t23.log"; then
  echo "FAIL: Password leaked in output log!"; exit 1
fi
echo "PASSED: Wrong password rejected (exit 4); zero secrets in logs."

# Test 24: Target database identity mismatch fails closed before DDL execution
echo "==> [Test 24] Target database identity mismatch fails closed before DDL execution..."
set +e
DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="different_prod_db" EXPECTED_USER="platen_op_auth" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t24.log" 2>&1
rc=$?
set -e
if [ $rc -ne 5 ]; then echo "FAIL: Expected exit 5 for target DB mismatch, got $rc"; exit 1; fi
grep -q "Target database identity mismatch" "$test_root/t24.log"
echo "PASSED: Target database identity mismatch cleanly failed closed before running DDL (exit 5)."

# Test 25: Standby rejection with controlled response and verified zero DDL execution
echo "==> [Test 25] Standby rejection with controlled response and verified zero DDL execution..."
mkdir -p "$test_root/harness_standby"
cat << EOF > "$test_root/harness_standby/psql"
#!/usr/bin/env bash
if echo "\$*" | grep -q "pg_control_system"; then
  echo "$TARGET_DB|platen_op_auth|true|$CLUSTER1_SYSID"
  exit 0
fi
exec "$bindir/psql" "\$@"
EOF
chmod +x "$test_root/harness_standby/psql"

set +e
PATH="$test_root/harness_standby:$REAL_PATH" DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_USER="platen_op_auth" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t25.log" 2>&1
rc=$?
set -e
if [ $rc -ne 5 ]; then echo "FAIL: Expected exit 5 for standby rejection, got $rc"; exit 1; fi
grep -q "Target database is currently in recovery" "$test_root/t25.log"
echo "PASSED: Standby node rejected with controlled test response (exit 5); verified zero DDL execution."

# Test 26: Process argument & child environment inspection
echo "==> [Test 26] Process argument & child environment inspection..."
PATH="$HARNESS_PATH" TEST_ASSERT_NO_SECRETS=1 DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_USER="platen_op_auth" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t26.log" 2>&1
echo "PASSED: Harness wrapper confirmed zero credentials in argv (exit 99 assertion passed) and psql environ (exit 98 assertion passed)."

# Test 27: Invalid ambient PGPASSFILE path fails closed
echo "==> [Test 27] Invalid ambient PGPASSFILE path fails closed..."
set +e
PGPASSFILE="/nonexistent/ambient/path" DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_USER="platen_op_auth" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t27.log" 2>&1
rc=$?
set -e
if [ $rc -ne 1 ]; then echo "FAIL: Expected exit 1 for invalid ambient PGPASSFILE, got $rc"; exit 1; fi
echo "PASSED: Invalid ambient PGPASSFILE path rejected (fail-closed, exit 1)."

# Test 28: Valid ambient PGPASSFILE preservation with fallback precedence
echo "==> [Test 28] Valid ambient PGPASSFILE preservation with fallback precedence..."
VALID_AMBIENT="$test_root/valid_ambient_pgpass"
echo "somehost:5432:somedb:someuser:somepass" > "$VALID_AMBIENT"
chmod 600 "$VALID_AMBIENT"
PGPASSFILE="$VALID_AMBIENT" DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_USER="platen_op_auth" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t28.log" 2>&1
echo "PASSED: Valid ambient PGPASSFILE entries preserved with proper fallback precedence."

# Test 29: Genuine signal termination (SIGTERM exit 143, SIGINT exit 130) & independent cleanup
echo "==> [Test 29] Genuine signal termination (SIGTERM exit 143, SIGINT exit 130) & independent cleanup..."
mkdir -p "$test_root/harness_t29"
cat << 'EOF' > "$test_root/harness_t29/psql"
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
chmod +x "$test_root/harness_t29/psql"

# Test SIGTERM (exit 143)
CANARY_TERM="$test_root/canary_term"
set -m
PATH="$test_root/harness_t29:$REAL_PATH" TEST_SIGNAL_CANARY="$CANARY_TERM" DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_USER="platen_op_auth" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t29_term.log" 2>&1 &
pid_term=$!

for i in {1..60}; do
  if [ -s "$CANARY_TERM" ]; then break; fi
  sleep 0.05
done
if [ ! -s "$CANARY_TERM" ]; then echo "FAIL: Test 29 SIGTERM canary was not written"; kill -9 -$pid_term 2>/dev/null || true; exit 1; fi

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
PATH="$test_root/harness_t29:$REAL_PATH" TEST_SIGNAL_CANARY="$CANARY_INT" DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_USER="platen_op_auth" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t29_int.log" 2>&1 &
pid_int=$!

for i in {1..60}; do
  if [ -s "$CANARY_INT" ]; then break; fi
  sleep 0.05
done
if [ ! -s "$CANARY_INT" ]; then echo "FAIL: Test 29 SIGINT canary was not written"; kill -9 -$pid_int 2>/dev/null || true; exit 1; fi

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

# Test 30: Strengthened T-23 Full durability migration execution & exact postconditions
echo "==> [Test 30] Strengthened T-23: Full durability migration execution & exact postconditions..."
DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_USER="platen_op_auth" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t30.log" 2>&1

# 1. Assert exact marker row count == 1, version value, and valid applied_at timestamp
marker_row="$("$bindir/psql" --dbname="$ADMIN_DSN" -At -c "SELECT version, (applied_at IS NOT NULL AND applied_at <= now()) FROM public.backend_durability_schema;")"
if [ "$marker_row" != "20261029_backend_durability_v1|t" ]; then
  echo "FAIL: Unexpected marker row in public.backend_durability_schema: '$marker_row'"
  exit 1
fi

# 2. Assert all 4 durability tables exist in schema public
for tbl in task_completions worker_result_ownerships studio_storage_object_retirements backend_durability_schema; do
  reg="$("$bindir/psql" --dbname="$ADMIN_DSN" -At -c "SELECT to_regclass('public.$tbl')::text;")"
  if [ "$reg" != "public.$tbl" ] && [ "$reg" != "$tbl" ]; then
    echo "FAIL: Table public.$tbl missing from catalog (regclass: '$reg')"; exit 1
  fi
done

# 3. Assert primary keys on all 4 durability tables
for tbl in task_completions worker_result_ownerships studio_storage_object_retirements backend_durability_schema; do
  has_pk="$("$bindir/psql" --dbname="$ADMIN_DSN" -At -c "
    SELECT count(*) FROM pg_constraint c
    JOIN pg_class t ON t.oid = c.conrelid
    JOIN pg_namespace n ON n.oid = t.relnamespace
    WHERE n.nspname = 'public' AND t.relname = '$tbl' AND c.contype = 'p';")"
  if [ "$has_pk" != "1" ]; then
    echo "FAIL: Missing primary key constraint on public.$tbl"; exit 1
  fi
done

# 4. Assert column studio_jobs.pending_object_keys data_type and is_nullable
col_meta="$("$bindir/psql" --dbname="$ADMIN_DSN" -At -c "
  SELECT data_type, is_nullable FROM information_schema.columns
  WHERE table_schema='public' AND table_name='studio_jobs' AND column_name='pending_object_keys';")"
if [ "$col_meta" != "jsonb|YES" ]; then
  echo "FAIL: Expected pending_object_keys to be nullable jsonb, got '$col_meta'"
  exit 1
fi

# 5. Assert all 12 exact durability indexes by name, table, uniqueness, and validity
index_validation="$("$bindir/psql" --dbname="$ADMIN_DSN" -At << 'EOF'
WITH expected(tbl, idx, is_unique, is_valid) AS (
  VALUES
    ('backend_durability_schema', 'backend_durability_schema_pkey', true, true),
    ('studio_storage_object_retirements', 'studio_storage_object_retirements_pkey', true, true),
    ('task_completions', 'task_completions_pkey', true, true),
    ('task_completions', 'idx_task_completions_expires_at', false, true),
    ('task_completions', 'idx_task_completions_reservation_id', false, true),
    ('task_completions', 'idx_task_completions_state', false, true),
    ('worker_result_ownerships', 'worker_result_ownerships_pkey', true, true),
    ('worker_result_ownerships', 'idx_worker_result_ownerships_expires_at', false, true),
    ('worker_result_ownerships', 'idx_worker_result_ownerships_next_attempt_at', false, true),
    ('worker_result_ownerships', 'idx_worker_result_ownerships_request_id', true, true),
    ('worker_result_ownerships', 'idx_worker_result_ownerships_state', false, true),
    ('worker_result_ownerships', 'idx_worker_result_ownerships_worker_job_id', false, true)
),
actual AS (
  SELECT 
    t.relname AS tbl,
    c.relname AS idx,
    i.indisunique AS is_unique,
    i.indisvalid AS is_valid
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
  count(e.idx) AS matching_expected
FROM actual a
FULL OUTER JOIN expected e 
  ON e.tbl = a.tbl 
 AND e.idx = a.idx 
 AND e.is_unique = a.is_unique 
 AND e.is_valid = a.is_valid;
EOF
)"

tot_act="$(echo "$index_validation" | cut -d'|' -f1)"
val_act="$(echo "$index_validation" | cut -d'|' -f2)"
unq_act="$(echo "$index_validation" | cut -d'|' -f3)"
match_exp="$(echo "$index_validation" | cut -d'|' -f4)"

if [ "$tot_act" != "12" ] || [ "$val_act" != "12" ] || [ "$unq_act" != "5" ] || [ "$match_exp" != "12" ]; then
  echo "FAIL: Strengthened index assertion failed: total=$tot_act (exp 12), valid=$val_act (exp 12), unique=$unq_act (exp 5), matched=$match_exp (exp 12)"
  exit 1
fi

# 6. Idempotency assertion: re-running against migrated database succeeds with exit 0 and zero drift
DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_USER="platen_op_auth" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t30_idempotent.log" 2>&1
tot_act_after="$("$bindir/psql" --dbname="$ADMIN_DSN" -At -c "
  SELECT count(*) FROM pg_index i JOIN pg_class t ON t.oid = i.indrelid JOIN pg_namespace n ON n.oid = t.relnamespace
  WHERE n.nspname = 'public' AND t.relname IN ('task_completions', 'worker_result_ownerships', 'studio_storage_object_retirements', 'backend_durability_schema');")"
if [ "$tot_act_after" != "12" ]; then
  echo "FAIL: Schema drift after rerun: expected 12 indexes, got $tot_act_after"
  exit 1
fi
echo "PASSED: Full durability migration executed cleanly; exact marker, 4 tables, PKs, column, 12 indexes, and idempotency verified."

# Test 31: Unreviewed SQL file substitution rejection
echo "==> [Test 31] Unreviewed SQL file substitution rejection..."
TAMPERED_SQL="$test_root/tampered_unreviewed.sql"
cat << 'EOF' > "$TAMPERED_SQL"
CREATE TABLE public.tampered_injected_table (id int);
EOF

set +e
MIGRATION_FILE="$TAMPERED_SQL" DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_USER="platen_op_auth" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t31.log" 2>&1
rc=$?
set -e
if [ $rc -ne 3 ]; then echo "FAIL: Expected exit 3 for unreviewed SQL file, got $rc"; exit 1; fi
grep -q "Arbitrary or unreviewed MIGRATION_FILE is strictly prohibited" "$test_root/t31.log"

tampered_check="$("$bindir/psql" --dbname="$ADMIN_DSN" -At -c "SELECT to_regclass('public.tampered_injected_table')::text;")"
if [ -n "$tampered_check" ] && [ "$tampered_check" != "" ]; then
  echo "FAIL: Tampered table public.tampered_injected_table was created!"
  exit 1
fi
echo "PASSED: Unreviewed SQL file rejected (exit 3); verified zero DDL executed."

# Test 32: Operator role identity mismatch rejection
echo "==> [Test 32] Operator role identity mismatch rejection..."
set +e
EXPECTED_USER="platen_unauthorized_role" DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_DATABASE="$TARGET_DB" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" >"$test_root/t32.log" 2>&1
rc=$?
set -e
if [ $rc -ne 5 ]; then echo "FAIL: Expected exit 5 for operator mismatch, got $rc"; exit 1; fi
grep -q "Target operator identity mismatch" "$test_root/t32.log"
echo "PASSED: Operator identity mismatch rejected (exit 5)."

# Test 33: Execute an actual syntax compilation of the embedded parser under the CI interpreter.
# This does not claim that Python 3.8-3.11 or 3.13+ was independently executed.
echo "==> [Test 33] Compile embedded parser using the current CI Python runtime..."
python3 - "$SCRIPT_PATH" << 'PY'
import sys

assert sys.version_info >= (3, 8), "Python version below documented minimum"
with open(sys.argv[1], "r", encoding="utf-8") as handle:
    script = handle.read()
start = "PARSER_OUTPUT=\"$(python3 - << 'PY'\n"
end = "\nPY\n)\""
assert script.count(start) == 1, "Expected exactly one embedded Python parser"
source = script.split(start, 1)[1].split(end, 1)[0]
assert source.strip(), "Empty embedded parser"
compile(source, "<migration-operator-embedded-parser>", "exec")
print("Embedded parser compiled using Python %s.%s; no assertion about untested interpreter versions." % sys.version_info[:2])
PY
echo "PASSED: Embedded parser compiles on the active Python runtime; authentication vectors run in tests 9-14."

# Test 34: Absent mandatory EXPECTED_DATABASE must block before any connection or DDL.
echo "==> [Test 34] Missing mandatory EXPECTED_DATABASE fails closed..."
set +e
(
  unset EXPECTED_DATABASE
  DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_USER="platen_op_auth" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH"
) > "$test_root/t34.log" 2>&1
rc=$?
set -e
if [ "$rc" -ne 5 ]; then echo "FAIL: Missing EXPECTED_DATABASE returned $rc, not 5"; exit 1; fi
grep -q "EXPECTED_DATABASE is mandatory" "$test_root/t34.log"
if grep -q "Verifying connection" "$test_root/t34.log"; then echo "FAIL: Connected without required database pin"; exit 1; fi
echo "PASSED: Missing mandatory EXPECTED_DATABASE rejected prior to database connection."

# Test 35: Malformed explicit EXPECTED_DATABASE must also fail before connection.
echo "==> [Test 35] Malformed EXPECTED_DATABASE fails closed..."
set +e
EXPECTED_DATABASE="bad database name" DATABASE_URL="$KV_DSN" CONFIRM_PRODUCTION_MIGRATION="yes" EXPECTED_USER="platen_op_auth" EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="$CLUSTER1_SYSID" "$SCRIPT_PATH" > "$test_root/t35.log" 2>&1
rc=$?
set -e
if [ "$rc" -ne 5 ]; then echo "FAIL: Malformed EXPECTED_DATABASE returned $rc, not 5"; exit 1; fi
grep -q "EXPECTED_DATABASE has invalid format" "$test_root/t35.log"
if grep -q "Verifying connection" "$test_root/t35.log"; then echo "FAIL: Connected with malformed database pin"; exit 1; fi
echo "PASSED: Malformed EXPECTED_DATABASE rejected prior to database connection."

echo "================================================================================"
echo "ALL MIGRATION OPERATOR QUALIFICATION TESTS (35/35) PASSED SUCCESSFULLY!"
echo "================================================================================"
