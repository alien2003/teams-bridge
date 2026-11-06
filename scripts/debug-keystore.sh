#!/usr/bin/env bash
# Generate the project-local Android debug keystore $ROOT/.tools/debug.keystore (BRIEF §1.1) if it is
# missing. android/app/build.gradle.kts points signingConfigs.debug at it, so AGP never creates
# ~/.android/debug.keystore. Same parameters as the SDK's default debug key.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
KS="$ROOT/.tools/debug.keystore"

if [[ -f "$KS" ]]; then
  echo "debug-keystore: $KS exists"
  exit 0
fi
command -v keytool >/dev/null || { echo "debug-keystore: keytool not on PATH; run 'mise run debug-keystore'" >&2; exit 1; }
mkdir -p "$(dirname "$KS")"
keytool -genkeypair -noprompt \
  -keystore "$KS" -storetype PKCS12 -storepass android \
  -alias androiddebugkey -keypass android \
  -keyalg RSA -keysize 2048 -validity 10950 \
  -dname "CN=Android Debug,O=Android,C=US"
echo "debug-keystore: created $KS (alias androiddebugkey, passwords 'android', RSA 2048, 30 years)"
