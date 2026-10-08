package repo

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/config"
	"gitea.swiftstrike.ai/swiftstrike/tugboat/internal/remote"
)

func TestReceivingSyncAcceptsCanonicalOrganizationCasing(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			base := t.TempDir()
			r := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "seed"))
			client := fakeClient{repos: map[string]map[string]remote.Repository{
				"ACME": {"app": remoteRepo(r)}, "acme": {"app": remoteRepo(r)},
			}}
			target := config.Target{Name: "app", Provider: "fake", Org: "ACME", Path: filepath.Join(base, "work")}
			dir := filepath.Join(target.Path, "app")
			if explicit {
				target.Repo = "app"
				dir = target.Path
			}
			m := newTestManager([]config.Target{target}, client)
			output := captureStdout(t, func() {
				if err := m.Sync(nil, SyncOptions{Workers: 1}); err != nil {
					t.Fatal(err)
				}
			})
			if !isGitRepo(dir) || strings.Contains(output, "configured repository moved") {
				t.Fatalf("valid owner casing did not clone: %s", output)
			}
			assertProgressCounts(t, output, 1)
		})
	}
}

func TestReceivingSyncClonesMissingRepositories(t *testing.T) {
	for _, mode := range []SyncMode{SyncBoth, SyncPull} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			base := t.TempDir()
			app := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "seed-app"))
			excluded := createTestRepo(t, base, "acme", "scratch", "main", filepath.Join(base, "seed-scratch"))
			archived := createTestRepo(t, base, "acme", "old", "main", filepath.Join(base, "seed-old"))
			archived.archived = true
			target := config.Target{Name: "acme", Provider: "fake", Org: "acme", Path: filepath.Join(base, "new", "acme"), Exclude: []string{"scratch"}}
			writeFile(t, filepath.Join(target.Path, "scratch", "keep"), "unmanaged files")
			m := newTestManager([]config.Target{target}, fakeClientForRepos(app, excluded, archived))
			output := captureStdout(t, func() {
				if err := m.Sync(nil, SyncOptions{Mode: mode, Workers: 2}); err != nil {
					t.Fatal(err)
				}
			})
			dir := filepath.Join(target.Path, "app")
			if !isGitRepo(dir) || gitText(t, dir, "rev-parse", "HEAD") != gitText(t, app.workPath, "rev-parse", "HEAD") {
				t.Fatal("missing repository not cloned")
			}
			id, err := readIdentity(dir)
			if err != nil || id == nil || id.RepositoryID != remoteRepo(app).ID {
				t.Fatalf("clone identity: %+v %v", id, err)
			}
			if _, err := os.Lstat(filepath.Join(target.Path, "old")); !os.IsNotExist(err) {
				t.Fatal("unexpected archive checkout")
			}
			if data, err := os.ReadFile(filepath.Join(target.Path, "scratch", "keep")); err != nil || string(data) != "unmanaged files" {
				t.Fatal("excluded directory not retained")
			}
			if !strings.Contains(output, "cloned missing repository") || !strings.Contains(output, "excluded by pattern") {
				t.Fatal(output)
			}
			assertProgressCounts(t, output, 2)
		})
	}
}

func TestPullOnlyRetainsLocalCommitsWithoutPushing(t *testing.T) {
	for _, diverged := range []bool{false, true} {
		t.Run(fmt.Sprint(diverged), func(t *testing.T) {
			base := t.TempDir()
			r := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "app"))
			seed := cloneRepo(t, r.remotePath, filepath.Join(base, "seed"))
			commitFile(t, r.workPath, "local.txt", "local\n", "local commit")
			if diverged {
				commitFile(t, seed, "remote.txt", "remote\n", "remote commit")
				runGit(t, seed, "push", "origin", "main")
			}
			remoteHead := gitText(t, r.remotePath, "rev-parse", "main")
			m := newTestManager([]config.Target{repoTarget(r)}, fakeClientForRepos(r))
			output := captureStdout(t, func() {
				if err := m.Sync(nil, SyncOptions{Mode: SyncPull, Workers: 1}); err != nil {
					t.Fatal(err)
				}
			})
			if gitText(t, r.remotePath, "rev-parse", "main") != remoteHead {
				t.Fatal("pull-only pushed local commits")
			}
			if _, err := os.Stat(filepath.Join(r.workPath, "local.txt")); err != nil {
				t.Fatal("lost local commit")
			}
			if diverged && strings.TrimSpace(gitText(t, r.workPath, "rev-list", "--count", "origin/main..HEAD")) != "1" {
				t.Fatal("local commit was not rebased and retained")
			}
			assertProgressCounts(t, output, 1)
		})
	}
}

