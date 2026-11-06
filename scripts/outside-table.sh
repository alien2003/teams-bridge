#!/usr/bin/env bash
# Print the OUTSIDE.md table as TSV: path<TAB>kind<TAB>why<TAB>removal<TAB>action (backticks stripped,
# Markdown-escaped pipes "\|" restored to "|").
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
awk '/<!-- BEGIN outside-table -->/{on=1; next} /<!-- END outside-table -->/{on=0} on && /^\|/ {print}' "$ROOT/OUTSIDE.md" |
  tail -n +3 |
  awk 'BEGIN{OFS="\t"} {
    gsub(/\\\|/, "\001")
    n = split($0, c, "|")
    for (i = 2; i <= 6; i++) { g = c[i]; gsub(/^[ \t]+|[ \t]+$/, "", g); gsub(/`/, "", g); gsub(/\001/, "|", g); f[i] = g }
    print f[2], f[3], f[4], f[5], f[6]
  }'
