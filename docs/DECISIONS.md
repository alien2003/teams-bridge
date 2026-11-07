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
  the SHA-256 from the `Packages` index (fetched over HTTPS only; the index signature is not
  checked) and `dpkg -x`es it into `.tools/edge/`.
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
* The host is a live desktop with background apps; they and the WSL `ext4.vhdx` write
  continuously. Their paths are acknowledged as host noise (generic Windows components in
  `OUTSIDE.md`, the apps of one particular machine in its git-ignored `OUTSIDE.local.md`, D21);
  anything else in a diff fails `check-outside-win`. Chromium's `scoped_dir<pid>_*` in `%TEMP%` is
  noise from Chromium/Electron apps, so `test-win` checks separately that none carries one of *our*
  browser PIDs.

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
  (`Application\157.0.4341.42\`). The daemon reads it again before every launch and cross-checks
  with CDP `Browser.getVersion` after attaching; if the running browser differs (an auto-update, or a
  version staged next to a still-old msedge.exe) it relaunches once with the running version's UA.

## D8 — Browser, hook and fake Teams (step 3)

What Ferdium taught (read in `.cache/ref/`, re-implemented, not copied): its Teams recipe overrides
the user agent to a desktop Chrome/Edge string, because Teams web refuses or degrades unknown
browsers; its webview preload replaces `window.Notification` with a class that forwards
`title`/`options` to the host and keeps `onclick`/`onclose` so a click can be replayed into the page;
badge counts come from the `(N)` prefix of `document.title`. tbd keeps the same three ideas and
moves them to CDP: `Page.addScriptToEvaluateOnNewDocument` + `Runtime.addBinding` instead of an
Electron preload/IPC.

* **Browser-wide UA switch.** `LaunchOptions.UserAgent` → `--user-agent=<UA>` (`browser.EffectiveUserAgent`:
  config `user_agent`, else the templated Windows Edge UA). Measured: without it the service worker's
  main-script fetch and first requests carry `X11; Linux x86_64 … HeadlessChrome/157`; the per-target
  CDP override cannot reach them because the worker target does not exist yet. Client hints come from
  the per-target `userAgentMetadata`. If the version is unknown before launch, the daemon asks CDP and
  relaunches once with the switch.
* **UA metadata.** Brands `Microsoft Edge`, `Chromium`, GREASE `"Not=A?Brand";v="24"`; platform
  `Windows` `19.0.0` (Windows 11 24H2/25H2), arch `x86`, bitness `64`, not mobile, no WoW64.
  `FullVersionList` uses the Edge build for both brands (the Chromium build is not known locally).
* **Sandbox stays on.** No default `--no-sandbox`/`--disable-dev-shm-usage` (Edge runs sandboxed in this
  WSL). Only `--disable-gpu` is added headless on non-Windows. A container without user namespaces sets
  `browser_args = ["--no-sandbox"]`; Launch suggests it when Edge reports "No usable sandbox".
* **Process lifetime.** Linux: own process group + `Pdeathsig=SIGKILL`. Windows: a job object with
  `KILL_ON_JOB_CLOSE`, so a crashed daemon cannot leave a headless Edge holding the profile lock;
  `taskkill.exe /F /T` from System32 with `CREATE_NO_WINDOW` as fallback. `Close` waits ≤ 10 s after
  `Browser.close`. A stale `DevToolsActivePort` is deleted before launch; stderr is read from an owned
  pipe to EOF so the last lines (e.g. a sandbox error) are not lost.
* **One tab.** chromedp's remote allocator opens a new tab in a new window; `openPage` attaches to the
  browser's existing page instead (otherwise `tbd login` showed two windows and never returned).
* **Service workers.** Only http(s) `service_worker` targets are hooked (dedicated workers inherit the
  page override); `Runtime.addBinding("__tbd")` on the worker target, `WithDetachOnCancel` (chromedp
  would otherwise *close* Teams' worker). A bounded in-worker queue drained every 1 s is the fallback
  when the binding is missing. Workers are not paused at start (chromedp v0.23 has no handle on the
  auto-attached session), so a notification a worker raises before it is hooked is missed: best effort.
* **Transport vs source.** `OnBinding(transport, payload)`: transport is how the report arrived
  (page/worker); the payload's `source` is page-controlled and informational only.
* **Disconnects.** `OnDisconnect` fires once for a lost CDP connection, a crashed renderer or a detached
  page; never for `Close`.
* **Preload config.** Go prepends `window.__tbdConfig={"hidden":<bool>};`; the preload reads and deletes it.
* **Untrusted payloads.** `hook.Parse` caps the payload at 64 KiB, requires an object with a known
  `kind`, strict types, truncates title/body/tag, drops oversized icon/data (`DataDropped`), replaces
  invalid UTF-8. preload.js keeps reports under 63 KiB and sends `dataError:true` instead of losing a
  notification whose `data` does not serialise (cycles, BigInt).
* **Title count.** Top-level document only; the whole `<head>` is observed; `(99+)` → 99.
* **Deep links** (Microsoft's deep-link doc, fetched 2026-11-06): chat id + message id →
  `/l/message/<chatId>/<messageId>?context={"contextType":"chat"}`; channel + message id →
  `/l/message/<channelId>/<messageId>`; chat id only → `/l/chat/<chatId>/conversations`; otherwise
  `teams_url`. Always on the `teams_url` origin. The PWA manifest is not reachable anonymously
  (`/v2/manifest.json` is 404), so whether `/l/…` is inside the PWA scope is **unverified**.
* **DescribeData** (info logs): ≤ 32 keys with value types/lengths, id-like keys masked; the raw data is
  logged only at debug.
* **Login "signed in"** = the main frame stayed on the Teams origin (not a login host) for 10 s (Teams
  commits `/v2/` before redirecting a signed-out user). Closing earlier → `ErrNotSignedIn`. Login uses
  the daemon's UA so Conditional Access sees the same platform.
* **faketeams** raises Alice (window), Bob (sw-page) and Carol (sw-worker) exactly once each, reports
  every raised notification, serves a dedicated worker and a service worker that make their own
  requests (UA of worker traffic is asserted), a 401 mode, a redirect to `localhost` (the fake login
  host, a different host on the same server) with a sign-in form, and `Notify()` on demand.
  `127.0.0.1`/`localhost` are secure contexts, so service workers work over plain http.

## D9 — FCM client (step 4)

* JWT RS256 by hand (`kid` = `private_key_id`, `iss`/`scope`/`aud`/`iat`/`exp=iat+3600`), form POST
  `grant_type=urn:ietf:params:oauth:grant-type:jwt-bearer`; token cached until 5 min before expiry
  (wall clock: the monotonic clock stops during suspend), one shared exchange for concurrent senders.
* Send: `POST {fcm_endpoint}/v1/projects/{id}/messages:send`, body only from `proto.FCMRequest`.
  401 → drop the token, retry once with a fresh one; 429 → Retry-After or 60 s; 5xx/network → backoff
  from 10 s doubling to 60 s, 3 attempts (Google's scale-fcm guidance); 400/403/404 never retried.
  Typed `*fcm.Error` with sentinels (`ErrUnauthenticated`, `ErrNotFound`, `ErrQuota`, …).
* `token_uri`/`fcm_endpoint` must be https except on loopback (the mock); redirects are not followed
  (they would resend the bearer token); `HTTPS_PROXY` is honoured. Keys, assertions, tokens, topic and
  payload never appear in logs or errors.
* **Possible 2048-byte topic limit.** FCM's ErrorCode reference says data may be "4096 bytes for most
  messages, or 2048 bytes in the case of messages to topics". A 2048-padded tbd message carries 2769
  bytes. Without credentials this cannot be verified, so the daemon sends through
  `fcm.Client.SendMessage`, which on a 400 for a 2048-padded payload re-seals it once truncated to the
  1024 size (same id). Receivers accept both sizes; the frozen protocol is unchanged. **To verify with
  real credentials** (README troubleshooting).
* `fcmtest` is independent of the client: strict JWT validation against the registered public key,
  exact §3 body shape, Google-style error JSON, scripted 401/404/429/5xx, decryption of what it
  receives. Service-account keys are generated at run time; none is committed.

## D10 — Autostart (step 5)

* The service command line is always `tbd run --home <resolved home>`: neither the systemd user manager
  nor Task Scheduler sees the installer's `TBD_HOME`/XDG environment.
* **Unit dir = the one the user manager searches** (its environment via `systemctl --user
  show-environment`), not the installer's `XDG_CONFIG_HOME` — measured: the manager's UnitPath ignores
  the mise redirect, so a unit written there would never be found.
* **No `network-online.target`** (brief corrected): it is `not-found` in a user manager. The daemon
  retries on its own.
* Unit: `Type=exec`, `Restart=always`, `RestartSec=10`, `KillMode=mixed`, `TimeoutStopSec=30` (SIGTERM
  reaches only tbd, which sends `daemon_stopping` and closes Edge itself). Install = `daemon-reload`,
  `enable`, `restart` (a re-install picks up the new binary); uninstall = `disable --now`, remove,
  `daemon-reload`.
* **WSL** (brief corrected): `tbd install` prints `loginctl enable-linger`, an *unelevated* PowerShell
  `Register-ScheduledTask` (logon trigger for the current user) that runs `conhost.exe --headless
  wsl.exe -d <distro> --exec sleep infinity` — systemd stays the only supervisor, the brief's `wsl.exe
  -- tbd run` would start a second daemon on the same profile — and `.wslconfig` `[general]
  instanceIdleTimeout=-1` (`vmIdleTimeout` only applies after all distros stopped).
* **Windows** (D5): XML task, `%SystemRoot%\System32\conhost.exe --headless "<exe>" run --home <home>
  --log-file <home>\tbd.log` (under a headless console stderr goes nowhere; the log file rotates at
  10 MiB while running, one `.1` generation is kept, and fatal startup errors are written to it too).
  RestartOnFailure count 255 (the schema maximum). Install also runs `schtasks /run`.
* Round-trip tests: Linux uses `systemctl --user --runtime link/enable` (symlinks in tmpfs
  `$XDG_RUNTIME_DIR`, never started, `~/.config/systemd` asserted unchanged) plus `systemd-analyze
  verify`; Windows creates/queries/deletes `tbd-test-<random>` (action `whoami.exe`, never run;
  existence judged by exit status so a parse failure can never leave the task behind).

## D11 — Session detectors (step 5)

* (1) Login navigation: a main-frame navigation to a login host (or subdomain) starts a 30 s grace
  timer and fires only if the frame has not come back to Teams — Teams passes through
  `login.microsoftonline.com` on every normal start (silent SSO). A Teams ⇄ login ping-pong (4 bounces
  in 5 min) fires at once.
* (2) 401/403 bursts: **the brief's rule** — three within 60 s from `api_hosts`, API requests only (XHR,
  Fetch, EventSource, or `/api/` paths). The session builder had added "a 2xx from the same host clears
  the count"; with Teams' background traffic (and the fake page's) that made the detector never fire,
  so it was replaced by *coalescing*: failures less than 1 s apart count once, so parallel requests
  that hit an expiring token together (then refreshed silently) are not an expiry.
* (3) Probe (`probe.js`): main frame off Teams, on a login host, or a visible sign-in prompt →
  expired + re-navigate (keep retrying); about:blank/chrome-error → re-navigate without alert (an
  outage is not an expiry); 3 evaluation errors in a row → restart the browser. The sign-in heuristics
  are conservative (Entra ID fields, account picker, "Sign in" buttons only in dialogs or sparse
  pages; English and Russian) and were tested on 21 local pages in real Edge; **to verify against real
  Teams** (what its own signed-out UI looks like).
* (4) Browser lost: restart only. `browser_dead` comes from the FailureTracker after 3 failed runs in a
  row (launch/attach error, death within 3 min, or unresponsive); backoff 2 s doubling to 5 min, ±20 %.
* One debouncer (6 h per kind) inside the Detector and the FailureTracker; the daemon persists the
  last alert times in `state.json` and seeds the debouncer on start, so a crash loop under
  `Restart=always` does not re-send the same alert.
* Heartbeat: once at start, then every `heartbeat_hours`, checked against the wall clock every minute
  (a heartbeat goes out within a minute after resume), failed sends retried after 5 min.

## D12 — Android app (step 6)

* **Versions (primary sources, 2026-11-06):** AGP 9.5.1 (needs Gradle ≥ 9.6 and JDK 17 — the pins
  work), Kotlin 2.4.21 via AGP 9's built-in Kotlin, compileSdk 37.2 / targetSdk 37 / build-tools
  37.0.0, minSdk 28, Firebase BoM 35.1.0 + google-services 4.5.0, zxing-android-embedded 4.3.0 (last
  release 2021, a maintenance risk), work-runtime 2.12.0, Material 1.14.0; test-only JUnit 4.13.2 and
  org.json (android.jar's org.json is a stub on the JVM). cmdline-tools 22.0, SHA-256 and SHA-1 checked.
* **Brief corrected: no `ANDROID_SDK_HOME`.** AGP 9.5 fails when it equals `ANDROID_USER_HOME` (the
  deprecated variable names the *parent* of `.android`). Removed from `mise.toml`; the Android
  scripts also unset it defensively.
* No Kotlin daemon (`in-process`), no KGP metrics, AGP analytics opted out, no SDK auto-download, no
  Gradle JDK auto-provisioning; `-Duser.home` was not needed (check-outside clean).
* **Debug APK size:** unminified 19.7 MB (Material/AppCompat, WorkManager+Room, Firebase); debug is
  built with R8 shrinking (no optimisation/obfuscation, readable traces), compressed dex, English
  resources only → 3.07 MB.
* **Storage:** `security-crypto` is deprecated, so the secret is wrapped by a non-exportable Keystore
  AES-256-GCM key (no user auth, so FCM can decrypt while locked); K and the topic are derived on
  demand. No backup/device transfer. A *permanently* lost key unpairs; a transient Keystore error keeps
  the pairing and drops the message.
* **Tap:** a no-UI, non-exported trampoline Activity opens the URL with `setPackage(chosen)` and falls
  back to a toast + chooser on `ActivityNotFoundException` (a system-fired PendingIntent cannot be
  caught; Android 12's trampoline ban targets services/receivers). Only absolute http(s) URLs without
  userinfo; anything else opens `https://teams.microsoft.com/v2/`. A "System default" choice exists.
* **Handler list:** queried for `https://teams.microsoft.com/` and `…/v2/` (a PWA scoped to `/v2/`);
  a resolved filter with a host → "PWA", `com.microsoft.teams` → "Teams app", else "Browser".
* **Notifications:** msg grouped per tag with a summary; alerts one per kind, ongoing + auto-cancel;
  "Paired ✓" once per pairing. Android drops new notifications above 50 per app, so the app keeps at
  most 40 `tbd.msg` notifications (oldest withdrawn first).
* **QR polarity:** `tbd pair` draws light modules on the (usually dark) terminal, an inverted code;
  the scanner runs zxing's `MIXED_SCAN` (normal + inverted). `tbd pair --invert` draws dark modules.
* FCM: token callbacks are still the SDK default (the FID registration mode is opt-in and unverified
  without a device); both `onNewToken` and `onRegistered` re-subscribe. Firelog delivery-metrics
  export is disabled in the manifest.
* The placeholder `google-services.json` uses an API key that passes Firebase's format check but not
  the secret scanner's pattern; with `project_id` `tbd-placeholder` the app shows a banner.

## D13 — Daemon integration (steps 4–5)

* `internal/daemon` (not in the brief's layout) holds the wiring so that the in-process E2E tests and
  `cmd/tbd` share it; `cmd/tbd` stays a thin CLI.
* **Single instance:** an advisory lock on `<home>/tbd.lock` (flock / LockFileEx). Two daemons — or a
  daemon and `tbd login` — on one profile would make Edge hand off to the other instance.
* **state.json** (no content): last heartbeat/message/capture, unread count, session OK, last alert per
  kind, last error; read by `tbd status`.
* Notifications are queued (256) so CDP goroutines never block on FCM. Identical title+body+tag within
  10 s are sent once (Teams may raise one message through both the page and the worker).
* `run --once` sends no heartbeat and no `daemon_stopping`; exit 0 after the first delivered `msg`,
  exit 3 on timeout. Without `--once`, a timeout or signal sends `daemon_stopping`.
* `tbd pair` shows the existing code unless `--new`; it sends a `pair` ping every 20 s for up to
  `--ping-for` (10 min) or until Enter (EOF on stdin does not stop it).
* `--home` and `--log-level` are accepted before and after the command (`tbd run --home …` is the
  service command line).

## D14 — Windows host findings (step 5, measured with `check-outside-win` and single-launch experiments)

* **Host Edge writes outside `--user-data-dir` (brief corrected).** One headless `msedge.exe` run with
  a fresh profile in the sandbox and TEMP redirected still writes per-user state: the profile's
  preference MACs under `HKCU\Software\Microsoft\Edge\PreferenceMACs\<profile name>`, one
  `PrefsEnclave` value, `StabilityMetrics`, `BLBeacon`/`ElfBeacon`, `EdgeUpdate\ClientState` lastrun and
  metrics ids, and it touches `%LOCALAPPDATA%\Microsoft\OneAuth` and its sandbox AppContainer profile
  `%LOCALAPPDATA%\Packages\cr.sb.*`. With "host Edge used as is" this cannot be switched off.
* **`--profile-directory=tbd`.** With the default profile name `Default`, the first `test-win` run
  overwrote the MACs of the *user's own* Edge profile in the registry (a report-only copy; Chromium's
  tracked-preference enforcement uses the Secure Preferences file, and the user's Edge rewrites it on
  its next start). Every launch now uses the profile name `tbd`, so Edge writes a key of our own,
  which `scripts/win/clean-edge-registry.sh` (run by `clean-outside`) deletes together with the
  `PrefsEnclave` values that are not in the baseline. The shared bookkeeping is listed in OUTSIDE.md
  as Edge noise.
* **Edge's implicit sign-in is off by default.** Measured: a fresh Edge profile on Windows signs itself
  in to the Windows account (OneAuth/WAM) and opens `edge://sync-confirmation-dialog/` — an extra page
  target (it broke the single-page tests) and a sync-consent dialog in `tbd login`. Edge's feature
  `msImplicitSignin` (found in msedge.dll) controls it; `--disable-features=msImplicitSignin` stops it
  (no account in the profile, no dialog). It is added to every launch and merged into a user's own
  `--disable-features` in `browser_args` (Chromium honours only the last occurrence of a switch).
  `implicit_signin = true` in config.toml re-enables it for Entra-joined PCs whose Conditional Access
  relies on Edge's OS-account SSO.
* **Windows byte-range locks are mandatory.** The single-instance lock now covers one byte far past
  the end of `tbd.lock`, so `tbd status` can still read the PID written at offset 0.
* **Task Scheduler + `conhost --headless` argv verified.** An opt-in probe (`TBD_TEST_TASK_RUN=1`) ran
  the `tbd-test-*` task once: `%SystemRoot%` was expanded and the exact argv, including an exe path and
  a `--home` with spaces, reached the program; the task was then deleted.
* **test-win plumbing.** Test binaries run with stdin from `/dev/null` (a Windows exe reading the
  loop's stdin ended the loop after the first package); snapshot lists are compared after an
  `LC_ALL=C` sort (PowerShell's ordinal sort differs from the locale's).

## D15 — Fixes from the independent review (step 7)

A read-only review in five dimensions (brief compliance, protocol, security, runtime, hygiene), each
finding checked by a skeptical verifier, found 31 real issues; all are fixed:

* **WSL core dumps on the Windows side (hygiene).** Two crashing `.tools/edge` processes had WSL's
  crash handler write ~30 MB dumps each into the real `%TEMP%\wsl-crashes`. The dev wrapper now runs
  Edge under `prlimit --core=1` (measured: an RLIMIT_CORE of exactly 1 byte skips the core_pattern
  pipe; a shell's `ulimit -c 1` is 1 *block* and does not), sets `GSETTINGS_BACKEND=memory` and
  `NO_AT_BRIDGE=1` (Edge's first launch had created `/run/user/1000/dconf/user` and D-Bus-activated
  `at-spi-dbus-bus`); all three leftovers are `run` rows in OUTSIDE.md.
* **Outside-check matching is exact.** A row covered its path *and everything below it*, so the bare
  `%TEMP%` row accepted any file in the real TEMP. Rows are now globs matched exactly (descendants only
  with a trailing `*`); check-outside's own exclusions distinguish trees from raw prefixes; a leftover
  `tbd-test-*` task always fails; a baseline is taken only with `--baseline`; the post-run Windows
  snapshot sees the real `%TEMP%` (test-win had exported the sandbox TEMP to it). PrefsEnclave values
  are deleted only if test-win recorded them as created by our browsers.
* **`tbd login`** accepts every Teams origin (`teams_url`'s plus teams.microsoft.com /
  teams.cloud.microsoft / teams.live.com), and applies the same UA override *with client hints* as the
  daemon (before, the window sent `Sec-CH-UA-Platform: "Linux"` from WSL).
* **`tbd pair --new` refuses while the daemon runs** (it would keep sending with the old secret).
* **Flood control for page-originated input:** notifications are rate limited (burst 30, 1/s
  sustained), per-event log lines are throttled (first 10, then every 100th), dedup keys are hashed,
  the worker queue drain is bounded (64 reports per call, size-checked).
* **No secret in error messages:** a TOML syntax error reports the line and key only (the parser
  quotes the offending token, e.g. an unquoted secret); an unreadable `fcm_service_account` value is
  named only if it looks like a path (not JSON, PEM or a base64 blob); a leading BOM is ignored.
* **Alerts:** only a *delivered* alert is persisted for the cross-restart debounce; the heartbeat loop
  logs through the daemon's logger.
* **Protocol:** the Go reference filter compares Unix seconds (no `time.Unix` overflow on hostile
  `ts`); the Android receiver requires padded base64 like Go; the Kotlin docs no longer claim strict
  parity with Go's JSON decoder (Android's org.json is lenient; only authenticated plaintext reaches it).
* **`mise run test` vs `mise run test-win`** stay separate tasks (BRIEF §9 lists both): test-win needs
  the Windows host, takes minutes and flashes a login window; both must be green.
* The `MISE_*_DIR` pins of D2 had not actually landed in `mise.toml`; they are there now.
* **Windows executables inside `while read` loops** read the loop's stdin (WSL interop forwards it)
  and end the loop early — found three times (test-win, clean-outside, clean-edge-registry). Every
  `.exe` started in a loop now gets `< /dev/null`; the clean-up is verified by comparing value lists,
  not by another loop.

## D16 — Real Firebase project (2026-11-06)

* Project `teams-bridge` (sender <id>). `android/app/google-services.json` is the real file
  (git-ignored); the service-account key lives in the Windows daemon's data dir
  `%LOCALAPPDATA%\tbd\fcm.sa.json`, outside the repository, next to a minimal `config.toml`.
* **Measured with the real credentials:** the token exchange works, and FCM v1 *accepts* a topic
  message carrying 2769 bytes of data (the 2048-padded class) as well as a 1405-byte one — sent to a
  throwaway random topic with no subscribers, ciphertext under a random key. So the "2048 bytes for
  topics" sentence of the ErrorCode reference is not enforced at send time; the small-padding
  fallback (D9) stays as a safety net. Delivery of a long message to a phone is still to be seen.

## D17 — Deep link: resolve the conversation from the page (measured on the real Teams)

* **Measured, structure only** (masked shapes, names, counts; no text, no values), on this PC's
  real Teams session (a personal account: the page lives on `https://teams.live.com`):
  * Teams v2 raises every message notification with `new Notification(title, {body, icon,
    timestamp, tag, requireInteraction, dir})` — **no `data`, an empty `tag`**. Nothing in the
    notification names the chat, so BRIEF §5's "look in options.data and tag" finds nothing.
  * The `icon` is the sender's avatar, `…/api/mt/beta/users/<MRI>/profilepicturev2?displayname=…`,
    i.e. it carries the sender's id (`8:live:.cid.…`; `8:orgid:<GUID>` for work accounts).
  * In the DOM that id occurs 4× (an element's `aria-labelledby`, a `data-person-mri`, an avatar
    `img[src]`), every time inside a chat-list item whose `data-fui-tree-item-value` is the
    conversation id (`19:uni01_…@thread.v2`). Scanning the DOM took 3 ms (819 elements).
  * The page's WebSocket frames (Trouter) of the 30 s before the notification did **not** contain the
    sender id in plain form, so a network correlator would need more decoding for less certainty.
* **Design (as revised after the review below).** Shortly after the notification the preload reads
  the chat list: the **first** list item (`role` treeitem/option/listitem/row) that carries a
  conversation id and whose last-message preview shows this notification's body (equal, or
  `<name>: <body>`, or either side cut with an ellipsis, at least 8 characters kept) is the
  conversation. No such item → no conversation → the link is `teams_url`. The hook reports it as
  `conversation`; Go validates it (whole-id regex) and `DeepLink` uses it unless the notification's
  own tag/data name a conversation, giving `<origin>/l/chat/<id>/conversations?context=…`. Nothing
  is clicked, typed or navigated; the id is not logged (only its masked shape). The first design
  scored the sender's avatars and the chat names against the title; it was replaced (see below).
* **Origin.** Links are built on the origin the page actually lives on if it is the `teams_url`
  origin or one of the well-known Teams origins (teams.microsoft.com, teams.cloud.microsoft,
  teams.live.com), else on the `teams_url` origin; a foreign origin reported by the page is ignored.
* **Rejected:** replaying the notification's own `onclick` (it would navigate the daemon's page and
  mark the chat read at capture time); a WebSocket/response-body correlator (measured: the sender is
  not visible in the frames; it would also keep message content in memory); CSS-class selectors
  (unstable) — the resolver keys on attribute *values* that are conversation ids, not on names.
