#!/usr/bin/env bash
# Check what is about to leave this machine: every commit being pushed, its whole tree and its
# metadata. Installed as .git/hooks/pre-push by `mise run hooks`; by hand it checks every ref.
#
#   prepush-check.sh                  every commit reachable from any ref (same as --all)
#   prepush-check.sh <rev-list args>  e.g. main, origin/main..main
#   prepush-check.sh --hook R URL     git's pre-push interface (refs on stdin)
#
# Per commit (and per annotated tag being pushed):
#   * stop list (.local/denylist.txt, as in secrets-check) over every file of the commit's tree and
#     over the raw commit object (message, author and committer names and emails);
#   * the secrets-check key/token patterns over the same;
#   * author and committer emails: only alien2003@protonmail.ch, *@users.noreply.github.com or
#     noreply@github.com;
#   * author and committer timezone offsets: only +0000 (commits are made with TZ=UTC; the offset
#     tells where the author lives).
# Output names commits and files only, never the matching text. Exit 1 on any finding.
set -euo pipefail
ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"
source scripts/secrets-lib.sh

mode=args
if [[ "${1:-}" == --hook ]]; then mode=hook; remote="${2:-}"; fi

zero='0000000000000000000000000000000000000000'
commits=() tags=()
if [[ $mode == hook ]]; then
  while read -r _lref lsha _rref rsha; do
    [[ -z "${lsha:-}" || "$lsha" == "$zero" || "$lsha" =~ ^0+$ ]] && continue   # a deletion
    [[ "$(git cat-file -t "$lsha")" == tag ]] && tags+=( "$lsha" )
    if [[ "$rsha" =~ ^0+$ ]] || ! git cat-file -e "$rsha^{commit}" 2>/dev/null; then
      mapfile -t -O "${#commits[@]}" commits < <(git rev-list "$lsha" --not --remotes="$remote" --)
    else
      mapfile -t -O "${#commits[@]}" commits < <(git rev-list "$rsha..$lsha" --)
    fi
  done
else
  (( $# )) || set -- --all
  mapfile -t commits < <(git rev-list "$@" --)
  [[ " $* " == *" --all "* ]] && mapfile -t tags < <(git for-each-ref --format='%(objectname) %(objecttype)' refs/tags | awk '$2=="tag"{print $1}')
fi
mapfile -t commits < <(printf '%s\n' "${commits[@]}" | awk 'NF && !seen[$0]++')
if (( ${#commits[@]} == 0 && ${#tags[@]} == 0 )); then echo "prepush-check: nothing to check"; exit 0; fi

fail=0
declare -A seen=()
finding() { echo "  $*"; fail=1; }
# once per file and kind: the same file usually stays the same across many commits
file_finding() {
  local c="$1" f="$2" kind="$3"
  [[ -n "${seen["$kind|${f%%:*}"]:-}" ]] && return 0
  seen["$kind|${f%%:*}"]=1
  finding "$(short "$c"):$f: $kind (listed once, for the newest commit that has it)"
}

# Stop list.
denylist="$(denylist_path)" pat=""
if [[ -s "$denylist" ]]; then
  pat="$(mktemp "$(scratch_dir)/denylist.XXXXXX")"
  trap 'rm -f "$pat"' EXIT
  denylist_patterns "$denylist" "$pat"
elif [[ "${TBD_NO_DENYLIST:-}" == 1 ]]; then
  echo "prepush-check: no stop list (TBD_NO_DENYLIST=1)"
else
  echo "prepush-check: FAIL — stop list $denylist missing or empty (set TBD_NO_DENYLIST=1 to run without it)"
  exit 1
fi
eargs=(); for p in "${SECRET_PATTERNS[@]}"; do eargs+=( -e "$p" ); done

short() { git rev-parse --short "$1"; }

# Metadata: identity lines of a raw commit or tag object ("author|committer|tagger Name <email> T TZ").
check_object() {
  local obj="$1" what="$2" raw line role email tz
  raw="$(git cat-file -p "$obj")"
  while IFS= read -r line; do
    [[ -z "$line" ]] && break                                  # end of the headers
    case "$line" in author\ *|committer\ *|tagger\ *) ;; *) continue ;; esac
    role="${line%% *}"
    email="$(sed -E 's/^.*<([^>]*)> [0-9]+ [-+][0-9]{4}$/\1/' <<<"$line")"
    tz="${line##* }"
    case "$email" in
      alien2003@protonmail.ch|*@users.noreply.github.com|noreply@github.com) ;;
      *) finding "$what: $role email is not an allowed address" ;;
    esac
    [[ "$tz" == +0000 ]] || finding "$what: $role timezone offset is $tz, not +0000"
  done <<<"$raw"
  if [[ -n "$pat" ]] && command grep -qaiF -f "$pat" <<<"$raw"; then
    finding "$what: stop-list value in the message or the author/committer fields"
  fi
  if command grep -aE "${eargs[@]}" <<<"$raw" | command grep -qvF "$SECRET_ALLOW_MARKER"; then
    finding "$what: secret-like pattern in the message or the author/committer fields"
  fi
}

# Trees: git grep over the commit's whole tree; print "<commit>:<path>" only.
grep_tree() {
  local c="$1" out rc
  if [[ -n "$pat" ]]; then
    set +e; out="$(git grep -l -I -i -F -f "$pat" "$c" --)"; rc=$?; set -e
    (( rc > 1 )) && { echo "prepush-check: git grep failed on $c"; exit 2; }
    while IFS= read -r f; do [[ -z "$f" ]] || file_finding "$c" "${f#*:}" "stop-list value"; done <<<"$out"
  fi
  set +e; out="$(git grep -n -I -E "${eargs[@]}" "$c" --)"; rc=$?; set -e
  (( rc > 1 )) && { echo "prepush-check: git grep failed on $c"; exit 2; }
  out="$(command grep -vF "$SECRET_ALLOW_MARKER" <<<"$out" | cut -d: -f2,3 || true)"
  while IFS= read -r f; do [[ -z "$f" ]] || file_finding "$c" "$f" "secret-like pattern"; done <<<"$out"
}

for c in "${commits[@]}"; do check_object "$c" "commit $(short "$c")"; grep_tree "$c"; done
for t in "${tags[@]}"; do check_object "$t" "tag $(git for-each-ref --points-at "$t" --format='%(refname:short)' refs/tags | head -n1)"; done

if (( fail )); then
  echo "prepush-check: FAIL (${#commits[@]} commit(s), ${#tags[@]} tag(s) checked)"; exit 1
fi
echo "prepush-check: clean (${#commits[@]} commit(s), ${#tags[@]} tag(s); $( [[ -n "$pat" ]] && echo "stop list $(command grep -c . "$denylist") entries" || echo "no stop list"))"
