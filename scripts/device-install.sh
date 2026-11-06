#!/usr/bin/env bash
# BRIEF §6: optional device install — only if adb.exe exists on the host, a device is listed and
# TBD_DEVICE_INSTALL=1. adb itself writes %USERPROFILE%\.android (its keys); that is the user's adb.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
apk="$ROOT/dist/teams-bridge-debug.apk"
if [[ "${TBD_DEVICE_INSTALL:-}" != "1" ]]; then
  echo "device-install: skipped (set TBD_DEVICE_INSTALL=1 to install $apk with the host's adb.exe)"; exit 0
fi
adb="$(command -v adb.exe || true)"
[[ -n "$adb" ]] || { echo "device-install: skipped (no adb.exe on the host)"; exit 0; }
[[ -f "$apk" ]] || { echo "device-install: $apk missing — run mise run build" >&2; exit 1; }
source "$ROOT/scripts/win/lib.sh"
mkdir -p "$WIN_SB_UNIX/dist" && cp "$apk" "$WIN_SB_UNIX/dist/"
cd "$WIN_SB_UNIX"
devices="$("$adb" devices | tr -d '\r' | awk 'NR>1 && $2=="device"{print $1}')"
[[ -n "$devices" ]] || { echo "device-install: skipped (adb lists no device)"; exit 0; }
for d in $devices; do
  echo "device-install: installing on $d"
  "$adb" -s "$d" install -r "$WIN_SB\\dist\\teams-bridge-debug.apk"
done
