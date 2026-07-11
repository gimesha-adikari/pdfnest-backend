# syntax=docker/dockerfile:1.7

FROM golang:1.26-bookworm AS go-builder
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/server .

FROM python:3.12-slim-bookworm AS runtime
ENV DEBIAN_FRONTEND=noninteractive
ENV PYTHONDONTWRITEBYTECODE=1
ENV PYTHONUNBUFFERED=1
ENV PORT=10000
ENV PATH="/app/venv/bin:${PATH}"

RUN apt-get update && apt-get install -y --no-install-recommends \
    ghostscript \
    tesseract-ocr \
    chromium \
    python3-venv \
    python3-pip \
    libpango-1.0-0 \
    libcairo2 \
    libgdk-pixbuf-2.0-0 \
    libffi8 \
    libssl3 \
    libjpeg62-turbo \
    zlib1g \
    shared-mime-info \
    fonts-dejavu-core \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY requirements.txt /app/requirements.txt
RUN python -m venv /app/venv && \
    /app/venv/bin/pip install --no-cache-dir --upgrade pip setuptools wheel && \
    /app/venv/bin/pip install --no-cache-dir -r /app/requirements.txt

COPY --from=go-builder /out/server /app/server
COPY --from=go-builder /src/scripts /app/scripts

# Keep if your app reads any runtime files from the repo root.
COPY --from=go-builder /src/config /app/config

EXPOSE 10000
CMD ["/app/server"]