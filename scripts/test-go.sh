#!/usr/bin/env bash
# go test ./... the way `mise run test` and CI run it: build the binary first (cmd/tbd's binary tests
# run it), TBD_REQUIRE_BROWSER=1 so a missing browser fails instead of skipping, log in
# .tmp/go-test.log. Linux: dist/tbd. Windows (CI, Git Bash; the dev PC uses test-win): dist/tbd.exe.
# macOS (CI only, D26): dist/tbd-darwin-<native arch>. Extra arguments go to `go test` (e.g. -run TestX).
# Run through mise. The macOS runners run it with bash 3.2: no bash 4 constructs, and an array that
# may be empty is expanded as ${a[@]+"${a[@]}"} (scripts/bash32_test.sh checks both).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
mkdir -p .tmp
case "$(go env GOOS)" in
  windows)
    scripts/build.sh windows
    exe="$ROOT/dist/tbd.exe"
    # Git Bash would hand a /d/a/... path to the native test binaries as is.
    if command -v cygpath >/dev/null; then exe="$(cygpath -m "$exe")"; fi
    ;;
  darwin)
    scripts/build.sh darwin
    exe="$ROOT/dist/tbd-darwin-$(go env GOARCH)"
    ;;
  *)
    scripts/build.sh linux
    exe="$ROOT/dist/tbd"
    ;;
esac
export TBD_REQUIRE_BROWSER=1 TBD_EXE="$exe"
# On CI runners (4 vCPUs) several packages start their own browsers at once, and a cold Edge start
# then exceeded the 30 s DevTools timeout (D25): run at most two packages at a time there.
par=()
if [[ "${GITHUB_ACTIONS:-}" == true ]]; then par=(-p 2); fi
go test -count=1 -timeout 15m ${par[@]+"${par[@]}"} "$@" ./... 2>&1 | tee .tmp/go-test.log
