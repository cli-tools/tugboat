# tugboat

Multi-repository management for Gitea and GitHub, with repo-centric targets and optional foldouts (.tugboat.json).

## Quick Start

1) Install

**Prebuilt binaries:** Download from [GitHub Releases](https://github.com/cli-tools/tugboat/releases)
```bash
# Example for Linux amd64
VERSION=v0.10.1
curl -L "https://github.com/cli-tools/tugboat/releases/download/${VERSION}/tugboat-${VERSION}-linux-amd64" -o tugboat
chmod +x tugboat
sudo mv tugboat /usr/local/bin/
```

**From source:**
```bash
make build
sudo mv tugboat /usr/local/bin/
```

2) Create a personal access token (PAT) on your provider:
   - **Gitea:** Settings → Applications → Generate Token with **read:organization** and **read:repository** scopes (add **write:repository** if you use `sync` or `sync --push`).
   - **GitHub:** Settings → Developer settings → Personal access tokens → Generate with **repo** scope (grants read/write access to repositories, including private ones).

   **Verify your token works:**
   ```bash
   # GitHub — should list the repo (returns 404 if the token lacks access)
   gh api repos/{owner}/{repo} --jq .full_name

   # Gitea
   curl -s -H "Authorization: token YOUR_TOKEN" https://gitea.acme.com/api/v1/repos/{owner}/{repo} | jq .full_name
   ```

3) Configure `~/.config/tugboat/config.json`
```jsonc
{
  "providers": {
    "gitea":  { "type": "gitea",  "api_url": "https://gitea.acme.com", "token": "gitea-token" },
    "github": { "type": "github", "api_url": "https://api.github.com", "token": "ghp_your_token_here",
                "options": { "clone": { "protocol": "https" } } }
  },
  "targets": [
    { "provider": "gitea",  "org": "acme-rideshare", "path": "~/acme/rideshare", "name": "rideshare" },   // full org
    { "provider": "gitea",  "org": "acme-infra",     "path": "~/acme/infra",     "name": "infra" },       // full org
    { "provider": "github", "org": "acme",           "repo": "mobile-app",       "path": "~/acme/mobile-app", "name": "mobile-app" } // single repo (can have foldouts)
  ]
}
```

4) (Optional) Foldouts inside a repo target (`~/acme/mobile-app/.tugboat.json`)
```jsonc
{
  "repos": [
    { "name": "acme/mobile-api",       "target": "api" },
    { "name": "acme/mobile-infra",     "target": "infra" },
    { "name": "acme/mobile-k8s",       "target": "k8s" },
    { "name": "acme/mobile-design",    "target": "design" }
  ]
}
```

5) Sync (also clones missing repos)
```bash
tugboat sync rideshare infra mobile-app   # orgs + repo with foldouts
```

6) Daily
```bash
tugboat status           # shows empty/dirty/ahead/behind + archived/orphan flags
tugboat status --all     # also list every clean repository
tugboat sync --pull      # update default branches only; skips dirty/local-only feature branches
tugboat sync --push      # push ahead repos
tugboat sync             # sync default branches only; skips dirty/local-only feature branches
tugboat sync --remove-archived  # safely remove archived local checkouts
```

## Commands
- `sync [target ...]` — clone missing repos, reconcile org renames, then pull and push default branches
- `sync --pull [target ...]` — clone missing repos, reconcile org renames, and pull without pushing local commits
- `sync --push [target ...]` — push existing checkouts without cloning, pulling, or switching branches
- `sync --clone-only [target ...]` — clone and reconcile without updating branches; accepts `-E/--exclude-empty` and `-a/--include-archived`
- `sync --remove-archived [target ...]` — additionally remove safe in-org archives and obsolete renamed duplicates; also works with `--pull`
- `status [target ...]` — show archived/attention/missing/empty state; `--all` expands clean rows
- `list [target ...]` — show local + remote; `-a/--include-archived` includes archives; `--refresh` bypasses discovery caches
- `migrate`, `help`, `version`

