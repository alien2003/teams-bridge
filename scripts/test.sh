#!/usr/bin/env bash
# BRIEF §7: everything except the device install and the Windows host run (mise run test-win).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
scripts/lint.sh
scripts/fetch-edge.sh >/dev/null            # the E2E tests need a real browser
scripts/build.sh go                         # cmd/tbd's binary test runs dist/tbd
export TBD_REQUIRE_BROWSER=1 TBD_EXE="$ROOT/dist/tbd"
go test -count=1 -timeout 15m ./... 2>&1 | tee "$ROOT/.tmp/go-test.log"
if [[ -z "${DISPLAY:-}${WAYLAND_DISPLAY:-}" ]]; then
  echo "test: NOTICE — no display in this WSL: the windowed \`tbd login\` test was skipped on Linux (it runs on Windows in test-win)"
fi
scripts/test-android.sh
echo "test: all green"
