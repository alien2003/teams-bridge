#!/usr/bin/env bash
# go test ./... the way `mise run test` and CI run it: build the binary first (cmd/tbd's binary tests
# run it), TBD_REQUIRE_BROWSER=1 so a missing browser fails instead of skipping, log in
# .tmp/go-test.log. Linux: dist/tbd. Windows (CI, Git Bash; the dev PC uses test-win): dist/tbd.exe.
# Extra arguments go to `go test` (e.g. -run TestX). Run through mise.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
mkdir -p .tmp
if [[ "$(go env GOOS)" == windows ]]; then
  scripts/build.sh windows
  exe="$ROOT/dist/tbd.exe"
  # Git Bash would hand a /d/a/... path to the native test binaries as is.
  command -v cygpath >/dev/null && exe="$(cygpath -m "$exe")"
else
  scripts/build.sh linux
  exe="$ROOT/dist/tbd"
fi
export TBD_REQUIRE_BROWSER=1 TBD_EXE="$exe"
# On CI runners (4 vCPUs) several packages start their own browsers at once, and a cold Edge start
# then exceeded the 30 s DevTools timeout (D25): run at most two packages at a time there.
par=()
[[ "${GITHUB_ACTIONS:-}" == true ]] && par=(-p 2)
go test -count=1 -timeout 15m "${par[@]}" "$@" ./... 2>&1 | tee .tmp/go-test.log
