package config

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ConfigV1 represents the v0.3.x config format (Gitea-only)
type ConfigV1 struct {
	GiteaURL      string           `json:"gitea_url"`
	GiteaToken    string           `json:"gitea_token"`
	Organizations []OrganizationV1 `json:"organizations"`
}

// OrganizationV1 represents an organization in v1 config
type OrganizationV1 struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// ReadV1 parses a v1 config and migrates it to the current Config format
func ReadV1(data []byte) (*Config, error) {
	var v1 ConfigV1
	if err := json.Unmarshal(data, &v1); err != nil {
		return nil, fmt.Errorf("parsing v1 config: %w", err)
	}

	// Validate required fields
	if v1.GiteaURL == "" {
		return nil, fmt.Errorf("gitea_url is required")
	}
	if v1.GiteaToken == "" {
		return nil, fmt.Errorf("gitea_token is required")
	}
	if len(v1.Organizations) == 0 {
		return nil, fmt.Errorf("at least one organization must be configured")
	}

	// Migrate to v2 format
	return migrateV1toV2(&v1), nil
}

// migrateV1toV2 converts a v1 config to the current Config format
func migrateV1toV2(v1 *ConfigV1) *Config {
	// Trim trailing slash from URL
	apiURL := strings.TrimSuffix(v1.GiteaURL, "/")

	cfg := &Config{
		Providers: map[string]Provider{
			"gitea": {
				Type:   "gitea",
				APIURL: apiURL,
				Token:  v1.GiteaToken,
				Options: ProviderOptions{
					Clone: CloneOptions{Protocol: "https"},
				},
			},
		},
		Targets: make([]Target, len(v1.Organizations)),
	}

	for i, org := range v1.Organizations {
		cfg.Targets[i] = Target{
			Name:     org.Name,
			Provider: "gitea",
			Org:      org.Name,
			Path:     expandPath(org.Path),
		}
	}

	return cfg
}
