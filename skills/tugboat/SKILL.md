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
tugboat status [targets...]   # Show dirty/ahead/behind/archived/orphan
tugboat pull [targets...]     # Fast-forward only pulls
tugboat push [targets...]     # Push repos that are ahead
tugboat sync [targets...]     # Pull then push (ff-only, skips dirty)
tugboat list [targets...]     # List local vs remote repos
tugboat help                  # Show help
tugboat version               # Show version
```

### Options
- `-w, --workers N` - Parallel workers (default: CPU cores)
- `-d, --debug` - Show timing info (status only)
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
/root/rideshare (master) [dirty]
/root/rideshare/api (main) [clean]
/root/rideshare/batch (master) [3 ahead, 2 behind, diverged]
/root/rideshare/web (master) [archived]

Summary: 10 clean, 2 dirty, 1 ahead, 3 behind, 1 diverged, 0 errors
```

Status flags:
- `[dirty]` - Uncommitted changes
- `[N ahead]` - Local commits not pushed
- `[N behind]` - Remote commits not pulled
- `[diverged]` - Both ahead and behind
- `[archived]` - Repo archived on remote
- `[orphan]` - Local repo, missing on remote

## Examples

### Clone everything
```bash
tugboat clone                 # All targets
tugboat clone myteam rideshare  # Specific targets
```

### Daily workflow
```bash
tugboat status    # See what needs attention
tugboat pull      # Get latest (ff-only)
# ... do work ...
tugboat push      # Push your commits
```

### Sync all repos
```bash
tugboat sync      # Pull then push, skips dirty repos
```

### Check what's remote vs local
```bash
tugboat list              # All targets
tugboat list -a           # Include archived
```

## Safety Features

- **ff-only pulls**: Never creates merge commits automatically
- **Dirty skip**: Sync skips repos with uncommitted changes
- **No force push**: Never force pushes
- **Archived flagging**: Warns about archived repos
- **Orphan detection**: Flags local repos missing from remote
