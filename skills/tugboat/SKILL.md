---
name: tugboat
description: Expert on tugboat multi-repo management tool. Use when user asks about tugboat CLI commands, configuration, foldouts, .tugboat.json, cloning repos, syncing, or managing multiple git repositories across Gitea/GitHub.
---

# Tugboat - Multi-Repository Management

Tugboat manages multiple git repositories across Gitea and GitHub with parallel operations, org-wide cloning, and "foldout" subrepos.

## Core Concepts

### Targets
A **target** is a named entry in your config that points to either:
- **Org target**: All repos in a Gitea/GitHub organization
- **Repo target**: A single repository (can have foldouts)

### Foldouts (Subrepos)
A **foldout** is a repo cloned *inside* another repo, managed via `.tugboat.json`. The parent repo's `.gitignore` hides the foldout directories so git treats them as separate repos.

```
~/rideshare/                  # Parent repo (acme/rideshare)
├── .git/                     # Parent's git
├── .tugboat.json             # Defines foldouts
├── .gitignore                # Contains /api/, /batch/, etc.
├── api/                      # Foldout: acme/rideshare-api
│   └── .git/                 # Foldout's own git
├── batch/                    # Foldout: acme/rideshare-batch
│   └── .git/
└── README.md
```

## CLI Commands

```bash
tugboat sync --clone-only [targets...]    # Clone repos (honors foldouts for repo targets)
tugboat status [targets...]   # Show grouped empty/dirty/ahead/behind/archived/orphan
tugboat status --all          # Expand clean repository rows
tugboat sync --pull [targets...]     # Update default branches safely
tugboat sync --push [targets...]     # Push repos that are ahead
tugboat sync [targets...]     # Clone missing repos, then pull and push default branches safely
tugboat sync --remove-archived [targets...]  # Remove verified-safe archived checkouts
tugboat list [targets...]     # List local vs remote repos
tugboat migrate               # Migrate a v1 config to v2
tugboat help                  # Show help
tugboat version               # Show version
```

### Options
- `--pull` - Clone missing repos and pull without pushing
- `--push` - Push existing repos without cloning, pulling, or switching branches
- `--clone-only` - Clone and reconcile without updating branches
- Direction switches are mutually exclusive; standalone `clone`, `pull`, and `push` commands are removed in v0.10.0. Update scripts to use the corresponding sync switches.
- `-w, --workers N` - Parallel workers (default: CPU cores)
- `-d, --debug` - Show timing info (status only)
- `--verbose` - Show provider metadata, reconciliation, identity downloads, check completions, Git operations, and update-start messages (sync). Default output has the initial sync line, one final result per repository as it completes, and the summary, numbered `[done/total]`. Known local and remote checkout paths are counted up front; the total adjusts for renames and newly discovered foldouts. Verified checkouts update before slower identity reconciliation; cloning also reports incrementally.
- `--all` - Include clean repository rows (status only)
- `--remove-archived` - Also remove safe in-org archives and obsolete renamed duplicates (receiving sync only)
- `-E, --exclude-empty` - Skip empty repos (`sync --clone-only`)
- `-a, --include-archived` - Include archived repos (sync --clone-only or list)

## Configuration

### Config File Locations (in order)
1. `$TUGBOAT_CONFIG`
2. `$XDG_CONFIG_HOME/tugboat/config.json`
3. `~/.config/tugboat/config.json`
4. `~/.tugboat.json`

### Config Structure

```json
{
  "workers": 16,
  "providers": {
    "gitea": {
      "type": "gitea",
      "api_url": "https://gitea.example.com",
      "token": "your-gitea-token",
      "options": {
        "clone": { "protocol": "https" },
        "sync": { "ff_only": true }
      }
    },
    "github": {
      "type": "github",
      "api_url": "https://api.github.com",
      "token": "ghp_your_token",
      "options": {
        "clone": { "protocol": "https" },
        "sync": { "ff_only": true }
      }
    }
  },
  "targets": [
    { "provider": "gitea", "org": "myteam", "path": "~/myteam", "name": "myteam" },
    { "provider": "github", "org": "myorg", "repo": "monorepo", "path": "~/monorepo", "name": "monorepo" }
  ]
}
```

