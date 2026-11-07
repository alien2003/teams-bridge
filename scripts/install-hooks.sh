#!/usr/bin/env bash
# Install the git hooks of this repository (`mise run hooks`): pre-push → scripts/prepush-check.sh.
# Idempotent; it refuses to replace a pre-push hook it did not write. The hook lives in the common
# git dir, so it covers every worktree.
set -euo pipefail
cd "$(dirname "$0")/.."
marker='# teams-bridge: installed by scripts/install-hooks.sh'
hooks="$(git rev-parse --path-format=absolute --git-path hooks)"
mkdir -p "$hooks"
hook="$hooks/pre-push"
if [[ -e "$hook" ]] && ! command grep -qF "$marker" "$hook"; then
  echo "install-hooks: $hook exists and is not ours; move it away, then rerun" >&2; exit 1
fi
# Literal text: no expansion here (a command substitution in it would run at install time).
want="$marker; edit that script, then run: mise run hooks"
# shellcheck disable=SC2016  # the $(…) and "$@" belong to the hook and must stay unexpanded
want="$(printf '%s\n' '#!/bin/sh' "$want" \
  '# Checks the commits being pushed: stop list, secret patterns, author/committer emails, UTC offsets.' \
  'exec "$(git rev-parse --show-toplevel)/scripts/prepush-check.sh" --hook "$@"')"
if [[ -f "$hook" && "$(cat "$hook")" == "$want" ]]; then
  echo "install-hooks: pre-push already installed ($hook)"
else
  printf '%s\n' "$want" > "$hook"
  chmod +x "$hook"
  echo "install-hooks: pre-push installed ($hook)"
fi
