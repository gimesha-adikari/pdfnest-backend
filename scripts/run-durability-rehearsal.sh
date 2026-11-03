#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# Platen PDF — Durability Migration Production Snapshot & Disposable Rehearsal
#
# Performs a streaming AES-256 encrypted snapshot of the production database
# directly into encryption (zero plaintext dump on disk) and executes a live
# restore, integrity verification, and migration dry run against an isolated,
# verified non-production disposable PostgreSQL instance.
#
# Usage:
#   DATABASE_URL="postgres://..." \
#   REHEARSAL_PGHOST="localhost" \
#   REHEARSAL_PGPORT="5432" \
#   REHEARSAL_PGUSER="rehearsal_operator" \
#   REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
#   scripts/run-durability-rehearsal.sh
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
# Safe Automated Cleanup Trap & Lifecycle State
# -----------------------------------------------------------------------------
MANAGED_PGPASSFILE=""
REHEARSAL_DB_CREATED_BY_US=false
REHEARSAL_MAINTENANCE_DSN=""
REHEARSAL_DB="${REHEARSAL_DB:-}"
TOC_FILE_CREATED=false
REHEARSAL_TOC_FILE=""
ENCRYPTED_BACKUP=""
BACKUP_PASSPHRASE_FILE="${BACKUP_PASSPHRASE_FILE:-}"

# Exit status reserved for a rehearsal whose checks may have passed but whose
# restored-production-data database could not be positively removed.
TEARDOWN_FAILURE_EXIT_CODE=21

# Independently confirms, via the rehearsal catalog, that the rehearsal database
# no longer exists. Returns 0 ONLY when pg_database positively reports zero
# matching rows; query errors, connection failures and any other output fail closed.
verify_rehearsal_db_absent() {
  local remaining
  remaining=$(psql -d "$REHEARSAL_MAINTENANCE_DSN" -X -t -A -v ON_ERROR_STOP=1 \
    -v target_db="$REHEARSAL_DB" 2>&1 << 'EOF'
SELECT count(*) FROM pg_catalog.pg_database WHERE datname = :'target_db';
EOF
) || { echo "ERROR: Could not query rehearsal catalog to verify teardown: $remaining" >&2; return 1; }
  [ "$remaining" = "0" ]
}

