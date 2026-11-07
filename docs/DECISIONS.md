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

* Measured: `schtasks /create /sc onlogon … /rl limited` fails with *Access is denied* from a
  non-elevated (medium integrity) process, even for an administrator under UAC.
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
  pages; English and Russian at first, since 2026-11 also German, French, Spanish, Portuguese,
  Italian, Dutch, Polish and Ukrainian, compared without diacritics) and were tested on 21 local pages in real Edge; **to verify against real
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

* **Measured, structure only** (masked shapes, names, counts; no text, no values), on a real
  Teams session (a personal account: the page lives on `https://teams.live.com`):
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
  commit's whole tree (binary files too, `git grep -a`: a PNG text chunk or a SQLite file counts),
  over every path in that tree, over the raw commit and tag objects (message, names, emails) and
  over the names of the refs being pushed (every ref by hand); a path, tag or ref name that holds a
  value is shown as an object id or a count, never as text. `secrets-check` likewise matches the
  stop list against binary files and against every path; the `secrets-check`
  key/token patterns over the same (patterns now shared in `scripts/secrets-lib.sh`); author,
  committer and tagger emails only `alien2003@protonmail.ch`, `*@users.noreply.github.com` or
  `noreply@github.com`; their timezone offsets only `+0000` (**fail**, not warn: commits are made
  with `TZ=UTC`, and an offset tells roughly where the author lives). It prints commit and file
  names only, never the matching text. By hand: `mise run prepush-check` (all refs) or
  `scripts/prepush-check.sh <rev-list args>`. The stop list never leaves the dev PC: CI runs
  `secrets-check` with `TBD_NO_DENYLIST=1`.
* Measured on 2026-11-07, before the history rewrite: `mise run prepush-check` fails on 41 commits,
  82 findings, all of them a non-UTC timezone offset; no stop-list, pattern or email finding.
  Then every value this entry removed from HEAD (the project number, the host-software names of
  the old OUTSIDE.md rows, the real region shards, a line of CLAUDE.md, and the host descriptions
  removed after the review of D22–D24) was added to the stop list, so that `prepush-check` over
  the rewritten history proves the scrub instead of checking only the offsets: it must be clean
  over every ref before the first push.

## D22 — Continuous integration on GitHub Actions

* **`.github/workflows/ci.yml`** runs on pushes to `main`, pull requests, by hand
  (`workflow_dispatch`) and as a reusable workflow (`workflow_call`, for the release workflow). Four
  jobs, each with `permissions: contents: read` under a top-level `permissions: {}`:
  * **Lint** (Linux): `mise run lint`, `mise run lint-ci`, `mise run secrets-check` with
    `TBD_NO_DENYLIST=1` (the stop list never leaves the dev PC; the pre-push hook of D21 applies it).
  * **Go tests**, Linux and Windows (`ubuntu-latest`, `windows-latest`): `scripts/test-go.sh`
    (new task `test-go`, also called by `mise run test`) builds the binary for the runner's OS and
    runs `go test -count=1 ./...` with `TBD_REQUIRE_BROWSER=1` against the **runner's preinstalled
    Edge**, found the way a user's machine finds it (`microsoft-edge` on `PATH`, `msedge.exe` under
    Program Files). Windows runs the tests natively from Git Bash, not cross-compiled as in
    `test-win`: the Go tests do not depend on the WSL sandbox (only `scripts/win/` does: `WSLENV`,
    the sandbox TEMP, the `scoped_dir` and registry checks), so nothing is skipped for CI. The
    usual skips stay: the windowed login on Linux without a display, the systemd round trip where
    `systemctl --user` does not work, `TBD_TEST_TASK_RUN` off. The log `.tmp/go-test.log` is
    uploaded when the job fails.
  * **Android** (Linux): `scripts/test-android.sh`, then `scripts/build.sh android` with the
    placeholder `google-services.json` and a throwaway debug key generated in the job. The debug
    APK becomes an artifact (14 days) only on pushes to `main`.
* Deliberately not in CI: `check-outside*` and `clean-outside` (they describe the dev PC),
  `test-win` and `scripts/win/`, `prepush-check`, `fetch-edge` (the runners have Edge), a
  newest-Edge job, `loginctl enable-linger`, xvfb for the windowed tests. No secrets are used.
