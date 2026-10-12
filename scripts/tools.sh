#!/usr/bin/env bash
# Download pinned golangci-lint and gitleaks into ./bin, verifying sha256.
# Idempotent: skips a tool whose installed version already matches.
set -euo pipefail

if [ "${BASH_VERSINFO[0]}" -lt 4 ]; then
  echo "tools.sh: bash >= 4 required (found $BASH_VERSION); on macOS: brew install bash" >&2
  exit 1
fi

GOLANGCI_VERSION=2.14.0
GITLEAKS_VERSION=8.30.1

declare -A SHA=(
  [golangci-lint-darwin-amd64]=a5667c1c3536be1740133213e1e822bfb8f0d98ea12903174d6d5f635e4ed68d
  [golangci-lint-darwin-arm64]=5ef5f36a7147e91dc58ef9ef4d11bb7bad5ead0c76eb6c01327a73c641d1dcc3
  [golangci-lint-linux-amd64]=ab90aeb7b066f92a33415b638a50fe5344bbb75a0d32ad30cc248d88f81032ab
  [golangci-lint-linux-arm64]=ee7ec5f3453d15ddf106fae5a4d6c71737712348a979d1fe9cd52ec7ea299bae
  [gitleaks-darwin-amd64]=dfe101a4db2255fc85120ac7f3d25e4342c3c20cf749f2c20a18081af1952709
  [gitleaks-darwin-arm64]=b40ab0ae55c505963e365f271a8d3846efbc170aa17f2607f13df610a9aeb6a5
  [gitleaks-linux-amd64]=551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb
  [gitleaks-linux-arm64]=e4a487ee7ccd7d3a7f7ec08657610aa3606637dab924210b3aee62570fb4b080
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

fetch() { # name url archive-binary-name
  local name=$1 url=$2 want tmp got
  want=${SHA[$name-$os-$arch]:-}
  [ -n "$want" ] || { echo "tools.sh: no pinned sha256 for $name-$os-$arch" >&2; exit 1; }
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  echo "tools.sh: downloading $name ($os/$arch)"
  curl -fsSL --retry 3 -o "$tmp/a.tgz" "$url"
  got=$(sha256 "$tmp/a.tgz")
  if [ "$got" != "$want" ]; then
    echo "tools.sh: sha256 mismatch for $name: want $want got $got" >&2
    exit 1
  fi
  tar -xzf "$tmp/a.tgz" -C "$tmp"
  local src
  src=$(find "$tmp" -type f -name "$name" | head -n1)
  [ -n "$src" ] || { echo "tools.sh: $name binary not found in archive" >&2; exit 1; }
  install -m 0755 "$src" "$bin/$name"
  rm -rf "$tmp"
  trap - EXIT
}

have() { # binary substring
  [ -x "$bin/$1" ] && "$bin/$1" --version 2>&1 | grep -q "$2"
}

if have golangci-lint "$GOLANGCI_VERSION"; then
  echo "tools.sh: golangci-lint $GOLANGCI_VERSION present"
else
  fetch golangci-lint "https://github.com/golangci/golangci-lint/releases/download/v${GOLANGCI_VERSION}/golangci-lint-${GOLANGCI_VERSION}-${os}-${arch}.tar.gz"
fi

if have gitleaks "$GITLEAKS_VERSION"; then
  echo "tools.sh: gitleaks $GITLEAKS_VERSION present"
else
  gl_arch=$arch
  [ "$arch" = amd64 ] && gl_arch=x64
  # gitleaks asset names use x64; the pin table is keyed by amd64.
  fetch gitleaks "https://github.com/gitleaks/gitleaks/releases/download/v${GITLEAKS_VERSION}/gitleaks_${GITLEAKS_VERSION}_${os}_${gl_arch}.tar.gz"
fi
echo "tools.sh: done"
