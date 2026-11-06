#!/usr/bin/env bash
# Build the Android debug APK (BRIEF §6, §9) and copy it to dist/teams-bridge-debug.apk.
# Ensures the project-local SDK, debug keystore and (if missing) the placeholder google-services.json
# first. Fails if the APK is 6 MB (6,000,000 bytes) or larger. Run through mise (mise.toml env).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LIMIT=6000000
die() { echo "build-android: $*" >&2; exit 1; }
command -v gradle >/dev/null || die "gradle not on PATH; run through mise from $ROOT"

# AGP 9.5 fails the build when ANDROID_SDK_HOME and ANDROID_USER_HOME are both set to the same path
# (the deprecated ANDROID_SDK_HOME names the *parent* of the .android folder). ANDROID_USER_HOME is
# the one that counts; drop the other for the build.
unset ANDROID_SDK_HOME

"$ROOT/scripts/android-sdk.sh"
"$ROOT/scripts/debug-keystore.sh"
"$ROOT/scripts/google-services-placeholder.sh"

apk="$ROOT/android/app/build/outputs/apk/debug/app-debug.apk"
# AGP packages debug APKs incrementally and never compacts them: entries that disappear between builds
# leave dead space in the zip (one switch to R8 left a 19.6 MB file holding 3 MB of entries). Without
# an old APK, packageDebug writes a fresh, compact one; it costs a second or two.
rm -f "$apk"
gradle -p "$ROOT/android" --console=plain assembleDebug
[[ -f "$apk" ]] || die "no APK at $apk"

"$ANDROID_HOME/build-tools/37.0.0/apksigner" verify "$apk" || die "APK signature does not verify"

mkdir -p "$ROOT/dist"
out="$ROOT/dist/teams-bridge-debug.apk"
cp -f "$apk" "$out"
size="$(stat -c %s "$out")"
awk -v f="$out" -v s="$size" -v l="$LIMIT" 'BEGIN { printf "build-android: %s  %d bytes (%.2f MB, limit %.2f MB)\n", f, s, s / 1e6, l / 1e6 }'
((size < LIMIT)) || die "debug APK is too large: $size bytes >= $LIMIT"
