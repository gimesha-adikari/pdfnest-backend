#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# Platen PDF — Canonical Transactional Durability Migration Operator Helper
#
# Safely executes the durability migration (config/migrations/20261029_backend_durability.up.sql)
# against the target PostgreSQL database without leaking credentials into process
# arguments (argv), general child environments, or logs.
#
# Threat Model & Operational Security Controls:
#   1. Zero credentials in command-line arguments (argv): passwords are never passed
#      as command-line parameters, preventing disclosure in global process listings
#      (ps -ef, /proc/$PID/cmdline).
#   2. Deterministic, target-specific credential matching with ZERO wildcard fallback.
#      Host, port, database, and user must be explicitly specified.
#   3. Strict libpq connection-string state-machine lexing (rejects unterminated quotes,
#      malformed escapes, and extraneous post-quote characters).
#   4. Immutable migration SQL binding: strictly executes the reviewed durability
#      migration and verifies its exact SHA-256 hash. Arbitrary SQL file overrides are
#      prohibited and rejected fail-closed.
#   5. Positive pre-execution target verification: validates cluster system_identifier,
#      target database name, operator role, and writable primary status.
#   6. Atomic session target enforcement: enforces connection identity assertions inside
#      the execution psql session itself before DDL execution to eliminate TOCTOU / target-shift.
#   7. Subprocess environment scrubbing: unsets DATABASE_URL and PGPASSWORD. Libpq reads
#      credentials exclusively from a managed, permission-restricted file (mode 0600)
#      accessible only to the operator UID.
#   8. Guaranteed cleanup across normal exits and signal terminations (SIGINT, SIGTERM).
# -----------------------------------------------------------------------------
set -euo pipefail

# Ensure PostgreSQL binaries from pg_config are in PATH if available
if command -v pg_config >/dev/null 2>&1; then
  pg_bindir="$(pg_config --bindir 2>/dev/null || true)"
  if [ -n "$pg_bindir" ] && [ -d "$pg_bindir" ]; then
    export PATH="$PATH:$pg_bindir"
  fi
fi

# -----------------------------------------------------------------------------
# Safe Lifecycle & Trap Management
# -----------------------------------------------------------------------------
MANAGED_PGPASSFILE=""
MIGRATION_RUNNER_SCRIPT=""

cleanup() {
  local exit_code=$?
  if [ -n "${MANAGED_PGPASSFILE:-}" ] && [ -f "$MANAGED_PGPASSFILE" ]; then
    rm -f "$MANAGED_PGPASSFILE"
  fi
  if [ -n "${MIGRATION_RUNNER_SCRIPT:-}" ] && [ -f "$MIGRATION_RUNNER_SCRIPT" ]; then
    rm -f "$MIGRATION_RUNNER_SCRIPT"
  fi
  exit "$exit_code"
}

trap 'cleanup' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# -----------------------------------------------------------------------------
# Parameter Validation & Affirmative Production Gate
# -----------------------------------------------------------------------------
if [ -z "${DATABASE_URL:-}" ]; then
  echo "ERROR: DATABASE_URL is required but not set." >&2
  exit 1
fi

CONFIRM_PRODUCTION_MIGRATION="${CONFIRM_PRODUCTION_MIGRATION:-false}"
if [ "$CONFIRM_PRODUCTION_MIGRATION" != "yes" ] && [ "$CONFIRM_PRODUCTION_MIGRATION" != "true" ]; then
  echo "ERROR: Affirmative confirmation required to execute durability migration." >&2
  echo "Set CONFIRM_PRODUCTION_MIGRATION=yes to authorize execution." >&2
  exit 2
fi

# -----------------------------------------------------------------------------
# Immutable Migration SQL Binding & Cryptographic Hash Verification
# -----------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
CANONICAL_MIGRATION_FILE="$REPO_ROOT/config/migrations/20261029_backend_durability.up.sql"
REVIEWED_MIGRATION_SHA256="5800307ef280515a4a8edb5dac5c65b62b3c497213d2947afae5043d09e55b6b"

