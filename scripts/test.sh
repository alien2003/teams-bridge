#!/usr/bin/env bash
# BRIEF §7: everything except the device install and the Windows host run (mise run test-win).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
mkdir -p .tmp                               # TMPDIR (mise.toml); a fresh clone has none
scripts/lint.sh
scripts/test-scripts.sh                     # the release scripts' table tests
scripts/fetch-edge.sh >/dev/null            # the E2E tests need a real browser
scripts/build.sh windows                    # build-go's other half (test-go builds dist/tbd)
scripts/test-go.sh
if [[ -z "${DISPLAY:-}${WAYLAND_DISPLAY:-}" ]]; then
  echo "test: NOTICE — no display in this WSL: the windowed \`tbd login\` test was skipped on Linux (it runs on Windows in test-win)"
fi
scripts/test-android.sh
echo "test: all green"
