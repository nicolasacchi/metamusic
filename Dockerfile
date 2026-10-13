# syntax=docker/dockerfile:1
FROM oven/bun:1.4.3-alpine@sha256:629e17411f1f129dbec3af78d5af9c9f2a937435c80349437206c6b0b7422373 AS web
WORKDIR /src
COPY openapi.json ./openapi.json
WORKDIR /src/web
# Manifests first so source edits do not re-run install. embed.go is always present, which
# keeps the glob COPY valid on a tree without the SPA sources.
COPY web/embed.go web/package.json* web/bun.lock* web/bunfig.toml* ./
RUN --mount=type=cache,target=/root/.bun/install/cache \
    if [ -f package.json ]; then bun install --frozen-lockfile; fi
COPY web/ ./
# Tolerates a tree without the SPA sources (go build still works via dist/.gitkeep).
RUN --mount=type=cache,target=/root/.bun/install/cache \
    if [ -f package.json ]; then bun run build; else mkdir -p dist; fi

FROM golang:1.27.2-alpine@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY web/embed.go ./web/
COPY --from=web /src/web/dist/ ./web/dist/
ARG VERSION=dev
ARG COMMIT=unknown
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/metamusic ./cmd/metamusic

FROM alpine:3.24
RUN apk add --no-cache ffmpeg flac ca-certificates tzdata \
 && adduser -D -u 1000 -h /state metamusic \
 && mkdir -p /state/data /state/backup && chown -R 1000:1000 /state
# Fail the build if the media tools lack what the import/serve pipeline, the clip service and Tier A
# analysis need (same lists as RequireClips / RequireTierA).
RUN ffmpeg -hide_banner -filters | grep -q ' ebur128 ' \
 && ffmpeg -hide_banner -encoders | grep -q libmp3lame \
 && for f in atrim asetpts afade aresample volume ametadata asetnsamples astats silencedetect aformat showspectrumpic; do \
      ffmpeg -hide_banner -filters | grep -q " $f " || { echo "ffmpeg lacks filter $f" >&2; exit 1; }; done \
 && for e in flac png; do \
      ffmpeg -hide_banner -encoders | grep -q " $e " || { echo "ffmpeg lacks encoder $e" >&2; exit 1; }; done \
 && metaflac --version
COPY --from=build /out/metamusic /usr/local/bin/metamusic
# One parent volume holds data/ and backup/ so snapshots can hardlink masters (same filesystem).
# MM_PUBLIC_URL must be set at run time to the externally visible URL (it feeds the MCP
# WWW-Authenticate metadata URL and the Secure-cookie decision). Secrets: /run/secrets.
ENV MM_ADDR=0.0.0.0:8080 MM_DATA_DIR=/state/data MM_BACKUP_DIR=/state/backup MM_SECRETS_DIR=/run/secrets
USER 1000:1000
WORKDIR /state/data
VOLUME /state
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["metamusic", "healthcheck"]
ENTRYPOINT ["metamusic"]
CMD ["serve"]
