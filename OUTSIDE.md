# OUTSIDE.md — everything outside the repository

Every path, registry entry, service or package outside `$ROOT` (`/home/user/teams-bridge`) that this
project created, needs, or touched. `mise run check-outside` / `mise run check-outside-win` treat the
rows below as acknowledged; anything else they find is a failure. `mise run clean-outside` executes
the *Removal* command of every row whose *clean-outside* column is `run`, prints it for `print` rows
(apt packages, anything needing sudo or the user's judgement) and skips `keep` / `noise` rows.

This file lists only what tbd, Edge, Windows, WSL and mise themselves touch. Host-specific noise of
your machine (the desktop apps, launchers, GPU caches and crash dumps of other software that happen
to write while the Windows checks run) goes into the git-ignored `OUTSIDE.local.md` next to this
file: the same table with the same `<!-- BEGIN outside-table -->` / `<!-- END outside-table -->`
markers. `scripts/outside-table.sh`, and so every check above, reads both files; a fresh clone has
no `OUTSIDE.local.md`, and the first `check-outside-win` failure says which rows your host needs.

Paths are globs matched exactly: a row ending in `*` also covers everything below it, a row without
it covers only that entry (a directory whose mtime changed). `~` is the WSL home directory,
`%VAR%` a Windows environment variable, `registry:` a line of the HKCU snapshot, `schtasks:` a
scheduled task.

<!-- BEGIN outside-table -->
| Path | Kind | Why | Removal | clean-outside |
|---|---|---|---|---|
| `~/.local/share/mise/installs/go/1.27.3*` | mise store | Go toolchain pinned in `mise.toml` | `mise uninstall go@1.27.3` | keep |
| `~/.local/share/mise/installs/java/temurin-17.0.21+9*` | mise store | JDK 17 for Gradle / Android | `mise uninstall java@temurin-17.0.21+9` | keep |
| `~/.local/share/mise/installs/gradle/9.8.2*` | mise store | Gradle for the Android build (no wrapper is committed) | `mise uninstall gradle@9.8.2` | keep |
| `~/.local/share/mise/installs/go-honnef-co-go-tools-cmd-staticcheck/0.8.2*` | mise store | staticcheck | `mise uninstall go:honnef.co/go/tools/cmd/staticcheck@0.8.2` | keep |
| `~/.local/share/mise/installs/actionlint/1.7.12*` | mise store | actionlint for `mise run lint-ci` (aqua backend) | `mise uninstall actionlint@1.7.12` | keep |
| `~/.local/share/mise/installs/zizmor/1.32.0*` | mise store | zizmor for `mise run lint-ci` (aqua backend) | `mise uninstall zizmor@1.32.0` | keep |
| `~/.local/share/mise/installs/shellcheck/0.11.0*` | mise store | shellcheck for `mise run lint-ci` and actionlint (aqua backend) | `mise uninstall shellcheck@0.11.0` | keep |
| `~/.cache/sigstore-rust*` | cache (mise) | `mise install` verifies the aqua tools' (actionlint, zizmor, shellcheck) sigstore attestations and refreshes sigstore's TUF trust metadata here; mise's own process does not see the project `XDG_CACHE_HOME` | `rm -rf ~/.cache/sigstore-rust` (a cache: refetched on the next verification) | keep |
| `~/.local/state/mise/trusted-configs/user-teams-bridge-*` | mise state | `mise trust` entry for this repository's `mise.toml` | `mise trust --untrust /home/user/teams-bridge/mise.toml` | keep |
| `~/.config/go/telemetry/local/*@go1.27.3-*.count` | noise | Pre-existing Go telemetry counters (born before our stamp). `mise install` runs `go version` without the project env, which bumps them; inside the project env Go writes to `.xdg/config/go/telemetry` | none (not ours) | noise |
| `/tmp/hsperfdata_user` | noise | Pre-existing HotSpot perf-data dir (born before our stamp). Its mtime is touched by the `java -version` that `mise install` runs; our own JVMs run with `-XX:-UsePerfData` | none (not ours) | noise |
| `/tmp/.tmp??????` | noise (mise) | `mise run` writes each task script to a transient Rust `tempfile` in the system temp dir (mise's own process does not see the project `TMPDIR`); it is gone when the task ends, but the `mise run check-outside` that is checking sees its own | none (transient) | noise |
| `/tmp/systemd-private-*` | noise (system) | root-owned `PrivateTmp=` dirs that systemd creates whenever a system service restarts | none (not ours) | noise |
| `/var/tmp/systemd-private-*` | noise (system) | the same, under `/var/tmp` | none (not ours) | noise |
| `/run/user/1000/dconf/user` | ours (tmpfs) | dconf runtime file created by the first launch of `.tools/edge` (before the wrapper set `GSETTINGS_BACKEND=memory`). Not scanned by check-outside (`/run`), recorded from the review | `rm -f /run/user/$(id -u)/dconf/user && rmdir /run/user/$(id -u)/dconf 2>/dev/null; true` | run |
| `systemd --user: at-spi-dbus-bus.service` | ours (process) | the accessibility bus, D-Bus-activated by the first launch of `.tools/edge` (before the wrapper set `NO_AT_BRIDGE=1`) | `systemctl --user stop at-spi-dbus-bus.service` | run |
| `/run/user/*/systemd/user/tbd-test-*` | transient (tmpfs) | Linux install round-trip test: `systemctl --user --runtime link/enable` of a never-started unit; removed by the test itself. The removal sweeps leftovers of an interrupted run | `for u in /run/user/$(id -u)/systemd/user/tbd-test-*.service; do [ -e "$u" ] && systemctl --user --runtime disable "$(basename "$u")"; done; systemctl --user daemon-reload` | run |
| `schtasks:\tbd-test-*` | Windows task | install/uninstall round-trip test (BRIEF §1.5/§7): created and deleted in the same test. check-outside-win fails if one is still there; the removal sweeps leftovers | `cd "$TBD_WIN_SANDBOX" && schtasks.exe /query /fo csv /nh \| tr -d '\r' \| cut -d, -f1 \| tr -d '"' \| command grep '^\\tbd-test-' \| while read -r t; do schtasks.exe /delete /tn "$t" /f < /dev/null; done; true` | run |
| `%TEMP%\wsl-crashes\wsl-crash-*_home_user_teams-bridge_*` | ours (files) | WSL core dumps (~30 MB each) of `.tools/edge` processes that crashed during tests. The wrapper now sets RLIMIT_CORE=1 (`prlimit`), which stops WSL's crash capture | `rm -f /mnt/c/Users/user/AppData/Local/Temp/wsl-crashes/wsl-crash-*_home_user_teams-bridge_*` | run |
| `%TEMP%\wsl-crashes` | noise (WSL) | the WSL crash-dump folder itself (it pre-dates our runs) | none | noise |
| `registry:*HKEY_CURRENT_USER\Software\Microsoft\Edge\PreferenceMACs\tbd*` | registry (ours) | Host Edge mirrors the preference MACs of our profile `tbd` here, whatever `--user-data-dir` says (D14) | `scripts/win/clean-edge-registry.sh` | run |
| `registry:*HKEY_CURRENT_USER\Software\Microsoft\Edge\PrefsEnclave*` | registry (ours, partly) | Host Edge adds one `ev_<hash>` value per profile path it opens. `test-win` records the names our browsers created in `snapshots/ours.prefsenclave.txt`; the script above deletes exactly those | `scripts/win/clean-edge-registry.sh` (same run as above) | noise |
| `registry:*HKEY_CURRENT_USER\Software\Microsoft\Edge\PreferenceMACs\Default*` | registry (Edge) | Overwritten once by the first `test-win` run, before the profile got its own name (D14). Report-only copy of the user's Edge profile MACs (Chromium uses it for metrics, not enforcement); the user's Edge rewrites it on its next start | none | noise |
| `registry:*HKEY_CURRENT_USER\Software\Microsoft\Edge\BLBeacon*` | registry (Edge) | Edge's launch beacon, written by every msedge.exe start (shared with the user's Edge) | none (shared) | noise |
| `registry:*HKEY_CURRENT_USER\Software\Microsoft\Edge\ElfBeacon*` | registry (Edge) | Edge's early-launch beacon, same | none (shared) | noise |
| `registry:*HKEY_CURRENT_USER\Software\Microsoft\Edge\StabilityMetrics*` | registry (Edge) | `exited_cleanly` flag, written at every start/exit (shared) | none (shared) | noise |
| `registry:*HKEY_CURRENT_USER\Software\Microsoft\EdgeUpdate*` | registry (Edge) | msedge.exe records `lastrun`/metrics ids for Edge Update at every start; EdgeUpdate's own scheduled tasks write here too (shared) | none (shared) | noise |
| `registry:*HKEY_CURRENT_USER\Software\Microsoft\Edge\Profiles\Default*` | registry (Edge) | The user's own Edge profile `Default` (a `MUID` value appeared while the user's Edge was running; our browsers use the profile `tbd`, D14) | none (not ours) | noise |
| `%LOCALAPPDATA%\Packages\cr.sb.*` | noise (Edge) | Chromium's sandbox AppContainer profile (`cr.sb.<hash of the browser>`), created once by Edge for its sandboxed processes and shared with the user's own Edge; it pre-dates our runs, they only touch it | none (shared) | noise |
| `%LOCALAPPDATA%\Microsoft\OneAuth*` | noise (Edge/host) | Microsoft identity library used by Edge and Office; touched by an msedge.exe start (less since implicit sign-in is off, D14) | none (shared) | noise |
| `%LOCALAPPDATA%\Microsoft\TokenBroker*` | noise (host) | Windows Web Account Manager token cache | none (not ours) | noise |
| `%LOCALAPPDATA%\Microsoft\Credentials*` | noise (host) | Windows Credential Manager (WAM/TokenBroker, OneAuth) | none (not ours) | noise |
| `%APPDATA%\Microsoft\Crypto\Keys*` | noise (host) | Windows CNG/DPAPI key store (also used by Edge's OSCrypt) | none (not ours) | noise |
| `%APPDATA%\Microsoft\Windows\Themes*` | noise (host) | Windows theme/wallpaper cache | none | noise |
| `%APPDATA%\Microsoft\Installer` | noise (host) | Windows Installer bookkeeping | none (not ours) | noise |
| `%LOCALAPPDATA%\Microsoft\Windows\Caches*` | noise (host) | Windows caches | none | noise |
| `%LOCALAPPDATA%\Microsoft\Windows\Explorer*` | noise (host) | Explorer icon/thumbnail caches | none | noise |
| `%LOCALAPPDATA%\Microsoft\Windows\ActionCenterCache*` | noise (host) | Windows notification centre | none (not ours) | noise |
| `%LOCALAPPDATA%\Microsoft\Windows\UsrClass.dat*` | noise (host) | Transaction logs of the HKCU\Software\Classes hive | none | noise |
| `%USERPROFILE%\ntuser.dat*` | noise (host) | Transaction logs of the user registry hive; change whenever any app writes HKCU | none | noise |
| `%LOCALAPPDATA%\D3DSCache*` | noise (host) | Direct3D shader cache, written by any GPU-using app | none (not ours) | noise |
| `%LOCALAPPDATA%\wsl*` | noise (host) | WSL's own `ext4.vhdx`: every write inside WSL lands in it | none | noise |
| `%TEMP%\????????-????-????-????-????????????\swap.vhdx` | noise (WSL) | WSL 2's swap disk (`%TEMP%\<GUID>\swap.vhdx`), written whenever the WSL VM swaps | none (WSL) | noise |
| `%TEMP%` | noise (host) | the TEMP directory itself (its mtime changes whenever anything creates a temp file) | none | noise |
| `%TEMP%\????????-????-????-????-????????????.tmp` | noise (host) | transient GUID-named temp files of a background app; they disappear within minutes | none (not ours) | noise |
| `%TEMP%\????.tmp*` | noise (host) | transient `XXXX.tmp` files and folders of background apps (pre-existing ones are touched) | none (not ours) | noise |
| `%TEMP%\notificationimages*` | noise (host) | Windows toast images | none (not ours) | noise |
| `%TEMP%\scoped_dir*` | noise (host) | Chromium/Electron apps on the host create `scoped_dir<pid>_*`. Our Edge runs with TEMP/TMP inside the sandbox; `test-win` separately fails on a new one whose process has exited | none (not ours) | noise |
| `%TEMP%\Microsoft.PackageManagement*` | noise (host) | PowerShell's PackageManagement module cache, touched by any `powershell.exe` start (including our snapshot script) | none | noise |
| `%TEMP%\MicrosoftEdgeUpdate.log` | noise (Edge) | Edge Update's own log | none (shared) | noise |
| `%TEMP%\cv_debug.log` | ours (real daemon, before D19) | Edge's correlation-vector log: the real daemon's Edge wrote it into the user's TEMP until D19 gave tbd's browser `<home>\tmp` (where it now appears). Edge recreates it on demand, so removing it is safe even if the user's own Edge writes one | `rm -f /mnt/c/Users/user/AppData/Local/Temp/cv_debug.log` | run |
| `%LOCALAPPDATA%\CrashDumps` | noise (host) | Windows Error Reporting's local dump folder itself | none | noise |
| `%LOCALAPPDATA%\Microsoft\Edge\User Data` | noise (host) | The user's own Edge, running on the host, touches its profile folder; our browsers never use it (`--user-data-dir`) | none (not ours) | noise |
| `%LOCALAPPDATA%\ConnectedDevicesPlatform*` | noise (host) | Windows' Connected Devices Platform service (Phone Link, nearby sharing) keeps its state here | none (not ours) | noise |
| `%LOCALAPPDATA%\Microsoft\Terminal Server Client\Cache*` | noise (host) | Remote Desktop client (mstsc/msrdc) bitmap cache | none (not ours) | noise |
| `%LOCALAPPDATA%\tbd*` | yours (production) | The daemon's default data dir on Windows when tbd is set up for real: `config.toml`, the FCM service-account key `fcm.sa.json`, the browser profile, `state.json`, `tbd.log`. Created on purpose by a real setup, never by tests | `Remove-Item -Recurse "$env:LOCALAPPDATA\tbd"` (only if you stop using tbd; it holds your pairing and the key) | keep |
| `%APPDATA%\Microsoft\Windows\Recent*` | noise (host) | Windows "recent items" | none (not ours) | noise |
| `%LOCALAPPDATA%\Microsoft\Windows\WebCache*` | noise (host) | Windows' shared web cache (browser downloads) | none (not ours) | noise |
| `%LOCALAPPDATA%\Programs\tbd*` | yours (production) | the installed `tbd.exe` of a real setup (the README's suggested location) | `Remove-Item -Recurse "$env:LOCALAPPDATA\Programs\tbd"` (after `tbd uninstall`) | keep |
<!-- END outside-table -->

## apt packages

None: Edge runs from `.tools/edge/` without any missing library (D3).

## Windows host

Besides the rows above, only `C:\Users\user\Projects\teams-bridge` (`TBD_WIN_SANDBOX`), which the user
deletes by hand. Snapshots, diffs and test logs live in it (`snapshots/`, `logs/`).
