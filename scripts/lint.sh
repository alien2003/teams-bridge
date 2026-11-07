#!/usr/bin/env bash
# gofmt, go vet and staticcheck for both target OSes (BRIEF §7 "Lint").
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p .tmp   # TMPDIR (mise.toml): go vet fails if it is missing, and a fresh clone has none
mapfile -t gofiles < <(git ls-files -co --exclude-standard '*.go' | while read -r f; do [[ -f "$f" ]] && echo "$f"; done)
unformatted="$(gofmt -l "${gofiles[@]}")"
if [[ -n "$unformatted" ]]; then echo "gofmt: needs formatting:"; echo "$unformatted"; exit 1; fi
for os in linux windows; do
  GOOS=$os GOARCH=amd64 CGO_ENABLED=0 go vet ./...
  GOOS=$os GOARCH=amd64 CGO_ENABLED=0 staticcheck ./...
done
echo "lint: ok"
