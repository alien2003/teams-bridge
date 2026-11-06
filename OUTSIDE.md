# OUTSIDE.md — everything outside the repository

Every path or package outside `$ROOT` (`/home/user/teams-bridge`) that this project created, needs, or
touched. `mise run check-outside` / `mise run check-outside-win` treat the rows below as acknowledged;
anything else they find is a failure. `mise run clean-outside` executes the *Removal* command of every
row whose *clean-outside* column is `run`, prints it for `print` rows (apt packages, anything needing
sudo or the user's judgement) and skips `keep` / `noise` rows.

Paths are globs; `~` is the WSL home directory, `%VAR%` a Windows environment variable.

<!-- BEGIN outside-table -->
| Path | Kind | Why | Removal | clean-outside |
|---|---|---|---|---|
| `~/.local/share/mise/installs/go/1.27.3` | mise store | Go toolchain pinned in `mise.toml` | `mise uninstall go@1.27.3` | keep |
| `~/.local/share/mise/installs/java/temurin-17.0.21+9` | mise store | JDK 17 for Gradle / Android | `mise uninstall java@temurin-17.0.21+9` | keep |
| `~/.local/share/mise/installs/gradle/9.8.2` | mise store | Gradle for the Android build (no wrapper is committed) | `mise uninstall gradle@9.8.2` | keep |
| `~/.local/share/mise/installs/go-honnef-co-go-tools-cmd-staticcheck/0.8.2` | mise store | staticcheck | `mise uninstall go:honnef.co/go/tools/cmd/staticcheck@0.8.2` | keep |
| `~/.local/state/mise/trusted-configs/user-teams-bridge-*` | mise state | `mise trust` entry for this repository's `mise.toml` | `mise trust --untrust /home/user/teams-bridge/mise.toml` | keep |
| `~/.config/go/telemetry/local/*@go1.27.3-*.count` | noise | Pre-existing Go telemetry counters (born before our stamp). `mise install` runs `go version` without the project env, which bumps them; inside the project env Go writes to `.xdg/config/go/telemetry` | none (not ours) | noise |
| `/tmp/hsperfdata_user` | noise | Pre-existing HotSpot perf-data dir (born before our stamp). Its mtime is touched by the `java -version` that `mise install` runs; our own JVMs run with `-XX:-UsePerfData` | none (not ours) | noise |
| `%APPDATA%\Ferdium\*` | noise (host) | Ferdium running on the host | none (not ours) | noise |
| `%LOCALAPPDATA%\D3DSCache\*` | noise (host) | Direct3D shader cache, written by any GPU-using app | none (not ours) | noise |
| `%LOCALAPPDATA%\wsl\*` | noise (host) | WSL's own `ext4.vhdx`: every write inside WSL lands in it | none | noise |
| `%LOCALAPPDATA%\Microsoft\Windows\ActionCenterCache` | noise (host) | Windows notification centre | none (not ours) | noise |
| `%APPDATA%\Microsoft\Installer` | noise (host) | Windows Installer bookkeeping | none (not ours) | noise |
| `%TEMP%` | noise (host) | The TEMP directory itself (its mtime changes whenever anything creates a temp file) | none | noise |
| `%TEMP%\????????-????-????-????-????????????.tmp` | noise (host) | Transient GUID-named temp files of a background app; they disappear within minutes | none (not ours) | noise |
| `%TEMP%\notificationimages` | noise (host) | Windows toast images | none (not ours) | noise |
| `%TEMP%\Microsoft.PackageManagement` | noise (host) | PowerShell's PackageManagement module cache, touched by any `powershell.exe` start (including our snapshot script) | none | noise |
| `%TEMP%\wsl-crashes` | noise (host) | WSL crash-dump folder | none | noise |
| `/tmp/.tmp??????` | noise (mise) | `mise run` writes each task script to a transient Rust `tempfile` in the system temp dir (mise's own process does not see the project `TMPDIR`); it is gone when the task ends, but the `mise run check-outside` that is checking sees its own | none (transient) | noise |
| `%USERPROFILE%\ntuser.dat*` | noise (host) | Transaction logs of the user registry hive; change whenever any app writes HKCU | none | noise |
| `%LOCALAPPDATA%\Microsoft\Windows\UsrClass.dat*` | noise (host) | Same for the HKCU\Software\Classes hive | none | noise |
| `%LOCALAPPDATA%\Microsoft\TokenBroker\*` | noise (host) | Windows Web Account Manager token cache | none (not ours) | noise |
| `/var/tmp/systemd-private-*` | noise (system) | the same, under `/var/tmp` | none (not ours) | noise |
| `/run/user/*/systemd/user/tbd-test-*` | transient (tmpfs) | Linux install round-trip test: `systemctl --user --runtime link/enable` of a never-started unit; removed by the test, gone at reboot anyway | `systemctl --user --runtime disable tbd-test-<id>.service; systemctl --user daemon-reload` | noise |
| `schtasks:\tbd-test-*` | Windows task | install/uninstall round-trip test (BRIEF §1.5/§7): created and deleted in the same test | `schtasks /delete /tn tbd-test-<id> /f` | noise |
| `registry:*HKEY_CURRENT_USER\Software\Microsoft\Edge\PreferenceMACs\tbd*` | registry (ours) | Host Edge mirrors the preference MACs of our profile `tbd` here, whatever `--user-data-dir` says (D14) | `scripts/win/clean-edge-registry.sh` | run |
| `registry:*HKEY_CURRENT_USER\Software\Microsoft\Edge\PrefsEnclave*` | registry (ours) | Host Edge adds one `ev_<hash>` value per profile path it opens; the script deletes the values that were not in the baseline | `scripts/win/clean-edge-registry.sh` (same run as above) | noise |
| `registry:*HKEY_CURRENT_USER\Software\Microsoft\Edge\PreferenceMACs\Default*` | registry (Edge) | Overwritten once by the first `test-win` run, before the profile got its own name (D14). Report-only copy of the user's Edge profile MACs (Chromium uses it for metrics, not enforcement); the user's Edge rewrites it on its next start | none | noise |
| `registry:*HKEY_CURRENT_USER\Software\Microsoft\Edge\BLBeacon*` | registry (Edge) | Edge's launch beacon, written by every msedge.exe start (shared with the user's Edge) | none (shared) | noise |
| `registry:*HKEY_CURRENT_USER\Software\Microsoft\Edge\ElfBeacon*` | registry (Edge) | Edge's early-launch beacon, same | none (shared) | noise |
| `registry:*HKEY_CURRENT_USER\Software\Microsoft\Edge\StabilityMetrics*` | registry (Edge) | `exited_cleanly` flag, written at every start/exit (shared) | none (shared) | noise |
| `registry:*HKEY_CURRENT_USER\Software\Microsoft\EdgeUpdate*` | registry (Edge) | msedge.exe records `lastrun`/metrics ids for Edge Update at every start; EdgeUpdate's own scheduled tasks write here too (shared) | none (shared) | noise |
| `%LOCALAPPDATA%\Microsoft\OneAuth\*` | noise (Edge/host) | Microsoft identity library used by Edge (implicit sign-in) and Office; touched by an msedge.exe start | none (shared) | noise |
| `%LOCALAPPDATA%\Microsoft\Credentials\*` | noise (host) | Windows Credential Manager (WAM/TokenBroker, OneAuth) | none (not ours) | noise |
| `%APPDATA%\Microsoft\Crypto\Keys` | noise (host) | Windows CNG/DPAPI key store (also used by Edge's OSCrypt) | none (not ours) | noise |
| `%APPDATA%\Microsoft\Windows\Themes` | noise (host) | Windows theme/wallpaper cache | none | noise |
| `%LOCALAPPDATA%\Microsoft\Windows\Caches` | noise (host) | Windows caches | none | noise |
| `%LOCALAPPDATA%\Microsoft\Windows\Explorer` | noise (host) | Explorer icon/thumbnail caches | none | noise |
| `%LOCALAPPDATA%\CEF\*` | noise (host) | a CEF-based desktop app on the host | none (not ours) | noise |
| `%LOCALAPPDATA%\Packages\cr.sb.*` | noise (Edge) | Chromium's sandbox AppContainer profile (`cr.sb.<hash of the browser>`), created once by Edge for its sandboxed processes and shared with the user's own Edge; it pre-dates our runs, they only touch it | none (shared) | noise |
<!-- END outside-table -->

## apt packages

None so far.

## Windows host

Only `C:\Users\user\Projects\teams-bridge` (`TBD_WIN_SANDBOX`), which the user deletes by hand.
Snapshot diffs from `mise run check-outside-win` are summarised in the table above.
