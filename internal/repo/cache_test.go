package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/cache"
	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/config"
	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/remote"
)

func TestCachedIdentityAvoidsRepeatedHistoryDownloads(t *testing.T) {
	for _, change := range []string{"none", "HEAD", "config", "origin", "refresh", "provider", "manual identity"} {
		t.Run(change, func(t *testing.T) {
			base := t.TempDir()
			r := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "work"))
			m := newTestManager([]config.Target{repoTarget(r)}, fakeClientForRepos(r))
			m.Cache = &cache.Store{Dir: filepath.Join(base, "cache")}
			job := statusJob{path: r.workPath, provider: "fake", org: "acme", name: "app"}
			repos := map[string]remote.Repository{"app": remoteRepo(r)}
			probe := &historyProbe{}
			defer probe.close()
			s := m.inspectIdentity(job, repos, probe)
			if s.Error != "" || s.RepositoryID != repos["app"].ID {
				t.Fatalf("cold identity: %+v", s)
			}
			if probe.dir != "" {
				t.Fatal("downloaded history despite locally available advertised commit")
			}
			if id, err := readIdentity(r.workPath); err != nil || id != nil {
				t.Fatal("read-only discovery saved checkout metadata")
			}
			// Recreate the manager as a separate CLI invocation would.
			next := NewManager(m.providers, m.config)
			next.Cache = &cache.Store{Dir: m.Cache.Dir}
			switch change {
			case "HEAD":
				commitFile(t, r.workPath, "new.txt", "new work", "new commit")
			case "config":
				runGit(t, r.workPath, "config", "tugboat.test", "changed")
			case "origin":
				runGit(t, r.workPath, "remote", "set-url", "origin", filepath.Join(base, "elsewhere.git"))
			case "refresh":
				next.RefreshCache = true
			case "provider":
				p := next.config.Providers["fake"]
				p.APIURL = "https://different.invalid"
				next.config.Providers["fake"] = p
			case "manual identity":
				id := newIdentity(m.config.Providers["fake"], "acme", repos["app"], r.remotePath)
				id.RepositoryID++
				if err := writeIdentity(r.workPath, id); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.RemoveAll(r.remotePath); err != nil {
				t.Fatal(err)
			}
			probe = &historyProbe{}
			defer probe.close()
			s = next.inspectIdentity(job, repos, probe)
			if change == "none" {
				if s.Error != "" || s.RepositoryID != repos["app"].ID || probe.dir != "" {
					t.Fatalf("warm identity required network: %+v", s)
				}
			} else if s.Error == "" && !s.Orphan {
				t.Fatalf("reused invalidated identity after %s: %+v", change, s)
			}
		})
	}
}

func TestCachedIdentityTracksIDAfterNameReuse(t *testing.T) {
	base := t.TempDir()
	r := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "work"))
	m := newTestManager([]config.Target{repoTarget(r)}, fakeClientForRepos(r))
	m.Cache = &cache.Store{Dir: filepath.Join(base, "cache")}
	job := statusJob{path: r.workPath, provider: "fake", org: "acme", name: "app"}
	old := remoteRepo(r)
	probe := &historyProbe{}
	defer probe.close()
	if s := m.inspectIdentity(job, map[string]remote.Repository{"app": old}, probe); s.Error != "" {
		t.Fatal(s.Error)
	}
	replacement := old
	replacement.ID++
	old.Name, old.FullName, old.Archived = "app-archive", "acme/app-archive", true
	s := m.inspectIdentity(job, map[string]remote.Repository{"app": replacement, old.Name: old}, probe)
	if s.RepositoryID != old.ID || s.ReplacementID != replacement.ID || s.IdentityIssue == "" {
		t.Fatalf("cached identity followed reused name: %+v", s)
	}
	repos := map[string]remote.Repository{"app": replacement, old.Name: old}
	m.inspectListedIdentity(job, repos, probe)
	status := m.scanStatus(job, &scanMetadata{index: map[string]map[string]remote.Repository{
		orgKey{"fake", "acme"}.string(): repos}, readOnly: true}, probe, nil, nil).status
	if !status.Archived || status.RepositoryID != old.ID || status.IdentityIssue == "" {
		t.Fatalf("status lost remote state while reusing cached discovery: %+v", status)
	}
}

