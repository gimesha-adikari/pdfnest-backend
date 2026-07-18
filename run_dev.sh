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
require uv
require pg_isready

#############################################
# Python
#############################################

if [ ! -d "venv" ]; then
    echo "Creating Python virtual environment..."
    uv venv
fi

source venv/bin/activate

echo "Installing Python dependencies..."
uv pip install -r requirements.txt

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

    until pg_isready -q
    do
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