### Target Types

**Org target** - clones all repos in the org:
```json
{ "provider": "gitea", "org": "myteam", "path": "~/myteam", "name": "myteam" }
```

**Repo target** - clones single repo (can have foldouts):
```json
{ "provider": "github", "org": "myorg", "repo": "rideshare", "path": "~/rideshare", "name": "rideshare" }
```

### Clone Exclusions

Organization targets can set `"exclude": ["benchmark-runs", "scratch-*"]` to skip matching repositories during receiving sync. Patterns match complete repository names case-sensitively using Go's `path.Match` syntax (`*`, `?`, bracket classes, and escaping). An omitted or empty list excludes nothing.

Empty patterns, malformed globs, `/` paths, and leading `!` exceptions fail config loading. Nonempty exclusions on single-repository targets are also errors. Exclusions also apply to replacement clones made by `sync`. They do not remove existing checkouts or prevent rename preservation, and do not affect updates to existing checkouts, explicit repository targets, or foldouts. No `.tugboatignore` file is read.

## Renamed and Replaced Repositories

For organization targets, Receiving `sync` modes preserve a renamed checkout at its
current remote name, update origin, and clone any replacement into the freed
name. Dirty files, ignored files, local commits, branches, and stashes stay with
the preserved checkout. Explicit repo targets and foldouts report conflicts
without moving configured paths. Receiving sync also clones missing active repositories, honoring organization
exclusions. Blocked reconciliation reserves its own paths without preventing
unrelated missing checkouts from being cloned.

Provider instance and repository ID are saved in `.git/tugboat.json`, checked
before fetching local refs. Existing checkouts without IDs first verify matching
origin and history against the current active upstream without probing archives.
Copied/shared history at that upstream is treated as belonging to it; manually
identify a checkout first if it must follow an archive instead. Unrelated histories
use unique history recovery from accessible provider archives, including transfers.
Multiple archive matches, shallow histories,
or ambiguous empty histories require manual identification; follow the README's
`.git/tugboat.json` example using the correct provider ID. History probes request
commit ancestry without file contents when the server supports filtered fetches.
Never identify a checkout by the replacement's ID merely because its name matches.

`status`/`list` report pending changes; `sync --push` skips pending repairs; receiving sync repairs org targets. Use
`sync TARGET` to reconcile organization targets. Moves refuse occupied paths,
symlinks, linked worktrees, nested checkouts, explicit worktree paths, active Git
operations, and conflicting fetch/push URLs. Interrupted replacements resume on
a subsequent receiving sync. `--remove-archived` retains every cleanup guard
and verifies the repository ID before deletion, after replacement publication.
Automatic directory moves currently use Linux primitives; other platforms detect
changes and retain ordinary sync operations.

Old upstream names that redirect to active repositories are verified by provider
ID and matching history. When the canonical checkout already exists, plain sync
reports the rename and retains the old checkout. `--remove-archived` can discard
the obsolete duplicate, even for an active upstream, only after verifying that
the canonical checkout is the same repository and all local refs are published.
Dirty files, stashes, linked worktrees, active operations, ambiguous identities,
and names reused by replacements prevent duplicate cleanup.

Transfers out of the target org leave maintenance. Default sync and `sync --pull`
discard verified-safe transferred archives, staging any replacement before
removal. Dirty work, unpublished branch/tag commits, stashes, and live or locked
worktrees prevent deletion. Missing, unlocked, prunable branch worktrees do not
block cleanup when their commits remain reachable from the branch; all branch
commits still undergo unpublished-commit checks. Remote-tracking caches do not
count as unpublished local work. Cleanup blockers name affected branches, tags,
stash, or detached HEAD and their commit counts in the single result line.
Protected transferred checkouts report `[SKIP]` without failing the command.
Failed provider requests, clones, and Git operations report `[ERROR]` and return
a failure exit status.
No extra directory layout is
created. Clone-only and push-only retain transfers. Active transfers and other
provider instances require manual handling.

