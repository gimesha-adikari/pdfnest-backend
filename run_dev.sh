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

warning() {
    echo "[PDFNest] WARNING: $*"
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

# PDFNest development uses TCP PostgreSQL.
#
# The current development setup exposes the Docker PostgreSQL
# container as:
#
#   localhost:5432 -> pdfnest-test-pg:5432
#
# Explicit PGHOST/PGPORT environment variables still override
# these defaults.

PGHOST="${PGHOST:-localhost}"
PGPORT="${PGPORT:-5432}"

#############################################
# PostgreSQL readiness
#############################################

pg_ready() {
    pg_isready \
        -h "$PGHOST" \
        -p "$PGPORT" \
        >/dev/null 2>&1
}

#############################################
# PostgreSQL diagnostics
#############################################

postgres_diagnostics() {
    error "PostgreSQL did not become ready."

    echo "PostgreSQL readiness check:"
    pg_isready \
        -h "$PGHOST" \
        -p "$PGPORT" \
        || true

    echo ""
    echo "------------------------------------------"
    echo "PostgreSQL endpoint"
    echo "------------------------------------------"
    echo "Host: $PGHOST"
    echo "Port: $PGPORT"

    echo ""
    echo "------------------------------------------"
    echo "Listening on PostgreSQL port"
    echo "------------------------------------------"

    if command -v ss >/dev/null 2>&1; then
        ss -ltnp 2>/dev/null |
            grep -E ":${PGPORT}\b" ||
            echo "Nothing is listening on port ${PGPORT}."
    fi

    echo ""
    echo "------------------------------------------"
    echo "Docker containers"
    echo "------------------------------------------"

    if command -v docker >/dev/null 2>&1; then
        docker ps \
            --format 'table {{.ID}}\t{{.Names}}\t{{.Image}}\t{{.Ports}}' \
            2>/dev/null ||
            true
    else
        echo "Docker is not installed or unavailable."
    fi

    echo ""
    echo "------------------------------------------"
    echo "PostgreSQL service status"
    echo "------------------------------------------"

    if command -v systemctl >/dev/null 2>&1; then
        sudo systemctl status postgresql \
            --no-pager \
            -l \
            2>/dev/null ||
            true
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
    echo "PostgreSQL cluster service status"
    echo "------------------------------------------"

    if command -v systemctl >/dev/null 2>&1; then
        if command -v pg_lsclusters >/dev/null 2>&1; then
            while read -r version cluster port status owner datadir logfile; do
                [ -n "${version:-}" ] || continue

                if [ "$version" = "Ver" ]; then
                    continue
                fi

                sudo systemctl status \
                    "postgresql@${version}-${cluster}.service" \
                    --no-pager \
                    -l \
                    2>/dev/null ||
                    true
            done < <(
                pg_lsclusters 2>/dev/null |
                tail -n +2 |
                awk '{print $1, $2, $3, $4, $5, $6, $7}'
            )
        fi
    fi

    echo ""
    echo "------------------------------------------"
    echo "Recent PostgreSQL logs"
    echo "------------------------------------------"

    if command -v journalctl >/dev/null 2>&1; then
        sudo journalctl \
            -u postgresql \
            -u 'postgresql@*.service' \
            -n 80 \
            --no-pager \
            2>/dev/null ||
            true
    fi

    echo ""
}

#############################################
# Detect Docker PostgreSQL
#############################################

docker_postgres_available() {
    if ! command -v docker >/dev/null 2>&1; then
        return 1
    fi

    docker ps \
        --format '{{.Names}} {{.Ports}}' \
        2>/dev/null |
        grep -Eq \
            '(^|[[:space:]])pdfnest-test-pg([[:space:]]|$).*0\.0\.0\.0:'"${PGPORT}"'->5432|(^|[[:space:]])pdfnest-test-pg([[:space:]]|$).*'"${PGPORT}"'->5432'
}

#############################################
# Start Docker PostgreSQL if it exists
#############################################

start_docker_postgresql() {
    if ! command -v docker >/dev/null 2>&1; then
        return 1
    fi

    if ! sudo docker inspect pdfnest-test-pg >/dev/null 2>&1; then
        return 1
    fi

    log "Found Docker PostgreSQL container: pdfnest-test-pg"

    local container_status

    container_status="$(
        sudo docker inspect \
            --format '{{.State.Status}}' \
            pdfnest-test-pg \
            2>/dev/null ||
            true
    )"

    if [ "$container_status" = "running" ]; then
        log "Docker PostgreSQL container is already running."
    else
        log "Starting Docker PostgreSQL container..."

        if ! sudo docker start pdfnest-test-pg >/dev/null; then
            warning "Could not start Docker container pdfnest-test-pg."
            return 1
        fi

        log "Docker PostgreSQL container started."
    fi

    return 0
}