* **Pins.** Actions by full commit SHA with the tag in a comment: `actions/checkout` v7.0.1,
  `actions/cache` (`restore`/`save`) v6.1.0, `actions/upload-artifact` v7.0.2, `jdx/mise-action`
  v5.2.5. mise itself is `MISE_VERSION` = the dev PC's 2026.9.17; bump both together. No `mise.lock`.
  The toolchain comes from `mise.toml` as on the dev PC. `mise-action` runs with `env: false`
  (every step goes through `mise run` / `mise exec`, so `[env]` applies only there) and one cache
  prefix per job, because each job drops the tools it does not need with `MISE_DISABLE_TOOLS`
  (backend-prefixed names such as `go:honnef.co/go/tools/cmd/staticcheck` work there, checked with
  mise 2026.9.17) and the action's cache key does not see that variable.
* **Caches** (Go module and build cache, staticcheck's, Gradle's, the project-local Android SDK)
  are restored on every run and saved only by pushes to `main`, so pull requests and tags cannot
  write a cache that `main` or a release later restores. Checkouts use `persist-credentials: false`.
* **Changes for CI and fresh clones.** `.gitattributes` (`* text=auto eol=lf`): a Windows checkout
  would otherwise convert `proto/vectors.json` and the scripts to CRLF; all 190 files were LF
  already, so nothing was renormalised. The scripts create `.tmp/` (`TMPDIR`, `java.io.tmpdir`)
  themselves: `go vet` fails without it, and a fresh clone has none. The Android scripts also unset
  `ANDROID_SDK_ROOT` (runners export it for their own SDK; AGP rejects two different SDK paths).
  `mise.toml` builds `MISE_*_DIR` from `get_env`: a value already set wins, `HOME` falls back to
  `USERPROFILE` (a native Windows process may have no `HOME`, and `{{env.HOME}}` aborted every mise
  command there).
* **`mise run lint-ci`** (`scripts/lint-ci.sh`): actionlint 1.7.12 (it also runs shellcheck on
  each `run:` block), `zizmor --offline --strict-collection` 1.32.0 over `.github/` (workflows and
  `dependabot.yml`), `bash -n` and shellcheck 0.11.0 over every tracked script; all three come from
  mise's aqua backend (whose `mise install` verifies their sigstore attestations and so refreshes
  `~/.cache/sigstore-rust`, an `OUTSIDE.md` row). Enabling shellcheck found 13 findings in existing scripts: two, on one line
  of `fetch-edge.sh`, were real (it printed the missing libraries through an unquoted `echo`; now a
  substitution), the rest are intended (literal `~/` and glob matches, unexpanded hook text,
  variables of a sourced file, Windows backslashes) and carry a `shellcheck disable` with the reason;
  `scripts/win/` changed only in comments.
* **Dependabot** (`.github/dependabot.yml`): `github-actions`, `gomod` and `gradle` (`/android`,
  no wrapper), weekly on Mondays, cooldown 7 days (30 for a major version, actions included), minor
  and patch updates as one grouped PR per ecosystem (the actions group takes majors too; Go and
  Gradle majors come as PRs of their own), commit prefixes `ci` / `build`. Versions are
  recorded here, so a Dependabot PR is merged together with its DECISIONS line. `mise.toml`, the
  mise version in `ci.yml`, `buildToolsVersion` and the SDK packages of `android-sdk.sh` stay
  manual (`mise outdated` lists the mise tools).
* Not known before the first run on GitHub: whether Edge's sandbox starts on the Linux runner
  (Ubuntu restricts unprivileged user namespaces; tbd does not pass `--no-sandbox`), whether the
  Windows runner's session lets the windowed login tests and `TestBinaryScheduledTaskEnd` (a
  scheduled task with an interactive logon) run, and whether `HOME` is set there. Fixes go into
  separate commits.

## D23 — Releases: signed APK, tbd binaries, provenance