# Prohibit arbitrary MIGRATION_FILE substitution in production execution path
if [ -n "${MIGRATION_FILE:-}" ]; then
  resolved_override="$(readlink -f "$MIGRATION_FILE" 2>/dev/null || realpath "$MIGRATION_FILE" 2>/dev/null || echo "$MIGRATION_FILE")"
  canonical_resolved="$(readlink -f "$CANONICAL_MIGRATION_FILE" 2>/dev/null || realpath "$CANONICAL_MIGRATION_FILE" 2>/dev/null || echo "$CANONICAL_MIGRATION_FILE")"
  if [ "$resolved_override" != "$canonical_resolved" ]; then
    echo "ERROR: Arbitrary or unreviewed MIGRATION_FILE is strictly prohibited in production execution." >&2
    echo "  Attempted file : $MIGRATION_FILE" >&2
    echo "  Reviewed file  : $CANONICAL_MIGRATION_FILE" >&2
    echo "Refusing to execute unreviewed SQL." >&2
    exit 3
  fi
fi

if [ ! -f "$CANONICAL_MIGRATION_FILE" ] || [ ! -r "$CANONICAL_MIGRATION_FILE" ]; then
  echo "ERROR: Reviewed migration file not found or unreadable: $CANONICAL_MIGRATION_FILE" >&2
  exit 3
fi

actual_sha256="$(sha256sum "$CANONICAL_MIGRATION_FILE" 2>/dev/null | awk '{print $1}')"
if [ "$actual_sha256" != "$REVIEWED_MIGRATION_SHA256" ]; then
  echo "ERROR: Durability migration file checksum verification failed!" >&2
  echo "  Expected SHA-256 : $REVIEWED_MIGRATION_SHA256" >&2
  echo "  Actual SHA-256   : $actual_sha256" >&2
  echo "Refusing to execute tampered or modified migration file." >&2
  exit 3
fi

MIGRATION_FILE="$CANONICAL_MIGRATION_FILE"

# Target identity expectations (defaults to Railway production database "railway")
EXPECTED_DATABASE="${EXPECTED_DATABASE:-railway}"
EXPECTED_USER="${EXPECTED_USER:-}"
EXPECTED_CLUSTER_SYSTEM_IDENTIFIER="${EXPECTED_CLUSTER_SYSTEM_IDENTIFIER:-}"

# -----------------------------------------------------------------------------
# Allocate Permission-Restricted PGPASSFILE
# -----------------------------------------------------------------------------
ORIGINAL_PGPASSFILE="${PGPASSFILE:-}"
MANAGED_PGPASSFILE="$(mktemp /tmp/platen-migration-pgpass-XXXXXX)"
touch "$MANAGED_PGPASSFILE"
chmod 600 "$MANAGED_PGPASSFILE"

export ORIGINAL_PGPASSFILE
export MANAGED_PGPASSFILE

# -----------------------------------------------------------------------------
# Audited libpq State-Machine Lexer & Sanitizer (Python 3.8+ stdlib)
# -----------------------------------------------------------------------------
PARSER_OUTPUT="$(python3 - << 'PY'
import os, sys, urllib.parse, re

def escape_pgpass(s):
    if s is None:
        return ""
    return str(s).replace("\\", "\\\\").replace(":", "\\:")

raw_url = os.environ.get("DATABASE_URL", "").strip()
pgpass_path = os.environ.get("MANAGED_PGPASSFILE", "")
orig_pgpass = os.environ.get("ORIGINAL_PGPASSFILE", "")

if not raw_url:
    sys.exit("DATABASE_URL is empty: fail closed.")

# Fail closed if ambient PGPASSFILE is specified but unreadable or nonexistent
if orig_pgpass:
    if not os.path.exists(orig_pgpass):
        sys.exit(f"Specified ambient PGPASSFILE does not exist: {orig_pgpass}")
    if not os.path.isfile(orig_pgpass) or not os.access(orig_pgpass, os.R_OK):
        sys.exit(f"Specified ambient PGPASSFILE is not readable: {orig_pgpass}")

user = None
password = None
host = None
port = None
dbname = None
clean_dsn = ""

