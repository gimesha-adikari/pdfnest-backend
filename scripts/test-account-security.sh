#!/usr/bin/env bash
# Disposable native PostgreSQL only; never uses the caller's DATABASE_URL.
set -euo pipefail
# Optional legacy fixtures must not pick up unrelated caller database settings
# or PostgreSQL service/password files.
unset ADMIN001_TEST_DATABASE_URL GIM6_TEST_DATABASE_URL PGHOSTADDR PGSERVICE PGSERVICEFILE PGPASSWORD PGOPTIONS
export PGPASSFILE=/dev/null
bindir="$(pg_config --bindir)"
account_test_root="$(mktemp -d /tmp/platen-account-tests.XXXXXXXX)"
cleanup() {
  "$bindir/pg_ctl" -D "$account_test_root/data" -m immediate -w stop >/dev/null 2>&1 || true
  rm -rf "$account_test_root"
}
trap cleanup EXIT
"$bindir/initdb" -D "$account_test_root/data" -A trust -U platen_test >"$account_test_root/init.log"
# Unix socket in a private directory; no TCP listener or shared development DB.
"$bindir/pg_ctl" -D "$account_test_root/data" -l "$account_test_root/postgres.log" -o "-h '' -k $account_test_root -p 55438 -F" -w start >/dev/null
"$bindir/createdb" -h "$account_test_root" -p 55438 -U platen_test platen_account_test
export DATABASE_URL="host=$account_test_root port=55438 user=platen_test dbname=platen_account_test sslmode=disable"
export LOCAL=true APP_ENV=test RESEND_API_KEY='' FROM_EMAIL=''
# Serialize packages sharing the ephemeral schema; tests still create explicit
# concurrent HTTP requests/transactions within each test.
go test -race ./internal/auth ./internal/user ./internal/authn ./internal/middleware ./internal/admin -count=1 -p 1 -parallel 1 -timeout 5m "$@"
