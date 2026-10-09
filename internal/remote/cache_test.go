package remote

import (
	"errors"
	"testing"

	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/cache"
)

type countingClient struct {
	calls int
	gets  int
	err   error
}

func (c *countingClient) ListOrgRepos(org string) ([]Repository, error) {
	c.calls++
	return []Repository{{ID: int64(c.calls), Name: org}}, c.err
}
func (c *countingClient) GetRepo(owner, name string) (*Repository, error) {
	c.gets++
	return &Repository{Name: name}, c.err
}
func (c *countingClient) ListArchivedRepos() ([]Repository, error) {
	return c.ListOrgRepos("archives")
}

func TestListingCachePersistsAndIsolatesScopes(t *testing.T) {
	store := &cache.Store{Dir: t.TempDir()}
	upstream := &countingClient{}
	c := &CachedClient{Client: upstream, Store: store, Scope: "provider-and-token"}
	first, _ := c.ListOrgRepos("acme")
	c = &CachedClient{Client: upstream, Store: store, Scope: c.Scope}
	warm, err := c.ListOrgRepos("acme")
	if err != nil || upstream.calls != 1 || warm[0].ID != first[0].ID {
		t.Fatalf("warm listing: %v, calls=%d", err, upstream.calls)
	}
	c.ListOrgRepos("other")
	c.ListArchivedRepos()
	c.ListArchivedRepos()
	if upstream.calls != 3 {
		t.Fatalf("organization/archive cache collided: %d", upstream.calls)
	}
	c.Scope = "different-token"
	c.ListOrgRepos("acme")
	if upstream.calls != 4 {
		t.Fatal("different credentials reused listing")
	}
	c.Refresh = true
	c.ListOrgRepos("acme")
	if upstream.calls != 5 {
		t.Fatal("refresh used cache")
	}
	c.GetRepo("acme", "app")
	c.GetRepo("acme", "app")
	if upstream.gets != 2 {
		t.Fatal("refresh reused cached individual lookup")
	}
	c.Refresh = false
	c.GetRepo("acme", "app")
	if upstream.gets != 2 {
		t.Fatal("individual lookup was not cached")
	}
}

func TestListingCacheDoesNotHideOrPersistFailures(t *testing.T) {
	upstream := &countingClient{}
	c := &CachedClient{Client: upstream, Store: &cache.Store{Dir: t.TempDir()}, Scope: "scope"}
	c.ListOrgRepos("acme")
	c.Refresh, upstream.err = true, errors.New("access denied")
	if _, err := c.ListOrgRepos("acme"); err == nil {
		t.Fatal("refresh hid provider failure")
	}
	c.Refresh = false
	if _, err := c.ListOrgRepos("uncached"); err == nil {
		t.Fatal("provider failure lost")
	}
	upstream.err = nil
	if repos, err := c.ListOrgRepos("uncached"); err != nil || repos[0].ID != 4 {
		t.Fatalf("failed listing was cached: %v %v", repos, err)
	}
}

func TestFreshRepositoryChecksBypassAndUpdateDiscoveryCache(t *testing.T) {
	upstream := &countingClient{}
	c := &CachedClient{Client: upstream, Store: &cache.Store{Dir: t.TempDir()}, Scope: "scope"}
	c.GetRepo("acme", "app")
	c.GetRepo("acme", "app")
	if upstream.gets != 1 {
		t.Fatal("discovery did not reuse repository metadata")
	}
	upstream.err = errors.New("access revoked")
	if _, err := GetRepoFresh(c, "acme", "app"); err == nil || upstream.gets != 2 {
		t.Fatal("fresh check reused cached authorization")
	}
	upstream.err = nil
	if _, err := GetRepoFresh(c, "acme", "app"); err != nil || upstream.gets != 3 {
		t.Fatal("fresh check failed")
	}
	c.GetRepo("acme", "app")
	if upstream.gets != 3 {
		t.Fatal("fresh check did not update discovery cache")
	}
	GetRepoFresh(upstream, "acme", "app")
	if upstream.gets != 4 {
		t.Fatal("plain client fresh lookup was skipped")
	}
}
