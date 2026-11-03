#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# Platen PDF — Canonical Transactional Durability Migration Operator Helper
#
# Safely executes the durability migration (config/migrations/20261029_backend_durability.up.sql)
# against the target PostgreSQL database without leaking credentials into process
# arguments (argv), child environments, or logs.
#
# Requirements & Enforcements:
#   1. Zero credentials in command-line arguments (uses permission-restricted PGPASSFILE mode 0600).
#   2. Deterministic, target-specific credential matching with ZERO wildcard fallback.
#   3. Strict libpq connection-string state-machine lexing (rejects unterminated quotes,
#      malformed escapes, and extraneous post-quote characters).
#   4. Positive pre-execution target database identity verification.
#   5. Subprocess environment scrubbing (unsets DATABASE_URL and PGPASSWORD).
#   6. Fail-closed ambient PGPASSFILE validation (rejects unreadable/missing paths).
#   7. Guaranteed cleanup across normal exits and signal terminations (SIGINT, SIGTERM).
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

cleanup() {
  local exit_code=$?
  if [ -n "${MANAGED_PGPASSFILE:-}" ] && [ -f "$MANAGED_PGPASSFILE" ]; then
    rm -f "$MANAGED_PGPASSFILE"
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

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
MIGRATION_FILE="${MIGRATION_FILE:-$REPO_ROOT/config/migrations/20261029_backend_durability.up.sql}"

if [ ! -f "$MIGRATION_FILE" ] || [ ! -r "$MIGRATION_FILE" ]; then
  echo "ERROR: Migration file not found or unreadable: $MIGRATION_FILE" >&2
  exit 3
fi

# Expected database identity defaults to authoritative Railway production DB "railway"
EXPECTED_DATABASE="${EXPECTED_DATABASE:-railway}"

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

    query_passwords = [v for k, v in q_list if k.lower() in ("password", "passwd")]
    if len(query_passwords) > 1:
        sys.exit("Ambiguous credentials: multiple password query parameters in URI: fail closed.")
    query_pass = query_passwords[0] if query_passwords else None

    if userinfo_pass is not None and query_pass is not None:
        sys.exit("Ambiguous credentials: password specified in both userinfo and query parameters: fail closed.")

    password = userinfo_pass if userinfo_pass is not None else query_pass
    user = userinfo_user

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
    if not port:
        port = "5432"

    # Reconstruct sanitized URI without credentials in netloc or query
    clean_q_list = [(k, v) for k, v in q_list if k.lower() not in ("password", "passwd")]
    clean_query = urllib.parse.urlencode(clean_q_list, safe="/:")

    host_port = parsed.hostname or ""
    if parsed.port is not None:
        host_port = f"{host_port}:{parsed.port}"
    if user:
        clean_netloc = f"{urllib.parse.quote(user)}@{host_port}"
    else:
        clean_netloc = host_port

    clean_dsn = urllib.parse.urlunsplit((parsed.scheme, clean_netloc, parsed.path, clean_query, parsed.fragment))

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
        if k == "user": user = v
        elif k == "host": host = v
        elif k == "port": port = v
        elif k == "dbname": dbname = v
        if k != "password":
            if mode == "quoted":
                clean_parts.append(f"{k}='{v.replace('\\', '\\\\').replace('\'', '\\\'')}'")
            else:
                clean_parts.append(f"{k}={v}")
    clean_dsn = " ".join(clean_parts)
    if not port:
        port = "5432"

# Strict Target-Specific Matching: Zero Wildcard Fallback Allowed
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
# Explicitly unset raw credential variables to prevent child process inheritance
unset DATABASE_URL
unset PGPASSWORD
export PGPASSFILE="$MANAGED_PGPASSFILE"

# -----------------------------------------------------------------------------
# Positive Pre-Execution Target Database Identity Verification
# -----------------------------------------------------------------------------
echo "==> Verifying connection and positive target database identity..."
IDENTITY_INFO="$(psql "$CLEAN_DSN" -At -c "SELECT current_database(), current_user, pg_catalog.pg_is_in_recovery()::text;" 2>&1)" || {
  echo "ERROR: Database target identity check failed to connect or authenticate." >&2
  echo "$IDENTITY_INFO" >&2
  exit 4
}

ACTUAL_DB="$(echo "$IDENTITY_INFO" | cut -d'|' -f1)"
ACTUAL_USER="$(echo "$IDENTITY_INFO" | cut -d'|' -f2)"
IS_RECOVERY="$(echo "$IDENTITY_INFO" | cut -d'|' -f3)"

if [ "$ACTUAL_DB" != "$EXPECTED_DATABASE" ]; then
  echo "ERROR: Target database identity mismatch!" >&2
  echo "  Expected database : $EXPECTED_DATABASE" >&2
  echo "  Connected database: $ACTUAL_DB (user: $ACTUAL_USER)" >&2
  echo "Refusing to apply migrations to unconfirmed database." >&2
  exit 5
fi

if [ "$IS_RECOVERY" = "t" ]; then
  echo "ERROR: Target database is currently in recovery (standby/read-only replica). Refusing DDL execution." >&2
  exit 5
fi

echo "==> Positively verified target database '$ACTUAL_DB' (operator: '$ACTUAL_USER', primary: true)."

# -----------------------------------------------------------------------------
# Execute Transactional Migration Script
# -----------------------------------------------------------------------------
echo "==> Executing durability migration script in a single transaction..."
psql "$CLEAN_DSN" \
  -v ON_ERROR_STOP=1 \
  -f "$MIGRATION_FILE"

echo "==> Production durability migration successfully executed without credential disclosure."
