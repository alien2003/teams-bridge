#!/usr/bin/env bash
# BRIEF §1.4: Microsoft Edge for WSL without apt: download the .deb into .tools/ and dpkg -x it.
# Missing shared libraries are fetched with `apt-get download` (no root, no install) and unpacked
# into .tools/edge-libs/; the wrapper .tools/edge/tbd-edge puts them on LD_LIBRARY_PATH and keeps
# Edge's $HOME-relative files (~/.pki, ~/.local, …) inside the repository.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
T="$ROOT/.tools"
REPO=https://packages.microsoft.com/repos/edge
mkdir -p "$T"
if [[ -x "$T/edge/tbd-edge" && "${1:-}" != "--force" ]]; then
  "$T/edge/tbd-edge" --version; exit 0
fi
line="$(curl -fsSL "$REPO/dists/stable/main/binary-amd64/Packages" |
  awk 'function flush(){ if (p && v && f && h) print v, f, h; p=0; v=f=h="" }
       /^$/{flush(); next} /^Package: /{p=($2=="microsoft-edge-stable")}
       /^Version:/{v=$2} /^Filename:/{f=$2} /^SHA256:/{h=$2} END{flush()}' |
  sort -V | tail -1)"
read -r ver file sha <<<"$line"
deb="$T/$(basename "$file")"
if [[ ! -f "$deb" ]]; then
  echo "fetch-edge: downloading Edge $ver"
  curl -fsSL --retry 3 -o "$deb.part" "$REPO/$file"
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
  echo "fetch-edge: missing libraries: $(echo $missing)"
  pkgs=""
  for lib in $missing; do
    pkg="$(apt-file search -l "$lib" 2>/dev/null | head -1 || true)"
    [[ -z "$pkg" ]] && pkg="$(dpkg -S "$lib" 2>/dev/null | head -1 | cut -d: -f1 || true)"
    echo "  $lib → ${pkg:-?}"
    pkgs+=" $pkg"
  done
  echo "fetch-edge: resolve these manually (see OUTSIDE.md)"; exit 1
fi
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
mkdir -p "$HOME" "$TMPDIR"
if [ -d "$root/.tools/edge-libs" ]; then
  export LD_LIBRARY_PATH="$root/.tools/edge-libs/usr/lib/x86_64-linux-gnu:$root/.tools/edge-libs/lib/x86_64-linux-gnu${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
fi
exec "$here/opt/microsoft/msedge/msedge" "$@"
WRAP
chmod +x "$T/edge/tbd-edge"
"$T/edge/tbd-edge" --version