cleanup() {
  local exit_code=$?
  # Prevent re-entry (INT/TERM handler followed by EXIT) and never let a failing
  # teardown command abort the remaining cleanup and reporting steps.
  trap - EXIT INT TERM
  set +e
  local teardown_failed=false
  local drop_output drop_rc
  echo "==> Rehearsal cleanup initiated (exit code: $exit_code)..."

  # POSITIVE OWNERSHIP SAFETY:
  # Only drop the rehearsal database if this specific execution positively created it!
  # Preexisting databases are NEVER dropped. No --force / session termination is used,
  # so cleanup never requires privileges beyond ownership of the rehearsal database.
  if [ "$REHEARSAL_DB_CREATED_BY_US" = "true" ] && [ -n "$REHEARSAL_MAINTENANCE_DSN" ] && [ -n "$REHEARSAL_DB" ]; then
    echo "==> Dropping rehearsal database '$REHEARSAL_DB' created by this execution..."
    drop_output=$(dropdb --maintenance-db="$REHEARSAL_MAINTENANCE_DSN" --if-exists "$REHEARSAL_DB" 2>&1)
    drop_rc=$?
    if [ "$drop_rc" -ne 0 ]; then
      echo "ERROR: dropdb failed for rehearsal database '$REHEARSAL_DB' (exit $drop_rc): $drop_output" >&2
      teardown_failed=true
    fi

    # FAIL-CLOSED TEARDOWN VERIFICATION:
    # Independently confirm absence regardless of what dropdb reported.
    if verify_rehearsal_db_absent; then
      echo "==> Teardown verified: rehearsal database '$REHEARSAL_DB' no longer exists on the rehearsal target."
      REHEARSAL_DB_CREATED_BY_US=false
    else
      teardown_failed=true
    fi

    if [ "$teardown_failed" = "true" ]; then
      {
        echo "================================================================================"
        echo "CONTAINMENT REQUIRED: REHEARSAL DATABASE TEARDOWN FAILED"
        echo "  The rehearsal database may still contain restored PRODUCTION data that is"
        echo "  NOT encrypted at rest by this procedure."
        echo "  Remaining database : $REHEARSAL_DB"
        echo "  Rehearsal target   : host=$REHEARSAL_PGHOST port=$REHEARSAL_PGPORT user=$REHEARSAL_PGUSER"
        echo "  Cluster identity   : system_identifier=${rehearsal_sysid:-unknown} data_directory=${rehearsal_data_dir:-unknown}"
        echo "  Remediation        : terminate remaining sessions connected to '$REHEARSAL_DB', drop it"
        echo "                       on the rehearsal target only, and confirm that"
        echo "                       SELECT count(*) FROM pg_database WHERE datname = '$REHEARSAL_DB';"
        echo "                       returns 0 before treating this rehearsal as contained."
        echo "  The rehearsal result is FAILED even if restore and migration checks passed."
        echo "================================================================================"
      } >&2
    fi
  fi

  # Clean up dedicated managed PGPASSFILE if created by this execution
  if [ -n "$MANAGED_PGPASSFILE" ] && [ -f "$MANAGED_PGPASSFILE" ]; then
    rm -f "$MANAGED_PGPASSFILE" 2>/dev/null || true
  fi

  # Clean up ephemeral TOC list file if created by us
  if [ "$TOC_FILE_CREATED" = "true" ] && [ -n "$REHEARSAL_TOC_FILE" ] && [ -f "$REHEARSAL_TOC_FILE" ]; then
    rm -f "$REHEARSAL_TOC_FILE" 2>/dev/null || true
  fi

  # CRITICAL RECOVERY ASSET PRESERVATION:
  # The recovery backup ($ENCRYPTED_BACKUP) and key material ($BACKUP_PASSPHRASE_FILE)
  # are NEVER deleted by this script, neither on failure nor on success.
  # Recovery assets are retained for owner-approved verification, disaster recovery,
  # or separate owner-approved disposal.
  if [ -n "$ENCRYPTED_BACKUP" ] && [ -f "$ENCRYPTED_BACKUP" ]; then
    echo "==> Production recovery backup preserved at: $ENCRYPTED_BACKUP"
    echo "==> Recovery encryption key preserved at:   $BACKUP_PASSPHRASE_FILE"
  fi

  # A teardown failure always forces a nonzero overall result.
  if [ "$teardown_failed" = "true" ] && [ "$exit_code" -eq 0 ]; then
    exit_code=$TEARDOWN_FAILURE_EXIT_CODE
  fi

  if [ "$teardown_failed" = "true" ]; then
    echo "==> REHEARSAL FAILED (exit $exit_code): rehearsal database teardown could not be verified. Backup and recovery key are retained. CONTAINMENT REQUIRED on the rehearsal target." >&2
  elif [ "$exit_code" -ne 0 ]; then
    echo "==> REHEARSAL FAILED. Backup and recovery key are retained. Ephemeral rehearsal target cleaned up." >&2
  else
    echo "==> REHEARSAL SUCCEEDED: all checks passed and rehearsal database absence was verified."
  fi

  exit "$exit_code"
}
trap cleanup EXIT INT TERM

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

# -----------------------------------------------------------------------------
# Credential Safety: Production Source & Rehearsal libpq PGPASSFILE Management
# -----------------------------------------------------------------------------
# Passwords must NEVER be passed in command line arguments (DSN parameters or URIs).
# Both production and rehearsal credentials are managed via a dedicated, permission-
# restricted PGPASSFILE (mode 0600) scoped to this rehearsal execution.
ORIGINAL_PGPASSFILE="${PGPASSFILE:-}"
MANAGED_PGPASSFILE="$(mktemp /tmp/platen-rehearsal-pgpass-XXXXXX)"
touch "$MANAGED_PGPASSFILE"
chmod 600 "$MANAGED_PGPASSFILE"
export MANAGED_PGPASSFILE
export ORIGINAL_PGPASSFILE
export REHEARSAL_PGHOST
export REHEARSAL_PGPORT
export REHEARSAL_PGUSER
export REHEARSAL_PGPASSWORD

