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
    if ! command -v "$1" >/dev/null 2>&1; then
        echo ""
        echo "Missing dependency: $1"
        echo "Please install it first."
        exit 1
    fi
}

require go
require python3

#############################################
# Install uv if missing
#############################################

if ! command -v uv >/dev/null 2>&1; then
    echo "Installing uv..."
    curl -LsSf https://astral.sh/uv/install.sh | sh

    export PATH="$HOME/.local/bin:$PATH"

    if ! command -v uv >/dev/null 2>&1; then
        echo "Failed to install uv."
        exit 1
    fi
fi

#############################################
# Create Python virtual environment
#############################################

if [ ! -d "venv" ]; then
    echo "Creating virtual environment..."
    uv venv
fi

source venv/bin/activate

#############################################
# Upgrade pip
#############################################

python -m pip install --upgrade pip setuptools wheel

#############################################
# Install Python dependencies
#############################################

if [ -f "requirements.txt" ]; then
    echo "Installing Python packages..."
    uv pip install -r requirements.txt
else
    echo "requirements.txt not found."
    exit 1
fi

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
    export $(grep -v '^#' .env | xargs)
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