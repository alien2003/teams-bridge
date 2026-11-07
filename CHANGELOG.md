# Changelog

Notable changes for users, newest first. Versions follow [Semantic Versioning](https://semver.org/);
the format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). The section of a
version becomes the notes of its GitHub release (`scripts/release-notes.sh`); a version that is not
tagged yet is marked "Unreleased" and gets its date when it is tagged.

## [0.1.0] - Unreleased

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
* Alerts on the phone when the Teams session expires, when the browser keeps failing and when the
  daemon stops; a heartbeat every 6 hours.
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
  `SHA256SUMS`, built by GitHub Actions with build provenance attestations; updates through
  Obtainium with the release certificate pinned.