# Sanitize DATABASE_URL: extract production and rehearsal credentials into MANAGED_PGPASSFILE,
# using deterministic target-specific entries (zero cross-target wildcards for host or port),
# properly escaping libpq special characters (\ and :), sanitizing all URI credential locations,
# preserving original PGPASSFILE entries with documented fallback precedence, and failing closed
# on ambiguous or malformed credential specifications.
PROD_CLEAN_DSN="$(python3 - << 'PY'
import os, urllib.parse, re, sys

def escape_pgpass(s):
    if s is None:
        return ""
    return str(s).replace("\\", "\\\\").replace(":", "\\:")

raw_url = os.environ.get("DATABASE_URL", "").strip()
pgpass_path = os.environ.get("MANAGED_PGPASSFILE", "")
r_host = os.environ.get("REHEARSAL_PGHOST", "").strip()
r_port = os.environ.get("REHEARSAL_PGPORT", "").strip()
r_user = os.environ.get("REHEARSAL_PGUSER", "").strip()
r_pass = os.environ.get("REHEARSAL_PGPASSWORD", "")
orig_pgpass = os.environ.get("ORIGINAL_PGPASSFILE", "").strip()

if not raw_url:
    sys.stderr.write("FATAL: DATABASE_URL is not set or empty.\n")
    sys.exit(1)

prod_password = None
clean_prod_dsn = None
prod_host = None
prod_port = None
prod_dbname = None
prod_user = None

if raw_url.startswith(("postgres://", "postgresql://")):
    try:
        parsed = urllib.parse.urlsplit(raw_url)
    except Exception as e:
        sys.stderr.write(f"FATAL: Malformed DATABASE_URL URI: {e}\n")
        sys.exit(1)

    userinfo_pass = urllib.parse.unquote(parsed.password) if parsed.password is not None else None
    userinfo_user = urllib.parse.unquote(parsed.username) if parsed.username is not None else None

    try:
        q_list = urllib.parse.parse_qsl(parsed.query, keep_blank_values=True)
    except Exception as e:
        sys.stderr.write(f"FATAL: Malformed query parameters in DATABASE_URL: {e}\n")
        sys.exit(1)

    query_passwords = [v for k, v in q_list if k.lower() in ("password", "passwd")]
    if len(query_passwords) > 1:
        sys.stderr.write("FATAL: Ambiguous credentials: multiple password query parameters in DATABASE_URL\n")
        sys.exit(1)

    query_pass = query_passwords[0] if query_passwords else None

    if userinfo_pass is not None and query_pass is not None:
        sys.stderr.write("FATAL: Ambiguous credentials: password specified in both userinfo and query parameters in DATABASE_URL\n")
        sys.exit(1)

    prod_password = userinfo_pass if userinfo_pass is not None else query_pass

    query_users = [v for k, v in q_list if k.lower() == "user"]
    if len(query_users) > 1:
        sys.stderr.write("FATAL: Ambiguous parameters: multiple user query parameters in DATABASE_URL\n")
        sys.exit(1)
    query_user = query_users[0] if query_users else None

    if userinfo_user is not None and query_user is not None and userinfo_user != query_user:
        sys.stderr.write("FATAL: Ambiguous parameters: conflicting user values in userinfo and query parameters\n")
        sys.exit(1)

    prod_user = userinfo_user if userinfo_user is not None else query_user

    prod_host = parsed.hostname
    prod_port = parsed.port
    prod_dbname = parsed.path.lstrip("/") if parsed.path else None

    q_dict = dict(q_list)
    if not prod_host and "host" in q_dict:
        prod_host = q_dict["host"]
    if prod_port is None and "port" in q_dict:
        try:
            prod_port = int(q_dict["port"])
        except ValueError:
            sys.stderr.write("FATAL: Invalid port parameter in DATABASE_URL\n")
            sys.exit(1)
    if not prod_dbname and "dbname" in q_dict:
        prod_dbname = q_dict["dbname"]

    # Build clean query removing all password occurrences
    clean_q_list = [(k, v) for k, v in q_list if k.lower() not in ("password", "passwd")]
    clean_query = urllib.parse.urlencode(clean_q_list, safe="/:")

    host_port = parsed.hostname or ""
    if parsed.port is not None:
        host_port = f"{host_port}:{parsed.port}"
    if userinfo_user:
        new_user = urllib.parse.quote(userinfo_user)
        clean_netloc = f"{new_user}@{host_port}"
    else:
        clean_netloc = host_port

    clean_prod_dsn = urllib.parse.urlunsplit((parsed.scheme, clean_netloc, parsed.path, clean_query, ""))

