#!/usr/bin/env bash
# Remove the registry entries host Edge created for *our* profile (D14): the PreferenceMACs\tbd key
# and the PrefsEnclave values recorded as ours by test-win (edge-registry-owned.sh). Edge's shared per-user
# bookkeeping (beacons, EdgeUpdate lastrun, StabilityMetrics) belongs to the user's Edge as well and
# is left alone.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
source "$ROOT/scripts/win/lib.sh"
cd "$WIN_SB_UNIX"
reg="/mnt/c/Windows/System32/reg.exe"
if "$reg" query 'HKCU\Software\Microsoft\Edge\PreferenceMACs\tbd' >/dev/null 2>&1; then
  "$reg" delete 'HKCU\Software\Microsoft\Edge\PreferenceMACs\tbd' /f | tr -d '\r'
fi
# PrefsEnclave values: only the names test-win recorded as created by our browsers.
owned="$WIN_SB_UNIX/snapshots/ours.prefsenclave.txt"
[[ -f "$owned" ]] || { echo "clean-edge-registry: no $owned; PrefsEnclave left alone"; exit 0; }
while read -r name; do
  [[ -n "$name" ]] || continue
  "$reg" delete 'HKCU\Software\Microsoft\Edge\PrefsEnclave' /v "$name" /f 2>/dev/null | tr -d '\r' || true
done < "$owned"
echo "clean-edge-registry: done"
