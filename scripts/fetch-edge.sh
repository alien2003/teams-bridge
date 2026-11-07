#!/usr/bin/env bash
# BRIEF §1.4: Microsoft Edge for WSL without apt: download the .deb into .tools/ and dpkg -x it.
# If ldd reports missing shared libraries, the script lists them and stops (none are missing here).
# The wrapper .tools/edge/tbd-edge keeps Edge's $HOME-relative files (~/.pki, …) inside the repo,
# keeps it away from dconf and the accessibility bus, and disables WSL's crash-dump capture (which
# would write core dumps into the Windows %TEMP%).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
T="$ROOT/.tools"
REPO=https://packages.microsoft.com/repos/edge
mkdir -p "$T"
write_wrapper() {
cat > "$T/edge/tbd-edge" <<'WRAP'
#!/bin/sh
# Dev wrapper around the unpacked Edge: keep everything Edge derives from $HOME inside the repo.
here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/../.." && pwd)"
export HOME="$root/.xdg/home"
export XDG_CONFIG_HOME="${XDG_CONFIG_HOME:-$root/.xdg/config}"
export XDG_CACHE_HOME="${XDG_CACHE_HOME:-$root/.xdg/cache}"
export XDG_DATA_HOME="${XDG_DATA_HOME:-$root/.xdg/data}"
export TMPDIR="${TMPDIR:-$root/.tmp}"
# No dconf database in $XDG_RUNTIME_DIR, no D-Bus activation of the accessibility bus.
export GSETTINGS_BACKEND=memory NO_AT_BRIDGE=1
mkdir -p "$HOME" "$TMPDIR"
if [ -d "$root/.tools/edge-libs" ]; then
  export LD_LIBRARY_PATH="$root/.tools/edge-libs/usr/lib/x86_64-linux-gnu:$root/.tools/edge-libs/lib/x86_64-linux-gnu${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
fi
# RLIMIT_CORE of exactly 1 byte makes the kernel skip the core_pattern pipe: WSL's crash handler
# would otherwise copy each crashing Edge process (~30 MB) into the Windows %TEMP%\wsl-crashes.
# (A shell's `ulimit -c 1` means 1 block, which does not count; prlimit sets bytes.)
if command -v prlimit >/dev/null 2>&1; then
  exec prlimit --core=1 "$here/opt/microsoft/msedge/msedge" "$@"
fi
exec "$here/opt/microsoft/msedge/msedge" "$@"
WRAP
chmod +x "$T/edge/tbd-edge"
}
if [[ -x "$T/edge/opt/microsoft/msedge/msedge" && "${1:-}" != "--force" ]]; then
  write_wrapper   # always current, even for an Edge unpacked by an older version of this script
  "$T/edge/tbd-edge" --version; exit 0
fi
line="$(curl -fsSL --proto '=https' "$REPO/dists/stable/main/binary-amd64/Packages" |
  awk 'function flush(){ if (p && v && f && h) print v, f, h; p=0; v=f=h="" }
       /^$/{flush(); next} /^Package: /{p=($2=="microsoft-edge-stable")}
       /^Version:/{v=$2} /^Filename:/{f=$2} /^SHA256:/{h=$2} END{flush()}' |
  sort -V | tail -1)"
read -r ver file sha <<<"$line"
deb="$T/$(basename "$file")"
if [[ ! -f "$deb" ]]; then
  echo "fetch-edge: downloading Edge $ver"
  curl -fsSL --proto '=https' --retry 3 -o "$deb.part" "$REPO/$file"
  mv "$deb.part" "$deb"
fi
echo "$sha  $deb" | sha256sum -c -
rm -rf "$T/edge"; mkdir -p "$T/edge"
dpkg -x "$deb" "$T/edge"
bin="$T/edge/opt/microsoft/msedge/msedge"
[[ -x "$bin" ]] || { echo "fetch-edge: $bin missing after extraction" >&2; exit 1; }

# Shared libraries the system does not have → apt-get download (as user) + dpkg -x.
# libqt5_shim.so / libqt6_shim.so are optional desktop-integration shims, loaded only for a Qt UI.
sos=( "$bin" ); for so in "$T/edge/opt/microsoft/msedge/"*.so; do [[ "$so" == *qt*_shim.so ]] || sos+=( "$so" ); done
missing="$(ldd "${sos[@]}" 2>/dev/null | awk '/not found/{print $1}' | sort -u)"
if [[ -n "$missing" ]]; then
  echo "fetch-edge: missing libraries: ${missing//$'\n'/ }"
  echo "fetch-edge: install the packages that provide them (record them in OUTSIDE.md), then rerun"; exit 1
fi
write_wrapper
"$T/edge/tbd-edge" --version