* The info line of a capture keeps the structure-only diagnostics (option names, page origin,
  whether the icon names a sender, the resolver's counts `resolve=items:N,matches:M` and the
  resolved id's shape) so the first days on the work machine
  show whether the same structure holds for work accounts, group chats and channels. Channel posts
  without a message id still fall back to `teams_url` (no documented channel link without groupId).
* **Amended after the research workflow (2026-11-06):**
  * The resolver runs after the notification, outside Teams' toast pipeline (a work tenant's page
    can be ~20k elements, ~44 ms per scan), and the chat list's preview showing this message's text
    is the strongest signal (it became the only one, see the review below): Teams titles a chat toast with the sender's
    name, so a group message from someone you also have a 1:1 chat with would otherwise open the
    1:1 chat. The text comparison happens inside the page; nothing of it is reported.
  * `options.timestamp` is forwarded; the daemon's 10 s dedup key now includes the resolved
    conversation and the timestamp (the tag is always empty, so the same short text in two chats was
    merged).
  * Verified from Teams' public JS bundle: `getNotificationOptionsPayload` builds `{body, icon,
    timestamp, tag: undefined, requireInteraction, dir}` — no `data`, ever — and Teams' click
    handler (`addEventListener("click")`) keeps the target chat only in a closure.
  * **Correction to D8:** `https://teams.microsoft.com/v2/manifest.json` is served (scope `/v2/`), so
    `…/l/…` links are outside the Teams PWA's scope and open in a browser; an installed PWA would need
    an in-scope form (`/v2/?clientType=pwa#deepLink=…`). Open item, decided once the tap test on the
    phone shows what the chosen handler does with `/l/chat/…`.
  * A receive-only Trouter tap (the worker's socket, decoding the push body) would give message-level
    links and channel ids; the measurement here cannot rule it in or out (it only looked at the
    page's own sockets, without decoding). Deferred: the DOM resolver covers chats; revisit if the
    work tenant shows gaps (channels, threads).
* **Second round of measurements (2026-11-06), work tenant (`teams.cloud.microsoft`, via console
  snippets the user ran in a browser — structure only) and the personal account:**
  * The avatar URL of a work account is `…/api/mt/part/<region>/beta/users/<tenant GUID>/profilepicturev2/8:orgid:<GUID>`;
    the sender id is whichever path segment looks like one.
  * The chat list's `data-fui-tree-item-value` (and the item's `id`, its title span's `id`, a
    `data-tabster` JSON) carry the conversation id *behind a prefix* — in both tenants, which is why
    whole-value matching found nothing. The resolver extracts the id from the value and considers only
    chat-list items (`role` treeitem/option/listitem/row). The open conversation's id appears elsewhere (`data-track-thread-id`), outside
    the list, and is ignored for scoring.
  * The **Activity** view has no conversation ids in its list: the resolver needs Teams' **Chat**
    view. Teams reopens the last used view, so the setup instructions say to open Chat in the
    `tbd login` window before closing it; the daemon's structure-only `chat list survey` line
    (logged when it changes, at every healthy probe) shows `conversation_items=0` otherwise.
  * Links have exactly the form of Teams' own "Copy link" (`/l/chat/<id %-encoded>/conversations?context=…`);
    opening one on the phone opened the chat (user check). Channel messages carry `tenantId`,
    `groupId`, `parentMessageId`, `teamName`, `channelName` in Teams' own links; tbd does not
    build channel links yet (taps on channel notifications open Teams).
  * Result on the personal account: the tap opens the right 1:1 chat, in the work and in the
    personal Android profile.
* **Revised after an independent review (2026-11-06).** The review reproduced, in Edge, wrong
  chats from the scored design: a group message from someone you also have a 1:1 chat with opened
  the 1:1 chat when the group's avatar shows other members (it usually does) or a short text such
  as "ok" did not reach the preview score; an unnamed group whose name lists the sender won over the
  group the message was posted in; a channel post (no chat-list item at all) opened some chat with
  the sender. A wrong chat is worse than Teams' start page, so:
  * Only the preview is evidence. Avatars, presence and chat names no longer pick a chat; without a
    preview match the link is `teams_url`.
  * Ties go to the first item in list order: the newest chat is on top, and a chat that just
    received a message moves up.
  * Two attempts, at +100 ms and +600 ms: the first catches a burst before the next message
    replaces the preview, the second gives a slow list time to update. A notification still pending
    when the page unloads (`pagehide`/`beforeunload`) is reported at once, so a reload never loses
    it.
  * The notification's own tag/data ids win over the page's (exact beats inferred); a message id
    is paired only with a thread from the same tag/data.
  * Dedup: the same text from both capture paths (page and service worker) within 3 s is one
    message; otherwise the key is text + conversation + timestamp, so the same short text in two
    chats is two messages.
  * `hook.Shape` replaces GUIDs and long ids by `<guid>`/`<id>` before masking, so a logged shape
    cannot fingerprint an id. The avatar/tag shapes and the DOM probe left the info line.
  * `test/faketeams/resolver_test.go` replays the review's scenarios (synthetic markup in the
    measured shape) against the real preload in Edge, plus a burst and a reload right after a
    notification.
