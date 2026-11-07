#!/usr/bin/env bash
# Test of scripts/obtainium-link.sh (mise run test-scripts): the link decodes back to the config, the
# config pins the release APK and certificate, and README.md carries exactly this link.
set -euo pipefail
cd "$(dirname "$0")/.."
fail=0 n=0
ok() { n=$((n + 1)); } # name
no() { n=$((n + 1)); echo "FAIL $1"; fail=1; }

url="$(scripts/obtainium-link.sh)"
json="$(scripts/obtainium-link.sh --json)"
prefix='https://apps.obtainium.imranr.dev/redirect?r=obtainium://app/'
if [[ "${url:0:${#prefix}}" == "$prefix" ]]; then ok; else no "redirect prefix"; fi
enc="${url:${#prefix}}"
if [[ "$enc" =~ ^([A-Za-z0-9_.!~*\'\(\)-]|%[0-9A-F]{2})+$ ]]; then ok; else no "only unreserved characters and %XX"; fi
# The redirect page reads r with URLSearchParams (one decoding, which would turn a "+" into a space)
# and then decodeURIComponent: so no literal "+" or "&", and the decoded config is the --json output.
if [[ "$enc" != *[+\&]* ]]; then ok; else no "no + or & in the parameter"; fi
dec="$(printf '%b' "${enc//%/\\x}")"
if [[ "$dec" == "$json" ]]; then ok; else no "decodes to the config"; fi

cert="$(command grep -v '^#' android/release-cert.sha256 | tr -d ' \n')"
has() { # name fragment
  if [[ "$json" == *"$2"* ]]; then ok; else no "$1: $2"; fi
}
has id '"id":"io.github.alien2003.teamsbridge"'
has url '"url":"https://github.com/alien2003/teams-bridge"'
has name '"name":"Teams Bridge"'
has author '"author":"alien2003"'
# additionalSettings is a string holding JSON (Obtainium's export format), so its quotes are escaped.
has "settings are a JSON string" '"additionalSettings":"{\"apkFilterRegEx\":'
has "APK filter" '\"apkFilterRegEx\":\"^teams-bridge[.]apk$\"'
has "certificate pin" "\\\"allowedSigningCertHashes\\\":\\\"$cert\\\"}\"}"

filter='^teams-bridge[.]apk$'
if [[ teams-bridge.apk =~ $filter ]]; then ok; else no "filter takes teams-bridge.apk"; fi
for a in teams-bridge-debug.apk teams-bridge.apk.sha256 teams-bridge-mapping.txt.gz SHA256SUMS \
  tbd-linux-amd64 tbd-windows-amd64.exe tbd-darwin-arm64 tbd-darwin-amd64 xteams-bridge.apk teams-bridgeXapk; do
  if [[ "$a" =~ $filter ]]; then no "filter skips $a"; else ok; fi
done

if command grep -qF "href=\"$url\"" README.md; then ok; else no "README.md carries the generated link"; fi
if command grep -qF 'src="docs/assets/badge_obtainium.png"' README.md; then ok; else no "README.md shows the badge"; fi

if ((fail)); then echo "obtainium-link_test: FAILED"; exit 1; fi
echo "obtainium-link_test: ok ($n checks)"
