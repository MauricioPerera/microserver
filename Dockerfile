# syntax=docker/dockerfile:1

# --- build stage ---
# Same Debian base (bookworm) in both stages on purpose: the binary is
# cgo-linked (SQLite + sqlite-vec compiled in via mattn/go-sqlite3 and
# asg017/sqlite-vec-go-bindings), so it dynamically links glibc at runtime.
# Building on glibc and running on musl (e.g. Alpine) would break at startup.
FROM golang:1.26-bookworm AS builder

RUN apt-get update && apt-get install -y --no-install-recommends build-essential \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY . .

# Dependencies are vendored (vendor/), so this needs no network access.
ENV CGO_ENABLED=1
RUN go build -o /out/microserver .

# --- runtime stage ---
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --home-dir /data --shell /usr/sbin/nologin microserver \
    && mkdir -p /data && chown microserver:microserver /data

COPY --from=builder /out/microserver /usr/local/bin/microserver

USER microserver
WORKDIR /data
# vec.db, its -wal/-shm sidecars, and backups/ are all created relative to
# the working directory — mount this to persist data across recreations.
VOLUME ["/data"]

# 0.0.0.0, not the app's native 127.0.0.1 default: inside a container the
# reverse proxy (if any) is normally a separate container reaching this one
# over the Docker network, not via loopback. Put this container on an
# internal-only network and let a TLS-terminating proxy container publish
# the public port instead of exposing 8080 directly.
ENV HTTP_ADDR=0.0.0.0:8080
EXPOSE 8080

# AUTH_USERNAME and AUTH_PASSWORD are required and intentionally not set
# here — baking credentials into the image would leak them to anyone with
# the image. Pass them at `docker run` / compose time.
# OLLAMA_URL: defaults to localhost:11434, which is this container, not the
# Docker host. Override it — see README.

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD curl -f http://127.0.0.1:8080/health || exit 1

ENTRYPOINT ["microserver"]
