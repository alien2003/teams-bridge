#!/usr/bin/env bash
# shellcheck disable=SC2016 # the rules and fixtures are code in single quotes, not expansions
# Bash 3.2 guard (D26, mise run test-scripts): macOS ships bash 3.2, and the macOS CI jobs run
# scripts/build.sh, scripts/test-go.sh, scripts/version.sh and scripts/ci-mac-footprint.sh with it.
# This greps those scripts for constructs that bash 3.2 rejects or runs differently, and first
# proves on a fixture that each pattern is caught. A static check only: the real proof is the
# macOS CI job.
# Usage: bash32_test.sh [FILE...]   (default: the scripts above)
# A line that must keep a flagged construct carries the comment "bash32: ok" with the reason.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# name<TAB>extended regex, matched against each line with its comment stripped.
rules=(
  $'case fall-through (;;& or ;&)\t;;?&'
  $'mapfile / readarray (bash 4)\t(^|[^[:alnum:]_-])(mapfile|readarray)([^[:alnum:]_-]|$)'
  $'associative or nameref array (declare -A / -n, bash 4.0 / 4.3)\t(^|[^[:alnum:]_])(declare|local|typeset)( +-[[:alpha:]]+)* +-[[:alpha:]]*[An]'
  $'case modification ${x,,} ${x^^} (bash 4)\t\\$\\{[#!]?[[:alnum:]_]+(\\[[^]]*\\])?(,|\\^)'
  $'parameter transformation ${x@Q} (bash 4.4)\t\\$\\{[[:alnum:]_]+(\\[[^]]*\\])?@[[:alpha:]]\\}'
  $'append both streams &>> (bash 4)\t&>>'
  $'pipe both streams |& (bash 4)\t\\|&'
  $'coproc (bash 4)\t(^|[^[:alnum:]_])coproc([^[:alnum:]_]|$)'
  $'automatic fd {fd}> (bash 4.1)\t(^|[^$[:alnum:]_])\\{[[:alpha:]_][[:alnum:]_]*\\}[<>]'
  $'negative array index ${a[-1]} (bash 4.3)\t\\$\\{[[:alnum:]_]+\\[-[0-9]'
  $'wait -n (bash 4.3)\t(^|[^[:alnum:]_])wait +-[[:alpha:]]*n'
  $'shopt globstar / lastpipe / ... (bash 4)\tshopt +-s +(globstar|lastpipe|autocd|dirspell|checkjobs|direxpand|globasciiranges|inherit_errexit|localvar_inherit|assoc_expand_once)'
  $'$EPOCHSECONDS / $EPOCHREALTIME / $BASHPID (bash 4 / 5)\t\\$\\{?(EPOCHSECONDS|EPOCHREALTIME|BASHPID)'
  $'"${a[@]}" of a possibly empty array under set -u (bash < 4.4: unbound); write ${a[@]+"${a[@]}"}\t\\$\\{[[:alpha:]_][[:alnum:]_]*\\[[@*]\\]\\}'
)

# Prints "file:line: rule" for every hit; returns 1 if there was one.
check() { # file
  local f="$1" n=0 line code rule name re bad=0
  while IFS= read -r line || [[ -n "$line" ]]; do
    n=$((n + 1))
    [[ "$line" == *"bash32: ok"* ]] && continue
    # Strip a comment: a "#" at the start or after a blank (good enough for these scripts; a "#"
    # inside a quoted string only hides the rest of that line from the check).
    code="$(sed -E 's/(^|[[:space:]])#.*$//' <<<"$line")"
    [[ -n "${code//[[:space:]]/}" ]] || continue
    # The guarded expansion ${a[@]+"${a[@]}"} is the safe form; drop it before the array rule.
    code="$(sed -E 's/\$\{([[:alpha:]_][[:alnum:]_]*)\[([@*])\]\+"\$\{\1\[\2\]\}"\}//g' <<<"$code")"
    for rule in "${rules[@]}"; do
      name="${rule%%$'\t'*}" re="${rule#*$'\t'}"
      if command grep -qE -- "$re" <<<"$code"; then
        echo "$f:$n: $name"
        bad=1
      fi
    done
  done <"$f"
  return "$bad"
}

fail=0 n=0
# Self-test: every rule fires on its fixture line, and nothing fires on the safe lines.
fixture_bad=(
  'case $x in a) echo ;;& b) echo ;; esac'
  'case $x in a) echo ;& esac'
  'mapfile -t lines <file'
  'readarray lines <file'
  'declare -A map'
  'local -rA map=()'
  'declare -n ref=x'
  'echo "${x,,}"'
  'echo "${x^^}"'
  'echo "${arr[1]^}"'
  'echo "${x@Q}"'
  'cmd &>>log'
  'cmd |& tee log'
  'coproc cat'
  'exec {fd}>file'
  'echo "${a[-1]}"'
  'wait -n'
  'shopt -s globstar'
  'echo "$EPOCHSECONDS"'
  'go test "${par[@]}" ./...'
  'printf "%s\n" "${list[*]}"'
)
fixture_ok=(
  'case $x in a) echo ;; esac'
  'go test ${par[@]+"${par[@]}"} "$@" ./...'
  'echo "${#arr[@]}" "${BASH_REMATCH[1]}" "${x:-y}" "${x#v}" "${x%%,*}" "${x//a/b}"'
  'local a b; declare -r x=1; declare -x y'
  '[[ "$a" == b ]] && echo ok   # mapfile in a comment is fine'
  'cmd >log 2>&1'
  'cmd 2>&1 | tee log'
  'echo "${x}" "${x/,/;}"'
  'wait "$pid"'
  'echo "${arr[@]}"   # bash32: ok, arr is never empty'
)
self="$(mktemp "${TMPDIR:-$ROOT/.tmp}/bash32-test.XXXXXX")"
trap 'rm -f "$self"' EXIT
for l in "${fixture_bad[@]}"; do
  n=$((n + 1))
  printf '%s\n' "$l" >"$self"
  if check "$self" >/dev/null; then echo "FAIL not caught: $l"; fail=1; fi
done
for l in "${fixture_ok[@]}"; do
  n=$((n + 1))
  printf '%s\n' "$l" >"$self"
  if ! out="$(check "$self")"; then echo "FAIL false positive: $l ($out)"; fail=1; fi
done

if (($# == 0)); then set -- scripts/build.sh scripts/test-go.sh scripts/version.sh scripts/ci-mac-footprint.sh; fi
for f in "$@"; do
  n=$((n + 1))
  if ! check "$f"; then fail=1; fi
done

((fail == 0)) || { echo "bash32_test: FAILED"; exit 1; }
echo "bash32_test: ok ($n checks)"
