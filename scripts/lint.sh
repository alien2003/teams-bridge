#!/usr/bin/env bash
# gofmt, go vet and staticcheck for every target (BRIEF §7 "Lint", D26): go vet for linux/amd64,
# windows/amd64, darwin/arm64 and darwin/amd64; staticcheck for the first three (darwin/amd64 has
# the same build tags as darwin/arm64). Runs on Linux only (the dev PC and CI's lint job).
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p .tmp   # TMPDIR (mise.toml): go vet fails if it is missing, and a fresh clone has none
mapfile -t gofiles < <(git ls-files -co --exclude-standard '*.go' | while read -r f; do [[ -f "$f" ]] && echo "$f"; done)
unformatted="$(gofmt -l "${gofiles[@]}")"
if [[ -n "$unformatted" ]]; then echo "gofmt: needs formatting:"; echo "$unformatted"; exit 1; fi
for target in linux/amd64 windows/amd64 darwin/arm64 darwin/amd64; do
  GOOS=${target%/*} GOARCH=${target#*/} CGO_ENABLED=0 go vet ./...
done
for target in linux/amd64 windows/amd64 darwin/arm64; do
  GOOS=${target%/*} GOARCH=${target#*/} CGO_ENABLED=0 staticcheck ./...
done
echo "lint: ok"
