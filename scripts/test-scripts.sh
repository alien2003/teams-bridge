#!/usr/bin/env bash
# Table tests of the release scripts (D23): version.sh and release-notes.sh. Needs only bash and git.
set -euo pipefail
cd "$(dirname "$0")/.."
scripts/version_test.sh
scripts/release-notes_test.sh
