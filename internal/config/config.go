package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Provider describes how to talk to a remote hosting service (gitea, github).
type Provider struct {
	Type    string          `json:"type"`    // gitea | github
	APIURL  string          `json:"api_url"` // base API endpoint
	Token   string          `json:"token"`   // personal access token
	Options ProviderOptions `json:"options,omitempty"`
}

type ProviderOptions struct {
	Clone CloneOptions `json:"clone,omitempty"`
	Sync  SyncOptions  `json:"sync,omitempty"`
}

type CloneOptions struct {
	Protocol string `json:"protocol,omitempty"` // ssh | https | auto (default https)
}

type SyncOptions struct {
	FFOnly *bool `json:"ff_only,omitempty"` // default true
}

// Helper to get bool value with default
func (s SyncOptions) GetFFOnly() bool {
	if s.FFOnly == nil {
		return true // default
	}
	return *s.FFOnly
}

// Target is a user-specified checkout target: either an entire org (Repo empty)
// or a single repo (Org + Repo).
type Target struct {
	Name     string `json:"name,omitempty"` // optional CLI name; defaults to Repo or Org
	Provider string `json:"provider"`
	Org      string `json:"org"`
	Repo     string `json:"repo,omitempty"`
	Path     string `json:"path"`
}

// Config holds the tugboat configuration
type Config struct {
	Workers   int                 `json:"workers,omitempty"` // default: number of CPU cores
	Providers map[string]Provider `json:"providers"`
	Targets   []Target            `json:"targets"`
}

// LoadResult contains the loaded config and metadata about the load operation
type LoadResult struct {
	Config       *Config
	Version      int
	IsDeprecated bool
	ConfigPath   string
}

// Load reads the configuration from file, auto-detecting the config version
func Load() (*Config, error) {
	result, err := LoadWithMetadata()
	if err != nil {
		return nil, err
	}

	// Print deprecation warning to stderr if using V1 config
	if result.IsDeprecated {
		fmt.Fprintf(os.Stderr, "WARNING: Using deprecated v1 config format. Run 'tugboat migrate' to upgrade.\n")
	}

	return result.Config, nil
}

// LoadWithMetadata reads the configuration and returns metadata about the load
func LoadWithMetadata() (*LoadResult, error) {
	configPath := getConfigPath()
	if configPath == "" {
		return nil, fmt.Errorf("no config file found")
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("reading config file %s: %w", configPath, err)
	}

	version, err := DetectVersion(data)
	if err != nil {
		return nil, err
	}

	var cfg *Config
	switch version {
	case 1:
		cfg, err = ReadV1(data)
	case 2:
		cfg, err = ReadV2(data)
	default:
		return nil, fmt.Errorf("unsupported config version: %d", version)
	}

	if err != nil {
		return nil, err
	}

	return &LoadResult{
		Config:       cfg,
		Version:      version,
		IsDeprecated: version < 2,
		ConfigPath:   configPath,
	}, nil
}

// LoadFromBytes parses config data, auto-detecting the version
func LoadFromBytes(data []byte) (*Config, error) {
	result, err := LoadFromBytesWithMetadata(data)
	if err != nil {
		return nil, err
	}
	return result.Config, nil
}

// LoadFromBytesWithMetadata parses config data and returns metadata
func LoadFromBytesWithMetadata(data []byte) (*LoadResult, error) {
	version, err := DetectVersion(data)
	if err != nil {
		return nil, err
	}

	var cfg *Config
	switch version {
	case 1:
		cfg, err = ReadV1(data)
	case 2:
		cfg, err = ReadV2(data)
	default:
		return nil, fmt.Errorf("unsupported config version: %d", version)
	}

	if err != nil {
		return nil, err
	}

	return &LoadResult{
		Config:       cfg,
		Version:      version,
		IsDeprecated: version < 2,
	}, nil
}

// LoadFromBytesWithWarning parses config and writes deprecation warning to w if needed
func LoadFromBytesWithWarning(data []byte, w io.Writer) (*Config, error) {
	result, err := LoadFromBytesWithMetadata(data)
	if err != nil {
		return nil, err
	}

	if result.IsDeprecated && w != nil {
		fmt.Fprintf(w, "WARNING: Using deprecated v1 config format. Run 'tugboat migrate' to upgrade.\n")
	}

	return result.Config, nil
}

// ToJSON serializes the config to JSON (v2 format)
func (c *Config) ToJSON() ([]byte, error) {
	return json.MarshalIndent(c, "", "  ")
}

// getConfigPath returns the path to the config file
func getConfigPath() string {
	// Check TUGBOAT_CONFIG env var first
	if path := os.Getenv("TUGBOAT_CONFIG"); path != "" {
		return expandPath(path)
	}

	// Try XDG config directory
	if xdgConfig := os.Getenv("XDG_CONFIG_HOME"); xdgConfig != "" {
		path := filepath.Join(xdgConfig, "tugboat", "config.json")
		if fileExists(path) {
			return path
		}
	}

	// Try ~/.config/tugboat/config.json
	if home, err := os.UserHomeDir(); err == nil {
		path := filepath.Join(home, ".config", "tugboat", "config.json")
		if fileExists(path) {
			return path
		}
	}

	// Try ~/.tugboat.json
	if home, err := os.UserHomeDir(); err == nil {
		path := filepath.Join(home, ".tugboat.json")
		if fileExists(path) {
			return path
		}
	}

	return ""
}

// expandPath expands ~ to home directory
func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// fileExists checks if a file exists
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// GetTargetByName returns a target pointer by its name.
func (c *Config) GetTargetByName(name string) *Target {
	for i := range c.Targets {
		if c.Targets[i].Name == name {
			return &c.Targets[i]
		}
	}
	return nil
}
