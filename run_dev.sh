#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

if [ -z "${PORT+x}" ] && [ -f .env ]; then
    ENV_PORT="$(sed -nE 's/^[[:space:]]*PORT[[:space:]]*=[[:space:]]*"?([^"#[:space:]]+)"?.*$/\1/p' .env | head -n 1)"
fi
PORT="${PORT:-${ENV_PORT:-8080}}"

stop_port_listener() {
    local port="$1" pids pid
    pids="$(lsof -nP -t -iTCP:"$port" -sTCP:LISTEN 2>/dev/null || true)"
    [ -n "$pids" ] || return 0

    echo "Port $port is already in use by:"
    ps -o pid=,user=,command= -p $pids
    read -r -p "Stop this process and continue? [y/N] " reply
    case "$reply" in
        [yY]|[yY][eE][sS]) ;;
        *) echo "Cancelled; port $port remains in use."; exit 1 ;;
    esac

    for pid in $pids; do kill "$pid" 2>/dev/null || true; done
    for _ in {1..10}; do
        sleep 1
        lsof -nP -t -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1 || return 0
    done
    echo "The process did not stop cleanly; forcing it to stop."
    for pid in $pids; do kill -KILL "$pid" 2>/dev/null || true; done
}

#############################################
# Check dependencies
#############################################

require() {
    command -v "$1" >/dev/null 2>&1 || {
        echo "Missing dependency: $1"
        exit 1
    }
}

require go
require air
require pg_isready
require lsof

#############################################
# Go
#############################################

echo "Downloading Go modules..."
go mod download

#############################################
# Start PostgreSQL
#############################################

echo "Checking PostgreSQL..."

if pg_isready -q; then
    echo "PostgreSQL is already running."
else
    echo "Starting PostgreSQL..."

    if command -v systemctl >/dev/null 2>&1; then
        sudo systemctl start postgresql
    else
        sudo service postgresql start
    fi

    echo "Waiting for PostgreSQL..."

    until pg_isready -q; do
        sleep 1
    done

    echo "PostgreSQL started."
fi

#############################################
# Run
#############################################

echo ""
echo "=================================="
echo "Starting PDFNest Backend (DEV)"
echo "=================================="
echo ""

stop_port_listener "$PORT"

exec air