func TestListingCacheAvoidsRepeatedUnresolvedHistoryProbes(t *testing.T) {
	base := t.TempDir()
	r := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "work"))
	other := createTestRepo(t, base, "acme", "other", "main", filepath.Join(base, "other-work"))
	runGit(t, r.workPath, "remote", "set-url", "origin", other.remotePath)
	m := newTestManager([]config.Target{repoTarget(r)}, fakeClientForRepos(other))
	m.Cache = &cache.Store{Dir: filepath.Join(base, "cache")}
	job := statusJob{path: r.workPath, provider: "fake", org: "acme", name: "app"}
	upstream := remoteRepo(other)
	upstream.Name, upstream.FullName = "app", "acme/app"
	repos := map[string]remote.Repository{"app": upstream}
	probe := &historyProbe{}
	defer probe.close()
	cold := m.inspectListedIdentity(job, repos, probe)
	if cold.Error != "" || cold.IdentityIssue == "" || cold.RepositoryID != 0 {
		t.Fatalf("expected unresolved identity: %+v", cold)
	}
	if err := os.RemoveAll(other.remotePath); err != nil {
		t.Fatal(err)
	}
	warmProbe := &historyProbe{}
	defer warmProbe.close()
	warm := m.inspectListedIdentity(job, repos, warmProbe)
	if warm.Error != "" || warm.IdentityIssue != cold.IdentityIssue || warmProbe.dir != "" {
		t.Fatalf("repeated unresolved probe: %+v", warm)
	}
	status := m.scanStatus(job, &scanMetadata{index: map[string]map[string]remote.Repository{
		orgKey{"fake", "acme"}.string(): repos}, readOnly: true}, warmProbe, nil, nil).status
	if status.Error != "" || status.IdentityIssue != cold.IdentityIssue || warmProbe.dir != "" {
		t.Fatalf("status repeated unresolved discovery: %+v", status)
	}
	// A mutating command's identity check must never use a listing result.
	if s := m.inspectIdentity(job, repos, warmProbe); s.Error == "" {
		t.Fatal("listing result authorized unresolved identity")
	}
	upstream.Archived = true
	repos["app"] = upstream
	if s := m.inspectListedIdentity(job, repos, warmProbe); s.Error == "" {
		t.Fatal("changed remote metadata did not invalidate listing")
	}
}

func TestSyncSchedulerUsesVerifiedDiscoveryCache(t *testing.T) {
	base := t.TempDir()
	r := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "work"))
	m := newTestManager([]config.Target{repoTarget(r)}, fakeClientForRepos(r))
	m.Cache = &cache.Store{Dir: filepath.Join(base, "cache")}
	job := statusJob{path: r.workPath, provider: "fake", org: "acme", name: "app"}
	repos := map[string]remote.Repository{"app": remoteRepo(r)}
	metadata := &scanMetadata{index: map[string]map[string]remote.Repository{orgKey{"fake", "acme"}.string(): repos}}
	if m.independentCheckout(job, metadata) {
		t.Fatal("unverified checkout entered independent queue")
	}
	probe := &historyProbe{}
	defer probe.close()
	if s := m.inspectListedIdentity(job, repos, probe); s.Error != "" {
		t.Fatal(s.Error)
	}
	if !m.independentCheckout(job, metadata) {
		t.Fatal("cached verified checkout entered serial reconciliation")
	}
	m.RefreshCache = true
	if m.independentCheckout(job, metadata) {
		t.Fatal("refresh reused cached scheduling identity")
	}
	m.RefreshCache = false
	id := newIdentity(m.config.Providers["fake"], "acme", repos["app"], r.remotePath)
	id.RepositoryID++
	if err := writeIdentity(r.workPath, id); err != nil {
		t.Fatal(err)
	}
	if m.independentCheckout(job, metadata) {
		t.Fatal("scheduler overrode explicit identity with cache")
	}
}

func TestLiveIdentityChecksCannotUseCachedRemoteResults(t *testing.T) {
	base := t.TempDir()
	r := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "work"))
	upstream := fakeClientForRepos(r)
	m := newTestManager([]config.Target{repoTarget(r)}, upstream)
	client := &remote.CachedClient{Client: upstream, Store: &cache.Store{Dir: filepath.Join(base, "cache")}, Scope: "scope"}
	m.providers["fake"] = client
	old := remoteRepo(r)
	client.GetRepo("acme", "app")
	current := old
	current.ID++
	upstream.repos["acme"]["app"] = current
	if err := m.confirmRepository("fake", "acme", old); err == nil {
		t.Fatal("live confirmation accepted stale cached repository ID")
	}
	old.Archived = true
	upstream.repos["acme"]["app"] = old
	if err := writeIdentity(r.workPath, newIdentity(m.config.Providers["fake"], "acme", old, r.remotePath)); err != nil {
		t.Fatal(err)
	}
	client.GetRepoFresh("acme", "app")
	current = old
	current.Archived = false
	upstream.repos["acme"]["app"] = current
	s := RepoStatus{Path: r.workPath, Provider: "fake", Org: "acme", Name: "app", RemoteName: "app", RepositoryID: old.ID,
		identity: newIdentity(m.config.Providers["fake"], "acme", old, r.remotePath)}
	if _, err := m.confirmArchivedRepository(s); err == nil {
		t.Fatal("archive confirmation accepted stale cached archive state")
	}
}

