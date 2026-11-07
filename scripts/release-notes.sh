#!/usr/bin/env bash
# Release notes for TAG on stdout (D23), used by .github/workflows/release.yml. Usage: release-notes.sh TAG
# The text is the first of:
#   1. the section of CHANGELOG.md headed "## [X.Y.Z…]", "## vX.Y.Z…" or "## X.Y.Z…" (up to the next "## ");
#   2. the message of the annotated tag, when it says more than the tag name;
#   3. the feat / fix / perf commits since the previous release tag (Conventional Commits subjects;
#      for a final release, since the previous final release).
# followed by how to verify the assets. Needs only bash and git; reads only the repository.
set -euo pipefail
cd "$(dirname "$0")/.."
die() { echo "release-notes: $*" >&2; exit 1; }
tag="${1:-}"
[[ -n "$tag" ]] || die "usage: release-notes.sh TAG"
git rev-parse -q --verify "refs/tags/$tag" >/dev/null || die "no tag $tag"
name="${tag#v}"
repo="${GITHUB_REPOSITORY:-alien2003/teams-bridge}"

notes=""
if [[ -f CHANGELOG.md ]]; then
  notes="$(awk -v n="$name" '
    function hit(h) { sub(/^## +/, "", h); sub(/^\[/, "", h); sub(/^v/, "", h)
                      return index(h, n) == 1 && substr(h, length(n) + 1) !~ /^[0-9A-Za-z.-]/ }
    /^## / { if (on) exit; if (hit($0)) { on = 1; next } }
    on { print }' CHANGELOG.md | sed -e '/./,$!d')"
fi
if [[ -z "$notes" && "$(git cat-file -t "refs/tags/$tag")" == tag ]]; then
  msg="$(git for-each-ref --format='%(contents:subject)%0a%0a%(contents:body)' "refs/tags/$tag")"
  # A message that only repeats the tag ("v0.1.0", "Release v0.1.0") says nothing.
  if [[ -n "$(sed -E "s/^(release )?v?${name//./\\.}$//I" <<<"$msg" | tr -d '[:space:]')" ]]; then
    notes="$msg"
  fi
fi
if [[ -z "$notes" ]]; then
  # A final release lists everything since the previous final release, not since its own rc.
  skip=()
  [[ "$tag" == *-* ]] || skip=(--exclude 'v*-*')
  prev="$(git describe --tags --abbrev=0 --match 'v[0-9]*' "${skip[@]}" "$tag^" 2>/dev/null || true)"
  range="$tag"
  [[ -n "$prev" ]] && range="$prev..$tag"
  section() { # title regex
    local lines
    lines="$(git log --no-merges --format='%s' "$range" | command grep -E "^$2(\([^)]*\))?!?: " |
      sed -E "s/^$2(\(([^)]*)\))?!?: (.*)$/* \3 (\2)/; s/ \(\)$//" || true)"
    [[ -n "$lines" ]] && printf '### %s\n\n%s\n\n' "$1" "$lines"
    return 0
  }
  notes="$(section Features feat; section Fixes fix; section Performance perf)"
  [[ -n "$notes" ]] || notes="Maintenance release (no user-visible changes)."
  [[ -n "$prev" ]] && notes+=$'\n\n'"Full list: https://github.com/$repo/compare/$prev...$tag"
fi

cert="$(command grep -v '^#' android/release-cert.sha256 | tr -d ' \n')"
cat <<EOF
$notes

## Assets

| File | |
|---|---|
| \`teams-bridge.apk\` | Android app, signed by the release key |
| \`tbd-linux-amd64\` | daemon for Linux and WSL (static) |
| \`tbd-windows-amd64.exe\` | daemon for Windows |
| \`teams-bridge-mapping.txt.gz\` | R8 mapping, to read an obfuscated stack trace |
| \`SHA256SUMS\` | SHA-256 of the files above |

## Verify

\`\`\`sh
sha256sum -c SHA256SUMS --ignore-missing
gh attestation verify teams-bridge.apk -R $repo --signer-workflow $repo/.github/workflows/release.yml
apksigner verify --print-certs teams-bridge.apk   # certificate SHA-256 below
\`\`\`

Release certificate SHA-256: \`$cert\`
EOF
