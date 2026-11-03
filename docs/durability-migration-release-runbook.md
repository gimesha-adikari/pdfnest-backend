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
> Fail-closed infrastructure checks must verify that rehearsal hosts and cluster identities do not match production before any creation, restore, dry-run, or deletion is attempted.

### Step 2.1: Canonical Executable Rehearsal Implementation

The rehearsal procedure is maintained as an executable, automated script at [`scripts/run-durability-rehearsal.sh`](file:///home/gimesha/My_Projects/platen/pdfnest-backend/scripts/run-durability-rehearsal.sh).
Both manual pre-migration rehearsals and automated CI qualification invoke this exact implementation.

To execute the rehearsal:
```bash
DATABASE_URL="$DATABASE_URL" \
REHEARSAL_PGHOST="<disposable-rehearsal-host-or-socket>" \
REHEARSAL_PGPORT="5432" \
REHEARSAL_PGUSER="<rehearsal-operator-user>" \
REHEARSAL_NONPRODUCTION_CONFIRMED="true" \
BACKUP_PASSPHRASE_FILE="/path/to/independently/protected/recovery.key" \
./scripts/run-durability-rehearsal.sh
```

Below is the verified, authoritative implementation of `scripts/run-durability-rehearsal.sh`:

```bash
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
    # Keyword / value format
    pw_matches = re.findall(r"(?:^|\s)password\s*=", raw_url)
    if len(pw_matches) > 1:
        sys.stderr.write("FATAL: Ambiguous credentials: multiple password parameters in DSN\n")
        sys.exit(1)

    m = re.search(r"(?:^|\s)password\s*=\s*([^\s'\"]+|'[^']*'|\"[^\"]*\")", raw_url)
    if m:
        raw_val = m.group(1)
        if (raw_val.startswith("'") and raw_val.endswith("'")) or (raw_val.startswith('"') and raw_val.endswith('"')):
            prod_password = raw_val[1:-1]
        else:
            prod_password = raw_val
        clean_prod_dsn = (raw_url[:m.start()] + " " + raw_url[m.end():]).strip()
        clean_prod_dsn = re.sub(r"\s+", " ", clean_prod_dsn)
    else:
        clean_prod_dsn = raw_url

    def get_k(k):
        km = re.search(r"(?:^|\s)" + k + r"\s*=\s*([^\s'\"]+|'[^']*'|\"[^\"]*\")", clean_prod_dsn)
        if km:
            v = km.group(1)
            if (v.startswith("'") and v.endswith("'")) or (v.startswith('"') and v.endswith('"')):
                return v[1:-1]
            return v
        return None

    prod_host = get_k("host")
    port_val = get_k("port")
    if port_val is not None:
        try:
            prod_port = int(port_val)
        except ValueError:
            sys.stderr.write("FATAL: Invalid port parameter in DSN\n")
            sys.exit(1)
    prod_user = get_k("user")
    prod_dbname = get_k("dbname")

    if not (prod_host or prod_user or prod_dbname):
        sys.stderr.write("FATAL: Unsupported or ambiguous DATABASE_URL format\n")
        sys.exit(1)

# Fail closed if password appears anywhere in clean_prod_dsn
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
```

### Step 2.2: Backup Access Control, Separate Key Custody, and Owner-Approved Disposal Standards
1. **Access Control & File Mode:** The backup archive contains sensitive production records (user identities, studio job parameters, file keys). Process permissions enforce `umask 077` and `chmod 600`. File ownership belongs strictly to the migration operator role with zero permissions granted to other users or group members.
2. **Durable Key Material, Separate Key Custody & Zero Command-Line Secrets:** Never pass encryption keys as command-line arguments (which are visible in process tables and audit logs). The procedure strictly requires a pre-provisioned, independently protected `BACKUP_PASSPHRASE_FILE` with permissions `0600` (e.g. from an owner-approved secret store or key management service). Storing or silently generating the recovery key beside the encrypted backup is strictly forbidden in production and staging to maintain absolute separation of backup and key custody. Synthetic key bypass flags (`ALLOW_SYNTHETIC_TEST_KEY`) are completely absent from the production executable. Decryption is verified in a separate, isolated process using solely this pre-provisioned key material.
3. **Deterministic Target-Specific Credentials & PGPASSFILE Isolation:** Passwords must never appear in command-line arguments (`psql`, `pg_dump`), environment variables of child processes, or execution traces (`ps`, `/proc/*/cmdline`). Both production and rehearsal credentials are managed via a dedicated, permission-restricted (`0600`) `MANAGED_PGPASSFILE`:
   - *Cross-Target Wildcard Elimination:* Production and rehearsal entries use deterministic, target-specific matching with zero host or port wildcards (`*`). Entries for production match exact production host and port; entries for rehearsal match exact rehearsal host and port. Even if production and rehearsal share identical usernames, credentials never match across targets.
   - *Sanitization of All URI Credential Locations:* The parser inspects both userinfo credentials (`user:password@`) and query parameters (`?password=...`). Extracted passwords are removed from the query string and userinfo, producing a sanitized connection string containing zero password parameters for CLI tool invocations (`psql`, `pg_dump`).
   - *Fail-Closed Boundary Enforcement:* Ambiguous or conflicting credential formats (such as passwords specified in both userinfo and query parameters, multiple password query parameters, multiple DSN password options, or unsupported formats) fail closed with exit status `1`.
   - *Original PGPASSFILE Safe Preservation:* The operator's ambient `PGPASSFILE` setting is captured before assigning the managed file. Preexisting entries are preserved by appending them after the rehearsal target-specific entries (providing explicit target precedence with ambient fallback). Nonexistent or unreadable `PGPASSFILE` paths fail closed.
   - *Subprocess Environment Scrubbing:* Raw credential variables (`DATABASE_URL`, `REHEARSAL_PGPASSWORD`, and ambient `PGPASSWORD`) are explicitly scrubbed via `unset` prior to spawning client subprocesses, neutralizing ambient `PGPASSWORD` precedence and preventing environment leakage in `/proc/$PID/environ`.
   - *Special-Character Handling:* Colons (`:`) and backslashes (`\`) in passwords, usernames, and database names are escaped with backslashes (`\:` and `\\`) in `.pgpass`, guaranteeing that valid complex values remain usable and cannot alter or split adjacent fields.
   - *Guaranteed Cleanup:* The managed `PGPASSFILE` is guaranteed to be deleted on exit across all paths (success, validation failure, teardown containment failure, and interrupt).
4. **Immutable Fail-Closed Production Privilege Check:** The production rehearsal executable enforces an immutable privilege query that rejects any rehearsal role possessing write or creation privileges on production. Arbitrary query overrides (`PRIVILEGE_QUERY_OVERRIDE`) have been completely removed from the production script to eliminate SQL injection risks. Negative failure testing is handled via isolated test harness wrappers.
5. **Elimination of Plaintext Dumps:** Plaintext database dumps must never touch physical or virtual disk storage. The snapshot is piped directly from `pg_dump -Fc` into `gpg --batch --pinentry-mode loopback --symmetric --cipher-algo AES256 --passphrase-file ...`. Rehearsal restore streams decrypted bytes directly from `gpg --decrypt` pipe into `pg_restore --exit-on-error`.
6. **Fail-Closed Rehearsal Database Teardown:** The rehearsal database contains restored production data that is not encrypted at rest by this procedure. On exit, the script drops only a database positively created by the same execution (never `--force`, never session termination, never a preexisting database), then independently confirms absence with a parameterized `pg_database` catalog query regardless of what `dropdb` reported. If deletion or verification fails, the script prints a `CONTAINMENT REQUIRED` block naming the remaining database and rehearsal target identity (host, port, user, system identifier; no secrets) and exits with status `21` even when all restore and migration checks passed. The encrypted backup and recovery key are preserved, and the managed `PGPASSFILE` is still removed. A rehearsal is only successful when the output ends with `REHEARSAL SUCCEEDED: all checks passed and rehearsal database absence was verified.` and the exit status is `0`.
7. **Permanent Recovery Asset Retention (No Automated Purge):** The rehearsal script **never** deletes the encrypted recovery backup or its key file under any circumstance (neither on failure nor on success). Automatic asset purging (`PURGE_BACKUP_ON_SUCCESS`) has been completely eliminated. The backup file is the ultimate disaster recovery asset; only ephemeral rehearsal database objects and temporary files are cleaned up on exit.
8. **Separate Owner-Approved Retention and Disposal Procedure:**
   - *Physical Limitations of Overwrite on SSDs/Cloud Volumes:* Modern solid-state drives (SSDs, NVMe) and virtualized cloud block devices (e.g. AWS EBS, GCP Persistent Disk, Railway volume mounts) utilize flash translation layers (FTL), wear-leveling algorithms, over-provisioning pools, and copy-on-write sparse remapping. Standard file overwriting tools (`shred`, `rm -P`) overwrite only the currently mapped logical blocks; residual physical flash cells or underlying storage snapshots retain historical data blocks until low-level garbage collection occurs.
   - *Crypto-Shredding as the Authoritative Standard:* Immediate symmetric encryption with AES-256 provides absolute confidentiality. Recovery assets (`$ENCRYPTED_BACKUP` and `$BACKUP_PASSPHRASE_FILE`) must be preserved throughout the migration maintenance window and retained according to the organization's data retention policy.
   - *Independent Disposal Execution:* Automatic purging by the rehearsal script is strictly forbidden. When the snapshot reaches the end of its owner-approved retention window (typically 24–48 hours post-migration following production verification):
     1. Written owner authorization for backup asset disposal must be recorded.
     2. Cryptographic erasure is performed by shredding the key file and the ciphertext backup in an independent operational step:
        ```bash
        shred -u "$BACKUP_PASSPHRASE_FILE"
        shred -u "$ENCRYPTED_BACKUP"
        ```

### Step 2.3: Pre-Migration Sign-Off & Approval Checklist

Before proceeding to Phase 3 (Production Execution), confirm and check off every requirement:

| Prerequisite | Verification Method | Status | Sign-Off |
| :--- | :--- | :--- | :--- |
| **Phase 1 Preflight Checks** | Read-only SQL queries in Step 1.1–1.4 executed; no locks > 10s, no unversioned tables, schema ledger verified | Required | [ ] |
| **Infrastructure Isolation Confirmed** | `system_identifier`, `data_directory`, and `current_user` verified; rehearsal cluster distinct from production | Required | [ ] |
| **Fail-Closed Privilege Verification** | Production privilege check proves rehearsal role has zero write/create privileges on production | Required | [ ] |
| **Streaming Encrypted Snapshot** | `pg_dump -Fc` streamed directly into AES-256 encryption (`.dump.gpg`); 0 bytes plaintext on disk | Required | [ ] |
| **Separate Key Custody Confirmed** | Pre-provisioned recovery key via `BACKUP_PASSPHRASE_FILE` (0600) separate from backup; no silent colocation | Required | [ ] |
| **Zero Credential Process Leakage** | `DATABASE_URL` and `REHEARSAL_PGPASSWORD` managed via temporary `PGPASSFILE` (0600); 0 passwords in `psql`/`pg_dump` CLI args, DSN strings, child environment, or logs | Required | [ ] |
| **Deterministic Credential Selection** | Target-specific matching (0 host/port wildcards); distinct canaries for production and rehearsal verified | Required | [ ] |
| **URI Credential Sanitization** | Passwords in userinfo and query parameters extracted and stripped; ambiguous formats fail closed | Required | [ ] |
| **TOC Integrity Confirmed** | `pg_restore --list` from decrypted stream confirms required table structures | Required | [ ] |
| **Collision-Safe DB Creation** | `createdb` executed only after verifying database does not preexist; preexisting DBs never dropped | Required | [ ] |
| **Streaming Rehearsal Restore Passed** | Decrypted stream restored to disposable DB with `--exit-on-error`; 0 errors | Required | [ ] |
| **Row Count Integrity Matched** | Restored row counts (`users`, `studio_jobs`, `schema_migrations`) match production 100% | Required | [ ] |
| **Migration Dry-Run Passed** | `20261029_backend_durability.up.sql` applied cleanly to restored snapshot data | Required | [ ] |
| **12 Indexes Programmatically Validated** | Exactly 12 indexes verified: 5 unique, 4 primary key, 7 secondary, all 12 valid (`is_valid = true`) | Required | [ ] |
| **Verified Rehearsal Teardown** | Script exit status `0` with `Teardown verified` and `REHEARSAL SUCCEEDED`; any exit `21` / `CONTAINMENT REQUIRED` is contained and re-run before sign-off | Required | [ ] |
| **Separate-Process Decryption Proven** | Encrypted backup decrypted in a separate, isolated process using only retained key file | Required | [ ] |
| **Recovery Asset Retention Confirmed** | Approved backup and key file preserved across rehearsal; no automatic purging | Required | [ ] |
| **Full 31-Check Harness Verified** | `scripts/test-runbook-rehearsal.sh` passed 31/31 negative and positive tests in CI | Required | [ ] |
| **Worker PR #13 Deployed** | Worker deployed first; running healthy on Railway (`pdfnest-worker`, `alert-benevolence`) | Required | [ ] |
| **Rollback Binary Qualified** | Preflight test `TestPreflight_HistoricalBinaryRuntimeCompatibility` passing in CI | Required | [ ] |
| **Explicit Owner Authorization** | Written owner/lead approval to execute production DDL | Required | [ ] |

### Step 2.4: Automated Rehearsal Qualification Suite (31 Comprehensive Checks)

The rehearsal safety invariants are continuously validated in CI via [`scripts/test-runbook-rehearsal.sh`](file:///home/gimesha/My_Projects/platen/pdfnest-backend/scripts/test-runbook-rehearsal.sh). This harness spawns two isolated, disposable PostgreSQL clusters (simulated production and disposable rehearsal) and executes 31 negative and positive checks:

1. **Negative Test 1:** Rejection of missing `DATABASE_URL` (exit code 1).
2. **Negative Test 2:** Rejection of missing `REHEARSAL_PGHOST` / `REHEARSAL_PGUSER` (exit code 2).
3. **Negative Test 3:** Rejection of missing affirmative non-production confirmation (exit code 3).
4. **Negative Test 4:** Enforce separate key custody; missing `BACKUP_PASSPHRASE_FILE` fails fast (exit code 13).
5. **Negative Test 5:** Verify synthetic-key bypass is completely unavailable in the production executable (exit code 13).
6. **Negative Test 6:** Infrastructure collision detection when rehearsal target matches production cluster (exit code 6).
7. **Negative Test 7:** Real privilege rejection for role with production write/create permissions before creating backup or DB (exit code 11).
8. **Negative Test 8:** Neutralize malicious inherited privilege overrides (`PRIVILEGE_QUERY_OVERRIDE` ignored, zero arbitrary SQL executed on production, rejected with exit code 11).
9. **Negative Test 9:** Rejection on privilege query error (fail-closed via harness failure injection, exit code 11).
10. **Negative Test 10:** Rejection on malformed or unexpected privilege query result (fail-closed, exit code 11).
11. **Test 11:** Positive verification of valid unprivileged roles (nonexistent role and read-only role evaluate cleanly to `'f'`).
12. **Negative Test 12:** Database name collision safety (preexisting database survives collision without deletion or corruption).
13. **Negative Test 13:** Asset preservation on rehearsal failure (external directory contents, encrypted backup, and recovery key intact).
14. **Positive Test 14:** Full end-to-end rehearsal execution using password-authenticated role via `PGPASSFILE`.
15. **Positive Test 15:** Credential safety verification (0 passwords in process args or logs, managed `PGPASSFILE` cleaned up on exit on success and failure, wrong password rejected).
16. **Positive Test 16:** Recovery asset retention on rehearsal success (no automatic purging).
17. **Positive Test 17:** Independent separate-process decryption proof using retained recovery key in a scrubbed environment.
18. **Negative Test 18:** Fail-closed teardown: a real active session blocks `dropdb`; the script reports the PostgreSQL refusal, prints the containment warning, exits `21` despite passing restore/migration checks, removes the managed `PGPASSFILE`, preserves the encrypted backup and key, and deletes no unrelated database. The harness independently confirms the database remained, then contains the fixture.
19. **Negative Test 19:** Fail-closed teardown: a harness `dropdb` that falsely reports success is caught by the script's independent catalog verification (exit `21`).
20. **Positive Test 20:** Successful teardown: exit `0` only after `Teardown verified`, independently confirmed absent by the harness.
21. **Positive Test 21:** Production source credential safety: full rehearsal execution with authenticated production source (`DATABASE_URL="$PROD_AUTH_URL"`) and authenticated rehearsal target using distinct canary passwords; verified backup, restore, row counts, 12 indexes, and teardown succeed; asserted zero canary password or `password=` in output; verified `PGPASSFILE` cleaned up; verified separate-process backup decryption.
22. **Negative Test 22:** Production source wrong-password rejection (`WrongCanarySecret_99999!`); verified fail-fast exit code 4; asserted zero password leakage in error messages; verified `PGPASSFILE` cleaned up on failure.
23. **Negative Test 23:** Authenticated production source boundary across failed rehearsal teardown (`hold_connection`); verified exit code 21, containment warning emitted, zero secrets leaked, `PGPASSFILE` cleaned up, recovery assets preserved, and test harness contained fixture.
24. **Positive Test 24:** Production source credential safety with keyword/value DSN format containing canary password; verified execution succeeds with exit code 0, zero password leakage, and `PGPASSFILE` cleaned up.
25. **Positive Test 25:** URI query-parameter credentials and DSN argument sanitization (`password=` extracted from query string and sanitized from CLI tool arguments; 0 secrets in `argv` or child environment).
26. **Negative Test 26:** Ambiguous and malformed credential specifications fail closed (passwords in both userinfo and query string, multiple query passwords, multiple DSN passwords, and malformed formats fail fast with exit code 1).
27. **Positive Test 27:** Cross-target wildcard elimination (identical usernames `platen_shared_operator` with distinct target passwords authenticate correctly without cross-target wildcard match).
28. **Negative Test 28:** Cross-target credential mismatch rejection (rehearsal target rejects production credentials and production target rejects rehearsal credentials).
29. **Test 29:** Preexisting ambient `PGPASSFILE` safe preservation (ambient file unmodified on exit) and invalid/nonexistent `PGPASSFILE` path rejection (fails closed).
30. **Positive Test 30:** Subprocess environment scrubbing and ambient `PGPASSWORD` neutralization (ambient `PGPASSWORD` cannot hijack or fail authentication; child process environment verified clean).
31. **Positive Test 31:** Special characters in passwords and usernames (passwords containing colons, backslashes, quotes, and symbols properly escaped in `.pgpass` and authenticated cleanly).

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

### Step 4.3: Confirm Exact Constraints and Indexes (Uniqueness, Primary Key & Validity)
```sql
-- Verify specific primary keys, index names, uniqueness, primary-key status, and validity
SELECT 
    t.relname AS tablename,
    i.relname AS indexname,
    idx.indisunique AS is_unique,
    idx.indisprimary AS is_primary,
    idx.indisvalid AS is_valid
FROM pg_index idx
JOIN pg_class t ON t.oid = idx.indrelid
JOIN pg_class i ON i.oid = idx.indexrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = 'public' 
  AND t.relname IN ('task_completions', 'worker_result_ownerships', 'studio_storage_object_retirements', 'backend_durability_schema')
ORDER BY t.relname, i.relname;
```
*Expected: Exactly 12 index objects across the 4 durability tables (all with `is_valid = true`, exactly 5 unique, exactly 4 primary keys):*
- `backend_durability_schema`:
  - `backend_durability_schema_pkey` (UNIQUE=true, PRIMARY=true, VALID=true)
- `studio_storage_object_retirements`:
  - `studio_storage_object_retirements_pkey` (UNIQUE=true, PRIMARY=true, VALID=true)
- `task_completions`:
  - `task_completions_pkey` (UNIQUE=true, PRIMARY=true, VALID=true)
  - `idx_task_completions_expires_at` (UNIQUE=false, PRIMARY=false, VALID=true)
  - `idx_task_completions_reservation_id` (UNIQUE=false, PRIMARY=false, VALID=true)
  - `idx_task_completions_state` (UNIQUE=false, PRIMARY=false, VALID=true)
- `worker_result_ownerships`:
  - `worker_result_ownerships_pkey` (UNIQUE=true, PRIMARY=true, VALID=true)
  - `idx_worker_result_ownerships_request_id` (UNIQUE=true, PRIMARY=false, VALID=true)
  - `idx_worker_result_ownerships_expires_at` (UNIQUE=false, PRIMARY=false, VALID=true)
  - `idx_worker_result_ownerships_next_attempt_at` (UNIQUE=false, PRIMARY=false, VALID=true)
  - `idx_worker_result_ownerships_state` (UNIQUE=false, PRIMARY=false, VALID=true)
  - `idx_worker_result_ownerships_worker_job_id` (UNIQUE=false, PRIMARY=false, VALID=true)

> [!NOTE]
> Automated verification of all 12 indexes, their uniqueness, primary key properties, and validity is enforced in preflight test `TestPreflight_ExplicitIndexAndConstraintValidation` and in `scripts/run-durability-rehearsal.sh`.

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