if raw_url.startswith("postgres://") or raw_url.startswith("postgresql://"):
    # Standard libpq URI format
    try:
        parsed = urllib.parse.urlsplit(raw_url)
    except Exception as e:
        sys.exit(f"Malformed database URI: {e}")

    userinfo_user = urllib.parse.unquote(parsed.username) if parsed.username is not None else None
    userinfo_pass = urllib.parse.unquote(parsed.password) if parsed.password is not None else None

    try:
        q_list = urllib.parse.parse_qsl(parsed.query, keep_blank_values=True)
    except Exception as e:
        sys.exit(f"Malformed query parameters in URI: {e}")

    q_passwords = [v for k, v in q_list if k == "password"]
    if userinfo_pass is not None and len(q_passwords) > 0:
        sys.exit("Ambiguous credential specification: password provided in both userinfo and query string.")
    if len(q_passwords) > 1:
        sys.exit("Ambiguous credential specification: multiple password query parameters.")

    if userinfo_pass is not None:
        password = userinfo_pass
    elif len(q_passwords) == 1:
        password = q_passwords[0]

    user = userinfo_user
    q_users = [v for k, v in q_list if k in ("user", "username")]
    if not user and len(q_users) == 1:
        user = q_users[0]

    host = parsed.hostname
    port = str(parsed.port) if parsed.port is not None else None
    dbname = parsed.path.lstrip("/") if parsed.path else None

    q_dict = dict(q_list)
    if not host and "host" in q_dict:
        host = q_dict["host"]
    if not port and "port" in q_dict:
        port = str(q_dict["port"])
    if not dbname and "dbname" in q_dict:
        dbname = q_dict["dbname"]
    elif not dbname and "database" in q_dict:
        dbname = q_dict["database"]
    if not port:
        port = "5432"

    # Reconstruct sanitized URI without password
    clean_netloc = ""
    if user:
        clean_netloc += urllib.parse.quote(user, safe="")
        clean_netloc += "@"
    if parsed.hostname:
        clean_netloc += parsed.hostname
        if parsed.port:
            clean_netloc += f":{parsed.port}"

    clean_query = urllib.parse.urlencode([(k, v) for k, v in q_list if k != "password"])
    clean_dsn = urllib.parse.urlunsplit((
        parsed.scheme,
        clean_netloc,
        parsed.path,
        clean_query,
        parsed.fragment
    ))
else:
    # Deterministic libpq keyword/value state-machine lexer
    s = raw_url
    idx = 0
    n = len(s)
    tokens = []
    while idx < n:
        while idx < n and s[idx].isspace():
            idx += 1
        if idx >= n:
            break
        keq_match = re.match(r'^([a-zA-Z_][a-zA-Z0-9_]*)\s*=\s*', s[idx:])
        if not keq_match:
            sys.exit(f"Invalid keyword/value connection string syntax at position {idx}")
        k = keq_match.group(1).lower()
        idx += keq_match.end()
        if idx >= n:
            tokens.append((k, "", "unquoted"))
            break
        if s[idx] == "'":
            idx += 1
            val_chars = []
            closed = False
            while idx < n:
                c = s[idx]
                if c == "\\":
                    idx += 1
                    if idx >= n:
                        sys.exit("Unterminated escape sequence inside quoted value: fail closed.")
                    val_chars.append(s[idx])
                    idx += 1
                elif c == "'":
                    idx += 1
                    closed = True
                    break
                else:
                    val_chars.append(c)
                    idx += 1
            if not closed:
                sys.exit("Unterminated quoted string in connection string: fail closed.")
            if idx < n and not s[idx].isspace():
                sys.exit("Extraneous character after closing quote in connection string: fail closed.")
            v = "".join(val_chars)
            tokens.append((k, v, "quoted"))
        else:
            val_chars = []
            while idx < n and not s[idx].isspace():
                if s[idx] in ("'", '"'):
                    sys.exit("Unquoted value contains unexpected quote character: fail closed.")
                val_chars.append(s[idx])
                idx += 1
            v = "".join(val_chars)
            tokens.append((k, v, "unquoted"))

    pwd_tokens = [tok for tok in tokens if tok[0] == "password"]
    if len(pwd_tokens) > 1:
        sys.exit("Multiple password parameters in connection string: fail closed.")
    if len(pwd_tokens) == 1:
        password = pwd_tokens[0][1]

    clean_parts = []
    for k, v, mode in tokens:
        if k in ("user", "username"): user = v
        elif k == "host": host = v
        elif k == "port": port = v
        elif k in ("dbname", "database"): dbname = v
        if k != "password":
            if mode == "quoted":
                clean_parts.append(f"{k}='{v.replace('\\', '\\\\').replace('\'', '\\\'')}'")
            else:
                clean_parts.append(f"{k}={v}")

    clean_dsn = " ".join(clean_parts)