else:
    # Keyword / value format according to PostgreSQL libpq connection-string grammar
    def parse_libpq_dsn(dsn):
        i = 0
        n = len(dsn)
        tokens = []  # list of (key, value, raw_param_str)

        while i < n:
            while i < n and dsn[i].isspace():
                i += 1
            if i >= n:
                break

            key_start = i
            while i < n and not dsn[i].isspace() and dsn[i] != '=':
                if dsn[i] in ("'", '"', '\\'):
                    raise ValueError(f"Invalid character {dsn[i]!r} in parameter keyword")
                i += 1
            key = dsn[key_start:i]
            if not key:
                raise ValueError("Empty keyword in connection string")

            while i < n and dsn[i].isspace():
                i += 1

            if i >= n or dsn[i] != '=':
                raise ValueError(f"Missing '=' after {key!r} in connection string")
            i += 1  # skip '='

            while i < n and dsn[i].isspace():
                i += 1

            val_start = i
            if i < n and dsn[i] == "'":
                # Quoted value: handles escaped quotes (\') and escaped backslashes (\\)
                i += 1
                val_chars = []
                while i < n:
                    c = dsn[i]
                    if c == '\\':
                        i += 1
                        if i >= n:
                            raise ValueError("Unterminated escape sequence in quoted value")
                        val_chars.append(dsn[i])
                        i += 1
                    elif c == "'":
                        i += 1
                        break
                    else:
                        val_chars.append(c)
                        i += 1
                else:
                    raise ValueError("Unterminated quoted string in connection string")

                if i < n and not dsn[i].isspace():
                    raise ValueError(f"Unexpected character {dsn[i]!r} after closing quote in connection string")
                val = ''.join(val_chars)
                raw_token = dsn[key_start:i]
            else:
                # Unquoted value: sequence of characters up to next whitespace
                val_chars = []
                while i < n and not dsn[i].isspace():
                    if dsn[i] == "'":
                        raise ValueError("Unescaped single quote in unquoted value in connection string")
                    val_chars.append(dsn[i])
                    i += 1
                val = ''.join(val_chars)
                raw_token = dsn[key_start:i]

            tokens.append((key, val, raw_token))

        return tokens

    try:
        dsn_tokens = parse_libpq_dsn(raw_url)
    except Exception as e:
        sys.stderr.write(f"FATAL: Unsupported or ambiguous DATABASE_URL format: {e}\n")
        sys.exit(1)

    pw_tokens = [tok for tok in dsn_tokens if tok[0].lower() in ("password", "passwd")]
    if len(pw_tokens) > 1:
        sys.stderr.write("FATAL: Ambiguous credentials: multiple password parameters in DSN\n")
        sys.exit(1)

    if pw_tokens:
        prod_password = pw_tokens[0][1]
    else:
        prod_password = None

    clean_tokens = [tok for tok in dsn_tokens if tok[0].lower() not in ("password", "passwd")]
    clean_prod_dsn = " ".join(tok[2] for tok in clean_tokens)

    def get_dsn_val(k):
        matches = [tok[1] for tok in dsn_tokens if tok[0].lower() == k.lower()]
        if len(matches) > 1:
            sys.stderr.write(f"FATAL: Ambiguous parameters: multiple {k} parameters in DSN\n")
            sys.exit(1)
        return matches[0] if matches else None

    prod_host = get_dsn_val("host")
    port_val = get_dsn_val("port")
    if port_val is not None:
        try:
            prod_port = int(port_val)
        except ValueError:
            sys.stderr.write("FATAL: Invalid port parameter in DSN\n")
            sys.exit(1)
    prod_user = get_dsn_val("user")
    prod_dbname = get_dsn_val("dbname")

    if not (prod_host or prod_user or prod_dbname):
        sys.stderr.write("FATAL: Unsupported or ambiguous DATABASE_URL format\n")
        sys.exit(1)

