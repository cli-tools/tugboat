package config

import (
	"encoding/json"
	"fmt"
)

// ReadV2 parses a v2 (current) config format
func ReadV2(data []byte) (*Config, error) {
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing v2 config: %w", err)
	}

	// Validate and apply defaults
	if err := validateAndNormalizeV2(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// validateAndNormalizeV2 validates the config and applies default values
func validateAndNormalizeV2(cfg *Config) error {
	// Validate providers
	if len(cfg.Providers) == 0 {
		return fmt.Errorf("at least one provider must be configured")
	}

	for name, p := range cfg.Providers {
		if p.Type != "gitea" && p.Type != "github" {
			return fmt.Errorf("provider %q has unsupported type %q", name, p.Type)
		}
		if p.Type == "gitea" && p.APIURL == "" {
			return fmt.Errorf("provider %q (gitea) requires api_url", name)
		}
		if p.Type == "github" && p.APIURL == "" {
			p.APIURL = "https://api.github.com"
			cfg.Providers[name] = p
		}
		if p.Token == "" {
			return fmt.Errorf("provider %q requires token", name)
		}
		// Default clone protocol
		if p.Options.Clone.Protocol == "" {
			p.Options.Clone.Protocol = "https"
		}
		cfg.Providers[name] = p
	}

	// Validate targets
	if len(cfg.Targets) == 0 {
		return fmt.Errorf("at least one target must be configured")
	}

	nameSet := make(map[string]bool, len(cfg.Targets))
	for i := range cfg.Targets {
		t := &cfg.Targets[i]
		if t.Provider == "" {
			return fmt.Errorf("target %d missing provider", i)
		}
		if _, ok := cfg.Providers[t.Provider]; !ok {
			return fmt.Errorf("target %d references unknown provider %q", i, t.Provider)
		}
		if t.Org == "" {
			return fmt.Errorf("target %d missing org", i)
		}
		if t.Path == "" {
			return fmt.Errorf("target %s missing path", t.Org)
		}
		t.Path = expandPath(t.Path)

		// Default name to repo or org
		if t.Name == "" {
			if t.Repo != "" {
				t.Name = t.Repo
			} else {
				t.Name = t.Org
			}
		}

		if nameSet[t.Name] {
			return fmt.Errorf("duplicate target name %q", t.Name)
		}
		nameSet[t.Name] = true
	}

	return nil
}