* **A pushed tag `vX.Y.Z` or `vX.Y.Z-rc.N` publishes a GitHub release** without a draft or a
  reviewer (`.github/workflows/release.yml`); an rc tag (the only accepted kind with a "-") becomes
  a prerelease. Assets, unversioned so `releases/latest/download/<name>` and an APK filter stay
  fixed: `teams-bridge.apk`, `teams-bridge-mapping.txt.gz`, `tbd-linux-amd64`,
  `tbd-windows-amd64.exe`, `SHA256SUMS`. amd64 only. Jobs: `tag` (contents: read; the tag parses
  and points at a commit on `main`; writes the notes), `verify` (the whole of `ci.yml` through
  `workflow_call`; caches restored, never saved on a tag), `build` (the only job with secrets, in
  the environment `release`; contents: read, id-token and attestations: write; no caches at all, so
  nothing a pull request wrote reaches the signed build; `actions/attest-build-provenance` v4.2.2
  with `subject-path` over all five assets, `SHA256SUMS` included: its `subject-checksums` input
  would attest only the files listed in `SHA256SUMS`, so `gh attestation verify SHA256SUMS` would
  find nothing), `publish` (contents: write; `gh release create --verify-tag` with the
  downloaded assets after `sha256sum -c`). `actions/download-artifact` v8.0.2. The reusable call
  keeps the `./` form: zizmor 1.32 prefers `$/`, which actionlint 1.7.12 rejects (an inline
  `zizmor: ignore`; a reusable workflow is resolved from the commit at queue time, not from the
  workspace).
* **Version from the tag** (`scripts/version.sh`, bash and git only): `versionCode = MAJOR·10⁶ +
  MINOR·10⁴ + PATCH·100 + (N for rc.N, 99 for the release)`, so v0.1.0 = 10099 and every rc sorts
  below its release; MINOR, PATCH < 100, N in 1..98, MAJOR < 2100, no leading zeros, anything else
  refused. Without a tag at HEAD a build is the development version (code 1,
  `0.0.0-dev+<commit>[.dirty]`); a plain Gradle build is `1` / `0.0.0-dev`. Gradle takes
  `-Ptbd.versionCode` / `-Ptbd.versionName`; tbd gets `-X main.version=<tag>` via `TBD_VERSION`.
  `mise run test-scripts` (also in `test` and the CI lint job) table-tests the scheme and the notes.
* **Signing in Gradle from the environment only**: `TBD_RELEASE_KEYSTORE` (path, PKCS12),
  `TBD_RELEASE_KEYSTORE_PASSWORD` (store = key password), `TBD_RELEASE_KEY_ALIAS` (default
  `teams-bridge`); without the keystore variable the release APK is unsigned. v2 + v3 signatures
  (v3 leaves room for a key rotation; with minSdk 28 apksigner reports only v3 as verified), no v1.
  `scripts/build-android.sh release` runs a signed build with `--no-configuration-cache
  --no-daemon`, so the password is neither in a configuration-cache entry nor in a JVM that
  outlives the build (checked: no file under `android/`, `.xdg/gradle`, `.tmp`, `dist` contains it
  afterwards). `scripts/build-release.sh` (`mise run release-build`) also takes the keystore as
  `TBD_RELEASE_KEYSTORE_B64` (decoded with umask 077 into `.tmp/release.*`, removed on exit) and the
  password from `TBD_RELEASE_KEYSTORE_PASSWORD_FILE`; with nothing set it uses the maintainer's
  local copy in `.local/release/`. CI secrets: `TBD_RELEASE_KEYSTORE_B64`,
  `TBD_RELEASE_KEYSTORE_PASSWORD`, `TBD_RELEASE_KEY_ALIAS`, `TBD_GOOGLE_SERVICES_JSON_B64`.
* **The release key**: RSA 4096, certificate valid until 2054, SHA-256
  `46:FC:C9:4A:53:66:85:09:66:12:4F:5C:30:A1:A4:C6:33:F0:3C:A8:2E:01:89:C7:48:63:19:E1:BA:16:FC:3E`,
  committed as `android/release-cert.sha256`. A release build fails unless exactly one signer with
  exactly this certificate signed it (`TBD_RELEASE_CERT_FILE` overrides the file for tests), the
  APK is not debuggable (`aapt2 dump badging`), its versionCode/versionName are the tag's, and it is
  under 6 MB like the debug APK. `build-release.sh` also refuses a dirty tree, a tag that does not
  point at HEAD, a missing or placeholder `google-services.json`, binaries that are not
  `CGO_ENABLED=0` amd64 or not stamped with the tag, and checks `sha256sum -c SHA256SUMS`.
