package remote

import (
	"time"

	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/cache"
)

// CachedClient shares discovery data between commands. Refresh forces live
// discovery and still updates the store; GetRepoFresh always bypasses it.
type CachedClient struct {
	Client
	Store   *cache.Store
	Scope   string
	Refresh bool
}

const ListingCacheAge = 5 * time.Minute

func (c *CachedClient) list(key string, fetch func() ([]Repository, error)) ([]Repository, error) {
	key = cache.Key("remote", c.Scope, key)
	var repos []Repository
	if !c.Refresh && c.Store.Read(key, ListingCacheAge, &repos) {
		return repos, nil
	}
	repos, err := fetch()
	if err == nil {
		c.Store.Write(key, repos)
	}
	return repos, err
}

func (c *CachedClient) ListOrgRepos(org string) ([]Repository, error) {
	return c.list("org/"+org, func() ([]Repository, error) { return c.Client.ListOrgRepos(org) })
}

func (c *CachedClient) ListArchivedRepos() ([]Repository, error) {
	lister, ok := c.Client.(ArchivedRepositoryLister)
	if !ok {
		return nil, nil
	}
	return c.list("archives", lister.ListArchivedRepos)
}

func (c *CachedClient) GetRepo(owner, name string) (*Repository, error) {
	key := cache.Key("remote-repo", c.Scope, owner, name)
	var repo *Repository
	if !c.Refresh && c.Store.Read(key, ListingCacheAge, &repo) {
		return repo, nil
	}
	return c.GetRepoFresh(owner, name)
}

func (c *CachedClient) GetRepoFresh(owner, name string) (*Repository, error) {
	repo, err := GetRepoFresh(c.Client, owner, name)
	if err == nil {
		c.Store.Write(cache.Key("remote-repo", c.Scope, owner, name), repo)
	}
	return repo, err
}
