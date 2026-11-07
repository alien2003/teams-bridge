# Changelog

Notable changes for users, newest first. Versions follow [Semantic Versioning](https://semver.org/);
the format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). The section of a
version becomes the notes of its GitHub release (`scripts/release-notes.sh`); a version that is not
tagged yet is marked "Unreleased" and gets its date when it is tagged.

## [0.2.0] - Unreleased

### Added

* macOS 13 or newer, on Apple Silicon and Intel: every command works as on Linux and Windows. The
  data directory is `~/Library/Application Support/tbd`; Microsoft Edge, Chromium or Google Chrome
  is found in `/Applications` or `~/Applications`, and `browser` in `config.toml` also accepts an
  app bundle (`/Applications/Microsoft Edge Beta.app`). The daemon's browser uses its own mock
  keychain instead of the login keychain (no prompts, nothing shared with your own Edge; see the
  README for the trade-off), shows no macOS notifications of its own and presents the same Windows
  identity as on the other systems (user agent, client hints, `navigator.platform`).
* `tbd install` on macOS writes and loads the LaunchAgent `~/Library/LaunchAgents/tbd.plist` (start
  at login, restart after a crash, logs in `tbd.log`); `tbd start` and `tbd stop` go through
  `launchctl`, and `tbd status` shows a `launchd:` line. `tbd install` points out a binary that
  macOS still has in quarantine.
* CI runs the Go tests, against a real Edge, on GitHub's macOS runners for both architectures, next
  to Linux and Windows.

### Changed

* `tbd login` also finishes when the Teams window is closed while the browser keeps running (as
  on macOS after its last window, or with a browser that stays in the background); it then closes
  the browser itself.
* At its next start, the daemon ends a headless browser that a crashed or killed daemon left
  running on its profile (log line `killed a browser left by a previous tbd`), instead of failing
  to start a new one.

### Releases

* `tbd-darwin-arm64` (macOS, Apple Silicon) and `tbd-darwin-amd64` (macOS, Intel), covered by
  `SHA256SUMS` and the build provenance attestation; before publishing, the release workflow runs
  each one natively on a GitHub macOS runner of its architecture. They are not signed with an Apple
  Developer ID and not notarized.

## [0.1.0] - 2026-11-07

The first public release.

### Daemon (`tbd`, Linux, WSL and Windows)

* Keeps Teams web open in a headless Microsoft Edge (or Chromium/Chrome) with its own profile and
  catches every notification Teams raises: from the page and from its service worker, with
  de-duplication and flood control.
* Pushes each notification end-to-end encrypted (ChaCha20-Poly1305, padded to 1024 or 2048 bytes)
  as a Firebase Cloud Messaging data message to a topic derived from the pairing secret; wire
  protocol v1 in `proto/PROTOCOL.md`.
* A tap opens the right chat when Teams' chat list shows the message (Teams' own "Copy link" form);
  otherwise, and for channel posts, it opens Teams.
* Alerts on the phone when the Teams session expires (it recognises Teams' sign-in prompts in
  English, German, French, Spanish, Portuguese, Italian, Dutch, Polish, Ukrainian and Russian),
  when the browser keeps failing and when the daemon stops; a heartbeat every 6 hours.
* Commands: `login`, `pair` (QR code and 26-character code), `run`, `stop`, `start`, `status`,
  `test-push`, `install` / `uninstall` (systemd user unit or Windows scheduled task, `--dry-run`),
  `version`.
* Logs only the structure of notifications (shapes, lengths, counts) unless `--log-level debug`.

### App (Teams Bridge, Android 9 or newer)

* Pairing by QR code or typed code; the secret is kept wrapped by the Android Keystore.
* Shows notifications and alerts and opens a tap in the browser or Teams PWA you pick; warns when
  no heartbeat arrived for 12 hours.

### Releases

* Signed `teams-bridge.apk`, `tbd-linux-amd64`, `tbd-windows-amd64.exe`, the R8 mapping and
  `SHA256SUMS`, built by GitHub Actions with a build provenance attestation for each of them; updates through
  Obtainium with the release certificate pinned.
