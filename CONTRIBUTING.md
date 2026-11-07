# Contributing

Thanks for looking. Bug reports, ideas and pull requests are welcome; for a vulnerability, follow
[`SECURITY.md`](SECURITY.md) instead of opening an issue. Small, focused changes are the easiest to
review. For anything larger (a new capture path, a protocol change, a new dependency), open an
issue first.

## Setup

Every toolchain (Go, Java, Gradle, staticcheck, the workflow and shell linters) comes from
[mise](https://mise.jdx.dev/), pinned in `mise.toml`, and every cache is redirected into the
repository (`.xdg/`, `.tools/`, `.tmp/`). Run toolchain commands through mise from the repository
root, never a bare `go` or `gradle`:

```sh
mise trust && mise install && mise run hooks   # once per checkout; `hooks` installs the pre-push check
mise run edge                                  # a headless Edge for the tests, into .tools/edge/
```

`mise tasks` lists every task; anything that is not a task runs as `mise exec -- <cmd>`.

## Checks

| Command | What | When |
|---|---|---|
| `mise run lint` | gofmt, go vet and staticcheck for Linux and Windows | before every commit |
| `mise run lint-ci` | actionlint and zizmor over `.github/`, `bash -n` and shellcheck over every script | after touching `.github/` or `scripts/` |
| `mise run test` | lint, the script tests, the Go unit and E2E tests in a real headless Edge, the Android JVM tests | before every pull request |
| `mise run test-go` | only the Go tests (`TBD_REQUIRE_BROWSER=1`, so a missing browser fails instead of skipping) | inner loop |
| `mise exec -- go test -count=1 -run TestX ./internal/hook/` | one package | inner loop (set `TBD_REQUIRE_BROWSER=1` too) |
| `mise run test-scripts` | table tests of `version.sh`, `release-notes.sh`, `obtainium-link.sh` | after touching them |
| `mise run secrets-check` | key and token patterns, forbidden files, the stop list (below) | before every commit |
| `mise run build` | `dist/tbd`, `dist/tbd.exe`, `dist/teams-bridge-debug.apk` | to try a change |

CI (`.github/workflows/ci.yml`) runs lint, lint-ci, the script tests, `secrets-check` without a stop
list, the Go tests on Linux and natively on Windows against the runners' Edge, and the Android tests
and debug build. `mise run test-win` runs the Windows tests from WSL in a sandbox folder on the
Windows host (`TBD_WIN_SANDBOX`); without such a setup, rely on the CI Windows job.
`mise run check-outside` verifies that nothing was written outside the repository except what
[`OUTSIDE.md`](OUTSIDE.md) lists; it compares against `.tmp/stamp` and is optional for contributors.

## Rules that keep the project safe to publish

* **No real data, ever.** Real tenant, team, channel, chat, message and person names and ids, real
  message text, e-mail addresses and screenshots of a real Teams never go into code, tests,
  fixtures, docs, issues or commit messages. Use obviously synthetic values of the real shape:
  Alice, Bob, Carol, Dave, "Project X", `19:uni01_abc@thread.v2`,
  `8:orgid:00000000-0000-4000-8000-00000000a11c`, `example.com`.
* **Tests never touch a real Teams account.** Reproduce Teams behaviour in `test/faketeams` (a fake
  Teams site served to a real headless Edge) and the FCM side with `internal/fcm/fcmtest`. Nothing
  in the project sends, types or reacts in Teams.
* **Logs carry structure, not content.** Above debug level, log only shapes, lengths, counts,
  booleans and key names (`hook.Shape`, `hook.DescribeData`); titles, bodies, names and ids never
  appear in a log line or an error.
* **The wire protocol v1 is frozen.** A change to `proto/PROTOCOL.md` or `proto/vectors.json` is a
  version bump; regenerate the vectors with `mise run gen-vectors`, never by hand.
* **Decisions are written down.** A non-obvious decision, a measured fact or a version change gets
  an entry in [`docs/DECISIONS.md`](docs/DECISIONS.md) (next number, newest last); user-visible
  changes go into `README.md` and [`CHANGELOG.md`](CHANGELOG.md) (under the unreleased version).

### The stop list and the pre-push check

`secrets-check` and the pre-push hook also refuse a **stop list**: a git-ignored file
`.local/denylist.txt` with one string per line (names and ids that must never be published; a GUID
is also matched without its dashes; matching ignores case). The list itself is the confidential
part, so it never leaves the machine it is on: it is not committed, not in CI, and the checks print
only file and commit names, never the matching text. Keep your own list with values from your own
tenant if you work on capture code against real Teams, or set `TBD_NO_DENYLIST=1` to run the checks
without one.

`mise run hooks` installs `.git/hooks/pre-push`, which runs `scripts/prepush-check.sh` over every
commit and annotated tag being pushed: the stop list and the secret patterns over each commit's
files and message, author, committer and tagger e-mail (only the maintainer's address or a
`users.noreply.github.com` one), and a `+0000` time zone. To match, commit with your GitHub noreply
address and `TZ=UTC git commit …` (or `git config` the address and export `TZ=UTC` in that shell).
`mise run prepush-check` runs the same check over every ref.

## Commits and pull requests

* [Conventional Commits](https://www.conventionalcommits.org/): `type(scope): summary`, e.g.
  `fix(hook): …`, `feat(android): …`, `docs: …`. The release notes are built from `feat`, `fix` and
  `perf` subjects when a version has no changelog section, so write them for users.
* One concern per pull request; the template's checklist is what CI cannot check.
* Contributions are under the project's licence, the [WTFPL v2](LICENSE); there is no CLA.

## Dependencies

* **Dependabot** (`.github/dependabot.yml`) proposes updates weekly for GitHub Actions (pinned by
  commit SHA), Go modules and the Android Gradle dependencies, after a cooldown of 7 days (30 for a
  major version), one grouped pull request per ecosystem. Versions are recorded in
  `docs/DECISIONS.md`, so a Dependabot pull request is merged together with its DECISIONS line.
* **Updated by hand** (Dependabot does not read them): the tools in `mise.toml` (`mise outdated`
  lists newer versions; bump, `mise install`, `mise run test`), `MISE_VERSION` in
  `.github/workflows/ci.yml` and `release.yml` (both at once, matching the mise used locally),
  `buildToolsVersion` in `android/app/build.gradle.kts` together with the SDK packages in
  `scripts/android-sdk.sh`.

## Releasing (maintainer)

1. Give the version's section in `CHANGELOG.md` its date (`## [X.Y.Z] - YYYY-MM-DD`) and commit.
2. `TZ=UTC git tag -a vX.Y.Z -m "vX.Y.Z"` on that commit on `main` (`vX.Y.Z-rc.N` for a
   prerelease) and push the tag. `.github/workflows/release.yml` re-runs CI, builds and signs the
   assets with the release key from the `release` environment, attests them and publishes the
   release with the changelog section as its notes (an rc without its own section gets the tag
   message, or else the `feat`/`fix`/`perf` commits). Details: `docs/DECISIONS.md` D23.
