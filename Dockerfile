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
 && adduser -D -u 1000 -h /data metamusic \
 && mkdir -p /data && chown 1000:1000 /data
COPY --from=build /out/metamusic /usr/local/bin/metamusic
# MM_PUBLIC_URL must be set at run time to the externally visible URL (it feeds the MCP
# WWW-Authenticate metadata URL and the Secure-cookie decision). Secrets: /run/secrets.
ENV MM_ADDR=0.0.0.0:8080 MM_DATA_DIR=/data
USER 1000:1000
WORKDIR /data
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["metamusic", "healthcheck"]
ENTRYPOINT ["metamusic"]
CMD ["serve"]
