# Changelog

## v0.9.0 - 2026-10-08

- Track provider repository IDs before fetching or updating checkouts, preventing old clones from being confused with replacement repositories that reuse their names.
- Reconcile organization repository renames during `sync` and `clone`: preserve local work, update origin, and stage replacement clones with interruption recovery and no-overwrite moves on Linux.
- Recover legacy checkouts from unique archived history matches; report ambiguous identities, explicit-target conflicts, and pending repairs in status/list output.
- Verify repository IDs during archived cleanup and honor organization exclusions for replacement cloning.
- Verify ordinary legacy checkouts against their current upstream before probing archives, and filter identity fetches to commit ancestry where supported.
- Print one final result per repository by default; include branch switches and rename/replacement outcomes in that line, with intermediate activity available through `--verbose`.
- Verify provider-confirmed origin redirects before repairing old URLs. Detect archived transfers outside an organization and stop normal maintenance; explicit archive cleanup may remove them only after all safety checks pass, preparing replacements before deletion.

## v0.8.0 - 2026-10-02

- Add per-organization `exclude` patterns to the JSON config so `clone` can skip repositories by name, including wildcards such as `benchmark-*`.
- Reject invalid exclusion patterns and exclusions on single-repository targets before cloning starts. Omitted or empty exclusion lists preserve existing behavior.
- Reduce default progress output to numbered check completions and update results, with aligned counts. Add `--verbose` to `pull`, `push`, and `sync` for detailed check stages and update-start messages.

## v0.7.1 - 2026-10-02

- Show incremental repository progress during `pull`, `push`, and `sync`, including checks, fetches, provider metadata requests, and updates.
- Report per-repository start messages and completion counts, with explicit results when no update is needed. Plain-text output also works in redirected logs.
- Keep scan completion separate from update results, and report archived cleanup completion only after removal succeeds or the checkout is retained.

## v0.7.0 - 2026-07-19

- Group status output by target and repository state, use relative paths, collapse clean rows by default, and report archived, orphaned, and missing counts in the summary.
- Add `status --all` to expand clean repository rows.
- Add `sync --remove-archived` to permanently remove archived checkouts only after provider, origin, worktree, branch, ref, worktree-link, operation, and nesting safety checks pass.
- Skip archived repositories during normal pull, push, and sync operations, and report configured checkouts removed by cleanup as missing.

## v0.6.4 - 2026-07-13
- Support repositories with no commits without reporting a branch-detection error.
- Report empty repositories explicitly and skip pull, push, and sync when neither the local clone nor origin has commits.
- Handle the first commit appearing locally or on origin so it can be pushed or pulled normally.

## v0.6.3 - 2026-06-03
- Clear inherited Git credential helpers before injecting Tugboat's ephemeral HTTPS token helper, so stale global helpers cannot override the configured provider token.

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