`--pull`, `--push`, and `--clone-only` are mutually exclusive. The former
standalone `clone`, `pull`, and `push` commands have been removed in v0.10.0.
Update scripts using those commands to use the corresponding sync switches.

Sync checks and updates repositories incrementally, printing one numbered final
result as each repository finishes, including when no update is needed. Results
appear in completion order as `[done/total]`. Tugboat counts known local checkouts
and missing active repositories up front, without waiting for Git history checks.
The total adjusts if a rename changes the planned paths or a newly cloned or
updated parent declares additional foldouts. Ordinary verified
checkouts proceed before slower rename and archive reconciliation. Cloning, branch switches, rename preservation, and replacement
cloning are included in that result. Default output contains the initial `Sync:`
line, one final result per repository, and the summary. Output uses plain lines
on stdout, including redirected logs. Add `--verbose` for provider metadata and
reconciliation diagnostics, identity downloads, check completions, Git operations,
and update-start messages: `tugboat sync --pull t1 --verbose`.


## Clone exclusions

Add an optional `exclude` array to an organization target in your JSON config:

```json
{
  "provider": "gitea",
  "org": "t1",
  "path": "~/t1",
  "name": "t1",
  "exclude": ["benchmark-runs", "scratch-*"]
}
```

Patterns match the complete repository name, case-sensitively. Supported syntax
is Go's `path.Match` syntax: `*` matches any sequence, `?` matches one character,
and bracket classes such as `[abc]`, `[0-9]`, or `[^0-9]` match one character.
Backslashes escape pattern characters and must themselves be escaped in JSON.
Any matching pattern excludes the repository from that target's organization
clone; the skip message shows which pattern matched.

An omitted or empty array excludes nothing. Empty patterns, malformed globs,
paths containing `/`, and leading `!` exception patterns are errors. A nonempty
`exclude` array on a single-repository target is also an error. Invalid exclusions
fail config loading for every command, before provider requests or cloning.

Exclusions affect organization-wide cloning, including replacement clones made
by `sync`. They do not remove existing checkouts, prevent rename preservation,
or affect updates to existing checkouts, explicit repository targets, or foldouts.
No `.tugboatignore` file is read.

## Renamed and replaced repositories

For organization targets, receiving `sync` modes automatically reconcile repository
renames. If the old `perception` is renamed and archived as `perception-yolo`, and
a different repository takes the name `perception`, Tugboat:

1. Prepares a fresh clone of the replacement.
2. Moves the old checkout to `perception-yolo` and updates its origin.
3. Places the replacement at `perception`.

The preserved checkout keeps its branches, commits, stashes, and tracked,
untracked, and ignored files, including dirty work. Reconciliation does not
switch or rebase its branches. Archived checkouts remain skipped during normal
updates. `sync --remove-archived` can subsequently remove the preserved checkout
only when all existing cleanup checks pass.

Legacy checkouts whose old name redirects to an active repository are also
identified by the provider ID and matching upstream history. For example,
`mango` can be identified as the renamed `perception`. If `perception` already
has a verified local checkout, plain sync retains `mango` and reports the rename.
`sync --remove-archived` can remove this obsolete duplicate even though the
upstream is active. It keeps the canonical checkout and requires all local
branches, tags, and HEAD commits to be published upstream, a clean worktree,
no stashes, and all existing path, worktree, and operation safety checks. It
retains unresolved identities and old names reused by a replacement repository.

Tugboat saves provider instance and repository ID in `.git/tugboat.json`. It
checks identity before fetching into local refs. `status` and `list` report
pending changes without moving folders or saving identity metadata inside the
checkout. `sync --push` skips pending repairs; receiving sync modes resolve organization targets.
Explicit repo targets and foldouts report conflicts without relocating their
configured paths or editing configuration.