# Strict fail-closed validation of required connection attributes
if not host:
    sys.exit("Target host is missing in connection specification: wildcard fallback prohibited.")
if not user:
    sys.exit("Target user is missing in connection specification: wildcard fallback prohibited.")
if not dbname:
    sys.exit("Target dbname is missing in connection specification: wildcard fallback prohibited.")
if not port:
    sys.exit("Target port is missing in connection specification: wildcard fallback prohibited.")

# Write deterministic exact-target line to MANAGED_PGPASSFILE
entry = f"{escape_pgpass(host)}:{escape_pgpass(port)}:{escape_pgpass(dbname)}:{escape_pgpass(user)}:{escape_pgpass(password or '')}\n"
with open(pgpass_path, "w") as f:
    f.write(entry)

# Append ambient PGPASSFILE entries with lower precedence if verified
if orig_pgpass:
    with open(orig_pgpass, "r") as src, open(pgpass_path, "a") as dst:
        dst.write(src.read())

print(clean_dsn)
PY
)" || {
  echo "ERROR: Failed to safely parse connection specification." >&2
  exit 1
}

CLEAN_DSN="$PARSER_OUTPUT"

# -----------------------------------------------------------------------------
# Subprocess Environment Scrubbing
# -----------------------------------------------------------------------------
# Explicitly unset raw credential variables to prevent child process inheritance.
# PostgreSQL libpq clients read credentials securely from MANAGED_PGPASSFILE (mode 0600).
unset DATABASE_URL
unset PGPASSWORD
export PGPASSFILE="$MANAGED_PGPASSFILE"

# -----------------------------------------------------------------------------
# Positive Pre-Execution Target Cluster, Database & Operator Verification
# -----------------------------------------------------------------------------
echo "==> Verifying connection and positive target database identity..."
IDENTITY_INFO="$(psql "$CLEAN_DSN" -At -c "SELECT current_database(), current_user, pg_catalog.pg_is_in_recovery()::text, coalesce((SELECT system_identifier::text FROM pg_control_system()), '');" 2>&1)" || {
  echo "ERROR: Database target identity check failed to connect or authenticate." >&2
  echo "$IDENTITY_INFO" >&2
  exit 4
}

# Parse strict 4-field output (db|user|recovery|sysid)
ACTUAL_DB="$(echo "$IDENTITY_INFO" | cut -d'|' -f1)"
ACTUAL_USER="$(echo "$IDENTITY_INFO" | cut -d'|' -f2)"
IS_RECOVERY="$(echo "$IDENTITY_INFO" | cut -d'|' -f3)"
ACTUAL_SYSID="$(echo "$IDENTITY_INFO" | cut -d'|' -f4)"
FIELD_COUNT="$(echo "$IDENTITY_INFO" | awk -F'|' '{print NF}')"

if [ "$FIELD_COUNT" -ne 4 ] || [ -z "$ACTUAL_DB" ] || [ -z "$ACTUAL_USER" ] || [ -z "$IS_RECOVERY" ]; then
  echo "ERROR: Target verification returned malformed or unexpected result." >&2
  echo "  Raw output : $IDENTITY_INFO" >&2
  echo "  Field count: $FIELD_COUNT (expected 4)" >&2
  exit 5
fi

# Reject standby replica status ('t' or 'true')
if [ "$IS_RECOVERY" = "t" ] || [ "$IS_RECOVERY" = "true" ]; then
  echo "ERROR: Target database is currently in recovery (standby/read-only replica: '$IS_RECOVERY'). Refusing DDL execution." >&2
  exit 5
fi

# Reject any malformed, empty or unexpected recovery state (must strictly be 'f' or 'false')
if [ "$IS_RECOVERY" != "f" ] && [ "$IS_RECOVERY" != "false" ]; then
  echo "ERROR: Target database recovery check returned unexpected state: '$IS_RECOVERY'." >&2
  exit 5
