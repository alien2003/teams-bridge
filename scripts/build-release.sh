#!/usr/bin/env bash
# Release assets into dist/release/ (D23), the same locally and in CI (.github/workflows/release.yml):
#   teams-bridge.apk             the release APK (R8: shrunk, optimised, obfuscated), signed by the
#                                release key whose certificate SHA-256 is android/release-cert.sha256
#   teams-bridge-mapping.txt.gz  R8's mapping, to de-obfuscate a stack trace
#   tbd-linux-amd64              static (CGO_ENABLED=0)
#   tbd-windows-amd64.exe
#   tbd-darwin-arm64             macOS, Apple Silicon (D26): ad-hoc signed by the Go linker, not
#   tbd-darwin-amd64             notarized; macOS, Intel. Both cross-built here, checked on macOS
#                                runners by the release workflow's macos job
#   SHA256SUMS                   sha256sum of the six files above
# Run through mise: mise run release-build.
#
# Inputs, all from the environment:
#   TBD_RELEASE_TAG                 the tag to build (CI: github.ref_name); default: the release tag at
#                                   HEAD, else a development version (scripts/version.sh). A given tag
#                                   must point at HEAD.
#   TBD_RELEASE_KEYSTORE            path to the PKCS12 release keystore (relative: to the repository
#                                   root), or
#   TBD_RELEASE_KEYSTORE_B64        the keystore in base64 (the CI secret), decoded into a private
#                                   temporary file under .tmp/ that is removed on exit
#   TBD_RELEASE_KEYSTORE_PASSWORD   store (= key) password, or TBD_RELEASE_KEYSTORE_PASSWORD_FILE
#   TBD_RELEASE_KEY_ALIAS           default teams-bridge
#   TBD_GOOGLE_SERVICES_JSON_B64    the real google-services.json in base64 (CI); locally the real
#                                   android/app/google-services.json. The placeholder is refused.
#   TBD_RELEASE_CERT_FILE           expected certificate fingerprint, default android/release-cert.sha256
#   TBD_RELEASE_ALLOW_DIRTY=1       build from a dirty tree (local trials only; the version says .dirty)
# With no key in the environment, the maintainer's local copy .local/release/teams-bridge-release.p12
# and .local/release/keystore.pass are used if they exist. Nothing here prints a password.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
mkdir -p .tmp   # TMPDIR (mise.toml); a fresh clone has none
die() { echo "build-release: $*" >&2; exit 1; }
for t in go gradle java git sha256sum gzip; do
  command -v "$t" >/dev/null || die "$t not on PATH; run through mise (mise run release-build)"
done

# --- tree and version -------------------------------------------------------------------------
if [[ -n "$(git status --porcelain --untracked-files=normal)" ]]; then
  [[ "${TBD_RELEASE_ALLOW_DIRTY:-}" == 1 ]] || die "the working tree is not clean (git status); commit first"
  echo "build-release: WARNING: dirty tree (TBD_RELEASE_ALLOW_DIRTY=1), not a publishable build" >&2
fi
tag="${TBD_RELEASE_TAG:-}"
if [[ -n "$tag" ]]; then
  at="$(git rev-parse -q --verify "refs/tags/$tag^{commit}" 2>/dev/null)" || die "no tag $tag"
  [[ "$at" == "$(git rev-parse HEAD)" ]] || die "tag $tag does not point at HEAD"
fi
vars="$(scripts/version.sh "$tag")" || exit 1
TBD_VERSION="$(sed -n 's/^TBD_VERSION=//p' <<<"$vars")"
TBD_VERSION_NAME="$(sed -n 's/^TBD_VERSION_NAME=//p' <<<"$vars")"
TBD_VERSION_CODE="$(sed -n 's/^TBD_VERSION_CODE=//p' <<<"$vars")"
if [[ -n "$(git status --porcelain --untracked-files=normal)" && "$TBD_VERSION" != *.dirty ]]; then
  TBD_VERSION="$TBD_VERSION.dirty" TBD_VERSION_NAME="$TBD_VERSION_NAME.dirty"
