# syntax=docker/dockerfile:1
FROM oven/bun:1.4-alpine AS web
WORKDIR /src
COPY openapi.json ./openapi.json
COPY web/ ./web/
WORKDIR /src/web
# Tolerates a tree without the SPA sources (go build still works via dist/.gitkeep).
RUN --mount=type=cache,target=/root/.bun/install/cache \
    if [ -f package.json ]; then bun install --frozen-lockfile && bun run build; else mkdir -p dist; fi

FROM golang:1.27.0-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
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
ENV MM_ADDR=0.0.0.0:8080 MM_DATA_DIR=/data
USER 1000:1000
WORKDIR /data
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["metamusic", "healthcheck"]
ENTRYPOINT ["metamusic"]
CMD ["serve"]
