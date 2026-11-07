#!/usr/bin/env bash
# Table test for scripts/version.sh (mise run test-scripts).
set -euo pipefail
cd "$(dirname "$0")/.."
fail=0 n=0

ok() { # tag code name prerelease
  local out
  n=$((n + 1))
  if ! out="$(scripts/version.sh "$1" 2>&1)"; then
    echo "FAIL $1: refused: $out"; fail=1; return
  fi
  local want
  want="$(printf 'TBD_VERSION=%s\nTBD_VERSION_NAME=%s\nTBD_VERSION_CODE=%s\nTBD_PRERELEASE=%s' "$1" "$3" "$2" "$4")"
  [[ "$out" == "$want" ]] || { echo "FAIL $1: got"; echo "$out"; echo "want"; echo "$want"; fail=1; }
}
bad() { # tag
  n=$((n + 1))
  if scripts/version.sh "$1" >/dev/null 2>&1; then echo "FAIL $1: accepted"; fail=1; fi
}

ok v0.1.0 10099 0.1.0 false
ok v0.1.0-rc.1 10001 0.1.0-rc.1 true
ok v0.1.0-rc.98 10098 0.1.0-rc.98 true
ok v0.1.1 10199 0.1.1 false
ok v0.10.0 100099 0.10.0 false
ok v1.2.3 1020399 1.2.3 false
ok v1.2.3-rc.4 1020304 1.2.3-rc.4 true
ok v2099.99.99 2099999999 2099.99.99 false

for t in v1.2 v01.2.3 v1.02.3 v1.2.03 v1.2.3-rc.99 v1.2.3-rc.0 v1.2.3-rc.01 v1.100.0 v1.2.100 \
  v2100.0.0 v99999999999999999999.0.0 1.2.3 v1.2.3-beta.1 v1.2.3-rc v1.2.3-rc.1.1 v1.2.3+build \
  'v1.2.3 ' ''; do
  [[ -z "$t" ]] && continue # an empty argument means "the tag at HEAD"
  bad "$t"
done

# Ordering: each rc below its release, each release below the next rc.
code() { scripts/version.sh "$1" | sed -n 's/^TBD_VERSION_CODE=//p'; }
prev=0
for t in v0.1.0-rc.1 v0.1.0-rc.2 v0.1.0 v0.1.1-rc.1 v0.1.1 v0.2.0 v1.0.0-rc.1 v1.0.0; do
  n=$((n + 1))
  c="$(code "$t")"
  ((c > prev)) || { echo "FAIL order: $t ($c) <= $prev"; fail=1; }
  prev="$c"
done

# No argument and no tag at HEAD: a development version that never looks like a release.
n=$((n + 1))
if [[ -z "$(git describe --tags --exact-match --match 'v[0-9]*' HEAD 2>/dev/null)" ]]; then
  dev="$(scripts/version.sh)"
  [[ "$dev" == *$'\nTBD_VERSION_CODE=1\n'* && "$dev" == *'TBD_VERSION_NAME=0.0.0-dev+'* ]] ||
    { echo "FAIL dev version:"; echo "$dev"; fail=1; }
fi

((fail == 0)) || { echo "version_test: FAILED"; exit 1; }
echo "version_test: ok ($n cases)"
