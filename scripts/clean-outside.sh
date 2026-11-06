#!/usr/bin/env bash
# BRIEF §1.3: remove everything listed in OUTSIDE.md that we created ourselves; print the rest.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ran=0 printed=0
while IFS=$'\t' read -r path kind _why removal action; do
  case "$action" in
    run)
      echo "clean-outside: $path  →  $removal"
      bash -c "$removal" < /dev/null || echo "clean-outside: WARNING: removal failed for $path" >&2
      ran=$((ran+1)) ;;
    print)
      echo "clean-outside: run this yourself ($kind): $removal"
      printed=$((printed+1)) ;;
  esac
done < <("$ROOT/scripts/outside-table.sh")
echo "clean-outside: executed $ran removal(s); $printed command(s) left for you."
echo "clean-outside: mise store entries (keep) can be removed with:"
"$ROOT/scripts/outside-table.sh" | awk -F'\t' '$5=="keep"{print "  " $4}'
