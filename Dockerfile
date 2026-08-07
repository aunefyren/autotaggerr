# ---------- Build ----------
FROM golang:1.25.0-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /app

# faster caching
COPY go.mod go.sum ./
RUN go mod download

COPY . .

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
