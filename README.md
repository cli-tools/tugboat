# tugboat

Multi-repository management for Gitea and GitHub, with repo-centric targets and optional foldouts (.tugboat.json).

## Quick Start

1) Install

**Prebuilt binaries:** Download from [GitHub Releases](https://github.com/cli-tools/tugboat/releases)
```bash
# Example for Linux amd64
curl -L https://github.com/cli-tools/tugboat/releases/download/v0.4.5/tugboat-v0.4.5-linux-amd64 -o tugboat
chmod +x tugboat
sudo mv tugboat /usr/local/bin/
```

**From source:**
```bash
make build
sudo mv tugboat /usr/local/bin/
```

2) Configure `~/.config/tugboat/config.json`
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

3) (Optional) Foldouts inside a repo target (`~/acme/mobile-app/.tugboat.json`)
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

4) Clone
```bash
tugboat clone rideshare infra mobile-app   # orgs + repo with foldouts
```

5) Daily
```bash
tugboat status           # shows dirty/ahead/behind + archived/orphan flags
tugboat pull             # ff-only pulls
tugboat push             # push ahead repos
tugboat sync             # pull then push, ff-only
```

## Commands
- `clone [target ...]`   — org targets clone all repos; repo targets honor foldouts
- `status [target ...]`  — reports state; shows archived/orphan via provider metadata
- `pull [target ...]`    — ff-only pulls (provider option)
- `push [target ...]`
- `sync [target ...]`    — pull then push, ff-only
- `list [target ...]`    — shows local + remote; flags archived/orphan
- `help`, `version`

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
- ff-only pulls, no force pushes.
- Dirty repos are skipped by sync.
- Archived repos flagged; orphans flagged (local but missing remote).

## Build & Test
```bash
make build
go test ./...
```
