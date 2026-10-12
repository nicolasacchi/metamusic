#!/usr/bin/env bash
# Download pinned dev/CI tools into ./bin, verifying sha256:
#   golangci-lint, gitleaks, ffmpeg (LGPL static, linux/amd64 only); flac/metaflac only on explicit request
#   (opt-in source build for hosts without a flac package; never part of the default set, never in CI).
# Idempotent: skips a tool whose installed version already matches.
# Usage: tools.sh [--print-urls] [tool ...]   (tools: golangci-lint gitleaks ffmpeg flac; default: all but flac)
set -euo pipefail

if [ "${BASH_VERSINFO[0]}" -lt 4 ]; then
  echo "tools.sh: bash >= 4 required (found $BASH_VERSION); on macOS: brew install bash" >&2
  exit 1
fi

GOLANGCI_VERSION=2.14.0
GITLEAKS_VERSION=8.30.1
# ffmpeg: BtbN LGPL static build (libmp3lame + ebur128, no GPL components). Upstream dated autobuild
# tags are pruned, so the archive is re-hosted unchanged (with source tarball, licence and configure
# files) as an asset of the repo release tools-ffmpeg-<FFMPEG_VERSION>. tools.sh tries the release
# first and falls back to the upstream URL; both are verified against the same sha256.
FFMPEG_VERSION=9.0.2
FFMPEG_BUILD=n9.0.2-27-g7e93014c5b
FFMPEG_ARCHIVE="ffmpeg-${FFMPEG_BUILD}-linux64-lgpl-9.0.tar.xz"
# flac: xiph keeps old releases; opt-in (`tools.sh flac`) and only when flac/metaflac are missing; CI uses apt.
FLAC_VERSION=1.5.0

declare -A SHA=(
  [golangci-lint-darwin-amd64]=a5667c1c3536be1740133213e1e822bfb8f0d98ea12903174d6d5f635e4ed68d
  [golangci-lint-darwin-arm64]=5ef5f36a7147e91dc58ef9ef4d11bb7bad5ead0c76eb6c01327a73c641d1dcc3
  [golangci-lint-linux-amd64]=ab90aeb7b066f92a33415b638a50fe5344bbb75a0d32ad30cc248d88f81032ab
  [golangci-lint-linux-arm64]=ee7ec5f3453d15ddf106fae5a4d6c71737712348a979d1fe9cd52ec7ea299bae
  [gitleaks-darwin-amd64]=dfe101a4db2255fc85120ac7f3d25e4342c3c20cf749f2c20a18081af1952709
  [gitleaks-darwin-arm64]=b40ab0ae55c505963e365f271a8d3846efbc170aa17f2607f13df610a9aeb6a5
  [gitleaks-linux-amd64]=551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb
  [gitleaks-linux-arm64]=e4a487ee7ccd7d3a7f7ec08657610aa3606637dab924210b3aee62570fb4b080
  [ffmpeg-linux-amd64]=12445b38a4700ba1ae7c9c7e57062f602c927dc4187555bfadc9f3e2f75f05b0
  [flac-src]=f2c1c76592a82ffff8413ba3c4a1299b6c7ab06c734dee03fd88630485c2b920
)

root=$(cd "$(dirname "$0")/.." && pwd)
bin="$root/bin"
mkdir -p "$bin"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in linux | darwin) ;; *) echo "tools.sh: unsupported OS $os" >&2; exit 1 ;; esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "tools.sh: unsupported arch $(uname -m)" >&2; exit 1 ;;
esac

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# download_verified <pin-key> <out> <url...>: first URL that downloads wins; sha256 must match the pin.
download_verified() {
  local key=$1 out=$2 want got url
  shift 2
  want=${SHA[$key]:-}
  [ -n "$want" ] || { echo "tools.sh: no pinned sha256 for $key" >&2; exit 1; }
  for url in "$@"; do
    if curl -fsSL --retry 3 -o "$out" "$url"; then
      got=$(sha256 "$out")
      if [ "$got" != "$want" ]; then
        echo "tools.sh: sha256 mismatch for $key: want $want got $got" >&2
        exit 1
      fi
      return 0
    fi
    echo "tools.sh: $url unavailable, trying next source" >&2
  done
  echo "tools.sh: could not download $key" >&2
  exit 1
}

fetch() { # name archive-member-name url...   (installs one binary from an archive)
  local name=$1 member=$2 src
  shift 2
  echo "tools.sh: downloading $name ($os/$arch)"
  download_verified "$name-$os-$arch" "$tmp/$name.archive" "$@"
  mkdir -p "$tmp/$name.x"
  tar -xf "$tmp/$name.archive" -C "$tmp/$name.x"
  src=$(find "$tmp/$name.x" -type f -name "$member" | head -n1)
  [ -n "$src" ] || { echo "tools.sh: $member not found in $name archive" >&2; exit 1; }
  install -m 0755 "$src" "$bin/$member"
  rm -rf "$tmp/$name.x" "$tmp/$name.archive"
}

