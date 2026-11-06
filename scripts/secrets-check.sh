#!/usr/bin/env bash
# Grep every tracked or would-be-tracked file for private keys and tokens (BRIEF §9).
set -euo pipefail
cd "$(dirname "$0")/.."
patterns=(
  '-----BEGIN ([A-Z]+ )?PRIVATE KEY-----'   # secrets-check: allow
  '"private_key"[[:space:]]*:[[:space:]]*"-----BEGIN'
  'AIza[0-9A-Za-z_-]{35}'          # Google API key  secrets-check: allow
  'ya29\.[0-9A-Za-z_-]{20,}'       # Google OAuth access token
  '1//[0-9A-Za-z_-]{30,}'          # Google OAuth refresh token
  'gh[pousr]_[0-9A-Za-z]{30,}'     # GitHub tokens
  'github_pat_[0-9A-Za-z_]{30,}'
  'AKIA[0-9A-Z]{16}'               # AWS access key id
  'xox[abprs]-[0-9A-Za-z-]{10,}'   # Slack
  'secret[[:space:]]*=[[:space:]]*"[A-Z2-7]{26}"'   # a tbd pairing secret in a config file  secrets-check: allow
)
files=()
while IFS= read -r -d '' f; do [[ -f "$f" ]] && files+=( "$f" ); done < <(git ls-files -co --exclude-standard -z)
hits=0
for p in "${patterns[@]}"; do
  if out="$(command grep -EnI -- "$p" "${files[@]}" 2>/dev/null | command grep -v 'secrets-check: allow')"; then
    echo "$out"; hits=1
  fi
done
# Files that must never be committed, whatever their content.
for f in "${files[@]}"; do
  case "$f" in
    android/app/google-services.json|*.sa.json|.env.local|*.keystore|*.jks) echo "forbidden file: $f"; hits=1 ;;
  esac
done
if (( hits )); then echo "secrets-check: FAIL"; exit 1; fi
echo "secrets-check: clean (${#files[@]} files)"