## Foldouts (.tugboat.json)

Place `.tugboat.json` in a repo target's root to define subrepos:

```json
{
  "repos": [
    { "name": "myorg/api-service", "target": "api" },
    { "name": "myorg/web-frontend", "target": "web" },
    { "name": "myorg/shared-lib", "target": "lib" }
  ]
}
```

- `name`: `org/repo` format (same provider as parent)
- `target`: Local directory name (relative to parent)

### Foldout Rules
1. Only on repo targets (not org targets)
2. Same provider as parent; org can differ
3. Targets must be unique, no `..` paths
4. Depth = 1 (no recursive foldouts)

## Git Subrepo Pattern

Tugboat uses `.gitignore` to hide foldouts from the parent repo's git:

**.gitignore in parent repo:**
```gitignore
/api/
/web/
/lib/
```

This means:
- Parent repo ignores foldout directories
- Each foldout has its own `.git/` and is a full repo
- You commit to parent and foldouts independently
- No git submodules or subtrees needed

### Adding a New Foldout

1. Add entry to `.tugboat.json`:
   ```json
   { "name": "myorg/new-service", "target": "new-service" }
   ```

2. Add to parent's `.gitignore`:
   ```gitignore
   /new-service/
   ```

3. Clone:
   ```bash
   tugboat sync --clone-only rideshare   # Re-clones, picks up new foldout
   ```

## Status Output

```
Target: rideshare  /root/rideshare

Archived (1)
  ARCHIVED  web    master

Attention (1)
  DIVERGED  batch  master  dirty, 3 ahead, 2 behind

Empty (1)
  EMPTY     api    main

Clean (10 hidden; use --all)

Summary: 13 repositories: 10 clean, 1 empty, 1 dirty, 1 ahead, 1 behind, 1 diverged, 1 archived, 0 orphan, 0 missing, 0 errors
```

Status is grouped by target and uses paths relative to the target root. Archived repositories remain in the Archived group even when details such as `dirty` or `2 ahead` block cleanup. Use `--all` to expand the Clean group. Configured repo targets and foldouts that are absent locally are reported as missing.

Empty repositories are valid. Sync modes skip a repository when neither side has a commit. If the first commit appears on origin, `sync --pull` or default `sync` initializes the local branch; if it appears locally, `sync --push` or default `sync` creates the remote branch.

## Examples

### Clone everything
```bash
tugboat sync --clone-only                 # All targets
tugboat sync --clone-only myteam rideshare  # Specific targets
```

### Daily workflow
```bash
tugboat status    # See what needs attention
tugboat sync --pull      # Safely update default branches
# ... do work ...
tugboat sync --push      # Push your commits
```

### Sync all repos
```bash
tugboat sync      # Pull then push, skips dirty repos
tugboat sync --remove-archived  # Fast-forward and remove safe archived checkouts
```

### Check what's remote vs local
```bash
tugboat list              # All targets
tugboat list -a           # Include archived
```

## Safety Features

- **Default-branch updates**: Receiving sync modes update default branches, switching away from clean, fully pushed feature branches when safe
- **ff-only first**: Pulls start ff-only; diverged branches fall back to a merge-preserving rebase and abort cleanly on conflicts
- **Dirty skip**: Receiving sync modes skip repos with uncommitted changes before switching, pulling, rebasing, or syncing
- **Local-commit protection**: Feature branches with local-only commits are not switched or updated
- **Empty-repo support**: Repositories with no commits are reported and skipped without errors
- **No force push**: Never force pushes
- **Archived handling**: Normal sync modes skip archived repos
- **Safe archive cleanup**: `sync --remove-archived` confirms archive metadata and origin identity, fast-forwards only, and refuses dirty worktrees, local-only commits or stashes, active operations, linked worktrees, and remaining nested checkouts. Ignored files are disposable.
- **Orphan detection**: Flags local repos missing from remote
