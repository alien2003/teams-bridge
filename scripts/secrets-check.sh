#!/usr/bin/env bash
# Grep every tracked or would-be-tracked file for private keys and tokens (BRIEF §9).
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/secrets-lib.sh
files=()
while IFS= read -r -d '' f; do [[ -f "$f" ]] && files+=( "$f" ); done < <(git ls-files -co --exclude-standard -z)
hits=0
for p in "${SECRET_PATTERNS[@]}"; do
  set +e
  out="$(command grep -EnI -- "$p" "${files[@]}")"; rc=$?
  set -e
  (( rc > 1 )) && { echo "secrets-check: grep failed (rc=$rc) for pattern $p"; exit 2; }
  out="$(printf '%s\n' "$out" | command grep -vF "$SECRET_ALLOW_MARKER" || true)"
  if [[ -n "$out" ]]; then echo "$out"; hits=1; fi
done
# A local, git-ignored stop list of strings that must never enter the repository (real tenant,
# team, channel and conversation ids and names). One string per line; never committed or printed.
# It lives in the MAIN checkout's .local/ (found from any worktree); a missing list is a failure
# unless TBD_NO_DENYLIST=1 says so explicitly (CI, fresh clones). It is matched against the
# content of every file, binary ones too (-a: a PNG text chunk counts), and against every path; a
# path that holds a value is counted, not printed.
denylist="$(denylist_path)"
if [[ -s "$denylist" ]]; then
  pat="$(mktemp "$(scratch_dir)/denylist.XXXXXX")"
  trap 'rm -f "$pat"' EXIT
  denylist_patterns "$denylist" "$pat"
  set +e
  hit="$(command grep -laiF -f "$pat" -- "${files[@]}")"; rc=$?
  set -e
  (( rc > 1 )) && { echo "secrets-check: grep failed on the stop list"; exit 2; }
  if [[ -n "$hit" ]]; then
    echo "secrets-check: stop-list value found in:"
    while IFS= read -r f; do
      if command grep -qiF -f "$pat" <<<"$f"; then echo "  (a file whose path holds a value too)"; else echo "  $f"; fi
    done <<<"$hit"
    hits=1
  fi
  n="$(printf '%s\0' "${files[@]}" | command grep -zciF -f "$pat" || true)"
  if (( n > 0 )); then echo "secrets-check: stop-list value in $n file path(s) (not printed)"; hits=1; fi
  echo "secrets-check: stop list checked ($(command grep -c . "$denylist") entries)"
elif [[ "${TBD_NO_DENYLIST:-}" == 1 ]]; then
  echo "secrets-check: no stop list (TBD_NO_DENYLIST=1)"
else
  echo "secrets-check: FAIL — stop list $denylist missing or empty (set TBD_NO_DENYLIST=1 to run without it)"
  exit 1
fi
# Files that must never be committed, whatever their content.
for f in "${files[@]}"; do
  case "$f" in
    android/app/google-services.json|*.sa.json|.env.local|*.keystore|*.jks|*.p12|*.pfx|*.pem|*.key|config.toml|*/config.toml|OUTSIDE.local.md|.local/*) echo "forbidden file: $f"; hits=1 ;;
  esac
done
if (( hits )); then echo "secrets-check: FAIL"; exit 1; fi
echo "secrets-check: clean (${#files[@]} files)"
