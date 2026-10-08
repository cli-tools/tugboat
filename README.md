# tugboat

Multi-repository management for Gitea and GitHub, with repo-centric targets and optional foldouts (.tugboat.json).

## Quick Start

1) Install

**Prebuilt binaries:** Download from [GitHub Releases](https://github.com/cli-tools/tugboat/releases)
```bash
# Example for Linux amd64
VERSION=v0.9.0
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
   - **Gitea:** Settings → Applications → Generate Token with **read:organization** and **read:repository** scopes (add **write:repository** if you use `push`/`sync`).
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

5) Clone
```bash
tugboat clone rideshare infra mobile-app   # orgs + repo with foldouts
```

6) Daily
```bash
tugboat status           # shows empty/dirty/ahead/behind + archived/orphan flags
tugboat status --all     # also list every clean repository
tugboat pull             # update default branches only; skips dirty/local-only feature branches
tugboat push             # push ahead repos
tugboat sync             # sync default branches only; skips dirty/local-only feature branches
tugboat sync --remove-archived  # safely remove archived local checkouts
```

## Commands
- `clone [target ...]`   — org targets clone repos except configured exclusions; repo targets honor foldouts
- `status [target ...]`  — groups archived/attention/missing/empty state by target; `--all` expands clean rows
- `pull [target ...]`    — updates default branches only; clean fully-pushed feature branches auto-switch back first
- `push [target ...]`
- `sync [target ...]`    — reconciles org repository renames, then syncs default branches; `--remove-archived` removes verified-safe archived checkouts
- `list [target ...]`    — shows local + remote; flags archived/orphan
- `help`, `version`

`pull`, `push`, and `sync` print one numbered final result per repository by
default, including when no update is needed. Branch switches, rename preservation,
and replacement cloning are included in that result. Provider metadata requests
also report progress. Output uses plain lines on stdout, including redirected logs.
Add `--verbose` for intermediate identity downloads, check completions, Git
operations, and update-start messages. For example: `tugboat pull t1 --verbose`.


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

For organization targets, `sync` and `clone` automatically reconcile repository
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

Tugboat saves provider instance and repository ID in `.git/tugboat.json`. It
checks identity before fetching into local refs. `status` and `list` report
pending changes without moving folders or saving identity metadata. `pull` and
`push` skip pending repairs; run `sync` to resolve organization targets.
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
histories require manual identification. Candidate histories are cached in
temporary bare repositories within a command. Servers supporting filtered fetches
send commit ancestry without file contents.

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
maintenance: normal `sync` and `clone` stop and report its destination without
moving or updating that checkout. Resolve it locally, or use
`sync --remove-archived` to discard an identified transferred archive only after
every cleanup check passes. Dirty files, local-only commits, or stashes stop that
cleanup and leave the checkout intact. Replacement clones are prepared before
cleanup so a clone failure retains the original. Tugboat creates no extra
directory layout for transferred checkouts. Other provider instances require
manual handling.

Occupied destinations, symlinked checkout/Git directories, linked worktrees,
nested checkouts, explicit `core.worktree` paths, active Git operations, and
conflicting fetch/push URLs prevent automatic moves. Interrupted replacement
operations record a pending marker so a later `sync` or `clone` can resume.
Replacement cloning honors organization exclusions; normal `sync` does not clone
unrelated missing repositories.

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
- `pull` and `sync` only manage each repo's default branch.
- Clean feature branches with no unpushed commits are auto-switched back to the default branch before `pull` or `sync` continues.
- `pull` and `sync` skip dirty repos before pulling, rebasing, switching branches, or syncing.
- Feature branches with local-only commits are skipped rather than updated.
- `push` may still push committed-ahead changes; it is not skipped solely because the worktree is dirty.
- Repos left on a deleted feature branch are only switched when the branch has no commits outside the default branch.
- Repos with no commits locally or on origin are reported as empty and safely skipped by `pull`, `push`, and `sync`.
- When an empty repo gets its first commit, Tugboat can pull it from origin or push it from the local clone normally.
- Archived repos are flagged and skipped by `pull`, `push`, and normal `sync`; orphans are flagged as local but missing remote.
- `sync --remove-archived` permanently removes an archived checkout only after confirming its provider identity, origin URL, clean worktree, upstream default branch, and absence of local-only commits, stashes, linked worktrees, active Git operations, or remaining nested checkouts.
- Behind archived default branches are fast-forwarded before removal. Diverged repositories and repositories containing local work are retained. Ignored files are considered disposable and are removed with an otherwise-safe checkout.
- Explicit repo and foldout declarations remain in configuration after cleanup; later status runs report those checkouts as missing rather than treating them as errors.

## Agent skill

The repository includes an Agent Skills-compatible guide at [`skills/tugboat/SKILL.md`](skills/tugboat/SKILL.md). Official binary releases do not install it automatically.

For Codex, install the skill matching the v0.9.0 binary with:

```bash
SKILLS_DIR="${CODEX_HOME:-$HOME/.codex}/skills"
SKILL_VERSION=v0.9.0
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
