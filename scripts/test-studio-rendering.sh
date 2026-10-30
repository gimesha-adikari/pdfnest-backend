#!/usr/bin/env bash
# Real PDF rendering qualification with task-owned PostgreSQL and local HTTP mocks.
# Set STUDIO_TEST_PYTHON to a Python interpreter with PyMuPDF already installed.
# Broader selections may require separate corpus and native metadata API fixtures;
# this script qualifies rendering through its local mock Worker transport only.
set -euo pipefail
unset DATABASE_URL REDIS_URL WORKER_DURABILITY_TEST_DSN BACKEND_DURABILITY_TEST_DSN PGSERVICE PGSERVICEFILE PGHOSTADDR PGPASSWORD PGOPTIONS
bindir="$(pg_config --bindir)"
render_test_root="$(mktemp -d /tmp/platen-rendering-tests.XXXXXXXX)"
cleanup() {
  "$bindir/pg_ctl" -D "$render_test_root/data" -m fast -w stop >/dev/null 2>&1 || true
  rm -rf "$render_test_root"
}
trap cleanup EXIT
export PGPASSFILE="$render_test_root/pgpass"
install -m 600 /dev/null "$PGPASSFILE"
"${STUDIO_TEST_PYTHON:-python3}" -c 'import pymupdf' # Never installs dependencies.
"$bindir/initdb" -D "$render_test_root/data" -A trust -U platen_test --encoding=UTF8 >"$render_test_root/init.log"
"$bindir/pg_ctl" -D "$render_test_root/data" -l "$render_test_root/postgres.log" -o "-h '' -k $render_test_root -p 55456 -F -c shared_buffers=32MB -c max_connections=256" -w start >/dev/null
"$bindir/createdb" -h "$render_test_root" -p 55456 -U platen_test rendering
export DATABASE_URL="host=$render_test_root port=55456 user=platen_test dbname=rendering sslmode=disable"
export APP_ENV=test LOCAL=true STORAGE_MODE=local LOCAL_STORAGE_DIR="$render_test_root/objects"
export PDFNEST_WORKER_URL=http://127.0.0.1:1 REDIS_URL=redis://127.0.0.1:1
export RESEND_API_KEY='' FROM_EMAIL='' GOMAXPROCS=2 GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local
go test -race ./internal/studio -run "${STUDIO_RENDER_TEST_RUN:-^Test(Benchmark|Stress|RealRendering)}" -count=1 -parallel=1 -timeout=5m "$@"