# Fail closed if password appears anywhere in clean_prod_dsn
if not clean_prod_dsn.startswith(("postgres://", "postgresql://")):
    try:
        check_tokens = parse_libpq_dsn(clean_prod_dsn)
        if any(tok[0].lower() in ("password", "passwd") for tok in check_tokens):
            sys.stderr.write("FATAL: Failed to sanitize password from production connection parameters\n")
            sys.exit(1)
    except Exception as e:
        sys.stderr.write(f"FATAL: Failed to validate sanitized production connection parameters: {e}\n")
        sys.exit(1)

if re.search(r"(?:^|[\s?&])password\s*=", clean_prod_dsn, re.IGNORECASE):
    sys.stderr.write("FATAL: Failed to sanitize password from production connection parameters\n")
    sys.exit(1)

# Build deterministic, target-specific .pgpass entries (NO WILDCARDS FOR HOST OR PORT)
pgpass_lines = []

# 1. Target-Specific Production Entries
if prod_password is not None:
    p_u = escape_pgpass(prod_user or "*")
    p_p = escape_pgpass(prod_password)
    p_db = escape_pgpass(prod_dbname or "*")
    p_port = str(prod_port) if prod_port is not None else "5432"
    p_h = prod_host or "localhost"

    pgpass_lines.append(f"{escape_pgpass(p_h)}:{p_port}:{p_db}:{p_u}:{p_p}\n")
    if p_h.startswith("/") or p_h.startswith("."):
        pgpass_lines.append(f"localhost:{p_port}:{p_db}:{p_u}:{p_p}\n")
    elif p_h in ("localhost", "127.0.0.1"):
        pgpass_lines.append(f"127.0.0.1:{p_port}:{p_db}:{p_u}:{p_p}\n")
        pgpass_lines.append(f"localhost:{p_port}:{p_db}:{p_u}:{p_p}\n")

# 2. Target-Specific Rehearsal Entries
if r_pass:
    r_u = escape_pgpass(r_user or "*")
    r_p = escape_pgpass(r_pass)
    r_port_str = str(r_port) if r_port else "5432"
    r_h_str = r_host or "localhost"

    pgpass_lines.append(f"{escape_pgpass(r_h_str)}:{r_port_str}:*:{r_u}:{r_p}\n")
    if r_h_str.startswith("/") or r_h_str.startswith("."):
        pgpass_lines.append(f"localhost:{r_port_str}:*:{r_u}:{r_p}\n")
    elif r_h_str in ("localhost", "127.0.0.1"):
        pgpass_lines.append(f"127.0.0.1:{r_port_str}:*:{r_u}:{r_p}\n")
        pgpass_lines.append(f"localhost:{r_port_str}:*:{r_u}:{r_p}\n")

# 3. Preserve Preexisting Ambient PGPASSFILE Entries (Documented Fallback Precedence)
if orig_pgpass:
    if not os.path.exists(orig_pgpass) or not os.path.isfile(orig_pgpass):
        sys.stderr.write(f"FATAL: Specified PGPASSFILE does not exist or is not a regular file: {orig_pgpass}\n")
        sys.exit(1)
    if not os.access(orig_pgpass, os.R_OK):
        sys.stderr.write(f"FATAL: Specified PGPASSFILE is not readable: {orig_pgpass}\n")
        sys.exit(1)
    try:
        with open(orig_pgpass, "r", encoding="utf-8", errors="replace") as f:
            for line in f:
                if line.strip():
                    pgpass_lines.append(line if line.endswith("\n") else line + "\n")
    except Exception as e:
        sys.stderr.write(f"FATAL: Failed to read existing PGPASSFILE: {e}\n")
        sys.exit(1)

if pgpass_path:
    with open(pgpass_path, "w", encoding="utf-8") as f:
        f.writelines(pgpass_lines)

sys.stdout.write(clean_prod_dsn)
PY
)"
py_status=$?

if [ $py_status -ne 0 ] || [ -z "$PROD_CLEAN_DSN" ]; then
  echo "FATAL: Failed to process database connection parameters (exit code: $py_status)." >&2
  exit 1
