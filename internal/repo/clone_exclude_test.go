package repo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/config"
	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/remote"
)

func TestCloneExclusionsMatchNamesAndStayWithinTarget(t *testing.T) {
	base := t.TempDir()
	seed := createTestRepo(t, base, "t1", "seed", "main", filepath.Join(base, "seed-work"))
	cases := []struct {
		name    string
		pattern string // empty means it should clone
	}{
		{"benchmark-runs", "benchmark-runs"},
		{"benchmark-runs-extra", ""},
		{"Benchmark-runs", ""},
		{"scratch-", "scratch-*"},
		{"scratch-temp", "scratch-*"},
		{"test-a", "test-?"},
		{"test-ab", ""},
		{"run-2", "run-[0-9]"},
		{"run-x", ""},
		{"app", ""},
	}
	repos := make(map[string]remote.Repository)
	for _, tc := range cases {
		r := remoteRepo(seed)
		r.Name, r.FullName = tc.name, "t1/"+tc.name
		repos[tc.name] = r
	}
	filtered := config.Target{
		Name: "filtered", Provider: "fake", Org: "t1", Path: filepath.Join(base, "filtered"),
		Exclude: []string{"benchmark-runs", "scratch-*", "test-?", "run-[0-9]"},
	}
	unfiltered := config.Target{Name: "unfiltered", Provider: "fake", Org: "t1", Path: filepath.Join(base, "unfiltered")}
	explicit := config.Target{Name: "explicit", Provider: "fake", Org: "t1", Repo: "benchmark-runs", Path: filepath.Join(base, "explicit")}
	manager := newTestManager([]config.Target{filtered, unfiltered, explicit}, fakeClient{
		repos: map[string]map[string]remote.Repository{"t1": repos},
	})
	output := captureStdout(t, func() {
		if err := manager.Clone(nil, false, false, 2); err != nil {
			t.Fatal(err)
		}
	})
	for _, tc := range cases {
		dest := filepath.Join(filtered.Path, tc.name)
		if tc.pattern != "" {
			if _, err := os.Stat(dest); !os.IsNotExist(err) {
				t.Errorf("excluded repository %s should not exist: %v", tc.name, err)
			}
			if !strings.Contains(output, `t1/`+tc.name+`: excluded by pattern "`+tc.pattern+`"`) {
				t.Errorf("missing exclusion message for %s:\n%s", tc.name, output)
			}
		} else if !isGitRepo(dest) {
			t.Errorf("permitted repository %s was not cloned", tc.name)
		}
		if !isGitRepo(filepath.Join(unfiltered.Path, tc.name)) {
			t.Errorf("exclusions leaked to another target for %s", tc.name)
		}
	}
	if !isGitRepo(explicit.Path) {
		t.Fatal("explicit repository target was not cloned")
	}

	// Adding an exclusion must leave an existing checkout and its local files intact.
	existing := filepath.Join(filtered.Path, "app")
	writeFile(t, filepath.Join(existing, "local.txt"), "keep me\n")
	manager.config.Targets[0].Exclude = []string{"*"}
	captureStdout(t, func() {
		if err := manager.Clone([]string{"filtered"}, false, false, 2); err != nil {
			t.Fatal(err)
		}
	})
	if data, err := os.ReadFile(filepath.Join(existing, "local.txt")); err != nil || string(data) != "keep me\n" || !isGitRepo(existing) {
		t.Fatalf("existing excluded checkout was changed: data=%q, err=%v", data, err)
	}
}

type cloneCountingClient struct {
	fakeClient
	calls int
}

func (c *cloneCountingClient) ListOrgRepos(org string) ([]remote.Repository, error) {
	c.calls++
	return c.fakeClient.ListOrgRepos(org)
}

func (c *cloneCountingClient) GetRepo(org, name string) (*remote.Repository, error) {
	c.calls++
	return c.fakeClient.GetRepo(org, name)
}

func TestCloneValidatesAllSelectedExclusionsBeforeWork(t *testing.T) {
	for _, tc := range []struct {
		name    string
		repo    string
		exclude []string
	}{
		{name: "malformed after match all", exclude: []string{"*", "bad["}},
		{name: "single repo target", repo: "app", exclude: []string{"app"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			targets := []config.Target{
				{Name: "first", Provider: "fake", Org: "t1", Path: filepath.Join(base, "first")},
				{Name: "invalid", Provider: "fake", Org: "t1", Path: filepath.Join(base, "invalid"), Repo: tc.repo, Exclude: tc.exclude},
			}
			// Even an empty provider result must not conceal an invalid pattern.
			client := &cloneCountingClient{}
			manager := newTestManager(targets, fakeClient{})
			manager.providers["fake"] = client
			err := manager.Clone(nil, false, false, 1)
			if err == nil || !strings.Contains(err.Error(), `target "invalid"`) {
				t.Fatalf("Clone() error = %v, want invalid target error", err)
			}
			if client.calls != 0 {
				t.Fatalf("made %d provider calls before rejecting exclusions", client.calls)
			}
			for _, target := range targets {
				if _, err := os.Stat(target.Path); !os.IsNotExist(err) {
					t.Errorf("target directory %s should not exist: %v", target.Path, err)
				}
			}
		})
	}
}
