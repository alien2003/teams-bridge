#!/usr/bin/env bash
# What the Go tests leave behind on a GitHub macOS runner, outside the repository (D26; the macOS
# counterpart of the D14 measurement). CI only (ci.yml, the macOS legs of the `go` job):
#   ci-mac-footprint.sh before   stamp + the state of the login keychain and Notification Center
#   ci-mac-footprint.sh after    report (paths and counts only, never contents) to stdout and the
#                                step summary; exit 1 if the tests left something that must not stay
# Fails on: a new "Microsoft Edge Safe Storage" keychain item (the browser ignored
# --use-mock-keychain), a loaded tbd-test-* launchd job or a ~/Library/LaunchAgents/tbd-test-* file,
# a job or plist named tbd (tests must never touch the user's own job), the default data directory,
# a tbd-test-* directory under ~/Library, or a browser process still running on a --user-data-dir.
# Reported only: files under ~/Library newer than the stamp whose path names a browser, tbd,
# LaunchAgents or background items, launchd's overrides naming tbd-test-*, and Notification Center
# lines naming Edge (whether a headless Edge registers for Notification Center banners on the Mac).
# The macOS runners run it with bash 3.2 (scripts/bash32_test.sh checks it).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
state="$ROOT/.tmp/mac-footprint"
lib="$HOME/Library"

keychain_item() { # yes or no; reads only the item's existence, never its secret
  if security find-generic-password -s "Microsoft Edge Safe Storage" >/dev/null 2>&1; then
    echo yes
  else
    echo no
  fi
}

ncprefs_edge() { # the number of lines of the Notification Center settings that name Edge's bundle
  local n
  n="$(defaults export com.apple.ncprefs - 2>/dev/null | command grep -c -i 'com[.]microsoft[.]edgemac' || true)"
  echo "${n:-0}"
}

browsers() { # browser processes started with a --user-data-dir (the tests' and tbd's, never the image's)
  ps -axww -o pid= -o command= |
    awk '/[M]icrosoft Edge|[C]hromium|[G]oogle Chrome/ && /--user-data-dir=/ { print substr($0, 1, 240) }'
}

case "${1:-}" in
  before)
    mkdir -p "$state"
    touch "$state/stamp"
    keychain_item >"$state/keychain"
    ncprefs_edge >"$state/ncprefs"
    echo "footprint: stamp set; keychain item: $(cat "$state/keychain"); Notification Center lines naming Edge: $(cat "$state/ncprefs")"
    exit 0
    ;;
  after) ;;
  *)
    echo "usage: $0 before|after" >&2
    exit 2
    ;;
esac

[[ -f "$state/stamp" ]] || { echo "footprint: no stamp; run '$0 before' first" >&2; exit 2; }
out="$state/report.md"
fail=0
problem() {
  echo "- **FAIL:** $*" >>"$out"
  echo "::error title=macOS footprint::$*"
  fail=1
}

# A browser that is still shutting down gets 10 s.
left=""
for _ in 1 2 3 4 5 6 7 8 9 10; do
  left="$(browsers)"
  [[ -z "$left" ]] && break
  sleep 1
done

{
  echo "### macOS footprint: $(sw_vers -productVersion) $(uname -m)"
  echo
} >"$out"

kc_before="$(cat "$state/keychain")"
kc_after="$(keychain_item)"
echo "- keychain item \"Microsoft Edge Safe Storage\": before $kc_before, after $kc_after" >>"$out"
if [[ "$kc_before" == no && "$kc_after" == yes ]]; then
  problem "the tests created the keychain item \"Microsoft Edge Safe Storage\" (--use-mock-keychain ignored)"
fi

nc_before="$(cat "$state/ncprefs")"
nc_after="$(ncprefs_edge)"
echo "- Notification Center lines naming Edge: before $nc_before, after $nc_after" >>"$out"
if [[ "$nc_after" != "$nc_before" ]]; then
  echo "::warning title=macOS footprint::Notification Center settings naming Edge changed ($nc_before -> $nc_after)"
fi

# The domain print also lists launchd's lasting enable/disable overrides ("disabled services"),
# which no launchctl verb removes: a name counts as a job left only if launchd has it loaded.
domain="gui/$(id -u)"
names="$(launchctl print "$domain" 2>/dev/null | command grep -o 'tbd-test-[0-9a-f]*' | sort -u || true)"
jobs=""
for n in $names; do
  if launchctl print "$domain/$n" >/dev/null 2>&1; then jobs="$jobs $n"; fi
done
echo "- tbd-test-* launchd jobs left:${jobs:- none}" >>"$out"
[[ -z "$jobs" ]] || problem "tbd-test-* launchd jobs left:$jobs"
overrides="$(launchctl print-disabled "$domain" 2>/dev/null | command grep -c 'tbd-test-' || true)"
echo "- launchd overrides naming tbd-test-* (launchctl print-disabled, report only): ${overrides:-0}" >>"$out"
if launchctl print "gui/$(id -u)/tbd" >/dev/null 2>&1; then
  problem "a launchd job named tbd exists (tests must use tbd-test-*)"
fi

agents=""
for f in "$lib/LaunchAgents/tbd.plist" "$lib/LaunchAgents"/tbd-test-*; do
  if [[ -e "$f" ]]; then agents="$agents ${f##*/}"; fi
done
echo "- tbd files in ~/Library/LaunchAgents:${agents:- none}" >>"$out"
[[ -z "$agents" ]] || problem "left in ~/Library/LaunchAgents:$agents"

dirs=""
for d in "$lib/Application Support/tbd" "$lib/Application Support"/tbd-test-* "$lib/Caches"/tbd-test-*; do
  if [[ -e "$d" ]]; then dirs="$dirs ~/${d#"$HOME"/}"; fi
done
echo "- tbd data directories under ~/Library:${dirs:- none}" >>"$out"
[[ -z "$dirs" ]] || problem "tbd directories left under ~/Library:$dirs"

echo "- browser processes on a --user-data-dir: $(printf '%s' "$left" | command grep -c . || true)" >>"$out"
if [[ -n "$left" ]]; then
  problem "browser processes left running on a --user-data-dir"
  {
    echo
    echo '```'
    echo "$left"
    echo '```'
  } >>"$out"
fi

# Paths only (never contents), pruning the image's large developer and container trees.
files="$(find "$lib" \( -path "$lib/Developer" -o -path "$lib/Containers" -o -path "$lib/Group Containers" \) -prune -o \
  -newer "$state/stamp" \( -ipath '*edge*' -o -ipath '*microsoft*' -o -ipath '*chrom*' -o -ipath '*tbd*' \
  -o -ipath '*LaunchAgents*' -o -ipath '*backgroundtask*' -o -ipath '*crashpad*' \) -print 2>/dev/null | sort || true)"
count="$(printf '%s' "$files" | command grep -c . || true)"
{
  echo "- files under ~/Library newer than the stamp naming a browser, tbd, LaunchAgents, background items or Crashpad: $count"
  if [[ -n "$files" ]]; then
    echo
    echo '```'
    printf '%s\n' "$files" | sed "s|^$HOME/|~/|" | head -n 150
    echo '```'
  fi
} >>"$out"

cat "$out"
if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then cat "$out" >>"$GITHUB_STEP_SUMMARY"; fi
if ((fail)); then echo "footprint: FAILED" >&2; exit 1; fi
echo "footprint: ok"
