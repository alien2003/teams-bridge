<!-- What changes and why. Link the issue it fixes, if any (Fixes #N). -->

## Checklist

- [ ] `mise run lint` and `mise run test` pass (`mise run lint-ci` too if `.github/` or `scripts/` changed)
- [ ] Windows-specific behaviour (`*_windows.go`, browser launch, paths, locks, `internal/service`) is covered by `mise run test-win` or the CI Windows job
- [ ] `mise run secrets-check` passes, with my stop list if I keep one (`TBD_NO_DENYLIST=1` otherwise)
- [ ] Only synthetic data in code, tests, docs and commit messages (Alice/Bob, "Project X", `19:uni01_abc@thread.v2`, zero GUIDs, `example.com`)
- [ ] Nothing logs content above debug level, and nothing sends, types or reacts in Teams
- [ ] Protocol untouched, or version bumped and vectors regenerated (`mise run gen-vectors`)
- [ ] `docs/DECISIONS.md` has an entry for a new decision or version change; `README.md` and `CHANGELOG.md` for a user-visible change
- [ ] Conventional Commit messages (`type(scope): summary`)
