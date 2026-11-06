#!/usr/bin/env bash
# BRIEF §1.5: snapshot %USERPROFILE% (1 level), %LOCALAPPDATA% / %APPDATA% (3 levels), %TEMP% (1 level)
# and the scheduled-task list; diff against the baseline taken before the first host action.
# Differences acknowledged in OUTSIDE.md (rows whose path starts with %VAR%\ or "schtasks:") pass;
# anything else fails. Usage: check-outside-win.sh [--baseline]
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
source "$ROOT/scripts/win/lib.sh"
mkdir -p "$WIN_SB_UNIX/scripts" "$WIN_SB_UNIX/snapshots"
cp "$ROOT/scripts/win/snapshot.ps1" "$WIN_SB_UNIX/scripts/snapshot.ps1"
SNAP="$WIN_SB_UNIX/snapshots"
BASE_NAME="${BASE:-baseline}"
if [[ "${1:-}" == "--baseline" ]]; then
  [[ -f "$SNAP/baseline.files.tsv" ]] && { echo "check-outside-win: baseline already exists, refusing to overwrite" >&2; exit 2; }
  winps snapshot.ps1 -OutDir "$WIN_SB\\snapshots" -Label baseline -Sandbox "$WIN_SB"
  echo "check-outside-win: baseline taken"; exit 0
fi
if [[ ! -f "$SNAP/$BASE_NAME.files.tsv" ]]; then
  echo "check-outside-win: no snapshot '$BASE_NAME' — take the baseline with --baseline before any host action" >&2; exit 2
fi
label="${LABEL:-after-$(date +%Y%m%d-%H%M%S)}"
base="${BASE:-baseline}"
winps snapshot.ps1 -OutDir "$WIN_SB\\snapshots" -Label "$label" -Sandbox "$WIN_SB"

diff_out="$SNAP/$label.diff.txt"
# files: path \t kind \t size \t mtime. A directory whose mtime changed is reported as "touched".
awk -F'\t' 'FNR==NR {b[$1]=$2"\t"$3"\t"$4; next}
  { a[$1]=$2"\t"$3"\t"$4 }
  END {
    for (p in a) if (!(p in b)) print "added\t" p
    for (p in b) if (!(p in a)) print "removed\t" p
    for (p in a) if ((p in b) && a[p] != b[p]) { split(a[p], x, "\t"); print (x[1]=="d" ? "touched" : "modified") "\t" p }
  }' "$SNAP/$base.files.tsv" "$SNAP/$label.files.tsv" | LC_ALL=C sort -t$'\t' -k2 > "$diff_out"
csort() { LC_ALL=C sort -u "$1"; }
if [[ -f "$SNAP/$base.reg.tsv" ]]; then
  LC_ALL=C comm -13 <(csort "$SNAP/$base.reg.tsv") <(csort "$SNAP/$label.reg.tsv") | sed 's/^/reg-added\tregistry:/' >> "$diff_out" || true
  LC_ALL=C comm -23 <(csort "$SNAP/$base.reg.tsv") <(csort "$SNAP/$label.reg.tsv") | sed 's/^/reg-removed\tregistry:/' >> "$diff_out" || true
fi
LC_ALL=C comm -13 <(csort "$SNAP/$base.tasks.txt") <(csort "$SNAP/$label.tasks.txt") | sed 's/^/task-added\tschtasks:/' >> "$diff_out" || true
LC_ALL=C comm -23 <(csort "$SNAP/$base.tasks.txt") <(csort "$SNAP/$label.tasks.txt") | sed 's/^/task-removed\tschtasks:/' >> "$diff_out" || true

# Acknowledged globs from OUTSIDE.md: %LOCALAPPDATA%\x → LOCALAPPDATA/x (backslashes → slashes).
globs=()
while IFS=$'\t' read -r path _rest; do
  case "$path" in
    %*%*) g="${path#%}"; g="${g/\%/}"; globs+=( "${g//\\//}" ) ;;
    schtasks:*|registry:*) globs+=( "${path//\\//}" ) ;;
  esac
done < <("$ROOT/scripts/outside-table.sh")

shopt -s nocasematch
unacked=() acked=0
while IFS=$'\t' read -r what p; do
  [[ -z "$p" ]] && continue
  q="${p//\\//}"
  [[ "$q" == LOCALAPPDATA/Temp || "$q" == LOCALAPPDATA/Temp/* ]] && q="TEMP${q#LOCALAPPDATA/Temp}"
  ok=0
  # A tbd-test-* task that still exists after a run was not deleted by its test: always a failure.
  if [[ "$what" == task-added && "$q" == schtasks:/tbd-test-* ]]; then unacked+=( "$what	$p" ); continue; fi
  for g in "${globs[@]}"; do
    [[ "$q" == $g ]] && { ok=1; break; }   # exact glob: rows that cover descendants end in *
    # a directory whose mtime changed only because an acknowledged child changed
    [[ "$what" == touched && "${g,,}" == "${q,,}/"* ]] && { ok=1; break; }
  done
  if [[ $ok == 1 ]]; then acked=$((acked+1)); else unacked+=( "$what	$p" ); fi
done < "$diff_out"
total=$(wc -l < "$diff_out")
echo "check-outside-win: $label vs $base: $total difference(s), $acked acknowledged in OUTSIDE.md (full diff: $diff_out)"
if (( ${#unacked[@]} )); then
  echo "check-outside-win: FAIL — not in OUTSIDE.md:"
  printf '  %s\n' "${unacked[@]}"
  exit 1
fi
echo "check-outside-win: clean"
