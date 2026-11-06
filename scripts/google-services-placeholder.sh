#!/usr/bin/env bash
# BRIEF §6: the real android/app/google-services.json is user-supplied and gitignored. If it is missing,
# drop in the structurally valid placeholder android/app/google-services.json.example so the APK builds.
# A placeholder build installs and runs, but FCM cannot deliver anything to it. Never overwrites a file.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
REAL="$ROOT/android/app/google-services.json"
EXAMPLE="$ROOT/android/app/google-services.json.example"

if [[ -e "$REAL" ]]; then
  if command grep -q '"tbd-placeholder"' "$REAL"; then
    echo "google-services: $REAL is still the PLACEHOLDER (FCM will not work)"
  else
    echo "google-services: using the real $REAL"
  fi
  exit 0
fi
cp "$EXAMPLE" "$REAL"
cat >&2 <<EOF
************************************************************************************************
* google-services: $REAL was missing.
* Copied the PLACEHOLDER from google-services.json.example so the APK builds.
* FCM WILL NOT WORK with this build. Download the real google-services.json for the Android app
* io.github.alien2003.teamsbridge from the Firebase console, put it at android/app/, rebuild.
************************************************************************************************
EOF
