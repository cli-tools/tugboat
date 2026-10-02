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
tugboat clone [targets...]    # Clone repos (honors foldouts for repo targets)
tugboat status [targets...]   # Show grouped empty/dirty/ahead/behind/archived/orphan
tugboat status --all          # Expand clean repository rows
tugboat pull [targets...]     # Update default branches safely
tugboat push [targets...]     # Push repos that are ahead
tugboat sync [targets...]     # Pull then push default branches safely
tugboat sync --remove-archived [targets...]  # Remove verified-safe archived checkouts
tugboat list [targets...]     # List local vs remote repos
tugboat migrate               # Migrate a v1 config to v2
tugboat help                  # Show help
tugboat version               # Show version
```

### Options
- `-w, --workers N` - Parallel workers (default: CPU cores)
- `-d, --debug` - Show timing info (status only)
- `--verbose` - Show detailed check stages and update-start messages (pull, push, sync)
- `--all` - Include clean repository rows (status only)
- `--remove-archived` - Permanently remove safe archived checkouts (sync only)
- `-E, --exclude-empty` - Skip empty repos (clone only)
- `-a, --include-archived` - Include archived repos

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

Organization targets can set `"exclude": ["benchmark-runs", "scratch-*"]` to skip matching repositories during `clone`. Patterns match complete repository names case-sensitively using Go's `path.Match` syntax (`*`, `?`, bracket classes, and escaping). An omitted or empty list excludes nothing.

Empty patterns, malformed globs, `/` paths, and leading `!` exceptions fail config loading. Nonempty exclusions on single-repository targets are also errors. Exclusions do not remove existing checkouts or affect other commands, explicit repository targets, or foldouts. No `.tugboatignore` file is read.

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
   tugboat clone rideshare   # Re-clones, picks up new foldout
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

Empty repositories are valid. `pull`, `push`, and `sync` skip a repository when neither side has a commit. If the first commit appears on origin, `pull` or `sync` initializes the local branch; if it appears locally, `push` or `sync` creates the remote branch.

## Examples

### Clone everything
```bash
tugboat clone                 # All targets
tugboat clone myteam rideshare  # Specific targets
```

### Daily workflow
```bash
tugboat status    # See what needs attention
tugboat pull      # Safely update default branches
# ... do work ...
tugboat push      # Push your commits
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

- **Default-branch updates**: Pull and sync update default branches, switching away from clean, fully pushed feature branches when safe
- **ff-only first**: Pulls start ff-only; diverged branches fall back to a merge-preserving rebase and abort cleanly on conflicts
- **Dirty skip**: Pull and sync skip repos with uncommitted changes before switching, pulling, rebasing, or syncing
- **Local-commit protection**: Feature branches with local-only commits are not switched or updated
- **Empty-repo support**: Repositories with no commits are reported and skipped without errors
- **No force push**: Never force pushes
- **Archived handling**: Pull, push, and normal sync skip archived repos
- **Safe archive cleanup**: `sync --remove-archived` confirms archive metadata and origin identity, fast-forwards only, and refuses dirty worktrees, local-only commits or stashes, active operations, linked worktrees, and remaining nested checkouts. Ignored files are disposable.
- **Orphan detection**: Flags local repos missing from remote
