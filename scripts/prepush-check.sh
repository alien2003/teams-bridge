#!/usr/bin/env bash
# Check what is about to leave this machine: every commit being pushed, its whole tree and its
# metadata. Installed as .git/hooks/pre-push by `mise run hooks`; by hand it checks every ref.
#
#   prepush-check.sh                  every commit reachable from any ref (same as --all)
#   prepush-check.sh <rev-list args>  e.g. main, origin/main..main
#   prepush-check.sh --hook R URL     git's pre-push interface (refs on stdin)
#
# Per commit (and per annotated tag being pushed):
#   * stop list (.local/denylist.txt, as in secrets-check) over every file of the commit's tree
#     (binary files too: a PNG text chunk counts), over every path in that tree and over the raw
#     commit object (message, author and committer names and emails);
#   * the secrets-check key/token patterns over the same;
#   * author and committer emails: only alien2003@protonmail.ch, *@users.noreply.github.com or
#     noreply@github.com;
#   * author and committer timezone offsets: only +0000 (commits are made with TZ=UTC; the offset
#     tells where the author lives).
# Also the stop list over the names of the refs being pushed (local and remote names; by hand with
# --all, every ref). Output names commits and files only, never the matching text; a path or ref
# name that itself holds a stop-list value is not printed. Exit 1 on any finding.
set -euo pipefail
ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"
source scripts/secrets-lib.sh

mode=args
if [[ "${1:-}" == --hook ]]; then mode=hook; remote="${2:-}"; fi

zero='0000000000000000000000000000000000000000'
commits=() tags=() refs=()
if [[ $mode == hook ]]; then
  while read -r lref lsha rref rsha; do
    [[ -z "${lsha:-}" || "$lsha" == "$zero" || "$lsha" =~ ^0+$ ]] && continue   # a deletion
    refs+=( "${lref:-}" "${rref:-}" )
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
  if [[ " $* " == *" --all "* ]]; then
    mapfile -t tags < <(git for-each-ref --format='%(objectname) %(objecttype)' refs/tags | awk '$2=="tag"{print $1}')
    mapfile -t refs < <(git for-each-ref --format='%(refname)')
  fi
fi
mapfile -t commits < <(printf '%s\n' "${commits[@]}" | awk 'NF && !seen[$0]++')
if (( ${#commits[@]} == 0 && ${#tags[@]} == 0 && ${#refs[@]} == 0 )); then echo "prepush-check: nothing to check"; exit 0; fi

fail=0
declare -A seen=()
finding() { echo "  $*"; fail=1; }
# once per file and kind: the same file usually stays the same across many commits. A path that
# itself holds a stop-list value is shown as its blob id only.
file_finding() {
  local c="$1" f="$2" kind="$3" shown
  [[ -n "${seen["$kind|${f%%:*}"]:-}" ]] && return 0
  seen["$kind|${f%%:*}"]=1
  shown="$f"
  if [[ -n "$pat" ]] && command grep -qiF -f "$pat" <<<"${f%%:*}"; then
    shown="(object $(git rev-parse --short "$c:${f%%:*}" 2>/dev/null || echo '?'), path not shown)${f#"${f%%:*}"}"
  fi
  finding "$(short "$c"):$shown: $kind (listed once, for the newest commit that has it)"
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

# Trees: git grep over the commit's whole tree, binary files included (-a, not -I), and the
# stop list over every path of the tree; print "<commit>:<path>" only.
grep_tree() {
  local c="$1" out rc f
  if [[ -n "$pat" ]]; then
    set +e; out="$(git grep -l -a -i -F -f "$pat" "$c" --)"; rc=$?; set -e
    (( rc > 1 )) && { echo "prepush-check: git grep failed on $c"; exit 2; }
    while IFS= read -r f; do [[ -z "$f" ]] || file_finding "$c" "${f#*:}" "stop-list value"; done <<<"$out"
    while IFS= read -r -d '' f; do
      file_finding "$c" "$f" "stop-list value in the path"
    done < <(git ls-tree -r -z --name-only "$c" | command grep -zaiF -f "$pat" || true)
  fi
  set +e; out="$(git grep -n -I -E "${eargs[@]}" "$c" --)"; rc=$?; set -e
  (( rc > 1 )) && { echo "prepush-check: git grep failed on $c"; exit 2; }
  out="$(command grep -vF "$SECRET_ALLOW_MARKER" <<<"$out" | cut -d: -f2,3 || true)"
  while IFS= read -r f; do [[ -z "$f" ]] || file_finding "$c" "$f" "secret-like pattern"; done <<<"$out"
}

if [[ -n "$pat" ]]; then
  n=0
  for r in "${refs[@]}"; do [[ -n "$r" ]] && command grep -qiF -f "$pat" <<<"$r" && n=$((n + 1)); done
  (( n == 0 )) || finding "$n ref name(s) hold a stop-list value (not printed)"
fi
for c in "${commits[@]}"; do check_object "$c" "commit $(short "$c")"; grep_tree "$c"; done
for t in "${tags[@]}"; do
  name="$(git for-each-ref --points-at "$t" --format='%(refname:short)' refs/tags | head -n1)"
  [[ -n "$pat" ]] && command grep -qiF -f "$pat" <<<"$name" && name="$(short "$t") (name not shown)"
  check_object "$t" "tag $name"
done

if (( fail )); then
  echo "prepush-check: FAIL (${#commits[@]} commit(s), ${#tags[@]} tag(s) checked)"; exit 1
fi
echo "prepush-check: clean (${#commits[@]} commit(s), ${#tags[@]} tag(s); $( [[ -n "$pat" ]] && echo "stop list $(command grep -c . "$denylist") entries" || echo "no stop list"))"
