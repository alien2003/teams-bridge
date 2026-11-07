#!/usr/bin/env bash
# Lint what CI runs and the scripts behind it (D22): actionlint over the workflows (it also runs
# ShellCheck on every `run:` block), zizmor --offline over .github/ (workflows and dependabot.yml),
# bash -n and shellcheck over every tracked shell script. All three tools are pinned in mise.toml.
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p .tmp
for t in actionlint zizmor shellcheck; do
  command -v "$t" >/dev/null || { echo "lint-ci: $t not on PATH; run through mise (mise run lint-ci)" >&2; exit 1; }
done
mapfile -t scripts < <(git ls-files -co --exclude-standard '*.sh' | while read -r f; do [[ -f "$f" ]] && echo "$f"; done)
for f in "${scripts[@]}"; do bash -n "$f"; done
shellcheck -x "${scripts[@]}"
if [[ -d .github ]]; then
  actionlint
  zizmor --offline --no-progress --strict-collection .github
fi
echo "lint-ci: ok (${#scripts[@]} scripts$([[ -d .github ]] && echo ', .github'))"
