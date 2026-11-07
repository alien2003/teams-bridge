#!/usr/bin/env bash
# Tests for scripts/release-notes.sh in a throwaway repository under .tmp/ (mise run test-scripts).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
mkdir -p "$ROOT/.tmp"
dir="$(mktemp -d "$ROOT/.tmp/notes-test.XXXXXX")"
trap 'rm -rf "$dir"' EXIT
fail=0 n=0

mkdir -p "$dir/scripts" "$dir/android"
cp "$ROOT/scripts/release-notes.sh" "$dir/scripts/"
cp "$ROOT/android/release-cert.sha256" "$dir/android/"
g() { git -C "$dir" -c user.name=Test -c user.email=test@example.com -c commit.gpgsign=false -c tag.gpgsign=false "$@"; }
commit() { g commit -q --allow-empty -m "$1"; }
notes() { GITHUB_REPOSITORY=example/repo "$dir/scripts/release-notes.sh" "$1"; }
has() { # name text needle
  n=$((n + 1))
  [[ "$2" == *"$3"* ]] || { echo "FAIL $1: no '$3' in:"; echo "$2"; fail=1; }
}
hasnt() {
  n=$((n + 1))
  [[ "$2" != *"$3"* ]] || { echo "FAIL $1: unexpected '$3' in:"; echo "$2"; fail=1; }
}

g init -q -b main
g add -A
commit "feat(app): first feature"
commit "fix: an early bug"
commit "docs: a README change"
g tag v0.1.0 # lightweight: notes from the commits
out="$(notes v0.1.0)"
has "commits" "$out" "### Features"
has "commits" "$out" "* first feature (app)"
has "commits" "$out" "* an early bug"
hasnt "commits" "$out" "README change"
hasnt "commits/first" "$out" "Full list"
has "footer" "$out" "Release certificate SHA-256: \`46:FC:"
has "footer" "$out" "--signer-workflow example/repo/.github/workflows/release.yml"
has "assets" "$out" "| \`tbd-linux-amd64\` |"
has "assets" "$out" "| \`tbd-windows-amd64.exe\` |"
has "assets/darwin" "$out" "| \`tbd-darwin-arm64\` | daemon for macOS, Apple Silicon (unsigned, not notarized: see README) |"
has "assets/darwin" "$out" "| \`tbd-darwin-amd64\` | daemon for macOS, Intel (unsigned, not notarized: see README) |"
has "verify/macOS" "$out" "grep tbd-darwin-arm64 SHA256SUMS | shasum -a 256 -c -"

commit "feat!: breaking thing"
commit "chore: tidy"
g tag -a v0.2.0-rc.1 -m "v0.2.0-rc.1" # annotated, but says nothing: commits since v0.1.0
out="$(notes v0.2.0-rc.1)"
has "since prev" "$out" "* breaking thing"
hasnt "since prev" "$out" "first feature"
has "since prev" "$out" "compare/v0.1.0...v0.2.0-rc.1"

commit "chore: more tidying"
g tag -a v0.2.0 -m "Teams Bridge 0.2.0" -m "The first public build. Install the APK, then pair."
out="$(notes v0.2.0)"
has "tag message" "$out" "The first public build."
hasnt "tag message" "$out" "### Features"

commit "chore: only chores"
g tag v0.2.1
out="$(notes v0.2.1)"
has "nothing" "$out" "Maintenance release"

printf '# Changelog\n\n## [0.3.0] - 2026-12-01\n\n* From the changelog.\n\n## [0.3.0-rc.1]\n\n* Not this one.\n' >"$dir/CHANGELOG.md"
g add CHANGELOG.md
commit "docs: changelog"
g tag -a v0.3.0 -m "Release v0.3.0" -m "Tag text that the changelog overrides."
out="$(notes v0.3.0)"
has "changelog" "$out" "* From the changelog."
hasnt "changelog" "$out" "Not this one"
hasnt "changelog" "$out" "overrides"
commit "feat: in the rc"
g tag v0.4.0-rc.1
commit "fix: after the rc"
g tag v0.4.0
out="$(notes v0.4.0)"
has "final since final" "$out" "* in the rc"
has "final since final" "$out" "* after the rc"
has "final since final" "$out" "compare/v0.3.0...v0.4.0"

n=$((n + 1))
if notes v9.9.9 >/dev/null 2>&1; then echo "FAIL missing tag accepted"; fail=1; fi

((fail == 0)) || { echo "release-notes_test: FAILED"; exit 1; }
echo "release-notes_test: ok ($n checks)"
