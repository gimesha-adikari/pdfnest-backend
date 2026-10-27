#!/usr/bin/env bash
# Isolated database; Redis is supplied by in-process miniredis test fixtures.
set -euo pipefail
unset PGHOST PGHOSTADDR PGSERVICE PGSERVICEFILE PGPASSWORD PGOPTIONS
export PGPASSFILE=/dev/null
bindir="$(pg_config --bindir)"
analyzer_test_root="$(mktemp -d /tmp/platen-analyzer-errors.XXXXXXXX)"
cleanup() {
 "$bindir/pg_ctl" -D "$analyzer_test_root/data" -m immediate -w stop >/dev/null 2>&1 || true
 rm -rf "$analyzer_test_root"
}
trap cleanup EXIT
"$bindir/initdb" -D "$analyzer_test_root/data" -A trust -U platen_test >"$analyzer_test_root/init.log"
"$bindir/pg_ctl" -D "$analyzer_test_root/data" -l "$analyzer_test_root/postgres.log" -o "-h '' -k $analyzer_test_root -p 55440 -F" -w start >/dev/null
"$bindir/createdb" -h "$analyzer_test_root" -p 55440 -U platen_test platen_analyzer_test
export DATABASE_URL="host=$analyzer_test_root port=55440 user=platen_test dbname=platen_analyzer_test sslmode=disable"
mkdir "$analyzer_test_root/storage" "$analyzer_test_root/tmp"
export APP_ENV=test LOCAL=true STORAGE_MODE=local
export LOCAL_STORAGE_DIR="$analyzer_test_root/storage" TMPDIR="$analyzer_test_root/tmp"
export PDFNEST_WORKER_URL=http://127.0.0.1:1
# No external repository or DNS requests in this gate.
go test -race ./internal/analyzer/worker ./internal/analyzer/api -run '^(TestPublicFailure|TestTaskProgressAdapter|TestFailure_|TestWatchdog_|TestWorkerBoundedConcurrency|TestExecutePipelineEndToEnd|TestMemoryJobQueueOperations)' -count=1 -p 1 -timeout=120s "$@"
