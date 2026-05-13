# Changelog

## v0.6.2 - 2026-05-13
- Make the update-safety design explicit: `pull` and `sync` skip dirty repos before pulling, rebasing, switching branches, or syncing.
- Clarify that `push` can still push committed-ahead changes from a dirty worktree.
- Document the release process, including the requirement to update this changelog for every tagged release.

## v0.6.1 - 2026-03-29
- Fall back to a rebase pull when an ff-only pull fails because branches have diverged.
- Abort failed rebase attempts so repositories are not left mid-rebase.

## v0.6.0 - 2026-03-22
- Auto-switch repositories off deleted feature branches to the default branch when it is safe.

## v0.5.0 - 2026-03-03
- Embed HTTPS authentication in Git operations with an ephemeral credential helper.

## v0.4.8 - 2026-02-18
- Improve clone error messages with a hint about token permissions.

## v0.4.7 - 2026-02-15
- Fix `pull` failures for repositories left on deleted upstream branches.

## v0.4.6 - 2026-02-12
- Suppress Git subprocess output unless the command fails.

## v0.4.5 - 2026-01-20
- Add CI and release workflows for GitHub Actions.

## v0.4.1 - 2025-12-11
- Add 0BSD license.

## v0.4.0 - 2025-12-11
- Add config-level `workers` setting to set default parallelism.
- Fix boolean config options (`ff_only`) to respect explicit `false` values.

## v0.3.0 - 2025-12-10
- Repo-centric targets: define single repos with optional foldouts (`.tugboat.json`).
- Provider options: `clone.protocol` (ssh/https/auto), `sync.ff_only`.
- GitHub provider support alongside Gitea.
- Parallel worker pool for all commands (`-w`/`--workers` flag).
- Detect orphaned repos (local but missing from remote).
- Capture and display remote errors in status output.

## v0.2.0 - 2025-12-08
- Clone, sync, status, and list accept optional organization names so you can target a subset of configured orgs.
- Archived repositories are excluded from sync/pull/push by default, and `list` hides them unless `--include-archived` is supplied.
- Repository listings (list/status/sync logs) are sorted alphabetically for easier scanning.
- `tugboat clone` now includes empty repositories by default; pass `--exclude-empty` to skip them when desired.
