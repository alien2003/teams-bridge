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
* **Design.** At notification time the preload resolves the conversation by reading the page: one
  pass collects the elements that carry the sender id (each +1 for its nearest ancestor whose
  attribute value is a whole conversation id) and every such conversation element scored by its
  accessible name (aria-labelledby) against the notification title (equal +3, containment +2). The
  highest score wins; ties go to the first in the list (the most recent chat). The hook reports it as
  `conversation`; Go validates it (whole-id regex) and `DeepLink` prefers it, giving
  `<origin>/l/chat/<id>/conversations`. Nothing is clicked, typed or navigated; the id is not
  logged (only its masked shape).
* **Origin.** Links are built on the origin the page actually lives on if it is the `teams_url`
  origin or one of the well-known Teams origins (teams.microsoft.com, teams.cloud.microsoft,
  teams.live.com), else on the `teams_url` origin; a foreign origin reported by the page is ignored.
* **Rejected:** replaying the notification's own `onclick` (it would navigate the daemon's page and
  mark the chat read at capture time); a WebSocket/response-body correlator (measured: the sender is
  not visible in the frames; it would also keep message content in memory); CSS-class selectors
  (unstable) — the resolver keys on attribute *values* that are conversation ids, not on names.
* The info line of a capture keeps the structure-only diagnostics (tag/icon shapes, option names,
  page origin, the DOM probe and the resolved id's shape) so the first days on the work machine
  show whether the same structure holds for work accounts, group chats and channels. Channel posts
  without a message id still fall back to `teams_url` (no documented channel link without groupId).
