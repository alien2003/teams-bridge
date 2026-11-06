#!/usr/bin/env bash
# BRIEF §7 "Windows parity": run every cross-compiled test binary on the host, cwd = its mirror dir,
# TBD_HOME/TEMP/TMP inside the sandbox; then diff the host against the baseline.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
source "$ROOT/scripts/win/lib.sh"
cd "$ROOT"
orig_wslenv="${WSLENV:-}"
[[ -f "$WIN_SB_UNIX/snapshots/baseline.files.tsv" ]] || "$ROOT/scripts/win/check-outside-win.sh" --baseline
scripts/win/sync-win.sh
run="$(date +%Y%m%d-%H%M%S)"
logs="$WIN_SB_UNIX/logs/$run"; mkdir -p "$logs" "$WIN_SB_UNIX/tmp" "$WIN_SB_UNIX/home"
export TEMP="$WIN_SB_UNIX/tmp" TMP="$WIN_SB_UNIX/tmp" TBD_HOME="$WIN_SB_UNIX/home"
export TBD_REQUIRE_BROWSER=1 TBD_EXE="$WIN_SB_UNIX/dist/tbd.exe"
export WSLENV="TBD_HOME/p:TEMP/p:TMP/p:TBD_EXE/p:TBD_REQUIRE_BROWSER${WSLENV:+:$WSLENV}"
# One-time sanity check that the variables arrive translated (BRIEF §1.5).
( cd "$WIN_SB_UNIX" && cmd.exe /c set 2>/dev/null | tr -d '\r' | command grep -E '^(TBD_HOME|TEMP|TMP|TBD_EXE)=' ) > "$logs/env.txt"
cat "$logs/env.txt"
command grep -q "^TEMP=$(printf '%s' "$WIN_SB" | sed 's/\\/\\\\/g')" "$logs/env.txt" || { echo "test-win: TEMP did not reach Windows translated" >&2; exit 1; }

realtemp="$(wslpath "$(cd "$WIN_SB_UNIX" && cmd.exe /c 'echo %LOCALAPPDATA%' | tr -d '\r')")/Temp"
mapfile -t scoped_before < <(ls -1 "$realtemp" 2>/dev/null | command grep '^scoped_dir' || true)
"$ROOT/scripts/win/edge-registry-owned.sh" before "$logs" || true
fail=0
while IFS= read -r -d '' exe; do
  dir="$(dirname "$exe")"; rel="${dir#"$WIN_SB_UNIX/test/"}"; log="$logs/${rel//\//_}.log"
  echo "test-win: $rel"
  if ( cd "$dir" && "./$(basename "$exe")" -test.v -test.count=1 -test.timeout=20m ) < /dev/null > "$log" 2>&1; then
    echo "  ok   ($(command grep -c '^--- PASS' "$log") passed, $(command grep -c '^--- SKIP' "$log") skipped)"
  else
    echo "  FAIL (see $log)"; { command grep -E '^(--- FAIL|panic:|FAIL)' "$log" | head -20 | sed 's/^/    /'; } || true; fail=1
  fi
done < <(find "$WIN_SB_UNIX/test" -name '*.test.exe' -print0 | sort -z)

# Our Edge runs with TEMP inside the sandbox (D4). Chromium names its temp dirs scoped_dir<pid>_*:
# a new one in the real %TEMP% whose process is gone by now is suspicious (the desktop apps that also
# create them are still running; our test browsers are not).
newscoped="$(comm -13 <(printf '%s\n' "${scoped_before[@]}" | sort) <(ls -1 "$realtemp" 2>/dev/null | command grep '^scoped_dir' | sort) || true)"
for d in $newscoped; do
  pid="${d#scoped_dir}"; pid="${pid%%_*}"
  if ( cd "$WIN_SB_UNIX" && tasklist.exe /FI "PID eq $pid" /NH /FO CSV 2>/dev/null | tr -d '\r' | command grep -q "\"$pid\"" ); then
    echo "test-win: new $d in the real TEMP belongs to a running process (not ours)"
  else
    echo "test-win: new $d in the real TEMP from an exited process — possibly our browser" >&2; fail=1
  fi
done
sb_scoped=$(ls -1 "$WIN_SB_UNIX/tmp" 2>/dev/null | command grep -c '^scoped_dir' || true)
echo "test-win: $sb_scoped scoped_dir* left in the sandbox TEMP"
# Record the PrefsEnclave values our browsers added during this run (clean-edge-registry deletes
# exactly those, never the user's own).
"$ROOT/scripts/win/edge-registry-owned.sh" after "$logs" || true
# The snapshot must see the user's real %TEMP%, not the sandbox TEMP exported above.
env -u TEMP -u TMP -u TBD_HOME WSLENV="$orig_wslenv" "$ROOT/scripts/win/check-outside-win.sh" || fail=1
(( fail == 0 )) && echo "test-win: all green (logs: $WIN_SB\\logs\\$run)" || { echo "test-win: FAILED (logs: $WIN_SB\\logs\\$run)"; exit 1; }
