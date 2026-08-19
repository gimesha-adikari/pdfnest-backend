#!/usr/bin/env bash

set -Eeuo pipefail

#############################################
# Configuration
#############################################

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

POSTGRES_TIMEOUT="${POSTGRES_TIMEOUT:-30}"

#############################################
# Load PORT from .env
#############################################

ENV_PORT=""

if [ -f ".env" ]; then
    ENV_PORT="$(
        sed -nE \
            's/^[[:space:]]*PORT[[:space:]]*=[[:space:]]*"?([^"#[:space:]]+)"?.*$/\1/p' \
            .env |
        head -n 1
    )"
fi

PORT="${PORT:-${ENV_PORT:-8080}}"

#############################################
# Helpers
#############################################

require() {
    if ! command -v "$1" >/dev/null 2>&1; then
        echo "ERROR: Missing dependency: $1"
        exit 1
    fi
}

log() {
    echo "[PDFNest] $*"
}

error() {
    echo ""
    echo "=========================================="
    echo "ERROR"
    echo "=========================================="
    echo "$*"
    echo ""
}

#############################################
# PostgreSQL configuration
#############################################

# Respect PostgreSQL environment variables if provided.
PGHOST="${PGHOST:-}"
PGPORT="${PGPORT:-5432}"

pg_ready() {
    if [ -n "$PGHOST" ]; then
        pg_isready \
            -h "$PGHOST" \
            -p "$PGPORT" \
            >/dev/null 2>&1
    else
        pg_isready \
            -p "$PGPORT" \
            >/dev/null 2>&1
    fi
}

#############################################
# PostgreSQL diagnostics
#############################################

postgres_diagnostics() {
    error "PostgreSQL did not become ready."

    echo "PostgreSQL readiness check:"
    if [ -n "$PGHOST" ]; then
        pg_isready -h "$PGHOST" -p "$PGPORT" || true
    else
        pg_isready -p "$PGPORT" || true
    fi

    echo ""
    echo "------------------------------------------"
    echo "PostgreSQL service status"
    echo "------------------------------------------"

    if command -v systemctl >/dev/null 2>&1; then
        sudo systemctl status postgresql --no-pager || true
    fi

    echo ""
    echo "------------------------------------------"
    echo "PostgreSQL clusters"
    echo "------------------------------------------"

    if command -v pg_lsclusters >/dev/null 2>&1; then
        pg_lsclusters || true
    else
        echo "pg_lsclusters is not available."
    fi

    echo ""
    echo "------------------------------------------"
    echo "Listening on PostgreSQL ports"
    echo "------------------------------------------"

    if command -v ss >/dev/null 2>&1; then
        ss -ltnp 2>/dev/null | grep -E ':(5432|[0-9]{4,5})\b.*postgres' || true
    fi

    echo ""
    echo "------------------------------------------"
    echo "Recent PostgreSQL logs"
    echo "------------------------------------------"

    if command -v journalctl >/dev/null 2>&1; then
        sudo journalctl \
            -u postgresql \
            -n 50 \
            --no-pager \
            || true
    fi

    echo ""
}

#############################################
# Start PostgreSQL cluster
#############################################

