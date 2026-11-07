#!/usr/bin/env bash
# Build the Android APK (BRIEF §6, §9). Usage: build-android.sh [debug|release]
#   debug    dist/teams-bridge-debug.apk, signed with the project-local debug key
#   release  dist/teams-bridge.apk (+ dist/teams-bridge-mapping.txt, R8's mapping) when a release key
#            is given (TBD_RELEASE_KEYSTORE, TBD_RELEASE_KEYSTORE_PASSWORD, TBD_RELEASE_KEY_ALIAS, see
#            android/app/build.gradle.kts), else dist/teams-bridge-unsigned.apk. A signed release must
#            be signed by exactly one certificate whose SHA-256 equals TBD_RELEASE_CERT_SHA256 when
#            that is set (scripts/build-release.sh sets it), and must not be debuggable.
# The version comes from TBD_VERSION_CODE / TBD_VERSION_NAME (scripts/version.sh) when set.
# Ensures the project-local SDK, debug keystore and (if missing) the placeholder google-services.json
# first. Fails if the APK is 6 MB (6,000,000 bytes) or larger. Run through mise (mise.toml env).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LIMIT=6000000
BUILD_TOOLS="$ANDROID_HOME/build-tools/37.0.0"
die() { echo "build-android: $*" >&2; exit 1; }
command -v gradle >/dev/null || die "gradle not on PATH; run through mise from $ROOT"
variant="${1:-debug}"
case "$variant" in debug|release) ;; *) die "usage: build-android.sh [debug|release]" ;; esac

# AGP 9.5 fails the build when ANDROID_SDK_HOME and ANDROID_USER_HOME are both set to the same path
# (the deprecated ANDROID_SDK_HOME names the *parent* of the .android folder). ANDROID_USER_HOME is
# the one that counts; drop the other for the build.
unset ANDROID_SDK_HOME
# CI runners export ANDROID_SDK_ROOT for their own SDK; AGP fails when it disagrees with ANDROID_HOME.
unset ANDROID_SDK_ROOT
mkdir -p "$ROOT/.tmp"   # TMPDIR and java.io.tmpdir (mise.toml); a fresh clone has none

"$ROOT/scripts/android-sdk.sh"
"$ROOT/scripts/debug-keystore.sh"
"$ROOT/scripts/google-services-placeholder.sh"

args=(-p "$ROOT/android" --console=plain)
[[ -n "${TBD_VERSION_CODE:-}" ]] && args+=("-Ptbd.versionCode=$TBD_VERSION_CODE")
[[ -n "${TBD_VERSION_NAME:-}" ]] && args+=("-Ptbd.versionName=$TBD_VERSION_NAME")
outputs="$ROOT/android/app/build/outputs"
signed=true
if [[ "$variant" == debug ]]; then
  apk="$outputs/apk/debug/app-debug.apk"
  out="$ROOT/dist/teams-bridge-debug.apk"
  task=assembleDebug
else
  if [[ -n "${TBD_RELEASE_KEYSTORE:-}" ]]; then
    # Gradle would resolve a relative path against android/app/.
    TBD_RELEASE_KEYSTORE="$(realpath -e -- "$TBD_RELEASE_KEYSTORE")" || die "TBD_RELEASE_KEYSTORE: no such file"
    export TBD_RELEASE_KEYSTORE
    apk="$outputs/apk/release/app-release.apk"
    out="$ROOT/dist/teams-bridge.apk"
    # The store password is in this environment: no configuration cache entry (it would hold the
    # signing config) and no daemon that outlives the build.
    args+=(--no-configuration-cache --no-daemon)
  else
    signed=false
    apk="$outputs/apk/release/app-release-unsigned.apk"
    out="$ROOT/dist/teams-bridge-unsigned.apk"
    echo "build-android: no TBD_RELEASE_KEYSTORE: building an UNSIGNED release APK (it cannot be installed)" >&2
  fi
  task=assembleRelease
fi

# AGP packages APKs incrementally and never compacts them: entries that disappear between builds
# leave dead space in the zip (one switch to R8 left a 19.6 MB file holding 3 MB of entries). Without
# an old APK, the package task writes a fresh, compact one; it costs a second or two.
rm -f "$outputs/apk/$variant/"*.apk
gradle "${args[@]}" "$task"
[[ -f "$apk" ]] || die "no APK at $apk"

if [[ "$signed" == true ]]; then
  certs="$("$BUILD_TOOLS/apksigner" verify -v --print-certs "$apk" 2>/dev/null)" || die "APK signature does not verify"
  if [[ "$variant" == release ]]; then
    # One "<scheme> Signer: certificate SHA-256 digest:" line per scheme (v2, v3): one signer, one cert.
    command grep -qx 'Number of signers: 1' <<<"$certs" || die "expected exactly one signer"
    mapfile -t digests < <(sed -nE 's/^([A-Za-z0-9.]+ )?Signer( #[0-9]+)?: certificate SHA-256 digest: //p' <<<"$certs" | sort -u)
    ((${#digests[@]} == 1)) || die "expected exactly one signing certificate, found ${#digests[@]}"
    # minSdk 28 covers v3, so apksigner reports only the v3 scheme as verified (v2 rides along).
    command grep -qx 'Verified using v3 scheme (APK Signature Scheme v3): true' <<<"$certs" || die "no v3 signature"
    if [[ -n "${TBD_RELEASE_CERT_SHA256:-}" ]]; then
      want="$(tr -d ': \n' <<<"$TBD_RELEASE_CERT_SHA256" | tr 'A-F' 'a-f')"
      [[ "${digests[0]}" == "$want" ]] ||
        die "the APK is not signed by the release key (signer SHA-256 ${digests[0]}, expected $want)"
    fi
    echo "build-android: signer certificate SHA-256 ${digests[0]}"
  fi
fi

badging="$("$BUILD_TOOLS/aapt2" dump badging "$apk")"
if [[ "$variant" == release ]] && command grep -q '^application-debuggable' <<<"$badging"; then
  die "the release APK is debuggable"
fi
pkg="$(sed -n '1s/^package: //p' <<<"$badging")"
if [[ -n "${TBD_VERSION_CODE:-}" && "$pkg" != *"versionCode='$TBD_VERSION_CODE'"* ]]; then
  die "versionCode is not $TBD_VERSION_CODE: $pkg"
fi
if [[ -n "${TBD_VERSION_NAME:-}" && "$pkg" != *"versionName='$TBD_VERSION_NAME'"* ]]; then
  die "versionName is not $TBD_VERSION_NAME: $pkg"
fi

mkdir -p "$ROOT/dist"
cp -f "$apk" "$out"
if [[ "$variant" == release ]]; then
  mapping="$outputs/mapping/release/mapping.txt"
  [[ -s "$mapping" ]] || die "no R8 mapping at $mapping"
  cp -f "$mapping" "$ROOT/dist/teams-bridge-mapping.txt"
fi
size="$(stat -c %s "$out")"
awk -v f="$out" -v s="$size" -v l="$LIMIT" -v p="$pkg" 'BEGIN { printf "build-android: %s  %d bytes (%.2f MB, limit %.2f MB)  %s\n", f, s, s / 1e6, l / 1e6, p }'
((size < LIMIT)) || die "$variant APK is too large: $size bytes >= $LIMIT"
