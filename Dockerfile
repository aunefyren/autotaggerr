# ---------- Build ----------
FROM golang:1.23.4-bullseye AS builder

ARG TARGETARCH
ARG TARGETOS
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /app/autotaggerr ./...

# ---------- Runtime ----------
FROM debian:bullseye-slim AS runtime

LABEL org.opencontainers.image.source="https://github.com/aunefyren/autotaggerr"

# UID/GID at *run time* (still overridable with -e)
ENV PUID=1000 PGID=1000
# Use the lightweight built-in UTF-8; no locales package needed
ENV LANG=C.UTF-8 LC_ALL=C.UTF-8
ARG DEBIAN_FRONTEND=noninteractive

WORKDIR /app

# Install only what's needed, no recommends; clean apt lists afterwards
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates curl ffmpeg flac && \
    rm -rf /var/lib/apt/lists/*

# Copy ONLY the artifacts you need
COPY --from=builder /app/autotaggerr /app/autotaggerr
COPY --from=builder /app/entrypoint.sh /app/entrypoint.sh

# Create user
RUN groupadd -g ${PGID} appgroup && \
    useradd -m -u ${PUID} -g appgroup appuser && \
    chmod +x /app/autotaggerr /app/entrypoint.sh && \
    chown -R appuser:appgroup /app

USER appuser
ENTRYPOINT ["/app/entrypoint.sh"]
