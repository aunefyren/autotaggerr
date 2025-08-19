# ---------- Build ----------
FROM golang:1.23.4-alpine AS builder
ARG TARGETARCH
ARG TARGETOS
RUN apk add --no-cache git
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /app/autotaggerr ./...

# ---------- Runtime ----------
FROM alpine:3.20

LABEL org.opencontainers.image.source="https://github.com/aunefyren/autotaggerr"

ENV PUID=1000 PGID=1000 TZ=UTC LANG=C.UTF-8 LC_ALL=C.UTF-8
WORKDIR /app

# ffmpeg + flac (metaflac) + tzdata if you log local time
RUN apk add --no-cache ffmpeg flac tzdata ca-certificates

COPY --from=builder /app/autotaggerr /app/autotaggerr
COPY --from=builder /app/entrypoint.sh /app/entrypoint.sh

# Create user
RUN addgroup -g ${PGID} appgroup && \
    adduser -D -u ${PUID} -G appgroup appuser && \
    chmod +x /app/autotaggerr /app/entrypoint.sh && \
    chown -R appuser:appgroup /app

USER appuser
ENTRYPOINT ["/app/entrypoint.sh"]
