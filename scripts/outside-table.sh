#!/usr/bin/env bash
# Print the OUTSIDE.md table as TSV: path<TAB>kind<TAB>why<TAB>removal<TAB>action (backticks stripped).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
awk '/<!-- BEGIN outside-table -->/{on=1; next} /<!-- END outside-table -->/{on=0} on && /^\|/ {print}' "$ROOT/OUTSIDE.md" |
  tail -n +3 |
  awk -F'|' 'BEGIN{OFS="\t"} {for(i=2;i<=6;i++){g=$i; gsub(/^[ \t]+|[ \t]+$/,"",g); gsub(/`/,"",g); f[i]=g} print f[2],f[3],f[4],f[5],f[6]}'
