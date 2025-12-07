package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/config"
	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/repo"
)

// parseWorkers extracts the --workers/-w flag value from args.
// Returns the worker count (0 means use default) and remaining args.
func parseWorkers(args []string) (int, []string) {
	var remaining []string
	workers := 0
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--workers" || arg == "-w" {
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil && n > 0 {
					workers = n
				}
				i++ // skip next arg
			}
		} else if strings.HasPrefix(arg, "--workers=") {
			if n, err := strconv.Atoi(strings.TrimPrefix(arg, "--workers=")); err == nil && n > 0 {
				workers = n
			}
		} else if strings.HasPrefix(arg, "-w=") {
			if n, err := strconv.Atoi(strings.TrimPrefix(arg, "-w=")); err == nil && n > 0 {
				workers = n
			}
		} else {
			remaining = append(remaining, arg)
		}
	}
	return workers, remaining
}

// resolveWorkers returns CLI workers if set, otherwise config workers (0 = use CPU count)
func resolveWorkers(cliWorkers int, cfg *config.Config) int {
	if cliWorkers > 0 {
		return cliWorkers
	}
	return cfg.Workers // 0 means pool.Run will use GOMAXPROCS
}

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		printHelp()
		os.Exit(0)
	}

	cmd := os.Args[1]

	switch cmd {
	case "clone", "c":
		runClone(os.Args[2:])
	case "sync", "s":
		runSync(os.Args[2:])
	case "status", "st":
		runStatus(os.Args[2:])
	case "list", "ls":
		runList(os.Args[2:])
	case "pull":
		runPull(os.Args[2:])
	case "push":
		runPush(os.Args[2:])
	case "migrate":
		runMigrate(os.Args[2:])
	case "help", "-h", "--help":
		printHelp()
	case "version", "-v", "--version":
		fmt.Printf("tugboat %s\n", version)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", cmd)
		printHelp()
		os.Exit(1)
	}
}

func printHelp() {
	help := `tugboat - Multi-repository management tool for Gitea and GitHub (repo-centric)

Usage: tugboat <command> [options]

Commands:
  clone, c      Clone targets (org or repo); -E/--exclude-empty, -a/--include-archived
  sync, s       Sync targets (ff-only)
  status, st    Show status for targets (foldouts included)
  list, ls      List targets (local vs remote); -a/--include-archived
  pull          Pull targets (ff-only)
  push          Push targets
  migrate       Migrate config from v1 to v2 format
  help          Show this help message
  version       Show version information

Global Options:
  -w, --workers N   Number of parallel workers (default: config "workers" or CPU cores)
  -d, --debug       Show timing information (status command only)

Configuration:
  tugboat reads from ~/.config/tugboat/config.json or TUGBOAT_CONFIG env var

  Example config (repo-centric):
  {
    "providers": {
      "gitea":  {"type": "gitea",  "api_url": "https://gitea.acme.com", "token": "gitea-token"},
      "github": {"type": "github", "api_url": "https://api.github.com", "token": "ghp_your_token"}
    },
    "targets": [
      { "provider": "gitea",  "org": "acme-rideshare", "path": "~/acme/rideshare", "name": "rideshare" },
      { "provider": "gitea",  "org": "acme-infra",     "path": "~/acme/infra",     "name": "infra" },
      { "provider": "github", "org": "acme",           "repo": "mobile-app",       "path": "~/acme/mobile-app", "name": "mobile-app" }
    ]
  }

  You can also set GITEA_TOKEN environment variable.

Examples:
  tugboat clone          # Clone all repos from configured orgs
  tugboat sync           # Pull and push all repos safely
  tugboat status         # Show which repos have changes
  tugboat status -w 16   # Use 16 parallel workers
  tugboat list           # List all managed repos
`
	fmt.Print(help)
}

func runClone(args []string) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	cliWorkers, args := parseWorkers(args)
	workers := resolveWorkers(cliWorkers, cfg)
	excludeEmpty := false
	includeArchived := false
	var targetNames []string
	for _, arg := range args {
		switch arg {
		case "--exclude-empty", "-E":
			excludeEmpty = true
		case "--include-archived", "-a":
			includeArchived = true
		default:
			targetNames = append(targetNames, arg)
		}
	}

	clients, err := cfg.BuildRemoteClients()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error building clients: %v\n", err)
		os.Exit(1)
	}
	manager := repo.NewManager(clients, cfg)

	if err := manager.Clone(targetNames, excludeEmpty, includeArchived, workers); err != nil {
		fmt.Fprintf(os.Stderr, "Error cloning repositories: %v\n", err)
		os.Exit(1)
	}
}

