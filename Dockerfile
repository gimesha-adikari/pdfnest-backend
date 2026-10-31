# syntax=docker/dockerfile:1.7

FROM golang:1.26-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./

RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=readonly -o /out/server .

FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive \
    PORT=10000 \
    CHROMIUM_PATH=/usr/bin/chromium

RUN rm -f /etc/apt/apt.conf.d/docker-clean && \
    apt-get -o Acquire::Retries=3 -o Acquire::http::Timeout=30 update && \
    apt-get -o Acquire::Retries=3 -o Acquire::http::Timeout=30 install -y --no-install-recommends \
    ca-certificates \
    curl \
    dumb-init \
    ghostscript \
    chromium \
    git

WORKDIR /app

COPY --from=builder /out/server /app/server
COPY --from=builder /src/config /app/config

RUN useradd --system --uid 10001 --create-home appuser && \
    mkdir -p /tmp/platen_storage && \
    chown -R appuser:appuser /app /tmp/platen_storage

USER appuser

EXPOSE 10000

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD curl -fsS "http://127.0.0.1:${PORT}/api/health" || exit 1

ENTRYPOINT ["dumb-init", "--"]
CMD ["/app/server"]