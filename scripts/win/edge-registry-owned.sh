#!/usr/bin/env bash
# Usage: edge-registry-owned.sh before|after <dir>. Lists the HKCU\Software\Microsoft\Edge\PrefsEnclave
# value names before and after a test run; names that appear in between were created by our test
# browsers and are appended to snapshots/ours.prefsenclave.txt for clean-edge-registry.sh (D14).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
source "$ROOT/scripts/win/lib.sh"
phase="$1" dir="$2"
names() { ( cd "$WIN_SB_UNIX" && /mnt/c/Windows/System32/reg.exe query 'HKCU\Software\Microsoft\Edge\PrefsEnclave' < /dev/null 2>/dev/null ) | tr -d '\r' | awk '$2 ~ /^REG_/ {print $1}' | LC_ALL=C sort -u; }
names > "$dir/prefsenclave.$phase.txt"
if [[ "$phase" == after && -f "$dir/prefsenclave.before.txt" ]]; then
  mkdir -p "$WIN_SB_UNIX/snapshots"
  LC_ALL=C comm -13 "$dir/prefsenclave.before.txt" "$dir/prefsenclave.after.txt" >> "$WIN_SB_UNIX/snapshots/ours.prefsenclave.txt"
  LC_ALL=C sort -u -o "$WIN_SB_UNIX/snapshots/ours.prefsenclave.txt" "$WIN_SB_UNIX/snapshots/ours.prefsenclave.txt"
fi
