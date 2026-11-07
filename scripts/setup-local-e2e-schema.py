#!/usr/bin/env python3
"""Apply the checked-in additive schema needed by a disposable local E2E DB.

The Backend must already have run its normal development GORM initializer. This
helper refuses non-loopback or non-test databases and applies only the four
reviewed SQL migration files in dependency order.
"""

from __future__ import annotations

import ipaddress
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
from urllib.parse import parse_qs, unquote, urlsplit


MIGRATIONS = (
    "20261029_backend_durability.up.sql",
    "20261105_artifact_recovery.up.sql",
    "20261105_account_cleanup.up.sql",
    "20261105_recovery_copies.up.sql",
)
EXPECTED_VERSIONS = (
    "20261029_backend_durability_v1",
    "20261105_artifact_recovery_v1",
    "20261105_account_cleanup_v1",
    "20261105_recovery_copies_v1",
)


def refuse(message: str) -> "NoReturn":
    print(f"Local E2E schema setup refused: {message}", file=sys.stderr)
    raise SystemExit(2)


def main() -> int:
    if os.environ.get("APP_ENV") != "test":
        refuse("APP_ENV must be exactly 'test'")
    if os.environ.get("PLATEN_LOCAL_E2E_CONFIRM") != "apply-checked-in-schema":
        refuse("set PLATEN_LOCAL_E2E_CONFIRM=apply-checked-in-schema for a disposable database")
    raw_dsn = os.environ.get("DATABASE_URL", "")
    try:
        parsed = urlsplit(raw_dsn)
        host = parsed.hostname or ""
        database = unquote(parsed.path.lstrip("/"))
        port = parsed.port or 5432
    except ValueError:
        refuse("DATABASE_URL is malformed")
    if parsed.scheme not in ("postgres", "postgresql"):
        refuse("DATABASE_URL must use a PostgreSQL URI")
    if not re.fullmatch(r"platen_e2e_[A-Za-z0-9_]+", database):
        refuse("database name must begin with platen_e2e_ and contain only safe identifier characters")
    try:
        loopback = host.lower() == "localhost" or ipaddress.ip_address(host).is_loopback
    except ValueError:
        loopback = False
    if not loopback:
        refuse("database host must be localhost or a loopback IP address")
    if not (1 <= port <= 65535):
        refuse("database port is invalid")
    query = parse_qs(parsed.query, keep_blank_values=True)
    if set(query) - {"sslmode", "connect_timeout"}:
        refuse("DATABASE_URL contains unsupported connection options")
    if query.get("sslmode", ["disable"])[0] != "disable":
        refuse("local test database must use sslmode=disable")
    if not parsed.username:
        refuse("DATABASE_URL must identify the disposable database role")
    psql = shutil.which("psql")
    if not psql:
        refuse("psql is unavailable")

    child_env = os.environ.copy()
    for name in ("DATABASE_URL", "PLATEN_LOCAL_E2E_CONFIRM"):
        child_env.pop(name, None)
    child_env.update({
        "PGHOST": host,
        "PGPORT": str(port),
        "PGDATABASE": database,
        "PGUSER": unquote(parsed.username),
        "PGPASSWORD": unquote(parsed.password or ""),
        "PGSSLMODE": "disable",
        "PGCONNECT_TIMEOUT": query.get("connect_timeout", ["5"])[0],
        "PGPASSFILE": "/dev/null",
    })

    def psql_value(sql: str) -> str:
        try:
            result = subprocess.run(
                [psql, "-X", "-A", "-t", "-q", "-v", "ON_ERROR_STOP=1", "-c", sql],
                env=child_env, check=True, capture_output=True, text=True, timeout=15,
            )
        except subprocess.CalledProcessError:
            refuse("a bounded schema preflight query failed")
        return result.stdout.strip()

    identity = psql_value("SELECT current_database() || '|' || COALESCE(inet_server_addr()::text, 'local_socket')")
    current_db, separator, server_address = identity.partition("|")
    if not separator or current_db != database:
        refuse("connected database identity did not match the requested local test database")
    if server_address != "local_socket":
        try:
            server_interface = ipaddress.ip_interface(server_address)
            address = server_interface.ip
            if server_interface.network.prefixlen != address.max_prefixlen:
                refuse("connected PostgreSQL server address was not a host address")
            if not (address.is_loopback or address.is_private) or address.is_link_local or address.is_multicast:
                refuse("connected PostgreSQL server address was not local or private")
        except ValueError:
            refuse("connected PostgreSQL server address could not be verified")

    ready = psql_value(
        "SELECT CASE WHEN to_regclass('public.schema_migrations') IS NOT NULL "
        "AND to_regclass('public.users') IS NOT NULL "
        "AND to_regclass('public.studio_jobs') IS NOT NULL THEN 'ready' ELSE 'missing' END"
    )
    if ready != "ready":
        refuse("normal Backend GORM initialization must create schema_migrations, users, and studio_jobs first")

    # Local RunSchemaMigrations AutoMigrate can create nullable duplicates of
    # several draft durability objects. The authoritative migration refuses
    # those unversioned objects, correctly. Remove only empty overlaps created
    # by a fresh test database; never repair an unversioned populated schema or
    # fabricate a version marker.
    ledger_exists = psql_value("SELECT to_regclass('public.backend_durability_schema') IS NOT NULL")
    base_marker = "absent" if ledger_exists == "f" else psql_value(
        "SELECT CASE WHEN EXISTS(SELECT 1 FROM public.backend_durability_schema "
        "WHERE version='20261029_backend_durability_v1') THEN 'present' ELSE 'unrecognized' END"
    )
    if base_marker == "unrecognized":
        refuse("an unrecognized durability schema ledger exists; inspect the test database instead of rewriting it")
    if base_marker == "absent":
        for table in ("task_completions", "worker_result_ownerships", "studio_storage_object_retirements"):
            exists = psql_value(f"SELECT to_regclass('public.{table}') IS NOT NULL")
            if exists == "t":
                rows = psql_value(f"SELECT count(*) FROM public.{table}")
                if rows != "0":
                    refuse(f"GORM overlap public.{table} is populated; preserving it")
                psql_value(f"DROP TABLE public.{table}")
        pending_column = psql_value(
            "SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_attribute "
            "WHERE attrelid='public.studio_jobs'::regclass AND attname='pending_object_keys' AND NOT attisdropped)"
        )
        if pending_column == "t":
            populated = psql_value("SELECT count(*) FROM public.studio_jobs WHERE pending_object_keys IS NOT NULL")
            if populated != "0":
                refuse("GORM overlap studio_jobs.pending_object_keys is populated; preserving it")
            psql_value("ALTER TABLE public.studio_jobs DROP COLUMN pending_object_keys")

    migration_dir = Path(__file__).resolve().parents[1] / "config" / "migrations"
    for filename in MIGRATIONS:
        migration = migration_dir / filename
        if not migration.is_file():
            refuse(f"checked-in migration is missing: {filename}")
        subprocess.run(
            [psql, "-X", "-v", "ON_ERROR_STOP=1", "-f", str(migration)],
            env=child_env, check=True, timeout=90,
        )

    versions = psql_value(
        "SELECT string_agg(version, ',' ORDER BY version) FROM public.backend_durability_schema "
        "WHERE version IN (" + ",".join("'" + value + "'" for value in EXPECTED_VERSIONS) + ")"
    )
    if versions != ",".join(sorted(EXPECTED_VERSIONS)):
        refuse("the expected schema version markers were not all present after migration")
    print("Local E2E schema setup passed for a loopback platen_e2e database.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
