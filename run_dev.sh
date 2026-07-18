#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

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

exec air