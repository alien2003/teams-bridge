#!/usr/bin/env bash
# BRIEF §9, D26: dist/tbd (linux/amd64, static), dist/tbd.exe (windows/amd64), dist/tbd-darwin-arm64
# and dist/tbd-darwin-amd64 (macOS, Apple Silicon and Intel), dist/teams-bridge-debug.apk.
# Usage: build.sh [all|go|linux|windows|darwin|android]
#   all = linux windows darwin android, go = linux windows darwin.
# Every Go binary is CGO_ENABLED=0 and cross-built from any host; the Go linker signs darwin/arm64
# ad hoc by itself (never strip or modify a darwin binary afterwards: that breaks the signature).
# The version stamped into tbd is TBD_VERSION if set (scripts/build-release.sh: the release tag),
# else `git describe`.
# The macOS CI job runs this script with the system bash 3.2: no bash 4 constructs (checked by
# scripts/bash32_test.sh, part of mise run test-scripts).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
mkdir -p .tmp   # TMPDIR (mise.toml); a fresh clone has none
usage() { echo "usage: build.sh [all|go|linux|windows|darwin|android]" >&2; exit 2; }
what="${1:-all}"
case "$what" in
  all) targets="linux windows darwin android" ;;
  go) targets="linux windows darwin" ;;
  linux|windows|darwin|android) targets="$what" ;;
  *) usage ;;
esac
version="${TBD_VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
ldflags="-s -w -X main.version=$version"
mkdir -p dist
build_go() { # os arch out
  CGO_ENABLED=0 GOOS="$1" GOARCH="$2" go build -trimpath -ldflags "$ldflags" -o "$3" ./cmd/tbd
  echo "build: $3 ($(du -h "$3" | cut -f1), $version)"
}
for t in $targets; do
  case "$t" in
    linux)
      build_go linux amd64 dist/tbd
      # ldd exits non-zero for a static binary; only a Linux host has a ldd that reads ELF.
      if [[ "$(uname -s)" == Linux ]] && command -v ldd >/dev/null && ldd dist/tbd >/dev/null 2>&1; then
        echo "build: dist/tbd is not static" >&2
        exit 1
      fi
      ;;
    windows)
      build_go windows amd64 dist/tbd.exe ;;
    darwin)
      build_go darwin arm64 dist/tbd-darwin-arm64
      build_go darwin amd64 dist/tbd-darwin-amd64
      ;;
    android)
      scripts/build-android.sh ;;
  esac
done