* **No tenant data in the repository.** Real tenant/team/channel/conversation ids and names the user
  shared for format analysis are never written to code, tests, docs or commits (tests use
  synthetic ids of the same shape); `mise run secrets-check` also fails on any entry of a local,
  git-ignored stop list (`.local/denylist.txt`).

## D18 — Android: one scrolling screen, settings inline, no overflow menu (brief corrected: §6 Settings screen)

* At the maintainer's request (the app is small; an overflow menu holding only "Settings" is not
  needed) `SettingsActivity`, its layout and `res/menu/main.xml` are gone. BRIEF §6's separate
  **Settings** screen is folded into `MainActivity`, one scrolling page, top to bottom:
  1. Pair (unpaired), "waiting for pair ping" with Retry/Cancel (paired, no contact yet) or Status
     (paired and heard from the daemon); the placeholder-Firebase banner as before.
  2. "Open Teams in" — the handler list (rebuilt in `onResume`), "system default" first, the
     "unavailable" entry, the no-handlers hint and a single "Test: open Teams" button; in every state.
  3. Notifications — allowed/blocked, "Allow" (API 33+, not granted; a second denial opens the app's
     notification settings) and "Notification settings". The Status warning scrolls here.
  4. Unpair (with its confirmation) — only once the daemon has been heard from; while waiting, the
     waiting section's Cancel does the same. After unpairing the page shows Pair again.

