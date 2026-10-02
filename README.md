# tugboat

Multi-repository management for Gitea and GitHub, with repo-centric targets and optional foldouts (.tugboat.json).

## Quick Start

1) Install

**Prebuilt binaries:** Download from [GitHub Releases](https://github.com/cli-tools/tugboat/releases)
```bash
# Example for Linux amd64
VERSION=v0.8.0
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
- `sync [target ...]`    — syncs default branches only; `--remove-archived` removes verified-safe archived checkouts
- `list [target ...]`    — shows local + remote; flags archived/orphan
- `help`, `version`

`pull`, `push`, and `sync` print progress as they discover, check, and update
repositories. During checking, each repository gets one numbered completion line
(`[NN/MM] Checked ...`). Updates report a numbered result, including
when no update is needed. Provider metadata requests also report progress. Scan
completion (`Checked`) is separate from the update result. Output uses plain lines
on stdout, so progress is also visible in redirected logs.
Add `--verbose` to `pull`, `push`, or `sync` for per-repository check stages
(including when `git fetch` starts) and update-start messages alongside normal
numbered progress. For example: `tugboat pull t1 --verbose`.


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

Exclusions only affect organization-wide `clone`. They do not remove existing
checkouts or affect other commands, explicit repository targets, or foldouts.
No `.tugboatignore` file is read.

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

For Codex, install the skill matching the v0.8.0 binary with:

```bash
SKILLS_DIR="${CODEX_HOME:-$HOME/.codex}/skills"
SKILL_VERSION=v0.8.0
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
