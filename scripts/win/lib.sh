# Shared helpers for the Windows-side scripts. Source, don't execute.
# shellcheck shell=bash
: "${TBD_WIN_SANDBOX:?TBD_WIN_SANDBOX is not set (run through mise)}"
WIN_SB_UNIX="$TBD_WIN_SANDBOX"
WIN_SB="$(wslpath -w "$WIN_SB_UNIX")"
winps() { # run a PowerShell script file that lives in the sandbox, cwd = sandbox
  local script="$1"; shift
  ( cd "$WIN_SB_UNIX" && powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "$WIN_SB\\scripts\\$script" "$@" ) | tr -d '\r'
}
