# Security policy

## Reporting a vulnerability

Report privately through GitHub's **private vulnerability reporting**:
<https://github.com/alien2003/teams-bridge/security/advisories/new> (the *Security* tab →
*Report a vulnerability*). Please do not open a public issue, discussion or pull request for a
vulnerability.

This is a spare-time project with one maintainer: reports are handled on a best-effort basis, with
no promised response or fix times. You will hear back in the advisory thread; a confirmed issue is
fixed in a new release and published as a GitHub security advisory, crediting you if you wish.

**Never include** in a report (or anywhere else on GitHub):

* log lines at debug level (`--log-level debug` logs notification titles, bodies and raw data),
  message text, sender, chat, team or channel names, Teams ids or URL paths, tenant names;
* `config.toml`, the pairing code or secret, a picture of the pairing QR code, the derived key or
  topic, a sealed payload, the FCM service-account key, OAuth tokens, cookies or browser profiles.

Info-level log lines hold only structure (shapes such as `aaaa 99`, lengths, counts, booleans, key
names); those are fine, after replacing your user name in paths. If a proof of concept needs real
data, describe its shape instead, or say so in the report so a safe way can be agreed.

## Supported versions

Only the latest release (0.x) gets fixes. Debug builds and older releases do not.

## Threat model (summary)

Teams Bridge forwards Microsoft Teams notifications from a daemon on your computer (`tbd`) to an
Android app, one way: daemon → Firebase Cloud Messaging (FCM) → phone. There is no server of ours,
no relay and no channel back from the phone. The wire protocol is specified in
[`proto/PROTOCOL.md`](proto/PROTOCOL.md).

**What is protected**

* the 16-byte pairing secret, from which the encryption key and the FCM topic are derived (HKDF);
* the content of notifications: title, sender, text and the chat link;
* the FCM service-account key (it stays on the computer running `tbd`; CI never has it);
* the Android release signing key and the integrity of the published binaries.

**Who sees what**

* **Google/FCM and the network** see ciphertext (ChaCha20-Poly1305, padded to exactly 1024 or 2048
  bytes), its size class, the timing and the topic name, which is derived from the secret and does
  not identify a person.
* **Someone who has the pairing secret** can subscribe to the topic (the Firebase client
  configuration is public, it is inside the APK) and read what is sent; they cannot send without
  the project's service-account key. A leaked secret is fixed by `tbd pair --new` and pairing again.
* **Someone holding the unlocked phone** sees what the app shows, as with any notification.
* **The phone app** opens only absolute `http(s)` links without user info, and the daemon builds
  them only for allow-listed Teams origins. Input from the Teams page is treated as untrusted
  (strict parsing, size caps, rate limits), because any script on that page can call the hook.
* **Logs** at the default level hold structure only; content appears only at debug level.

**Supply chain**

* `teams-bridge.apk` is signed by the release key whose certificate SHA-256 is in
  [`android/release-cert.sha256`](android/release-cert.sha256) and in the README.
* Every release asset, `SHA256SUMS` included, is built by GitHub Actions from the tagged commit
  and has a build provenance attestation (`gh attestation verify`); `SHA256SUMS` lists the other
  assets' checksums.
* `tbd-windows-amd64.exe` has no Authenticode signature, so Windows SmartScreen may warn.
* Actions are pinned by commit SHA; dependency updates come through Dependabot with a cooldown.

## Out of scope

* Microsoft Teams itself, the Teams web client, Microsoft Edge/Chromium, Android, Google Play
  services and FCM, and whether using this project fits your organisation's policies or Teams'
  terms.
* Malware or another user with access to your computer account: it can read the Teams session and
  `config.toml` directly.
* A phone that is rooted, compromised or unlocked in someone else's hands.
* Debug builds (`teams-bridge-debug.apk`, `tbd run --log-level debug`) and self-built APKs signed
  with your own key.
* Loss of the pairing secret by the user (pair again with `tbd pair --new`).
* Denial of service through FCM delays or quotas, and the timing and size information that FCM
  necessarily sees.
