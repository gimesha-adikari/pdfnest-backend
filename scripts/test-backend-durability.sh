#!/usr/bin/env bash
# Task-owned PostgreSQL and Redis only; never use caller databases or services.
set -euo pipefail
unset DATABASE_URL REDIS_URL WORKER_DURABILITY_TEST_DSN BACKEND_DURABILITY_TEST_DSN PGSERVICE PGSERVICEFILE PGHOSTADDR PGPASSWORD PGOPTIONS
bindir="$(pg_config --bindir)"
durability_test_root="$(mktemp -d /tmp/platen-durability-tests.XXXXXXXX)"
export PGPASSFILE="$durability_test_root/pgpass"
install -m 600 /dev/null "$PGPASSFILE"
cleanup() {
  if [[ -f "$durability_test_root/redis.pid" ]]; then kill "$(cat "$durability_test_root/redis.pid")" 2>/dev/null || true; fi
  "$bindir/pg_ctl" -D "$durability_test_root/data" -m fast -w stop >/dev/null 2>&1 || true
  rm -rf "$durability_test_root"
}
trap cleanup EXIT
"$bindir/initdb" -D "$durability_test_root/data" -A trust -U platen_test --encoding=UTF8 >"$durability_test_root/init.log"
"$bindir/pg_ctl" -D "$durability_test_root/data" -l "$durability_test_root/postgres.log" -o "-h '' -k $durability_test_root -p 55448 -F -c shared_buffers=32MB -c max_connections=100" -w start >/dev/null
redis_binary="${PLATEN_TEST_REDIS_BINARY:-redis-server}"
"$redis_binary" --port 0 --unixsocket "$durability_test_root/redis.sock" --unixsocketperm 700 --save '' --appendonly no --daemonize yes --pidfile "$durability_test_root/redis.pid" --logfile "$durability_test_root/redis.log"
for attempt in {1..50}; do [[ -S "$durability_test_root/redis.sock" ]] && break; sleep 0.05; done
[[ -S "$durability_test_root/redis.sock" ]]
export REDIS_URL="unix://$durability_test_root/redis.sock?db=0"
export APP_ENV=test LOCAL=true STORAGE_MODE=local RESEND_API_KEY='' FROM_EMAIL='' GOMAXPROCS=2
export LOCAL_STORAGE_DIR="$durability_test_root/objects"
export PDFNEST_WORKER_URL=http://127.0.0.1:1
for package in config studio billing tasks ocr worker conversion; do
  "$bindir/createdb" -h "$durability_test_root" -p 55448 -U platen_test "durability_$package"
  export DATABASE_URL="host=$durability_test_root port=55448 user=platen_test dbname=durability_$package sslmode=disable"
  export BACKEND_DURABILITY_TEST_DSN="$DATABASE_URL"
  case "$package" in
    config)
      BACKEND_DURABILITY_TEST_DSN="$DATABASE_URL" go test -race ./config -run '^(TestBackendDurabilityDraft|TestPreflight_)' -count=1 -timeout=5m ;;
    studio)
      # These are application/DB suites. Rendering benchmarks and stress suites
      # require a separately running native Worker and are qualified separately.
      go test -race ./internal/studio -run '^Test(Retention|StorageCleanup|Service_|Repository_|DurableAdmission|AmbiguousSubmission|ExpiryWins|AdmissionRollback|ExpiryRetains|PreparedObjectOwnership|ExpiredSession|StorageRetirement|StoragePublication|SessionReclamation|AcknowledgedSuccess|StudioEditorDurability|StudioCompile|StudioJobReconciler|StudioSourceAdmission|StudioPDF|StudioPresign|StudioPreviewBodyLimit)' -count=1 -timeout=5m ;;
    worker)
      WORKER_DURABILITY_TEST_DSN="$DATABASE_URL" go test -race ./internal/worker -count=1 -timeout=3m ;;
    conversion)
      go test -race ./internal/conversion -run 'Durab|Async|Billing|Reservation|PDFToMarkdown|LiveWorkerOwnerIsolation' -count=1 -timeout=5m ;;
    *) go test -race "./internal/$package" -count=1 -timeout=5m ;;
  esac
done
go test -race ./internal/analyzer/engine/acquisition ./internal/analyzer/engine/exclusion ./internal/netguard ./internal/process ./internal/storage ./internal/ocrv2 ./internal/edit ./internal/markup -count=1 -p 1 -timeout=3m