func runSync(args []string) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	cliWorkers, args := parseWorkers(args)
	workers := resolveWorkers(cliWorkers, cfg)

	clients, err := cfg.BuildRemoteClients()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error building clients: %v\n", err)
		os.Exit(1)
	}
	manager := repo.NewManager(clients, cfg)

	if err := manager.Sync(args, workers); err != nil {
		fmt.Fprintf(os.Stderr, "Error syncing repositories: %v\n", err)
		os.Exit(1)
	}
}

func runStatus(args []string) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	cliWorkers, args := parseWorkers(args)
	workers := resolveWorkers(cliWorkers, cfg)
	debug := false
	var targetNames []string
	for _, arg := range args {
		switch arg {
		case "--debug", "-d":
			debug = true
		default:
			targetNames = append(targetNames, arg)
		}
	}

	clients, err := cfg.BuildRemoteClients()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error building clients: %v\n", err)
		os.Exit(1)
	}
	manager := repo.NewManager(clients, cfg)

	if err := manager.Status(targetNames, debug, workers); err != nil {
		fmt.Fprintf(os.Stderr, "Error showing status: %v\n", err)
		os.Exit(1)
	}
}

func runList(args []string) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	cliWorkers, args := parseWorkers(args)
	workers := resolveWorkers(cliWorkers, cfg)
	includeArchived := false
	var targetNames []string
	for _, arg := range args {
		switch arg {
		case "--include-archived", "-a":
			includeArchived = true
		default:
			targetNames = append(targetNames, arg)
		}
	}

	clients, err := cfg.BuildRemoteClients()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error building clients: %v\n", err)
		os.Exit(1)
	}
	manager := repo.NewManager(clients, cfg)

	if err := manager.List(targetNames, includeArchived, workers); err != nil {
		fmt.Fprintf(os.Stderr, "Error listing repositories: %v\n", err)
		os.Exit(1)
	}
}

func runPull(args []string) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	cliWorkers, args := parseWorkers(args)
	workers := resolveWorkers(cliWorkers, cfg)

	clients, err := cfg.BuildRemoteClients()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error building clients: %v\n", err)
		os.Exit(1)
	}
	manager := repo.NewManager(clients, cfg)

	if err := manager.Pull(args, workers); err != nil {
		fmt.Fprintf(os.Stderr, "Error pulling repositories: %v\n", err)
		os.Exit(1)
	}
}

func runPush(args []string) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	cliWorkers, args := parseWorkers(args)
	workers := resolveWorkers(cliWorkers, cfg)

	clients, err := cfg.BuildRemoteClients()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error building clients: %v\n", err)
		os.Exit(1)
	}
	manager := repo.NewManager(clients, cfg)

	if err := manager.Push(args, workers); err != nil {
		fmt.Fprintf(os.Stderr, "Error pushing repositories: %v\n", err)
		os.Exit(1)
	}
}

func runMigrate(args []string) {
	// Check for --write flag
	writeInPlace := false
	for _, arg := range args {
		if arg == "--write" || arg == "-w" {
			writeInPlace = true
		}
	}

	result, err := config.LoadWithMetadata()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	if result.Version == 2 {
		fmt.Println("Config is already v2 format. No migration needed.")
		return
	}

	// Generate v2 JSON
	v2JSON, err := result.Config.ToJSON()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error generating v2 config: %v\n", err)
		os.Exit(1)
	}

	if writeInPlace {
		// Backup original
		backupPath := result.ConfigPath + ".v1.backup"
		data, _ := os.ReadFile(result.ConfigPath)
		if err := os.WriteFile(backupPath, data, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating backup: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Backed up v1 config to: %s\n", backupPath)

		// Write new config
		if err := os.WriteFile(result.ConfigPath, v2JSON, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing v2 config: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Migrated config to v2: %s\n", result.ConfigPath)
	} else {
		fmt.Println("# Migrated v2 config (use --write to save in place):")
		fmt.Println(string(v2JSON))
	}
}
