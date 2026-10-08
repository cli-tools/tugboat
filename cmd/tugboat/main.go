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

// parseVerbose accepts the flag before or after target names.
func parseVerbose(args []string) (bool, []string) {
	verbose := false
	var remaining []string
	for _, arg := range args {
		if arg == "--verbose" {
			verbose = true
		} else {
			remaining = append(remaining, arg)
		}
	}
	return verbose, remaining
}

// resolveWorkers returns CLI workers if set, otherwise config workers (0 = use CPU count)
func resolveWorkers(cliWorkers int, cfg *config.Config) int {
	if cliWorkers > 0 {
		return cliWorkers
	}
	return cfg.Workers // 0 means pool.Run will use GOMAXPROCS
}

func parseSyncArgs(args []string) (repo.SyncOptions, []string, error) {
	var opts repo.SyncOptions
	var targets []string
	for _, arg := range args {
		mode := repo.SyncBoth
		switch arg {
		case "--pull":
			mode = repo.SyncPull
		case "--push":
			mode = repo.SyncPush
		case "--clone-only":
			mode = repo.SyncCloneOnly
		case "--remove-archived":
			opts.RemoveArchived = true
			continue
		case "--exclude-empty", "-E":
			opts.ExcludeEmpty = true
			continue
		case "--include-archived", "-a":
			opts.IncludeArchived = true
			continue
		default:
			if strings.HasPrefix(arg, "-") {
				return opts, nil, fmt.Errorf("unknown sync option %s", arg)
			}
			targets = append(targets, arg)
			continue
		}
		if opts.Mode != repo.SyncBoth && opts.Mode != mode {
			return opts, nil, fmt.Errorf("--pull, --push, and --clone-only are mutually exclusive")
		}
		opts.Mode = mode
	}
	if opts.Mode != repo.SyncCloneOnly && (opts.ExcludeEmpty || opts.IncludeArchived) {
		return opts, nil, fmt.Errorf("--exclude-empty and --include-archived require --clone-only")
	}
	if opts.RemoveArchived && (opts.Mode == repo.SyncCloneOnly || opts.Mode == repo.SyncPush) {
		return opts, nil, fmt.Errorf("--remove-archived requires receiving sync")
	}
	return opts, targets, nil
}

func parseStatusArgs(args []string) (debug, showAll bool, targetNames []string) {
	for _, arg := range args {
		switch arg {
		case "--debug", "-d":
			debug = true
		case "--all":
			showAll = true
		default:
			targetNames = append(targetNames, arg)
		}
	}
	return debug, showAll, targetNames
}

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		printHelp()
		os.Exit(0)
	}

	cmd := os.Args[1]

	switch cmd {
	case "sync", "s":
		runSync(os.Args[2:])
	case "status", "st":
		runStatus(os.Args[2:])
	case "list", "ls":
		runList(os.Args[2:])
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
  sync, s       Clone missing repos, reconcile org renames, then pull and push
  status, st    Show grouped status; --all includes clean repository rows
  list, ls      List targets (local vs remote); -a/--include-archived
  migrate       Migrate config from v1 to v2 format
  help          Show this help message
  version       Show version information

Sync Options:
      --pull            Clone missing repos and pull; never push
      --push            Push existing repos; never clone or pull
      --clone-only      Clone and reconcile without updating branches
      --remove-archived Also remove safe in-org archives and obsolete renamed duplicates
  -E, --exclude-empty   Skip empty repos (--clone-only)
  -a, --include-archived Include archives (--clone-only; also supported by list)

Global Options:
  -w, --workers N   Number of parallel workers (default: config "workers" or CPU cores)
  -d, --debug       Show timing information (status command only)
      --verbose     Show detailed sync activity

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
  tugboat sync           # Clone missing repos and sync safely
  tugboat sync --pull    # Receive updates only
  tugboat sync --push    # Send commits only
  tugboat sync --clone-only # Create checkouts without updating branches
  tugboat sync --remove-archived  # Remove archived checkouts that pass every safety check
  tugboat status         # Show which repos have changes
  tugboat status --all   # Include clean repository rows
  tugboat status -w 16   # Use 16 parallel workers
  tugboat list           # List all managed repos
`
	fmt.Print(help)
}

func runSync(args []string) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	verbose, args := parseVerbose(args)
	cliWorkers, args := parseWorkers(args)
	workers := resolveWorkers(cliWorkers, cfg)
	opts, targetNames, err := parseSyncArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid sync options: %v\n", err)
		os.Exit(1)
	}
	opts.Workers = workers

	clients, err := cfg.BuildRemoteClients()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error building clients: %v\n", err)
		os.Exit(1)
	}
	manager := repo.NewManager(clients, cfg)
	manager.Verbose = verbose

	if err := manager.Sync(targetNames, opts); err != nil {
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
	debug, showAll, targetNames := parseStatusArgs(args)

	clients, err := cfg.BuildRemoteClients()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error building clients: %v\n", err)
		os.Exit(1)
	}
	manager := repo.NewManager(clients, cfg)

	if err := manager.Status(targetNames, repo.StatusOptions{Debug: debug, ShowAll: showAll, Workers: workers}); err != nil {
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