fi

# Positively verify database name
if [ "$ACTUAL_DB" != "$EXPECTED_DATABASE" ]; then
  echo "ERROR: Target database identity mismatch!" >&2
  echo "  Expected database : $EXPECTED_DATABASE" >&2
  echo "  Connected database: $ACTUAL_DB (user: $ACTUAL_USER)" >&2
  echo "Refusing to apply migrations to unconfirmed database." >&2
  exit 5
fi

# Positively verify operator role if specified
if [ -n "$EXPECTED_USER" ] && [ "$ACTUAL_USER" != "$EXPECTED_USER" ]; then
  echo "ERROR: Target operator identity mismatch!" >&2
  echo "  Expected operator : $EXPECTED_USER" >&2
  echo "  Connected operator: $ACTUAL_USER" >&2
  echo "Refusing to apply migrations under unconfirmed operator identity." >&2
  exit 5
fi

# Positively verify cluster system identifier if specified
if [ -n "$EXPECTED_CLUSTER_SYSTEM_IDENTIFIER" ] && [ "$ACTUAL_SYSID" != "$EXPECTED_CLUSTER_SYSTEM_IDENTIFIER" ]; then
  echo "ERROR: Target cluster system_identifier mismatch!" >&2
  echo "  Expected cluster identifier : $EXPECTED_CLUSTER_SYSTEM_IDENTIFIER" >&2
  echo "  Connected cluster identifier: $ACTUAL_SYSID" >&2
  echo "Refusing DDL execution against unconfirmed cluster." >&2
  exit 5
fi

echo "==> Positively verified target cluster (system_identifier: '${ACTUAL_SYSID:-unknown}'), database: '$ACTUAL_DB', operator: '$ACTUAL_USER', primary: true."

# -----------------------------------------------------------------------------
# Atomic Within-Session Verification & Migration Execution
# -----------------------------------------------------------------------------
# Enforce that the target cannot change between preflight verification and DDL execution.
# The runner script asserts session-level connection properties within the execution connection itself.
MIGRATION_RUNNER_SCRIPT="$(mktemp /tmp/platen-migration-runner-XXXXXX.sql)"
touch "$MIGRATION_RUNNER_SCRIPT"
chmod 600 "$MIGRATION_RUNNER_SCRIPT"

escaped_db="${ACTUAL_DB//\'/\'\'}"
escaped_user="${ACTUAL_USER//\'/\'\'}"
escaped_sysid="${ACTUAL_SYSID//\'/\'\'}"

cat << EOF > "$MIGRATION_RUNNER_SCRIPT"
\set ON_ERROR_STOP on

-- Atomic within-session target verification (eliminates TOCTOU / target-change boundary)
DO \$\$
DECLARE
  v_db text := current_database();
  v_user text := current_user;
  v_rec boolean := pg_catalog.pg_is_in_recovery();
  v_sysid text := coalesce((SELECT system_identifier::text FROM pg_control_system()), '');
BEGIN
  IF v_db IS DISTINCT FROM '$escaped_db' THEN
    RAISE EXCEPTION 'Session target database changed between preflight and execution: % != %', v_db, '$escaped_db';
  END IF;
  IF v_user IS DISTINCT FROM '$escaped_user' THEN
    RAISE EXCEPTION 'Session target operator changed between preflight and execution: % != %', v_user, '$escaped_user';
  END IF;
  IF '$escaped_sysid' <> '' AND v_sysid IS DISTINCT FROM '$escaped_sysid' THEN
    RAISE EXCEPTION 'Session target cluster changed between preflight and execution: % != %', v_sysid, '$escaped_sysid';
  END IF;
  IF v_rec THEN
    RAISE EXCEPTION 'Session target transitioned to standby recovery mode; refusing DDL execution';
  END IF;
END \$\$;

\i '$MIGRATION_FILE'
EOF

echo "==> Executing verified durability migration in an atomic session..."
psql "$CLEAN_DSN" \
  -v ON_ERROR_STOP=1 \
  -f "$MIGRATION_RUNNER_SCRIPT"

echo "==> Production durability migration successfully executed without credential disclosure."
