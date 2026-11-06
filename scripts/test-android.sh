#!/usr/bin/env bash
# Android JVM unit tests (BRIEF §6, §7) against proto/vectors.json, with Kotlin compiler warnings as
# errors (-Ptbd.werror=true covers the main and the test sources). No emulator, no device.
# Fails on any compiler warning, test failure, or if no tests ran. Run through mise.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
die() { echo "test-android: $*" >&2; exit 1; }
command -v gradle >/dev/null || die "gradle not on PATH; run through mise from $ROOT"
[[ -f "$ROOT/proto/vectors.json" ]] || die "proto/vectors.json missing (mise run gen-vectors)"

# See build-android.sh: AGP 9.5 rejects ANDROID_SDK_HOME == ANDROID_USER_HOME.
unset ANDROID_SDK_HOME

"$ROOT/scripts/android-sdk.sh"
"$ROOT/scripts/debug-keystore.sh"
# The google-services plugin's resources are part of the R class the tests compile against.
"$ROOT/scripts/google-services-placeholder.sh"

results="$ROOT/android/app/build/test-results/testDebugUnitTest"
rm -rf "$results"
gradle -p "$ROOT/android" --console=plain testDebugUnitTest -Ptbd.werror=true

shopt -s nullglob
files=("$results"/*.xml)
((${#files[@]})) || die "no test results in $results"
read -r tests failures errors skipped < <(
  cat "${files[@]}" | command grep -oE '<testsuite [^>]*>' |
    sed -E 's/.* tests="([0-9]+)".* skipped="([0-9]+)".* failures="([0-9]+)".* errors="([0-9]+)".*/\1 \3 \4 \2/' |
    awk '{t+=$1; f+=$2; e+=$3; s+=$4} END {print t+0, f+0, e+0, s+0}'
)
echo "test-android: $tests tests, $failures failures, $errors errors, $skipped skipped (${#files[@]} classes)"
((tests > 0)) || die "no tests ran"
((failures == 0 && errors == 0)) || die "test failures"
