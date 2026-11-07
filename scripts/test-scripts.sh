#!/usr/bin/env bash
# Table tests of the release scripts (D23, D24): version.sh, release-notes.sh, obtainium-link.sh, and the
# bash 3.2 guard over the scripts the macOS jobs run (D26). Needs only bash, git, sed and grep.
set -euo pipefail
cd "$(dirname "$0")/.."
scripts/version_test.sh
scripts/release-notes_test.sh
scripts/obtainium-link_test.sh
scripts/bash32_test.sh
