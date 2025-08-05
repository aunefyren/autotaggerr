FROM golang:1.23.4-bullseye as builder

ARG TARGETARCH
ARG TARGETOS

WORKDIR /app

COPY . .

RUN GO111MODULE=on CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build

FROM debian:bullseye-slim as runtime

LABEL org.opencontainers.image.source = "https://github.com/aunefyren/autotaggerr"

# Let UID/GID be passed in at build or run time
ENV PUID=1000
ENV PGID=1000
ARG DEBIAN_FRONTEND=noninteractive

WORKDIR /app

COPY --from=builder /app .

# Install dependencies
RUN apt update && \
    apt install -y ca-certificates curl flac ffmpeg && \
    rm -rf /var/lib/apt/lists/*

# Create a user and group with the specified UID and GID
RUN groupadd -g ${PGID} appgroup && \
    useradd -m -u ${PUID} -g appgroup appuser

# Copy and set permissions
RUN chmod +x /app/autotaggerr /app/entrypoint.sh && \
    chown -R appuser:appgroup /app

USER appuser

ENTRYPOINT ["/app/entrypoint.sh"]