start_postgresql() {
    log "Checking PostgreSQL..."

    if pg_ready; then
        log "PostgreSQL is already running and ready."
        return 0
    fi

    log "PostgreSQL is not ready."
    log "Starting PostgreSQL..."

    #########################################
    # Debian / Ubuntu
    #########################################

    if command -v systemctl >/dev/null 2>&1; then
        if sudo systemctl start postgresql; then
            log "PostgreSQL service start command completed."
        else
            log "WARNING: systemctl could not start PostgreSQL."
        fi
    elif command -v service >/dev/null 2>&1; then
        if sudo service postgresql start; then
            log "PostgreSQL service start command completed."
        else
            log "WARNING: service could not start PostgreSQL."
        fi
    fi

    #########################################
    # Ubuntu/Debian cluster handling
    #
    # Sometimes the PostgreSQL service is
    # active while an individual cluster is
    # still down.
    #########################################

    if command -v pg_lsclusters >/dev/null 2>&1 && \
       command -v pg_ctlcluster >/dev/null 2>&1; then

        log "Checking PostgreSQL clusters..."

        while read -r version cluster port status owner datadir logfile; do
            # Skip empty lines.
            [ -n "${version:-}" ] || continue

            # Skip header.
            if [ "$version" = "Ver" ]; then
                continue
            fi

            log "Cluster detected: ${version}/${cluster} on port ${port} (${status})"

            if [ "$status" != "online" ]; then
                log "Starting cluster ${version}/${cluster}..."

                if sudo pg_ctlcluster "$version" "$cluster" start; then
                    log "Cluster ${version}/${cluster} start command completed."
                else
                    log "WARNING: Could not start cluster ${version}/${cluster}."
                fi
            fi

        done < <(
            pg_lsclusters 2>/dev/null |
            tail -n +2 |
            awk '{print $1, $2, $3, $4, $5, $6, $7}'
        )
    fi

    #########################################
    # Wait for PostgreSQL
    #########################################

    log "Waiting for PostgreSQL..."

    for ((i = 1; i <= POSTGRES_TIMEOUT; i++)); do

        if pg_ready; then
            echo ""
            log "PostgreSQL is ready."
            return 0
        fi

        printf "\r[PDFNest] PostgreSQL not ready... %2d/%-2d seconds" \
            "$i" \
            "$POSTGRES_TIMEOUT"

        sleep 1
    done

    echo ""

    postgres_diagnostics

    return 1
}

#############################################
# Stop process using development port
#############################################

stop_port_listener() {
    local port="$1"
    local pids
    local pid

    pids="$(
        lsof \
            -nP \
            -t \
            -iTCP:"$port" \
            -sTCP:LISTEN \
            2>/dev/null || true
    )"

    if [ -z "$pids" ]; then
        return 0
    fi

    echo ""
    echo "=========================================="
    echo "Port $port is already in use"
    echo "=========================================="
    echo ""

    ps -o pid=,user=,command= -p $pids || true

    echo ""

    read -r -p "Stop this process and continue? [y/N] " reply

    case "$reply" in
        [yY]|[yY][eE][sS])
            ;;
        *)
            echo "Cancelled; port $port remains in use."
            exit 1
            ;;
    esac

    for pid in $pids; do
        kill "$pid" 2>/dev/null || true
    done

    #########################################
    # Wait for graceful shutdown
    #########################################

    for _ in {1..10}; do
        sleep 1

        if ! lsof \
            -nP \
            -t \
            -iTCP:"$port" \
            -sTCP:LISTEN \
            >/dev/null 2>&1; then

            echo "Port $port is now free."
            return 0
        fi
    done

    #########################################
    # Force shutdown
    #########################################

    echo "Process did not stop cleanly."
    echo "Forcing process termination..."

    for pid in $pids; do
        kill -KILL "$pid" 2>/dev/null || true
    done

    sleep 1
}

#############################################
# Check dependencies
#############################################

require go
require air
require pg_isready
require lsof

#############################################
# Startup information
#############################################

echo ""
echo "=========================================="
echo " PDFNest Backend - Development"
echo "=========================================="
echo ""
echo "Project:  $ROOT_DIR"
echo "Port:     $PORT"
echo "PG Port:  $PGPORT"
if [ -n "$PGHOST" ]; then
    echo "PG Host:  $PGHOST"
else
    echo "PG Host:  default"
fi
echo ""

#############################################
# Go dependencies
#############################################

log "Downloading Go modules..."

if ! go mod download; then
    error "Failed to download Go modules."
    exit 1
fi

log "Go modules ready."

#############################################
# PostgreSQL
#############################################

if ! start_postgresql; then
    exit 1
fi

#############################################
# Development port
#############################################

stop_port_listener "$PORT"

#############################################
# Start Air
#############################################

echo ""
echo "=========================================="
echo "Starting PDFNest Backend"
echo "=========================================="
echo ""
echo "Air is starting..."
echo ""

exec air