# ---------- Web UI ----------
# The SPA is built here rather than taken from the commit. web/dist is committed so
# that `go build` works without a Node toolchain, but nothing verified it was current —
# so an image could ship a stale bundle from a green CI run, since CI rebuilds the UI
# into its own workspace and then throws it away.
#
# Pinned to the *build* platform, not the target. The bundle is byte-identical for
# every architecture, so without this buildx would emulate a full npm install once per
# target — four of them, including arm/v7 and 386 — to produce four identical
# directories.
#
# Debian rather than alpine: this is a discarded build stage, so its size does not
# reach the final image, and matching CI's glibc removes the one platform variable
# (musl) that a release pipeline should not discover the hard way.
FROM --platform=$BUILDPLATFORM node:22-slim AS web
WORKDIR /src/webui
# Manifests first, so a source-only change reuses the install layer.
COPY webui/package.json webui/package-lock.json ./
RUN npm ci
COPY webui/ ./
# vite writes to ../web/dist (webui/vite.config.ts), i.e. /src/web/dist.
RUN npm run build

# ---------- Build ----------
FROM golang:1.25.0-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /app

# faster caching
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# web/dist is excluded by .dockerignore, so this is the only copy that supplies it and
# a stale committed bundle cannot reach the image. It must land before `go build`:
# package web embeds the directory at compile time (`//go:embed all:dist`), and a
# missing one fails the build loudly rather than shipping an old UI quietly.
COPY --from=web /src/web/dist ./web/dist

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /app/autotaggerr .

# ---------- Runtime ----------
FROM alpine:3.21
ENV PUID=1000 PGID=1000 LANG=C.UTF-8 LC_ALL=C.UTF-8
WORKDIR /app
# chromaprint provides fpcalc, used only by the optional AcoustID identification.
# Bundling it means the feature works out of the box once a data source is added;
# without it Autotaggerr logs the absence once and behaves exactly as before.
# ffmpeg was dropped once ID3 moved in-process: `flac` (metaflac) is the only tag
# binary left. chromaprint still pulls the ffmpeg *libraries* it needs.
RUN apk add --no-cache flac chromaprint ca-certificates tzdata su-exec
COPY --from=builder /app/autotaggerr /app/autotaggerr
COPY --from=builder /app/entrypoint.sh /app/entrypoint.sh
COPY --from=builder /app/web/ /app/web/
RUN chmod +x /app/autotaggerr /app/entrypoint.sh
# Starts as root so the entrypoint can align the runtime user with PUID/PGID and
# fix ownership of the mounted config volume before dropping privileges.
ENTRYPOINT ["/app/entrypoint.sh"]
