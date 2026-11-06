# Teams Bridge (`tbd`)

Microsoft Teams notifications on an Android phone without the Teams app. `tbd` keeps Teams web open
in a headless Edge/Chromium on your computer, catches every notification Teams raises (by replacing
`window.Notification` in the page, the way Ferdium does), encrypts it and pushes it through Firebase
Cloud Messaging to the **Teams Bridge** app, which shows it and, on tap, opens the chat in the
browser or installed Teams PWA you picked.

**Threat model.** Everything leaves your computer end-to-end encrypted (ChaCha20-Poly1305 with a key
derived from a 16-byte pairing secret that only the computer and the phone hold) and padded to one
of two fixed sizes. Google/FCM sees only ciphertext, its size class, timing, and a topic name that is
derived from the secret and therefore unguessable. Titles, senders, message text and URLs never leave
the daemon in clear text; there is no relay server of ours and no reverse channel. Losing the secret
means pairing again. The protocol is specified in [`proto/PROTOCOL.md`](proto/PROTOCOL.md).

Not affiliated with, endorsed or supported by Microsoft. "Microsoft Teams" and "Microsoft Edge" are
trademarks of Microsoft Corporation.

## Prerequisites

* **Computer:** Linux (incl. WSL2) or Windows 10/11, with Microsoft Edge or Chromium/Chrome installed.
  `tbd` finds Edge in its standard Windows location and `microsoft-edge`, `chromium`,
  `chromium-browser` or `google-chrome` on Linux; set `browser = "/path"` in `config.toml` otherwise.
* **Firebase project** (free tier is enough):
  1. Create a project at <https://console.firebase.google.com/>.
  2. Add an Android app with package name `io.github.alien2003.teamsbridge`; download its
     `google-services.json` into `android/app/` (needed to build the app).
  3. Cloud Messaging is enabled by default for new projects (*Project settings → Cloud Messaging*:
     "Firebase Cloud Messaging API (V1)" must be *Enabled*).
  4. *Project settings → Service accounts → Generate new private key* → save the JSON next to the
     daemon's config (e.g. `~/.local/share/tbd/fcm.sa.json`, mode 0600) and set
     `fcm_service_account = "fcm.sa.json"` in `config.toml`.
* **Phone:** Android 9 (API 28) or newer with Google Play services.

## Build

Everything is built with [mise](https://mise.jdx.dev/) and stays inside the repository:

```sh
mise trust && mise install
mise run build      # dist/tbd (Linux, static), dist/tbd.exe (Windows), dist/teams-bridge-debug.apk
mise run test       # Go unit + E2E tests against a fake Teams in a real headless Edge, Android JVM tests
```

Without `android/app/google-services.json` the build drops in a placeholder: the APK builds, but push
does not work until you rebuild with your real file.

## Set up

```sh
tbd login      # a browser window opens with the daemon's own profile: sign in to Teams, close it
tbd pair       # prints a QR code and the 26-character code, then sends pairing pings
```

Install `dist/teams-bridge-debug.apk` on the phone, open **Teams Bridge → Pair**, scan the QR code
(or type the code), and wait for **Paired ✓**. In **Settings** pick where a tap opens Teams (a
browser, or the Teams PWA if you installed one) and grant the notification permission.

```sh
tbd test-push  # a test notification should arrive on the phone
tbd install    # start the daemon at logon (systemd user unit / Windows scheduled task)
tbd status     # pairing, browser, last heartbeat and last delivered message
```

`tbd install --dry-run` prints exactly what would be written and executed. The daemon sends a
heartbeat every 6 hours; the app warns you if none arrived for 12 hours. If Teams signs you out,
the phone gets a "session expired" alert.

### WSL

WSL does not start on its own, and the distro stops when nothing keeps it running. `tbd install` in
WSL writes and enables the systemd user unit and prints the two Windows-side steps that keep it
alive: `loginctl enable-linger`, and a logon task that starts the distro (it runs `sleep infinity`,
not a second `tbd`, so systemd stays the only supervisor), plus the `.wslconfig` setting that stops
the VM from idling out. systemd must be enabled in `/etc/wsl.conf` (`[boot] systemd=true`).

### Windows

Use `tbd.exe` natively with the installed Edge; no WSL needed. `tbd install` registers a scheduled
task for your user that starts `tbd.exe run` at logon in a hidden console (`conhost.exe --headless`),
logging to `%LOCALAPPDATA%\tbd\tbd.log`. It needs no administrator rights. The data directory is
`%LOCALAPPDATA%\tbd`; the browser profile inside it is called `tbd`.

Edge keeps some per-user bookkeeping in the registry no matter which profile it runs
(`HKCU\Software\Microsoft\Edge\…`); for the `tbd` profile that is the key `PreferenceMACs\tbd`.

## Troubleshooting

* **Conditional Access / device compliance.** Tenants that require a compliant or managed device may
  refuse the headless browser (or demand sign-in from a managed browser). `tbd` cannot work around
  that. On an Entra-joined Windows PC, `implicit_signin = true` in `config.toml` lets Edge sign the
  daemon's profile in with the Windows account, which some policies require (it is off by default).
* **Long notifications.** FCM documents a 2048-byte data limit for topic messages; tbd's larger
  padding class is 2769 bytes. If FCM rejects it, tbd resends the message shortened to the smaller
  class automatically (logged as a warning).
* **"Session expired" alert.** Stop the daemon (`systemctl --user stop tbd`, or end the task), run
  `tbd login` again, start the daemon.
* **Latency.** FCM topic messages can take from seconds to a few minutes; high priority is requested,
  but battery savers and Doze may still delay them. Exempt Teams Bridge from battery optimisation.
* **Tap opens a browser tab instead of the PWA.** Only Chrome and Edge install a real WebAPK, which
  Android can route links to. Vivaldi, Firefox, Brave and others open a tab instead.
* **Nothing arrives.** `tbd status` shows whether notifications are being captured; `tbd run
  --log-level debug` logs their raw structure (including text — debug only). `tbd test-push` checks
  the FCM path on its own.
