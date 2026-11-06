#!/usr/bin/env bash
# Remove the registry entries host Edge created for *our* profile (D14): the PreferenceMACs\tbd key
# and the PrefsEnclave values that did not exist in the baseline snapshot. Edge's shared per-user
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
base="$WIN_SB_UNIX/snapshots/baseline.reg.tsv"
key='HKEY_CURRENT_USER\Software\Microsoft\Edge\PrefsEnclave'
[[ -f "$base" ]] || { echo "clean-edge-registry: no baseline; PrefsEnclave left alone"; exit 0; }
"$reg" query 'HKCU\Software\Microsoft\Edge\PrefsEnclave' 2>/dev/null | tr -d '\r' | awk '$2 ~ /^REG_/ {print $1}' | while read -r name; do
  if ! awk -F'\t' -v k="$key" -v n="$name" '$1=="V" && $2==k && $3==n {f=1} END{exit !f}' "$base"; then
    "$reg" delete 'HKCU\Software\Microsoft\Edge\PrefsEnclave' /v "$name" /f | tr -d '\r'
  fi
done
echo "clean-edge-registry: done"