fi

# Scrub raw credentials and ambient password variables from process environment
# to prevent subprocess inheritance and prevent PGPASSWORD from overriding PGPASSFILE.
unset DATABASE_URL
unset REHEARSAL_PGPASSWORD
unset PGPASSWORD
unset ORIGINAL_PGPASSFILE

export PGPASSFILE="$MANAGED_PGPASSFILE"

# Explicit maintenance DSN using structured parameters (passwords omitted from CLI arguments)
REHEARSAL_MAINTENANCE_DSN="host=$REHEARSAL_PGHOST port=$REHEARSAL_PGPORT user=$REHEARSAL_PGUSER dbname=$REHEARSAL_MAINTENANCE_DB"

# -----------------------------------------------------------------------------
# 2. Infrastructure Identity & Isolation Verification
# -----------------------------------------------------------------------------
echo "==> Verifying production and rehearsal infrastructure identity and isolation..."

# Query production cluster identity
# Explicitly handles Unix sockets where inet_server_addr() is NULL
prod_info=$(psql "$PROD_CLEAN_DSN" -t -A -v ON_ERROR_STOP=1 -c "
  SELECT 
    coalesce(inet_server_addr()::text, 'unix-socket'),
    coalesce(inet_server_port()::text, coalesce((SELECT setting FROM pg_settings WHERE name = 'port'), '5432')),
    current_user,
    current_database(),
    coalesce((SELECT setting FROM pg_settings WHERE name = 'data_directory'), ''),
    coalesce((SELECT setting FROM pg_settings WHERE name = 'unix_socket_directories'), ''),
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
    coalesce(inet_server_port()::text, coalesce((SELECT setting FROM pg_settings WHERE name = 'port'), '5432')),
    current_user,
    current_database(),
    coalesce((SELECT setting FROM pg_settings WHERE name = 'data_directory'), ''),
    coalesce((SELECT setting FROM pg_settings WHERE name = 'unix_socket_directories'), ''),
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
  echo "Rehearsal must run against a physically distinct PostgreSQL cluster." >&2
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

# 6. Privilege check: rehearsal user must have zero write/create privilege on production.
# Evaluates superuser, database CREATE, schema CREATE, and table write capabilities.
# Uses safe parameter passing (:'ruser') and strictly inspects PostgreSQL boolean results ('t' / 'f').
# Fails closed on 't', query errors, malformed results, and unknown states without issuing writes.
set +e
priv_output=$(psql "$PROD_CLEAN_DSN" -t -A -v ON_ERROR_STOP=1 -v ruser="$rehearsal_user" << 'EOF' 2>&1
SELECT CASE 
  WHEN NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'ruser') THEN 'f'
  WHEN (SELECT rolsuper FROM pg_roles WHERE rolname = :'ruser') THEN 't'
  WHEN has_database_privilege(:'ruser', current_database(), 'CREATE') THEN 't'
  WHEN has_schema_privilege(:'ruser', 'public', 'CREATE') THEN 't'
  WHEN EXISTS (
    SELECT 1 
    FROM pg_class c 
    JOIN pg_namespace n ON n.oid = c.relnamespace 
    WHERE n.nspname = 'public' 
      AND c.relkind IN ('r', 'p') 
      AND (
        has_table_privilege(:'ruser', c.oid, 'INSERT') OR 
        has_table_privilege(:'ruser', c.oid, 'UPDATE') OR 
        has_table_privilege(:'ruser', c.oid, 'DELETE') OR 
        has_table_privilege(:'ruser', c.oid, 'TRUNCATE')
      )
  ) THEN 't'
  ELSE 'f'
END;
EOF
)
priv_status=$?
set -e

if [ "$priv_status" -ne 0 ]; then
  echo "FATAL: Privilege query failed against production database (exit code $priv_status): $priv_output" >&2
  echo "Query errors cannot be treated as evidence of safety; aborting rehearsal." >&2
  exit 11
fi

if [ "$priv_output" = "t" ]; then
  echo "FATAL: Rehearsal user '$rehearsal_user' has write/create privileges on production database or cluster!" >&2
  echo "Rehearsals must be strictly isolated with zero production write capability." >&2
  exit 11
