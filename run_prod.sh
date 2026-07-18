#!/usr/bin/env bash
set -Eeuo pipefail

#############################################
# PDFNest Backend Production Runner
#############################################

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

echo "========================================="
echo " PDFNest Backend"
echo "========================================="

#############################################
# Check required commands
#############################################

require() {
    command -v "$1" >/dev/null 2>&1 || {
        echo ""
        echo "Missing dependency: $1"
        echo "Please install it first."
        exit 1
    }
}

require go

#############################################
# Download Go modules
#############################################

echo "Downloading Go modules..."
go mod download

#############################################
# Build backend
#############################################

echo "Building backend..."

mkdir -p bin

go build -o bin/server .

#############################################
# Environment
#############################################

if [ -f ".env" ]; then
    set -a
    source .env
    set +a
fi

#############################################
# Run
#############################################

echo ""
echo "========================================="
echo " Backend Started"
echo "========================================="
echo ""

exec ./bin/server