#############################################
# Start PostgreSQL
#############################################

start_postgresql() {
    log "Checking PostgreSQL at ${PGHOST}:${PGPORT}..."

    #########################################
    # First check
    #
    # This is the most important check.
    #
    # If Docker PostgreSQL is already exposing
    # localhost:5432, we immediately continue.
    #########################################

    if pg_ready; then
        log "PostgreSQL is already running and ready."
        return 0
    fi

    log "PostgreSQL is not ready."

    #########################################
    # Docker PostgreSQL
    #
    # Prefer the known PDFNest development
    # PostgreSQL container before touching the
    # host PostgreSQL service.
    #########################################

    if start_docker_postgresql; then
        log "Waiting for Docker PostgreSQL..."

        for ((i = 1; i <= POSTGRES_TIMEOUT; i++)); do
            if pg_ready; then
                echo ""
                log "PostgreSQL is ready at ${PGHOST}:${PGPORT}."
                return 0
            fi

            printf "\r[PDFNest] PostgreSQL not ready... %2d/%-2d seconds" \
                "$i" \
                "$POSTGRES_TIMEOUT"

            sleep 1
        done

        echo ""
        warning "Docker PostgreSQL was started but did not become ready."
    fi

    #########################################
    # Check again before system PostgreSQL
    #########################################

    if pg_ready; then
        log "PostgreSQL is already available."
        return 0
    fi

    #########################################
    # Debian / Ubuntu PostgreSQL service
    #########################################

    if command -v systemctl >/dev/null 2>&1; then

        log "Attempting to start system PostgreSQL service..."

        if sudo systemctl start postgresql; then
            log "PostgreSQL service start command completed."
        else
            warning "systemctl could not start PostgreSQL."
        fi

        #####################################
        # Check the actual configured endpoint
        #
        # Do NOT immediately start clusters.
        # Docker may already have taken the port.
        #####################################

        if pg_ready; then
            log "PostgreSQL is ready at ${PGHOST}:${PGPORT}."
            return 0
        fi

    elif command -v service >/dev/null 2>&1; then

        log "Attempting to start PostgreSQL service..."

        if sudo service postgresql start; then
            log "PostgreSQL service start command completed."
        else
            warning "service could not start PostgreSQL."
        fi

        if pg_ready; then
            log "PostgreSQL is ready at ${PGHOST}:${PGPORT}."
            return 0
        fi
    fi

    #########################################
    # Ubuntu/Debian cluster handling
    #
    # Only attempt this while the configured
    # PostgreSQL endpoint remains unavailable.
    #########################################

    if command -v pg_lsclusters >/dev/null 2>&1 && \
       command -v pg_ctlcluster >/dev/null 2>&1; then

        log "Checking PostgreSQL clusters..."

        while read -r version cluster port status owner datadir logfile; do

            [ -n "${version:-}" ] || continue

            if [ "$version" = "Ver" ]; then
                continue
            fi

            log "Cluster detected: ${version}/${cluster} on port ${port} (${status})"

            #####################################
            # Never start another cluster if the
            # configured endpoint is already ready.
            #####################################

            if pg_ready; then
                log "PostgreSQL is already available at ${PGHOST}:${PGPORT}."
                return 0
            fi

            #####################################
            # Only start a cluster if its port
            # matches the configured PostgreSQL port.
            #
            # This prevents accidentally starting
            # an unrelated cluster.
            #####################################

            if [ "$port" != "$PGPORT" ]; then
                log "Skipping ${version}/${cluster}; cluster uses port ${port}."
                continue
            fi

            if [ "$status" != "online" ]; then

                log "Starting cluster ${version}/${cluster}..."

                if sudo pg_ctlcluster "$version" "$cluster" start; then
                    log "Cluster ${version}/${cluster} start command completed."
                else
                    warning "Could not start cluster ${version}/${cluster}."
                fi

                #################################
                # Check immediately after start.
                #################################

                if pg_ready; then
                    log "PostgreSQL is ready at ${PGHOST}:${PGPORT}."
                    return 0
                fi
            fi

        done < <(
            pg_lsclusters 2>/dev/null |
            tail -n +2 |
            awk '{print $1, $2, $3, $4, $5, $6, $7}'
        )
    fi

    #########################################
    # Final wait
    #########################################

    log "Waiting for PostgreSQL at ${PGHOST}:${PGPORT}..."

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
            2>/dev/null ||
            true
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
echo "PG Host:  $PGHOST"
echo "PG Port:  $PGPORT"
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