* **The release APK carries the maintainer's Firebase configuration** (`google-services.json` from
  the secret), so it receives only from a daemon holding that project's service account: other
  users build their own APK against their own project (README). Its project id, API key and sender
  number become public with the APK; none of them lets anyone send (that needs the service account).
* **R8 in release** shrinks, optimises and obfuscates (debug only shrinks); the mapping ships as
  `teams-bridge-mapping.txt.gz`. Checked on the first signed build (2026-11-07): the app uses no
  reflection (JCA by algorithm name, platform `org.json`, enums not persisted by name), the
  manifest classes keep their names, WorkManager's consumer rules keep
  `WatchdogWorker(Context, WorkerParameters)`, Firebase's keep every `ComponentRegistrar`; `seeds.txt`
  lists all of them. No keep rule had to be added. Release APK 1.81 MB (debug 3.07 MB). The assets
  contain no absolute host path. `dependenciesInfo` is off (a blob encrypted for Google that
  F-Droid-style scanners flag). The behaviour on a phone is still to be checked with the first rc.
* **Notes** (`scripts/release-notes.sh`): the `CHANGELOG.md` section of the version if that file
  exists, else the annotated tag's message when it says more than the tag name, else the `feat` /
  `fix` / `perf` subjects since the previous release tag (a final release counts from the previous
  final release); then the asset table and how to verify (`sha256sum -c`, `gh attestation verify
  --signer-workflow`, `apksigner verify --print-certs`, the certificate fingerprint).

## D24 — Publication: documents, Obtainium, how the pieces fit

* **What publishing consists of.** The repository goes public at
  `https://github.com/alien2003/teams-bridge` with: CI on every push to `main` and every pull
  request (D22); a release for every pushed tag `v*`, built, signed with the release key from
  the `release` environment's secrets, attested and published without a draft or a reviewer (D23);
  the stop list and the pre-push check on the maintainer's PC only (D21); CodeQL default setup,
  private vulnerability reporting, secret scanning and the repository settings are configured
  through the API, not in a workflow file. **The whole history is rewritten before the first push**
  (the values D21 removed from HEAD, every author and committer timestamp in UTC), so the first
  public commit already passes `prepush-check`; the first tag is `v0.1.0`.
