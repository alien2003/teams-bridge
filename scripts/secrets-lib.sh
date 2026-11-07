# Shared by secrets-check.sh and prepush-check.sh (sourced, not run).

# Extended regexes of private keys and tokens. A line that carries the marker
# "secrets-check: allow" is never a hit (this file's own patterns carry it where they would match).
SECRET_PATTERNS=(
  '-----BEGIN [A-Z0-9 ]*PRIVATE KEY( BLOCK)?-----'   # secrets-check: allow
  '"private_key"[[:space:]]*:[[:space:]]*"-----BEGIN'
  'AIza[0-9A-Za-z_-]{35}'          # Google API key  secrets-check: allow
  'ya29\.[0-9A-Za-z_-]{20,}'       # Google OAuth access token
  '1//[0-9A-Za-z_-]{30,}'          # Google OAuth refresh token
  'gh[pousr]_[0-9A-Za-z]{30,}'     # GitHub tokens
  'github_pat_[0-9A-Za-z_]{30,}'
  'AKIA[0-9A-Z]{16}'               # AWS access key id
  'xox[abprs]-[0-9A-Za-z-]{10,}'   # Slack
  'secret[[:space:]]*=[[:space:]]*"[A-Z2-7]{26}"'   # a tbd pairing secret in a config file  secrets-check: allow
)
SECRET_ALLOW_MARKER='secrets-check: allow'

# denylist_path: the git-ignored stop list of the MAIN checkout (found from any worktree), or
# $TBD_DENYLIST. One string per line; never committed or printed.
denylist_path() {
  local common
  common="$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null || echo "$PWD/.git")"
  echo "${TBD_DENYLIST:-$(dirname "$common")/.local/denylist.txt}"
}

# denylist_patterns LIST OUT: write the fixed-string patterns for grep -F -f to OUT: each entry,
# plus GUID-shaped entries without their dashes.
denylist_patterns() {
  { command grep -v '^[[:space:]]*$' "$1"
    command grep -E '^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$' "$1" | tr -d '-' || true
  } > "$2"
}

# scratch_dir: the repository's .tmp (TMPDIR under mise), created if missing; never /tmp.
scratch_dir() {
  local common d
  common="$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null || echo "$PWD/.git")"
  d="${TMPDIR:-$(dirname "$common")/.tmp}"
  mkdir -p "$d" && echo "$d"
}
