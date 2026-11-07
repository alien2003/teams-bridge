#!/usr/bin/env bash
# Table tests of the release scripts (D23, D24): version.sh, release-notes.sh, obtainium-link.sh. Needs only bash and git.
set -euo pipefail
cd "$(dirname "$0")/.."
scripts/version_test.sh
scripts/release-notes_test.sh
scripts/obtainium-link_test.sh
