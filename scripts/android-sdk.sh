#!/usr/bin/env bash
# Install a project-local Android SDK into $ANDROID_HOME (= $ROOT/.tools/android-sdk, set by mise.toml).
# BRIEF §1.1 / §6: everything stays inside the repository. Idempotent: a second run only re-checks.
#
#  1. command-line tools: pinned zip from dl.google.com, SHA-256 from developer.android.com/studio
#     (repository2-3.xml only publishes SHA-1 for it; both are checked), unpacked into
#     $ANDROID_HOME/cmdline-tools/latest;
#  2. licences accepted non-interactively;
#  3. exactly the platform and build-tools that android/app/build.gradle.kts asks for, plus
#     platform-tools: AGP 9.5 warns on every build without it and, if allowed to download, installs it
#     itself. android/gradle.properties sets android.builder.sdkDownload=false, so AGP never fetches
#     anything on its own and this list is the whole SDK.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# Same environment as build-android.sh / test-android.sh: ANDROID_USER_HOME is the one that counts;
# the deprecated ANDROID_SDK_HOME (the parent of a .android folder) only conflicts with it.
unset ANDROID_SDK_HOME
# CI runners export ANDROID_SDK_ROOT for their own SDK; sdkmanager and AGP must see only ANDROID_HOME.
unset ANDROID_SDK_ROOT
mkdir -p "$ROOT/.tmp"   # mktemp and the install log below; a fresh clone has none

# Keep in sync with compileSdk / buildToolsVersion in android/app/build.gradle.kts.
PACKAGES=("platforms;android-37.2" "build-tools;37.0.0" "platform-tools")

CMDLINE_ZIP="commandlinetools-linux-15859902_latest.zip" # cmdline-tools 22.0
CMDLINE_URL="https://dl.google.com/android/repository/$CMDLINE_ZIP"
CMDLINE_SHA256="4e4c464f145a7512b57d088ac6c278c03c9eea610886b35a5e0804e74eedf583"
CMDLINE_SHA1="040d3996a65543d22ec4bf73e4c37aa37a8d4af4"

die() { echo "android-sdk: $*" >&2; exit 1; }
[[ -n "${ANDROID_HOME:-}" ]] || die "ANDROID_HOME is not set; run 'mise run android-sdk' from $ROOT"
[[ "$ANDROID_HOME" == "$ROOT"/* ]] || die "ANDROID_HOME=$ANDROID_HOME is outside $ROOT"
[[ -n "${ANDROID_USER_HOME:-}" && "$ANDROID_USER_HOME" == "$ROOT"/* ]] ||
  die "ANDROID_USER_HOME must point inside $ROOT (sdkmanager would write ~/.android)"
command -v java >/dev/null || die "java not on PATH; run through mise"
command -v unzip >/dev/null || die "unzip is required"

SDKMANAGER="$ANDROID_HOME/cmdline-tools/latest/bin/sdkmanager"
DL="$ROOT/.tools/downloads"

verify() { # file
  echo "$CMDLINE_SHA256  $1" | sha256sum -c --status && echo "$CMDLINE_SHA1  $1" | sha1sum -c --status
}

if [[ ! -x "$SDKMANAGER" ]]; then
  mkdir -p "$DL"
  zip="$DL/$CMDLINE_ZIP"
  if [[ ! -f "$zip" ]] || ! verify "$zip"; then
    echo "android-sdk: downloading $CMDLINE_URL"
    curl -fL --retry 3 --proto '=https' -o "$zip.part" "$CMDLINE_URL"
    mv -f "$zip.part" "$zip"
  fi
  verify "$zip" || { rm -f "$zip"; die "checksum mismatch for $CMDLINE_ZIP (expected sha256 $CMDLINE_SHA256)"; }
  echo "android-sdk: $CMDLINE_ZIP sha256 ok"
  staging="$(mktemp -d "$ROOT/.tmp/cmdline-tools.XXXXXX")"
  unzip -q "$zip" -d "$staging"
  [[ -x "$staging/cmdline-tools/bin/sdkmanager" ]] || die "unexpected zip layout"
  mkdir -p "$ANDROID_HOME/cmdline-tools"
  rm -rf "$ANDROID_HOME/cmdline-tools/latest"
  mv "$staging/cmdline-tools" "$ANDROID_HOME/cmdline-tools/latest"
  rmdir "$staging"
fi

# sdkmanager prints a deprecation notice pointing at the new "android sdk" CLI (cmdline-tools 22+);
# It still works and is the tool BRIEF §1.1 names; the notice is harmless.
sdk() { "$SDKMANAGER" --sdk_root="$ANDROID_HOME" "$@"; }

missing=()
for p in "${PACKAGES[@]}"; do
  [[ -f "$ANDROID_HOME/${p//;//}/source.properties" ]] || missing+=("$p")
done
if ((${#missing[@]})); then
  # Licences: accept every one the packages may need (writes $ANDROID_HOME/licenses/*).
  (yes 2>/dev/null || :) | sdk --licenses >/dev/null
  echo "android-sdk: installing ${missing[*]}"
  log="$ROOT/.tmp/sdkmanager-install.log"
  # sdkmanager redraws a progress bar thousands of times; keep it in a log, show it on failure.
  if ! (yes 2>/dev/null || :) | sdk --install "${missing[@]}" >"$log" 2>&1; then
    tr '\r' '\n' <"$log" | tail -20 >&2
    die "sdkmanager --install failed (full log: $log)"
  fi
fi
for p in "${PACKAGES[@]}"; do
  [[ -f "$ANDROID_HOME/${p//;//}/source.properties" ]] || die "$p did not install"
done
echo "android-sdk: ready in $ANDROID_HOME (${PACKAGES[*]})"
