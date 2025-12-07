# Changelog

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
