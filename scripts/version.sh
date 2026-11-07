#!/usr/bin/env bash
# Release version from a git tag (D23). Usage: version.sh [TAG]
#
# A release tag is vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-rc.N (no leading zeros). The Android
# versionCode is MAJOR*1000000 + MINOR*10000 + PATCH*100 + (N for an rc, 99 for the final release),
# so every rc installs below its release and each release above the previous one; hence MINOR and
# PATCH < 100, N in 1..98 and MAJOR < 2100 (Android's limit is 2100000000). Anything else fails.
#
# Without TAG: the release tag at HEAD if there is one, else a local development version
# (versionCode 1, versionName 0.0.0-dev+<short commit>[.dirty]), which never matches a release.
# Prints shell assignments (eval or source them; the values need no quoting):
#   TBD_VERSION=v1.2.3-rc.4        tbd's -X main.version and the release name
#   TBD_VERSION_NAME=1.2.3-rc.4    Android versionName
#   TBD_VERSION_CODE=1020304       Android versionCode
#   TBD_PRERELEASE=true            true for an rc tag (the GitHub release is a prerelease)
# Needs only bash and git (the release workflow runs it before any toolchain is installed).
set -euo pipefail
die() { echo "version: $*" >&2; exit 1; }

tag="${1:-}"
if [[ -z "$tag" ]]; then
  tag="$(git describe --tags --exact-match --match 'v[0-9]*' HEAD 2>/dev/null || true)"
  if [[ -z "$tag" ]]; then
    dirty=""
    git diff --quiet HEAD -- 2>/dev/null || dirty=".dirty"
    name="0.0.0-dev+$(git rev-parse --short HEAD 2>/dev/null || echo unknown)$dirty"
    printf 'TBD_VERSION=v%s\nTBD_VERSION_NAME=%s\nTBD_VERSION_CODE=1\nTBD_PRERELEASE=true\n' "$name" "$name"
    exit 0
  fi
fi

num='(0|[1-9][0-9]*)'
[[ "$tag" =~ ^v$num\.$num\.$num(-rc\.([1-9][0-9]*))?$ ]] ||
  die "'$tag' is not a release tag (vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-rc.N, no leading zeros)"
major="${BASH_REMATCH[1]}" minor="${BASH_REMATCH[2]}" patch="${BASH_REMATCH[3]}" rc="${BASH_REMATCH[5]}"
# Length first: arithmetic on a 20-digit number would overflow.
((${#major} <= 4 && major < 2100)) || die "'$tag': MAJOR must be below 2100"
((${#minor} <= 2)) || die "'$tag': MINOR must be below 100"
((${#patch} <= 2)) || die "'$tag': PATCH must be below 100"
if [[ -n "$rc" ]]; then
  ((${#rc} <= 2 && rc <= 98)) || die "'$tag': the rc number must be 1..98"
  step="$rc" prerelease=true
else
  step=99 prerelease=false
fi
code=$((major * 1000000 + minor * 10000 + patch * 100 + step))
printf 'TBD_VERSION=%s\nTBD_VERSION_NAME=%s\nTBD_VERSION_CODE=%d\nTBD_PRERELEASE=%s\n' \
  "$tag" "${tag#v}" "$code" "$prerelease"