func TestEveryCommandReusesLocalIdentitiesWarmedByList(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := t.TempDir()
	logPath := filepath.Join(wrapper, "unexpected-probe")
	script := `#!/bin/sh
case "$PWD" in
    */tugboat-history-*) printf '%s\n' "$PWD $*" >> "$CACHE_PROBE_LOG"; exit 86 ;;
esac
if [ "$1" = ls-remote ]; then
    printf '%s\n' "$PWD $*" >> "$CACHE_PROBE_LOG"
    exit 86
fi
exec "$CACHE_REAL_GIT" "$@"
`
	if err := os.WriteFile(filepath.Join(wrapper, "git"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	for _, shape := range []string{"organization", "explicit", "foldout"} {
		for _, command := range []string{"status", "sync", "pull", "push", "clone-only"} {
			t.Run(shape+"/"+command, func(t *testing.T) {
				base := t.TempDir()
				root := filepath.Join(base, "work")
				if err := os.MkdirAll(root, 0755); err != nil {
					t.Fatal(err)
				}
				r := createTestRepo(t, base, "acme", "app", "main", filepath.Join(root, "app"))
				target := repoTarget(r)
				client := fakeClientForRepos(r)
				if shape == "organization" {
					target.Repo, target.Path = "", root
				}
				if shape == "foldout" {
					child := createTestRepo(t, base, "acme", "child", "main", filepath.Join(r.workPath, "child"))
					commitFile(t, r.workPath, ".gitignore", "/child/\n", "ignore foldout")
					commitFile(t, r.workPath, ".tugboat.json", `{"repos":[{"name":"acme/child","target":"child"}]}`, "declare foldout")
					runGit(t, r.workPath, "push", "origin", "main")
					client = fakeClientForRepos(r, child)
				}
				m := newTestManager([]config.Target{target}, client)
				m.Cache = &cache.Store{Dir: filepath.Join(base, "cache")}
				captureStdout(t, func() {
					if err := m.List(nil, false, 2); err != nil {
						t.Fatal(err)
					}
				})
				if id, err := readIdentity(r.workPath); err != nil || id != nil {
					t.Fatal("list wrote explicit checkout identity")
				}
				// Only Git history discovery is disabled; live fetches and all
				// branch/worktree operations still run against the real fixture.
				t.Setenv("CACHE_REAL_GIT", realGit)
				t.Setenv("CACHE_PROBE_LOG", logPath)
				t.Setenv("PATH", wrapper+string(os.PathListSeparator)+os.Getenv("PATH"))
				next := NewManager(m.providers, m.config)
				next.Cache = &cache.Store{Dir: m.Cache.Dir}
				if command == "status" {
					writeFile(t, filepath.Join(r.workPath, "untracked.txt"), "new local work")
				}
				var runErr error
				output := captureStdout(t, func() {
					if command == "status" {
						runErr = next.Status(nil, StatusOptions{Workers: 2, ShowAll: true})
						return
					}
					mode := map[string]SyncMode{"sync": SyncBoth, "pull": SyncPull, "push": SyncPush, "clone-only": SyncCloneOnly}[command]
					runErr = next.Sync(nil, SyncOptions{Mode: mode, Workers: 2})
				})
				if runErr != nil || strings.Contains(output, "ERROR") || strings.Contains(output, "identity is ambiguous") {
					t.Fatalf("cached identity command failed: %v\n%s", runErr, output)
				}
				if command == "status" && !strings.Contains(output, "dirty") {
					t.Fatalf("status reused cached worktree state:\n%s", output)
				}
				if log, err := os.ReadFile(logPath); err == nil {
					t.Fatalf("command repeated history discovery:\n%s", log)
				}
			})
		}
	}
}

func TestAdvertisedHistoryIgnoresLocalAncestryOverrides(t *testing.T) {
	for _, override := range []string{"replace", "graft", "graft-env"} {
		t.Run(override, func(t *testing.T) {
			base := t.TempDir()
			local := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "local"))
			upstream := createTestRepo(t, base, "acme", "replacement", "main", filepath.Join(base, "upstream"))
			root := strings.TrimSpace(gitText(t, upstream.remotePath, "rev-parse", "refs/heads/main"))
			tree := strings.TrimSpace(gitText(t, upstream.remotePath, "rev-parse", root+"^{tree}"))
			tip := strings.TrimSpace(gitText(t, upstream.remotePath, "-c", "user.name=Test", "-c", "user.email=test@example.invalid",
				"commit-tree", tree, "-p", root, "-m", "advance unrelated upstream"))
			runGit(t, upstream.remotePath, "update-ref", "refs/heads/main", tip)
			runGit(t, local.workPath, "remote", "set-url", "origin", upstream.remotePath)
			runGit(t, local.workPath, "fetch", "origin")
			head := strings.TrimSpace(gitText(t, local.workPath, "rev-parse", "HEAD"))
			if override == "replace" {
				runGit(t, local.workPath, "replace", head, tip)
			} else {
				graftFile := filepath.Join(local.workPath, ".git", "info", "grafts")
				if override == "graft-env" {
					graftFile = filepath.Join(base, "custom-grafts")
					t.Setenv("GIT_GRAFT_FILE", graftFile)
				}
				writeFile(t, graftFile, head+" "+root+"\n")
			}
			if _, err := gitOutput(local.workPath, "merge-base", "HEAD", tip); err != nil {
				t.Fatal("fixture did not override ancestry")
			}
			replacement := remoteRepo(upstream)
			replacement.Name, replacement.FullName = "app", "acme/app"
			probe := &historyProbe{}
			defer probe.close()
			if probe.advertisedHistoryMatches(local.workPath, "", "https", replacement) {
				t.Fatal("local ancestry override authorized unrelated upstream")
			}
			match, err := probe.matches(local.workPath, "fake/acme", "", "https", replacement)
			if err != nil || match {
				t.Fatalf("isolated comparison = %v, %v", match, err)
			}
		})
	}
}

