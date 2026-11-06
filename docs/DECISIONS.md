# Decisions

Every non-obvious decision taken while implementing BRIEF.md, newest section last. Where the brief
turned out to be wrong against reality, the entry says so and what was done instead.

## D1 — Toolbox and versions (step 1)

* Exact pins in `mise.toml` for reproducibility: Go 1.27.3 (latest stable on 2026-11-06), Temurin
  17.0.21+9, Gradle 9.8.2, staticcheck 0.8.2 via the `go:` backend. No Node: nothing needs it.
* No Gradle wrapper is committed. mise is the only sanctioned toolbox, so the Android build runs the
  mise-provided `gradle`; a wrapper would add a second toolchain download path and a binary jar.
* `GOTOOLCHAIN=local` (never auto-download another Go), `GOFLAGS=-modcacherw` (the module cache inside
  `.xdg/` stays deletable with `rm -rf`).

## D2 — Hygiene redirects beyond BRIEF §1.1

* `XDG_STATE_HOME` is redirected as well.
* Because `XDG_*` point into the repo, a *nested* `mise` (one started from inside a task) would look for
  its data, config and trust state there and try to reinstall every tool. `MISE_DATA_DIR`,
  `MISE_CACHE_DIR`, `MISE_CONFIG_DIR`, `MISE_STATE_DIR` are therefore pinned to mise's real locations.
* `JAVA_TOOL_OPTIONS=-XX:-UsePerfData -Djava.io.tmpdir=$ROOT/.tmp`: HotSpot ignores `TMPDIR`; it always
  writes `/tmp/hsperfdata_<user>` and defaults `java.io.tmpdir` to `/tmp`. This is a `/tmp` fix, not
  the `user.home` last resort of §1.1 (not needed so far).
* `check-outside` uses `-newer` **or** `-cnewer` (an extracted file can carry an old mtime), ignores
  directories whose only change is an excluded child, excludes the agent's own `/tmp/claude-*` files
  next to `~/.claude*`, and treats rows of `OUTSIDE.md` as acknowledged, so `OUTSIDE.md` is the single
  list of everything outside the repo. Its `mise run` wrapper's own transient `/tmp/.tmpXXXXXX` (mise
  writes the task script there before our `TMPDIR` applies) is such a row.
* `mise install` runs `go version` / `java -version` *without* the project env, which touches two
  pre-existing paths (Go telemetry counters, `/tmp/hsperfdata_user`). Both were born before the stamp;
  they are recorded as noise, not removed (not ours).

## D3 — Edge in WSL without apt (BRIEF §1.4)

* `mise run edge` downloads the newest `microsoft-edge-stable` from packages.microsoft.com, verifies
  the SHA-256 from the signed `Packages` index and `dpkg -x`es it into `.tools/edge/`.
* `ldd` reports only Qt5/Qt6 missing, and only for `libqt5_shim.so` / `libqt6_shim.so`, which Edge
  loads solely for a Qt desktop UI. No apt package is needed; the script would stop and list
  packages otherwise.
* `.tools/edge/tbd-edge` is a dev-only wrapper that sets `HOME=$ROOT/.xdg/home` (plus XDG defaults)
  before exec'ing Edge: Chromium creates `~/.pki/nssdb` from `$HOME`, which no command-line switch
  avoids. The daemon itself does **not** override `HOME` for the browser in production: the user's
  NSS database may hold corporate CA certificates that Teams needs behind a TLS-inspecting proxy.

## D4 — Windows host checks (BRIEF §1.5)

* Snapshot depth: `%USERPROFILE%` and `%TEMP%` one level, `%LOCALAPPDATA%` and `%APPDATA%` three
  levels (deep enough to see e.g. `LOCALAPPDATA\Microsoft\Edge\User Data`), each entry with type,
  size and mtime; the scheduled-task list by name only (the other CSV columns change on their own).
  A full snapshot takes ~2 s.
  `ext4.vhdx` write continuously. Their paths are acknowledged as host noise in `OUTSIDE.md`; anything
  else in a diff fails `check-outside-win`. Chromium's `scoped_dir<pid>_*` in `%TEMP%` is noise from
  the Electron apps, so `test-win` checks separately that none carries one of *our* browser PIDs.

## D5 — Windows autostart: XML task, not `/sc onlogon` (brief corrected)

* Measured on this host: `schtasks /create /sc onlogon … /rl limited` fails with *Access is denied*
  from a non-elevated (medium integrity) process, even though the user is an administrator under UAC.
  A logon trigger for *any* user needs elevation.
* `tbd install` therefore uses `schtasks /create /tn <name> /xml <file> /f` with a task definition
  whose `LogonTrigger` and `Principal` name the current user (`InteractiveToken`, `LeastPrivilege`).
  This works unelevated (probed once with a task `tbd-test-probe<random>`, created and immediately
  deleted). The XML also sets `ExecutionTimeLimit=PT0S` — the default of 72 h would kill the daemon
  after three days — `MultipleInstancesPolicy=IgnoreNew`, restart on failure, and no battery
  conditions.
* Hidden window: the action is `conhost.exe --headless "<tbd.exe>" run`. Built into Windows 10 1809+
  and 11, no helper file (VBS/PowerShell) to drop, and no window even when Windows Terminal is the
  default console host. Building tbd.exe as a GUI-subsystem binary would hide the window too but
  would also swallow the output of every interactive command (`tbd pair`, `tbd status`).

## D6 — Protocol details within the frozen §3

* The code decoder accepts only canonical base32 (re-encoding must reproduce the input), so each
  secret has exactly one code; it also ignores spaces and an optional `tbd:v1:` prefix, so a pasted QR
  text works.
* Truncation order when a document exceeds 2046 bytes: body (rune-wise, longest fitting prefix + `…`),
  then title, then drop tag, url, kind. Never splits UTF-8.
* JSON is emitted without HTML escaping (`&` in URLs would cost 5 extra bytes each).
* Receivers are strict about padding (size, declared length, smallest size, zero bytes); the AEAD
  already authenticates it, strictness just keeps both implementations honest.
* An empty `url` means "open `https://teams.microsoft.com/v2/`".
* The vector secret is the public constant `00112233…eeff`; the vectors' nonces are fixed. A Go test
  fails if `proto/vectors.json` is not byte-identical to the generator output. The vectors were
  checked once against an independent Python implementation (HKDF via `hmac`, `cryptography`'s
  ChaCha20Poly1305).

## D7 — Configuration

* On Linux the default data dir honours `$XDG_DATA_HOME` (`$XDG_DATA_HOME/tbd`, else
  `~/.local/share/tbd`); on Windows `%LOCALAPPDATA%\tbd`.
* Keys beyond the brief: `browser_args` (extra switches), `api_hosts` (host patterns for the 401/403
  detector, default `*.teams.microsoft.com`, `*.teams.cloud.microsoft`, where `*.x` also matches `x`),
  `report_hidden` (default true: the page believes it is in the background, BRIEF "configurable"),
  `fcm_endpoint` (tests). Unknown keys are an error, so a typo does not silently disable something.
* `config.toml` is written atomically with mode 0600 (it holds the secret).
* Browser version: `--version` on Linux. On Windows `msedge.exe --version` opens a browser window
  instead of printing, so the version comes from the version-named directory next to `msedge.exe`
  (`Application\157.0.4341.42\`); the daemon cross-checks with CDP `Browser.getVersion` after launch.