Older checkouts have no saved ID. A matching origin and history with the current
active upstream take the fast path: Tugboat records that repository's ID without
probing archives. This treats copied/shared history at the current upstream as
that upstream; manually identify a checkout first if it must instead follow an archive.
When current upstream history does not match, Tugboat compares committed history
against accessible archives on the provider, including archives transferred out
of the organization. Only a unique archive match permits
recovery. This also works after an attempted pull has fetched the replacement
into `origin/*`. Multiple archive matches, shallow histories, and ambiguous empty
histories require manual identification. Before downloading history for an active
upstream, Tugboat checks whether a freshly advertised remote commit already
exists locally and shares ancestry with HEAD. Candidate histories are cached in
temporary bare repositories within a command. Servers supporting filtered fetches
send commit ancestry without file contents.

Discovery results also persist under `$XDG_CACHE_HOME/tugboat/discovery-v1`
(normally `~/.cache/tugboat/discovery-v1`). History-verified local identities are
reused for up to 30 days when the checkout path, HEAD, origin, Git configuration,
shallow state, provider instance, and Git directory modification time match.
An explicit `.git/tugboat.json` always takes precedence. Current remote metadata
still resolves the cached repository ID, so renames and reused names retain their
identity checks. Dirty state and branch synchronization results are never cached.

`list` caches remote metadata and local listing results, including unresolved
identities, for five minutes. Remote entries are separate for each provider
instance and credential; local results also depend on remote metadata and any
explicit identity file. Local checks run in parallel using
`--workers`. All repository commands share the cache setup: `sync` (including
`--pull`, `--push`, and `--clone-only`) and `status` reuse verified local
identities and refresh the remote cache with live provider responses. Sync's
parallel scheduler recognizes externally cached identities as well as saved
checkout metadata. `status` also reuses unresolved discovery results, while
always checking current worktree and branch state. Sync does not reuse unresolved
listing results, and fetch, clone, rename, and removal confirmation checks always
bypass remote caches.

Use `--refresh` with `list`, `status`, or any `sync` mode to bypass discovery
cache reads and refresh local identity verification. Remote metadata in `sync`
and `status` is always live, regardless of this flag.
Missing, corrupt, expired, or unwritable caches fall back to normal discovery.
Removing the discovery cache directory is safe; the next run rebuilds it.

To identify an ambiguous checkout, obtain its **correct repository ID** from the
provider API and create `.git/tugboat.json` in that checkout, for example:

```json
{
  "version": 1,
  "provider_type": "gitea",
  "api_url": "https://gitea.example.com",
  "repository_id": 123,
  "full_name": "t1/perception-yolo",
  "origin": "gitea.example.com/t1/perception"
}
```

Use the configured provider type and API URL (without credentials, query strings,
or a trailing slash). `origin` is the existing origin normalized to
`host[:port]/owner/repo`, without credentials, scheme, or `.git`; local file
origins use their absolute path. The ID must identify the preserved repository,
not its replacement. Then run `tugboat sync TARGET`. Provider aliases and token
rotation do not change identity. A transfer out of the target organization leaves
maintenance: default sync and `sync --pull` discard verified-safe transferred
archives and prepare any replacement clone before deletion. Dirty files,
local-only branch or tag commits, stashes, or live/locked linked worktrees retain
the checkout and report the blocker as `[SKIP]`. These safety skips do not cause
a failure exit status; failed provider requests, clones, and Git operations are
`[ERROR]` and return a failure exit status. Missing, unlocked, prunable branch worktree
registrations do not block cleanup when their commits are still reachable from
the branch; those branch commits undergo the same unpublished-commit checks.
Cached remote-tracking refs do not count as unpublished local work. When commits
block cleanup, the single result line names only the affected branches, tags,
stash, or detached HEAD, with a commit count for each.
`sync --clone-only` and `sync --push` retain transferred checkouts. Active transfers
and other provider instances require manual handling. Tugboat creates no extra
directory layout for transferred checkouts.

