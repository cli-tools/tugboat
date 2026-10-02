package config

import (
	"fmt"
	"path"
	"strings"
)

// ValidateExclusions checks all clone exclusions, including patterns that may
// never be reached while matching repositories.
func (t Target) ValidateExclusions() error {
	if t.Repo != "" && len(t.Exclude) > 0 {
		return fmt.Errorf("target %q: exclude is only supported on organization targets", t.Name)
	}
	for i, pattern := range t.Exclude {
		var err error
		switch {
		case strings.TrimSpace(pattern) == "":
			err = fmt.Errorf("pattern must not be empty")
		case strings.Contains(pattern, "/"):
			err = fmt.Errorf("pattern must match a repository name, not a path")
		case strings.HasPrefix(pattern, "!"):
			err = fmt.Errorf("! exception patterns are not supported")
		default:
			_, err = path.Match(pattern, "")
		}
		if err != nil {
			return fmt.Errorf("target %q: exclude[%d] pattern %q: %w", t.Name, i, pattern, err)
		}
	}
	return nil
}