* **Documents.** `README.md`: badges for CI, the latest release and the licence (a static WTFPL
  badge, independent of GitHub's licence detection), a mermaid diagram under "How it works", the
  release install with `SHA256SUMS`, `gh attestation verify --signer-workflow` and the certificate,
  and the switch from a debug APK: one uninstall (Android never updates an app with one signed by
  another key), then scan the QR code plain `tbd pair` shows again (it keeps the secret, so the
  daemon is untouched). `SECURITY.md`: GitHub private vulnerability reporting only, best effort with
  no promised times, what never to post (debug logs, content, names, ids, `config.toml`, the
  pairing code or QR), the threat model in short, and what is out of scope. `CONTRIBUTING.md`:
  mise, the tasks, the stop list as a concept (never its contents), the pre-push check (contributors
  commit with their GitHub noreply address and `TZ=UTC`), Dependabot and what is updated by hand,
  how a release is made. Issue forms (bug: "paste only structure-only log lines" and a required
  privacy checkbox; feature) with blank issues off and the security contact link pointing to private
  reporting; a pull-request checklist. No `CODEOWNERS` (one maintainer, no required review), no
  screenshots yet.
* **`CHANGELOG.md`** (Keep a Changelog): the next version's section is `## [X.Y.Z] - Unreleased`
  and gets its date when tagged; `release-notes.sh` takes that section as the release notes (its
  heading line is not copied). An rc has no section of its own and gets its tag message, else the
  `feat`/`fix`/`perf` subjects.
* **Obtainium, checked in its source** (`ImranR98/Obtainium`, release 1.7.4 of 2026-11-01):
  * `obtainium://app/<encodeURIComponent(JSON)>` imports one app config of Obtainium's export
    format: `id`, `url`, `author`, `name`, `preferredApkIndex`, and `additionalSettings` as a
    **string holding JSON** (`App.fromJson` runs `jsonDecode` on it). Obtainium asks before adding it: its
    *Import app* dialog has no form fields, only a collapsed *Raw JSON* tile with the config; the
    settings appear under their labels (*Filter links by regex*, *Expected signing certificate
    hashes*) only in the app's settings after the import. The badge links to `https://apps.obtainium.imranr.dev/redirect?r=…`,
    whose page reads `r` with `URLSearchParams`, decodes it once more and re-encodes it, and offers
    "get Obtainium" when the app link does nothing; it carries that site's analytics, so the README
    also says how to add the app by hand.
  * `apkFilterRegEx` is matched against the asset's file name: `^teams-bridge[.]apk$` (D23's one
    unversioned APK; the debug artifact and the mapping never match).
  * **Certificate pinning is supported**: `allowedSigningCertHashes`, a string of SHA-256
    certificate hashes separated by whitespace, commas or semicolons, normalised to uppercase
    colon pairs (the format of `android/release-cert.sha256`). With it set, Obtainium refuses an APK
    whose signer certificates are not all in the list, or whose certificates it cannot read, with
    no override. First released in 1.6.16 (2026-09-13; 1.6.17 is the stable release with it); the
    check is fixed for Android 9 and 10 (this app's minSdk 28) only in 1.7.4, which the README
    names. Separately, with *Warn about signing certificate changes* (`verifySigningCertHashes`,
    on by default) Obtainium compares the downloaded APK with the installed app's certificate: over
    a self-built debug APK the release APK passes the pin but gets the same "Signing certificate
    mismatch" title as a warning ("signed with a different certificate than the installed
    version", with *Install anyway*), so the README tells the two texts apart and sends the first
    one to the uninstall. Obtainium compares the
    certificate history for a single-signer APK, so after a key rotation (v3) the list must hold
    every certificate of the lineage, and existing users must edit the setting by hand.
  * Prereleases are skipped unless *Include prereleases* is turned on (it is off by default), which
    suits rc tags.
  * `scripts/obtainium-link.sh` builds the link from `android/release-cert.sha256` and the
    applicationId (refusing if either differs in form); `scripts/obtainium-link_test.sh` (in
    `test-scripts`) decodes it back, checks the fields and the filter, and checks that `README.md`
    carries exactly this link, so a new certificate cannot leave a stale pin in the README.
  * The badge is Obtainium's `badge_obtainium.png` (GPL-3.0), trimmed of its transparent padding to
    564×168 and shown at 161×48 as Obtainium's badging guide suggests, hosted in `docs/assets/`
    (source and licence in `docs/assets/README.md`; the guide asks not to hotlink it). It is the
    one file not under the WTFPL: the README's License section names the exception, and the
    GPL-3.0 text ships next to it as `docs/assets/LICENSE-GPL-3.0.txt`.
    Obtainium's own app directory wants ≥ 35 stars and a project ≥ 4 months old: not now.

## D25 — First release candidate: what the runners and an emulator showed (2026-11-07)

* **CI on GitHub's runners** needed three fixes the dev PC could not show: mise points `TMPDIR` at
  `.tmp/`, which a fresh checkout lacks (every job now creates it before `mise-action`); Ubuntu 24.04
  runners forbid unprivileged user namespaces through AppArmor, so Edge fell back to its
  unconfigured SUID helper and aborted (the Go test job lifts the restriction with `sysctl`, the
  browser sandbox stays on); and tests that waited for the browser with a bare channel receive hung
  until the package timeout instead of failing (they wait with a timeout now). A Windows E2E test
  read the fake sign-in page before it was parsed; it polls now.
* **v0.1.0-rc.1** was released by the pipeline (one job re-run after that flake). Checked from the
  published assets: `sha256sum -c SHA256SUMS`, `gh attestation verify` for all five assets (signed by
  `release.yml` at `refs/tags/v0.1.0-rc.1`), the APK's v3 signature with the release certificate
  (SHA-256 `46fcc94a…16fc3e`), not debuggable, versionName `0.1.0-rc.1`, versionCode 10001.
* **Smoke test of the R8 build** on an Android 16 emulator (`google_apis` x86_64 image, KVM; the
  Play Store image refuses adb without an on-screen confirmation): install, start, pair by typed
  code with a throwaway test daemon (fresh secret, same Firebase project), `tbd test-push`, the
  notification shows and a tap opens the browser; no entry in the crash log. The README screenshots
  come from that run, with the status bar in demo mode (fixed clock).
* **Emulator hygiene:** the emulator and adb ignore `ANDROID_USER_HOME` for some files (`adbkey`,
  feature flags, modem state, the console auth token) and the emulator writes crash reports to
  `/tmp/android-$USER`; run them with `HOME`, `ANDROID_EMULATOR_HOME`, `ANDROID_USER_HOME`,
  `ANDROID_TMP` and `TMPDIR` pointed into the repository (`.xdg/emu-home`, `.tmp/android`).
* **Load on the runners:** `go test ./...` runs packages in parallel and several of them start Edge
  at the same time; on a 4-vCPU runner a cold start then exceeded the 30 s DevTools timeout, and a
  `--once` run delivered a second, already queued notification. On GitHub Actions
  `scripts/test-go.sh` runs at most two packages at a time, and the `--once` test accepts any
  delivered count (shutdown flushes the queue on purpose). No retries: a failure stays a failure.


## D26 — macOS (darwin/arm64 and darwin/amd64)

`tbd` runs on macOS 13 or newer, Apple Silicon and Intel, with the same commands as on Linux and
Windows. There is no Mac in the development setup: every macOS behaviour is either a GOOS-parameterised
pure function tested on Linux or exercised on GitHub's macOS runners. This entry grows with the port;
the build and release part:

* **Two separate binaries, cross-built on Linux:** `tbd-darwin-arm64` and `tbd-darwin-amd64`, with
  `CGO_ENABLED=0 -trimpath -ldflags "-s -w"` like the others (`scripts/build.sh darwin`). No
  universal binary: `lipo` does not exist on Linux, each download is half the size, and the
  per-architecture checks stay simple. Measured on the dev PC: the Go linker writes an ad-hoc code
  signature (`LC_CODE_SIGNATURE`) into the cross-built arm64 binary, `-s -w` included, which Apple
  Silicon requires to run anything; the amd64 binary carries none (Intel Macs run unsigned code).
  Nothing may modify a darwin binary after the link (strip, a second `-ldflags` pass), or the
  signature breaks.
* **Not signed with a Developer ID, not notarized:** both need a paid Apple Developer account and
  are out of scope. Gatekeeper therefore refuses a copy downloaded with a browser (it carries
  `com.apple.quarantine`) until the user clears the attribute; `curl` and `gh release download` set
  none. The README says so.
* **Checks:** `scripts/build-release.sh` verifies `GOOS`/`GOARCH` from `go version -m` and the
  Mach-O header (magic `feedfacf`, CPU type arm64 or x86_64) for each binary, and `SHA256SUMS`
  covers six files (APK, mapping, four binaries); provenance attests every asset. The release
  workflow's `macos` job then runs each binary natively before publishing: `macos-latest` (arm64)
  and `macos-15-intel` (x86_64), `uname -m` and `lipo -archs` equal to the binary's architecture
  (so not under Rosetta), `codesign --verify` and `Signature=adhoc` on arm64, `tbd version` ending in
  `darwin/<arch>)`, and `gh attestation verify` as a user would run it. `publish` waits for it.
* **bash 3.2:** macOS ships bash 3.2, and the macOS jobs run `scripts/build.sh`, `test-go.sh` and
  `version.sh` with whatever `bash` is first on the PATH. Those scripts avoid bash 4 constructs
  (`;;&`, `mapfile`, associative arrays, `${x,,}`, `&>>`, …) and expand an array that may be empty
  as `${a[@]+"${a[@]}"}` (bash < 4.4 calls an empty `"${a[@]}"` unbound under `set -u`).
  `scripts/bash32_test.sh` (in `test-scripts`) greps for them; the real proof is the macOS job.
  macOS also has no `sha256sum`: workflow steps there use `shasum -a 256`.
* **Lint** vets all four targets and runs staticcheck for linux/amd64, windows/amd64 and
  darwin/arm64 (darwin/amd64 has the same build tags).

Configuration (`internal/config`):

* **Data directory:** `~/Library/Application Support/tbd` by default, Apple's place for per-user
  application data and the counterpart of `%LOCALAPPDATA%\tbd` on Windows; `--home` and
  `TBD_HOME` still win, and `XDG_DATA_HOME` is ignored on macOS (as on Windows, and as Go's
  `os.UserConfigDir` does there). Rejected: `~/.local/share/tbd`, foreign on a Mac and hidden from
  Finder users. The space in the path is safe: arguments are passed as separate argv elements and
  printed commands are shell-quoted. The one catch, Chromium mapping a profile under
  `Application Support` to a disk cache under `~/Library/Caches/<same path>`, is handled by the
  browser switches (`--disk-cache-dir`, below).
* **Browser detection:** config `browser` → `TBD_BROWSER` → source `macos-standard`: Microsoft
  Edge, Chromium, Google Chrome (the Linux priority), each looked for as
  `/Applications/<X>.app/Contents/MacOS/<X>` and then under `~/Applications` → the Linux names on
  `PATH` → `.tools/edge` (dev). Edge's installer puts it in `/Applications`; Chrome and Chromium can
  be dragged to `~/Applications`. Beta, Dev and Canary are not auto-detected (set `browser`). A
  `.app` directory in `browser` or `TBD_BROWSER` stands for `Contents/MacOS/<CFBundleExecutable>`
  (from its `Info.plist`; else the bundle's name without `.app`), so
  `browser = "/Applications/Microsoft Edge.app"` works; a regular file is always used as is.
* **Browser version:** read from `CFBundleShortVersionString` in the bundle's `Contents/Info.plist`
  (Chromium writes the four-part version there, as XML), which starts no process: no Gatekeeper
  first-launch scan, no framework load, no keychain access, no 15 s timeout. A binary property list
  (`bplist00`), a missing key, anything that is not exactly `a.b.c.d`, or a browser outside a bundle
  falls back to `--version`, which also works on a Mac. Only the top-level dictionary counts
  (Info.plist nests dictionaries with keys of the same names). Unit tests on every OS cover the
  order, the bundle resolution and the plist fixtures; a darwin-only test on the runners checks the
  real bundle, `browser = "<bundle>.app"` and that `Info.plist` agrees with `--version`.

Browser (`internal/browser`):

* **Switches** (`platformLaunchArgsFor`, a pure function of GOOS, so the tests of every OS pin the
  macOS list): `--use-mock-keychain` always, for the headless daemon and the windowed login alike,
  and never through `browser_args`, so the two cannot disagree on the key of the cookie store.
  Without it Edge creates and reads `Microsoft Edge Safe Storage` in the user's login keychain, the
  item the user's own Edge shares by name, and access can prompt, or block under launchd while the
  keychain is locked, silently losing the session. The trade-off: cookies at rest are then
  encrypted with a public constant key, so only the 0700 home protects them, as on Linux without a
  keyring. No `--password-store` (a Linux-only switch). `--disk-cache-dir=<home>/cache` always (see
  the data directory above); `--disable-gpu` headless, as on Linux.
* **Native notifications off:** the features `NativeNotifications,SystemNotifications` are
  disabled on macOS in a list of their own that is always merged into `--disable-features` (not
  `DisabledEdgeFeatures`, which `implicit_signin` drops). `worker.js` still calls the original
  `showNotification` after capturing; on a Mac that would reach the Notification Center: a
  permission prompt for the browser that nobody answers, or real banners repeating Teams content.
  Chromium's own message center, the fallback, is invisible headless (what Linux headless does
  already). Capture is unaffected. Never `--disable-notifications`: the hook would no longer be
  called.
* **Crash dumps:** Crashpad on macOS ignores `--user-data-dir` and `--crash-dumps-dir`; the browser
  gets `BREAKPAD_DUMP_LOCATION=<home>/crashes` so that it does not write into the user's own
  `~/Library/Application Support/Microsoft Edge/Crashpad`. `--crash-dumps-dir` stays.
* **navigator.platform:** D8 holds on macOS too (the Windows 11 Edge UA and Windows client hints,
  one identity for sign-in and daemon), and where the Windows metadata is applied (page, workers,
  login) `Emulation.setUserAgentOverride` also sets `platform = "Win32"`: a Mac would otherwise
  report `MacIntel` next to a Windows UA, a combination never tested against Conditional Access.
  Linux keeps its own `Linux x86_64` (proven in production); Windows says `Win32` itself. A user's
  non-Chromium `user_agent` gets neither metadata nor platform.
* **A browser left behind:** macOS has no Pdeathsig, so a daemon that crashed or was SIGKILLed
  leaves its headless browser holding the profile; the next browser would hand over to it through
  Chromium's process singleton and exit, and `Launch` would fail ("exited before the DevTools
  endpoint appeared") over and over under launchd's KeepAlive. Before every launch, on every Unix,
  `killStaleBrowser` reads `<profile>/SingletonLock` (a symlink to `<host>-<pid>`) and ends that
  process's group (SIGTERM, SIGKILL after 5 s) only if the process is alive, leads its own process
  group (Launch sets Setpgid) and has exactly `--user-data-dir=<profile>` in its argv (macOS:
  `kern.procargs2`; Linux: `/proc/<pid>/cmdline`). `tbd.lock` makes any such process ours. It logs
  `killed a browser left by a previous tbd`. Windows needs nothing (the job object kills the tree
  with the daemon). Rejected: `--remote-debugging-pipe` (the browser would die with its pipe, but
  the CDP transport would have to be rewritten). Tests: a stand-in shell "browser" on Linux and
  macOS (killed with its group; left alone when its argv names another or a parent profile, when it
  is not a group leader, for a dead PID, a regular file, our own PID or an unreadable argv; SIGKILL
  when SIGTERM is ignored), and the real browser: a helper process launches it and is SIGKILLed,
  and the next `Launch` on the same home must succeed with nothing of the old group left (on Linux
  Pdeathsig gets there first; on macOS the log line must appear).
* **`tbd login` ends when the window closes:** on macOS closing the last window does not quit the
  browser, so `Login` also ends when no page target is left (re-counted with `Target.getTargets`
  on every target destroyed or crashed, and every 2 s), then closes the browser itself; on every
  OS (measured on the dev PC: headless Edge on Linux keeps running without pages too, so
  `TestLoginReturnsWhenPageClosed` exercises the watcher there). The message says "close the window
  (or quit the browser)". On macOS `Page.bringToFront` after navigating, since a browser started
  from Terminal opens behind it, and a failed start over SSH (`SSH_CONNECTION` set) adds "run tbd
  login in Terminal on the Mac itself". The windowed tests close the page (`Target.closeTarget`),
  not the browser, and run on macOS only in a GUI session (`launchctl managername` says `Aqua`);
  `TBD_REQUIRE_GUI=1` turns that skip into a failure.
* **Cookie persistence** (every OS): faketeams sets a persistent cookie; a headless login on a
  home, then a `Launch` + `Attach` on the same home must send it back, which proves that the store
  survives a restart and that login and daemon agree on its key.
* **Runner-only check** (`cache_darwin_test.go`, `GITHUB_ACTIONS=true`): with a home under the
  real `~/Library/Application Support/tbd-test-<random>`, after a page load
  `~/Library/Caches/tbd-test-<random>` must not exist and `<home>/cache` must hold files; both are
  removed afterwards.
* **Socket path:** Chromium's singleton socket lives in `TMPDIR`, and `sun_path` holds 104 bytes
  on macOS (108 on Linux). The browser keeps the caller's `TMPDIR` on macOS as on Linux; whether a
  runner's `TMPDIR` is short enough is first seen on the macOS legs. Only comments changed.
