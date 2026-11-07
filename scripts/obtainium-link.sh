#!/usr/bin/env bash
# The Obtainium "add this app" link of the README badge (D24). Usage: obtainium-link.sh [--json]
# Prints the redirect URL https://apps.obtainium.imranr.dev/redirect?r=obtainium://app/<config>,
# or with --json the app config itself: Obtainium's export format, whose additionalSettings is a
# JSON document inside a string. The settings pin what Obtainium installs to the release assets:
#   apkFilterRegEx            only the asset teams-bridge.apk (D23: one unversioned APK per release);
#   allowedSigningCertHashes  only an APK signed by android/release-cert.sha256 (Obtainium >= 1.7.0
#                             refuses any other signer, with no override; uppercase hex pairs).
# scripts/obtainium-link_test.sh decodes the link again and checks that README.md carries it.
# Needs only bash.
set -euo pipefail
cd "$(dirname "$0")/.."
die() { echo "obtainium-link: $*" >&2; exit 1; }

repo="alien2003/teams-bridge"
app_id="io.github.alien2003.teamsbridge" # must equal the APK's applicationId
apk_filter='^teams-bridge[.]apk$'

cert="$(command grep -v '^#' android/release-cert.sha256 | tr -d ' \n')"
[[ "$cert" =~ ^([0-9A-F]{2}:){31}[0-9A-F]{2}$ ]] || die "android/release-cert.sha256: not an uppercase SHA-256 with colons"
grep -q "applicationId = \"$app_id\"" android/app/build.gradle.kts || die "applicationId is not $app_id"

# The values above contain no quote, backslash or non-ASCII character, so no JSON escaping is needed
# beyond quoting the inner document once.
settings="{\"apkFilterRegEx\":\"$apk_filter\",\"allowedSigningCertHashes\":\"$cert\"}"
config="{\"id\":\"$app_id\",\"url\":\"https://github.com/$repo\",\"author\":\"alien2003\",\"name\":\"Teams Bridge\",\"preferredApkIndex\":0,\"additionalSettings\":\"${settings//\"/\\\"}\"}"

# encodeURIComponent: everything but A-Z a-z 0-9 - _ . ! ~ * ' ( ) becomes %XX.
encode() {
  local LC_ALL=C s="$1" out="" c i
  for ((i = 0; i < ${#s}; i++)); do
    c="${s:i:1}"
    case "$c" in
      [A-Za-z0-9_.!~*\'\(\)-]) out+="$c" ;;
      *) printf -v c '%%%02X' "'$c"; out+="$c" ;;
    esac
  done
  printf '%s' "$out"
}

case "${1:-}" in
  --json) printf '%s\n' "$config" ;;
  "") printf 'https://apps.obtainium.imranr.dev/redirect?r=obtainium://app/%s\n' "$(encode "$config")" ;;
  *) die "usage: obtainium-link.sh [--json]" ;;
esac
