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
ENV LANG=en_US.UTF-8
ENV LANGUAGE=en_US:en
ENV LC_ALL=en_US.UTF-8
ARG DEBIAN_FRONTEND=noninteractive

WORKDIR /app

COPY --from=builder /app .

# Install dependencies and locales
RUN apt update && \
    apt install -y ca-certificates curl flac ffmpeg locales && \
    sed -i '/en_US.UTF-8/s/^# //g' /etc/locale.gen && \
    locale-gen && \
    update-locale LANG=en_US.UTF-8 && \
    export LANG=en_US.UTF-8

# Create a user and group with the specified UID and GID
RUN groupadd -g ${PGID} appgroup && \
    useradd -m -u ${PUID} -g appgroup appuser

# Copy and set permissions
RUN chmod +x /app/autotaggerr /app/entrypoint.sh && \
    chown -R appuser:appgroup /app

USER appuser

ENTRYPOINT ["/app/entrypoint.sh"]