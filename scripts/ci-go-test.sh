#!/usr/bin/env bash
# Usage: scripts/ci-go-test.sh fast|media <covdata-dir>
# CI runs the Go tests as two parallel jobs (M2c C67): "media" holds the packages that decode audio with
# the real ffmpeg/flac/metaflac and the acceptance tier; "fast" is every other package, so the union is
# always the whole module. Both run -race -shuffle=on with coverage data written to <covdata-dir>.
set -euo pipefail
tier=${1:?tier fast|media}
cov=${2:?covdata dir}
media=(
  ./cmd/metamusic ./internal/acceptance ./internal/analysis/ffmpeg ./internal/analysis/spectro
  ./internal/archivetest ./internal/importer ./internal/jobs ./internal/media/clip ./internal/media/ingest
  ./internal/provider/fake ./internal/servertest ./internal/synth
)
mkdir -p "$cov"
case "$tier" in
  media) pkgs=("${media[@]}") ;;
  fast)
    mapfile -t pkgs < <(go list ./... | sed "s|^$(go list -m)|.|" | grep -v -x -F "$(printf '%s\n' "${media[@]}")")
    ;;
  *) echo "ci-go-test: unknown tier $tier" >&2; exit 2 ;;
esac
[ "${#pkgs[@]}" -gt 0 ] || { echo "ci-go-test: no packages for tier $tier" >&2; exit 2; }
exec go test -timeout 18m -race -shuffle=on -cover "${pkgs[@]}" -args -test.gocoverdir="$(cd "$cov" && pwd)"
