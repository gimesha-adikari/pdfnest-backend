# syntax=docker/dockerfile:1.7

FROM golang:1.26-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./

RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -o /out/server .

FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive \
    PORT=10000 \
    CHROMIUM_PATH=/usr/bin/chromium

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    dumb-init \
    chromium \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /out/server /app/server
COPY --from=builder /src/config /app/config

RUN useradd --system --uid 10001 --create-home appuser && \
    chown -R appuser:appuser /app

USER appuser

EXPOSE 10000

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD curl -fsS "http://127.0.0.1:${PORT}/api/health" || exit 1

ENTRYPOINT ["dumb-init", "--"]
CMD ["/app/server"]