func TestPushOnlyLeavesWorktreesAndMissingRepositoriesAlone(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "work")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	r := createTestRepo(t, base, "acme", "app", "main", filepath.Join(root, "app"))
	missing := createTestRepo(t, base, "acme", "missing", "main", filepath.Join(base, "seed-missing"))
	runGit(t, r.workPath, "switch", "-c", "feature")
	runGit(t, r.workPath, "push", "-u", "origin", "feature")
	commitFile(t, r.workPath, "local.txt", "local\n", "feature work")
	writeFile(t, filepath.Join(r.workPath, "dirty.txt"), "unsaved\n")
	head := gitText(t, r.workPath, "rev-parse", "HEAD")
	dirty := gitText(t, r.workPath, "status", "--porcelain")
	m := newTestManager([]config.Target{{Name: "acme", Provider: "fake", Org: "acme", Path: root}}, fakeClientForRepos(r, missing))
	output := captureStdout(t, func() {
		if err := m.Sync(nil, SyncOptions{Mode: SyncPush, Workers: 1}); err != nil {
			t.Fatal(err)
		}
	})
	if gitText(t, r.remotePath, "rev-parse", "feature") != head {
		t.Fatal("feature commits not pushed")
	}
	if strings.TrimSpace(gitText(t, r.workPath, "branch", "--show-current")) != "feature" || gitText(t, r.workPath, "status", "--porcelain") != dirty {
		t.Fatal("push-only changed worktree")
	}
	if _, err := os.Lstat(filepath.Join(root, "missing")); !os.IsNotExist(err) {
		t.Fatal("push-only cloned a missing repository")
	}
	assertProgressCounts(t, output, 1)
}

func TestReceivingSyncClonesParentAndNestedFoldout(t *testing.T) {
	base := t.TempDir()
	parent := createTestRepo(t, base, "acme", "parent", "main", filepath.Join(base, "seed-parent"))
	child := createTestRepo(t, base, "other", "api", "main", filepath.Join(base, "seed-api"))
	commitFile(t, parent.workPath, ".tugboat.json", `{"repos":[{"name":"other/api","target":"services/api"}]}`, "add foldout")
	commitFile(t, parent.workPath, ".gitignore", "services/\n", "ignore foldout")
	runGit(t, parent.workPath, "push", "origin", "main")
	target := repoTarget(parent)
	target.Path = filepath.Join(base, "new", "parent")
	m := newTestManager([]config.Target{target}, fakeClientForRepos(parent, child))
	output := captureStdout(t, func() {
		if err := m.Sync(nil, SyncOptions{Workers: 2}); err != nil {
			t.Fatal(err)
		}
	})
	if !isGitRepo(target.Path) || !isGitRepo(filepath.Join(target.Path, "services", "api")) {
		t.Fatal("parent/foldout not cloned")
	}
	assertProgressCounts(t, output, 2)
	if !strings.Contains(output, "[1/1] [OK]") || !strings.Contains(output, "[2/2] [OK]") {
		t.Fatalf("new foldout did not extend total: %s", output)
	}
}

func TestMissingCheckoutFailureIsReportedWithoutOverwritingDirectory(t *testing.T) {
	base := t.TempDir()
	r := createTestRepo(t, base, "acme", "app", "main", filepath.Join(base, "seed-app"))
	target := repoTarget(r)
	target.Path = filepath.Join(base, "occupied")
	writeFile(t, filepath.Join(target.Path, "keep"), "local data")
	m := newTestManager([]config.Target{target}, fakeClientForRepos(r))
	var err error
	output := captureStdout(t, func() { err = m.Sync(nil, SyncOptions{Workers: 1}) })
	if err == nil || !strings.Contains(output, "clone destination is occupied") {
		t.Fatalf("%v: %s", err, output)
	}
	if data, err := os.ReadFile(filepath.Join(target.Path, "keep")); err != nil || string(data) != "local data" {
		t.Fatal("occupied directory overwritten")
	}
	assertProgressCounts(t, output, 1)
}

func TestCloneOnlyDoesNotUpdateExistingBranches(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "work")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	r := createTestRepo(t, base, "acme", "app", "main", filepath.Join(root, "app"))
	missing := createTestRepo(t, base, "acme", "missing", "main", filepath.Join(base, "seed-missing"))
	seed := cloneRepo(t, r.remotePath, filepath.Join(base, "seed"))
	commitFile(t, seed, "remote.txt", "remote\n", "remote commit")
	runGit(t, seed, "push", "origin", "main")
	before := gitText(t, r.workPath, "show-ref")
	m := newTestManager([]config.Target{{Name: "acme", Provider: "fake", Org: "acme", Path: root}}, fakeClientForRepos(r, missing))
	captureStdout(t, func() {
		if err := m.Sync(nil, SyncOptions{Mode: SyncCloneOnly, Workers: 1}); err != nil {
			t.Fatal(err)
		}
	})
	if gitText(t, r.workPath, "show-ref") != before {
		t.Fatal("clone-only updated existing refs")
	}
	if !isGitRepo(filepath.Join(root, "missing")) {
		t.Fatal("clone-only did not clone missing repo")
	}
}

func TestReceivingSyncFailsWhenEmptyOrganizationMetadataIsUnavailable(t *testing.T) {
	base := t.TempDir()
	good := createTestRepo(t, base, "good", "app", "main", filepath.Join(base, "app"))
	client := fakeClientForRepos(good)
	client.listErr = map[string]error{"bad": fmt.Errorf("provider unavailable")}
	m := newTestManager([]config.Target{repoTarget(good), {Name: "bad", Provider: "fake", Org: "bad", Path: filepath.Join(base, "bad")}}, client)
	var err error
	output := captureStdout(t, func() { err = m.Sync(nil, SyncOptions{Workers: 1}) })
	if err == nil || !strings.Contains(output, "1 failed") {
		t.Fatalf("%v: %s", err, output)
	}
	assertProgressCounts(t, output, 2)
}
