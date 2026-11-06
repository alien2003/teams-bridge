#!/usr/bin/env bash
# BRIEF §1.2: list everything under $HOME, /tmp and /var/tmp that is newer than .tmp/stamp,
# excluding $ROOT, mise's own dirs, the agent's own state and shell history. Non-empty → fail.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
STAMP="$ROOT/.tmp/stamp"
[[ -f "$STAMP" ]] || { echo "check-outside: $STAMP missing" >&2; exit 2; }
H="${REAL_HOME:-$(getent passwd "$(id -un)" | cut -d: -f6)}"
U="$(id -u)"

# Paths (prefix match) that are allowed to change. Every entry is explained in OUTSIDE.md / DECISIONS.md.
excl=(
  "$ROOT"
  "$H/.local/share/mise" "$H/.cache/mise" "$H/.config/mise" "$H/.local/state/mise"   # mise toolbox + its trust/state files
  "$H/.claude" "$H/.claude.json" "$H/.claude.json.backup" "/tmp/claude-" "/tmp/cc-daemon-$U"  # the agent's own state (/tmp/claude-<uid>, /tmp/claude-<id>-cwd)
  "$H/.zsh_history" "$H/.bash_history" "$H/.histfile" "$H/.zcompdump"                 # shell history / completion dump
)
args=()
for e in "${excl[@]}"; do args+=( -path "$e" -prune -o -path "$e*" -prune -o ); done

found="$(
  for base in "$H" /tmp /var/tmp; do
    find "$base" -mindepth 1 -xdev "${args[@]}" \( -newer "$STAMP" -o -cnewer "$STAMP" \) -print 2>/dev/null || true
  done | sort -u
)"
# Rows acknowledged in OUTSIDE.md (Linux paths only) are globs that are allowed to change.
acked=()
while IFS=$'\t' read -r path _kind _why _removal _action; do
  case "$path" in
    "~/"*) acked+=( "$H/${path#\~/}" ) ;;
    /*)    acked+=( "$path" ) ;;
  esac
done < <("$ROOT/scripts/outside-table.sh")

# Drop acknowledged paths (and anything below them), and directories whose only change is that an
# excluded or acknowledged child was touched (a directory that is an ancestor of such a path).
if [[ -n "$found" ]]; then
  filtered=""
  while IFS= read -r p; do
    keep=1
    for g in "${acked[@]}"; do
      # shellcheck disable=SC2053
      [[ "$p" == $g || "$p" == $g/* ]] && { keep=0; break; }
    done
    if [[ $keep == 1 && -d "$p" && ! -L "$p" ]]; then
      for e in "${excl[@]}" "${acked[@]}"; do [[ "$e" == "$p/"* ]] && { keep=0; break; }; done
    fi
    [[ $keep == 1 ]] && filtered+="$p"$'\n'
  done <<<"$found"
  found="${filtered%$'\n'}"
fi
if [[ -n "$found" ]]; then
  echo "check-outside: FAIL — changed outside \$ROOT since $(date -r "$STAMP" -Iseconds):"
  printf '%s\n' "$found" | sed 's/^/  /'
  exit 1
fi
echo "check-outside: clean (nothing outside \$ROOT changed since $(date -r "$STAMP" -Iseconds))"