Occupied destinations, symlinked checkout/Git directories, linked worktrees,
nested checkouts, explicit `core.worktree` paths, active Git operations, and
conflicting fetch/push URLs prevent automatic moves. Interrupted replacement
operations record a pending marker so a later receiving sync can resume.
All organization cloning honors exclusions. Receiving sync also clones missing
active repositories after reconciliation. A blocked checkout reserves only its
own reconciliation paths; other missing repositories in the org can still clone.

Automatic directory reconciliation uses Linux locks and atomic no-replace
renames, matching the supported release platforms. Other platforms report
pending changes and retain ordinary cloning and update commands.

## Provider Options (defaults)
- `clone.protocol`: https (ssh|https|auto)
- `sync.ff_only`: true
- `sync.fetch`: true

## Foldout rules
- Only on repo targets.
- Same provider; org may differ (`name` uses `org/repo`).
- Targets are relative paths under the parent repo; must be unique and no `..`.
- Depth = 1 (no recursive foldouts).

## Config locations
1. `$TUGBOAT_CONFIG`
2. `$XDG_CONFIG_HOME/tugboat/config.json`
3. `~/.config/tugboat/config.json`
4. `~/.tugboat.json`

## Safety
- ff-only pulls by default; diverged branches are rebased (rebase is aborted on conflicts).
- `sync --pull` and default `sync` only manage each repo's default branch.
- Clean feature branches with no unpushed commits are auto-switched back to the default branch before `sync --pull` or default `sync` continues.
- `sync --pull` and default `sync` skip dirty repos before pulling, rebasing, switching branches, or syncing.
- Feature branches with local-only commits are skipped rather than updated.
- `sync --push` may still push committed-ahead changes; it is not skipped solely because the worktree is dirty.
- Repos left on a deleted feature branch are only switched when the branch has no commits outside the default branch.
- Repos with no commits locally or on origin are reported as empty and safely skipped by all sync modes.
- When an empty repo gets its first commit, Tugboat can pull it from origin or push it from the local clone normally.
- Archived repos are flagged and skipped by normal sync modes; orphans are flagged as local but missing remote.
- `sync --remove-archived` permanently removes an archived checkout only after confirming its provider identity, origin URL, clean worktree, upstream default branch, and absence of local-only commits, stashes, linked worktrees, active Git operations, or remaining nested checkouts.
- Behind archived default branches are fast-forwarded before removal. Diverged repositories and repositories containing local work are retained. Ignored files are considered disposable and are removed with an otherwise-safe checkout.
- Explicit repo and foldout declarations remain in configuration after cleanup; later status runs report those checkouts as missing rather than treating them as errors.

## Agent skill

The repository includes an Agent Skills-compatible guide at [`skills/tugboat/SKILL.md`](skills/tugboat/SKILL.md). Official binary releases do not install it automatically.

For Codex, install the skill matching the v0.10.1 binary with:

```bash
SKILLS_DIR="${CODEX_HOME:-$HOME/.codex}/skills"
SKILL_VERSION=v0.10.1
mkdir -p "$SKILLS_DIR/tugboat"
curl -fsSL "https://raw.githubusercontent.com/cli-tools/tugboat/${SKILL_VERSION}/skills/tugboat/SKILL.md" \
  -o "$SKILLS_DIR/tugboat/SKILL.md"
```

For other Agent Skills-compatible tools, use the tool's configured skills directory instead.

## Build & Test
```bash
make build
go test ./...
```

## Release process
- Update `CHANGELOG.md` for every tagged release.
- Check whether `README.md` needs user-facing updates before committing a release.
- Run tests before tagging.
- Commit all release changes before creating the tag.
- Create an annotated `v*` tag for the release.
- Pushing a `v*` tag triggers the release workflow and publishes release artifacts.
