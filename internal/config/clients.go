package config

import (
	"fmt"

	"github.com/cli-tools/tugboat/internal/gitea"
	"github.com/cli-tools/tugboat/internal/github"
	"github.com/cli-tools/tugboat/internal/remote"
)

// BuildRemoteClients instantiates remote clients for each configured provider.
func (c *Config) BuildRemoteClients() (map[string]remote.Client, error) {
	clients := make(map[string]remote.Client, len(c.Providers))

	for name, p := range c.Providers {
		switch p.Type {
		case "gitea":
			clients[name] = gitea.NewClient(p.APIURL, p.Token)
		case "github":
			clients[name] = github.NewClient(p.APIURL, p.Token)
		default:
			return nil, fmt.Errorf("unsupported provider type %q", p.Type)
		}
	}

	return clients, nil
}
