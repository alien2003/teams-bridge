#!/usr/bin/env bash
# BRIEF §9: dist/tbd (linux/amd64, static), dist/tbd.exe (windows/amd64), dist/teams-bridge-debug.apk.
# Usage: build.sh [all|go|linux|windows|android]
# The version stamped into tbd is TBD_VERSION if set (scripts/build-release.sh: the release tag),
# else `git describe`.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
mkdir -p .tmp   # TMPDIR (mise.toml); a fresh clone has none
what="${1:-all}"
version="${TBD_VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
ldflags="-s -w -X main.version=$version"
mkdir -p dist
build_go() { # os out
  CGO_ENABLED=0 GOOS="$1" GOARCH=amd64 go build -trimpath -ldflags "$ldflags" -o "$2" ./cmd/tbd
  echo "build: $2 ($(du -h "$2" | cut -f1), $version)"
}
case "$what" in
  all|go|linux)
    build_go linux dist/tbd
    if ldd dist/tbd >/dev/null 2>&1; then echo "build: dist/tbd is not static" >&2; exit 1; fi ;;&
  all|go|windows)
    build_go windows dist/tbd.exe ;;&
  all|android)
    scripts/build-android.sh ;;
  go|linux|windows) ;;
  *) echo "usage: build.sh [all|go|linux|windows|android]" >&2; exit 2 ;;
esac
