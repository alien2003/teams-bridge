#!/usr/bin/env bash
# BRIEF §1.6/§8: reference code, shallow, read-only, gitignored.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
R="$ROOT/.cache/ref"; mkdir -p "$R"
for repo in ferdium/ferdium-recipes ferdium/ferdium-app; do
  d="$R/${repo#*/}"
  [[ -d "$d/.git" ]] || git -c core.hooksPath=/dev/null clone -q --depth 1 "https://github.com/$repo" "$d"
  chmod -R a-w "$d"
  echo "fetch-refs: $repo @ $(git -C "$d" rev-parse --short HEAD)"
done