fi
export TBD_VERSION TBD_VERSION_NAME TBD_VERSION_CODE
echo "build-release: version $TBD_VERSION (versionCode $TBD_VERSION_CODE)"

# --- private temporary files (the decoded keystore) ---------------------------------------------
work="$(umask 077 && mktemp -d "$ROOT/.tmp/release.XXXXXX")"
trap 'rm -rf "$work"' EXIT

# --- google-services.json ---------------------------------------------------------------------
gs="$ROOT/android/app/google-services.json"
if [[ -n "${TBD_GOOGLE_SERVICES_JSON_B64:-}" ]]; then
  base64 -d <<<"$TBD_GOOGLE_SERVICES_JSON_B64" >"$work/google-services.json" ||
    die "TBD_GOOGLE_SERVICES_JSON_B64 is not valid base64"
  if [[ -e "$gs" ]] && ! cmp -s "$gs" "$work/google-services.json"; then
    die "$gs exists and differs from TBD_GOOGLE_SERVICES_JSON_B64; remove one of them"
  fi
  cp "$work/google-services.json" "$gs"
fi
[[ -f "$gs" ]] || die "no android/app/google-services.json (and no TBD_GOOGLE_SERVICES_JSON_B64): a release needs the real one"
! command grep -q '"tbd-placeholder"' "$gs" || die "android/app/google-services.json is the placeholder: a release needs the real one"

# --- release key ------------------------------------------------------------------------------
local_ks="$ROOT/.local/release/teams-bridge-release.p12" local_pass="$ROOT/.local/release/keystore.pass"
if [[ -n "${TBD_RELEASE_KEYSTORE_B64:-}" ]]; then
  [[ -z "${TBD_RELEASE_KEYSTORE:-}" ]] || die "set TBD_RELEASE_KEYSTORE or TBD_RELEASE_KEYSTORE_B64, not both"
  (umask 077 && base64 -d <<<"$TBD_RELEASE_KEYSTORE_B64" >"$work/release.p12") ||
    die "TBD_RELEASE_KEYSTORE_B64 is not valid base64"
  TBD_RELEASE_KEYSTORE="$work/release.p12"
elif [[ -z "${TBD_RELEASE_KEYSTORE:-}" && -z "${TBD_RELEASE_KEYSTORE_PASSWORD:-}${TBD_RELEASE_KEYSTORE_PASSWORD_FILE:-}" &&
  -f "$local_ks" && -f "$local_pass" ]]; then
  TBD_RELEASE_KEYSTORE="$local_ks" TBD_RELEASE_KEYSTORE_PASSWORD_FILE="$local_pass"
  echo "build-release: using the local release key in .local/release/"
fi
[[ -n "${TBD_RELEASE_KEYSTORE:-}" ]] ||
  die "no release key: set TBD_RELEASE_KEYSTORE (or TBD_RELEASE_KEYSTORE_B64) and TBD_RELEASE_KEYSTORE_PASSWORD"
[[ -f "$TBD_RELEASE_KEYSTORE" ]] || die "TBD_RELEASE_KEYSTORE: no such file"
if [[ -z "${TBD_RELEASE_KEYSTORE_PASSWORD:-}" && -n "${TBD_RELEASE_KEYSTORE_PASSWORD_FILE:-}" ]]; then
  [[ -f "$TBD_RELEASE_KEYSTORE_PASSWORD_FILE" ]] || die "TBD_RELEASE_KEYSTORE_PASSWORD_FILE: no such file"
  TBD_RELEASE_KEYSTORE_PASSWORD="$(<"$TBD_RELEASE_KEYSTORE_PASSWORD_FILE")"