## D19 — Windows: the browser gets a private temp directory

* **Observed** (`check-outside-win`, 2026-11-06): a `cv_debug.log` (Edge's correlation-vector log)
  appeared in the user's `%TEMP%` right after the real daemon's Edge started; Chromium also creates
  `scoped_dir<pid>_*` folders there, which a killed browser (job object, crash, restart loop) never
  removes. On the production PC that would slowly litter the user's own temp directory, outside
  `%LOCALAPPDATA%\tbd`.
* **Decision.** On Windows `browser.Launch` points `TMP` and `TEMP` (what `GetTempPath` reads) at
  `<home>\tmp` and empties that folder before each launch (one browser per home: `tbd.lock`). The
  daemon's whole footprint stays inside `<home>`, so removing tbd is removing that folder.
* **Not on Linux/macOS:** there Chromium puts its singleton socket under `TMPDIR`, and a deep
  `TMPDIR` (a test's `<home>/tmp`) overflows the 108-byte socket path limit; Edge aborted at start
  (measured). The browser keeps the caller's `TMPDIR` (the repo's `.tmp/` under mise; `/tmp`,
  cleaned by the OS, in production).


## D20 — `tbd stop` / `tbd start`; the daemon follows the task's console host (Windows)

* **Measured** (2026-11-07, Windows host): a scheduled task whose action is
  `C:\Windows\System32\conhost.exe --headless C:\Windows\System32\PING.EXE -t 127.0.0.1`, after
  `schtasks /run` and `schtasks /end`, still had `PING.EXE` running. `schtasks /end` terminates the
  action's process, conhost, and nothing below it. With the D5 action (`conhost.exe --headless
  tbd.exe run …`) that left tbd.exe running after "end the task", and the next `tbd login` failed on
  `tbd.lock`. The old install note ("stop: schtasks /end /tn tbd") and README were wrong. Also
  measured: a Windows process started from WSL has `wsl.exe` as its parent, not conhost.
* **`tbd stop [--timeout 20s]`** stops whatever holds `<home>/tbd.lock` (the daemon, or a
  `tbd login`), for `--home`/`TBD_HOME` like every command; no holder → "not running", exit 0. It
  needs no valid `config.toml`.
  1. *Who holds the lock.* The PID comes from the lock file, which only a holder writes, but `tbd
     status` and `tbd stop` take the lock for a moment to test it, and then the file still names the
     previous holder (maybe a reused PID). So the PID counts only when two try-locks 150 ms apart
     both fail with the same PID in the file, and on Linux `/proc/locks` must list a FLOCK on the
     file's inode by that PID (when it lists any; the device column is ignored, it differs from
     `st_dev` on btrfs/overlayfs). Otherwise `tbd stop` refuses rather than guess.
  2. *Graceful = the SIGTERM path* (D13: `daemon_stopping`, browser closed, lock released). Unix:
     SIGTERM through a pidfd (`os.FindProcess`), sent only after re-checking the holder with the
     process pinned. Linux under systemd (the holder is `tbd.service`'s `MainPID`):
     `systemctl --user stop --no-block tbd.service`, because with `Restart=always` systemd would
     restart a daemon that merely exits. Windows: a named event, below.
  3. Wait until the lock is released; only after the timeout end it forcibly and say so (no
     `daemon_stopping` then). Unix: SIGKILL (pidfd, re-checked). Windows: open the process, check its
     image is `tbd.exe` (a renamed binary is not forced), re-check the lock holder, then
     `TerminateProcess` on that same handle (an open handle pins the PID), exit code 0 so Task
     Scheduler does not count a failure. The job object (D14) takes Edge with it.
* **Windows stop mechanism: a named event** `Local\tbd-stop-<16 hex>`, created (manual reset) by
  whoever takes the lock and waited on by `tbd run` and `tbd login`; `tbd stop` opens and sets it.
  Alternatives rejected: Windows has no SIGTERM; `GenerateConsoleCtrlEvent` reaches only processes on
  the caller's own console, and the task's daemon sits on a hidden pseudo console of its own; there
  is no window for `WM_CLOSE`; a pipe or port needs a protocol and an endpoint to secure. A `Local\`
  event is reachable from every process of the user in the same logon session (the task runs with
  `InteractiveToken` in the desktop session) and its default DACL admits only that user and SYSTEM.
  The name hashes the lock file's **volume serial number and file index**, not the path string, so
  every spelling of the home (case, 8.3 names, junctions, `subst`) reaches the same daemon, and the
  name shows no path or user name. If the event already exists when a daemon starts (a stopper of
  the previous holder still has it open), it is reset. A holder without the event (a tbd from before
  this version, or another session, e.g. SSH) gets the timeout and then the forced path.
* **Windows parent watch.** `tbd run` looks up its parent PID (process snapshot) first thing, opens
  it with `SYNCHRONIZE | PROCESS_QUERY_LIMITED_INFORMATION` (from then on its PID cannot be reused)
  and watches it only when its image is `conhost.exe` **and** it was created no later than tbd (a
  younger process under that PID reused it). When that conhost ends — `schtasks /end` — tbd stops
  the SIGTERM way. Any other parent (cmd/PowerShell/Windows Terminal, Explorer, `wsl.exe`, a test
  harness) is not watched. Info lines: "watching the console host …" and "console host exited;
  stopping" (image base name and PID only). `schtasks /end /tn tbd` therefore works again, and
  `tbd stop` is the documented way.
* **`tbd start`** runs `schtasks /run /tn tbd` or `systemctl --user start tbd.service` (names from
  `internal/service`), after checking the service exists (`schtasks /query` exit status,
  `systemctl --user is-enabled` ≠ `not-found`; else "run `tbd install`"), and waits up to 20 s for a
  daemon to take the lock. With the lock already held it only says "already running".
* **Messages.** Every refusal on a held lock (`run`, `login`, …) reads "tbd is already running for
  this data directory (pid N): run `tbd stop`, then this command again (`tbd start` starts the daemon
  again)"; `pair --new` and the `session_expired` alert body name `tbd stop` / `tbd start` too; the
  install notes say "stop: tbd stop; start again: tbd start".
* **Tests.** Unit: holder detection (a momentary probe is no holder, `/proc/locks` parsing), event
  naming (Windows: the same event for another spelling of the path, a different one per home),
  parent-watch decisions, Stop's phases with fakes, Start against a fake service manager. Real
  mechanisms: a re-executed test binary holding the lock (SIGTERM/event obeyed → graceful; ignored →
  SIGKILL on Unix, and on Windows *refused*, since it is not tbd.exe); `Stop` against the in-process
  daemon (E2E, `daemon_stopping`); `tbd stop` against a real `tbd run` with faketeams + fcmtest;
  and, on Windows, a `tbd-test-*` scheduled task with the real install's action, `schtasks /run`,
  then `schtasks /end`: tbd.exe logs the watch, stops gracefully and pushes `daemon_stopping`.
  (Measured: `conhost.exe --headless <client>` started directly from WSL or a test, i.e. with
  standard handles, does not run the client — it acts as a ConPTY server on those handles — so
  only Task Scheduler reproduces production.)

## D21 — Public repository: privacy and push checks

* **Licence: WTFPL v2** (brief corrected: the brief said 0BSD unless the user named another; the
  maintainer did). `LICENSE`, README and the preload comment say so; there are no SPDX headers.
* **Nothing about the maintainer's own machine is committed.** `OUTSIDE.md` keeps only rows about
  tbd, Edge, Windows, WSL and mise themselves and generic Windows components; noise of the desktop
  apps, launchers, GPU caches and crash dumps of other software on one particular host goes into the
  git-ignored `OUTSIDE.local.md` (same table, same markers), which `scripts/outside-table.sh` reads
  after `OUTSIDE.md` when it exists, so `check-outside`, `check-outside-win` and `clean-outside`
  see both. A fresh clone starts without it; its first `check-outside-win` names the rows it needs.
  Found while splitting: WSL 2's swap disk `%TEMP%\<GUID>\swap.vhdx` changes whenever the VM swaps
  (a generic WSL row now).
* Fixtures and docs use the synthetic region shard `zz-01` / `zz` (avatar URLs `/api/mt/part/zz-01/…`,
  hosts `zz.ng.msg.teams.microsoft.com`) instead of a real one; the matchers never depended on it.
  The Firebase project number (the FCM sender id) is not written in docs; the project id stays.
* **`.gitignore`**: `config.toml`, `*.sa.json`, `*.p12`, `*.jks`, `*.keystore`, `*.pfx` at any depth,
  `OUTSIDE.local.md`, release outputs (`*.apk`, `*.aab`, mapping archives, `SHA256SUMS`, `/release/`)
  besides `.local/` and `dist/`; `google-services.json.example` stays tracked. `secrets-check` also
  refuses a force-added `config.toml`, `OUTSIDE.local.md` or anything under `.local/`.
* **Pre-push hook** (`mise run hooks` → `scripts/install-hooks.sh` writes `.git/hooks/pre-push`,
  idempotent, refuses to overwrite a foreign hook): `scripts/prepush-check.sh` checks every commit
  being pushed (`git rev-list <remote>..<local>`, or everything not on the remote for a new ref) and
  every annotated tag: the stop list (as in `secrets-check`, including dashless GUIDs) over the
  commit's whole tree and over the raw commit object (message, names, emails); the `secrets-check`
  key/token patterns over the same (patterns now shared in `scripts/secrets-lib.sh`); author,
  committer and tagger emails only `alien2003@protonmail.ch`, `*@users.noreply.github.com` or
  `noreply@github.com`; their timezone offsets only `+0000` (**fail**, not warn: commits are made
  with `TZ=UTC`, and an offset tells roughly where the author lives). It prints commit and file
  names only, never the matching text. By hand: `mise run prepush-check` (all refs) or
  `scripts/prepush-check.sh <rev-list args>`. The stop list never leaves the dev PC: CI runs
  `secrets-check` with `TBD_NO_DENYLIST=1`.
* Measured on 2026-11-07, before the history rewrite: `mise run prepush-check` fails on 41 commits,
  82 findings, all of them a non-UTC timezone offset; no stop-list, pattern or email finding.