fi

if [ "$priv_output" != "f" ]; then
  echo "FATAL: Privilege check returned unexpected/malformed result: '$priv_output'." >&2
  echo "Unknown privilege state; aborting rehearsal." >&2
  exit 11
fi

echo "Production privilege check confirmed: rehearsal user '$rehearsal_user' has zero write/create privileges on production."

echo "Infrastructure isolation confirmed:"
echo "  Production: [addr=$prod_addr, port=$prod_port, db=$prod_db, sysid=${prod_sysid:0:8}...]"
echo "  Rehearsal:  [addr=$rehearsal_addr, port=$rehearsal_port, user=$rehearsal_user, sysid=${rehearsal_sysid:0:8}...]"

# -----------------------------------------------------------------------------
# 3. Filesystem, Process Permissions & Key Management
# -----------------------------------------------------------------------------
# Enforce strict umask for all created files and directories
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

# Safeguard against accidental overwrite of an existing backup file
if [ -e "$ENCRYPTED_BACKUP" ]; then
  echo "FATAL: Backup target file '$ENCRYPTED_BACKUP' already exists. Aborting to prevent overwrite." >&2
  exit 12
fi

# Recoverable Encryption Key Material (Separate Custody Required)
if [ -z "${BACKUP_PASSPHRASE_FILE:-}" ]; then
  echo "FATAL: BACKUP_PASSPHRASE_FILE must be set to a pre-provisioned, independently protected recovery key file." >&2
  echo "Production use requires separate backup and key custody. Generating recovery keys beside the backup is forbidden." >&2
  exit 13
fi

if [ ! -f "$BACKUP_PASSPHRASE_FILE" ]; then
  echo "FATAL: Specified BACKUP_PASSPHRASE_FILE '$BACKUP_PASSPHRASE_FILE' does not exist." >&2
  exit 13
fi

chmod 600 "$BACKUP_PASSPHRASE_FILE"

if [ ! -s "$BACKUP_PASSPHRASE_FILE" ]; then
  echo "FATAL: BACKUP_PASSPHRASE_FILE is empty." >&2
  exit 14
fi

# Rehearsal database parameters
REHEARSAL_DB="${REHEARSAL_DB:-rehearsal_${BACKUP_TS}_${RANDOM}}"
REHEARSAL_DB_CREATED_BY_US=false
REHEARSAL_TOC_FILE="$BACKUP_DIR/toc_${BACKUP_TS}_${RANDOM}.list"
TOC_FILE_CREATED=false

REHEARSAL_DB_DSN="host=$REHEARSAL_PGHOST port=$REHEARSAL_PGPORT user=$REHEARSAL_PGUSER dbname=$REHEARSAL_DB"

# -----------------------------------------------------------------------------
# 4. Record Production Baseline Metrics
# -----------------------------------------------------------------------------
echo "==> 1. Recording baseline production row counts..."
baseline_users=$(psql "$PROD_CLEAN_DSN" -t -A -v ON_ERROR_STOP=1 -c "SELECT count(*) FROM public.users;")
baseline_jobs=$(psql "$PROD_CLEAN_DSN" -t -A -v ON_ERROR_STOP=1 -c "SELECT count(*) FROM public.studio_jobs;")
baseline_migrations=$(psql "$PROD_CLEAN_DSN" -t -A -v ON_ERROR_STOP=1 -c "SELECT count(*) FROM public.schema_migrations;")
echo "Production baseline: users=$baseline_users, studio_jobs=$baseline_jobs, schema_migrations=$baseline_migrations"

# -----------------------------------------------------------------------------
# 6. Streaming Custom-Format Snapshot Directly into AES-256 Encryption
# -----------------------------------------------------------------------------
echo "==> 2. Streaming custom-format snapshot directly into AES-256 encryption..."
# Zero plaintext dump touches the filesystem: stream pg_dump directly into gpg
pg_dump "$PROD_CLEAN_DSN" --format=custom --schema=public | \
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
# Fail-fast if rehearsal database already exists
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

# Verify primary and foreign keys
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
# Automatic trap drops $REHEARSAL_DB on exit, independently verifies its absence,
# and forces a nonzero exit (21) if teardown cannot be verified.