fi
[[ -n "${TBD_RELEASE_KEYSTORE_PASSWORD:-}" ]] || die "no TBD_RELEASE_KEYSTORE_PASSWORD (or _FILE)"
export TBD_RELEASE_KEYSTORE TBD_RELEASE_KEYSTORE_PASSWORD
[[ -z "${TBD_RELEASE_KEY_ALIAS:-}" ]] || export TBD_RELEASE_KEY_ALIAS
unset TBD_RELEASE_KEYSTORE_B64 TBD_GOOGLE_SERVICES_JSON_B64 TBD_RELEASE_KEYSTORE_PASSWORD_FILE

cert_file="${TBD_RELEASE_CERT_FILE:-$ROOT/android/release-cert.sha256}"
[[ -s "$cert_file" ]] || die "no expected certificate fingerprint in $cert_file"
TBD_RELEASE_CERT_SHA256="$(command grep -v '^#' "$cert_file" | tr -d ' \n')"
[[ "$TBD_RELEASE_CERT_SHA256" =~ ^([0-9A-Fa-f]{2}:?){31}[0-9A-Fa-f]{2}$ ]] || die "$cert_file holds no SHA-256 fingerprint"
export TBD_RELEASE_CERT_SHA256

# --- build ------------------------------------------------------------------------------------
scripts/build.sh go
scripts/build-android.sh release
# The Gradle build was the only consumer of the key.
unset TBD_RELEASE_KEYSTORE TBD_RELEASE_KEYSTORE_PASSWORD

out="$ROOT/dist/release"
rm -rf "$out"
mkdir -p "$out"
cp dist/teams-bridge.apk "$out/teams-bridge.apk"
gzip -9 -n -c dist/teams-bridge-mapping.txt >"$out/teams-bridge-mapping.txt.gz"
cp dist/tbd "$out/tbd-linux-amd64"
cp dist/tbd.exe "$out/tbd-windows-amd64.exe"
cp dist/tbd-darwin-arm64 "$out/tbd-darwin-arm64"
cp dist/tbd-darwin-amd64 "$out/tbd-darwin-amd64"

binaries=(tbd-linux-amd64:linux:amd64 tbd-windows-amd64.exe:windows:amd64 tbd-darwin-arm64:darwin:arm64
  tbd-darwin-amd64:darwin:amd64)
for entry in "${binaries[@]}"; do
  IFS=: read -r name goos goarch <<<"$entry"
  b="$out/$name"
  info="$(go version -m "$b")"
  command grep -qx $'\tbuild\tCGO_ENABLED=0' <<<"$info" || die "$b: not built with CGO_ENABLED=0"
  command grep -qx $'\tbuild\tGOOS='"$goos" <<<"$info" || die "$b: not $goos"
  command grep -qx $'\tbuild\tGOARCH='"$goarch" <<<"$info" || die "$b: not $goarch"
  # -trimpath leaves -ldflags out of the build info: look for the stamped string itself.
  command grep -qaF "$TBD_VERSION" "$b" || die "$b: version is not $TBD_VERSION"
  # The first 8 bytes of a Mach-O 64 file: magic feedfacf, then the CPU type (little endian).
  magic="$(od -An -tx1 -N8 "$b" | tr -d ' \n')"
  case "$goos/$goarch" in
    darwin/arm64) [[ "$magic" == cffaedfe0c000001 ]] || die "$b: not a Mach-O arm64 file" ;;
    darwin/amd64) [[ "$magic" == cffaedfe07000001 ]] || die "$b: not a Mach-O x86_64 file" ;;
  esac
done
"$out/tbd-linux-amd64" version | command grep -qF "tbd $TBD_VERSION " || die "tbd-linux-amd64 version is not $TBD_VERSION"

(
  cd "$out"
  sha256sum teams-bridge.apk teams-bridge-mapping.txt.gz tbd-linux-amd64 tbd-windows-amd64.exe \
    tbd-darwin-arm64 tbd-darwin-amd64 >SHA256SUMS
  sha256sum --quiet -c SHA256SUMS
)
echo "build-release: $out ($TBD_VERSION)"
(cd "$out" && stat -c '  %10s  %n' -- *)