func TestAdvertisedHistoryIgnoresCheckoutURLRewrites(t *testing.T) {
	base := t.TempDir()
	local := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "local"))
	upstream := createTestRepo(t, base, "acme", "replacement", "main", filepath.Join(base, "upstream"))
	replacement := remoteRepo(upstream)
	replacement.Name, replacement.FullName, replacement.SSHURL = "app", "acme/app", "file://"+upstream.remotePath
	runGit(t, local.workPath, "remote", "set-url", "origin", replacement.SSHURL)
	runGit(t, local.workPath, "config", "url."+local.remotePath+".insteadOf", replacement.CloneURL)
	if origin, err := checkoutOrigin(local.workPath); err != nil || !originMatches(origin, replacement) {
		t.Fatalf("fixture origin did not match upstream: %q, %v", origin, err)
	}
	localRefs, err := gitOutputWithAuth(local.workPath, "", "ls-remote", "--heads", replacement.CloneURL)
	if err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(gitText(t, local.workPath, "rev-parse", "HEAD"))
	if !strings.Contains(localRefs, head) {
		t.Fatal("fixture did not redirect advertisement to unrelated archive")
	}
	probe := &historyProbe{}
	defer probe.close()
	if probe.advertisedHistoryMatches(local.workPath, "", "https", replacement) {
		t.Fatal("checkout URL rewrite substituted another repository")
	}
	match, err := probe.matches(local.workPath, "fake/acme", "", "https", replacement)
	if err != nil || match {
		t.Fatalf("isolated comparison = %v, %v", match, err)
	}
	archive := remoteRepo(local)
	archive.Name, archive.FullName, archive.Archived = "app-archive", "acme/app-archive", true
	m := newTestManager([]config.Target{repoTarget(local)}, fakeClientForRepos(local, upstream))
	m.Cache = &cache.Store{Dir: filepath.Join(base, "cache")}
	s := m.inspectIdentity(statusJob{path: local.workPath, provider: "fake", org: "acme", name: "app"},
		map[string]remote.Repository{"app": replacement, archive.Name: archive}, probe)
	if s.Error != "" || s.RepositoryID != archive.ID || s.ReplacementID != replacement.ID {
		t.Fatalf("redirected advertisement selected replacement identity: %+v", s)
	}
}