have() { # binary substring [version-flag]
  local out flag=${3:---version}
  [ -x "$bin/$1" ] || return 1
  out=$("$bin/$1" "$flag" 2>&1) || true
  case "$out" in *"$2"*) return 0 ;; *) return 1 ;; esac
}

urls_golangci-lint() { echo "https://github.com/golangci/golangci-lint/releases/download/v${GOLANGCI_VERSION}/golangci-lint-${GOLANGCI_VERSION}-${os}-${arch}.tar.gz"; }
urls_gitleaks() {
  local gl_arch=$arch # gitleaks asset names use x64; the pin table is keyed by amd64.
  [ "$arch" = amd64 ] && gl_arch=x64
  echo "https://github.com/gitleaks/gitleaks/releases/download/v${GITLEAKS_VERSION}/gitleaks_${GITLEAKS_VERSION}_${os}_${gl_arch}.tar.gz"
}
urls_ffmpeg() {
  [ "$os-$arch" = linux-amd64 ] || return 0
  echo "https://github.com/nicolasacchi/metamusic/releases/download/tools-ffmpeg-${FFMPEG_VERSION}/${FFMPEG_ARCHIVE}"
  echo "https://github.com/BtbN/FFmpeg-Builds/releases/download/autobuild-2026-10-12-13-12/${FFMPEG_ARCHIVE}"
}
urls_flac() { echo "https://ftp.osuosl.org/pub/xiph/releases/flac/flac-${FLAC_VERSION}.tar.xz"; }

all_tools="golangci-lint gitleaks ffmpeg flac"
default_tools="golangci-lint gitleaks ffmpeg"

if [ "${1:-}" = "--print-urls" ]; then
  for t in $all_tools; do
    "urls_$t" | while read -r u; do echo "$t $u"; done
  done
  exit 0
fi

want_tools=${*:-$default_tools}
for t in $want_tools; do
  case " $all_tools " in *" $t "*) ;; *) echo "tools.sh: unknown tool $t" >&2; exit 1 ;; esac
done
wanted() { case " $want_tools " in *" $1 "*) return 0 ;; *) return 1 ;; esac; }

if wanted golangci-lint; then
  if have golangci-lint "$GOLANGCI_VERSION"; then
    echo "tools.sh: golangci-lint $GOLANGCI_VERSION present"
  else
    mapfile -t u < <(urls_golangci-lint)
    fetch golangci-lint golangci-lint "${u[@]}"
  fi
fi

if wanted gitleaks; then
  if have gitleaks "$GITLEAKS_VERSION"; then
    echo "tools.sh: gitleaks $GITLEAKS_VERSION present"
  else
    mapfile -t u < <(urls_gitleaks)
    fetch gitleaks gitleaks "${u[@]}"
  fi
fi

if wanted ffmpeg; then
  if [ "$os-$arch" != linux-amd64 ]; then
    echo "tools.sh: notice: no pinned ffmpeg for $os/$arch; install ffmpeg (libmp3lame, ebur128) on PATH" >&2
  elif have ffmpeg "$FFMPEG_BUILD" -version; then
    echo "tools.sh: ffmpeg $FFMPEG_BUILD present"
  else
    mapfile -t u < <(urls_ffmpeg)
    fetch ffmpeg ffmpeg "${u[@]}"
  fi
fi

if wanted flac; then
  if [ -n "${CI:-}${GITHUB_ACTIONS:-}" ]; then
    echo "tools.sh: notice: CI detected, not building flac (install the flac package with apt)" >&2
  elif have metaflac "$FLAC_VERSION" && have flac "$FLAC_VERSION"; then
    echo "tools.sh: flac/metaflac $FLAC_VERSION present"
  elif command -v flac >/dev/null 2>&1 && command -v metaflac >/dev/null 2>&1; then
    echo "tools.sh: flac and metaflac on PATH ($(metaflac --version)), not building"
  else
    echo "tools.sh: building flac $FLAC_VERSION from source (flac or metaflac missing)"
    mapfile -t u < <(urls_flac)
    download_verified flac-src "$tmp/flac.tar.xz" "${u[@]}"
    mkdir -p "$tmp/flac.x"
    tar -xf "$tmp/flac.tar.xz" -C "$tmp/flac.x"
    (
      cd "$tmp/flac.x/flac-$FLAC_VERSION"
      ./configure --disable-shared --enable-static --disable-ogg --disable-cpplibs --disable-doxygen-docs >/dev/null
      make -j"$(nproc 2>/dev/null || echo 2)" >/dev/null
    )
    install -m 0755 "$tmp/flac.x/flac-$FLAC_VERSION/src/flac/flac" "$bin/flac"
    install -m 0755 "$tmp/flac.x/flac-$FLAC_VERSION/src/metaflac/metaflac" "$bin/metaflac"
  fi
fi
echo "tools.sh: done"
