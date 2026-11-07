#!/usr/bin/env bash
# Print the OUTSIDE.md table as TSV: path<TAB>kind<TAB>why<TAB>removal<TAB>action (backticks stripped,
# Markdown-escaped pipes "\|" restored to "|"). The git-ignored OUTSIDE.local.md (host-specific noise
# of this machine, same table between the same markers) is read after it when it exists.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
files=( "$ROOT/OUTSIDE.md" )
[[ -f "$ROOT/OUTSIDE.local.md" ]] && files+=( "$ROOT/OUTSIDE.local.md" )
# Per file: the table rows between the markers, minus the header and the |---| separator.
awk 'FNR==1 {on=0; n=0}
  /<!-- BEGIN outside-table -->/ {on=1; next}
  /<!-- END outside-table -->/ {on=0}
  on && /^\|/ { if (++n > 2) print }' "${files[@]}" |
  awk 'BEGIN{OFS="\t"} {
    gsub(/\\\|/, "\001")
    n = split($0, c, "|")
    for (i = 2; i <= 6; i++) { g = c[i]; gsub(/^[ \t]+|[ \t]+$/, "", g); gsub(/`/, "", g); gsub(/\001/, "|", g); f[i] = g }
    print f[2], f[3], f[4], f[5], f[6]
  }'
