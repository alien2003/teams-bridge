#!/usr/bin/env bash
# BRIEF §7: cross-compile dist/tbd.exe and one <pkg>.test.exe per package, copy them plus each package's
# testdata/ (and the shared proto/ vectors) into TBD_WIN_SANDBOX/test/<pkg-dir>/, mirroring the repo.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
source "$ROOT/scripts/win/lib.sh"
cd "$ROOT"
scripts/build.sh windows
mkdir -p "$WIN_SB_UNIX/dist" "$WIN_SB_UNIX/test" "$WIN_SB_UNIX/scripts"
cp dist/tbd.exe "$WIN_SB_UNIX/dist/tbd.exe"
cp scripts/win/*.ps1 "$WIN_SB_UNIX/scripts/"
rm -rf "$WIN_SB_UNIX/test"; mkdir -p "$WIN_SB_UNIX/test"
mapfile -t pkgs < <(go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.Dir}}{{end}}' ./...)
for dir in "${pkgs[@]}"; do
  rel="${dir#"$ROOT"/}"
  dst="$WIN_SB_UNIX/test/$rel"; mkdir -p "$dst"
  name="$(basename "$rel")"
  CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go test -c -trimpath -o "$dst/$name.test.exe" "./$rel"
  [[ -d "$dir/testdata" ]] && cp -r "$dir/testdata" "$dst/"
  echo "sync-win: test/$rel/$name.test.exe"
done
mkdir -p "$WIN_SB_UNIX/test/proto" && cp proto/vectors.json "$WIN_SB_UNIX/test/proto/"
# shellcheck disable=SC2028  # the backslash is part of a Windows path, bash's echo keeps it
echo "sync-win: ${#pkgs[@]} test binaries in $WIN_SB\\test"
