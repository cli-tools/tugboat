package config

import (
	"encoding/json"
	"fmt"
)

// versionProbe is used to detect the config version from JSON
type versionProbe struct {
	// V2 indicators
	Version   int                    `json:"version,omitempty"`
	Providers map[string]interface{} `json:"providers,omitempty"`

	// V1 indicators
	GiteaURL string `json:"gitea_url,omitempty"`
}

// DetectVersion determines the config format version from raw JSON data
// Returns 1 for v0.3.x format (Gitea-only), 2 for current multi-provider format
func DetectVersion(data []byte) (int, error) {
	var probe versionProbe
	if err := json.Unmarshal(data, &probe); err != nil {
		return 0, fmt.Errorf("parsing config for version detection: %w", err)
	}

	// Check for V2 indicators first (providers map takes precedence)
	if probe.Providers != nil {
		return 2, nil
	}

	// Check for explicit version field
	if probe.Version > 0 {
		return probe.Version, nil
	}

	// Check for V1 indicators
	if probe.GiteaURL != "" {
		return 1, nil
	}

	return 0, fmt.Errorf("unrecognized config format: missing 'providers' (v2) or 'gitea_url' (v1)")
}
